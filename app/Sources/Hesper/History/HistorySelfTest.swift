import AppKit
import HesperCore

/// Shared history UI test (`make -C app test-ui-history`,
/// Tools/run-history.sh): the real app against the fake daemon with
/// `--projects demo --sessions 600 --indexing-ms 3000`. Drives ⌘Y, the
/// filter Pills (project, Mac, tool, age, more), search, the scope switch
/// (⇥ ⌘K ↔ ⌘Y), the preview and every action (checking which
/// sessions.* the fake saw), a live session opening its agent, ghost
/// cards, ⌘K's History section; phase "nosessions" (an older daemon):
/// History stays hidden. Screenshots with `--history-shots DIR`.
@MainActor
final class HistorySelfTest {
    let manager: WindowManager
    let out: String
    let shots: String?
    let phase: String?
    var checks: [SelfTest.Check] = []
    private var shotFiles: [String] = []
    private var model: AppModel { manager.primary }
    private var main: WallEntry { manager.walls[0] }
    private var window: NSWindow { main.window }
    private var panel: HistoryPanel? { HistoryPanelHost.panel(for: model) }
    private let pasteboard = NSPasteboard(name: NSPasteboard.Name("hesper.historytest.\(ProcessInfo.processInfo.processIdentifier)"))

    struct Result: Encodable { var passed: Int; var failed: Int; var checks: [SelfTest.Check]; var shots: [String] }

    init(manager: WindowManager, out: String, env: AppEnvironment) {
        self.manager = manager
        self.out = out
        shots = env.values["history-shots"]
        phase = env.values["history-phase"]
        Task { await run() }
    }

    private func check(_ name: String, _ ok: Bool, _ detail: String = "") {
        checks.append(SelfTest.Check(name: name, ok: ok, detail: detail))
        print(ok ? "ok  " : "FAIL", name, detail)
    }

    private func calls(clear: Bool = true) async -> [(method: String, params: JSONValue)] {
        guard let v = try? await model.client.call("fake.sessionCalls", .object(["clear": .bool(clear)])) else { return [] }
        return (v.arrayValue ?? []).map { ($0["method"]?.stringValue ?? "", $0["params"] ?? .null) }
    }

    private func key(_ chars: String, _ code: UInt16, _ flags: NSEvent.ModifierFlags = []) {
        Harness.press(chars, keyCode: code, flags: flags, window: window)
    }

    private func makeKey() async {
        for _ in 0..<3 {
            NSApp.activate(ignoringOtherApps: true)
            window.makeKeyAndOrderFront(nil)
            if await Harness.wait(1, { self.window.isKeyWindow }) { return }
        }
    }

    private func typeInSearch(_ s: String) {
        guard let p = panel else { return }
        window.makeFirstResponder(p.searchField)
        for c in s { key(String(c), 0) }
    }

    /// ⌘Y when History is closed (a resume closed it); then its rows.
    private func openHistory() async {
        if panel?.isOpen != true { key("y", 16, .command) }
        _ = await Harness.wait(3) { self.panel?.isOpen == true }
        _ = await settled()
    }

    private func clearSearch() {
        guard let p = panel else { return }
        p.searchField.stringValue = ""
        p.controlTextDidChange(Notification(name: NSControl.textDidChangeNotification))
    }

    private func settled(_ timeout: Double = 3) async -> Bool {
        guard let p = panel else { return false }
        let n = p.searches
        _ = n
        return await Harness.wait(timeout) { p.list.loaded && p.rows.count == p.list.items.count }
    }

    private func reloaded(after: () -> Void) async -> Bool {
        guard let p = panel else { return false }
        var fired = false
        p.onRowsShown = { fired = true }
        after()
        let ok = await Harness.wait(3) { fired }
        p.onRowsShown = nil
        return ok
    }

