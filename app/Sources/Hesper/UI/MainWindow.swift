import AppKit
import HesperCore
import Observation
import SwiftUI

/// Re-runs `body` whenever an observed property it read changes.
@MainActor
func observeChanges(_ body: @escaping @MainActor () -> Void) {
    withObservationTracking {
        body()
    } onChange: {
        DispatchQueue.main.async { observeChanges(body) }
    }
}

/// The key window routes every ⌘ shortcut through KeyRouter before menus or
/// the terminal see it, so the wall, the composer and the focus view
/// behave the same; open overlays get ↑↓ ⏎ ⌘⏎ ⇥ esc and typing first
/// (OverlayKeys).
class MainWindow: NSWindow { // not final: the window layer's AgentWindow refines ⌘W, ⌘[ ⌘]
    weak var model: AppModel?

    private var textHasFocus: Bool { firstResponder is NSText }
    private var composerHasFocus: Bool { firstResponder is ComposerTextView }

    /// A close asking on the agent's strip (needs you, a running command,
    /// its worktree) takes ⏎ and esc first, wherever the keyboard is
    /// (the wall, an active tile, the focus view's terminal).
    private func closeStripKey(_ event: NSEvent) -> Bool {
        guard event.type == .keyDown, let model, let p = model.closeConfirm, !model.showPalette, model.popover == nil,
              model.current?.id == p.id || model.selectedID == p.id else { return false }
        let chord = KeyChord(event: event)
        guard chord.plain && !chord.shift else { return false }
        if chord.key == .enter { model.confirmClose(); return true }
        if chord.key == .escape { model.cancelClose(); return true }
        return false
    }

    override func performKeyEquivalent(with event: NSEvent) -> Bool {
        guard let model, event.type == .keyDown else { return super.performKeyEquivalent(with: event) }
        if HistoryPanelHost.handleKeyEquivalent(event, window: self) { return true } // shared history: the panel's ⌘ keys
        let chord = KeyChord(event: event)
        if model.showPalette {
            if chord.command && chord.key == .char("k") { model.showPalette = false; return true }
            if chord.command && chord.key == .enter { return model.paletteKey(.alternate) }
            return super.performKeyEquivalent(with: event)
        }
        if model.popover != nil {
            if chord.command && chord.key == .enter { return model.popoverKey(.alternate) }
            if chord.command && chord.key == .char("k") { model.closePopover(); model.perform(.palette); return true }
            if chord.command && chord.key == .char("o"), model.popover == .attention { return model.attentionOpenSelected() } // ⌘J inbox: open the tile
            if chord.command && !chord.option && !chord.control && !chord.shift && chord.key == .char("w"), model.popover == .background {
                return model.closeFromTray() // the background tray: ⌘W closes the selected agent
            }
        }
        if model.activeTileID != nil, firstResponder is GhosttySurfaceView, model.mode != .focus {
            // The active tile: like focus, only the app's ⌘ shortcuts.
            let cmd = KeyRouter.routeActiveTile(chord)
            switch cmd {
            case .passThrough, .none: return super.performKeyEquivalent(with: event)
            default: model.perform(cmd); return true
            }
        }
        if composerHasFocus {
            let cmd = KeyRouter.route(chord, mode: .compose, selectedState: nil)
            switch cmd {
            case .passThrough, .none: return super.performKeyEquivalent(with: event)
            default: model.perform(cmd); return true
            }
        }
        if textHasFocus, chord.command, !chord.option, case .char(let c) = chord.key, "zxcvay".contains(c) {
            return super.performKeyEquivalent(with: event) // a text field's own editing
        }
        let mode: AppMode = model.mode == .compose ? .wall : model.mode
        let cmd = KeyRouter.route(chord, mode: mode, selectedState: model.current?.state, selectedIsDraft: model.selectedDraft != nil)
        switch cmd {
        case .none, .passThrough, .select, .answer, .denyWithMessage, .editDraft, .typeInto, .activateTile, .jumpBand:
            return super.performKeyEquivalent(with: event)
        case .stepNext where !chord.command, .stepPrevious where !chord.command: // Tab: the wall's keyDown (a text field keeps it)
            return super.performKeyEquivalent(with: event)
        default:
            model.perform(cmd)
            return true
        }
    }

