import Foundation

/// Coalesces a watched terminal's output before it reaches the surface.
/// A burst of small data frames (hundreds of KB in a second) would
/// otherwise be one handover each; here bytes collect until there are
/// `chunk` of them or `window` has passed since the first, then go to the
/// sink in one piece. Chunks reach the sink one at a time and in order.
public actor TerminalFeeder {
    /// Hand over as soon as this many bytes are waiting.
    public static let chunk = 64 * 1024
    /// Hand over at most this long after the first waiting byte.
    public static let window: Duration = .milliseconds(16)

    private let sink: @Sendable (Data) async -> Void
    private var pending = Data()
    private var timer: Task<Void, Never>?
    /// A handover is running; new bytes wait for its loop.
    private var draining = false

    public init(sink: @escaping @Sendable (Data) async -> Void) {
        self.sink = sink
    }

    public func push(_ data: Data) async {
        guard !data.isEmpty else { return }
        pending.append(data)
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

    /// Hands over everything waiting now, for the end of a stream.
    public func flush() async {
        await drain()
    }

    private func windowEnded() async {
        timer = nil
        await drain()
    }

    private func drain() async {
        guard !draining else { return }
        draining = true
        timer?.cancel()
        timer = nil
        // Bytes pushed while the sink runs are picked up by the next turn,
        // so order holds and a burst still leaves in large pieces.
        while !pending.isEmpty {
            let next = pending
            pending = Data()
            await sink(next)
        }
        draining = false
    }
}
