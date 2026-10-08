import AppKit
import HesperCore
import SwiftUI

/// One agent full window, attached read-write as the size owner: this view's
/// grid becomes the PTY's size. Same card language as the wall: the tile
/// header at window scale, the terminal edge to edge on its own background
/// (the grid fills the whole area; libghostty paints the sub-cell remainder
/// in the terminal background), the attention band below. Stepping to the
/// previous / next agent slides the card in from that side.
@MainActor
final class FocusView: NSView, TerminalDropTarget {
    private weak var model: AppModel?
    private(set) var terminal: AgentTerminal?
    private let card = FlippedView()
    private let header = NSHostingView(rootView: FocusHeader(agent: nil, local: true, index: 0, count: 0))
    private let divider = NSView()
    private let bar = NSHostingView(rootView: AttentionBar(agent: Agent(id: "-"), inFocus: true, onAnswer: { _ in }, onDenyMessage: {}, onOpen: {}, onResume: {}))
    /// The tile header at window scale, within the chrome limit.
    static let headerHeight = DS.chromeMaxHeight
    static let margin = DS.Spacing.l
    /// The terminal spans the card's body: no tile-colored frame.
    static let inset: CGFloat = 0
    /// Room around the text, in the terminal's own background.
    static let padX = DS.Spacing.l
    static let padY = DS.Spacing.m
    /// A readable line length: on a wide window the text column stays at
    /// most this wide, centered, with the terminal's background around it
    /// (the agent then wraps at ~180 columns, not 300+).
    static let maxTextWidth: CGFloat = 1400
    private let terminalBackdrop = NSView()
    /// The close's strip shows (one row).
    private var confirming = false
    /// The slide's offset and starting opacity.
    static let slideOffset = DS.Spacing.xxl
    static let slideFadeFrom: Float = 0.35

    override var isFlipped: Bool { true }

    init(model: AppModel) {
        self.model = model
        super.init(frame: .zero)
        wantsLayer = true
        card.wantsLayer = true
        DS.Radius.apply(DS.Radius.tile, to: card.layer, masks: true)
        card.layer?.borderWidth = 1
        header.wantsLayer = true
        divider.wantsLayer = true
        applyTheme()
        addSubview(card)
        card.addSubview(header)
        card.addSubview(divider)
        card.addSubview(bar)
        setAccessibilityIdentifier("focus")
        registerForDraggedTypes(TerminalDrop.types)
    }

    // MARK: Drop to attach (TerminalDrop): the highlight covers the card.

    lazy var terminalDrop = TerminalDrop(view: card, agentID: { [weak self] in self?.agentID }, model: { [weak self] in self?.model })
    override func draggingEntered(_ sender: any NSDraggingInfo) -> NSDragOperation { terminalDrop.entered(sender) }
    override func draggingUpdated(_ sender: any NSDraggingInfo) -> NSDragOperation { terminalDrop.updated(sender) }
    override func draggingExited(_ sender: (any NSDraggingInfo)?) { terminalDrop.exited() }
    override func prepareForDragOperation(_ sender: any NSDraggingInfo) -> Bool { true }
    override func performDragOperation(_ sender: any NSDraggingInfo) -> Bool { terminalDrop.perform(sender) }

    @available(*, unavailable)
    required init?(coder: NSCoder) { fatalError() }

    override func viewDidChangeEffectiveAppearance() {
        super.viewDidChangeEffectiveAppearance()
        applyTheme()
    }

    private func applyTheme() {
        themed {
            layer?.backgroundColor = Theme.windowBG.cgColor
            card.layer?.backgroundColor = Theme.tileBG.cgColor
            card.layer?.borderColor = Theme.tileBorder.cgColor
            header.layer?.backgroundColor = Theme.headerBG.cgColor
            divider.layer?.backgroundColor = Theme.hairline.cgColor
        }
    }

    var agentID: String? { terminal?.agent.id }

    /// Window layer: whether a new terminal starts as the PTY's size owner
    /// (SizeOwnership adjusts it as windows become key).
    var ownsSizeDefault = true

    /// The direction of the next agent switch's slide (+1 in from the
    /// right, -1 from the left): set by an agent window's step; otherwise
    /// inferred from the wall's order.
    var nextSlide: Int?

