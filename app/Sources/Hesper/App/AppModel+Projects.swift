import AppKit
import HesperCore

/// What the project sidebar asks the window layer for.
enum SidebarAction {
    /// Click: this wall shows it.
    case scope(WallScope)
    /// ⌥-click: a new wall scoped to it.
    case newWall(WallScope)
    /// Dragged out of the window: a wall there (screen point, AppKit
    /// coordinates).
    case wallAt(WallScope, NSPoint)
}

/// The wall as a view (docs/rebuild-contract.md "As built — projects
/// (views)"): bands from the scope and grouping, collapse, band order,
/// drafts in bands, band navigation, project-first stepping.
extension AppModel {
    var catalog: ProjectCatalog { registry.catalog }

    /// The project of a card (agents: the daemon's projectId, else by
    /// folder; drafts: the band they were opened in — nil: the "New"
    /// area, whatever folder they chose since).
    func projectID(of item: WallItem) -> String? {
        switch item {
        case .agent(let a): return catalog.projectID(for: a)
        case .draft(let d): return d.band ?? draftBands[d.id]
        case .ghost(let s): return s.projectId ?? catalog.projectID(path: s.cwd, machine: s.machine) // shared history
        }
    }

    /// The project of a folder; a local folder also by its real path
    /// (hesperd records /private/var/…, a draft may say /var/…).
    func folderProjectID(_ path: String?, machine: String) -> String? {
        let id = catalog.projectID(path: path, machine: machine)
        guard let path, machine == localMachine, let id, catalog.projects[id] == nil,
              let real = realpath(path, nil) else { return id }
        defer { free(real) }
        let resolved = String(cString: real)
        guard resolved != path, let other = catalog.projectID(path: resolved, machine: machine), catalog.projects[other] != nil else { return id }
        return other
    }

    func viewItem(_ item: WallItem) -> ViewItem {
        switch item {
        case .agent(let a): return ViewItem(id: a.id, projectID: catalog.projectID(for: a), branch: a.branch, worktree: a.worktree)
        case .draft(let d): return ViewItem(id: d.id, projectID: projectID(of: item), isNew: (d.band ?? draftBands[d.id]) == nil)
        case .ghost(let s): return ViewItem(id: GhostCards.itemID(s), projectID: projectID(of: item), branch: s.branch) // shared history
        }
    }

    var groupLevel: GroupLevel { ViewGrouping.level(grouping, scope: scope, hasGroups: catalog.hasGroups) }

    func resolveView(_ items: [WallItem]) -> ResolvedView {
        // An agent window has no wall (its scope is empty): nothing to group.
        // Desks: projects / groups on their own wall as pointer lines.
        let ptrs = pointers.map { p in (pointer: p, items: p.agents.compactMap { agent($0) }.map { viewItem(.agent($0)) }) }
        var v = ViewResolver.resolve(items.map(viewItem), level: groupLevel, catalog: catalog, scope: scope,
                                     bandOrder: bandOrder, continued: continuedBands, pointers: ptrs)
        for i in v.bands.indices {
            guard let p = v.bands[i].pointer else { continue }
            v.bands[i].subtitle = "in “\(pointerTitle?(p.wall) ?? "its own")” window ↗"
        }
        return v
    }

    /// Collapsed bands and pointer lines are one header line.
    func isCollapsed(_ b: Band) -> Bool { b.pointer != nil || collapsedBands.contains(b.key) }

    /// The bands of this wall now.
    var resolvedView: ResolvedView { resolveView(scopedWallItems) }

    /// Cards in collapsed bands (not shown, skipped by Tab).
    var hiddenIDs: Set<String> {
        guard !collapsedBands.isEmpty else { return [] }
        let v = resolvedView
        guard v.showsBands else { return [] }
        return Set(v.bands.filter { collapsedBands.contains($0.key) }.flatMap(\.members))
    }

    // MARK: Band actions

    /// A card in a collapsed band becomes visible (⌘J, a click in the
    /// sidebar's attention, a needs-you card).
    func reveal(_ id: String) {
        guard !collapsedBands.isEmpty, let b = resolvedView.band(of: id), collapsedBands.contains(b.key) else { return }
        collapsedBands.remove(b.key)
    }

