import AppKit
import HesperCore
import Observation

/// Shared history's hooks into the wall model (docs "As built — shared
/// history (app)"): ⌘Y, ghost cards among the wall's items, the ⌘K
/// "History" section. The marked integration points in AppModel,
/// AppModel+Overlays, WallView and MainWindow call these.
extension AppModel {
    var history: HistoryHub { HistoryHub.shared(for: self) }
    /// hesperd has sessions.* (History shows; else it's hidden).
    var historyAvailable: Bool { history.supported == true }

    /// ⌘Y / the menu: open or close History on this window. From ⌘K it
    /// is the same search surface switching scope: the query comes along.
    func toggleHistory(scope: HistoryScope? = nil) {
        if mode == .compose { leaveComposer() }
        if showPalette && paletteScope == nil && scope == nil {
            return switchSearchScope(to: .history, text: paletteList.query)
        }
        HistoryPanelHost.toggle(self, scope: scope)
    }

    /// The search surface's scope switch (⇥, the scope Pills): ⌘K "All"
    /// ↔ ⌘Y "History", carrying the query.
    func switchSearchScope(to target: SearchScope, text: String, select: Session? = nil) {
        switch target {
        case .history:
            guard historyAvailable else { NSSound.beep(); return }
            showPalette = false
            HistoryPanelHost.open(self, text: text, select: select)
        case .all:
            if HistoryPanelHost.isOpen(self) { HistoryPanelHost.panel(for: self)?.close() }
            closePopover()
            paletteScope = nil
            paletteList = OverlayList(query: text)
            showPalette = true
        }
    }

    // MARK: Ghost cards

    /// The wall's items plus ghost cards (Grid + Shelf only, Settings ›
    /// Agents "Show closed agents as ghost cards" on): an agent being
    /// closed becomes its ghost in place; removed agents of the last day follow at the end (their
    /// band's shelf).
    func withGhosts(_ items: [WallItem]) -> [WallItem] {
        guard arrangement == .shelf, settings.ghostCards else { return items }
        let hub = history
        guard hub.supported == true else { return items } // an older hesperd: no ghosts (nothing could resume them)
        var out = items
        var present = Set(items.map(\.id))
        let daemon = Dictionary(hub.ghosts.map { (GhostCards.itemID($0), $0) }, uniquingKeysWith: { a, _ in a })
        if !pendingRemoval.isEmpty {
            let full = registry.wallOrder
            for (i, a) in full.enumerated() where pendingRemoval.contains(a.id) {
                var g = GhostCards.pending(from: a)
                let gid = GhostCards.itemID(g)
                guard !hub.dismissed.contains(gid), !present.contains(gid) else { continue }
                if let d = daemon[gid] { g = d }
                // Right after the agent that was before it (its slot).
                let anchor = full[..<i].reversed().first { present.contains($0.id) }?.id
                let at = anchor.flatMap { id in out.firstIndex { $0.id == id } }.map { $0 + 1 } ?? out.count
                out.insert(.ghost(g), at: at)
                present.insert(gid)
            }
        }
        for g in hub.ghosts where !present.contains(GhostCards.itemID(g)) {
            if let id = g.liveAgentID, registry.agents[id] != nil { continue }
            out.append(.ghost(g))
            present.insert(GhostCards.itemID(g))
        }
        return out
    }

    /// Whether a ghost belongs on this wall (scoped like its project).
    func ghostInScope(_ s: Session) -> Bool {
        let pid = s.projectId ?? catalog.projectID(path: s.cwd, machine: s.machine)
        switch scope {
        case .all, .overflow: return true
        case .project(let p): return catalog.lineage(pid).contains(p)
        case .group(let g): return catalog.groupIDs(of: pid).contains(g)
        default: return false
        }
    }

    var selectedGhost: Session? {
        guard let id = selectedID, id.hasPrefix("ghost:") else { return nil }
        for case .ghost(let s) in wallItems where GhostCards.itemID(s) == id { return s }
        return nil
    }

    /// ⏎ / ⌫ on a selected ghost card (WallView.keyDown); true: handled.
    func ghostKey(_ chord: KeyChord, keyCode: UInt16) -> Bool {
        guard mode == .wall, let g = selectedGhost, chord.plain else { return false }
        if chord.key == .enter { resumeGhost(g); return true }
        if keyCode == 51 || keyCode == 117 { forgetGhost(g); return true }
        return false
    }

    func resumeGhost(_ g: Session) {
        // Still closing (in its undo window): undo the close.
        let gid = GhostCards.itemID(g)
        for e in undo.entries {
            if case .reopen(let rs) = e.action, rs.count == 1, let a = agent(rs[0].agentID), GhostCards.itemID(GhostCards.pending(from: a)) == gid {
                undo(e.id)
                return
            }
        }
        HistoryActions(model: self, hub: history).resume(g, here: false, changes: nil)
    }

