import AppKit
import HesperCore
import SwiftUI

final class FlippedView: NSView {
    override var isFlipped: Bool { true }
    /// Re-applies layer colors set from outside (Dusk / Daylight).
    var onAppearanceChange: (() -> Void)?

    override func viewDidChangeEffectiveAppearance() {
        super.viewDidChangeEffectiveAppearance()
        onAppearanceChange?()
    }
}

/// One agent on the wall: a flat card with a header (`DS.tileHeader`), the
/// live terminal (exactly the rows it shows, spanning the body edge to edge
/// on the terminal's own background), and the footer, which becomes the
/// action strip (sliding up over the bottom rows) when it needs the user.
/// The card's border is the ring: signal for approval, question for a
/// question, error for an error, with a static soft glow (a layer shadow);
/// `working` when selected or active. Hover shows the focus Kbd.
@MainActor
final class TileView: NSView, TerminalDropTarget {
    let terminal: AgentTerminal
    private let body = FlippedView()
    /// The whole terminal area (under the header, above the band) in the
    /// terminal's own background: the sub-cell remainder above the
    /// bottom-anchored rows is never tile-colored, in Dusk or Daylight.
    private let terminalBackdrop = NSView()
    private let header: NSHostingView<TileHeader>
    private let divider = NSView()
    private let band: NSHostingView<AttentionBar>
    private let pill = NSHostingView(rootView: ScrollPill(offset: 0, newLines: 0, onLive: {}))
    private weak var model: AppModel?
    private(set) var agent: Agent
    /// What the footer shows (and the layout uses): the agent, held back
    /// 400 ms for calm changes (working ↔ idle, activity per tool call) so
    /// the footer never flickers; approval/question/error and leaving them
    /// show at once.
    private(set) var shown: Agent
    private var footerWork: DispatchWorkItem?
    private var footerPendingSince: Date?
    /// The shown state changed (the wall may move the card: the shelf).
    var onShownChanged: ((AgentState, AgentState) -> Void)?
    /// The layout this card was placed with (tests, layout dump).
    private(set) var placed: WallTile?
    /// Selected: a highlight ring, the wall's keys (arrows move, ⏎ / typing
    /// make it active, ⏎ / A / N answer when it needs you), the key hints
    /// in its footer. No cursor, no read-write attach.
    var isSelected = false { didSet { if oldValue != isSelected { updateRing(); refreshContent() } } }
    /// The active tile: typing goes to the agent (read-write attach).
    var isActive = false {
        didSet {
            guard oldValue != isActive else { return }
            terminal.interactive = isActive
            updateRing()
            refreshContent()
            if isActive { terminal.focusInput() }
        }
    }

    override var isFlipped: Bool { true }

    init(agent: Agent, model: AppModel) {
        self.agent = agent
        self.shown = agent
        self.model = model
        terminal = AgentTerminal(agent: agent, role: .tile, env: model.env)
        header = NSHostingView(rootView: TileHeader(agent: agent, local: agent.machine == model.localMachine))
        band = NSHostingView(rootView: AttentionBar(agent: agent, onAnswer: { _ in }, onDenyMessage: {}, onOpen: {}, onResume: {}))
        super.init(frame: .zero)
        wantsLayer = true
        DS.Radius.apply(DS.Radius.tile, to: layer)
        layer?.masksToBounds = false
        layer?.shadowOffset = .zero
        body.wantsLayer = true
        DS.Radius.apply(DS.Radius.tile, to: body.layer, masks: true)
        header.wantsLayer = true
        divider.wantsLayer = true
        terminalBackdrop.wantsLayer = true
        terminalBackdrop.layer?.backgroundColor = DS.terminalBackground.cgColor
        applyTheme()
        addSubview(body)
        body.addSubview(terminalBackdrop)
        body.addSubview(terminal.host)
        body.addSubview(header)
        body.addSubview(divider)
        body.addSubview(band)
        body.addSubview(pill)
        pill.isHidden = true
        terminal.onScrollChanged = { [weak self] in self?.scrollChanged() }
        addSubview(windowMarker) // window layer: ↗ marker
        setAccessibilityElement(true)
        setAccessibilityRole(.group)
        setAccessibilityIdentifier("tile.\(agent.id)")
        registerForDraggedTypes(TerminalDrop.types)
        apply(agent, connected: model.isConnected)
    }

    // MARK: Drop to attach (TerminalDrop): any state, the shelf included.

    lazy var terminalDrop = TerminalDrop(view: self, agentID: { [weak self] in self?.agent.id }, model: { [weak self] in self?.model })
    override func draggingEntered(_ sender: any NSDraggingInfo) -> NSDragOperation { terminalDrop.entered(sender) }
    override func draggingUpdated(_ sender: any NSDraggingInfo) -> NSDragOperation { terminalDrop.updated(sender) }
    override func draggingExited(_ sender: (any NSDraggingInfo)?) { terminalDrop.exited() }
    override func prepareForDragOperation(_ sender: any NSDraggingInfo) -> Bool { true }
    override func performDragOperation(_ sender: any NSDraggingInfo) -> Bool { terminalDrop.perform(sender) }

    @available(*, unavailable)
    required init?(coder: NSCoder) { fatalError() }

    /// What the band needs at this card's width: the footer's one line, or
    /// more for a two-row approval, which then covers the terminal's
    /// bottom rows instead of shrinking it.
    func bandHeight(width: CGFloat) -> CGFloat {
        max(Metrics.footer, AttentionBar.height(shown, width: width, confirming: model?.closeConfirm?.id == agent.id || model?.moveConfirm?.id == agent.id))
    }
    /// On the shelf: header and one line, no terminal.
    private(set) var onShelf = false

    func apply(_ a: Agent, connected: Bool) {
        agent = a
        guard let model else { return }
        scheduleFooter(a)
        refreshContent()
        terminal.update(a, connected: connected)
        setAccessibilityLabel(Self.accessibilityLabel(a))
        setAccessibilityValue(a.state.rawValue)
        updateRing()
        windowMarker.isHidden = !(model.hasAgentWindow?(a.id) ?? false) // window layer: ↗ marker
    }