    private func run() async {
        let connected = await Harness.wait(20) { self.model.isConnected && self.model.wall.count >= (self.phase == "nosessions" ? 3 : self.phase == "real" ? 0 : 6) }
        check("connects", connected, "\(model.wall.count) agents")
        guard connected else { return finish() }
        await makeKey()
        if phase == "nosessions" { return await noSessions() }
        if phase == "real" { return await realDaemon() }

        let supported = await Harness.wait(5) { self.model.history.supported == true }
        check("the daemon has sessions.*: History is available", supported)
        let item = NSApp.mainMenu?.items.compactMap(\.submenu).flatMap(\.items).first { $0.title == "History" }
        check("the History menu item shows (⌘Y)", item != nil && item?.isHidden == false && item?.keyEquivalent == "y")
        await Harness.sleep(1.5)

        // 1. ⌘Y opens the panel: rows, the search field focused, indexing.
        key("y", 16, .command)
        let opened = await Harness.wait(3) { self.panel?.isOpen == true && (self.panel?.rows.count ?? 0) > 0 }
        guard opened, let p = panel else { check("⌘Y opens History with sessions", false); return finish() }
        check("⌘Y opens History with sessions", true, "\(p.rows.count) rows in \(String(format: "%.1f", p.openToFirstRowsMs ?? -1)) ms")
        check("the search field has the keyboard", (window.firstResponder as? NSTextView)?.delegate as? NSTextField === p.searchField)
        check("the first index shows its progress (sessions.indexing)", model.history.sawIndexing,
              model.history.indexing.map { "\($0.done)/\($0.total)" } ?? "finished")
        check("opens in this project (the selected card's): \(p.query.scope)", p.query.scope != .all)
        check("the wall underneath keeps its tiles (no mode change, nothing re-attached)", model.mode == .wall && main.root.wall.tiles.count == model.wall.count)
        p.perform(.first)
        await Harness.sleep(0.5)
        shot("history-panel")

        // 2. The project filter: counts per project, Scratch, Elsewhere; choosing one filters.
        let side = p.projectRows
        check("project filter: All, projects with session counts, Scratch, Elsewhere",
              side.first?.id == "all" && side.contains { $0.projectID == "p-hesper" && $0.count > 0 }
                && side.contains { $0.id == HistorySidebar.scratchRow && $0.count > 0 } && side.contains { $0.id == HistorySidebar.elsewhereRow && $0.count > 0 },
              side.map { "\($0.title) \($0.count)" }.joined(separator: ", "))
        if let r = p.projectRow("p:p-edition") ?? p.projectRows.first(where: { $0.projectID == "p-edition" }).flatMap({ p.projectRow($0.id) }) {
            _ = await reloaded { r.press() }
            check("a project row filters the list", p.query.scope == .project("p-edition") && !p.list.items.isEmpty
                  && p.list.items.allSatisfy { $0.projectId == "p-edition" }, "\(p.list.items.count)")
        }
        if let r = p.projectRow(HistorySidebar.elsewhereRow) {
            _ = await reloaded { r.press() }
            check("Elsewhere: sessions whose folder is gone", !p.list.items.isEmpty && p.list.items.allSatisfy { $0.projectId == nil })
        }
        if let r = p.projectRow(HistorySidebar.scratchRow) {
            _ = await reloaded { r.press() }
            check("Scratch: sessions in scratch folders", !p.list.items.isEmpty && p.list.items.allSatisfy { $0.projectId?.hasPrefix("scratch:") == true })
        }
        if let r = p.projectRow("all") { _ = await reloaded { r.press() } }
        check("All: every project", p.query.scope == .all && Set(p.list.items.compactMap(\.projectId)).count > 2)

        // 3. Filters (the old chip ids: tool, Mac, more, age).
        if let c = p.chip("kind:codex") { _ = await reloaded { c.press() } }
        check("Codex off: only Claude", !p.list.items.isEmpty && p.list.items.allSatisfy { $0.kind == "claude" })
        if let c = p.chip("kind:codex") { _ = await reloaded { c.press() } }
        check("Codex on again: both kinds", Set(p.list.items.map(\.kind)) == ["claude", "codex"])
        if let c = p.chip("mac:M") { _ = await reloaded { c.press() } }
        check("the mini chip: only the mini's sessions", !p.list.items.isEmpty && p.list.items.allSatisfy { $0.machine == "M" })
        if let c = p.chip("mac:M") { _ = await reloaded { c.press() } }
        if let c = p.chip("live") { _ = await reloaded { c.press() } }
        check("live in Hesper: sessions with an agent", !p.list.items.isEmpty && p.list.items.allSatisfy { $0.liveAgentID != nil }, "\(p.list.items.count)")
        if let c = p.chip("live") { _ = await reloaded { c.press() } }
        if let c = p.chip("external") { _ = await reloaded { c.press() } }
        check("external: started outside Hesper", !p.list.items.isEmpty && p.list.items.allSatisfy(\.external))
        if let c = p.chip("external") { _ = await reloaded { c.press() } }
        if let c = p.chip("archived") { _ = await reloaded { c.press() } }
        check("archived: only archived", !p.list.items.isEmpty && p.list.items.allSatisfy(\.archived))
        if let c = p.chip("archived") { _ = await reloaded { c.press() } }
        let hiddenMoved = !p.list.items.contains { $0.movedTo != nil }
        if let c = p.chip("moved") { _ = await reloaded { c.press() } }
        check("moved sessions only with the moved chip; shown as moved", hiddenMoved && p.list.items.contains { $0.movedTo != nil }
              && p.rows.contains { $0.state.hasPrefix("moved to") })
        if let c = p.chip("moved") { _ = await reloaded { c.press() } }
        if let c = p.chip("time") { _ = await reloaded { c.press() } }
        let day = Date().addingTimeInterval(-86_400)
        check("time chip: the last 24 h", p.query.time == .day && p.list.items.allSatisfy { ($0.lastActivity ?? .distantPast) >= day })
        for _ in 0..<3 { if let c = p.chip("time") { _ = await reloaded { c.press() } } }
        check("time chip cycles back to any time", p.query.time == .any)
        _ = await calls()
        let search = await calls(clear: false)
        _ = search

        // 4. Paging: scrolling far loads the next pages by cursor.
        let first = p.list.items.count
        for _ in 0..<12 {
            p.perform(.last)
            await Harness.sleep(0.15)
        }
        _ = await Harness.wait(3) { p.list.items.count > first }
        let paged = await calls()
        check("scrolling down pages by cursor", p.list.items.count > first && paged.contains { $0.method == "sessions.search" && $0.params["cursor"] != nil },
              "\(first) → \(p.list.items.count)")
        p.perform(.first)

        // 5. Search: FTS as you type, matches highlighted.
        window.makeFirstResponder(p.searchField)
        let typed = await reloaded { typeInSearch("debounce") }
        await Harness.sleep(0.3)
        check("typing searches (live FTS, debounced)", typed && p.query.text == "debounce" && !p.rows.isEmpty
              && p.rows.allSatisfy { !$0.highlights.isEmpty }, "\(p.rows.count) rows, \(String(format: "%.1f", p.lastTypeToRowsMs ?? -1)) ms")
        let searches = await calls()
        check("one search per pause, not per key", searches.filter { $0.method == "sessions.search" }.count <= 3, "\(searches.count)")
        await Harness.sleep(0.4)
        shot("history-search")
        clearSearch()
        _ = await settled()

        // 6. The card: at once from the row, git details into their slot.
        guard let pick = p.list.items.firstIndex(where: { !$0.isLive && $0.machine == "M" && !$0.external }) else {
            check("a mini session to resume", false); return finish()
        }
        p.table.deselectAll(nil)
        p.select(row: pick)
        let s = p.list.items[pick]
        check("the card shows at once (you asked / it answered / todos / branch)", p.card.text?.asked.isEmpty == false && p.card.text?.title == s.title
              && p.card.text?.answered.isEmpty == false && p.card.text?.branch.isEmpty == false, p.card.text?.title ?? "nil")
        let cardFrame = p.subviews.first.map { _ in true } ?? true
        _ = cardFrame
        let details = await Harness.wait(3) { p.card.text?.changed != nil }
        check("changed files, uncommitted, ahead/behind arrive from sessions.show", details && p.card.text?.changed?.contains("rail.py") == true
              && p.card.text?.branch.contains("ahead") == true, p.card.text?.changed ?? "")
        check("actions: ⏎ Resume on mini, ⌥⏎ Fork, ⌘⏎ Continue on laptop with its note",
              p.card.text?.actions.first?.title == "Resume on mini" && p.card.text?.actions.contains { $0.key == "⌥⏎" && $0.title == "Fork" } == true
                && p.card.text?.actions.contains { $0.key == "⌘⏎" && $0.title == "Continue on laptop" } == true
                && p.card.text?.resumeHereNote?.contains("uncommitted work comes along if mini is reachable") == true)
        await Harness.sleep(0.3)
        shot("history-card")

        // 7. Keys: ⇧⇥ to the list, letters act there; typing goes back to search.
        key("\t", 48, .shift)
        check("⇧⇥ gives the list the keys", !((window.firstResponder as? NSTextView)?.delegate is NSSearchField))
        p.pasteboard = pasteboard
        key("c", 8, .command)
        check("⌘C copies the session id", pasteboard.string(forType: .string) == s.sessionId, pasteboard.string(forType: .string) ?? "nil")
        _ = await calls()

        // F: fork.
        let agentsBefore = Set(model.registry.agents.keys)
        key("f", 3)
        let forked = await Harness.wait(4) { Set(self.model.registry.agents.keys).subtracting(agentsBefore).count == 1 }
        var cs = await calls()
        check("F forks (sessions.fork) and the new agent joins the wall", forked && cs.contains { $0.method == "sessions.fork" && $0.params["id"]?.stringValue == s.id },
              cs.map(\.method).joined(separator: ","))
        check("the panel closes after starting", p.isOpen == false)

        // ⌘⏎: continue on the other Mac (a mini session comes here; moves ownership).
        await openHistory()
        _ = await settled()
        if let i = p.list.items.firstIndex(where: { $0.id == s.id }) { p.select(row: i) }
        let before2 = Set(model.registry.agents.keys)
        key("\r", 36, .command)
        let here = await Harness.wait(4) { Set(self.model.registry.agents.keys).subtracting(before2).count == 1 }
        cs = await calls()
        let newID = Set(model.registry.agents.keys).subtracting(before2).first ?? ""
        check("⌘⏎ continues on the other Mac (here): sessions.resume {machine: L}", here && cs.contains { $0.method == "sessions.resume" && $0.params["machine"]?.stringValue == "L" }
              && model.agent(newID)?.machine == "L", cs.map { "\($0.method) \($0.params)" }.joined(separator: "; "))
        check("the result's note shows (what came along)", model.toast?.text.contains("uncommitted work came along") == true, model.toast?.text ?? "nil")
        let overlay = await Harness.wait(2) { self.main.root.wall.tiles[newID]?.subviews.contains { $0 is SessionCardOverlay } == true }
        check("the resumed tile shows the left-off card as its first screen", overlay)
        _ = await Harness.wait(3) { self.model.selectedID == newID }
        check("the resumed agent is selected in its project's band", model.selectedID == newID
              && (main.root.wall.view.showsBands ? main.root.wall.view.band(of: newID) != nil : true))
        await Harness.sleep(0.2)
        shot("resume-card")
        let gone = await Harness.wait(14) { self.main.root.wall.tiles[newID]?.subviews.contains { $0 is SessionCardOverlay } != true }
        check("…until the CLI has drawn", gone)

        // A live session opens its agent, never a second one.
        await openHistory()
        _ = await settled()
        if let c = p.chip("live") { _ = await reloaded { c.press() } }
        if let live = p.list.items.first(where: { $0.liveAgentID == newID }) ?? p.list.items.first {
            p.select(row: p.list.row(of: live.id) ?? 0)
            let n = model.registry.agents.count
            key("\r", 36)
            let opened = await Harness.wait(2) { self.model.selectedID == live.liveAgentID }
            cs = await calls()
            check("⏎ on a live session opens its agent (no resume)", opened && model.registry.agents.count == n && !cs.contains { $0.method == "sessions.resume" },
                  "\(model.selectedID ?? "nil") vs \(live.liveAgentID ?? "nil")")
        }
        await openHistory()
        if let c = p.chip("live") { _ = await reloaded { c.press() } }

        // C: continue in the other kind with an edited brief.
        guard let ci = p.list.items.firstIndex(where: { !$0.isLive && $0.kind == "claude" }) else { check("a Claude session", false); return finish() }
        p.select(row: ci)
        let cSession = p.list.items[ci]
        key("\t", 48, .shift)
        key("c", 8)
        let brief = await Harness.wait(3) { p.card.mode == .continuing && !p.card.briefLoading && !p.card.brief.isEmpty }
        check("C shows the brief (sessions.brief), Codex preselected", brief && p.card.continueKind == "codex", String(p.card.brief.prefix(60)))
        await Harness.sleep(0.3)
        shot("history-continue")
        let before3 = Set(model.registry.agents.keys)
        key("\r", 36, .command)
        let cont = await Harness.wait(4) { Set(self.model.registry.agents.keys).subtracting(before3).count == 1 }
        cs = await calls()
        let ca = cs.first { $0.method == "sessions.continueAs" }
        check("⌘⏎ starts Codex from hesperd's brief (sessions.continueAs {kind: codex})", cont && ca?.params["kind"]?.stringValue == "codex"
              && ca?.params["id"]?.stringValue == cSession.id, cs.map(\.method).joined(separator: ","))
        // Edited: the edited text is the new agent's first prompt.
        await openHistory()
        _ = await settled()
        if let i = p.list.row(of: cSession.id) { p.select(row: i) }
        key("\t", 48, .shift)
        key("c", 8)
        _ = await Harness.wait(3) { p.card.mode == .continuing && !p.card.briefLoading && !p.card.brief.isEmpty }
        p.card.brief += "\nAlso: keep the tests green."
        let before4 = Set(model.registry.agents.keys)
        key("\r", 36, .command)
        let cont2 = await Harness.wait(4) { Set(self.model.registry.agents.keys).subtracting(before4).count == 1 }
        let newC = Set(model.registry.agents.keys).subtracting(before4).first.flatMap { model.agent($0) }
        check("an edited brief starts Codex with that text in the session's folder", cont2 && newC?.kind == "codex"
              && newC?.task?.contains("keep the tests green") == true && newC?.project == cSession.cwd, (newC?.task?.suffix(40).description ?? "nil") + " toast: \(model.toast?.text ?? "-") mode \(p.card.mode) open \(p.isOpen) sel \(p.selectedSession?.id ?? "-") vs \(cSession.id)")

        // A: archive; ⌫: delete with undo.
        await openHistory()
        _ = await settled()
        guard p.list.items.count > 3 else { check("sessions left", false); return finish() }
        guard let ai = p.list.items.indices.dropFirst().first(where: { !p.list.items[$0].isLive }) else { check("an ended session", false); return finish() }
        p.select(row: ai)
        let aSession = p.list.items[ai]
        key("\t", 48, .shift)
        key("a", 0)
        _ = await Harness.wait(2) { p.list.row(of: aSession.id) == nil }
        _ = await Harness.waitAsync(5) { (await self.calls(clear: false)).contains { $0.method == "sessions.archive" } }
        await Harness.sleep(0.3)
        cs = await calls()
        check("A archives (sessions.archive) and the row leaves the list", p.list.row(of: aSession.id) == nil
              && cs.contains { $0.method == "sessions.archive" && $0.params["archived"]?.boolValue == true && $0.params["id"]?.stringValue == aSession.id }, "row \(p.list.row(of: aSession.id).map(String.init) ?? "gone") calls \(cs.map { "\($0.method) \($0.params["id"]?.stringValue ?? "")" }) want \(aSession.id) mode \(p.card.mode) toast \(model.toast?.text ?? "-")")
        guard let di = p.list.items.indices.dropFirst().first(where: { !p.list.items[$0].isLive }) else { check("an ended session", false); return finish() }
        p.select(row: di)
        let dSession = p.list.items[di]
        key("\u{7f}", 51)
        _ = await Harness.wait(5) { self.model.undoToast != nil }
        cs = await calls()
        check("⌫ deletes (sessions.delete) with an undo toast", p.list.row(of: dSession.id) == nil && model.undoToast != nil
              && cs.contains { $0.method == "sessions.delete" && $0.params["id"]?.stringValue == dSession.id }, "row \(p.list.row(of: dSession.id).map(String.init) ?? "gone") undo \(model.undoToast?.label ?? "nil") toast \(model.toast?.text ?? "-") calls \(cs.map(\.method)) fr \(String(describing: type(of: window.firstResponder as Any)))")
        await Harness.sleep(0.2)
        shot("history-undo")
        key("z", 6, .command)
        let restored = await Harness.waitAsync(6) { (await self.calls(clear: false)).contains { $0.method == "sessions.delete" && $0.params["undo"]?.boolValue == true } }
        _ = await Harness.wait(5) { p.list.row(of: dSession.id) != nil }
        check("⌘Z takes it back (sessions.delete {undo: true}) and it is back", restored && p.list.row(of: dSession.id) != nil, "restored \(restored) row \(p.list.row(of: dSession.id).map(String.init) ?? "gone") toast \(model.toast?.text ?? "-")")
        _ = await calls()

        // ⏎ on an ended session: resume where it ran.
        if let ri = p.list.items.firstIndex(where: { !$0.isLive && $0.machine == "L" && !$0.external }) {
            p.select(row: ri)
            let rs = p.list.items[ri]
            let b = Set(model.registry.agents.keys)
            key("\r", 36)
            let ok = await Harness.wait(4) { Set(self.model.registry.agents.keys).subtracting(b).count == 1 }
            cs = await calls()
            check("⏎ resumes where it ran (sessions.resume without machine)", ok
                  && cs.contains { $0.method == "sessions.resume" && $0.params["id"]?.stringValue == rs.id && $0.params["machine"] == nil })
        }
        // One search surface: ⇥ takes the query to ⌘K's All scope and back.
        await openHistory()
        _ = await settled()
        window.makeFirstResponder(p.searchField)
        _ = await reloaded { typeInSearch("debounce") }
        key("\t", 48)
        let toAll = await Harness.wait(2) { self.model.showPalette && !p.isOpen }
        check("⇥ in History: ⌘K's All scope with the query", toAll && model.paletteList.query == "debounce", model.paletteList.query)
        _ = model.filteredPalette()
        _ = await Harness.wait(3) { self.model.filteredPalette().contains { $0.section == "History" } }
        if let i = model.filteredPalette().firstIndex(where: { !$0.id.hasPrefix("agent:") }) { model.paletteList.index = i }
        key("\t", 48)
        let back = await Harness.wait(3) { p.isOpen && !self.model.showPalette }
        check("⇥ in ⌘K: History with the query", back && p.query.text == "debounce", p.query.text)
        clearSearch()
        _ = await settled()
        key("\u{1b}", 53)
        check("esc closes History", !p.isOpen)
        await Harness.sleep(0.4)

        // 8. Ghost cards: a removed agent stays on its band's shelf.
        await ghostCards()
        // 9. ⌘K: History section.
        await palette()
        finish()
    }