    override func sendEvent(_ event: NSEvent) {
        if closeStripKey(event) { return } // closing agents: the strip's ⏎ / esc
        if event.type == .keyDown, !(model?.showPalette ?? false), HistoryPanelHost.handleKey(event, window: self) { return } // shared history
        if event.type == .keyDown, let model {
            let chord = KeyChord(event: event)
            let action = OverlayKeys.route(chord, characters: event.characters)
            if model.showPalette {
                switch action {
                case .up, .down, .activate, .actOn, .back, .close:
                    if model.paletteKey(action) { return }
                default: break
                }
            } else if model.popover != nil, !textHasFocus || action == .close {
                if model.popoverKey(action) { return }
            }
        }
        super.sendEvent(event)
    }
}

/// The overlay layer takes clicks only where an overlay is (or everywhere
/// while one is modal-ish: the palette, a popover: a click outside closes).
final class OverlayHostingView: NSHostingView<OverlayRoot> {
    var hit: OverlayHit?

    override func hitTest(_ point: NSPoint) -> NSView? {
        guard let hit, !isHidden else { return nil }
        if hit.catchAll { return super.hitTest(point) }
        let p = convert(point, from: superview)
        guard hit.hot.values.contains(where: { $0.contains(p) }) else { return nil }
        return super.hitTest(point)
    }
}

@MainActor
final class RootView: NSView {
    let model: AppModel
    let wall: WallView
    let focus: FocusView
    /// The project sidebar (⌘0, walls only).
    let sidebar: ProjectSidebar
    /// The toolbar's glass: the wall scrolls under it.
    let toolbarGlass = ToolbarGlass()
    private let overlay: OverlayHostingView
    private let hit = OverlayHit()
    var userFontSize: Double = 13
    /// Toolbar controls' frames (screen coordinates) for popovers.
    var toolbarAnchor: ((PopoverKind) -> NSRect?)?
    private var hideOverlayWork: DispatchWorkItem?
    private var zoomGhost: NSView?

    override var isFlipped: Bool { true }

    init(model: AppModel) {
        self.model = model
        wall = WallView(model: model)
        focus = FocusView(model: model)
        sidebar = ProjectSidebar(model: model)
        overlay = OverlayHostingView(rootView: OverlayRoot(model: model, hit: hit))
        super.init(frame: .zero)
        overlay.hit = hit
        wantsLayer = true
        layer?.backgroundColor = Theme.windowBG.cg(in: self)
        addSubview(wall.scrollView)
        addSubview(wall.leftFade)
        addSubview(wall.rightFade)
        addSubview(focus)
        addSubview(sidebar)
        addSubview(toolbarGlass)
        addSubview(overlay)
        wall.scrollView.automaticallyAdjustsContentInsets = false
        sidebar.isHidden = true
        focus.isHidden = true
        overlay.isHidden = true
        model.neighbor = { [weak self] id, move in self?.wall.neighbor(id, move) }
        model.tileHasTerminal = { [weak self] id in self?.wall.tiles[id].map { !$0.onShelf } ?? true }
        model.insertIntoDraft = { [weak self] id, text in self?.wall.typeIntoDraft(id, text) }
        model.onAgentsChanged = { [weak self] in self?.agentsChanged() }
        model.onModeChanged = { [weak self] in self?.modeChanged() }
        model.onLayoutSettingsChanged = { [weak self] in self?.wall.settingsChanged() }
        model.onReconnected = { [weak self] in self?.wall.reconnected(); self?.focus.reconnected() }
        model.anchorProvider = { [weak self] k in self?.anchor(for: k) }
        model.attentionFrames = { [weak self] in
            guard let self, self.model.mode != .focus else { return [] }
            return self.wall.attentionFrames(in: self)
        }
        model.cardFrame = { [weak self] id in
            guard let self, let c = self.wall.card(id) else { return nil }
            return c.convert(c.bounds, to: self)
        }
        observeChanges { [weak self] in self?.overlayStateChanged() }
        HistoryPanelHost.attach(self) // shared history (⌘Y panel, ghost cards)
        observeChanges { [weak self] in
            guard let self else { return }
            _ = (self.model.sidebarVisible, self.model.scope, self.model.machines)
            self.needsLayout = true
            if self.showsSidebar { self.sidebar.reload() }
        }
    }