    /// Urgent changes show now; calm ones after 400 ms of quiet (at most
    /// 1 s while they keep coming).
    private func scheduleFooter(_ a: Agent) {
        let s = shown
        let urgent = a.state.needsAttention || s.state.needsAttention || (a.exit == nil) != (s.exit == nil) || a.state == .exited
        if urgent {
            setShown(a)
            return
        }
        if a == s { return }
        let now = Date()
        let since = footerPendingSince ?? now
        footerPendingSince = since
        footerWork?.cancel()
        let delay = max(0, min(0.4, 1.0 - now.timeIntervalSince(since)))
        let w = DispatchWorkItem { [weak self] in
            guard let self else { return }
            self.setShown(self.agent)
        }
        footerWork = w
        DispatchQueue.main.asyncAfter(deadline: .now() + delay, execute: w)
    }

    private func setShown(_ a: Agent) {
        footerWork?.cancel()
        footerWork = nil
        footerPendingSince = nil
        let old = shown.state
        shown = a
        refreshContent()
        // Needs you now: the footer slides up as the action strip.
        if !old.needsAttention && a.state.needsAttention && !onShelf && window != nil { slideBand = true }
        if old != a.state { onShownChanged?(old, a.state) }
        needsLayout = true
    }

    /// VoiceOver: "api, mini, needs approval".
    static func accessibilityLabel(_ a: Agent) -> String { "\(a.name), \(a.machine), \(a.state.label)" }

    // MARK: Hover: the focus Kbd in the header

    private var hovering = false { didSet { if oldValue != hovering { refreshContent() } } }

    /// The card sits in its own project's band (WallView.sync): the header
    /// leaves the project out; `bandBranch` the band's branch.
    private var inOwnBand = false
    private var bandBranch: String?
    func setBand(own: Bool, branch: String?) {
        guard own != inOwnBand || branch != bandBranch else { return }
        inOwnBand = own
        bandBranch = branch
        refreshHeader()
    }
    private var hoverArea: NSTrackingArea?

    override func updateTrackingAreas() {
        super.updateTrackingAreas()
        if let hoverArea { removeTrackingArea(hoverArea) }
        let t = NSTrackingArea(rect: .zero, options: [.mouseEnteredAndExited, .activeInActiveApp, .inVisibleRect], owner: self)
        addTrackingArea(t)
        hoverArea = t
    }

    override func mouseEntered(with event: NSEvent) { hovering = true }
    override func mouseExited(with event: NSEvent) { hovering = false }

    private func refreshHeader(width: CGFloat? = nil) {
        guard let model else { return }
        let w = width ?? (bounds.width > 0 ? bounds.width : 600)
        let moving = model.moving[agent.id].map { model.machine($0)?.displayName ?? $0 }
        let project = model.projectLabel(agent)
        header.rootView = TileHeader(agent: agent, local: agent.machine == model.localMachine, compact: w < Self.compactHeaderWidth, movingTo: moving,
                                     typing: isActive, projectName: project?.name, projectColor: project?.colorHex, hovering: hovering,
                                     inOwnBand: inOwnBand, bandBranch: bandBranch, machineLabel: model.machine(agent.machine)?.displayName,
                                     killed: model.isKilled(agent))
    }

    /// Below this card width the header keeps the name, status and machine.
    static let compactHeaderWidth: CGFloat = 300

    private func refreshContent(width: CGFloat? = nil) {
        guard let model else { return }
        let a = shown
        let w = width ?? (bounds.width > 0 ? bounds.width : 600)
        let moving = model.moving[a.id].map { model.machine($0)?.displayName ?? $0 }
        refreshHeader(width: w)
        // Key hints only where the keys work: the selected (not active) tile.
        let keys = isSelected && !isActive
        band.rootView = AttentionBar(
            agent: a, wide: AttentionBar.isWide(a, width: w), shelf: onShelf, tight: AttentionBar.isTight(width: w),
            keyHints: keys, selectionHint: keys ? Self.selectionHint(width: w, shelf: onShelf, running: a.isRunning) : nil,
            onAnswer: { [weak model] d in model?.answer(a, d) },
            onDenyMessage: { [weak model] in model?.denyOpen = a.id },
            onOpen: { [weak model] in model?.focus(a.id) },
            onResume: { [weak model] in model?.resume(a) },
            deny: Self.denyField(a, model: model),
            closeStrip: Self.closeStrip(a, model: model),
            killed: model.isKilled(a), onClose: { [weak model] in model?.requestClose(a) },
            onChoose: { [weak model] i in model?.sendInput(a.id, "\(i + 1)") },
            moveStrip: Self.moveStrip(a, model: model, tight: AttentionBar.isTight(width: w)),
            moveProgress: Self.moveProgress(a, model: model),
            moveOffer: isSelected || hovering ? Self.moveOffer(a, model: model) : nil,
            reviewReady: Self.reviewReady(a, model: model))
        alphaValue = moving != nil ? Self.movingAlpha : (a.isRunning ? 1 : Self.stoppedAlpha)
    }

    /// The selected tile's footer: what the wall's keys do here.
    static func selectionHint(width: CGFloat, shelf: Bool, running: Bool) -> String? {
        guard running else { return nil }
        if shelf { return "⏎ open" }
        if AttentionBar.isTight(width: width) { return "←→↑↓ · ⏎ type" }
        return "←→↑↓ move · ⏎ type · ⇧-click window"
    }

    /// Review: the finished agent's "Ready to review" line.
    static func reviewReady(_ a: Agent, model: AppModel) -> AttentionBar.ReviewReady? {
        guard let item = model.reviewItem(a.id) else { return nil }
        return AttentionBar.ReviewReady(item: item, onOpen: { [weak model] in model?.openReview(select: a.id) })
    }

    /// The close's question on this agent's strip, if one is asked.
    static func closeStrip(_ a: Agent, model: AppModel) -> AttentionBar.CloseStrip? {
        guard let p = model.closeConfirm, p.id == a.id else { return nil }
        return AttentionBar.CloseStrip(ask: p.ask,
                                       onPrimary: { [weak model] in model?.confirmClose() },
                                       onSecondary: { [weak model] in model?.confirmClose(discardWorktree: true) },
                                       onCancel: { [weak model] in model?.cancelClose() })
    }

    /// A move's question on this agent's strip (moving: AppModel+Move).
    static func moveStrip(_ a: Agent, model: AppModel, tight: Bool) -> AttentionBar.MoveStrip? {
        guard let p = model.moveConfirm, p.id == a.id else { return nil }
        let target = model.machine(p.to)?.displayName ?? p.to
        let project = a.project.map { ($0 as NSString).lastPathComponent }
        return AttentionBar.MoveStrip(message: p.ask.message(name: a.name, target: target, project: project),
                                      primary: p.ask.primary(tight: tight)?.title, detail: p.ask.detail, actionable: p.ask.actionable,
                                      onPrimary: { [weak model] in model?.confirmMove() },
                                      onCancel: { [weak model] in model?.cancelMove() })
    }

