import AppKit
import HesperCore

/// App control (docs/rebuild-contract.md "As built — app control"): the
/// app.* requests hesperd forwards from hesperctl (`hesperctl open`,
/// `wall`, `desk`). Every action is the one the menus, keys and clicks
/// run (WindowManager, AppModel, DeskController); the params and names are
/// read by the pure rules in HesperCore/AppControl.swift. Changes persist
/// the usual way (desks.json, UserDefaults).
@MainActor
final class AppControl {
    unowned let manager: WindowManager
    private var desks: DeskController { manager.desks }
    private var primary: AppModel { manager.primary }

    init(manager: WindowManager) {
        self.manager = manager
        manager.primary.client.onAppRequest = { [weak self] method, params in
            await MainActor.run {
                guard let self else { return .failure(AppControlError("unavailable", "Hesper.app is quitting").rpc) }
                return self.handle(method, params)
            }
        }
    }

    func handle(_ method: String, _ params: JSONValue) -> Result<JSONValue, RPCError> {
        do {
            switch AppControlMethod(rawValue: method) {
            case .state: return .success(state())
            case .open: return .success(try open(AppOpenRequest.parse(params)))
            case .wallSet: return .success(try wallSet(WallSetRequest.parse(params)))
            case .desk: return .success(try desk(DeskRequest.parse(params)))
            case nil: throw AppControlError.notFound("no method \(method)")
            }
        } catch let e as AppControlError {
            return .failure(e.rpc)
        } catch {
            return .failure(AppControlError("invalid", "\(error)").rpc)
        }
    }

    // MARK: State

    func state() -> JSONValue {
        var focused: [String: JSONValue] = [:]
        if let k = NSApp.keyWindow ?? NSApp.mainWindow {
            if let w = manager.walls.first(where: { $0.window === k }) {
                focused["wall"] = .string(w.id)
                if w.model.mode == .focus, let id = w.model.focusedID { focused["agent"] = .string(id) }
            } else if let c = (k as? AgentWindow)?.controller {
                focused["agentWindow"] = .string(c.agentID)
                focused["agent"] = .string(c.agentID)
            }
        }
        let current = manager.activeWall.id
        return [
            "active": .bool(NSApp.isActive),
            "focused": .object(focused),
            "currentWall": .string(current),
            "walls": .array(manager.walls.enumerated().map { i, w in wallState(w, home: i == 0) }),
            "agentWindows": .array(manager.agentWindows.map { c in
                ["agent": .string(c.agentID), "title": .string(c.window?.title ?? ""), "key": .bool(c.window?.isKeyWindow == true)]
            }),
            "desks": deskList(),
            "arrangements": .array(WallArrangement.allCases.map { .string($0.rawValue) }),
            "groupings": .array(Grouping.allCases.map { .string($0.rawValue) }),
            "ownWalls": .array(OwnWallMode.allCases.map { .string($0.rawValue) }),
            "densities": ["dense", "normal"],
            "scopes": .string(ScopeSpec.help),
        ]
    }

    private func wallState(_ w: WallEntry, home: Bool) -> JSONValue {
        let m = w.model
        let v = m.resolvedView
        var s: [String: JSONValue] = [
            "id": .string(w.id),
            "title": .string(w.window.title),
            "isMain": .bool(w.isMain),
            "isHome": .bool(home),
            "open": .bool(manager.isOpen(w)),
            "visible": .bool(manager.isReallyVisible(w)),
            "scope": .string(ScopeSpec.format(w.scope)),
            "arrangement": .string(m.arrangement.rawValue),
            "grouping": .string(m.grouping.rawValue),
            "minChars": .number(Double(m.minChars)),
            "density": .string(Density.name(m.minChars)),
            "collapsed": .array(m.collapsedBands.sorted().map { .string($0) }),
            "bandOrder": .array(m.bandOrder.map { .string($0) }),
            "sidebar": .bool(m.sidebarVisible),
            "ownWalls": .string(m.ownWallMode.rawValue),
            "mode": .string(m.mode == .focus ? "focus" : m.mode == .compose ? "compose" : "wall"),
            "agents": .array(m.wall.map { .string($0.id) }),
            "bands": .array(v.showsBands ? v.bands.map { b in
                ["key": .string(b.key), "title": .string(b.displayTitle), "collapsed": .bool(m.isCollapsed(key: b.key)),
                 "agents": .number(Double(b.counted.count)), "pointer": .bool(b.pointer != nil)]
            } : []),
        ]
        if let name = manager.scopeName(w.scope) { s["scopeName"] = .string(name) }
        if m.mode == .focus, let id = m.focusedID { s["focusedAgent"] = .string(id) }
        if let id = m.selectedID { s["selectedAgent"] = .string(id) }
        return .object(s)
    }

