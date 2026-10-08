import Foundation
import Testing
@testable import HesperCore

// Shared history (docs "As built — shared history (app)").
@Suite struct SessionTests {
    static func json(_ s: String) -> JSONValue { try! JSONDecoder().decode(JSONValue.self, from: Data(s.utf8)) }

    static let wire = json("""
    {"id":"s-1","kind":"codex","sessionId":"0199-abc","machine":"M","cwd":"/Users/mini/projects/hesper","projectId":"p-hesper",
     "branch":"fix/flaky-badge","title":"","firstPrompt":"Fix the flaky badge test\\nthen stop","lastUser":"make it pass 20 times",
     "lastAssistant":"20 runs, all green.","todos":[{"text":"open a PR","done":false},{"text":"run it","done":true},"plain todo"],
     "turns":46,"tokens":312000,"startedAt":"2026-10-06T16:00:00Z","lastActivity":"2026-10-06T18:42:10.500Z",
     "live":{"agentId":"M/a7f3","external":false},"external":false,"archived":false,"mirrored":["L"],"snippet":"the <b>debounce</b> race"}
    """)

    @Test func decodesTheWireSession() {
        let s = Session(json: Self.wire)!
        #expect(s.id == "s-1" && s.kind == "codex" && s.sessionId == "0199-abc" && s.machine == "M")
        #expect(s.title == "Fix the flaky badge test") // empty title: the first prompt's first line
        #expect(s.todos == [SessionTodo(text: "open a PR"), SessionTodo(text: "run it", done: true), SessionTodo(text: "plain todo")])
        #expect(s.turns == 46 && s.tokens == 312_000)
        #expect(s.lastActivity == Date(timeIntervalSince1970: 1_791_312_130.5))
        #expect(s.liveAgentID == "M/a7f3" && s.isLive && s.mirrored == ["L"] && s.otherKind == "claude")
        #expect(Session(json: s.json)?.id == "s-1")
    }