    /// The quiet progress line while it moves (agents.moving).
    static func moveProgress(_ a: Agent, model: AppModel) -> AttentionBar.MoveLine? {
        guard let to = model.moving[a.id] else { return nil }
        let p = model.moveBook.progress[a.id] ?? MoveProgress(id: a.id, to: to)
        return AttentionBar.MoveLine(progress: p, target: model.machine(to)?.displayName ?? to, fork: model.moveBook.forks.contains(a.id))
    }

    /// "Continue on mini · Fork on mini" on a finished agent's band.
    static func moveOffer(_ a: Agent, model: AppModel) -> AttentionBar.MoveOffer? {
        guard MoveRules.showsOnStrip(a), let t = model.moveTargets(a).first else { return nil }
        return AttentionBar.MoveOffer(target: t.displayName,
                                      onMove: { [weak model] in model?.requestMove(a, to: t.short) },
                                      onFork: { [weak model] in model?.requestMove(a, to: t.short, options: MoveOptions(fork: true)) })
    }

    static func denyField(_ a: Agent, model: AppModel) -> AttentionBar.DenyField? {
        guard model.denyOpen == a.id, a.state == .approval else { return nil }
        return AttentionBar.DenyField(
            text: Binding(get: { [weak model] in model?.denyText[a.id] ?? "" }, set: { [weak model] in model?.denyText[a.id] = $0 }),
            onSubmit: { [weak model] msg in model?.answer(a, .deny, message: msg) },
            onCancel: { [weak model] in model?.denyOpen = nil })
    }

    // MARK: Scrolling back

    private func scrollChanged() {
        let info = terminal.scrollInfo
        pill.rootView = ScrollPill(offset: info.offset, newLines: info.new, onLive: { [weak self] in self?.terminal.scrollToLive() })
        pill.isHidden = info.offset == 0
        needsLayout = true
    }

    /// The wheel or trackpad over a read-only tile moves its window into
    /// the agent's scrollback (fractional lines add up, momentum included);
    /// at the live bottom, or sideways, the wall scrolls instead. The
    /// active tile's own terminal scrolls natively.
    override func scrollWheel(with event: NSEvent) {
        guard !onShelf, !terminal.surfaceIsInteractive, abs(event.scrollingDeltaY) >= abs(event.scrollingDeltaX) else {
            super.scrollWheel(with: event)
            return
        }
        let cell = placed?.cell.height ?? 16
        let lines = event.hasPreciseScrollingDeltas ? Double(event.scrollingDeltaY) / max(cell, 1) : Double(event.scrollingDeltaY) * 3
        if lines == 0 || !terminal.scroll(lines: lines) { super.scrollWheel(with: event) }
    }

    /// The model's per-tile state changed (deny field, moving, confirm).
    func refreshBand() { refreshContent() }

    /// The color of the ring (border) now shown (tests).
    private(set) var ringColor: NSColor = Theme.tileBorder

    override func viewDidChangeEffectiveAppearance() {
        super.viewDidChangeEffectiveAppearance()
        applyTheme()
        updateRing()
    }

    private func applyTheme() {
        body.layer?.backgroundColor = Theme.tileBG.cg(in: self)
        header.layer?.backgroundColor = Theme.headerBG.cg(in: self)
        divider.layer?.backgroundColor = Theme.hairline.cg(in: self)
    }

    private func updateRing() { themed { updateRingInAppearance() } }

    /// A moving card (to another machine) and a stopped one are quieter.
    static let movingAlpha: CGFloat = 0.6
    static let stoppedAlpha: CGFloat = 0.72

    /// The ring: 1 pt `line` (flat, no shadow); 2 pt `working` selected or
    /// active; needs you: 2 pt (3 selected) in the state's color with a
    /// static soft glow (a layer shadow along the shadow path: no blur
    /// pass per frame, nothing animates).
    enum Ring {
        static let rest: CGFloat = 1
        static let selected: CGFloat = 2
        static let need: CGFloat = 2
        static let needSelected: CGFloat = 3
        static let glowRadius: CGFloat = DS.Spacing.l
        static let glowAlpha: CGFloat = 0.45
    }

    private func updateRingInAppearance() {
        guard let layer else { return }
        let need = agent.state.needsAttention
        let color: NSColor
        let width: CGFloat
        if need {
            color = Theme.stateNS(agent.state)
            width = isSelected ? Ring.needSelected : Ring.need
            layer.shadowColor = color.withAlphaComponent(Ring.glowAlpha).cgColor
            layer.shadowRadius = Ring.glowRadius
            layer.shadowOpacity = 1
        } else {
            color = isActive || isSelected ? Theme.ns(.working) : Theme.tileBorder
            width = isActive || isSelected ? Ring.selected : Ring.rest
            layer.shadowOpacity = 0
        }
        ringColor = color
        layer.borderColor = color.cgColor
        layer.borderWidth = width
    }

    /// Places the card as the wall laid it out (wall coordinates); moves
    /// animate when `animated` (the terminal keeps its final size meanwhile:
    /// libghostty resizes once, and a new font re-attaches once, debounced).
    func place(_ t: WallTile, animated: Bool) {
        placed = t
        let target = NSRect(x: t.card.x, y: t.card.y, width: t.card.width, height: t.card.height)
        let shelfChanged = onShelf != t.quiet
        if shelfChanged {
            onShelf = t.quiet
            terminal.host.isHidden = t.quiet
            terminalBackdrop.isHidden = t.quiet
            terminal.parked = t.quiet
        }
        if shelfChanged || frame.width != target.width { refreshContent(width: target.width) }
        if !t.quiet { terminal.tileFont = t.font }
        if animated && !frame.isEmpty && frame != target {
            animator().frame = target
        } else if frame != target {
            frame = target
        }
        needsLayout = true
    }

