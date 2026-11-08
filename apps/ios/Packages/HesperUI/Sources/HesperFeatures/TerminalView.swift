import HesperCore
import HesperDesign
import HesperFleet
import HesperSurfaces
import HesperTerminal
import SwiftUI
import TerminalModel
import UIKit

/// The agent's live terminal, watched. On a Mac that names size owners the
/// phone owns the size while the view is open (fitted, `AttachPlan`): the
/// grid is what fits the area above the dock at 18 pt, unscaled, and
/// follows rotation and the dock (RESIZE, debounced). Otherwise, or once
/// the Mac took the size back, a mirror: the Mac's grid, scaled and panned
/// to the phone (`Viewport`). Bottom-anchored, so the grid's last row sits
/// on the dock.
///
/// The attach opens when the view appears and closes when it disappears.
/// In the background a mirror closes after 30 s; a fitted one at once,
/// inside a background task, so the Mac gets its size back. Both open
/// again on return. A link that breaks reads "Reconnecting…" and attaches
/// again; a Mac that refuses reads its reason.
struct TerminalView: View {
    let agent: Agent
    @Environment(FleetModel.self) private var model
    @Environment(\.scenePhase) private var scenePhase
    @State private var watch = TerminalWatch()
    /// The app was in the background for 30 s: the attach is closed.
    @State private var asleep = false

    /// How long the app may stay in the background with the attach open.
    private static let backgroundGrace: Duration = .seconds(30)
    /// How long the app asks to keep running after it let go, so the close
    /// reaches the Mac before iOS suspends it.
    private static let releaseTime: Duration = .seconds(2)