    /// Click: collapse / expand; ⌥-click: this one open, every other collapsed.
    func toggleCollapse(_ key: String, others: Bool = false) {
        if others {
            let keys = Set(resolvedView.bands.map(\.key))
            collapsedBands = keys.subtracting([key])
        } else if collapsedBands.contains(key) {
            collapsedBands.remove(key)
        } else {
            collapsedBands.insert(key)
            if let sel = selectedID, resolvedView.band(of: sel)?.key == key {
                activeTileID = nil
                selectedID = resolvedView.bands.first { !collapsedBands.contains($0.key) }?.members.first ?? selectedID
                onModeChanged?()
            }
        }
    }

    /// Header drag: the band goes to `index` in this wall's band order.
    func moveBand(_ key: String, to index: Int) {
        var keys = resolvedView.bands.map(\.key)
        guard let from = keys.firstIndex(of: key) else { return }
        keys.remove(at: from)
        keys.insert(key, at: max(0, min(index, keys.count)))
        // Keep the order of bands not on the wall now (they come back there).
        bandOrder = keys + bandOrder.filter { !keys.contains($0) }
    }

    /// ⌥↑ / ⌥↓: the first card of the previous / next open band.
    func jumpBand(_ d: Int) {
        let v = resolvedView
        guard v.showsBands else { return }
        let bands = v.bands.map { (key: $0.key, members: collapsedBands.contains($0.key) ? [] : $0.members) }
        guard let id = BandNavigation.target(bands: bands, current: selectedID, delta: d) else { return }
        if activeTileID != nil { activeTileID = nil }
        if editingDraftID != nil && editingDraftID != id { leaveComposer() }
        selectedID = id
        onModeChanged?()
    }

    /// ⌥⌘← ⌥⌘→ in a single view: the same project's agents first, then
    /// the next project's (projects in band order).
    func projectStepOrder(_ ids: [String]) -> [String] {
        var proj: [String: String] = [:]
        for id in ids { if let a = agent(id) { proj[id] = catalog.root(catalog.projectID(for: a)) } }
        var rank: [String] = []
        for id in ids { if let p = proj[id], !rank.contains(p) { rank.append(p) } }
        return ProjectStep.order(ids, projectOf: { proj[$0] }, projectRank: rank)
    }

    /// A tile's project: name and color (headers, D's tint).
    func projectLabel(_ a: Agent) -> (name: String, colorHex: String)? {
        guard let pid = catalog.projectID(for: a) else { return nil }
        return (catalog.name(project: pid), catalog.colorHex(project: pid))
    }

    /// Counts for a band header.
    func bandCounts(_ b: Band) -> (total: Int, active: Int, needsYou: Int, quiet: Int) {
        var t = 0, act = 0, need = 0, q = 0
        for id in b.counted {
            guard let a = agent(id) else { if draft(id) != nil { t += 1 }; continue }
            t += 1
            if a.state.needsAttention { need += 1 }
            if a.state == .done || a.state == .idle || a.state == .exited { q += 1 } else { act += 1 }
        }
        return (t, act, need, q)
    }

    // MARK: Project data (sidebar context menu)

    func renameProject(_ id: String, to name: String) {
        run("rename the project") { _ = try await self.client.updateProject(id, ["name": .string(name)]) }
    }

    func setProjectColor(_ id: String, _ hex: String) {
        run("change the color") { _ = try await self.client.updateProject(id, ["color": .string(hex)]) }
    }

    func setProjectDefaults(_ id: String, profile: String?, machine: String?) {
        var d: [String: JSONValue] = [:]
        let cur = catalog.project(id)?.defaults
        if let p = profile ?? cur?.profile { d["profile"] = .string(p) }
        if let m = machine ?? cur?.machine { d["machine"] = .string(m) }
        run("change the defaults") { _ = try await self.client.updateProject(id, ["defaults": .object(d)]) }
    }

    func promoteProject(_ id: String, name: String? = nil) {
        guard let p = catalog.project(id) else { return }
        let machine = p.paths.keys.sorted().first { $0 != "*" } ?? localMachine
        guard let path = p.synthesized ? p.paths["*"] : p.path(on: machine, local: localMachine) else { return }
        run("promote the folder") { _ = try await self.client.promoteProject(path: path, machine: machine, name: name) }
    }

    func removeProject(_ id: String) {
        run("remove the project") { try await self.client.removeProject(id) }
    }

    func saveGroup(_ g: ProjectGroup) {
        run("save the group") { _ = try await self.client.saveGroup(g) }
    }

    func removeGroup(_ id: String) {
        run("remove the group") { try await self.client.removeGroup(id) }
    }
}
