import HesperCore
import HesperDesign
import HesperFleet
import HesperSurfaces
import HesperTerminal
import SwiftUI
import TerminalModel
import UIKit

/// The agent's live terminal, watched: a read-only attach drawn at the
/// Mac's grid size, scaled and panned to the phone (`Viewport`).
/// Bottom-anchored, so the grid's last row sits on the dock.
///
/// The attach opens when the view appears and closes when it disappears,
/// and after 30 s in the background (it opens again on return). A link
/// that breaks reads "Reconnecting…" and attaches again; a Mac that
/// refuses reads its reason.
struct TerminalView: View {
    let agent: Agent
    @Environment(FleetModel.self) private var model
    @Environment(\.scenePhase) private var scenePhase
    @State private var watch = TerminalWatch()
    /// The app was in the background for 30 s: the attach is closed.
    @State private var asleep = false

    /// How long the app may stay in the background with the attach open.
    private static let backgroundGrace: Duration = .seconds(30)

    var body: some View {
        ZStack {
            if case .unavailable(let reason) = watch.phase {
                unavailable(reason)
            } else {
                TerminalCanvas(watch: watch)
                    .overlay(alignment: .bottomLeading) { status }
                    .overlay(alignment: .bottomTrailing) { livePill }
                    .overlay(alignment: .topTrailing) { columnsHint }
            }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background(Theme.background)
        .environment(\.colorScheme, .dark)
        .task(id: AttachKey(agent: agent.id, reachable: model.fleet.isReachable(agent), asleep: asleep)) {
            guard !asleep else { return }
            await watch.run(agent) { try await model.attach($0) }
        }
        .task(id: scenePhase) {
            guard scenePhase != .active else {
                asleep = false
                return
            }
            guard (try? await Task.sleep(for: Self.backgroundGrace)) != nil else { return }
            asleep = true
        }
    }

    /// The grey line under the grid while it is not drawn yet.
    @ViewBuilder private var status: some View {
        switch watch.phase {
        case .loading:
            if let size = agent.size {
                caption("Mac's size · \(size.cols) × \(size.rows)")
            }
        case .reconnecting:
            caption("Reconnecting…")
        case .live, .exited, .unavailable:
            EmptyView()
        }
    }

    /// Back to the newest line, while scrolled up into the history.
    @ViewBuilder private var livePill: some View {
        if watch.phase == .live, !watch.isAtBottom {
            Button("Live ↓") { watch.goLive() }
                .secondaryAction()
                .padding(DS.Spacing.l)
        }
    }

    /// How wide the Mac's grid is, while readable mode cuts it off.
    @ViewBuilder private var columnsHint: some View {
        if watch.viewport.mode == .readable, watch.viewport.panRange.x.upperBound > 0 {
            Text("\(watch.viewport.cols) cols →")
                .font(Typeface.mono(11, relativeTo: .caption2))
                .foregroundStyle(Theme.muted)
                .padding(.horizontal, DS.Spacing.s)
                .padding(.vertical, DS.Spacing.xs)
                .background(Theme.background.opacity(0.8))
                .padding(DS.Spacing.s)
                .allowsHitTesting(false)
        }
    }

    /// Today's placeholder, with the Mac's reason in one line.
    private func unavailable(_ reason: String) -> some View {
        VStack(alignment: .leading, spacing: DS.Spacing.s) {
            Spacer()
            Text(AgentCopy.headline(agent))
                .font(.system(.footnote, design: .monospaced))
                .foregroundStyle(Theme.text)
            Text("The live terminal is not available: \(reason)")
                .font(.caption2.monospaced())
                .foregroundStyle(Theme.dim)
                .lineLimit(1)
        }
        .padding(DS.Spacing.l)
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .bottomLeading)
    }

    private func caption(_ text: String) -> some View {
        Text(text)
            .font(.caption2.monospaced())
            .foregroundStyle(Theme.dim)
            .padding(DS.Spacing.l)
            .allowsHitTesting(false)
    }
}

/// What the attach depends on: a change closes it and opens a fresh one.
private struct AttachKey: Equatable {
    let agent: String
    let reachable: Bool
    let asleep: Bool
}

/// One watched terminal: the attach loop, the surface it feeds, and how
/// the grid sits in the view (scale and pan).
@Observable
final class TerminalWatch {
    enum Phase: Equatable {
        case loading
        case live
        case reconnecting
        case unavailable(String)
        /// The agent exited; the last screen stays, the dock offers Resume.
        case exited
    }

    private(set) var phase: Phase = .loading
    private(set) var surface = TerminalWatch.makeSurface()
    private(set) var viewport = Viewport(grid: (0, 0), view: .zero, cell: .zero, mode: .fitWidth)
    /// How far the grid's top-left corner is moved left and up, within
    /// `viewport.panRange`.
    private(set) var pan: CGPoint = .zero
    /// The newest line is on screen (not scrolled up into the history).
    private(set) var isAtBottom = true