    /// The sidebar shows on walls (not agent windows), not in focus.
    var showsSidebar: Bool { model.sidebarVisible && model.sidebarAction != nil && model.mode != .focus }

    @available(*, unavailable)
    required init?(coder: NSCoder) { fatalError() }

    override func viewDidChangeEffectiveAppearance() {
        super.viewDidChangeEffectiveAppearance()
        layer?.backgroundColor = Theme.windowBG.cg(in: self)
    }

    private func overlayStateChanged() {
        let completion = model.editingDraftID.flatMap { model.composers[$0]?.completion } != nil
        let modal = model.showPalette || model.popover != nil
        let show = modal || model.toast != nil || model.undoToast != nil || completion
        hit.catchAll = modal
        hideOverlayWork?.cancel()
        if show {
            overlay.isHidden = false
        } else {
            // Let the 80 ms close finish, then take the layer away entirely.
            let w = DispatchWorkItem { [weak self] in self?.overlay.isHidden = true }
            hideOverlayWork = w
            DispatchQueue.main.asyncAfter(deadline: .now() + 0.12, execute: w)
        }
        if !modal && !model.showPalette {
            restoreKeyboard()
        }
    }

    /// After an overlay closed: the keyboard goes back where it belongs.
    private func restoreKeyboard() {
        guard let window else { return }
        if historyPanel?.isOpen == true { return } // shared history: the panel keeps the keyboard (toasts come and go)
        if window.firstResponder is NSText && !(window.firstResponder is ComposerTextView) && model.denyOpen != nil { return }
        switch model.mode {
        case .focus: focus.focusTerminal()
        case .compose: wall.focusEditingDraft()
        case .wall:
            if let id = model.activeTileID, let t = wall.tiles[id], t.terminal.surfaceIsInteractive {
                t.terminal.focusInput()
            } else if model.denyOpen == nil, !(window.firstResponder === wall) { window.makeFirstResponder(wall) }
        }
    }

    override func layout() {
        super.layout()
        // The window has a full-size content view; the unified toolbar (the
        // status) floats over the top `top` points on its glass. The wall
        // scrolls under it (a content inset keeps the first band clear);
        // the sidebar and the focus view start below it. Full screen: the
        // toolbar auto-hides and the inset is 0.
        let top = safeAreaInsets.top
        var content = NSRect(x: 0, y: top, width: bounds.width, height: max(0, bounds.height - top))
        let side = showsSidebar
        if sidebar.isHidden == side {
            sidebar.isHidden = !side
            if side { sidebar.reload() }
        }
        if side {
            sidebar.frame = NSRect(x: 0, y: top, width: ProjectSidebar.width, height: content.height)
            content = NSRect(x: ProjectSidebar.width, y: top, width: max(0, bounds.width - ProjectSidebar.width), height: content.height)
        }
        wall.scrollView.frame = NSRect(x: content.minX, y: 0, width: content.width, height: bounds.height)
        setWallTopInset(top)
        let fade = Self.fadeWidth
        wall.leftFade.frame = NSRect(x: content.minX, y: content.minY, width: fade, height: content.height)
        wall.rightFade.frame = NSRect(x: content.maxX - fade, y: content.minY, width: fade, height: content.height)
        focus.frame = content
        toolbarGlass.frame = NSRect(x: 0, y: 0, width: bounds.width, height: top)
        toolbarGlass.isHidden = top <= 0
        overlay.frame = bounds
        historyPanel?.frame = historyFrame // shared history
    }

    /// The soft edge on a side with cards scrolled off it.
    static let fadeWidth: CGFloat = 56

    /// The wall's room under the toolbar glass; a wall scrolled to its top
    /// stays at its top when the inset changes.
    private func setWallTopInset(_ top: CGFloat) {
        let sv = wall.scrollView
        guard sv.contentInsets.top != top else { return }
        let clip = sv.contentView
        let atTop = clip.bounds.minY <= -sv.contentInsets.top + 0.5
        sv.contentInsets = NSEdgeInsets(top: top, left: 0, bottom: 0, right: 0)
        if atTop {
            clip.scroll(to: NSPoint(x: clip.bounds.minX, y: -top))
            sv.reflectScrolledClipView(clip)
        }
        wall.needsLayout = true
    }

