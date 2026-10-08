import Foundation
import Testing
@testable import HesperCore

/// Scratch projects (Scratch.swift and the scratch parts of ProjectNav /
/// ViewResolver / Sessions): naming, states, actions, the draft's "New
/// scratch", the Scratch band, the sidebar's sections, settings.
@Suite struct ScratchTests {
    static let now = Date(timeIntervalSince1970: 2_000_000_000)
    static func ago(_ h: Double) -> Date { now.addingTimeInterval(-h * 3600) }

    static func scratch(_ id: String, _ name: String, _ st: ScratchState?, keep: Bool = false, used: Double = 1) -> Project {
        Project(id: id, name: name, kind: .scratch, paths: ["L": "/Users/o/scratch/2026-10-08-" + name.replacingOccurrences(of: " ", with: "-")],
                lastUsed: ago(used), scratch: st.map { ScratchInfo(state: $0, keep: keep, home: "L") })
    }

    static let catalog = ProjectCatalog(projects: [
        Project(id: "gh", name: "hesper", kind: .repo, paths: ["L": "/Users/o/projects/hesper"], lastUsed: ago(2)),
        scratch("s-csv", "csv cleanup", .active, used: 0.1),
        scratch("s-font", "font check", .resting, keep: true, used: 20),
        scratch("s-icon", "icon variants", .resting, used: 50),
        scratch("s-old", "old notes", .archived, used: 900),
        Project(id: "f1", name: "2026-10-04-loose-folder", kind: .folder, paths: ["L": "/Users/o/scratch/2026-10-04-loose-folder"]),
    ])

    func agent(_ id: String, _ pid: String, state: AgentState = .working) -> Agent {
        var a = Agent(id: "L/\(id)", machine: "L", state: state, projectId: pid)
        a.stateSince = Self.now.addingTimeInterval(-60)
        return a
    }

    // MARK: Naming

    @Test func namesFollowHesperdsSlug() {
        #expect(ScratchName.slug("Clean up the CSV export!") == "clean-up-the-csv-export")
        #expect(ScratchName.preview(task: "Clean up the CSV export\nand more") == "clean up the csv export")
        #expect(ScratchName.preview(task: "  ") == nil && ScratchName.preview(task: "…") == nil)
        #expect(ScratchName.slug("!!!") == "task")
        let long = ScratchName.slug("one two three four five six seven eight nine ten eleven")
        #expect(long.count <= ScratchName.limit && long == "one-two-three-four-five-six-seven-eight")
        #expect(ScratchName.slug(String(repeating: "x", count: 60)).count == 40, "one long word is cut")
        var cal = Calendar(identifier: .gregorian)
        cal.timeZone = TimeZone(identifier: "UTC")!
        #expect(ScratchName.folder(task: "CSV cleanup", date: Self.now, calendar: cal) == "2033-05-18-csv-cleanup")
        #expect(ScratchName.display(Project(id: "x", name: "2026-10-08-csv-cleanup", kind: .scratch)) == "csv-cleanup")
        #expect(ScratchName.display(Project(id: "x", name: "csv cleanup", kind: .scratch)) == "csv cleanup")
    }

    // MARK: Wire