    /// Placement changed: the canvas moves the surface.
    @ObservationIgnored var onPlace: (() -> Void)?
    /// When the owner last panned or zoomed; the view follows the output
    /// again `FollowRule.idle` later.
    @ObservationIgnored private var lastPan: Date?
    /// A vertical drag past the grid's edge, not yet a whole row of history.
    @ObservationIgnored private var scrollRemainder: CGFloat = 0
    /// The surface has been fed: a new attach starts on a fresh one.
    @ObservationIgnored private var used = false
    /// The last written row of the screen, read after output settles.
    @ObservationIgnored private var lastTextRow: Int?
    /// A read of the last written row is due.
    @ObservationIgnored private var pendingRead: Task<Void, Never>?

    /// How long output settles before the last written row is read: at
    /// most one read of the whole screen per interval.
    static let readInterval: Duration = .milliseconds(100)

    /// Wait before attaching again after the link broke.
    static let backoff: Duration = .seconds(1)

    init() {
        observeSurface()
    }

    /// Attaches and draws until the agent exits or the task is cancelled
    /// (the view disappeared, or the attach's key changed), attaching again
    /// whenever the link breaks.
    func run(_ agent: Agent, attach: (Agent) async throws -> any AttachStream) async {
        if used { replaceSurface() }
        phase = .loading
        while !Task.isCancelled {
            let stream: any AttachStream
            do {
                stream = try await attach(agent)
            } catch {
                if !Task.isCancelled { phase = .unavailable(String(describing: error)) }
                return
            }
            used = true
            let exited = await withTaskCancellationHandler {
                await consume(stream)
            } onCancel: {
                stream.close()
            }
            stream.close()
            if exited {
                phase = .exited
                return
            }
            guard !Task.isCancelled else { return }
            // The frames ended without `.exit`: the link broke.
            phase = .reconnecting
            replaceSurface()
            guard (try? await Task.sleep(for: Self.backoff)) != nil else { return }
        }
    }

    /// True when the agent exited.
    private func consume(_ stream: any AttachStream) async -> Bool {
        let feeder = TerminalFeeder { [surface] data in surface.feed(data) }
        for await frame in stream.frames {
            switch frame {
            case .size(let cols, let rows):
                showGrid(cols: cols, rows: rows)
            case .data(let data):
                await feeder.push(data)
                phase = .live
                scheduleRead()
            case .exit:
                await feeder.flush()
                return true
            case .scroll:
                break
            }
        }
        await feeder.flush()
        return false
    }

    /// The Mac's grid, from the attach's reply or a resize on the Mac. The
    /// first one of a watch picks the mode; later ones keep it.
    private func showGrid(cols: Int, rows: Int) {
        let first = viewport.cols == 0
        surface.resize(cols: cols, rows: rows)
        viewport.setGrid(cols: cols, rows: rows)
        if first { viewport.mode = Viewport.defaultMode(cols: cols) }
        follow()
    }

    // MARK: Gestures (the canvas's container)

    func setView(_ size: CGSize) {
        guard viewport.view != size else { return }
        viewport.view = size
        follow()
    }

    /// A pinch: zooms within the viewport's clamps, keeping the point under
    /// the fingers in place.
    func zoom(to scale: CGFloat, around point: CGPoint) {
        let old = viewport.scale
        viewport.zoom(to: scale)
        let new = viewport.scale
        lastPan = .now
        guard old > 0, new != old else { return }
        let k = new / old
        setPan(CGPoint(x: (point.x + pan.x) * k - point.x, y: (point.y + pan.y) * k - point.y))
    }

    /// A double tap: readable and fit width, in turn.
    func toggleMode() {
        viewport.mode = viewport.mode == .readable ? .fitWidth : .readable
        lastPan = nil
        follow()
    }

    /// A drag by `delta` points. Sideways pans; up and down pan the grid
    /// first and past its edge scroll the history, a row per cell height.
    func drag(by delta: CGPoint) {
        lastPan = .now
        let range = viewport.panRange
        var target = CGPoint(x: pan.x - delta.x, y: pan.y)
        var rest = delta.y
        if rest > 0 {
            // Down: towards the top of the grid, then back in time.
            let moved = min(rest, target.y - range.y.lowerBound)
            target.y -= moved
            rest -= moved
        } else if rest < 0, isAtBottom {
            // Up at the newest line: towards the grid's bottom only.
            target.y -= rest
            rest = 0
        }
        setPan(target)
        scrollRemainder -= rest
        let row = viewport.cell.height * viewport.scale
        guard row > 0 else { return }
        let rows = Int(scrollRemainder / row)
        guard rows != 0 else { return }
        scrollRemainder -= CGFloat(rows) * row
        surface.scroll(rows: rows)
    }

    /// The "Live ↓" pill: the newest line, and the view follows again.
    func goLive() {
        lastPan = nil
        surface.scrollToBottom()
        follow()
    }

    // MARK: Placement

    /// New output: once it has settled (`readInterval`), the last written
    /// row is read and followed. One read per interval, however many
    /// frames arrive in it.
    private func scheduleRead() {
        guard pendingRead == nil else { return }
        pendingRead = Task { [weak self] in
            try? await Task.sleep(for: Self.readInterval)
            guard let self, !Task.isCancelled else { return }
            self.pendingRead = nil
            self.followIfIdle()
        }
    }