    private func ghostCards() async {
        let wall = main.root.wall
        model.settings.ghostCards = true // Settings › Agents: off by default
        guard let victim = model.wall.first(where: { !$0.state.needsAttention && $0.kind != "shell" }) else { check("an agent to remove", false); return }
        model.select(victim.id)
        _ = try? await model.client.stopAgent(victim.id)
        _ = await Harness.wait(4) { self.model.agent(victim.id)?.isRunning == false }
        await Harness.sleep(0.8)
        let shelfFrame = wall.tiles[victim.id]?.frame
        model.close(model.agent(victim.id)!) // ⌘W without the worktree question
        let gid = "ghost:" + (victim.sessionId ?? "")
        let pending = await Harness.wait(2) { wall.ghostTiles[gid] != nil }
        await Harness.sleep(0.5)
        check("removing an agent leaves a ghost card in its shelf place", pending && wall.tiles[victim.id] == nil,
              "\(shelfFrame.map { "\($0)" } ?? "nil") → \(wall.ghostTiles[gid].map { "\($0.frame)" } ?? "nil")")
        // After the undo window: the daemon's removed session takes over.
        let committed = await Harness.wait(10) { self.model.registry.agents[victim.id] == nil && self.model.history.ghosts.contains { GhostCards.itemID($0) == gid } }
        await Harness.sleep(0.5)
        check("after the undo window it stays (sessions.changed with removedAt)", committed && wall.ghostTiles[gid] != nil)
        await Harness.sleep(0.5)
        shot("ghost-cards")
        // ⏎ on the selected ghost: resume.
        model.select(gid)
        window.makeFirstResponder(wall)
        _ = await calls()
        let b = Set(model.registry.agents.keys)
        key("\r", 36)
        let back = await Harness.wait(4) { Set(self.model.registry.agents.keys).subtracting(b).count == 1 }
        let cs = await calls()
        _ = await Harness.wait(3) { wall.ghostTiles[gid] == nil }
        check("⏎ on a ghost card resumes it (sessions.resume) and the ghost goes", back && wall.ghostTiles[gid] == nil
              && cs.contains { $0.method == "sessions.resume" })
        // ⌫ forgets another one.
        guard let v2 = model.wall.first(where: { !$0.isRunning || $0.state == .done || $0.state == .idle }) ?? model.wall.last else { return }
        _ = try? await model.client.stopAgent(v2.id)
        _ = await Harness.wait(4) { self.model.agent(v2.id)?.isRunning == false }
        _ = try? await model.client.remove(v2.id)
        let g2 = "ghost:" + (v2.sessionId ?? "")
        _ = await Harness.wait(3) { wall.ghostTiles[g2] != nil }
        model.select(g2)
        window.makeFirstResponder(wall)
        key("\u{7f}", 51)
        let forgot = await Harness.wait(2) { wall.ghostTiles[g2] == nil }
        check("⌫ on a ghost card forgets it", forgot)
        // The setting turns them off.
        _ = try? await model.client.stopAgent(model.wall.last!.id)
        let v3 = model.wall.last!
        _ = await Harness.wait(4) { self.model.agent(v3.id)?.isRunning == false }
        _ = try? await model.client.remove(v3.id)
        _ = await Harness.wait(3) { !wall.ghostTiles.isEmpty }
        model.settings.ghostCards = false
        let off = await Harness.wait(2) { wall.ghostTiles.isEmpty }
        check("Settings › ghost cards off: none on the shelf", off)
        model.settings.ghostCards = true
        _ = await Harness.wait(2) { !wall.ghostTiles.isEmpty }
    }