    // MARK: Shared history (the ⌘Y panel sits over the wall, under the overlays)

    private(set) weak var historyPanel: HistoryPanel?
    var historyFrame: NSRect {
        let top = safeAreaInsets.top
        return NSRect(x: 0, y: top, width: bounds.width, height: max(0, bounds.height - top))
    }

    func addHistoryPanel(_ p: HistoryPanel) {
        historyPanel = p
        addSubview(p, positioned: .below, relativeTo: overlay)
    }

    func historyClosed() { restoreKeyboard() }

    private func anchor(for kind: PopoverKind) -> CGRect? {
        switch kind {
        case .move(let id), .rename(let id):
            if model.mode == .focus {
                let f = focus.convert(focus.bounds, to: self)
                return CGRect(x: f.minX + 24, y: f.minY, width: 200, height: FocusView.headerHeight)
            }
            guard let t = wall.card(id) else { return nil }
            let f = t.convert(t.bounds, to: self)
            return CGRect(x: f.minX + 12, y: f.minY + 4, width: min(200, f.width - 24), height: Metrics.wall.header - 6)
        case .chip(let id, let k):
            return wall.draftTiles[id]?.chipFrame(k, in: self)
        case .desks, .deskName:
            // Desks: under the toolbar, centred on the window.
            let top = safeAreaInsets.top
            return CGRect(x: bounds.midX - 20, y: top - 6, width: 40, height: 4)
        case .attention, .machines, .layout, .scope, .background:
            // The toolbar gives screen coordinates (in full screen it is a
            // window of its own).
            guard let window else { return nil }
            if let r = toolbarAnchor?(kind) {
                let local = convert(window.convertFromScreen(r), from: nil)
                if local.maxY > 0 && local.minY < bounds.height { return local }
            }
            // The toolbar is away (full screen, auto-hidden): the popover
            // hangs from the top edge, on the control's side.
            let w = DS.Spacing.xxl
            let right = kind == .attention || kind == .layout || kind == .background
            return CGRect(x: right ? bounds.maxX - DS.Spacing.xl - w : bounds.minX + DS.Spacing.xl, y: 0, width: w, height: DS.Spacing.xs)
        }
    }

    private func agentsChanged() {
        wall.sync()
        if showsSidebar { sidebar.reload() }
        if model.mode == .focus, let id = focus.agentID {
            if let a = model.agent(id) { focus.apply(a, connected: model.isConnected) } else { model.exitFocus() }
        }
    }

    private var shownMode: AppMode = .wall

    private func modeChanged() {
        wall.updateSelection()
        needsLayout = true
        let entering = model.mode == .focus && shownMode != .focus
        let leaving = model.mode != .focus && shownMode == .focus
        shownMode = model.mode
        switch model.mode {
        case .wall, .compose:
            if leaving {
                let from = focus.cardFrame(in: self)
                let id = focus.agentID
                focus.close()
                focus.isHidden = true
                wall.scrollView.isHidden = false
                wall.updateFades()
                wall.renderingVisible = true
                if let id, let to = model.cardFrame?(id) { zoom(from: from, to: to, done: {}) }
            }
            if model.mode == .compose {
                DispatchQueue.main.async { [weak self] in self?.wall.focusEditingDraft() }
            } else if let id = model.activeTileID, let t = wall.tiles[id] {
                t.terminal.focusInput()
            } else if model.denyOpen == nil {
                window?.makeFirstResponder(wall)
            }
        case .focus:
            guard let a = model.agent(model.focusedID) else { model.exitFocus(); return }
            if entering, let from = model.cardFrame?(a.id) {
                // Tiles stop rendering at once (the zoom covers them).
                wall.renderingVisible = false
                focus.alphaValue = 0
                focus.isHidden = false
                focus.show(a, fontSize: userFontSize)
                let to = focus.cardFrame(in: self)
                zoom(from: from, to: to) { [weak self] in
                    guard let self, self.model.mode == .focus else { return }
                    self.hideWall()
                    self.focus.alphaValue = 1
                    self.focus.focusTerminal()
                }
            } else {
                hideWall()
                focus.alphaValue = 1
                focus.isHidden = false
                focus.show(a, fontSize: userFontSize)
            }
        }
    }