    override func layout() {
        super.layout()
        body.frame = bounds
        layer?.shadowPath = CGPath(roundedRect: bounds, cornerWidth: DS.Radius.tile, cornerHeight: DS.Radius.tile, transform: nil)
        let spec = Metrics.wall
        let w = bounds.width
        header.frame = NSRect(x: 0, y: 0, width: w, height: spec.header)
        divider.frame = NSRect(x: 0, y: spec.header - 1, width: w, height: 1)
        let bandH = onShelf ? max(0, bounds.height - spec.header) : bandHeight(width: w)
        // The terminal sits above the footer's one line; a taller band
        // (the action strip) covers its bottom rows, drawn above it.
        placeBand(NSRect(x: 0, y: bounds.height - bandH, width: w, height: bandH))
        band.isHidden = bandH == 0
        windowMarker.place(in: bounds) // window layer: ↗ marker
        if let term = placed?.terminal, !pill.isHidden {
            let size = pill.fittingSize
            let fromBottom = placed!.card.maxY - term.maxY
            pill.frame = NSRect(x: bounds.width - size.width - DS.Spacing.l, y: bounds.height - fromBottom - size.height - DS.Spacing.m,
                                width: size.width, height: size.height)
        }
        if let t = placed, let term = t.terminal {
            // Bottom-anchored like the layout: the agent's last rows sit
            // right above the band, also while the card animates.
            let fromBottom = t.card.maxY - term.maxY
            let frame = NSRect(x: term.x - t.card.x, y: bounds.height - fromBottom - term.height, width: term.width, height: term.height)
            if terminal.host.frame != frame { terminal.host.frame = frame }
            // The body behind it, from under the header down to the band.
            // It spans the body edge to edge; the padding around the text
            // shows the terminal's own background, never another color.
            let back: NSRect
            if let body = t.body {
                let fromBottomBody = t.card.maxY - body.maxY
                back = NSRect(x: body.x - t.card.x, y: body.y - t.card.y, width: body.width,
                              height: max(0, bounds.height - fromBottomBody - (body.y - t.card.y)))
            } else {
                back = frame
            }
            if terminalBackdrop.frame != back { terminalBackdrop.frame = back }
        }
        terminal.layout()
    }

    /// The next band placement slides up from the card's bottom edge (the
    /// action strip appearing; DS.Motion.move, none under Reduce Motion).
    private var slideBand = false
    /// The band's last target (an animation in flight is not restarted).
    private var bandTarget: NSRect?

    private func placeBand(_ target: NSRect) {
        defer { slideBand = false }
        guard target != bandTarget else { return }
        bandTarget = target
        if slideBand && !DS.reduceMotion && target.height > 0 {
            band.frame = target.offsetBy(dx: 0, dy: target.height)
            DS.animate(.move) { band.animator().frame = target }
        } else {
            band.frame = target
        }
    }

    override func viewDidMoveToWindow() {
        super.viewDidMoveToWindow()
        terminal.layout()
    }

    // Window layer integration (docs: "As built — windows"): ⇧/⌘-click,
    // ⌥⇧-click and ⌃-click go to the window layer first; the context menu
    // and the ↗ marker (the agent has its own window) come from it too.
    let windowMarker = AgentWindowMarker()

    override func menu(for event: NSEvent) -> NSMenu? {
        model?.tileMenu?(agent.id) ?? super.menu(for: event)
    }

    /// A click selects the tile (the wall keeps the keys); a click on the
    /// selected tile makes it active (typing goes to the agent); a double
    /// click opens it full size.
    override func mouseDown(with event: NSEvent) {
        guard let model else { return }
        if model.tileClick?(agent.id, event, self) == true { return } // window layer
        if event.clickCount == 2 { model.focus(agent.id); return }
        if model.activeTileID == agent.id {
            terminal.focusInput()
        } else if model.selectedID == agent.id && model.editingDraftID == nil {
            model.activate(agent.id)
        } else {
            model.select(agent.id)
        }
        if !terminal.interactive { window?.makeFirstResponder(enclosingWall) }
    }

    private var enclosingWall: NSView? {
        var v = superview
        while let s = v, !(s is WallView) { v = s.superview }
        return v
    }
}

/// The wall: every agent as a live card, and drafts (new agents being
/// written) as composer cards, arranged by `WallLayout` (fills the window
/// to its margin; scrolls down, or sideways for columns, when cards would
/// get smaller than the minimum). It is the document view of a scroll
/// view; edge fades show cards off screen to the left or right.
@MainActor
final class WallView: NSView {
    private weak var model: AppModel?
    private(set) var tiles: [String: TileView] = [:]
    private(set) var draftTiles: [String: DraftTileView] = [:]
    /// Shared history: removed agents' ghost cards (Grid + Shelf).
    private(set) var ghostTiles: [String: GhostCardView] = [:]
    private(set) var order: [String] = []
    private(set) var layoutResult = WallLayout.empty
    private let empty: NSHostingView<EmptyWall>
    let scrollView = NSScrollView()
    let leftFade = EdgeFade(leading: true)
    let rightFade = EdgeFade(leading: false)
    /// The next layout animates its frame changes (an agent came, went,
    /// moved to or from the shelf, the arrangement changed).
    private var animateNext = false

    // Projects (views): bands, their headers and frames, the home wall's
    // needs-you strip.
    /// The bands this wall shows now (ResolvedView.flat: a plain wall).
    private(set) var view = ResolvedView.flat
    private(set) var bandHeaders: [String: BandHeaderView] = [:]
    private(set) var backdrops: [String: BandBackdropView] = [:]
    /// E's band weights after hysteresis (BandAllocator), from the last layout.
    private(set) var bandWeights: [String: Int] = [:]
    /// Grid's column count from the last layout (hysteresis: resizing a
    /// few points never flips it).
    private(set) var gridColumns: Int = 0
    private var bandSignature: [String] = []
    let strip = NSHostingView(rootView: NeedsYouStrip(agents: [], projectName: { _ in nil }, onOpen: { _ in }, onAllow: { _ in }))
    static let stripHeight: CGFloat = 54
    private var drag: (key: String, start: NSPoint, header: NSRect)?

    override var isFlipped: Bool { true }
    override var acceptsFirstResponder: Bool { true }

    init(model: AppModel) {
        self.model = model
        empty = NSHostingView(rootView: EmptyWall(message: nil, onNew: {}))
        super.init(frame: .zero)
        wantsLayer = true
        layer?.backgroundColor = Theme.windowBG.cg(in: self)
        addSubview(empty)
        strip.isHidden = true
        strip.setAccessibilityIdentifier("home.strip")
        addSubview(strip)
        setAccessibilityIdentifier("wall")
        scrollView.documentView = self
        scrollView.drawsBackground = true
        scrollView.backgroundColor = Theme.windowBG
        scrollView.hasVerticalScroller = true
        scrollView.hasHorizontalScroller = true
        scrollView.autohidesScrollers = true
        scrollView.scrollerStyle = .overlay
        scrollView.verticalScrollElasticity = .automatic
        scrollView.horizontalScrollElasticity = .automatic
        scrollView.contentView.postsBoundsChangedNotifications = true
        scrollView.postsFrameChangedNotifications = true
        NotificationCenter.default.addObserver(forName: NSView.frameDidChangeNotification, object: scrollView, queue: .main) { [weak self] _ in
            MainActor.assumeIsolated { self?.needsLayout = true }
        }
        NotificationCenter.default.addObserver(forName: NSView.boundsDidChangeNotification, object: scrollView.contentView, queue: .main) { [weak self] _ in
            MainActor.assumeIsolated { self?.updateFades() }
        }
        // Density (Settings): header, gutter, margin and band headings change.
        NotificationCenter.default.addObserver(forName: DS.densityDidChange, object: nil, queue: .main) { [weak self] _ in
            MainActor.assumeIsolated { self?.densityChanged() }
        }
    }

