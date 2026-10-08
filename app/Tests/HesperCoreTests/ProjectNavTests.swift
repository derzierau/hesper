import Foundation
import Testing
@testable import HesperCore

/// The project sidebar as a navigator (ProjectNav): Active, Recent, the
/// full list, search, scratch folding, duplicate names.
@Suite struct ProjectNavTests {
    static let now = Date(timeIntervalSince1970: 2_000_000_000)
    static func ago(_ s: TimeInterval) -> Date { now.addingTimeInterval(-s) }

    static let machines = [Machine(short: "L", name: "ABC123456", online: true), Machine(short: "mini", name: "XYZ987654 Mac mini", online: true)]

    static let catalog = ProjectCatalog(projects: [
        Project(id: "news", name: "news-api", kind: .repo, identity: "github.com/example/news-api", paths: ["L": "/Users/o/projects/news-api"],
                lastUsed: ago(3600)),
        Project(id: "gh", name: "hesper", kind: .repo, identity: "github.com/example/hesper", paths: ["L": "/Users/o/projects/hesper"],
                groups: ["g2"]),
        Project(id: "as", name: "acme-apps", kind: .repo, paths: ["L": "/Users/o/p/acme-apps", "mini": "/Users/o/p/acme-apps"], groups: ["g1"],
                lastUsed: ago(86400 * 2)),
        Project(id: "ios", name: "ios-app", kind: .package, parentId: "as", paths: ["L": "/Users/o/p/acme-apps/apps/ios-app"]),
        Project(id: "ed", name: "design-system", kind: .repo, paths: ["L": "/Users/o/p/design-system"], lastUsed: ago(60 * 5)),
        Project(id: "t3a", name: "t3code", kind: .repo, paths: ["L": "/Users/o/projects/t3code"], lastUsed: ago(86400 * 3)),
        Project(id: "t3b", name: "t3code", kind: .repo, paths: ["mini": "/Users/o/projects/t3code"], lastUsed: ago(86400 * 4)),
        Project(id: "t3c", name: "t3code", kind: .repo, paths: ["L": "/Users/o/old/t3code"]),
        Project(id: "old", name: "ancient", kind: .repo, paths: ["L": "/Users/o/projects/ancient"], lastUsed: ago(86400 * 30)),
        // Scratch noise.
        Project(id: "s1", name: "2026-10-06-please-understand-the-relay", kind: .folder, identity: "github.com/example/news-api",
                paths: ["L": "/Users/o/scratch/2026-10-06-please-understand-the-relay"], lastUsed: ago(600)),
        Project(id: "s2", name: "2026-10-04-scratch-check", kind: .folder, paths: ["L": "/Users/o/scratch/2026-10-04-scratch-check"]),
        Project(id: "wt", name: "agent-a81c", kind: .repo, identity: "github.com/example/hesper",
                paths: ["L": "/Users/o/projects/hesper/.claude/worktrees/agent-a81c"]),
        Project(id: "wt2", name: "feature-x", kind: .repo, paths: ["L": "/Users/o/worktrees/design-system/feature-x"]),
    ], groups: [
        ProjectGroup(id: "g1", name: "acme apps", projectIds: ["as", "ed"], order: 0),
        ProjectGroup(id: "g2", name: "tools", projectIds: ["gh"], order: 1),
    ])

    func agent(_ id: String, _ pid: String, machine: String = "L", state: AgentState = .working, branch: String? = nil, at: TimeInterval = 0) -> Agent {
        var a = Agent(id: "\(machine)/\(id)", machine: machine, state: state, projectId: pid, branch: branch)
        a.stateSince = Self.now.addingTimeInterval(-at)
        return a
    }

    func make(_ agents: [Agent], _ state: ProjectNavState = ProjectNavState(), history: Bool = false) -> [ProjectNavSection] {
        ProjectNav.make(catalog: Self.catalog, agents: agents, machines: Self.machines, local: "L", now: Self.now, state: state, historyAvailable: history)
    }

    // MARK: Sections

    @Test func activeShowsOnlyProjectsWithAgentsMostUrgentFirst() {
        let a = [agent("1", "news", machine: "mini", at: 30), agent("2", "news", machine: "mini", state: .done, at: 10), agent("3", "ed", state: .approval, at: 500),
                 agent("4", "t3b", machine: "mini", state: .done, at: 5), agent("5", "gh", branch: "feat/x", at: 100)]
        let s = make(a)
        #expect(s.map(\.id) == ["all", "active", "recent", "more"])
        let all = s[0].rows[0]
        #expect(all.title == "All agents" && all.tally == { var t = StateTally(); t.needsYou = 1; t.working = 2; t.done = 2; t.total = 5; return t }())
        #expect(all.tally.parts.map(\.kind) == [.needsYou, .working, .done])
        let active = s[1].rows
        // acme apps (needs you) → news-api (working, newer) → tools (working) → t3code (done only).
        #expect(active.map(\.id) == ["g:g1", "p:news", "g:g2", "p:t3b"], "\(active.map(\.id))")
        #expect(active[0].tally.needsYou == 1 && active[0].expandable && !active[0].expanded)
        #expect(active[1].meta == "mini", "every agent on another Mac")
        #expect(active[3].suffix == "mini" && active[3].meta == nil, "a duplicate name gets its machine (once)")
    }