    /// Shows `agent`, replacing the current terminal if it's another agent.
    func show(_ agent: Agent, fontSize: Double) {
        guard let model else { return }
        if terminal?.agent.id != agent.id {
            let previous = terminal?.agent.id
            let direction = nextSlide ?? previous.map { FocusSlide.direction(from: $0, to: agent.id, order: model.wall.map(\.id)) } ?? 1
            nextSlide = nil
            terminal?.close()
            terminal?.host.removeFromSuperview()
            let t = AgentTerminal(agent: agent, role: .focus, env: model.env)
            t.userFontSize = fontSize
            t.ownsSize = ownsSizeDefault // window layer: size ownership
            terminal = t
            card.addSubview(t.host, positioned: .below, relativeTo: header)
            needsLayout = true
            layoutSubtreeIfNeeded()
            if previous != nil { slide(direction) }
        }
        apply(agent, connected: model.isConnected)
        focusTerminal()
    }

    /// ←/→ between agents: the card comes in from the side it was stepped
    /// to (DS.Motion.move); instant under Reduce Motion. Only the card's
    /// layer moves (no relayout, the PTY size is untouched).
    private func slide(_ direction: Int) {
        guard let layer = card.layer,
              let move = DS.caAnimation(.move, keyPath: "transform.translation.x"),
              let fade = DS.caAnimation(.move, keyPath: "opacity") else { return }
        move.fromValue = CGFloat(direction) * Self.slideOffset
        move.toValue = 0
        fade.fromValue = Self.slideFadeFrom
        fade.toValue = 1
        layer.add(move, forKey: "focus.slide")
        layer.add(fade, forKey: "focus.slideFade")
    }

    func focusTerminal() {
        guard let t = terminal, let v = t.surface?.view else {
            DispatchQueue.main.async { [weak self] in
                if let v = self?.terminal?.surface?.view { self?.window?.makeFirstResponder(v) }
            }
            return
        }
        window?.makeFirstResponder(v)
    }

    private var cardWidth: CGFloat { max(0, bounds.width - 2 * Self.margin) }

    func apply(_ a: Agent, connected: Bool) {
        guard let model, let t = terminal, t.agent.id == a.id else { return }
        let w = model.wall
        let barChanged = AttentionBar.height(a, width: cardWidth, inFocus: true) != AttentionBar.height(t.agent, width: cardWidth, inFocus: true)
        t.update(a, connected: connected)
        header.rootView = FocusHeader(agent: a, local: a.machine == model.localMachine,
                                      index: (w.firstIndex(where: { $0.id == a.id }) ?? 0) + 1, count: w.count, killed: model.isKilled(a))
        bar.rootView = AttentionBar(agent: a, inFocus: true, wide: AttentionBar.isWide(a, width: cardWidth, inFocus: true), keyHints: false,
                                    onAnswer: { [weak model] d in model?.answer(a, d) },
                                    onDenyMessage: { [weak model] in model?.denyOpen = a.id },
                                    onOpen: {}, onResume: { [weak model] in model?.resume(a) },
                                    deny: TileView.denyField(a, model: model),
                                    closeStrip: TileView.closeStrip(a, model: model),
                                    killed: model.isKilled(a), onClose: { [weak model] in model?.requestClose(a) })
        if barChanged || confirming != (model.closeConfirm?.id == a.id) { needsLayout = true }
        confirming = model.closeConfirm?.id == a.id
    }

    /// The card's frame in `view` (the zoom transition).
    func cardFrame(in view: NSView) -> NSRect {
        let m = Self.margin
        let f = NSRect(x: m, y: m, width: max(0, bounds.width - 2 * m), height: max(0, bounds.height - 2 * m))
        return convert(f, to: view)
    }

    func close() {
        terminal?.close()
        terminal?.host.removeFromSuperview()
        terminal = nil
    }

    func reconnected() { terminal?.daemonReconnected() }