    private func palette() async {
        key("k", 40, .command)
        _ = await Harness.wait(2) { self.model.showPalette }
        model.paletteList.query = "debounce"
        _ = model.filteredPalette()
        let shown = await Harness.wait(3) { self.model.filteredPalette().contains { $0.section == "History" } }
        let items = model.filteredPalette().filter { $0.section == "History" }
        check("⌘K lists matching sessions under History (full text)", shown && !items.isEmpty, "\(items.count) \(items.first?.title ?? "")")
        await Harness.sleep(0.4)
        shot("palette-history")
        // ⇥: the card in History.
        if let i = model.filteredPalette().firstIndex(where: { $0.section == "History" }) {
            model.paletteList.index = i
            let target = model.filteredPalette()[i].id.dropFirst(8)
            _ = model.paletteKey(.actOn)
            let card = await Harness.wait(3) { self.panel?.isOpen == true && self.panel?.selectedSession?.id == String(target) }
            check("⇥ on a History row shows its card in the panel", card && !model.showPalette, "\(panel?.selectedSession?.id ?? "nil")")
            panel?.close()
        }
        // ⏎: resume.
        key("k", 40, .command)
        _ = await Harness.wait(2) { self.model.showPalette }
        model.paletteList.query = "flaky badge"
        _ = model.filteredPalette()
        _ = await Harness.wait(3) { self.model.filteredPalette().contains { $0.section == "History" } }
        _ = await calls()
        if let i = model.filteredPalette().firstIndex(where: { $0.section == "History" && $0.dot != Theme.Token.done.hex }) {
            model.paletteList.index = i
            let b = Set(model.registry.agents.keys)
            _ = model.paletteKey(.activate)
            let ok = await Harness.wait(4) { Set(self.model.registry.agents.keys).subtracting(b).count == 1 }
            let cs = await calls()
            check("⏎ on a History row resumes it", ok && cs.contains { $0.method == "sessions.resume" })
        } else {
            check("⏎ on a History row resumes it", false, "no ended session found")
        }
        model.showPalette = false
    }

