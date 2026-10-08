import Foundation
import Testing
@testable import HesperCore

// The one search surface: ⌘K All / ⌘Y History (scopes, keys, rows,
// filters, preview actions).
@Suite struct SearchSurfaceTests {
    static let now = Date(timeIntervalSince1970: 1_791_398_530)
    static let names = ["M": "mini", "L": "laptop"]

    @Test func scopesSwitch() {
        #expect(SearchScope.all.other == .history && SearchScope.history.other == .all)
        #expect(SearchScope.all.title == "All" && SearchScope.history.shortcut == "⌘Y")
    }

    @Test func historyKeys() {
        func r(_ k: KeyChord, focus: HistoryFocus = .search) -> SearchKeyAction { SearchKeys.history(k, characters: nil, focus: focus) }
        #expect(r(KeyChord(.enter)) == .history(.resume))
        #expect(r(KeyChord(.enter, option: true)) == .history(.fork))
        #expect(r(KeyChord(.enter, command: true)) == .continueOnOtherMac)
        #expect(r(KeyChord(.tab)) == .switchScope)
        #expect(r(KeyChord(.tab, shift: true)) == .toggleFocus)
        #expect(r(KeyChord(.escape)) == .history(.close))
        #expect(r(KeyChord(.down)) == .history(.down))
        #expect(r(KeyChord(.char("y"), command: true)) == .history(.close), "⌘Y toggles")
        // Letters stay actions in the list.
        #expect(SearchKeys.history(KeyChord(.char("a")), characters: "a", focus: .list) == .history(.archive))
        #expect(SearchKeys.history(KeyChord(.char("a")), characters: "a", focus: .search) == .history(.pass))
    }

    @Test func shortcutsAreKbd() {
        #expect(SearchKeys.isShortcut("⌘N"))
        #expect(SearchKeys.isShortcut("⌥⌘1"))
        #expect(!SearchKeys.isShortcut("mini · working"))
        #expect(!SearchKeys.isShortcut("draft"))
        #expect(!SearchKeys.isShortcut(""))
    }

    @Test func ages() {
        let n = Self.now
        #expect(SearchFormat.age(nil, now: n) == "")
        #expect(SearchFormat.age(n.addingTimeInterval(-20), now: n) == "now")
        #expect(SearchFormat.age(n.addingTimeInterval(-5 * 60), now: n) == "5m")
        #expect(SearchFormat.age(n.addingTimeInterval(-3 * 3600), now: n) == "3h")
        #expect(SearchFormat.age(n.addingTimeInterval(-4 * 86_400), now: n) == "4d")
        var cal = Calendar(identifier: .gregorian)
        cal.timeZone = TimeZone(identifier: "UTC")!
        let old = cal.date(from: DateComponents(year: 2026, month: 9, day: 3, hour: 12))!
        #expect(SearchFormat.age(old, now: n, calendar: cal) == "3 Sep")
        let older = cal.date(from: DateComponents(year: 2025, month: 10, day: 3, hour: 12))!
        #expect(SearchFormat.age(older, now: n, calendar: cal) == "3 Oct 2025")
    }

    @Test func rowText() {
        let s = Session(id: "s1", kind: "codex", machine: "M", title: "Fix the badge", lastActivity: Self.now.addingTimeInterval(-7200),
                        snippet: "the [debounce] race and [flaky] badge")
        let r = SearchRowText(s, machines: Self.names, now: Self.now)
        #expect(r.meta == "mini · 2h")
        #expect(r.snippet == "the debounce race and flaky badge")
        #expect(r.highlights == [4..<12, 22..<27])
        #expect(r.utf16Highlights == [NSRange(location: 4, length: 8), NSRange(location: 22, length: 5)])
        #expect(r.mark == .past && !r.live)
        #expect(r.accessibilityLabel.hasPrefix("Codex session Fix the badge, mini · 2h"))
        let live = SearchRowText(Session(id: "s2", live: SessionLive(agentId: "L/a1")), machines: Self.names, now: Self.now)
        #expect(live.mark == .live && live.title == "(untitled)")
        // Emoji before a match: UTF-16 offsets differ from characters.
        let e = SearchRowText(Session(id: "s3", snippet: "🙂 [hit]"), machines: [:], now: Self.now)
        #expect(e.highlights == [2..<5] && e.utf16Highlights == [NSRange(location: 3, length: 3)])
    }

