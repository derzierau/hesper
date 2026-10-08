import AppKit
import HesperCore
import SwiftUI

/// One agent in its own window. The content is the app's own RootView held
/// in focus mode on the window's AppModel (an empty wall scope): the same
/// focus view and terminal (rw, the PTY's size owner while this window is
/// key), attention band, overlays (⌘K palette, move / rename popovers,
/// deny, undo toasts) and keys as the wall's ⌘↩ view. Title = agent name,
/// subtitle = project · machine · kind; a square stop in the title bar
/// mirrors the agent's state. Closing the window (⌥⌘W, the close button)
/// never stops the agent; ⌘W closes the agent with it.
@MainActor
final class AgentWindowController: NSWindowController, NSWindowDelegate {
    /// The agent shown; changes only by `retarget` (⌥⌘← ⌥⌘→ untabbed).
    private(set) var agentID: String
    let model: AppModel
    let root: RootView
    weak var manager: WindowManager?
    static let tabbingID = "hesper.agent"
    /// Agent windows tab together per project ("hesper.agent.<projectId>"):
    /// Merge All Windows gathers one project's agents.
    static func tabbingID(project: String?) -> String { project.map { "\(tabbingID).\($0)" } ?? tabbingID }
    /// A new agent window's content size, and the smallest it gets.
    static let defaultSize = NSSize(width: 980, height: 680)
    static let minSize = NSSize(width: 420, height: 260)
    private var rootModeChanged: (() -> Void)?
    private var rootAgentsChanged: (() -> Void)?

    init(agent: Agent, model: AppModel, manager: WindowManager, fontSize: Double, ownsSize: Bool) {
        agentID = agent.id
        self.model = model
        self.manager = manager
        let id = agent.id
        model.wallScope = { _ in [] }   // no wall tiles behind the focus view
        model.draftsShown = { _ in [] } // no wall: ⌘N opens quick launch
        root = RootView(model: model)
        root.userFontSize = fontSize
        let w = AgentWindow(contentRect: NSRect(origin: .zero, size: Self.defaultSize),
                            styleMask: [.titled, .closable, .miniaturizable, .resizable], backing: .buffered, defer: false)
        w.model = model
        w.titlebarAppearsTransparent = true
        w.backgroundColor = Theme.windowBG
        w.contentView = root
        w.isReleasedWhenClosed = false
        w.tabbingIdentifier = Self.tabbingID(project: model.catalog.root(model.catalog.projectID(for: agent)))
        w.tabbingMode = .automatic
        w.collectionBehavior.insert(.fullScreenPrimary)
        w.minSize = Self.minSize
        w.setAccessibilityIdentifier("agentWindow.\(id)")
        super.init(window: w)
        w.addTitlebarAccessoryViewController(stop)
        w.controller = self
        w.delegate = self
        // RootView drives the focus view; the window layer keeps it on this
        // agent and routes everything that would leave it.
        rootModeChanged = model.onModeChanged
        rootAgentsChanged = model.onAgentsChanged
        model.onModeChanged = { [weak self] in self?.modeChanged() }
        model.onAgentsChanged = { [weak self] in self?.agentsChanged() }
        model.focusedID = id
        model.selectedID = id
        model.mode = .focus
        // ⌘W: once the agent is closed, so is its window.
        model.closeInFocus = { [weak self] _ in self?.window?.close() }
        root.focus.ownsSizeDefault = ownsSize
        rootModeChanged?()
        apply(agent)
    }

    @available(*, unavailable)
    required init?(coder: NSCoder) { fatalError() }

    var agent: Agent? { model.agent(agentID) }
    var terminal: AgentTerminal? { root.focus.terminal }

    private func agentsChanged() {
        guard let a = agent else {
            window?.close() // removed from the daemon: nothing to show
            return
        }
        rootAgentsChanged?()
        apply(a)
    }

    func apply(_ a: Agent) {
        guard let w = window else { return }
        // The title is the agent's name; the subtitle says where it is
        // ("acme-apps · mini · Claude"); the title bar's square stop mirrors
        // its state (no tinted title bar: only "needs you" is Signal).
        if w.title != a.name { w.title = a.name }
        let pid = model.catalog.projectID(for: a)
        let project = pid.map { model.catalog.name(project: $0) }
        let tab = Self.tabbingID(project: model.catalog.root(pid))
        if w.tabbingIdentifier != tab { w.tabbingIdentifier = tab }
        projectColor = pid.map { model.catalog.colorHex(project: $0) }
        w.subtitle = Self.subtitle(a, project: project)
        w.tab.title = a.name
        stop.kind = StateMarkKind(a.state)
    }