    /// The density changed: every card and band heading re-lays out (once,
    /// animated with the layout's settle).
    func densityChanged() {
        for t in tiles.values { t.needsLayout = true }
        for h in bandHeaders.values { h.needsLayout = true }
        settingsChanged()
    }

    @available(*, unavailable)
    required init?(coder: NSCoder) { fatalError() }

    override func viewDidChangeEffectiveAppearance() {
        super.viewDidChangeEffectiveAppearance()
        layer?.backgroundColor = Theme.windowBG.cg(in: self)
    }

    var renderingVisible = true {
        didSet { for t in tiles.values { t.terminal.visible = renderingVisible && !t.isHidden } }
    }

    /// The card view of an item (agent or draft).
    func card(_ id: String) -> NSView? { tiles[id] ?? draftTiles[id] ?? ghostTiles[id] }

    func sync() {
        guard let model else { return }
        let scoped = model.scopedWallItems
        let v = model.resolveView(scoped)
        view = v.showsBands ? v : .flat
        var items = scoped
        if v.showsBands {
            var byID: [String: WallItem] = [:]
            for i in scoped { byID[i.id] = i }
            items = v.order.compactMap { byID[$0] }
        }
        let ids = items.map(\.id)
        let signature = view.bands.map { "\($0.key)|\($0.members.joined(separator: ","))|\(model.isCollapsed($0))" }
        var relayout = ids != order || signature != bandSignature
        var animate = relayout
        bandSignature = signature
        syncBands(model)
        let staleAgents = tiles.keys.filter { !ids.contains($0) }
        let staleDrafts = draftTiles.keys.filter { !ids.contains($0) }
        for item in items {
            switch item {
            case .agent(let a):
                if let t = tiles[a.id] {
                    // The terminal area never changes with the state: only
                    // the shown (debounced) state moves cards, where the
                    // arrangement says so (onShownChanged).
                    t.apply(a, connected: model.isConnected)
                } else {
                    let t = TileView(agent: a, model: model)
                    t.onShownChanged = { [weak self] old, new in self?.shownStateChanged(old, new) }
                    t.terminal.visible = renderingVisible
                    t.terminal.onCellMeasured = { [weak self] in self?.needsLayout = true }
                    tiles[a.id] = t
                    // A started draft: the agent's card takes the draft's
                    // place and frame, so nothing moves.
                    if let did = model.adoptions.removeValue(forKey: a.id), let d = draftTiles[did] {
                        t.frame = d.frame
                        addSubview(t, positioned: .above, relativeTo: d)
                        t.alphaValue = Self.adoptAlpha
                        DS.animate(.move) { t.animator().alphaValue = a.isRunning ? 1 : TileView.stoppedAlpha }
                    } else {
                        addSubview(t, positioned: .below, relativeTo: empty)
                    }
                    SessionCardOverlay.attachIfResumed(t, model: model) // shared history: the left-off card first
                    relayout = true
                }
                tiles[a.id]?.isSelected = a.id == model.selectedID
                let band = view.band(of: a.id)
                tiles[a.id]?.setBand(own: TilePlace.inOwnProjectBand(projectID: model.catalog.projectID(for: a), band: band, catalog: model.catalog),
                                     branch: band?.level == .branch ? band?.branch : nil)
                tiles[a.id]?.refreshBand()
            case .draft(let d):
                if let t = draftTiles[d.id] {
                    let wasQuiet = t.draft.parked && !t.editing
                    t.apply(d)
                    t.editing = model.editingDraftID == d.id
                    if wasQuiet != (d.parked && !t.editing) { relayout = true; animate = true }
                } else {
                    let t = DraftTileView(draft: d, model: model)
                    t.editing = model.editingDraftID == d.id
                    draftTiles[d.id] = t
                    addSubview(t, positioned: .below, relativeTo: empty)
                    relayout = true
                }
                draftTiles[d.id]?.isSelected = d.id == model.selectedID
            case .ghost(let s): // shared history
                let gid = GhostCards.itemID(s)
                if let g = ghostTiles[gid] {
                    g.apply(s)
                } else {
                    let g = GhostCardView(session: s, model: model)
                    ghostTiles[gid] = g
                    addSubview(g, positioned: .below, relativeTo: empty)
                    relayout = true
                }
                ghostTiles[gid]?.isSelected = gid == model.selectedID
            }
        }
        for id in ghostTiles.keys where !ids.contains(id) { ghostTiles.removeValue(forKey: id)?.removeFromSuperview() } // shared history
        for id in staleAgents {
            tiles[id]?.terminal.close()
            tiles[id]?.removeFromSuperview()
            tiles.removeValue(forKey: id)
        }
        for id in staleDrafts {
            draftTiles[id]?.removeFromSuperview()
            draftTiles.removeValue(forKey: id)
        }
        order = ids
        syncStrip(model)
        empty.rootView = EmptyWall(message: items.isEmpty ? model.connectionMessage : nil,
                                   onNew: { [weak model] in model?.perform(.newAgent) })
        empty.isHidden = !items.isEmpty
        if relayout { animateNext = animateNext || animate; needsLayout = true }
    }

    /// A card's shown state changed: the shelf, treemap weights and the
    /// main slot follow it.
    private func shownStateChanged(_ old: AgentState, _ new: AgentState) {
        guard let model else { return }
        let quietChanged = WallTileInput(grid: GridSize(cols: 1, rows: 1), state: old).isQuiet
            != WallTileInput(grid: GridSize(cols: 1, rows: 1), state: new).isQuiet
        if quietChanged || model.arrangement == .treemap || model.arrangement == .mainStack {
            animateNext = true
            needsLayout = true
        }
    }