    /// Back to the newest line and the last written row at the left,
    /// unless the owner moved the view within `FollowRule.idle`. The
    /// surface reports no cursor; the last row with text stands in for
    /// it, and on a full screen that is the grid's bottom.
    private func followIfIdle() {
        guard FollowRule().shouldFollow(lastPan: lastPan, now: .now) else { return }
        if !isAtBottom { surface.scrollToBottom() }
        lastTextRow = surface.lastTextRow()
        follow()
    }

    private func follow() {
        scrollRemainder = 0
        setPan(CGPoint(x: 0, y: viewport.followOffset(lastTextRow: lastTextRow)))
    }

    private func setPan(_ wanted: CGPoint) {
        let range = viewport.panRange
        pan = CGPoint(
            x: min(range.x.upperBound, max(range.x.lowerBound, wanted.x)),
            y: min(range.y.upperBound, max(range.y.lowerBound, wanted.y))
        )
        onPlace?()
    }

    /// A broken link's surface holds a screen the next attach redraws, and
    /// its history again: a fresh one starts clean.
    private func replaceSurface() {
        surface.onChange = nil
        surface = Self.makeSurface()
        pendingRead?.cancel()
        pendingRead = nil
        lastTextRow = nil
        observeSurface()
        if viewport.cols > 0 { surface.resize(cols: viewport.cols, rows: viewport.rows) }
        isAtBottom = true
        onPlace?()
    }

    private static func makeSurface() -> TerminalSurface {
        TerminalSurface(background: Theme.uiColor(.background))
    }

    private func observeSurface() {
        surface.onChange = { [weak self] in
            guard let self else { return }
            if self.viewport.cell != self.surface.cell, self.surface.cell != .zero {
                self.viewport.cell = self.surface.cell
                self.follow()
            }
            self.isAtBottom = self.surface.isAtBottom
            self.onPlace?()
        }
    }
}

/// The surface in a clipping container that takes the gestures; the
/// surface itself takes no touches (its own pinch would change the grid).
private struct TerminalCanvas: UIViewRepresentable {
    let watch: TerminalWatch

    func makeUIView(context: Context) -> TerminalContainer {
        TerminalContainer(watch: watch)
    }

    func updateUIView(_ view: TerminalContainer, context: Context) {
        view.place()
    }
}

/// Places the surface by `layer.position` (the pan, bottom-anchored) and
/// `transform` (the scale), never by `frame`: the surface's bounds are the
/// Mac's grid, which `TerminalSurface.resize` owns.
final class TerminalContainer: UIView {
    private let watch: TerminalWatch
    private var pinchStart: CGFloat = 1

    init(watch: TerminalWatch) {
        self.watch = watch
        super.init(frame: .zero)
        clipsToBounds = true
        backgroundColor = Theme.uiColor(.background)

        let pinch = UIPinchGestureRecognizer(target: self, action: #selector(pinched(_:)))
        let doubleTap = UITapGestureRecognizer(target: self, action: #selector(doubleTapped))
        doubleTap.numberOfTapsRequired = 2
        let drag = UIPanGestureRecognizer(target: self, action: #selector(dragged(_:)))
        drag.maximumNumberOfTouches = 1
        for g in [pinch, doubleTap, drag] { addGestureRecognizer(g) }

        watch.onPlace = { [weak self] in self?.place() }
    }

    @available(*, unavailable)
    required init?(coder: NSCoder) { fatalError("not used from a storyboard") }

    override func layoutSubviews() {
        super.layoutSubviews()
        watch.setView(bounds.size)
        place()
    }

    func place() {
        let surface = watch.surface.view
        if surface.superview !== self {
            for old in subviews { old.removeFromSuperview() }
            addSubview(surface)
        }
        let viewport = watch.viewport
        guard viewport.cell != .zero, viewport.cols > 0 else {
            // Not measured yet: unscaled at the corner, so libghostty can
            // lay out and report its cell.
            surface.transform = .identity
            surface.layer.position = .zero
            return
        }
        let scale = viewport.scale
        let drop = max(0, bounds.height - viewport.contentSize.height)
        surface.transform = CGAffineTransform(scaleX: scale, y: scale)
        surface.layer.position = CGPoint(x: -watch.pan.x, y: drop - watch.pan.y)
    }

    @objc private func pinched(_ g: UIPinchGestureRecognizer) {
        switch g.state {
        case .began:
            pinchStart = watch.viewport.scale
        case .changed:
            watch.zoom(to: pinchStart * g.scale, around: g.location(in: self))
        default:
            break
        }
    }

    @objc private func doubleTapped() {
        watch.toggleMode()
    }

    @objc private func dragged(_ g: UIPanGestureRecognizer) {
        guard g.state == .changed else { return }
        watch.drag(by: g.translation(in: self))
        g.setTranslation(.zero, in: self)
    }
}