    /// "project · machine · tool", then what the agent needs, if anything.
    static func subtitle(_ a: Agent, project: String?) -> String {
        var parts = [project, a.machine, Theme.kindLabel(a.kind)].compactMap { $0 }
        switch a.state {
        case .approval: parts.append("needs approval")
        case .question: parts.append("asks a question")
        case .error: parts.append("error")
        case .exited: parts.append("exited")
        default: break
        }
        return parts.joined(separator: " · ")
    }

    /// The project's color (tab grouping, tests).
    private(set) var projectColor: String?

    /// The title bar's full stop: the agent's state.
    let stop = TitleStopAccessory()

    /// The model left focus on this agent: ⌘↩ / ⌘Esc show it in a wall;
    /// the palette, ⌘[ ⌘] or ⌘J picked another agent (its own window).
    private func modeChanged() {
        let target = model.mode == .focus ? model.focusedID : nil
        if model.mode == .focus && target == agentID { rootModeChanged?(); return }
        model.focusedID = agentID
        model.selectedID = agentID
        model.activeTileID = nil
        model.mode = .focus
        rootModeChanged?()
        guard agent != nil else { window?.close(); return }
        if let target, target != agentID {
            manager?.openAgentWindow(target)
        } else if target == nil {
            manager?.showInWall(agentID)
        }
    }

    /// Shows another agent in this window (the WindowManager keeps the
    /// one-window-per-agent book): a new focus terminal, title, tint.
    func retarget(to id: String) {
        guard let a = model.agent(id), id != agentID else { return }
        agentID = id
        root.focus.ownsSizeDefault = window?.isKeyWindow ?? true
        model.focus(id) // → modeChanged: target == agentID → the focus view shows it
        apply(a)
    }

    func windowWillClose(_ notification: Notification) {
        model.closePopover()
        root.focus.close()
        manager?.agentWindowClosed(self)
    }

    func windowDidBecomeKey(_ notification: Notification) {
        root.focus.focusTerminal()
    }

    func windowDidResize(_ notification: Notification) {
        model.closePopover()
    }
}

/// The wall window's key routing, with the agent window's own meanings:
/// ⌘W closes the agent and the window (the same rules as on the wall),
/// ⌥⌘W closes only the window (the agent stays on its wall), ⌥⌘← ⌥⌘→ (⌘[ ⌘]) step
/// to the previous / next agent within this window, ⌘N starts a new agent
/// on the frontmost wall.
final class AgentWindow: MainWindow {
    weak var controller: AgentWindowController?

    override func performKeyEquivalent(with event: NSEvent) -> Bool {
        guard let c = controller, event.type == .keyDown, let model,
              !model.showPalette, model.popover == nil, attachedSheet == nil else { return super.performKeyEquivalent(with: event) }
        let cmd = KeyRouter.route(KeyChord(event: event), mode: .focus, selectedState: model.current?.state)
        switch cmd {
        case .closeAgent:
            model.perform(.closeAgent) // closes the agent (same rules), then this window (closeInFocus)
        case .backgroundAgent:
            closeKeepingAgent()
        case .stepPrevious:
            step(-1)
        case .stepNext:
            step(1)
        case .newAgent:
            c.model.perform(.newAgent) // quick launch, preset to this agent's project and machine
        default:
            return super.performKeyEquivalent(with: event)
        }
        return true
    }

    /// ⌥⌘W: the window closes, the agent stays, shown on its wall.
    func closeKeepingAgent() {
        let id = controller?.agentID
        let manager = controller?.manager
        performClose(nil)
        if let id, manager?.primary.agent(id) != nil { manager?.showInWall(id) }
    }

    /// Previous / next agent within this window. Tabbed: the previous /
    /// next tab (wrapping, as in a browser) — each tab keeps its agent.
    /// Untabbed: the window shows the previous / next agent in wall order
    /// that has no window of its own (one window per agent); a beep when
    /// every other agent has one.
    func step(_ d: Int) {
        if (tabbedWindows?.count ?? 0) > 1 {
            if d > 0 { selectNextTab(nil) } else { selectPreviousTab(nil) }
            return
        }
        guard let c = controller else { NSSound.beep(); return }
        c.root.focus.nextSlide = d > 0 ? 1 : -1 // the next agent slides in from that side
        let stepped = c.manager?.step(c, by: d) == true
        c.root.focus.nextSlide = nil // used (or not) by the retarget
        if !stepped { NSSound.beep() }
    }
}