    /// The arrangement or the minimum card width changed.
    func settingsChanged() {
        animateNext = true
        needsLayout = true
        for t in tiles.values { t.refreshBand() }
        sync()
    }

    func reconnected() {
        for t in tiles.values { t.terminal.daemonReconnected() }
    }

    func updateSelection() {
        guard let model else { return }
        for (id, t) in tiles {
            t.isSelected = id == model.selectedID
            t.isActive = id == model.activeTileID && model.mode != .focus
        }
        var draftChanged = false
        for (id, t) in draftTiles {
            t.isSelected = id == model.selectedID
            let editing = model.editingDraftID == id
            if t.editing != editing { t.editing = editing; draftChanged = true }
        }
        for (id, g) in ghostTiles { g.isSelected = id == model.selectedID } // shared history
        // Main + stack follows the selection when nothing needs you.
        if model.arrangement == .mainStack || draftChanged { animateNext = true; needsLayout = true }
        scrollSelectionIntoView()
    }

    /// The editing draft's editor gets the keyboard (after layout placed it).
    func focusEditingDraft() {
        guard let id = model?.editingDraftID, let t = draftTiles[id] else { return }
        layoutSubtreeIfNeeded()
        t.focusEditor()
    }

    /// A selection not placed yet (a new draft): scrolled to after layout.
    private var scrollPending = false

    private func scrollSelectionIntoView() {
        guard let id = model?.selectedID else { return }
        guard let p = (tiles[id]?.placed ?? draftTiles[id]?.placed ?? ghostTiles[id]?.placed) else { scrollPending = true; return }
        scrollPending = false
        let m = Metrics.wall.margin
        // The room under the toolbar glass counts as hidden.
        let inset = scrollView.contentInsets.top
        let r = NSRect(x: p.card.x - m, y: p.card.y - m - inset, width: p.card.width + 2 * m, height: p.card.height + 2 * m + inset)
        let visible = scrollView.contentView.bounds
        guard !visible.contains(r) else { return }
        DS.animate(.settle) { scrollToVisible(r) }
    }

    static var reduceMotion: Bool { DS.reduceMotion }
    /// A started draft's agent card fades in from this.
    static let adoptAlpha: CGFloat = 0.4

    /// The visible size the wall lays out for (without the room under the
    /// toolbar glass: RootView's content inset).
    var viewport: NSSize {
        let s = scrollView.contentSize, i = scrollView.contentInsets
        return NSSize(width: max(0, s.width - i.left - i.right), height: max(0, s.height - i.top - i.bottom))
    }

    func spec() -> WallSpec {
        var s = Metrics.wall
        s.minChars = model?.minChars ?? 80
        s.tileFont = model?.settings.tileFontSize ?? 12
        return s
    }

    private func input(_ id: String) -> WallTileInput {
        if ghostTiles[id] != nil { return WallTileInput(grid: GridSize(cols: 120, rows: 40), state: .exited) } // shared history: shelf
        if let t = tiles[id] {
            return WallTileInput(grid: t.agent.size ?? GridSize(cols: 120, rows: 40), state: t.shown.state, band: Metrics.footer)
        }
        // A draft: an active card while written, quiet (the shelf) when left.
        let d = draftTiles[id]
        let quiet = (d?.draft.parked ?? false) && model?.editingDraftID != id && model?.startingDrafts[id] == nil
        return WallTileInput(grid: GridSize(cols: 120, rows: 40), state: quiet ? .idle : .working)
    }

    /// Lays the cards out for `size` (the scroll view's visible size).
    func computeLayout(for size: NSSize) -> WallLayout {
        let scale = Double(window?.backingScaleFactor ?? 2)
        let spec = spec()
        let cell: (Double) -> CellSize = { CellMetrics.cell($0, scale: scale) }
        let arrangement = model?.arrangement ?? .shelf
        var main: Int?
        if arrangement == .mainStack {
            let agents = order.compactMap { tiles[$0]?.agent }
            // The main card follows clicks, typing and focus (mainPick),
            // never the arrow keys: moving the selection resizes nothing.
            let target = AttentionQueue.ordered(agents).first?.id ?? model?.editingDraftID ?? model?.mainPick ?? model?.selectedID
            main = order.firstIndex { $0 == target }
        }
        // Every live card reserves the same one-line footer: no state
        // changes the terminal area.
        let inputs = order.map(input)
        let top = stripTop
        guard view.showsBands, let model else {
            return WallLayout.make(inputs, arrangement: arrangement, width: size.width, height: size.height, spec: spec, main: main, top: top,
                                   previousColumns: gridColumns, cell: cell)
        }
        var index: [String: Int] = [:]
        for (i, id) in order.enumerated() { index[id] = i }
        let slots = view.bands.map { BandSlot(key: $0.key, items: $0.members.compactMap { index[$0] }, collapsed: model.isCollapsed($0)) }
        return WallLayout.make(inputs, bands: slots, arrangement: arrangement, width: size.width, height: size.height, spec: spec, main: main,
                               top: top, previousWeights: bandWeights, previousColumns: gridColumns, cell: cell)
    }

    /// No strip row: "needs you elsewhere" is in the toolbar (attention popover).
    private var stripTop: Double { 0 }

    override func layout() {
        let size = viewport
        layoutResult = computeLayout(for: size)
        let content = NSSize(width: max(size.width, layoutResult.contentWidth), height: max(size.height, layoutResult.contentHeight))
        if frame.size != content { setFrameSize(content) }
        super.layout()
        empty.frame = NSRect(origin: .zero, size: size)
        let animate = animateNext && !Self.reduceMotion && window != nil
        animateNext = false
        NSAnimationContext.runAnimationGroup { ctx in
            ctx.duration = animate ? DS.Motion.settle.duration : 0
            ctx.timingFunction = DS.timingFunction
            for (i, id) in order.enumerated() where i < layoutResult.tiles.count {
                let lt = layoutResult.tiles[i]
                let card: NSView? = tiles[id] ?? draftTiles[id] ?? ghostTiles[id]
                // A collapsed band's cards: kept as they were, not shown, not rendered.
                if lt.hidden {
                    card?.isHidden = true
                    tiles[id]?.terminal.visible = false
                    continue
                }
                if card?.isHidden == true {
                    card?.isHidden = false
                    tiles[id]?.terminal.visible = renderingVisible
                }
                if let t = tiles[id] { t.place(lt, animated: animate) }
                else if let d = draftTiles[id] { d.place(lt, animated: animate) }
                else if let g = ghostTiles[id] { g.place(lt, animated: animate) } // shared history
            }
            placeBands(animated: animate)
        }
        bandWeights = layoutResult.bandWeights
        gridColumns = layoutResult.gridColumns
        if scrollPending { DispatchQueue.main.async { [weak self] in self?.scrollSelectionIntoView() } }
        let m = Metrics.wall.margin
        strip.frame = NSRect(x: m, y: m, width: max(0, size.width - 2 * m), height: Self.stripHeight)
        updateFades()
    }