    /// The real hesperd (relay/dist) over synthetic transcripts, every
    /// profile the fake TUI: History end to end on the real wire.
    private func realDaemon() async {
        let ready = await Harness.wait(240) {
            self.model.history.refreshStatsThrottled()
            return self.model.history.supported == true && self.model.history.stats.total >= 100 && self.model.history.indexing == nil
        }
        check("real hesperd: sessions.* and the first index of the synthetic transcripts", ready, "\(model.history.stats.total) sessions")
        guard ready else { return finish() }
        key("y", 16, .command)
        let opened = await Harness.wait(5) { self.panel?.isOpen == true && (self.panel?.rows.count ?? 0) > 0 }
        guard opened, let p = panel else { check("⌘Y opens History", false); return finish() }
        check("⌘Y opens History with the real index (pages of 50)", p.rows.count == 50 && p.list.hasMore,
              "\(p.rows.count) rows in \(String(format: "%.1f", p.openToFirstRowsMs ?? -1)) ms")
        let typed = await reloaded { typeInSearch("quokka") }
        await Harness.sleep(0.3)
        check("search: hesperd's FTS with [highlights]", typed && !p.rows.isEmpty && p.rows.allSatisfy { !$0.highlights.isEmpty && !$0.snippet.contains("[") },
              "\(p.rows.count) rows, first: \(p.rows.first?.snippet ?? "")")
        shot("real-search")
        clearSearch()
        _ = await settled()
        guard let ci = p.list.items.firstIndex(where: { $0.kind == "claude" && !$0.isLive }) else { check("a Claude session", false); return finish() }
        p.table.deselectAll(nil)
        p.select(row: ci)
        let s = p.list.items[ci]
        let details = await Harness.wait(5) { p.card.text?.changed != nil }
        check("the card: you asked / it answered / todos, then sessions.show's git line", details && p.card.text?.asked.isEmpty == false
              && p.card.text?.answered.isEmpty == false && p.card.text?.todos == ["Open a PR"], p.card.text?.changed ?? "nil")
        shot("real-card")
        let before = Set(model.registry.agents.keys)
        key("\r", 36)
        let started = await Harness.wait(10) { Set(self.model.registry.agents.keys).subtracting(before).count == 1 }
        let id = Set(model.registry.agents.keys).subtracting(before).first ?? ""
        check("⏎ resumes it through hesperd (claude --resume, the fake TUI)", started && model.agent(id)?.sessionId == s.sessionId,
              "\(model.agent(id)?.sessionId ?? "nil") vs \(s.sessionId)")
        let overlay = await Harness.wait(3) { self.main.root.wall.tiles[id]?.subviews.contains { $0 is SessionCardOverlay } == true }
        check("the resumed tile shows the left-off card first", overlay)
        await Harness.sleep(1.5)
        key("y", 16, .command)
        _ = await settled()
        let liveRow = await Harness.wait(5) { p.list.items.contains { $0.id == s.id && $0.liveAgentID == id } }
        check("the session is live with its agent (sessions.changed)", liveRow)
        if let i = p.list.row(of: s.id) {
            p.select(row: i)
            let n = model.registry.agents.count
            key("\r", 36)
            let back = await Harness.wait(3) { self.model.selectedID == id && self.model.registry.agents.count == n }
            check("⏎ on the live session opens its agent", back)
        }
        // Archive and delete + undo on another one.
        key("y", 16, .command)
        _ = await settled()
        guard let ai = p.list.items.indices.dropFirst().first(where: { !p.list.items[$0].isLive }) else { return finish() }
        p.select(row: ai)
        let a = p.list.items[ai]
        key("\t", 48, .shift)
        key("a", 0)
        let archived = await Harness.wait(3) { p.list.row(of: a.id) == nil }
        check("A archives (the row leaves; the archived chip has it)", archived)
        guard let di = p.list.items.indices.dropFirst().first(where: { !p.list.items[$0].isLive }) else { return finish() }
        p.select(row: di)
        let d = p.list.items[di]
        key("\u{7f}", 51)
        let deleted = await Harness.wait(3) { p.list.row(of: d.id) == nil && self.model.undoToast != nil }
        key("z", 6, .command)
        let undone = await Harness.wait(5) { p.list.row(of: d.id) != nil }
        check("⌫ deletes, ⌘Z takes it back (sessions.delete {undo})", deleted && undone, "deleted \(deleted) undone \(undone) toast \(model.toast?.text ?? "-")")
        key("\u{1b}", 53)
        key("k", 40, .command)
        model.paletteList.query = "wombat"
        _ = model.filteredPalette()
        let pal = await Harness.wait(3) { self.model.filteredPalette().contains { $0.section == "History" } }
        check("⌘K: History rows from the real index", pal)
        model.showPalette = false
        finish()
    }