    /// Runs `release` inside a background task that lasts `releaseTime`.
    private static func inBackgroundTask(_ release: () -> Void) async {
        let app = UIApplication.shared
        let task = app.beginBackgroundTask(withName: "Let go of the terminal size", expirationHandler: nil)
        release()
        try? await Task.sleep(for: releaseTime)
        app.endBackgroundTask(task)
    }

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
            await watch.run(agent, model: model)
        }
        .onChange(of: agent.sizeOwner) { _, owner in watch.ownerChanged(owner) }
        .task(id: scenePhase) {
            guard scenePhase != .active else {
                asleep = false
                return
            }
            if watch.fitsPhone {
                // The Mac's terminal keeps the phone's size until the phone
                // lets go: close (X) now, while iOS still runs the app.
                await Self.inBackgroundTask { asleep = true }
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
            if !watch.fitsPhone, let size = agent.size {
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
    /// Whether the phone owns the Mac's terminal size and still holds it.
    private(set) var fit = FitState(plan: AttachPlan(mode: .mirror, grid: AttachPlan.fallbackSize), device: nil)
    var fitsPhone: Bool { fit.isFitted }

    /// The open attach, for RESIZE.
    @ObservationIgnored private var stream: (any AttachStream)?
    /// The Mac took the size back while this view was open: it stays a
    /// mirror until the agent screen opens again.
    @ObservationIgnored private var macTookBack = false
    /// A RESIZE waiting for the area to settle.
    @ObservationIgnored private var pendingFit: Task<Void, Never>?

    /// Placement changed: the canvas moves the surface.
    @ObservationIgnored var onPlace: (() -> Void)?
    /// When the owner last panned or zoomed; the view follows the output
    /// again `FollowRule.idle` later.
    @ObservationIgnored private var lastPan: Date?
    /// The momentum of the last flick, until it rests or a finger lands.
    @ObservationIgnored private var flinging: Task<Void, Never>?
    /// The surface has been fed: a new attach starts on a fresh one.
    @ObservationIgnored private var used = false
    /// The last written row of the screen, read after output settles.
    @ObservationIgnored private var lastTextRow: Int?
    /// A read of the last written row is due.
    @ObservationIgnored private var pendingRead: Task<Void, Never>?
    /// The attach's feeder, told when the surface reaches a grid.
    @ObservationIgnored private var feeder: TerminalFeeder?

    /// How long output settles before the last written row is read: at
    /// most one read of the whole screen per interval.
    static let readInterval: Duration = .milliseconds(100)

    /// Wait before attaching again after the link broke.
    static let backoff: Duration = .seconds(1)

    /// How long the area must hold before the Mac's terminal follows it,
    /// so a rotation or the keyboard redraws the agent once.
    static let fitDebounce: Duration = .milliseconds(300)

    init() {
        observeSurface()
    }

    /// Attaches and draws until the agent exits or the task is cancelled
    /// (the view disappeared, or the attach's key changed), attaching again
    /// whenever the link breaks.
    func run(_ agent: Agent, model: FleetModel) async {
        if used { replaceSurface() }
        phase = .loading
        while !Task.isCancelled {
            await waitForArea()
            let planned = model.plan(for: agent, area: viewport.view, cell: cell)
            let wanted = macTookBack ? AttachPlan.mirror(agent, typing: planned.typing) : planned
            let stream: any AttachStream
            let plan: AttachPlan
            do {
                let attached = try await model.attach(agent, plan: wanted)
                stream = attached.stream
                plan = attached.plan
            } catch {
                if !Task.isCancelled { phase = .unavailable(String(describing: error)) }
                return
            }
            used = true
            start(plan, device: model.device(on: agent.machine))
            self.stream = stream
            // Wheel input reaches the agent only on a read-write attach.
            if plan.typing {
                surface.onInput = { @Sendable data in stream.write(data) }
            } else {
                surface.onInput = nil
            }
            let exited = await withTaskCancellationHandler {
                await consume(stream)
            } onCancel: {
                stream.close()
            }
            surface.onInput = nil
            self.stream = nil
            pendingFit?.cancel()
            pendingFit = nil
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

    /// An attach began. Fitted: unscaled, the surface sized to the grid
    /// asked for. A mirror keeps its mode, or leaves the fitted one.
    private func start(_ plan: AttachPlan, device: String?) {
        stopFling()
        fit = FitState(plan: plan, device: device)
        if plan.owner {
            viewport.mode = .fitted
            surface.resize(cols: plan.grid.cols, rows: plan.grid.rows)
        } else if viewport.mode == .fitted {
            viewport.mode = Viewport.defaultMode(cols: plan.grid.cols)
        }
    }

    /// The agent's size owner changed (its info): the Mac may have taken
    /// the size back.
    func ownerChanged(_ owner: String?) {
        if fit.owner(owner) { dropToMirror(cols: viewport.cols) }
    }

    /// The Mac owns the size again: today's mirror, for as long as this
    /// view is open.
    private func dropToMirror(cols: Int) {
        macTookBack = true
        pendingFit?.cancel()
        pendingFit = nil
        viewport.mode = Viewport.defaultMode(cols: cols)
        lastPan = nil
        follow()
    }

    /// The cell a grid is sized by: the surface's, or until it has drawn
    /// the last one measured.
    private var cell: CGSize { surface.cell != .zero ? surface.cell : TerminalSurface.expectedCell }

    /// The container reports its size after the first layout; a fitted
    /// grid needs it (a second at most, then the plan falls back).
    private func waitForArea() async {
        for _ in 0..<50 where viewport.view.width <= 0 || viewport.view.height <= 0 {
            guard (try? await Task.sleep(for: .milliseconds(20))) != nil else { return }
        }
    }

    /// The area or the cell changed: once it has held for `fitDebounce`,
    /// the Mac's terminal is sized to what fits now (RESIZE).
    private func scheduleFit() {
        guard fit.isFitted else { return }
        pendingFit?.cancel()
        pendingFit = Task { [weak self] in
            guard (try? await Task.sleep(for: Self.fitDebounce)) != nil, let self else { return }
            self.pendingFit = nil
            guard let stream = self.stream, let grid = AttachPlan.fit(area: self.viewport.view, cell: self.cell),
                let ask = self.fit.resize(to: grid)
            else { return }
            stream.resize(cols: ask.cols, rows: ask.rows)
        }
    }

    /// True when the agent exited.
    private func consume(_ stream: any AttachStream) async -> Bool {
        let feeder = TerminalFeeder { [surface] data in surface.feed(data) }
        self.feeder = feeder
        defer { self.feeder = nil }
        for await frame in stream.frames {
            switch frame {
            case .size(let cols, let rows):
                if fit.size(GridSize(cols: cols, rows: rows)) { dropToMirror(cols: cols) }
                // The history replay follows right after: it waits until
                // the surface lays out the Mac's grid, or it is garbled.
                let current = TerminalFeeder.Grid(cols: surface.grid.cols, rows: surface.grid.rows)
                await feeder.expect(grid: .init(cols: cols, rows: rows), current: current)
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
        if first, !fit.isFitted { viewport.mode = Viewport.defaultMode(cols: cols) }
        follow()
    }

    // MARK: Gestures (the canvas's container)

    func setView(_ size: CGSize) {
        guard viewport.view != size else { return }
        viewport.view = size
        follow()
        scheduleFit()
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
        guard viewport.mode != .fitted else { return }
        viewport.mode = viewport.mode == .readable ? .fitWidth : .readable
        lastPan = nil
        follow()
    }

    /// A drag by `delta` points. Sideways pans; up and down pan the grid
    /// first and past its edge go to the terminal as wheel input: the
    /// phone's scrollback, or the agent's own (Claude Code draws on the
    /// alternate screen, which has none here). A finger stops a fling.
    func drag(by delta: CGPoint, fromFinger: Bool = true) {
        if fromFinger { stopFling() }
        lastPan = .now
        let range = viewport.panRange
        var target = CGPoint(x: pan.x - delta.x, y: pan.y)
        var rest = delta.y
        if rest > 0 {
            // Down: towards the top of the grid, then back in time.
            let moved = max(0, min(rest, target.y - range.y.lowerBound))
            target.y -= moved
            rest -= moved
        } else if rest < 0 {
            // Up: towards the grid's bottom, then forward in time.
            let moved = min(0, max(rest, target.y - range.y.upperBound))
            target.y -= moved
            rest -= moved
        }
        setPan(target)
        guard viewport.scale > 0 else { return }
        surface.wheel(points: rest / viewport.scale)
    }

    /// The finger lifted at `velocity` points per second (positive moving
    /// down): the drag goes on and slows down (`Fling`).
    func fling(velocity: CGFloat) {
        stopFling()
        let start = Fling(velocity: velocity)
        flinging = Task { [weak self] in
            var fling = start
            let tick: Duration = .milliseconds(16)
            while let d = fling.step(tick) {
                guard (try? await Task.sleep(for: tick)) != nil, let self else { return }
                self.drag(by: CGPoint(x: 0, y: d), fromFinger: false)
            }
        }
    }

    /// Ends the momentum of a flick.
    func stopFling() {
        flinging?.cancel()
        flinging = nil
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
            if let feeder = self.feeder {
                let grid = TerminalFeeder.Grid(cols: self.surface.grid.cols, rows: self.surface.grid.rows)
                Task { await feeder.gridChanged(to: grid) }
            }
            if self.viewport.cell != self.surface.cell, self.surface.cell != .zero {
                self.viewport.cell = self.surface.cell
                self.follow()
                self.scheduleFit()
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
        switch g.state {
        case .began:
            watch.stopFling()
        case .changed:
            watch.drag(by: g.translation(in: self))
            g.setTranslation(.zero, in: self)
        case .ended, .cancelled:
            watch.fling(velocity: g.velocity(in: self).y)
        default:
            break
        }
    }
}