    // MARK: Bands (projects)

    private func syncBands(_ model: AppModel) {
        let keys = Set(view.bands.map(\.key))
        for k in bandHeaders.keys where !keys.contains(k) {
            bandHeaders.removeValue(forKey: k)?.removeFromSuperview()
            backdrops.removeValue(forKey: k)?.removeFromSuperview()
        }
        for b in view.bands {
            let h: BandHeaderView
            if let x = bandHeaders[b.key] {
                h = x
            } else {
                h = BandHeaderView(key: b.key)
                let key = b.key
                h.onToggle = { [weak model] others in
                    guard let model else { return }
                    // A pointer line brings its project's own wall forward.
                    if let p = model.resolvedView.band(key: key)?.pointer { model.openPointer?(p.wall); return }
                    model.toggleCollapse(key, others: others)
                    // The header has the focus now: ⌘N opens a draft here.
                    model.focusedBandKey = key
                }
                h.onNew = { [weak model] in model?.newDraft(band: key) }
                h.onOpenWall = { [weak model] in
                    guard let model, let s = model.resolvedView.band(key: key)?.wallScope else { return }
                    model.sidebarAction?(.newWall(s))
                }
                h.onDrag = { [weak self] phase, p in self?.bandDrag(key, phase, p) }
                bandHeaders[b.key] = h
                addSubview(h)
                let bd = BandBackdropView()
                backdrops[b.key] = bd
                addSubview(bd, positioned: .below, relativeTo: subviews.first)
            }
            h.apply(b, counts: model.bandCounts(b), collapsed: model.isCollapsed(b), canOpenWall: model.sidebarAction != nil,
                    focused: model.focusedBandKey == b.key)
            backdrops[b.key]?.colorHex = b.colorHex
        }
    }

    private func placeBands(animated: Bool) {
        var seen: Set<String> = []
        for f in layoutResult.bands {
            seen.insert(f.key)
            guard let h = bandHeaders[f.key], let bd = backdrops[f.key] else { continue }
            let header = NSRect(x: f.header.x, y: f.header.y, width: f.header.width, height: f.header.height)
            let frame = NSRect(x: f.frame.x, y: f.frame.y, width: f.frame.width, height: f.frame.height)
            h.isHidden = false
            // The band's bordered container around its heading and cards
            // (working-tinted while it is a drag's drop target).
            // Only around cards: a collapsed band is just its heading row.
            bd.isHidden = f.collapsed
            if drag?.key == f.key { continue }
            if animated && !h.frame.isEmpty { h.animator().frame = header } else { h.frame = header }
            if animated && !bd.frame.isEmpty { bd.animator().frame = frame } else { bd.frame = frame }
        }
        for (k, h) in bandHeaders where !seen.contains(k) { h.isHidden = true; backdrops[k]?.isHidden = true }
    }

    /// Header drag: the header follows the mouse, the band under it
    /// lights up; dropped there, the band takes that place (this wall's
    /// band order, saved with the wall).
    private func bandDrag(_ key: String, _ phase: BandHeaderView.DragPhase, _ p: NSPoint) {
        guard let h = bandHeaders[key] else { return }
        switch phase {
        case .began:
            drag = (key, p, h.frame)
            h.alphaValue = 0.85
            h.layer?.zPosition = 10
        case .moved:
            guard let d = drag else { return }
            h.frame = d.header.offsetBy(dx: p.x - d.start.x, dy: p.y - d.start.y)
            let target = bandIndex(at: p)
            for (i, f) in layoutResult.bands.enumerated() {
                let on = i == target && f.key != key
                backdrops[f.key]?.highlighted = on
                if f.collapsed { backdrops[f.key]?.isHidden = !on }
            }
        case .ended:
            let target = bandIndex(at: p)
            drag = nil
            h.alphaValue = 1
            h.layer?.zPosition = 0
            for bd in backdrops.values { bd.highlighted = false }
            if let target { model?.moveBand(key, to: target) }
            animateNext = true
            needsLayout = true
        }
    }

    /// The band a point is over (else the nearest by its center).
    private func bandIndex(at p: NSPoint) -> Int? {
        let fs = layoutResult.bands
        guard !fs.isEmpty else { return nil }
        if let i = fs.firstIndex(where: { NSRect(x: $0.frame.x, y: $0.frame.y, width: $0.frame.width, height: $0.frame.height).contains(p) }) { return i }
        return fs.indices.min { a, b in
            hypot(fs[a].frame.midX - p.x, fs[a].frame.midY - p.y) < hypot(fs[b].frame.midX - p.x, fs[b].frame.midY - p.y)
        }
    }

    private func syncStrip(_ model: AppModel) {
        // "Needs you elsewhere" lives in the toolbar's attention pill and its
        // popover (marked "not on this wall"); the wall itself shows no strip.
        strip.isHidden = true
    }

    /// Shows a soft fade on a side with cards scrolled off it.
    func updateFades() {
        let b = scrollView.contentView.bounds
        leftFade.isHidden = b.minX <= 1
        rightFade.isHidden = b.maxX >= frame.width - 1
    }

    /// The card a keyboard move from `id` selects (nothing selected: the
    /// first card, End the last).
    func neighbor(_ id: String?, _ move: WallMove) -> String? {
        guard !order.isEmpty, layoutResult.tiles.count == order.count else { return nil }
        guard let id, let i = order.firstIndex(of: id) else {
            return order[layoutResult.neighbor(of: 0, move == .last ? .last : .first)]
        }
        return order[layoutResult.neighbor(of: i, move)]
    }

    /// Typing on a selected draft: its editor (now editing) gets the text.
    func typeIntoDraft(_ id: String, _ text: String) {
        layoutSubtreeIfNeeded()
        guard let t = draftTiles[id] else { return }
        t.focusEditor()
        let tv = t.editor.textView
        if window?.firstResponder === tv { tv.insertText(text, replacementRange: tv.selectedRange()) }
    }

    /// A click on the wall's background leaves the active tile (and the
    /// composer).
    override func mouseDown(with event: NSEvent) {
        model?.leaveComposer()
        model?.deactivateTile()
        window?.makeFirstResponder(self)
    }