    /// An older hesperd (no sessions.*): nothing of History shows.
    private func noSessions() async {
        let known = await Harness.wait(5) { self.model.history.supported == false }
        check("an older daemon: sessions.* unsupported", known)
        let item = NSApp.mainMenu?.items.compactMap(\.submenu).flatMap(\.items).first { $0.title == "History" }
        check("the History menu item is hidden", item?.isHidden == true)
        key("y", 16, .command)
        await Harness.sleep(0.4)
        check("⌘Y opens nothing", panel?.isOpen != true)
        key("k", 40, .command)
        model.paletteList.query = "debounce"
        await Harness.sleep(0.5)
        check("⌘K has no History section", !model.filteredPalette().contains { $0.section == "History" })
        model.showPalette = false
        let v = model.wall.last!
        _ = try? await model.client.stopAgent(v.id)
        _ = await Harness.wait(4) { self.model.agent(v.id)?.isRunning == false }
        model.close(model.agent(v.id)!)
        await Harness.sleep(0.5)
        check("closing an agent works as before, without a ghost card", main.root.wall.tiles[v.id] == nil && main.root.wall.ghostTiles.isEmpty)
        finish()
    }

    private func shot(_ name: String) {
        guard let dir = shots else { return }
        let path = "\(dir)/\(name).png"
        let p = Process()
        p.executableURL = URL(fileURLWithPath: "/usr/sbin/screencapture")
        p.arguments = ["-x", "-o", "-l", "\(window.windowNumber)", path]
        try? p.run()
        p.waitUntilExit()
        shotFiles.append(path)
    }

    private func finish() {
        let failed = checks.filter { !$0.ok }.count
        Harness.write(Result(passed: checks.count - failed, failed: failed, checks: checks, shots: shotFiles), to: out)
        pasteboard.releaseGlobally()
        exit(failed == 0 ? 0 : 1)
    }
}