    override func layout() {
        super.layout()
        let m = Self.margin
        // The same margin on every side, also below the top bar.
        card.frame = NSRect(x: m, y: m, width: cardWidth, height: max(0, bounds.height - 2 * m))
        let w = card.bounds.width, h = card.bounds.height
        header.frame = NSRect(x: 0, y: 0, width: w, height: Self.headerHeight)
        divider.frame = NSRect(x: 0, y: Self.headerHeight - 1, width: w, height: 1)
        // A fixed one-line footer (the PTY's size must not follow states);
        // a two-row band covers the terminal's bottom instead.
        let reserve = Metrics.footer
        let barH = max(reserve, terminal.map { AttentionBar.height($0.agent, width: w, inFocus: true, confirming: confirming) } ?? 0)
        bar.frame = NSRect(x: 0, y: h - barH, width: w, height: barH)
        bar.isHidden = false
        let i = Self.inset
        let bodyFrame = NSRect(x: i, y: Self.headerHeight + i, width: max(0, w - 2 * i),
                               height: max(0, h - Self.headerHeight - reserve - 2 * i))
        if terminalBackdrop.superview !== card {
            terminalBackdrop.wantsLayer = true
            terminalBackdrop.layer?.backgroundColor = DS.terminalBackground.cgColor
            card.addSubview(terminalBackdrop, positioned: .below, relativeTo: nil)
        }
        terminalBackdrop.frame = bodyFrame
        var text = bodyFrame.insetBy(dx: Self.padX, dy: Self.padY)
        if text.width > Self.maxTextWidth {
            text.origin.x += ((text.width - Self.maxTextWidth) / 2).rounded()
            text.size.width = Self.maxTextWidth
        }
        terminal?.host.frame = text
        terminal?.layout()
    }

    override func viewDidMoveToWindow() {
        super.viewDidMoveToWindow()
        terminal?.layout()
    }
}

/// The tile header at window scale: the state square, the name in Geist
/// 600, how long it has been in that state, machine · tool in Geist Mono
/// on the right (Horizon for another Mac), then the agent's place in the
/// wall's order and the keys that step and leave.
struct FocusHeader: View {
    var agent: Agent?
    var local: Bool
    var index: Int
    var count: Int
    /// Ended by ⌃⌘W: "Killed".
    var killed = false

    var body: some View {
        HStack(spacing: DS.Spacing.m) {
            if let agent {
                StateMark(state: agent.state)
                Text(agent.name)
                    .font(DS.font(.panelTitle, .semibold))
                    .foregroundStyle(Theme.fg)
                    .lineLimit(1).truncationMode(.tail)
                    .layoutPriority(2)
                if let since = Theme.elapsed(since: agent.stateSince) {
                    Text("\(killed ? CloseText.killed : agent.state.label) · \(since)")
                        .font(DS.font(.chrome)).foregroundStyle(Theme.dim)
                        .lineLimit(1).truncationMode(.tail)
                }
                Spacer(minLength: DS.Spacing.m)
                Text("\(agent.machine) · \(Theme.kindLabel(agent.kind))")
                    .font(DS.font(.meta))
                    .foregroundStyle(local ? Theme.fg2 : Theme.color(.horizon))
                    .lineLimit(1).fixedSize()
                    .accessibilityIdentifier("tile.mark")
            } else {
                Spacer()
            }
            Rectangle().fill(Theme.stroke).frame(width: 1, height: DS.Spacing.l)
            if count > 0 { Text("\(index) of \(count)").font(DS.font(.meta)).foregroundStyle(Theme.dim).fixedSize() } // window layer: agent windows have no wall order
            // Plain ← → belong to the agent; ⌥⌘← ⌥⌘→ (⌘[ ⌘]) step.
            hint("⌥⌘← →", "agents").accessibilityIdentifier("focus.stepHint")
            hint("⌘↩", "wall")
        }
        .padding(.horizontal, DS.Spacing.l)
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .accessibilityElement(children: .contain)
        .accessibilityLabel(agent.map { "\($0.name), \($0.machine), \($0.state.label)" } ?? "")
    }

    private func hint(_ keys: String, _ what: String) -> some View {
        HStack(spacing: DS.Spacing.xs) {
            Kbd(keys)
            Text(what).font(DS.font(.chrome)).foregroundStyle(Theme.dim)
        }
        .fixedSize()
    }
}
