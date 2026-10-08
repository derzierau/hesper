import AppKit
import HesperCore

/// Takes a snapshot of every wall (scope, view, layout, card width, frame,
/// display, full screen; walls in desk order, home first) and every agent
/// window (agent, frame, display, tab group), and applies one: at launch
/// and when the desk changes (DeskController, desks.json). Frames are
/// clamped to the displays there are now. Agent windows come back once
/// the daemon listed its agents (removed agents' windows don't); until
/// then the pending ones are kept in the desk.
///
/// File (DeskController): `--window-state PATH`; otherwise, only when
/// talking to the live daemon (no --socket / --state-dir), ~/Library/
/// Application Support/Hesper/desks.json (windows.json migrated). Test and
/// fake-daemon runs never touch the user's files.
@MainActor
final class WindowRestorer {
    weak var manager: WindowManager?
    weak var desks: DeskController?
    /// Agent windows of the applied desk that wait for the agent list.
    private var pendingAgentWindows: [SavedAgentWindow]?
    private(set) var restoring = false
    private var savePending = false

    init(env: AppEnvironment) {}

    var url: URL? { desks?.url }

    // MARK: Screens

    static func clamped(_ f: SavedFrame, screen: String?, screens: [ScreenArea]) -> NSRect {
        let r = FrameClamp.clamp(f.rect, screen: screen, screens: screens)
        return NSRect(x: r.x, y: r.y, width: r.width, height: r.height)
    }

    private func clamped(_ f: SavedFrame, screen: String?) -> NSRect {
        Self.clamped(f, screen: screen, screens: desks?.areas ?? [])
    }

    private func saved(_ w: NSWindow) -> (SavedFrame, String?, Bool) {
        let full = w.styleMask.contains(.fullScreen)
        let f = w.frame
        return (SavedFrame(x: f.minX, y: f.minY, width: f.width, height: f.height), desks?.displayKey(for: f), full)
    }

    // MARK: Snapshot

    func snapshot() -> SavedWindows? {
        guard let m = manager else { return nil }
        var out = SavedWindows()
        for w in m.walls {
            let (f, s, full) = saved(w.window)
            let m = w.model
            out.walls.append(SavedWall(id: w.id, isMain: w.isMain, scope: w.scope, arrangement: m.arrangement,
                                       minChars: m.minChars, frame: f, screen: s, fullScreen: full,
                                       grouping: m.grouping == .auto ? nil : m.grouping,
                                       collapsed: m.collapsedBands.isEmpty ? nil : m.collapsedBands.sorted(),
                                       bandOrder: m.bandOrder.isEmpty ? nil : m.bandOrder,
                                       sidebar: m.sidebarVisible ? true : nil,
                                       ownWalls: m.ownWallMode == .collapsed ? nil : m.ownWallMode))
        }
        // Agent windows in window order, tab groups kept together in tab order.
        var seen = Set<ObjectIdentifier>()
        var group = 0
        let ordered = NSApp.orderedWindows.compactMap { ($0 as? AgentWindow)?.controller } + m.agentWindows
        for c in ordered {
            guard let w = c.window, !seen.contains(ObjectIdentifier(w)), m.agentWindow(for: c.agentID) === c else { continue }
            let tabs = (w.tabbedWindows ?? [w]).compactMap { ($0 as? AgentWindow)?.controller }
            let gid: Int? = tabs.count > 1 ? group : nil
            if gid != nil { group += 1 }
            let selected = w.tabGroup?.selectedWindow
            for t in tabs {
                guard let tw = t.window, !seen.contains(ObjectIdentifier(tw)) else { continue }
                seen.insert(ObjectIdentifier(tw))
                let (f, s, full) = saved(tw)
                out.agentWindows.append(SavedAgentWindow(agent: t.agentID, frame: f, screen: s, tabGroup: gid,
                                                         selectedTab: selected === tw, fullScreen: full))
            }
        }
        if let pending = pendingAgentWindows {
            let open = Set(out.agentWindows.map(\.agent))
            out.agentWindows += pending.filter { !open.contains($0.agent) }
        }
        return out
    }

    func scheduleSave() {
        guard desks?.url != nil, !restoring, !savePending else { return }
        savePending = true
        DispatchQueue.main.asyncAfter(deadline: .now() + 0.5) { [weak self] in
            self?.savePending = false
            self?.saveNow()
        }
    }

    /// Into the current desk, unless a desk is being applied (or the
    /// displays changed and the switch is still pending: DeskController).
    func saveNow() {
        guard !restoring, let s = snapshot() else { return }
        desks?.record(s)
    }

    /// The current desk's windows (tests).
    func load() -> SavedWindows? { desks?.currentWindows }

    // MARK: Restore

    /// At launch: the desk of this display setup (walls now; agent windows
    /// when the daemon listed its agents).
    func restoreWalls() {
        guard let s = desks?.launchWindows() else { return }
        apply(s, animate: false)
    }