    private func deskList() -> JSONValue {
        let book = desks.book, setup = desks.active.id, cur = desks.currentDesk?.id
        let ordered = book.desks(for: setup) + book.setups.sorted { $0.lastUsed > $1.lastUsed }.filter { $0.id != setup }.flatMap { book.desks(for: $0.id) }
        return .array(ordered.map { d in
            var o: [String: JSONValue] = [
                "id": .string(d.id), "name": .string(d.name), "current": .bool(d.id == cur), "automatic": .bool(d.automatic),
                "thisDisplays": .bool(d.setup == setup), "walls": .number(Double(d.windows.walls.count)),
                "agentWindows": .number(Double(d.windows.agentWindows.count)),
            ]
            if let s = book.setup(d.setup) { o["displays"] = .string(s.screens.map(\.name).joined(separator: " + ")) }
            return .object(o)
        })
    }

    // MARK: Open

    private func wall(_ ref: WallRef) throws -> WallEntry {
        let id = try ref.resolve(walls: manager.walls.map(\.id), current: manager.activeWall.id)
        guard let w = manager.walls.first(where: { $0.id == id }) else { throw AppControlError.notFound("no wall \(id)") }
        return w
    }

    private func scope(_ s: String) throws -> WallScope { try ScopeSpec.parse(s, catalog: primary.catalog) }

    func open(_ r: AppOpenRequest) throws -> JSONValue {
        switch r.target {
        case .agent(let ref, let mode):
            let id = try AgentRef.resolve(ref, agents: Array(primary.registry.agents.values))
            switch mode {
            case .focus: manager.open(id, focus: true) // notification click: its window, the wall showing it, or home
            case .wall: manager.showInWall(id) // ⌘↩ in an agent window: selected on the frontmost wall showing it
            case .window: manager.openAgentWindow(id) // ⇧-click
            case .tab: manager.openAgentWindow(id, asTab: true) // ⌥⇧-click
            }
            NSApp.activate(ignoringOtherApps: true)
            return ["agent": .string(id), "mode": .string(mode.rawValue)]

        case .wall(let ref, let scopeText, let newWall):
            let s = try scopeText.map(scope)
            let w: WallEntry
            if newWall {
                w = manager.newWall(scope: s) // ⌥⌘N
            } else if let ref {
                w = try wall(ref)
                if let s { manager.setScope(s, for: w) } // the sidebar's click
                manager.bringForward(w.window)
            } else if let s {
                // The sidebar's ⌥-click, unless a wall shows it already.
                if let existing = manager.walls.first(where: { $0.scope == s }) {
                    w = existing
                    manager.bringForward(w.window)
                } else {
                    w = manager.newWall(scope: s)
                }
            } else {
                w = manager.activeWall
                if !w.window.isVisible { w.window.orderFront(nil) }
                manager.bringForward(w.window)
            }
            if r.target.showsWall && w.model.mode == .focus { w.model.exitFocus() } // as ⌘Esc
            NSApp.activate(ignoringOtherApps: true)
            return wallState(w, home: manager.walls.first === w)

        case .composer(let p, let ref):
            let w = try wall(ref)
            let m = w.model
            var folder: String?, projectID: String?
            if let proj = p.project {
                if ProjectRef.isFolder(proj) {
                    folder = (ProjectRef.expand(proj) as NSString).standardizingPath
                } else if let id = ProjectRef.resolve(proj, catalog: m.catalog) {
                    projectID = id
                } else {
                    throw AppControlError.notFound("no project \(proj) (a folder starts with / or ~)")
                }
            }
            if let mach = p.machine, mach != m.localMachine, !m.machines.contains(where: { $0.short == mach }) {
                throw AppControlError.notFound("no machine \(mach)")
            }
            manager.bringForward(w.window)
            if m.mode == .focus { m.exitFocus() } // ⌘N in focus opens quick launch instead
            m.newDraft(project: folder, projectID: projectID, machine: p.machine, machineExplicit: p.machine != nil ? true : nil) // ⌘N
            guard let id = m.editingDraftID, var d = m.drafts[id] else { throw AppControlError("unavailable", "the wall could not open a draft") }
            if let t = p.task { d.text = t }
            if let prof = p.profile {
                d.profile = prof
            } else if let k = p.kind {
                d.profile = m.profiles?.defaultProfile(project: d.project, kind: k) ?? d.profile
            }
            if let wt = p.worktree { d.worktree = wt }
            if let b = p.branch { d.branch = b; if p.worktree == nil { d.worktree = true } }
            m.editDraftContent(d)
            m.onAgentsChanged?()
            NSApp.activate(ignoringOtherApps: true)
            return ["draft": .string(id), "wall": .string(w.id)]

        case .history(let query):
            let w = manager.activeWall
            guard w.model.historyAvailable else { throw AppControlError("unavailable", "History needs a hesperd with sessions.*") }
            manager.bringForward(w.window)
            HistoryPanelHost.open(w.model, text: query, select: nil) // ⌘Y (with the query, as ⌘K → History)
            return ["wall": .string(w.id), "query": query.map { .string($0) } ?? .null]

        case .inbox:
            let w = manager.activeWall
            manager.bringForward(w.window)
            let n = primary.counts.needingYou
            if w.model.popover != .attention { w.model.toggleAttentionQueue() } // ⌘J
            return ["wall": .string(w.id), "needsYou": .number(Double(n))]
        }
    }

