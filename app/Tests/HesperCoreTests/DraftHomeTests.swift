import Foundation
import Testing
@testable import HesperCore

/// New agents per window (docs/rebuild-contract.md "As built — new agents
/// per window"): every draft is visible in exactly one open wall window,
/// presets per scope, the lock, and the "started elsewhere" decision.
@Suite struct DraftHomeTests {
    static let catalog = ProjectCatalog(projects: [
        Project(id: "as", name: "acme-apps", kind: .repo, paths: ["L": "/p/acme-apps"], groups: ["g1"], lastUsed: Date(timeIntervalSince1970: 100)),
        Project(id: "ios", name: "ios-app", kind: .package, parentId: "as", paths: ["L": "/p/acme-apps/ios"]),
        Project(id: "ed", name: "design-system", kind: .repo, paths: ["L": "/p/ed"], groups: ["g1"], lastUsed: Date(timeIntervalSince1970: 500)),
        Project(id: "gh", name: "hesper", kind: .repo, paths: ["L": "/p/hesper"], groups: ["g2"]),
    ], groups: [
        ProjectGroup(id: "g1", name: "acme apps", projectIds: ["as", "ed"], order: 0),
        ProjectGroup(id: "g2", name: "tools", projectIds: ["gh"], order: 1),
    ])
    var c: ProjectCatalog { Self.catalog }

    func draft(_ id: String, wall: String?, band: String? = nil) -> Draft {
        var d = Draft(id: id, text: id)
        d.wall = wall
        d.band = band
        return d
    }

    /// Every draft in exactly one open wall, for a window set.
    func expectExactlyOnce(_ drafts: [Draft], open: [OpenWall], main: String = "main", _ note: Comment) {
        let acting = DraftHome.actingMain(openWalls: open, main: main)
        let ids = open.map(\.id)
        for d in drafts {
            let shownBy = ids.filter { w in DraftHome.visible(drafts: [d], openWalls: ids, mainWall: acting, this: w).contains(d) }
            #expect(shownBy.count == 1, "\(note): \(d.id) shown by \(shownBy)")
        }
    }

    static let all: [Draft] = [
        Draft(id: "d-legacy", text: "made before walls owned drafts"),
        { var d = Draft(id: "d-main", text: "x"); d.wall = "main"; return d }(),
        { var d = Draft(id: "d-w2", text: "x"); d.wall = "wall-2"; return d }(),
        { var d = Draft(id: "d-proj", text: "x"); d.wall = "wall-gh"; d.band = "gh"; d.projectLocked = true; return d }(),
        { var d = Draft(id: "d-gone", text: "x"); d.wall = "wall-closed"; d.band = "as"; return d }(),
    ]

    @Test func everyDraftIsVisibleInExactlyOneOpenWall() {
        let sets: [(String, [OpenWall])] = [
            ("only main open", [OpenWall(id: "main", scope: .all)]),
            ("main closed, Wall 2 and a project window", [OpenWall(id: "wall-2", scope: .all), OpenWall(id: "wall-gh", scope: .project("gh"))]),
            ("several walls", [OpenWall(id: "main", scope: .all), OpenWall(id: "wall-2", scope: .overflow), OpenWall(id: "wall-gh", scope: .project("gh")),
                               OpenWall(id: "wall-g1", scope: .group("g1"))]),
            ("main closed, only a project window", [OpenWall(id: "wall-gh", scope: .project("gh"))]),
            ("main scoped to a project", [OpenWall(id: "main", scope: .project("as")), OpenWall(id: "wall-2", scope: .all)]),
        ]
        for (note, open) in sets { expectExactlyOnce(Self.all, open: open, Comment(rawValue: note)) }
    }