    private func hideWall() {
        wall.renderingVisible = false
        wall.scrollView.isHidden = true
        wall.leftFade.isHidden = true
        wall.rightFade.isHidden = true
    }

    /// Focus ⌘↩: the card grows to the window (and shrinks back): a card
    /// shape with the tile's look; Reduce Motion: a fade.
    private func zoom(from: NSRect, to: NSRect, done: @escaping @MainActor () -> Void) {
        zoomGhost?.removeFromSuperview()
        if WallView.reduceMotion || from.isEmpty || to.isEmpty {
            done()
            return
        }
        let g = NSView(frame: from)
        g.wantsLayer = true
        g.layer?.backgroundColor = Theme.tileBG.cg(in: self)
        g.layer?.cornerRadius = Metrics.radius
        g.layer?.borderWidth = 2
        g.layer?.borderColor = Theme.ns(.working, alpha: 0.7).cg(in: self)
        g.layer?.shadowOpacity = 1
        g.layer?.shadowColor = NSColor.black.withAlphaComponent(0.5).cgColor
        g.layer?.shadowRadius = 18
        let header = NSView(frame: NSRect(x: 0, y: 0, width: from.width, height: Metrics.wall.header))
        header.wantsLayer = true
        header.layer?.backgroundColor = Theme.headerBG.cg(in: self)
        header.autoresizingMask = [.width]
        g.addSubview(header)
        addSubview(g, positioned: .below, relativeTo: overlay)
        zoomGhost = g
        NSAnimationContext.runAnimationGroup { ctx in
            ctx.duration = 0.2
            ctx.timingFunction = CAMediaTimingFunction(name: .easeInEaseOut)
            g.animator().frame = to
        }
        // Timed, not the animation's completion (which an occluded window
        // may never deliver).
        DispatchQueue.main.asyncAfter(deadline: .now() + 0.2) {
            done()
            NSAnimationContext.runAnimationGroup { ctx in
                ctx.duration = 0.08
                g.animator().alphaValue = 0
            }
            DispatchQueue.main.asyncAfter(deadline: .now() + 0.1) { g.removeFromSuperview() }
        }
    }
}

@MainActor
final class MainWindowController: NSWindowController, NSWindowDelegate {
    let model: AppModel
    let root: RootView
    let toolbar: MainToolbar

    init(model: AppModel, userFontSize: Double) {
        self.model = model
        root = RootView(model: model)
        root.userFontSize = userFontSize
        toolbar = MainToolbar(model: model)
        let screen = NSScreen.main?.visibleFrame ?? NSRect(x: 0, y: 0, width: 1600, height: 1000)
        let w = MainWindow(contentRect: screen.insetBy(dx: screen.width * 0.04, dy: screen.height * 0.04),
                           styleMask: [.titled, .closable, .miniaturizable, .resizable, .fullSizeContentView], backing: .buffered, defer: false)
        w.model = model
        w.title = "Hesper"
        w.titlebarAppearsTransparent = true
        w.titleVisibility = .hidden
        w.backgroundColor = Theme.windowBG
        w.setFrameAutosaveName("HesperWall")
        w.contentView = root
        w.isReleasedWhenClosed = false
        w.tabbingMode = .disallowed
        super.init(window: w)
        w.delegate = self
        toolbar.install(in: w)
        root.toolbarAnchor = { [weak self] k in self?.toolbar.anchor(for: k) }
    }

    @available(*, unavailable)
    required init?(coder: NSCoder) { fatalError() }

    func windowDidResize(_ notification: Notification) {
        model.closePopover()
    }

    func window(_ window: NSWindow, willUseFullScreenPresentationOptions proposedOptions: NSApplication.PresentationOptions) -> NSApplication.PresentationOptions {
        FullScreenChrome.options(proposedOptions)
    }

    func windowShouldClose(_ sender: NSWindow) -> Bool {
        // Closing the window only hides the wall; agents live in the daemon.
        sender.orderOut(nil)
        return false
    }
}

/// Extra walls get the same full-screen chrome as the main wall.
extension WallWindowController {
    func window(_ window: NSWindow, willUseFullScreenPresentationOptions proposedOptions: NSApplication.PresentationOptions) -> NSApplication.PresentationOptions {
        FullScreenChrome.options(proposedOptions)
    }
}