    @Test func projectsCarryTheirScratchState() throws {
        let json = #"{"id":"s1","name":"csv cleanup","kind":"scratch","paths":{"L":"/s"},"scratch":{"state":"archived","keep":true,"archivedAt":"2026-10-01T10:00:00Z","home":"L"}}"#
        let p = try JSONDecoder().decode(Project.self, from: Data(json.utf8))
        #expect(p.scratch == ScratchInfo(state: .archived, keep: true, archivedAt: p.scratch?.archivedAt, home: "L") && p.scratch?.archivedAt != nil)
        let back = try JSONDecoder().decode(Project.self, from: JSONEncoder().encode(p))
        #expect(back.scratch == p.scratch)
        let odd = try JSONDecoder().decode(Project.self, from: Data(#"{"id":"s2","kind":"scratch","scratch":{"state":"weird"}}"#.utf8))
        #expect(odd.scratch?.state == .resting && odd.scratch?.keep == false)
        let old = try JSONDecoder().decode(Project.self, from: Data(#"{"id":"s3","kind":"scratch"}"#.utf8))
        #expect(old.scratch == nil)
    }

    @Test func spawnSendsScratchWithoutAProject() {
        var r = SpawnRequest(project: "", task: "csv cleanup")
        r.scratch = true
        #expect(r.params["scratch"] == .bool(true) && r.params["project"] == nil && r.params["task"]?.stringValue == "csv cleanup")
        #expect(SpawnRequest(project: "/p", task: "t").params["scratch"] == nil)
    }

    @Test func missingMeansNoScratchesOnThatMac() {
        #expect(ScratchSupport.missing(RPCError(code: -32601, message: "no method", kind: .notFound)))
        #expect(ScratchSupport.missing(RPCError(code: -32000, message: "Unknown method projects.scratch", kind: .remote)))
        #expect(ScratchSupport.missing(RPCError(code: -32602, message: "project must be an absolute path", kind: .remote)), "an older hesperd's spawn")
        #expect(!ScratchSupport.missing(RPCError(code: -32000, message: "machine mini is offline", kind: .remote)))
        var s = ScratchSupport()
        #expect(!s.supported("L", local: "L") && !s.supported("mini", local: "L"), "unknown: no scratches")
        s.byMachine["L"] = true
        #expect(s.supported("L", local: "L") && s.supported("mini", local: "L"), "another Mac goes through this hesperd until it says no")
        s.byMachine["mini"] = false
        #expect(!s.supported("mini", local: "L"))
    }

    @Test func settingsReadInEveryShape() {
        #expect(ScratchSettings(json: ["scratch.archiveAfterDays": 7, "scratch.deleteAfterDays": 60]) == ScratchSettings(archiveAfterDays: 7, deleteAfterDays: 60))
        #expect(ScratchSettings(json: ["scratch": ["archiveAfterDays": 3]]) == ScratchSettings(archiveAfterDays: 3, deleteAfterDays: 30))
        #expect(ScratchSettings(json: ["values": ["scratch.deleteAfterDays": "45"]]) == ScratchSettings(archiveAfterDays: 14, deleteAfterDays: 45))
        #expect(ScratchSettings(json: ["other": 1]) == nil && ScratchSettings(json: nil) == nil)
        #expect(ScratchSettings.setParams(ScratchSettings.archiveKey, days: 900)["values"]?[ScratchSettings.archiveKey] == .number(365))
    }

    // MARK: States and actions

    @Test func statesFollowLiveAgents() {
        let c = Self.catalog
        #expect(ScratchLifecycle.state(c.project("s-font"), live: true) == .active)
        #expect(ScratchLifecycle.state(c.project("s-csv"), live: false) == .resting, "hesperd's active without agents is resting")
        #expect(ScratchLifecycle.state(c.project("s-old"), live: true) == .archived)
        #expect(ScratchLifecycle.state(c.project("gh"), live: true) == nil)
        #expect(ScratchLifecycle.state(Project(id: "v", name: "v", kind: .scratch), live: false) == .resting, "no state from an older hesperd")
        #expect(!ScratchLifecycle.isLifecycle(c.project("scratch:/tmp/x")), "the app's own made-up scratch")
    }

    @Test func actionsDependOnStateAgentsAndSupport() {
        let c = Self.catalog
        let idle = ScratchActions.available(c.project("s-icon"), live: 0, supported: true)
        #expect(idle.map(\.action) == [.rename, .keep, .promote, .archive, .delete] && idle.allSatisfy(\.enabled))
        let kept = ScratchActions.available(c.project("s-font"), live: 0, supported: true)
        #expect(kept.map(\.action).contains(.unkeep) && !kept.map(\.action).contains(.keep))
        let busy = ScratchActions.available(c.project("s-csv"), live: 2, supported: true)
        #expect(busy.filter { !$0.enabled }.map(\.action) == [.promote, .archive, .delete])
        #expect(busy.first { $0.action == .promote }?.reason == ScratchActions.busyReason)
        #expect(ScratchActions.available(c.project("s-old"), live: 0, supported: true).map(\.action) == [.restore, .delete])
        #expect(ScratchActions.available(c.project("s-icon"), live: 0, supported: false).isEmpty, "an older hesperd")
        #expect(ScratchActions.available(c.project("gh"), live: 0, supported: true).isEmpty)
        #expect(ScratchActions.available(c.project("f1"), live: 0, supported: true).isEmpty, "a folder, not hesperd's scratch")
        #expect(ScratchActions.deleteMessage(c.project("s-icon")!).contains("“icon variants”"))
    }

    // MARK: The draft

    @Test func draftsWithoutAFolderStartAScratch() {
        #expect(ScratchDraft.starts(project: nil, cloneURL: nil, locked: false, supported: true))
        #expect(!ScratchDraft.starts(project: "/p", cloneURL: nil, locked: false, supported: true), "a #folder replaces it")
        #expect(!ScratchDraft.starts(project: nil, cloneURL: "git@x:y.git", locked: false, supported: true))
        #expect(!ScratchDraft.starts(project: nil, cloneURL: nil, locked: true, supported: true), "a project window's draft")
        #expect(!ScratchDraft.starts(project: nil, cloneURL: nil, locked: false, supported: false), "today's Choose folder")
        #expect(ScratchDraft.detail(task: "CSV cleanup") == "csv cleanup")
    }

    // MARK: The band

    @Test func scratchAgentsShareOneCollapsedBand() {
        let c = Self.catalog
        let items = [ViewItem(id: "1", projectID: "gh"), ViewItem(id: "2", projectID: "s-csv"), ViewItem(id: "3", projectID: "s-icon"),
                     ViewItem(id: "4", projectID: nil)]
        let v = ViewResolver.resolve(items, level: .project, catalog: c)
        #expect(v.bands.map(\.key) == ["p:gh", "p:~scratch", "p:~none"], "\(v.bands.map(\.key))")
        let s = v.bands[1]
        #expect(s.title == "Scratch" && s.members == ["2", "3"] && s.projectID == nil && s.wallScope == nil)
        #expect(s.subtitle == "csv cleanup, icon variants")
        let g = ViewResolver.resolve(items, level: .group, catalog: c)
        #expect(g.band(key: "g:~scratch")?.members == ["2", "3"] && g.band(key: "g:~scratch")?.title == "Scratch")
        #expect(ViewResolver.collapsedByDefault("p:~scratch") && ViewResolver.collapsedByDefault("g:~scratch") && !ViewResolver.collapsedByDefault("p:gh"))
    }

    @Test func collapseRemembersAnOpenedScratchBand() {
        var c: Set<String> = [], e: Set<String> = []
        #expect(BandCollapse.isCollapsed("g:~scratch", collapsed: c, expanded: e))
        #expect(!BandCollapse.isCollapsed("g:g1", collapsed: c, expanded: e))
        (c, e) = BandCollapse.toggle("g:~scratch", collapsed: c, expanded: e)
        #expect(!BandCollapse.isCollapsed("g:~scratch", collapsed: c, expanded: e) && c.isEmpty && e == ["g:~scratch"])
        (c, e) = BandCollapse.toggle("g:~scratch", collapsed: c, expanded: e)
        #expect(BandCollapse.isCollapsed("g:~scratch", collapsed: c, expanded: e) && c == ["g:~scratch"] && e.isEmpty)
        (c, e) = BandCollapse.open("g:~scratch", collapsed: c, expanded: e)
        #expect(!BandCollapse.isCollapsed("g:~scratch", collapsed: c, expanded: e))
        (c, e) = BandCollapse.toggle("g:g1", collapsed: [], expanded: [])
        #expect(c == ["g:g1"] && e.isEmpty)
    }

    @Test func tileHeadersNameTheScratch() {
        // In the Scratch band the card keeps its place label: the scratch's name.
        let band = ViewResolver.resolve([ViewItem(id: "2", projectID: "s-csv"), ViewItem(id: "1", projectID: "gh")], level: .project, catalog: Self.catalog)
            .band(key: "p:~scratch")
        #expect(!TilePlace.inOwnProjectBand(projectID: "s-csv", band: band, catalog: Self.catalog))
        #expect(TilePlace.label(project: ScratchName.display(Self.catalog.project("s-csv")!), branch: "main", ownBand: false) == "csv cleanup · main")
    }

    // MARK: The sidebar

    func nav(_ agents: [Agent], _ state: ProjectNavState = ProjectNavState()) -> [ProjectNavSection] {
        ProjectNav.make(catalog: Self.catalog, agents: agents, local: "L", now: Self.now, state: state)
    }

    @Test func activeScratchesShowAsScratchRows() {
        let s = nav([agent("1", "s-csv"), agent("2", "gh")])
        let active = s.first { $0.id == "active" }!.rows
        let row = active.first { $0.projectID == "s-csv" }!
        #expect(row.title == "scratch · csv cleanup" && row.suffix == nil)
        #expect(s.map(\.id) == ["all", "active", "scratch", "more"], "\(s.map(\.id))")
        #expect(!(s.first { $0.id == "recent" }?.rows ?? []).contains { $0.projectID?.hasPrefix("s-") == true }, "scratches are never Recent")
    }

    @Test func theFoldListsRestingScratchesArchivedOnDemand() {
        var st = ProjectNavState()
        let closed = nav([agent("1", "s-csv")], st).first { $0.id == "scratch" }!.rows
        #expect(closed.map(\.id) == [ProjectNav.scratchRow] && closed[0].meta == "2", "resting only: font check, icon variants")
        st.scratchExpanded = true
        let open = nav([agent("1", "s-csv")], st).first { $0.id == "scratch" }!.rows
        #expect(open.map(\.projectID) == [nil, "s-font", "s-icon", nil], "newest first, then Show archived")
        #expect(open[1].title == "font check" && open[1].kept && !open[2].kept)
        #expect(open[3].kind == .showArchived && open[3].title == "Show archived" && open[3].meta == "1")
        st.showArchived = true
        let all = nav([agent("1", "s-csv")], st).first { $0.id == "scratch" }!.rows
        #expect(all.last?.projectID == "s-old" && all.last?.archived == true && all.last?.meta == "archived")
        #expect(all.first { $0.kind == .showArchived }?.title == "Hide archived")
        // Without agents the active scratch rests in the fold too.
        st = ProjectNavState()
        #expect(nav([], st).first { $0.id == "scratch" }!.rows[0].meta == "3")
    }

    @Test func theFullListFoldsOlderScratchFoldersToo() {
        var st = ProjectNavState()
        st.showAll = true
        st.scratchExpanded = true
        let fold = nav([agent("1", "s-csv")], st).first { $0.id == "scratch" }!.rows
        #expect(fold[0].meta == "3" && fold.map(\.projectID).contains("f1"), "date-prefixed folders without hesperd's kind")
        #expect(!fold.map(\.projectID).contains("s-csv"), "the live scratch is in Active")
    }

    // MARK: History

    @Test func deletedScratchSessionsSayFolderRemoved() {
        let s = Session(json: ["id": "x", "cwd": "/Users/o/scratch/2026-10-01-old", "folderRemoved": true, "machine": "L"])!
        #expect(s.folderRemoved)
        #expect(Session(json: ["id": "y", "folder": "removed"])?.folderRemoved == true)
        #expect(Session(json: ["id": "z"])?.folderRemoved == false)
        let t = SessionCardText(s, changes: nil, loadingChanges: false, local: "L", machines: [:], home: "/Users/o")
        #expect(t.subtitle.hasSuffix("· folder removed"), "\(t.subtitle)")
        let acts = SearchPreview.actions(s, card: t, local: "L", online: ["L"], machines: [:], restoreScratch: true)
        #expect(acts.contains { $0.key == SearchPreview.restoreScratchKey && $0.title == "Restore scratch" })
        #expect(!SearchPreview.actions(s, card: t, local: "L", online: ["L"], machines: [:]).contains { $0.key == SearchPreview.restoreScratchKey })
    }
}