    @Test func decodesLeniently() {
        let s = Session(json: Self.json(#"{"id":"x","turns":"12","lastActivity":1791398530000,"live":true}"#))!
        #expect(s.turns == 12 && s.kind == "claude" && s.lastActivity == Date(timeIntervalSince1970: 1_791_398_530) && s.isLive && s.liveAgentID == nil)
        #expect(Session(json: Self.json(#"{"title":"no id"}"#)) == nil)
        let page = SessionPage(json: Self.json(#"{"items":[{"id":"a"},{"nope":1},{"id":"b"}],"cursor":"100"}"#))
        #expect(page.items.map(\.id) == ["a", "b"] && page.cursor == "100")
        #expect(SessionPage(json: Self.json(#"{"items":[],"cursor":""}"#)).cursor == nil)
    }

    @Test func decodesShowAndStats() {
        let d = SessionDetail(json: Self.json(#"{"id":"s","changes":{"files":[{"path":"rail.py","added":14,"removed":6}],"uncommitted":3,"ahead":2,"behind":0,"base":"main","worktreeExists":false}}"#))!
        #expect(d.changes?.files == [SessionChanges.File(path: "rail.py", added: 14, removed: 6)])
        #expect(d.changes?.uncommitted == true && d.changes?.uncommittedCount == 3 && d.changes?.worktreeExists == false)
        let st = SessionStats(json: Self.json(#"{"projects":{"p":5},"scratch":2,"elsewhere":1,"indexing":{"done":3,"total":9}}"#))
        #expect(st.byProject == ["p": 5] && st.total == 8 && st.indexing == IndexingProgress(done: 3, total: 9))
        #expect(IndexingProgress(done: 9, total: 9).finished && !IndexingProgress(done: 1, total: 9).finished)
    }

    @Test func buildsSearchParams() {
        let now = Date(timeIntervalSince1970: 1_791_400_000)
        var q = HistoryQuery(text: "  flaky badge ", scope: .project("p-hesper"), kinds: ["codex"], machines: ["M", "L"], time: .day)
        q.liveOnly = true
        let p = q.params(cursor: "100", now: now)
        #expect(p["query"]?.stringValue == "flaky badge" && p["projectId"]?.stringValue == "p-hesper")
        #expect(p["kinds"] == .array(["codex"]) && p["machines"] == .array(["L", "M"]))
        #expect(p["since"]?.stringValue == Session.format(now.addingTimeInterval(-86_400)))
        #expect(p["live"]?.boolValue == true && p["external"] == nil && p["archived"] == nil && p["cursor"]?.stringValue == "100")
        #expect(p["limit"]?.doubleValue == 50)
        #expect(HistoryQuery(scope: .scratch).params()["projectId"]?.stringValue == "~scratch")
        #expect(HistoryQuery(scope: .elsewhere).params()["projectId"]?.stringValue == "~elsewhere")
        #expect(HistoryQuery().params()["query"] == nil)
    }

    @Test func chipsToggle() {
        var q = HistoryQuery()
        let chips = HistoryChips.make(q, context: "p-hesper", contextName: "hesper", machines: [("L", "laptop"), ("M", "mini")])
        #expect(chips.map(\.id) == ["this", "all", "kind:claude", "kind:codex", "mac:L", "mac:M", "time", "live", "external", "archived", "moved"])
        #expect(chips.first?.title == "hesper" && chips.first { $0.id == "kind:codex" }?.on == true)
        q = HistoryChips.toggle(.thisProject, in: q, context: "p-hesper")
        #expect(q.scope == .project("p-hesper"))
        q = HistoryChips.toggle(.allProjects, in: q, context: "p-hesper")
        #expect(q.scope == .all)
        q = HistoryChips.toggle(.kind("codex"), in: q, context: nil)
        #expect(q.kinds == ["claude"])
        q = HistoryChips.toggle(.kind("claude"), in: q, context: nil) // the last one off: all again
        #expect(q.kinds.isEmpty)
        q = HistoryChips.toggle(.machine("M"), in: q, context: nil)
        #expect(q.machines == ["M"])
        for _ in 0..<4 { q = HistoryChips.toggle(.time, in: q, context: nil) }
        #expect(q.time == .any)
        q = HistoryChips.toggle(.archived, in: q, context: nil)
        #expect(q.archived)
    }

    @Test func listPagesAndMergesChanges() {
        let a = Session(id: "a", lastActivity: Date()), b = Session(id: "b"), c = Session(id: "c")
        var l = HistoryList()
        #expect(!l.loaded)
        l.replace(with: SessionPage(items: [a, b], cursor: "2"))
        #expect(l.hasMore && l.needsMore(lastVisibleRow: 1) && !HistoryList().needsMore(lastVisibleRow: 0))
        let r = l.append(SessionPage(items: [b, c], cursor: nil))
        #expect(r == 2..<3 && l.items.map(\.id) == ["a", "b", "c"] && !l.hasMore)
        var a2 = a
        a2.turns = 9
        #expect(l.apply(a2, query: HistoryQuery()) == .updated(0))
        var arch = b
        arch.archived = true
        #expect(l.apply(arch, query: HistoryQuery()) == .removed(1))
        #expect(l.apply(Session(id: "new"), query: HistoryQuery()) == .inserted(0))
        #expect(l.apply(Session(id: "other"), query: HistoryQuery(text: "x")) == .none) // searching: no surprise rows
        #expect(l.apply(Session(id: "z", kind: "codex"), query: HistoryQuery(kinds: ["claude"])) == .none)
        #expect(l.remove(id: "c") != nil && l.row(of: "c") == nil)
    }

    @Test func mergesBurstsInOnePass() {
        let t = Date()
        var l = HistoryList()
        l.replace(with: SessionPage(items: (0..<5).map { Session(id: "s\($0)", lastActivity: t.addingTimeInterval(Double(-$0 * 60))) }))
        var s2 = l.items[2]
        s2.turns = 7
        var m = l.merge(changed: [s2], removed: [], query: HistoryQuery())
        #expect(m == HistoryList.Merge(updated: [2], structural: false) && l.items[2].turns == 7)
        // An old session busy elsewhere does not push into the list; a new one goes on top.
        let old = Session(id: "old", lastActivity: t.addingTimeInterval(-86_400)), new = Session(id: "new", lastActivity: t.addingTimeInterval(5))
        var arch = l.items[1]
        arch.archived = true
        m = l.merge(changed: [old, new, arch], removed: ["s4"], query: HistoryQuery())
        #expect(m.structural && l.items.map(\.id) == ["new", "s0", "s2", "s3"] && l.row(of: "s3") == 3)
        #expect(l.merge(changed: [Session(id: "x", lastActivity: t.addingTimeInterval(9))], removed: [], query: HistoryQuery(text: "q")).structural == false)
    }

    @Test func queryMatchesLikeTheDaemon() {
        let s = Session(id: "x", kind: "claude", machine: "L", projectId: "scratch:/tmp/x", lastActivity: Date().addingTimeInterval(-3 * 86_400))
        #expect(HistoryQuery(scope: .scratch).matches(s))
        #expect(!HistoryQuery(scope: .elsewhere).matches(s))
        #expect(!HistoryQuery(time: .day).matches(s) && HistoryQuery(time: .week).matches(s))
        #expect(!HistoryQuery(liveOnly: true).matches(s))
        #expect(HistoryQuery(scope: .elsewhere).matches(Session(id: "y")))
    }

    @Test func coalescesToFourASecond() {
        var c = UpdateCoalescer(maxPerSecond: 4)
        #expect(c.event(at: 10.0) == .flushNow)
        c.flushed(at: 10.0)
        if case .scheduled(let w) = c.event(at: 10.05) { #expect(abs(w - 0.2) < 1e-9) } else { Issue.record("not scheduled") }
        #expect(c.event(at: 10.06) == .alreadyScheduled)
        c.flushed(at: 10.25)
        #expect(c.event(at: 10.6) == .flushNow)
        // 1000 events over a second: ≤ 5 flushes.
        var d = UpdateCoalescer(maxPerSecond: 4)
        var flushes = 0
        var due: Double?
        for i in 0..<1000 {
            let t = Double(i) / 1000
            if let at = due, t >= at { d.flushed(at: t); flushes += 1; due = nil }
            switch d.event(at: t) {
            case .flushNow: d.flushed(at: t); flushes += 1
            case .scheduled(let w): due = t + w
            case .alreadyScheduled: break
            }
        }
        #expect(flushes <= 5)
    }

    @Test func formatsRows() {
        let s = Session(json: Self.wire)!
        let names = ["L": "laptop", "M": "mini"]
        #expect(SessionFormat.meta(s, machines: names) == "mini · fix/flaky-badge · 46 turns · 312k tokens")
        #expect(SessionFormat.tokens(1_200_000) == "1.2M tokens" && SessionFormat.tokens(8_400) == "8.4k tokens" && SessionFormat.tokens(900) == "900 tokens")
        let h = SessionFormat.highlights("…the <b>debounce</b> race in <b>rail</b>")
        #expect(h.text == "…the debounce race in rail" && h.ranges == [5..<13, 22..<26])
        #expect(SessionFormat.highlights("a\u{2}b\u{3}c").ranges == [1..<2])
        #expect(SessionFormat.highlights("…the [debounce] race").text == "…the debounce race")
        #expect(SessionFormat.highlights("…the [debounce] race").ranges == [5..<13])
        #expect(SessionFormat.highlights("a ] b").text == "a ] b") // a lone ] is text
        let row = HistoryRowText(s, machines: names)
        #expect(row.kind == "Codex" && row.quote == "the debounce race" && row.highlights == [4..<12] && row.state == "● live in Hesper" && row.live)
        var ended = s
        ended.live = nil
        ended.snippet = nil
        #expect(HistoryRowText(ended, machines: names).quote == "“20 runs, all green.”")
        #expect(SessionFormat.stateLabel(ended) == "mirrored on 2 Macs")
        ended.external = true
        #expect(SessionFormat.stateLabel(ended) == "external")
        #expect(SessionFormat.oneLine("a\n\n  b   c", max: 3) == "a b…")
    }

    @Test func formatsWhen() {
        var cal = Calendar(identifier: .gregorian)
        cal.timeZone = TimeZone(identifier: "Europe/Berlin")!
        let now = cal.date(from: DateComponents(year: 2026, month: 10, day: 7, hour: 15))!
        #expect(SessionFormat.when(cal.date(from: DateComponents(year: 2026, month: 10, day: 7, hour: 14, minute: 5)), now: now, calendar: cal) == "14:05")
        #expect(SessionFormat.when(cal.date(from: DateComponents(year: 2026, month: 10, day: 6, hour: 18, minute: 42)), now: now, calendar: cal) == "yesterday 18:42")
        #expect(SessionFormat.when(cal.date(from: DateComponents(year: 2026, month: 10, day: 3, hour: 9)), now: now, calendar: cal) == "Sat 09:00")
        #expect(SessionFormat.when(cal.date(from: DateComponents(year: 2026, month: 9, day: 3)), now: now, calendar: cal) == "3 Sept" || SessionFormat.when(cal.date(from: DateComponents(year: 2026, month: 9, day: 3)), now: now, calendar: cal) == "3 Sep")
        #expect(SessionFormat.when(cal.date(from: DateComponents(year: 2025, month: 10, day: 3)), now: now, calendar: cal).hasSuffix("2025"))
    }

    @Test func formatsTheCard() {
        var s = Session(json: Self.wire)!
        s.live = nil
        let names = ["L": "laptop", "M": "mini"]
        let loading = SessionCardText(s, changes: nil, loadingChanges: true, local: "L", machines: names, home: "/Users/mini")
        #expect(loading.changed == nil && loading.subtitle == "Codex on mini · ~/projects/hesper")
        #expect(loading.asked == "make it pass 20 times" && loading.answered == "20 runs, all green." && loading.todos == ["open a PR", "plain todo"])
        #expect(loading.actions.map(\.key) == ["⏎", "⌥⏎", "F", "C", "A", "⌫", "⌘C"])
        #expect(loading.actions[0].title == "Resume on mini" && loading.actions[1].title == "Resume here (laptop)" && loading.actions[3].title == "Continue in Claude")
        #expect(loading.resumeHereNote == "continues on laptop, moves ownership; uncommitted work comes along if mini is reachable")
        let c = SessionChanges(files: [.init(path: "src/rail.py", added: 14, removed: 6), .init(path: "tests/test_rail.py", added: 31, removed: 0)],
                               uncommitted: true, ahead: 2, behind: 0, base: "main", worktreeExists: true)
        let full = SessionCardText(s, changes: c, loadingChanges: false, local: "L", machines: names)
        #expect(full.changed == "rail.py +14 −6 · test_rail.py +31" && full.uncommitted == "uncommitted")
        #expect(full.branch == "fix/flaky-badge · 2 ahead of main · folder on mini")
        var gone = c
        gone.worktreeExists = false
        #expect(SessionCardText(s, changes: gone, loadingChanges: false, local: "L", machines: names).branch.contains("folder gone"))
        // Here, and live.
        var here = s
        here.machine = "L"
        let h = SessionCardText(here, changes: nil, loadingChanges: false, local: "L", machines: names)
        #expect(h.actions[0].title == "Resume" && !h.actions.contains { $0.key == "⌥⏎" } && h.resumeHereNote == nil && h.changed == "—")
        here.live = SessionLive(agentId: "L/x")
        #expect(SessionCardText(here, changes: nil, loadingChanges: false, local: "L", machines: names).actions[0].title == "Open (live)")
        var moved = s
        moved.movedTo = "L"
        let m = SessionCardText(moved, changes: nil, loadingChanges: false, local: "L", machines: names)
        #expect(m.actions[0].title == "Open on laptop" && !m.actions.contains { $0.key == "⌥⏎" } && SessionFormat.stateLabel(moved) == "moved to L")
        var mq = HistoryQuery()
        mq.moved = true
        #expect(!HistoryQuery().matches(moved) && mq.matches(moved) && mq.params()["moved"]?.boolValue == true)
    }

    @Test func ghostCardsExpireAndCap() {
        let now = Date(timeIntervalSince1970: 1_000_000)
        func g(_ id: String, _ ago: TimeInterval, project: String = "p") -> Session {
            Session(id: id, sessionId: "sid-" + id, projectId: project, removedAt: now.addingTimeInterval(-ago))
        }
        let all = [g("a", 60), g("b", 120), g("c", 180), g("d", 240), g("old", 25 * 3600), g("x", 30, project: "q"), Session(id: "never")]
        let v = GhostCards.visible(all, now: now)
        #expect(v.map(\.id) == ["x", "a", "b", "c"]) // newest 3 per project, a day at most
        #expect(GhostCards.visible(all, now: now, dismissed: ["ghost:sid-a"]).map(\.id) == ["x", "b", "c", "d"])
        var live = g("l", 10)
        live.live = SessionLive(agentId: "L/1")
        #expect(GhostCards.visible([live], now: now).isEmpty)
        #expect(GhostCards.nextExpiry(v) == now.addingTimeInterval(-180 + 24 * 3600))
        #expect(GhostCards.itemID(g("a", 1)) == "ghost:sid-a" && GhostCards.itemID(Session(id: "s")) == "ghost:s")
        let agent = Agent(id: "L/1", machine: "L", kind: "claude", name: "edition picker", state: .exited)
        let p = GhostCards.pending(from: agent, removedAt: now)
        #expect(p.title == "edition picker" && p.removedAt == now && GhostCards.itemID(p) == "ghost:pending:L/1")
    }

    @Test func routesHistoryKeys() {
        func r(_ k: KeyChord, _ code: UInt16 = 0, _ chars: String? = nil, f: HistoryFocus = .list, sel: Bool = false) -> HistoryKeyAction {
            HistoryKeys.route(k, keyCode: code, characters: chars, focus: f, fieldHasSelection: sel)
        }
        #expect(r(KeyChord(.down), f: .search) == .down && r(KeyChord(.up)) == .up)
        #expect(r(KeyChord(.enter), f: .search) == .resume && r(KeyChord(.enter, option: true)) == .resumeHere)
        #expect(r(KeyChord(.escape), f: .search) == .close && r(KeyChord(.char("y"), command: true)) == .close)
        #expect(r(KeyChord(.tab), f: .search) == .focusList && r(KeyChord(.tab, shift: true)) == .focusSearch(nil))
        #expect(r(KeyChord(.char("f")), 3, "f") == .fork && r(KeyChord(.char("c")), 8, "c") == .continueOther && r(KeyChord(.char("a")), 0, "a") == .archive)
        #expect(r(KeyChord(.char("\u{7f}")), 51, "\u{7f}") == .delete)
        // In the search field letters and ⌫ are text.
        #expect(r(KeyChord(.char("f")), 3, "f", f: .search) == .pass && r(KeyChord(.char("\u{7f}")), 51, "\u{7f}", f: .search) == .pass)
        // Typing in the list goes back to the search with the text.
        #expect(r(KeyChord(.char("x")), 7, "x") == .focusSearch("x"))
        #expect(r(KeyChord(.char("f"), command: true), 3) == .focusSearch(nil))
        #expect(r(KeyChord(.char("c"), command: true), 8, nil, f: .search, sel: true) == .pass)
        #expect(r(KeyChord(.char("c"), command: true), 8, nil, f: .search) == .copyID)
        #expect(r(KeyChord(.char("z"), command: true), 6) == .pass) // ⌘Z: the window's undo
        #expect(KeyRouter.route(KeyChord(.char("y"), command: true), mode: .wall, selectedState: nil) == .history)
        #expect(KeyRouter.route(KeyChord(.char("y"), command: true), mode: .focus, selectedState: nil) == .history)
    }

    @Test func sidebarCountsSessions() {
        let cat = ProjectCatalog(projects: [Project(id: "p-a", name: "acme-apps"), Project(id: "p-ios", name: "ios-app", kind: .package, parentId: "p-a"),
                                            Project(id: "p-g", name: "hesper")],
                                 groups: [ProjectGroup(id: "g", name: "acme", projectIds: ["p-a"])])
        let st = SessionStats(total: 20, byProject: ["p-a": 5, "p-ios": 3, "p-g": 4, "p-unknown": 2], scratch: 4, elsewhere: 2)
        let s = HistorySidebar.make(catalog: cat, stats: st)
        let rows = s.flatMap(\.rows)
        #expect(s.map(\.title) == [nil, "acme", "Other projects", "Scratch", "Elsewhere"])
        #expect(rows.first { $0.projectID == "p-a" }?.count == 8) // its package's sessions too
        #expect(rows.first { $0.projectID == "p-ios" }?.depth == 1)
        #expect(rows.contains { $0.projectID == "p-unknown" && $0.title == "p-unknown" })
        let scratch = rows.first { $0.id == HistorySidebar.scratchRow }!
        #expect(HistorySidebar.scope(of: scratch) == .scratch && scratch.count == 4)
        #expect(HistorySidebar.scope(of: rows[0]) == .all && HistorySidebar.isCurrent(rows.first { $0.projectID == "p-g" }!, .project("p-g")))
    }

    @Test func rpcErrorCarriesData() {
        let e = RPCConnection.rpcError(Self.json(#"{"code":-32000,"message":"live","data":{"code":"live","agentId":"L/abc"}}"#))
        #expect(e.dataCode == "live" && e.data?["agentId"]?.stringValue == "L/abc" && e.kind == .unknown)
    }

    // The real hesperd's shape (relay/pkg/wire/sessions.go), refinements incl.
    static let real = json("""
    {"id":"mini:claude:7c1e","kind":"claude","sessionId":"7c1e","machine":"mini","cwd":"/Users/o/projects/x","projectId":"",
     "branch":"","title":"Rail badges","firstPrompt":"fix it","lastUser":"","lastAssistant":"done","todos":[],"turns":3,
     "startedAt":"2026-10-06T16:00:00Z","lastActivity":"2026-10-06T18:00:00Z","external":true,"archived":false,
     "mirrored":["mini","laptop"],"snippet":"the [flaky] [badge] test","origin":"claude-desktop",
     "removedAt":"2026-10-06T18:05:00.25Z","movedTo":"laptop","bytes":123456}
    """)

    @Test func decodesTheRealSessionShape() {
        let s = Session(json: Self.real)!
        #expect(s.id == "mini:claude:7c1e" && s.projectId == nil && s.branch == nil && s.tokens == nil && s.live == nil)
        #expect(s.origin == "claude-desktop" && s.movedTo == "laptop" && s.bytes == 123_456 && s.external)
        #expect(s.removedAt == Date(timeIntervalSince1970: 1_791_309_900.25) && s.mirrored == ["mini", "laptop"])
        let back = Session(json: s.json)!
        #expect(back == s)
        // Moved entries only with the chip; the card opens it on the new home.
        #expect(!HistoryQuery().matches(s) && HistoryQuery(moved: true).matches(s))
        let card = SessionCardText(s, changes: nil, loadingChanges: true, local: "laptop", machines: [:], home: "/Users/o")
        #expect(card.subtitle == "Claude on mini (desktop app) · ~/projects/x" && card.actions.first?.title == "Open on laptop")
        #expect(card.resumeHereNote == nil && card.changed == nil)
        #expect(SessionFormat.originLabel("cli") == nil && SessionFormat.originLabel("codex_exec") == "codex exec")
        #expect(SessionFormat.originLabel("codex-chrome-extension-sidepanel") == "browser extension")
        // stats: the real shape (count, indexing always there, 0/0 idle)
        let st = SessionStats(json: Self.json(#"{"count":2000,"byKind":{"claude":1200,"codex":800},"byMachine":{"mini":2000},"indexBytes":1,"mirrorBytes":2,"indexing":{"done":0,"total":0}}"#))
        #expect(st.total == 2000 && st.byKind["codex"] == 800 && st.byProject.isEmpty && st.indexing?.finished == true)
        // sessions.changed / sessions.removed / sessions.indexing params
        #expect(Session(json: Self.json(#"{"session":{"id":"a:codex:1"}}"#)["session"]!)?.kind == "claude")
        #expect(IndexingProgress(json: Self.json(#"{"done":5,"total":9}"#))?.fraction == 5.0 / 9.0)
    }

    @Test func highlightsBracketedMatches() {
        let h = SessionFormat.highlights("…the [flaky] [badge] test")
        #expect(h.text == "…the flaky badge test" && h.ranges == [5..<10, 11..<16])
        #expect(SessionFormat.highlights("[x]").ranges == [0..<1] && SessionFormat.highlights("no marks").ranges.isEmpty)
        #expect(SessionFormat.highlights("[ünïcödé] ok").ranges == [0..<7])
        let row = HistoryRowText(Session(json: Self.real)!, machines: [:])
        #expect(row.quote == "the flaky badge test" && row.highlights == [4..<9, 10..<15])
    }

    @Test func pagesByCursor() {
        // limit: default and cap 50; ⌘K's 8 stays 8; a cursor is passed through.
        #expect(HistoryQuery().params()["limit"]?.doubleValue == 50)
        #expect(HistoryQuery(limit: 500).params()["limit"]?.doubleValue == 50)
        #expect(HistoryQuery(limit: 8).params()["limit"]?.doubleValue == 8)
        #expect(HistoryQuery(limit: 0).params()["limit"]?.doubleValue == 1)
        #expect(HistoryQuery(moved: true).params()["moved"]?.boolValue == true && HistoryQuery().params()["moved"] == nil)
        let pageOf = { (r: Range<Int>, cursor: String?) in SessionPage(items: r.map { Session(id: "s\($0)") }, cursor: cursor) }
        var l = HistoryList()
        l.replace(with: pageOf(0..<50, "c1"))
        #expect(l.items.count == 50 && l.cursor == "c1" && !l.needsMore(lastVisibleRow: 5) && l.needsMore(lastVisibleRow: 10))
        // A session that moved up between pages is not shown twice.
        #expect(l.append(pageOf(49..<100, "c2")) == 50..<100 && l.items.count == 100 && l.cursor == "c2")
        #expect(l.append(pageOf(100..<120, nil)) == 100..<120 && !l.hasMore && !l.needsMore(lastVisibleRow: 119))
        // A new search starts over.
        l.replace(with: pageOf(0..<3, nil))
        #expect(l.items.map(\.id) == ["s0", "s1", "s2"] && !l.hasMore)
        // Deleted (sessions.removed) and undone: back in its place.
        let t = Date(timeIntervalSince1970: 1_000)
        var m = HistoryList()
        m.replace(with: SessionPage(items: [Session(id: "a", lastActivity: t.addingTimeInterval(30)), Session(id: "b", lastActivity: t.addingTimeInterval(20)),
                                           Session(id: "c", lastActivity: t)]))
        let b = m.items[1]
        #expect(m.merge(changed: [], removed: ["b"], query: HistoryQuery()).structural && m.items.map(\.id) == ["a", "c"])
        #expect(m.restore(b, query: HistoryQuery()) == 1 && m.items.map(\.id) == ["a", "b", "c"])
    }

    @Test func ghostCardsExpireAfterADay() {
        let now = Date(timeIntervalSince1970: 2_000_000)
        let s = Session(id: "g", sessionId: "sid", projectId: "p", removedAt: now.addingTimeInterval(-(24 * 3600 - 1)))
        #expect(GhostCards.visible([s], now: now).count == 1)
        #expect(GhostCards.visible([s], now: now.addingTimeInterval(2)).isEmpty) // expired
        #expect(GhostCards.nextExpiry([s]) == now.addingTimeInterval(1))
        #expect(GhostCards.visible([s], now: now, dismissed: ["g"]).isEmpty) // forgotten by session id
        var notRemoved = s
        notRemoved.removedAt = nil
        #expect(GhostCards.visible([notRemoved], now: now).isEmpty && GhostCards.nextExpiry([]) == nil)
        var resumed = s
        resumed.live = SessionLive(agentId: "L/9")
        #expect(GhostCards.visible([resumed], now: now).isEmpty)
    }

    @Test func routesLiveErrors() {
        let withAgent = RPCConnection.rpcError(Self.json(#"{"code":-32000,"message":"running","data":{"code":"live","agentId":"mini/x7"}}"#))
        #expect(SessionStart.live(from: withAgent) == .live(agentId: "mini/x7"))
        let outside = RPCConnection.rpcError(Self.json(#"{"code":-32000,"message":"running","data":{"code":"live"}}"#))
        #expect(SessionStart.live(from: outside) == .live(agentId: nil))
        let emptyID = RPCConnection.rpcError(Self.json(#"{"code":-32000,"message":"running","data":{"code":"live","agentId":""}}"#))
        #expect(SessionStart.live(from: emptyID) == .live(agentId: nil))
        let notFound = RPCConnection.rpcError(Self.json(#"{"code":-32000,"message":"gone","data":{"code":"not_found"}}"#))
        #expect(SessionStart.live(from: notFound) == nil && SessionStart.live(from: ConnectionError.closed) == nil)
    }
}