    // MARK: Wall

    func wallSet(_ r: WallSetRequest) throws -> JSONValue {
        let w = try wall(r.wall)
        let m = w.model
        let newScope = try r.scope.map(scope)
        let before = settings(of: m), oldScope = w.scope
        if let s = newScope { manager.setScope(s, for: w) }
        let basics = before.applying(r)
        if r.arrangement != nil { m.mainPick = m.selectedID } // as ⌥⌘1…5
        apply(basics, to: m)
        do {
            // Bands as the new grouping / scope draws them.
            let full = try basics.applyingBands(r, bands: m.resolvedView.bands)
            apply(full, to: m)
        } catch {
            apply(before, to: m) // a bad band changes nothing
            manager.setScope(oldScope, for: w)
            throw error
        }
        if r.home { manager.makeHome(w) } // Window ▸ Make Home Wall
        manager.restorer.scheduleSave()
        return wallState(w, home: manager.walls.first === w)
    }

    private func settings(of m: AppModel) -> WallViewSettings {
        WallViewSettings(arrangement: m.arrangement, grouping: m.grouping, minChars: m.minChars, collapsed: m.collapsedBands,
                         bandOrder: m.bandOrder, sidebar: m.sidebarVisible, ownWalls: m.ownWallMode)
    }

    private func apply(_ s: WallViewSettings, to m: AppModel) {
        m.arrangement = s.arrangement
        m.grouping = s.grouping
        m.minChars = s.minChars
        m.collapsedBands = s.collapsed
        m.bandOrder = s.bandOrder
        m.sidebarVisible = s.sidebar
        m.ownWallMode = s.ownWalls
    }

    // MARK: Desks

    func desk(_ r: DeskRequest) throws -> JSONValue {
        func find() throws -> Desk {
            guard let d = DeskRequest.find(r.name ?? "", in: desks.book.desks, setup: desks.active.id) else {
                throw AppControlError.notFound("no desk \(r.name ?? "")")
            }
            return d
        }
        switch r.action {
        case .list:
            break
        case .save:
            desks.saveAs(r.name!) // Save Desk As…
        case .switch:
            desks.select(try find().id) // ⌃⌘D picker
        case .rename:
            desks.rename(try find().id, to: r.newName!)
        case .remove:
            let d = try find()
            guard !d.automatic else { throw AppControlError.invalid("\(d.name) is the automatic desk of its displays; it can't be removed") }
            desks.remove(d.id)
        }
        return deskList()
    }
}