    @Test func groupsExpandToTheirActiveProjects() {
        var st = ProjectNavState()
        st.expandedGroups = ["g2"]
        let s = make([agent("5", "gh", branch: "feat/x"), agent("6", "ios")], st)
        let ids = s[1].rows.map(\.id)
        #expect(ids.contains("g:g2/p:gh") && ids.firstIndex(of: "g:g2/p:gh") == ids.firstIndex(of: "g:g2")! + 1)
        let gh = s[1].rows.first { $0.id == "g:g2/p:gh" }!
        #expect(gh.meta == "feat/x" && gh.depth == 1 && gh.base?.scope == .project("gh"))
        #expect(ids.contains("g:g1") && !ids.contains("g:g1/p:as"), "collapsed: only the group")
        let g1 = s[1].rows.first { $0.id == "g:g1" }!
        #expect(g1.tally.total == 1 && g1.base?.scope == .group("g1"), "a package's agent counts for its repository's group")
    }

    @Test func recentListsQuietProjectsOfTheLastWeek() {
        let s = make([agent("1", "news")])
        let recent = s.first { $0.id == "recent" }!.rows
        #expect(recent.map(\.projectID) == ["ed", "as", "t3a", "t3b"], "newest first, live and old ones left out, scratch never")
        #expect(recent.map(\.meta) == ["5m", "2d", "3d", "4d"])
        #expect(recent.allSatisfy { $0.quiet })
        let more = s.last!.rows[0]
        #expect(more.kind == .allProjects && more.meta == "8")
    }

    @Test func selectedQuietProjectOffersActions() {
        var st = ProjectNavState()
        st.selectedProject = "ed"
        let rows = make([], st, history: true).first { $0.id == "recent" }!.rows
        #expect(rows.map(\.kind).prefix(3) == [.project, .newAgent, .history])
        #expect(rows[1].projectID == "ed")
        #expect(make([], st, history: false).first { $0.id == "recent" }!.rows.map(\.kind).prefix(3) == [.project, .newAgent, .project])
    }

    @Test func scratchWithAgentsShowsUnderItsParentsName() {
        let s = make([agent("1", "s1", state: .approval), agent("2", "wt"), agent("3", "s2")])
        let active = s[1].rows
        let s1 = active.first { $0.projectID == "s1" }!
        #expect(s1.title == "news-api" && s1.suffix == "please-understand-the-relay", "same git remote")
        let wt = active.first { $0.projectID == "wt" }!
        #expect(wt.title == "hesper" && wt.suffix == "agent-a81c" && wt.depth == 0, "a worktree stays out of its parent's group")
        let s2 = active.first { $0.projectID == "s2" }!
        #expect(s2.title == "scratch-check" && s2.suffix == nil)
    }