    @Test func continueTarget() {
        let remote = Session(id: "a", machine: "M")
        #expect(SearchPreview.continueTarget(remote, local: "L", online: ["M"]) == "L", "another Mac's session comes here")
        let here = Session(id: "b", machine: "L")
        #expect(SearchPreview.continueTarget(here, local: "L", online: ["L", "M"]) == "M", "this Mac's goes to the other one")
        #expect(SearchPreview.continueTarget(here, local: "L", online: ["L"]) == nil, "no other Mac online")
        #expect(SearchPreview.continueTarget(Session(id: "c", machine: "M", live: SessionLive(agentId: "x")), local: "L", online: []) == nil)
        #expect(SearchPreview.continueTarget(Session(id: "d", machine: "M", movedTo: "L"), local: "L", online: []) == nil)
    }

    @Test func previewActions() {
        let s = Session(id: "a", kind: "claude", machine: "M")
        let card = SessionCardText(s, changes: nil, loadingChanges: true, local: "L", machines: Self.names)
        let a = SearchPreview.actions(s, card: card, local: "L", online: ["M"], machines: Self.names)
        #expect(a.map(\.key) == ["⏎", "⌥⏎", "⌘⏎", "C", "A", "⌫", "⌘C"])
        #expect(a[0].title == "Resume on mini" && a[0].primary)
        #expect(a[1].title == "Fork")
        #expect(a[2].title == "Continue on laptop")
        #expect(a[3].title == "Continue in Codex")
        #expect(SearchPreview.continueNote(s, local: "L", online: ["M"], machines: Self.names)?.contains("uncommitted work comes along if mini is reachable") == true)
        let alone = SearchPreview.actions(Session(id: "b", machine: "L"), card: card, local: "L", online: [], machines: Self.names)
        #expect(!alone.contains { $0.key == "⌘⏎" })
    }

    static func row(_ id: String, _ title: String, project: String?, depth: Int = 0, count: Int = 1) -> SidebarRow {
        SidebarRow(id: id, kind: id == "all" ? .all : .project, title: title, colorHex: "#7aa2f7", count: count, needsYou: false, depth: depth,
                   groupID: nil, projectID: project, scratch: false, scope: project.map(WallScope.project) ?? .all)
    }

    static let projects = [
        SidebarSection(id: "all", title: nil, rows: [row("all", "All sessions", project: nil, count: 9)]),
        SidebarSection(id: "projects", title: "Projects", rows: [row("p:p-hesper", "hesper", project: "p-hesper", count: 5),
                                                                 row("p:p-pkg", "pkg", project: "p-pkg", depth: 1, count: 2)]),
        SidebarSection(id: "elsewhere", title: "Elsewhere", rows: [row(HistorySidebar.elsewhereRow, "folder gone", project: nil, count: 1)]),
    ]

    @Test func filterMenus() {
        var q = HistoryQuery()
        let macs = [(short: "L", name: "laptop"), (short: "M", name: "mini")]
        var f = HistoryFilters.make(q, context: "p-hesper", contextName: "hesper", machines: macs, projects: Self.projects)
        #expect(f.map(\.kind) == [.project, .machine, .tool, .age, .more])
        #expect(f.map(\.title) == ["All projects", "Any Mac", "Any tool", "any time", "More"])
        #expect(f.allSatisfy { !$0.active })
        let p = f[0].options
        #expect(p.first?.id == "this" && p.contains { $0.id == "h:projects" && $0.action == .header })
        #expect(p.first { $0.id == "p:p-pkg" }?.depth == 1 && p.first { $0.id == "p:p-hesper" }?.count == 5)

        q = HistoryFilters.apply(.scope(.project("p-hesper")), to: q, context: "p-hesper")
        q = HistoryFilters.apply(.chip(.machine("M")), to: q, context: "p-hesper")
        q = HistoryFilters.apply(.chip(.kind("codex")), to: q, context: "p-hesper")
        q = HistoryFilters.apply(.time(.week), to: q, context: "p-hesper")
        q = HistoryFilters.apply(.chip(.archived), to: q, context: "p-hesper")
        f = HistoryFilters.make(q, context: "p-hesper", contextName: "hesper", machines: macs, projects: Self.projects)
        #expect(f.map(\.title) == ["hesper", "mini", "Claude", "last 7 days", "archived"])
        #expect(f.allSatisfy { $0.active })
        #expect(f[0].options.first { $0.id == "this" }?.on == true)
        #expect(f[2].options.map(\.on) == [true, false])
        #expect(f[0].label == "Project: hesper")

        // The current project again: all projects.
        q = HistoryFilters.apply(.scope(.project("p-hesper")), to: q, context: "p-hesper")
        #expect(q.scope == .all)
        q = HistoryFilters.apply(.scope(.elsewhere), to: q, context: nil)
        #expect(HistoryFilters.make(q, context: nil, contextName: nil, machines: [], projects: Self.projects)[0].title == "folder gone")
        #expect(HistoryFilters.apply(.header, to: q, context: nil) == q)
    }
}
