import AppKit
import HesperCore
import os

/// Cell sizes per font, as the engine reports them (every surface uses the
/// same font family, so a font size always gives the same cell).
@MainActor
enum CellMetrics {
    static var table = FontFit.CellTable(scale: 2)

    static func cell(_ font: Double, scale: Double) -> CellSize {
        if table.scale != scale { table = FontFit.CellTable(scale: scale, ratio: table.ratio) }
        return table.cell(font)
    }

    /// Records a surface's real cell size; true when it is news (the wall
    /// then lays out again with the exact size).
    static func record(font: Double, widthPx: Int, heightPx: Int, scale: Double) -> Bool {
        if table.scale != scale { table = FontFit.CellTable(scale: scale, ratio: table.ratio) }
        return table.record(font: font, widthPx: widthPx, heightPx: heightPx)
    }
}

/// The engine factory: the one place that names the concrete engine.
@MainActor
enum TerminalEngine {
    static func makeSurface() -> any TerminalSurface { GhosttySurfaceView(frame: .zero) }
}

/// One agent's terminal in a view: a surface running
/// `hesperd attach <id> --fit` (a view: the agent's last rows that fit, at
/// the font the wall picked, rendered by the daemon at this grid) or
/// `hesperd attach <id> --owner` (the size owner: the view's size becomes
/// the PTY's). Never a read-write attach without owner: its raw stream is
/// at the PTY's grid, which libghostty (sizing its grid from the frame)
/// would wrap and garble (SizeOwner). Reattaches when the attach process
/// ends while the agent is still alive.
@MainActor
final class AgentTerminal: NSObject, TerminalSurfaceDelegate {
    enum Role { case tile, focus }
    let role: Role
    let env: AppEnvironment
    private(set) var agent: Agent
    private(set) var surface: (any TerminalSurface)?
    let host = NSView()
    var userFontSize: Double = 13
    var visible = true { didSet { surface?.isRenderingVisible = visible; prewarm?.isRenderingVisible = visible } }
    /// Tiles: the font the wall laid this tile out with. libghostty must not
    /// change a live surface's font (see "As built"), so a new font means a
    /// re-attach (debounced).
    var tileFont: Double = 10 {
        didSet { if role == .tile && oldValue != tileFont { scheduleRefit() } }
    }
    /// A shelf card shows no terminal: its surface (and attach) is closed
    /// until it leaves the shelf.
    var parked = false {
        didSet {
            guard oldValue != parked else { return }
            if parked { close() } else { layout() }
        }
    }
    /// Window layer integration (docs: "As built — windows"): a read-write
    /// terminal (focus role, or the active tile) attaches as the PTY's size
    /// owner only while its window is the app's key window (SizeOwnership
    /// decides, debounced; one owner per agent); otherwise it is a view
    /// (`--fit`) like a tile, and the daemon falls back to the views' fit.
    /// Changing it re-attaches seamlessly (swapMode; the daemon has no
    /// "release owner" frame).
    var ownsSize = true {
        didSet { if oldValue != ownsSize && !resolvingOwnership && readWrite && surface != nil { swapMode() } }
    }
    /// Whether this terminal would type: the focus role or the active tile.
    var readWrite: Bool { role == .focus || interactive }
    /// The window layer's answer to "does this terminal own its agent's
    /// size now?" (SizeOwnership), asked whenever a surface is made, so a
    /// new terminal in a window that is not key never attaches as a second
    /// owner (nil: keep `ownsSize`).
    static var ownershipResolver: ((AgentTerminal) -> Bool)?
    private var resolvingOwnership = false
    /// The active wall tile: typing goes to the agent. Its surface is a
    /// read-write attach (size owner at the tile's own grid, which is the
    /// grid the tile's fit asked for, so nothing resizes) in the
    /// low-latency engine app; every other tile stays a cheap read-only
    /// view. The switch is seamless: the new surface starts underneath the
    /// shown one, and replaces it once it has drawn the same screen.
    var interactive = false {
        didSet { if oldValue != interactive && role == .tile { swapMode() } }
    }
    /// A surface prewarming underneath the shown one (the mode switch).
    private(set) var prewarm: (any TerminalSurface)?
    private var swapGeneration = 0
    /// Called when a surface reported a cell size the wall did not know yet.
    var onCellMeasured: (() -> Void)?
    private var reattachTimes: [Date] = []
    private var pendingReattach = false
    static let log = Logger(subsystem: "de.olezierau.hesper", category: "terminal")

    init(agent: Agent, role: Role, env: AppEnvironment) {
        self.agent = agent
        self.role = role
        self.env = env
        super.init()
        host.wantsLayer = true
        // The terminal's own background (libghostty keeps its own theme, not
        // the chrome tokens): sub-cell remainders are invisible.
        host.layer?.backgroundColor = DS.terminalBackground.cgColor
        host.postsFrameChangedNotifications = true
        NotificationCenter.default.addObserver(forName: NSView.frameDidChangeNotification, object: host, queue: .main) { [weak self] _ in
            MainActor.assumeIsolated { self?.layout() }
        }
    }