    @Test func fullListFoldsScratchAndWorktrees() {
        var st = ProjectNavState()
        st.showAll = true
        let s = make([], st)
        #expect(s.map(\.id) == ["all", "groups", "projects", "scratch"])
        let titles = s[2].rows.map { [$0.title, $0.suffix].compactMap { $0 }.joined(separator: " · ") }
        #expect(titles == ["acme-apps", "ancient", "design-system", "hesper", "news-api", "t3code · mini", "t3code · ~/old", "t3code · ~/projects"],
                "\(titles)")
        #expect(s[3].rows.map(\.id) == [ProjectNav.scratchRow] && s[3].rows[0].meta == "1", "only the scratch folder no project claims")
        st.scratchExpanded = true
        st.expandedGroups = ["g1"]
        let open = make([], st)
        #expect(open[3].rows.map(\.projectID) == [nil, "s2"])
        #expect(open[1].rows.map(\.id) == ["g:g1", "g:g1/p:as", "g:g1/p:ed", "g:g2"])
    }

    @Test func searchMatchesEverything() {
        var st = ProjectNavState()
        st.query = "news"
        let s = make([agent("1", "s1")], st)
        #expect(s.map(\.id) == ["all", "results"])
        let r = s[1].rows
        #expect(r.first?.projectID == "s1", "live first among equals")
        #expect(r.map(\.projectID).contains("news"))
        #expect(s[1].title == "2 matches")
        st.query = "OLD"
        #expect(make([], st)[1].rows.map(\.projectID) == ["t3c"], "a folder path matches")
        st.query = "zzz"
        #expect(make([], st)[1].rows.map(\.kind) == [.empty])
        st.query = "tools"
        #expect(make([], st)[1].rows.first?.kind == .group)
    }

    // MARK: Rules

    @Test func scoring() {
        #expect(ProjectNav.score("news", name: "news-api", paths: []) == 0)
        #expect(ProjectNav.score("api", name: "news-api", paths: []) == 1)
        #expect(ProjectNav.score("ews", name: "news-api", paths: []) == 2)
        #expect(ProjectNav.score("nai", name: "news-api", paths: []) == 3)
        #expect(ProjectNav.score("users", name: "x", paths: ["/Users/o/x"]) == 4)
        #expect(ProjectNav.score("q", name: "x", paths: ["/a"]) == nil)
    }

    @Test func dateAndWorktreeDetection() {
        #expect(ProjectNav.isDatePrefixed("2026-10-04-scratch-check"))
        #expect(!ProjectNav.isDatePrefixed("2026-10-04"))
        #expect(!ProjectNav.isDatePrefixed("v2026-10-04-x"))
        #expect(ProjectNav.label("2026-10-06-please-understand") == "please-understand")
        #expect(ProjectNav.label("hesper") == "hesper")
        #expect(ProjectNav.isWorktreePath("/u/p/hesper/.claude/worktrees/agent-1"))
        #expect(ProjectNav.isWorktreePath("/u/worktrees/repo/feature"))
        #expect(ProjectNav.isWorktreePath("/u/repo.worktrees/feature"))
        #expect(ProjectNav.isWorktreePath("/u/p/repo/.worktrees/x"))
        #expect(!ProjectNav.isWorktreePath("/u/worktrees/repo"), "a repo kept in a folder named worktrees")
        #expect(!ProjectNav.isWorktreePath("/u/p/hesper"))
        let design = Self.catalog.projects["ed"]!
        #expect(ProjectNav.parent(of: Self.catalog.projects["wt2"]!, in: [design]) == "ed", "named after its worktrees folder")
        #expect(ProjectNav.parent(of: Self.catalog.projects["s2"]!, in: Array(Self.catalog.projects.values)) == nil)
    }

    @Test func duplicateNames() {
        let ps = ["t3a", "t3b", "t3c", "news"].map { Self.catalog.projects[$0]! }
        let d = ProjectNav.disambiguate(ps, machines: Self.machines)
        #expect(d["t3b"] == "mini" && d["t3a"] == "~/projects" && d["t3c"] == "~/old" && d["news"] == nil, "\(d)")
        #expect(ProjectNav.folderLabel("/Users/o/old") == "~/old")
        #expect(ProjectNav.folderLabel("/Users/o") == "~")
        #expect(ProjectNav.folderLabel("/srv/x/clones") == "…/clones")
        // The same folder on two Macs, as two projects: the machines.
        let twin = [Project(id: "a", name: "x", paths: ["L": "/Users/o/x"]), Project(id: "b", name: "x", paths: ["L": "/Users/o/x", "mini": "/Users/o/x"])]
        let t = ProjectNav.disambiguate(twin, machines: Self.machines)
        #expect(t["a"] != t["b"])
    }

    @Test func ages() {
        #expect(ProjectNav.age(Self.ago(10), now: Self.now) == "now")
        #expect(ProjectNav.age(Self.ago(60 * 12), now: Self.now) == "12m")
        #expect(ProjectNav.age(Self.ago(3600 * 2), now: Self.now) == "2h")
        #expect(ProjectNav.age(Self.ago(86400 * 3), now: Self.now) == "3d")
        #expect(ProjectNav.age(Self.ago(86400 * 21), now: Self.now) == "3w")
    }

    @Test func keyboardSteps() {
        let s = make([agent("1", "news")])
        let rows = ProjectNav.flat(s)
        #expect(ProjectNav.step(rows, from: nil, by: 1) == "all")
        #expect(ProjectNav.step(rows, from: "all", by: 1) == "p:news")
        #expect(ProjectNav.step(rows, from: "all", by: -1) == "all")
        #expect(ProjectNav.step(rows, from: nil, by: -1) == ProjectNav.allProjectsRow)
    }

    @Test func metrics() {
        #expect(ProjectNav.Metrics.width == 232)
        #expect(ProjectNav.Metrics.rowHeight(.comfortable) == 28 && ProjectNav.Metrics.rowHeight(.compact) == 24)
    }
}