    /// Makes the windows what the desk says: walls closed, reconfigured
    /// (same id) or opened, put in desk order (home first); agent windows
    /// closed, moved or opened, tab groups rebuilt. `animate`: window
    /// moves are animated (a desk switch).
    func apply(_ s: SavedWindows, animate: Bool) {
        guard let m = manager else { return }
        restoring = true
        defer { restoring = false }
        let wanted = Set(s.walls.map(\.id))
        for w in m.walls where !w.isMain && !wanted.contains(w.id) { w.window.close() }
        for sw in s.walls {
            let frame = clamped(sw.frame, screen: sw.screen)
            let w: WallEntry
            if sw.isMain {
                w = m.mainWall
            } else if let existing = m.walls.first(where: { $0.id == sw.id }) {
                w = existing
            } else {
                w = m.newWall(scope: sw.scope, frame: frame, id: sw.id, show: true)
            }
            w.scope = sw.scope
            w.model.arrangement = sw.arrangement
            w.model.minChars = sw.minChars
            Self.applyView(sw, to: w.model)
            place(w.window, frame: frame, fullScreen: sw.fullScreen, animate: animate)
            if sw.isMain && animate && !w.window.isVisible { w.window.orderFront(nil) }
        }
        m.orderWalls(s.walls.map(\.id))
        m.resyncWalls()
        pendingAgentWindows = s.agentWindows
        if m.primary.isConnected && !m.primary.registry.agents.isEmpty { agentsKnown(animate: animate) }
    }

    /// The daemon's agent list arrived (or a desk was applied): agent
    /// windows of the desk whose agents still exist, tab groups rebuilt
    /// in order; others closed.
    func agentsKnown(animate: Bool = false) {
        guard let m = manager, let pending = pendingAgentWindows else { return }
        pendingAgentWindows = nil
        let kept = SavedWindows(agentWindows: pending).keeping(agents: Set(m.primary.registry.agents.keys)).agentWindows
        let wanted = Set(kept.map(\.agent))
        let wasRestoring = restoring
        restoring = true
        defer { restoring = wasRestoring }
        for c in m.agentWindows where !wanted.contains(c.agentID) { c.window?.close() }
        // Existing windows leave tab groups that the desk doesn't have.
        var target: [String: Int] = [:]
        for sw in kept { if let g = sw.tabGroup { target[sw.agent] = g } }
        for sw in kept {
            guard let w = m.agentWindow(for: sw.agent)?.window, let tabs = w.tabbedWindows, tabs.count > 1 else { continue }
            let mates = Set(tabs.compactMap { ($0 as? AgentWindow)?.controller?.agentID })
            let want = Set(kept.filter { $0.tabGroup != nil && $0.tabGroup == target[sw.agent] }.map(\.agent))
            if mates != want { w.tabGroup?.removeWindow(w) }
        }
        let key = NSApp.keyWindow
        var groupHost: [Int: NSWindow] = [:]
        var selected: [NSWindow] = []
        var fulls: [NSWindow] = []
        for sw in kept {
            let frame = clamped(sw.frame, screen: sw.screen)
            let existing = m.agentWindow(for: sw.agent)
            guard let c = existing ?? m.openAgentWindow(sw.agent, frame: frame, activate: false, ownsSize: false), let w = c.window else { continue }
            if let g = sw.tabGroup, let host = groupHost[g] {
                if host.tabbedWindows?.contains(w) != true { host.addTabbedWindow(w, ordered: .above) }
            } else {
                if let g = sw.tabGroup { groupHost[g] = w }
                if existing != nil { place(w, frame: frame, fullScreen: false, animate: animate) }
                w.orderFront(nil)
            }
            if sw.selectedTab { selected.append(w) }
            if sw.fullScreen { fulls.append(w) }
        }
        for w in selected { w.orderFront(nil) }
        key?.makeKeyAndOrderFront(nil)
        for w in fulls { fullScreenLater(w) }
        m.ownership.schedule()
        DispatchQueue.main.async { [weak self] in self?.scheduleSave() }
    }

    /// The wall's view (projects): grouping, collapsed bands, band order,
    /// sidebar; desks: own walls.
    static func applyView(_ sw: SavedWall, to m: AppModel) {
        m.grouping = sw.grouping ?? .auto
        m.collapsedBands = Set(sw.collapsed ?? [])
        m.bandOrder = sw.bandOrder ?? []
        m.sidebarVisible = sw.sidebar ?? false
        m.ownWallMode = sw.ownWalls ?? .collapsed
    }

    /// Moves a window (animated on a desk switch); full screen in or out
    /// as the desk says (a full-screen window leaves full screen first and
    /// is moved after).
    private func place(_ w: NSWindow, frame: NSRect, fullScreen: Bool, animate: Bool) {
        let isFull = w.styleMask.contains(.fullScreen)
        if isFull && !fullScreen {
            w.toggleFullScreen(nil)
            DispatchQueue.main.asyncAfter(deadline: .now() + 0.9) { w.setFrame(frame, display: true, animate: false) }
            return
        }
        if isFull { return }
        if w.frame != frame {
            if animate && w.isVisible && !NSWorkspace.shared.accessibilityDisplayShouldReduceMotion {
                NSAnimationContext.runAnimationGroup { ctx in
                    ctx.duration = 0.3
                    ctx.timingFunction = CAMediaTimingFunction(name: .easeInEaseOut)
                    w.animator().setFrame(frame, display: true)
                }
            } else {
                w.setFrame(frame, display: false)
            }
        }
        if fullScreen { fullScreenLater(w) }
    }

    private func fullScreenLater(_ w: NSWindow) {
        DispatchQueue.main.asyncAfter(deadline: .now() + 0.5) {
            if !w.styleMask.contains(.fullScreen) { w.toggleFullScreen(nil) }
        }
    }
}