    /// Live attach only while the process runs; an ended agent keeps the
    /// last screen its tile already shows.
    var isAttachable: Bool { agent.isRunning && !parked }

    func update(_ a: Agent, connected: Bool) {
        let old = agent
        agent = a
        let resumed = !old.isRunning && a.isRunning
        if resumed || (old.pid != a.pid && a.pid != nil && old.pid != nil) {
            recreate()
        } else if surface == nil || (surface?.processExited ?? false) {
            if connected && isAttachable { scheduleReattach() }
        }
        // A changed PTY size needs nothing here: a tile's view follows the
        // screen, and the wall re-lays out (maybe with another font).
    }

    /// Called after the daemon connection comes back.
    func daemonReconnected() {
        if surface == nil || surface!.processExited { recreate() }
    }

    func layout() {
        guard let surface else {
            if isAttachable && host.bounds.width > 4 && host.bounds.height > 4 { createSurface() }
            return
        }
        // Anchored top-left at the host's size: for tiles that is exactly
        // the rows shown, so the engine's grid fills it.
        surface.view.frame = host.bounds
        prewarm?.view.frame = host.bounds
    }

    func recreate() {
        surface?.close()
        surface = nil
        if isAttachable && host.bounds.width > 4 { createSurface() }
    }

    func close() {
        prewarm?.close()
        prewarm = nil
        surface?.close()
        surface = nil
    }

    /// Whether the shown surface is a read-write attach (tests).
    private(set) var surfaceIsInteractive = false
    /// How the shown surface attached (tests, the window layer).
    private(set) var surfaceAttach: SizeOwner.Attach = .view

    private func swapMode() {
        swapGeneration += 1
        let gen = swapGeneration
        prewarm?.close()
        prewarm = nil
        guard surface != nil, isAttachable, host.window != nil, host.bounds.width > 4 else {
            if surface == nil { layout() }
            return
        }
        let s = makeSurface(interactive: interactive, below: surface?.view)
        prewarm = s
        // Swap once the new surface shows content (its attach redraw), at
        // most 800 ms later.
        let start = Date()
        func poll() {
            DispatchQueue.main.asyncAfter(deadline: .now() + 0.03) { [weak self] in
                guard let self, gen == self.swapGeneration, let p = self.prewarm, p === s else { return }
                let drawn = (p.readScreen()?.contains { !$0.isWhitespace } ?? false) && p.metrics != nil
                if drawn || Date().timeIntervalSince(start) > 0.8 { self.finishSwap() } else { poll() }
            }
        }
        poll()
    }

    private func finishSwap() {
        guard let p = prewarm else { return }
        scrollInfo = ScrollInfo(offset: 0, max: -1, new: 0)
        onScrollChanged?()
        let old = surface
        // Window layer: a focus-role swap (ownership) keeps typing focus.
        let hadFocus = old.map { host.window?.firstResponder === $0.view } ?? false
        prewarm = nil
        surface = p
        surfaceAttach = prewarmAttach
        surfaceIsInteractive = interactive && prewarmAttach == .owner
        p.acceptsInput = prewarmAttach == .owner
        old?.close()
        // A focus view that just became the owner (its window became key)
        // takes typing too: as a view it could not be first responder.
        let takeFocus = surfaceIsInteractive || (role == .focus && p.acceptsInput && (hadFocus || host.window?.isKeyWindow == true))
        if takeFocus, let w = host.window { w.makeFirstResponder(p.view) }
        surfaceMetricsDidChange(p)
    }

    /// Focus the active tile's terminal (typing).
    func focusInput() {
        guard interactive, let s = surface, surfaceIsInteractive else { return }
        host.window?.makeFirstResponder(s.view)
    }

    var acceptsInput: Bool {
        get { surface?.acceptsInput ?? false }
        set { surface?.acceptsInput = newValue }
    }

    private func createSurface() {
        guard host.window != nil else { return }
        prewarm?.close()
        prewarm = nil
        scrollInfo = ScrollInfo(offset: 0, max: -1, new: 0)
        let s = makeSurface(interactive: role == .tile && interactive, below: nil, assign: true)
        surfaceAttach = prewarmAttach
        surfaceIsInteractive = role == .tile && interactive && prewarmAttach == .owner
        if surfaceIsInteractive { host.window?.makeFirstResponder(s.view) }
        _ = s
    }

    /// How the last surface made attached.
    private var prewarmAttach: SizeOwner.Attach = .view

