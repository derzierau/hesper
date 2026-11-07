import Foundation

/// Coalesces a watched terminal's output before it reaches the surface.
/// A burst of small data frames (hundreds of KB in a second) would
/// otherwise be one handover each; here bytes collect until there are
/// `chunk` of them or `window` has passed since the first, then go to the
/// sink in one piece. Chunks reach the sink one at a time and in order.
///
/// It also holds output while the surface is at the wrong grid. A resize
/// takes the surface a layout pass or two; bytes it drew in between would
/// be laid out at the wrong width (cursor moves clamped, lines wrapped
/// short) and stay garbled. After `expect(grid:current:)` nothing reaches
/// the sink until `gridChanged(to:)` reports that grid, or `gateTimeout`
/// has passed.
public actor TerminalFeeder {
    /// A terminal grid, in cells.
    public struct Grid: Equatable, Sendable {
        public var cols: Int
        public var rows: Int
        public init(cols: Int, rows: Int) {
            self.cols = cols
            self.rows = rows
        }
    }

    /// Hand over as soon as this many bytes are waiting.
    public static let chunk = 64 * 1024
    /// Hand over at most this long after the first waiting byte.
    public static let window: Duration = .milliseconds(16)

    private let sink: @Sendable (Data) async -> Void
    private let gateTimeout: Duration
    private var pending = Data()
    private var timer: Task<Void, Never>?
    /// A handover is running; new bytes wait for its loop.
    private var draining = false
    /// The grid the surface must reach before bytes go on; nil when open.
    private var expected: Grid?
    /// Counts `expect` calls, so an old gate's timeout opens no newer one.
    private var generation = 0
    /// `flush` calls waiting for the gate to open.
    private var opened: [CheckedContinuation<Void, Never>] = []

    /// `gateTimeout` opens a gate whose grid never comes: libghostty may
    /// not reach a grid, and an off grid beats a blank terminal.
    public init(gateTimeout: Duration = .seconds(1), sink: @escaping @Sendable (Data) async -> Void) {
        self.gateTimeout = gateTimeout
        self.sink = sink
    }

    public func push(_ data: Data) async {
        guard !data.isEmpty else { return }
        pending.append(data)
        guard expected == nil else { return }
        if pending.count >= Self.chunk {
            await drain()
        } else if timer == nil, !draining {
            timer = Task { [weak self] in
                try? await Task.sleep(for: Self.window)
                guard !Task.isCancelled else { return }
                await self?.windowEnded()
            }
        }
    }

    /// Hands over everything waiting, for the end of a stream. A closed
    /// gate is waited for first (its grid or its timeout): its bytes are
    /// for the coming grid.
    public func flush() async {
        if expected != nil {
            await withCheckedContinuation { opened.append($0) }
        }
        await drain()
    }

    /// The surface is being resized to `grid` and is at `current` now.
    /// Bytes already waiting were written for the old grid and go first;
    /// later ones wait until the surface reports `grid`.
    public func expect(grid: Grid, current: Grid) async {
        await drain()
        generation += 1
        guard grid != current else {
            expected = nil
            return
        }
        expected = grid
        let gate = generation
        // Strong: a `flush` may be waiting on this timeout.
        Task { [gateTimeout] in
            try? await Task.sleep(for: gateTimeout)
            await self.timedOut(gate)
        }
    }

    /// The surface laid out `grid`.
    public func gridChanged(to grid: Grid) async {
        guard let expected, grid == expected else { return }
        await open()
    }

    private func timedOut(_ gate: Int) async {
        guard gate == generation, expected != nil else { return }
        await open()
    }

    private func open() async {
        expected = nil
        await drain()
        let waiting = opened
        opened = []
        for flush in waiting { flush.resume() }
    }

    private func windowEnded() async {
        timer = nil
        await drain()
    }

    private func drain() async {
        guard !draining, expected == nil else { return }
        draining = true
        timer?.cancel()
        timer = nil
        // Bytes pushed while the sink runs are picked up by the next turn,
        // so order holds and a burst still leaves in large pieces. A gate
        // that closes meanwhile stops the loop.
        while !pending.isEmpty, expected == nil {
            let next = pending
            pending = Data()
            await sink(next)
        }
        draining = false
    }
}