    func forgetGhost(_ g: Session) {
        let gid = GhostCards.itemID(g)
        let items = wallItems
        let i = items.firstIndex { $0.id == gid }
        history.forgetGhost(gid)
        HistoryPanelHost.wallsChanged()
        if selectedID == gid {
            let rest = wallItems
            selectedID = rest.isEmpty ? nil : rest[min(i ?? 0, rest.count - 1)].id
            onModeChanged?()
        }
    }

    // MARK: ⌘K

    /// The palette's "History" section: sessions matching the query
    /// (titles and full text), from a debounced search off the main
    /// thread; typing never waits for it.
    func historyPaletteItems() -> [OverlayItem] {
        guard historyAvailable, paletteScope == nil, HistoryPaletteSource.enabled else { return [] }
        let source = HistoryPaletteSource.shared(for: self)
        let q = paletteList.query.trimmingCharacters(in: .whitespaces)
        source.want(q, model: self)
        guard !q.isEmpty, source.query == q else { return [] }
        let names = MachineLabel.names(machines)
        return source.results.prefix(8).map { s in
            let quote = s.snippet.map { SessionFormat.highlights(SessionFormat.oneLine($0, max: 90)).text } ?? SessionFormat.meta(s, machines: names)
            return OverlayItem(id: "history:\(s.id)", section: "History", title: s.title.isEmpty ? "(untitled)" : SessionFormat.oneLine(s.title, max: 80),
                               detail: "\(SessionFormat.kindLabel(s.kind)) · \(SessionFormat.when(s.lastActivity)) · \(quote)",
                               dot: s.isLive ? Theme.Token.done.hex : Theme.Token.dim.hex,
                               run: { [weak self] in
                                   guard let self else { return }
                                   self.showPalette = false
                                   HistoryActions(model: self, hub: self.history).resume(s, here: false, changes: nil)
                               },
                               alternate: { [weak self] in
                                   guard let self else { return }
                                   self.switchSearchScope(to: .history, text: self.paletteList.query, select: s)
                               })
        }
    }

    /// ⇥ in ⌘K (not on an agent, whose ⇥ shows its actions): the History
    /// scope with the query; on a History row, that session selected in
    /// the preview. true: handled.
    func paletteTab(selectedID id: String?) -> Bool {
        guard paletteScope == nil, historyAvailable else { return false }
        if let id, id.hasPrefix("agent:") { return false }
        var session: Session?
        if let id, id.hasPrefix("history:") {
            let sid = String(id.dropFirst(8))
            session = HistoryPaletteSource.shared(for: self).results.first { $0.id == sid }
        }
        switchSearchScope(to: .history, text: paletteList.query, select: session)
        return true
    }

    /// ⇥ on a History row in the palette: its card in the panel.
    func showHistoryCard(paletteItemID id: String) -> Bool {
        guard id.hasPrefix("history:") else { return false }
        return paletteTab(selectedID: id)
    }
}

/// ⌘K's History results: one debounced (60 ms) search per query, decoded
/// off the main thread; @Observable so the palette redraws when they land.
@MainActor
@Observable
final class HistoryPaletteSource {
    private static var sources: [ObjectIdentifier: HistoryPaletteSource] = [:]
    static func shared(for m: AppModel) -> HistoryPaletteSource {
        let k = ObjectIdentifier(m)
        if let s = sources[k] { return s }
        let s = HistoryPaletteSource()
        sources[k] = s
        return s
    }

    /// Perf baselines turn the section off.
    @ObservationIgnored nonisolated(unsafe) static var enabled = true
    private(set) var query = ""
    private(set) var results: [Session] = []
    @ObservationIgnored private var wanted = ""
    @ObservationIgnored private var work: DispatchWorkItem?
    @ObservationIgnored private var generation = 0
    /// Query → results on screen (perf).
    @ObservationIgnored private(set) var lastLatencyMs: Double?

    func want(_ q: String, model: AppModel) {
        guard q != wanted else { return }
        wanted = q
        work?.cancel()
        guard !q.isEmpty else { return }
        let client = model.client
        let w = DispatchWorkItem { [weak self] in
            MainActor.assumeIsolated {
                guard let self else { return }
                self.generation += 1
                let gen = self.generation
                let t0 = CACurrentMediaTime()
                Task { @MainActor [weak self] in
                    let page = await Task.detached { try? await client.searchSessions(HistoryQuery(text: q, limit: 8)) }.value
                    guard let self, gen == self.generation else { return }
                    self.results = page?.items ?? []
                    self.query = q
                    self.lastLatencyMs = (CACurrentMediaTime() - t0) * 1000
                }
            }
        }
        work = w
        // Never inside the palette's body evaluation.
        DispatchQueue.main.asyncAfter(deadline: .now() + 0.06, execute: w)
    }
}