    override func keyDown(with event: NSEvent) {
        guard let model else { return }
        let chord = KeyChord(event: event)
        // An inline question on the selected tile takes ⏎ and esc.
        if let p = model.closeConfirm, p.id == model.selectedID {
            if chord.key == .enter && chord.plain { model.confirmClose(); return }
            if chord.key == .escape && chord.plain { model.cancelClose(); return }
        }
        if let p = model.moveConfirm, p.id == model.selectedID {
            if chord.key == .enter && chord.plain { model.confirmMove(); return }
            if chord.key == .escape && chord.plain { model.cancelMove(); return }
        }
        // The active tile whose read-write terminal hasn't taken the
        // keyboard yet (it swaps in ≤ 800 ms after "start typing"): its keys
        // still go to the agent, in order.
        if model.mode == .wall, let id = model.activeTileID, model.agent(id)?.isRunning == true,
           let bytes = TerminalKeys.bytes(chord, characters: event.characters) {
            model.sendInput(id, bytes)
            return
        }
        // End: a scrolled-back tile goes live again.
        if event.keyCode == 119, let id = model.selectedID, let t = tiles[id], t.terminal.scrollInfo.offset > 0 {
            t.terminal.scrollToLive()
            return
        }
        if model.ghostKey(chord, keyCode: event.keyCode) { return } // shared history: ⏎ resume / ⌫ forget a ghost card
        let cmd = KeyRouter.route(chord, mode: .wall, selectedState: model.current?.state, selectedIsDraft: model.selectedDraft != nil,
                                   selectedAnswers: model.current?.attention?.answers ?? [], typed: event.characters,
                                   selectedReviewable: model.reviewItem(model.current?.id) != nil)
        if cmd == .none { super.keyDown(with: event) } else { model.perform(cmd) }
    }

    var tileViews: [NSView] { order.compactMap { tiles[$0]?.terminal.surface?.view } }

    /// Frames of cards that need the user, in `view`'s coordinates.
    func attentionFrames(in view: NSView) -> [CGRect] {
        tiles.values.filter { $0.agent.state.needsAttention && !$0.isHidden }.map { convert($0.frame, to: view) }
    }
}

/// A soft gradient at the wall's left or right edge: more cards that way.
final class EdgeFade: NSView {
    private let leading: Bool
    init(leading: Bool) {
        self.leading = leading
        super.init(frame: .zero)
        isHidden = true
    }

    /// The chevron pill: a Pill-high, twice as tall capsule.
    static let pillWidth = Pill.height
    static let pillHeight = 2 * Pill.height

    override func draw(_ dirtyRect: NSRect) {
        let bg = Theme.windowBG
        let g = NSGradient(colors: [bg.withAlphaComponent(1), bg.withAlphaComponent(0.85), bg.withAlphaComponent(0)],
                           atLocations: [0, 0.35, 1], colorSpace: .sRGB)
        g?.draw(in: bounds, angle: leading ? 0 : 180)
        // A small chevron pill: more cards that way.
        let w = Self.pillWidth, h = Self.pillHeight, r = w / 2
        let pill = NSRect(x: leading ? DS.Spacing.m : bounds.width - w - DS.Spacing.m, y: bounds.midY - h / 2, width: w, height: h)
        Theme.ns(.surface).setFill()
        NSBezierPath(roundedRect: pill, xRadius: r, yRadius: r).fill()
        Theme.tileBorder.setStroke()
        NSBezierPath(roundedRect: pill.insetBy(dx: 0.5, dy: 0.5), xRadius: r, yRadius: r).stroke()
        if let img = NSImage(systemSymbolName: leading ? "chevron.left" : "chevron.right", accessibilityDescription: nil)?
            .withSymbolConfiguration(.init(pointSize: CGFloat(DS.TextStyle.meta.size), weight: .semibold)) {
            let tinted = NSImage(size: img.size, flipped: false) { r in
                img.draw(in: r)
                Theme.ns(.dim).set()
                r.fill(using: .sourceAtop)
                return true
            }
            tinted.draw(in: NSRect(x: pill.midX - img.size.width / 2, y: pill.midY - img.size.height / 2, width: img.size.width, height: img.size.height))
        }
    }

    @available(*, unavailable)
    required init?(coder: NSCoder) { fatalError() }

    override func hitTest(_ point: NSPoint) -> NSView? { nil }
}

/// No agents yet (or no daemon): "Nothing running." in Geist 28 and the
/// three keys that start something: ⌘N new agent (clickable), ⌘Y history,
/// ⌘K search.
struct EmptyWall: View {
    var message: String?
    var onNew: () -> Void

    var body: some View {
        VStack(spacing: DS.Spacing.l) {
            Text(message == nil ? "Nothing running." : "Waiting for hesperd.")
                .font(DS.font(.display, .semibold))
                .foregroundStyle(Theme.fg)
            if let message {
                HStack(spacing: DS.Spacing.m) {
                    ProgressView().controlSize(.small)
                    Text(message)
                }
                .font(DS.font(.body))
                .foregroundStyle(Theme.dim)
                .accessibilityIdentifier("wall.empty")
            } else {
                VStack(alignment: .leading, spacing: DS.Spacing.s) {
                    Button(action: onNew) { key("⌘N", "new agent") }
                        .buttonStyle(.plain)
                        .accessibilityLabel("New agent, Command N")
                    key("⌘Y", "history")
                    key("⌘K", "search")
                }
                .accessibilityIdentifier("wall.empty")
            }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }

    private func key(_ keys: String, _ what: String) -> some View {
        HStack(spacing: DS.Spacing.m) {
            Kbd(keys)
            Text(what).font(DS.font(.body)).foregroundStyle(Theme.fg2)
        }
        .contentShape(Rectangle())
        .accessibilityElement(children: .combine)
    }
}

extension KeyChord {
    init(event: NSEvent) {
        let f = event.modifierFlags
        let key: Key
        switch event.keyCode {
        case 36, 76: key = .enter
        case 53: key = .escape
        case 123: key = .left
        case 124: key = .right
        case 125: key = .down
        case 126: key = .up
        case 48: key = .tab
        case 115: key = .home
        case 119: key = .end
        default:
            if let c = event.charactersIgnoringModifiers?.first { key = .char(c) } else { key = .other }
        }
        self.init(key, command: f.contains(.command), shift: f.contains(.shift), option: f.contains(.option), control: f.contains(.control))
    }
}