    @Test func aDraftShowsInItsOwnWindowWhateverItsScopeOrOrder() {
        let ids = ["main", "wall-2", "wall-gh"]
        #expect(DraftHome.visible(drafts: Self.all, openWalls: ids, mainWall: "main", this: "wall-2").map(\.id) == ["d-w2"])
        #expect(DraftHome.visible(drafts: Self.all, openWalls: ids, mainWall: "main", this: "wall-gh").map(\.id) == ["d-proj"],
                "a project window shows the drafts made there")
        #expect(DraftHome.visible(drafts: Self.all, openWalls: ids, mainWall: "main", this: "main").map(\.id) == ["d-legacy", "d-main", "d-gone"],
                "main: its own, the legacy ones, those of closed windows")
    }

    @Test func theActingMainWall() {
        #expect(DraftHome.actingMain(openWalls: [OpenWall(id: "wall-gh", scope: .project("gh")), OpenWall(id: "main", scope: .all)], main: "main") == "main")
        #expect(DraftHome.actingMain(openWalls: [OpenWall(id: "wall-gh", scope: .project("gh")), OpenWall(id: "wall-3", scope: .overflow),
                                                 OpenWall(id: "wall-2", scope: .all)], main: "main") == "wall-2", "the first open All wall")
        #expect(DraftHome.actingMain(openWalls: [OpenWall(id: "wall-gh", scope: .project("gh")), OpenWall(id: "wall-3", scope: .overflow)], main: "main")
                == "wall-gh", "no All wall: the first open wall")
        #expect(DraftHome.actingMain(openWalls: [], main: "main") == nil)
    }

    @Test func closingTheOwnerMovesItsDraftsToMainKeepingTheirBand() {
        let released = DraftHome.released(Self.all, closing: "wall-gh")
        #expect(released.map(\.id) == ["d-proj"] && released[0].wall == nil && released[0].band == "gh")
        let ids = ["main", "wall-2"]
        #expect(DraftHome.visible(drafts: released, openWalls: ids, mainWall: "main", this: "main").map(\.id) == ["d-proj"])
    }

    @Test func theWallSurvivesEncodingAndOldDraftsHaveNone() throws {
        var d = Draft(id: "d-w", text: "x")
        d.wall = "wall-ab12"
        d.band = "gh"
        d.projectLocked = true
        let data = try JSONEncoder().encode(d)
        let s = String(decoding: data, as: UTF8.self)
        #expect(s.contains("\"wall\":\"wall-ab12\"") && s.contains("\"projectLocked\":true"))
        let back = try JSONDecoder().decode(Draft.self, from: data)
        #expect(back.wall == "wall-ab12" && back.projectLocked && back.band == "gh")
        let old = try JSONDecoder().decode(Draft.self, from: Data(#"{"id":"d-old","text":"x","wall":""}"#.utf8))
        #expect(old.wall == nil && !old.projectLocked)
        #expect(!String(decoding: try JSONEncoder().encode(Draft(id: "d-n")), as: UTF8.self).contains("projectLocked"))
    }

    // MARK: Presets per scope

    @Test func presetsFollowTheWindowsScope() {
        #expect(DraftPreset.make(scope: .all, catalog: c) == DraftPreset(), "All: no project (the New area)")
        #expect(DraftPreset.make(scope: .overflow, catalog: c) == DraftPreset())
        #expect(DraftPreset.make(scope: .project("gh"), catalog: c) == DraftPreset(projectID: "gh", locked: true))
        let g = DraftPreset.make(scope: .group("g1"), catalog: c)
        #expect(g.projectID == "ed" && !g.locked, "a group: the most recently used of its projects")
        #expect(g.choices == ["ed", "as", "ios"], "the chip offers the group's projects, recent first (packages through their repository)")
        #expect(DraftPreset.make(scope: .filter(WallFilter(machines: ["M", "A"])), catalog: c) == DraftPreset(machine: "A"))
        #expect(DraftPreset.make(scope: .filter(WallFilter(kinds: ["codex"])), catalog: c) == DraftPreset())
    }

    @Test func aBandInAProjectWindowLocks() {
        #expect(DraftPreset.locks(projectID: "ios", scope: .project("as"), catalog: c), "a package band of the window's project")
        #expect(DraftPreset.locks(projectID: "as", scope: .project("as"), catalog: c))
        #expect(!DraftPreset.locks(projectID: "ed", scope: .project("as"), catalog: c))
        #expect(!DraftPreset.locks(projectID: "as", scope: .all, catalog: c))
        #expect(!DraftPreset.locks(projectID: nil, scope: .project("as"), catalog: c))
    }

    // MARK: The lock

    @Test func anotherProjectsFolderUnlocks() {
        var d = Draft(id: "d-l", text: "x", project: "/p/hesper")
        d.band = "gh"
        d.projectLocked = true
        #expect(DraftLock.check(d, folderProject: "gh", catalog: c).projectLocked, "its own folder keeps the lock")
        #expect(!DraftLock.check(d, folderProject: "ed", catalog: c).projectLocked, "another project's folder unlocks")
        #expect(!DraftLock.check(d, folderProject: "scratch:/tmp/x", catalog: c).projectLocked, "a folder in no project unlocks")
        var sub = d
        sub.band = "as"
        #expect(DraftLock.check(sub, folderProject: "ios", catalog: c).projectLocked, "a package of the project keeps it")
        var none = d
        none.project = nil
        #expect(DraftLock.check(none, folderProject: nil, catalog: c).projectLocked, "no folder yet: still locked")
    }

    // MARK: Started elsewhere

    @Test func startedElsewherePointsToTheWindowThatShowsIt() {
        let walls: [(id: String, shows: Set<String>)] = [("main", ["a1", "new"]), ("wall-gh", ["a2"]), ("wall-news", ["new2"])]
        #expect(StartLanding.elsewhere(agent: "new", this: "main", walls: walls, mainWall: "main") == nil, "lands here: nothing to say")
        #expect(StartLanding.elsewhere(agent: "new2", this: "wall-gh", walls: walls, mainWall: "main") == "wall-news", "the window that shows it")
        #expect(StartLanding.elsewhere(agent: "lost", this: "wall-gh", walls: walls, mainWall: "main") == "main", "nobody shows it: the main wall")
    }
}