    /// A surface for this agent: a read-write owner attach (the focus role
    /// or the active tile, in the owner window), else a read-only fit view.
    @discardableResult
    private func makeSurface(interactive: Bool, below: NSView?, assign: Bool = false) -> any TerminalSurface {
        let s = TerminalEngine.makeSurface()
        s.delegate = self
        s.view.frame = host.bounds
        s.view.autoresizingMask = []
        // Drops on the terminal go to the tile / focus view (TerminalDrop).
        s.view.registerForDraggedTypes(TerminalDrop.types)
        if let below { host.addSubview(s.view, positioned: .below, relativeTo: below) } else { host.addSubview(s.view) }
        if let resolve = Self.ownershipResolver {
            resolvingOwnership = true
            ownsSize = resolve(self)
            resolvingOwnership = false
        }
        let mode = SizeOwner.attach(readWrite: role == .focus || interactive, owns: ownsSize)
        prewarmAttach = mode
        let rw = mode == .owner
        s.acceptsInput = rw && (role == .focus || assign)
        s.prefersLowLatency = rw
        s.isRenderingVisible = visible
        let font = role == .tile ? tileFont : userFontSize
        let argv = env.attachArgv(id: agent.id, mode)
        // Assign first: libghostty reports the initial cell size from inside
        // surface creation, and the delegate must see this surface.
        if assign { surface = s }
        s.start(TerminalCommand(argv: argv, env: env.attachEnvironment), fontSize: font)
        if !assign { return s }
        DispatchQueue.main.async { [weak self, weak s] in
            guard let self, let s, s === self.surface else { return }
            self.surfaceMetricsDidChange(s)
        }
        Self.log.debug("attach \(self.agent.id, privacy: .public) role=\(String(describing: self.role), privacy: .public) mode=\(String(describing: mode), privacy: .public) font=\(font)")
        return s
    }

    private var scale: Double { Double(host.window?.backingScaleFactor ?? 2) }

    private var refitGeneration = 0
    private func scheduleRefit() {
        refitGeneration += 1
        let gen = refitGeneration
        // Debounced: arrangements that resize often (treemap, main + stack)
        // and frame animations settle before the one re-attach.
        DispatchQueue.main.asyncAfter(deadline: .now() + 0.3) { [weak self] in
            guard let self, gen == self.refitGeneration, let surface = self.surface else { return }
            if abs(self.tileFont - surface.fontSize) > 0.001 { self.recreate() }
        }
    }

    // MARK: Scrolling a tile (a view attach)

    /// Where the tile's window is in the agent's scrollback (hesperd's
    /// ScrollState, through `hesperd attach --view`'s title): nil until
    /// told; offset 0 is live.
    struct ScrollInfo: Equatable, Decodable { var offset: Int; var max: Int; var new: Int }
    private(set) var scrollInfo = ScrollInfo(offset: 0, max: -1, new: 0)
    var onScrollChanged: (() -> Void)?
    private var scrollAccum: Double = 0

    /// Moves the window by `lines` (> 0: back); fractional amounts add up
    /// (trackpads). False when there is nowhere to go (the wall scrolls).
    @discardableResult
    func scroll(lines: Double) -> Bool {
        guard role == .tile, !surfaceIsInteractive, let s = surface else { return false }
        if lines < 0 && scrollInfo.offset == 0 { scrollAccum = 0; return false }
        if lines > 0 && scrollInfo.max == 0 { scrollAccum = 0; return false }
        scrollAccum += lines
        let n = Int(scrollAccum.rounded(.towardZero))
        guard n != 0 else { return true }
        scrollAccum -= Double(n)
        s.sendText(n > 0 ? "S+\(n);" : "S-\(-n);")
        // Optimistic: the next state from hesperd corrects it.
        scrollInfo.offset = max(0, scrollInfo.offset + n)
        onScrollChanged?()
        return true
    }

    /// Back to the live bottom.
    func scrollToLive() {
        scrollAccum = 0
        guard role == .tile, let s = surface, scrollInfo.offset != 0 else { return }
        s.sendText("S=0;")
        scrollInfo.offset = 0
        scrollInfo.new = 0
        onScrollChanged?()
    }

    func surfaceTitleDidChange(_ s: any TerminalSurface, title: String) {
        guard s === surface, title.hasPrefix("hesper-scroll "),
              let info = try? JSONDecoder().decode(ScrollInfo.self, from: Data(title.dropFirst(14).utf8)) else { return }
        if info != scrollInfo {
            scrollInfo = info
            onScrollChanged?()
        }
    }

    // MARK: TerminalSurfaceDelegate

    func surfaceMetricsDidChange(_ s: any TerminalSurface) {
        guard s === surface, let m = s.metrics else { return }
        if CellMetrics.record(font: s.fontSize, widthPx: m.cellWidthPx, heightPx: m.cellHeightPx, scale: scale) {
            onCellMeasured?()
        }
    }

    func surfaceDidExit(_ s: any TerminalSurface) {
        guard s === surface else { return }
        if isAttachable { scheduleReattach() }
    }

    private func scheduleReattach() {
        guard !pendingReattach else { return }
        let now = Date()
        reattachTimes = reattachTimes.filter { now.timeIntervalSince($0) < 10 }
        guard reattachTimes.count < 5 else { return }
        reattachTimes.append(now)
        pendingReattach = true
        DispatchQueue.main.asyncAfter(deadline: .now() + 0.3) { [weak self] in
            guard let self else { return }
            self.pendingReattach = false
            if self.isAttachable && (self.surface == nil || self.surface!.processExited) { self.recreate() }
        }
    }
}
