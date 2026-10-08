import Foundation
import Testing
@testable import HesperCore

/// Projects (views): scope + grouping + layout (docs/rebuild-contract.md
/// "As built — projects (views)").
@Suite struct ProjectsView {
    // A catalog like the fake daemon's demo: two groups, a repo with a
    // package, a scratch folder, and a project in no group.
    static let catalog = ProjectCatalog(projects: [
        Project(id: "as", name: "acme-apps", color: "blue", kind: .repo, paths: ["L": "/p/acme-apps", "M": "/m/acme-apps"], groups: ["g1"]),
        Project(id: "ios", name: "ios-app", kind: .package, parentId: "as", paths: ["L": "/p/acme-apps/apps/ios-app"]),
        Project(id: "ed", name: "design-system", color: "#ff9e64", kind: .repo, paths: ["L": "/p/design-system"]),
        Project(id: "gh", name: "hesper", kind: .repo, paths: ["L": "/p/hesper"]),
        Project(id: "loose", name: "loose", kind: .repo, paths: ["L": "/p/loose"]),
        Project(id: "tr", name: "hesper-trial", kind: .scratch, paths: ["L": "/s/trial"]),
    ], groups: [
        ProjectGroup(id: "g1", name: "acme apps", projectIds: ["as", "ed"], order: 0),
        ProjectGroup(id: "g2", name: "tools", projectIds: ["gh"], order: 1),
    ])
    var c: ProjectCatalog { Self.catalog }

    func agent(_ id: String, _ path: String?, machine: String = "L", state: AgentState = .working, branch: String? = nil, pid: String? = nil) -> Agent {
        var a = Agent(id: "\(machine)/\(id)", machine: machine, state: state, project: path, projectId: pid, branch: branch)
        a.stateSince = Date(timeIntervalSince1970: Double(id.hashValue & 0xffff))
        return a
    }

    // MARK: Catalog

    @Test func agentsFindTheirProjectByIdThenFolder() {
        #expect(c.projectID(for: agent("a", "/x", pid: "gh")) == "gh")
        #expect(c.projectID(for: agent("a", "/p/acme-apps")) == "as")
        #expect(c.projectID(for: agent("a", "/p/acme-apps/src/x")) == "as")
        #expect(c.projectID(for: agent("a", "/p/acme-apps/apps/ios-app")) == "ios", "a package beats its repo")
        #expect(c.projectID(for: agent("a", "/m/acme-apps", machine: "M")) == "as", "same project on another Mac")
        #expect(c.projectID(for: agent("a", "/elsewhere")) == "scratch:/elsewhere", "no project: hesperd's scratch id")
        #expect(c.project("scratch:/elsewhere")?.name == "elsewhere")
        let old = ProjectCatalog(supported: false) // a daemon without projects
        #expect(old.projectID(for: agent("a", "/elsewhere")) == "path:/elsewhere" && old.project("path:/elsewhere")?.name == "elsewhere")
        #expect(c.groupIDs(of: "ios") == ["g1"], "a package is in its repository's groups")
        #expect(c.lineage("ios") == ["ios", "as"])
        #expect(c.colorHex(project: "as") == "#7aa2f7" && c.colorHex(project: "ed") == "#ff9e64")
        #expect(c.colorHex(project: "ios") == "#7aa2f7", "a package without a color: its repo's")
        #expect(ProjectColor.hex(nil, key: "x") == ProjectColor.hex(nil, key: "x"))
    }

    @Test func projectJSONDecodesTheWire() throws {
        let json = #"{"id":"p1","name":"acme-apps","color":"blue","kind":"repo","identity":"github.com/x","paths":{"L":"/a"},"groups":["g"],"defaults":{"profile":"codex-full"},"detectedPackages":[{"path":"apps/ios"}],"lastUsed":"2026-10-07T10:00:00Z","future":1}"#
        let p = try JSONDecoder().decode(Project.self, from: Data(json.utf8))
        #expect(p.paths["L"] == "/a" && p.defaults?.profile == "codex-full" && p.detectedPackages == ["apps/ios"] && p.lastUsed != nil)
        // hesperd's shape (relay/pkg/wire/projects.go): identity object, colorSet, packages with tool.
        let real = ##"{"id":"p-0123456789abcdef","name":"app","color":"#bb9af7","colorSet":true,"kind":"repo","identity":{"remote":"github.com/o/app"},"paths":{"L":"/a"},"groups":[],"defaults":{},"detectedPackages":[{"path":"apps/web","name":"web","tool":"pnpm"}],"lastUsed":"0001-01-01T00:00:00Z"}"##
        let r = try JSONDecoder().decode(Project.self, from: Data(real.utf8))
        #expect(r.identity == "github.com/o/app" && r.colorSet && r.detectedPackages == ["apps/web"] && r.lastUsed == nil)
        #expect(ProjectCatalog(projects: [r]).colorHex(project: r.id) == "#bb9af7", "the daemon's color as given")
        // Scratch folders: no notifications; known by their id.
        let sc = agent("s", "/tmp/x", pid: "scratch:/tmp/x")
        #expect(c.projectID(for: sc) == "scratch:/tmp/x" && c.project("scratch:/tmp/x")?.isScratch == true)
        #expect(!c.needsRefresh(for: agent("k", "/p/acme-apps", pid: "as")) && c.needsRefresh(for: agent("u", "/q", pid: "p-new")))
        let odd = try JSONDecoder().decode(Project.self, from: Data(#"{"id":"p2","kind":"weird"}"#.utf8))
        #expect(odd.kind == .unknown && odd.name == "p2" && odd.paths.isEmpty)
        let g = try JSONDecoder().decode(ProjectGroup.self, from: Data(#"{"id":"g","name":"tools","projectIds":["p1"],"order":2}"#.utf8))
        #expect(g.order == 2 && g.projectIds == ["p1"])
        let a = try JSONDecoder().decode(Agent.self, from: Data(#"{"id":"L/x","state":"working","projectId":"p1"}"#.utf8))
        #expect(a.projectId == "p1")
    }

    // MARK: Scope and grouping resolution

    @Test func groupingGoesOneLevelBelowTheScope() {
        #expect(ViewGrouping.level(.auto, scope: .all, hasGroups: true) == .group)
        #expect(ViewGrouping.level(.auto, scope: .all, hasGroups: false) == .project)
        #expect(ViewGrouping.level(.auto, scope: .overflow, hasGroups: true) == .group)
        #expect(ViewGrouping.level(.auto, scope: .filter(WallFilter()), hasGroups: true) == .group)
        #expect(ViewGrouping.level(.auto, scope: .group("g1"), hasGroups: true) == .project)
        #expect(ViewGrouping.level(.auto, scope: .project("as"), hasGroups: true) == .branch)
        #expect(ViewGrouping.level(.none, scope: .all, hasGroups: true) == .none)
        #expect(ViewGrouping.level(.group, scope: .all, hasGroups: false) == .project, "no groups: by project")
        #expect(ViewGrouping.level(.branch, scope: .all, hasGroups: true) == .branch)
    }

    func items(_ agents: [Agent]) -> [ViewItem] {
        agents.map { ViewItem(id: $0.id, projectID: c.projectID(for: $0), branch: $0.branch, worktree: $0.worktree) }
    }

    @Test func bandsByGroupInGroupOrderThenOtherScratchNone() {
        let a = [agent("1", "/p/hesper"), agent("2", "/s/trial"), agent("3", "/p/acme-apps"), agent("4", "/p/loose"),
                 agent("5", nil), agent("6", "/p/design-system"), agent("7", "/p/acme-apps/apps/ios-app")]
        let v = ViewResolver.resolve(items(a), level: .group, catalog: c)
        #expect(v.bands.map(\.key) == ["g:g1", "g:g2", "g:~other", "g:~scratch", "g:~none"])
        #expect(v.bands[0].members == ["L/3", "L/6", "L/7"], "wall order inside a band")
        #expect(v.bands[0].title == "acme apps" && v.bands[0].subtitle == "acme-apps, design-system")
        #expect(v.bands[3].title == "scratch")
        #expect(v.order == ["L/3", "L/6", "L/7", "L/1", "L/4", "L/2", "L/5"])
        #expect(v.showsBands)
        // The wall's own order (header drag) comes first.
        let mine = ViewResolver.resolve(items(a), level: .group, catalog: c, bandOrder: ["g:g2", "g:~scratch"])
        #expect(mine.bands.map(\.key) == ["g:g2", "g:~scratch", "g:g1", "g:~other", "g:~none"])
    }

    @Test func bandsByProjectFoldPackagesIntoTheirRepo() {
        let a = [agent("1", "/p/acme-apps/apps/ios-app"), agent("2", "/p/hesper"), agent("3", "/p/acme-apps")]
        let v = ViewResolver.resolve(items(a), level: .project, catalog: c)
        #expect(v.bands.map(\.key) == ["p:as", "p:gh"])
        #expect(v.bands[0].members == ["L/1", "L/3"] && v.bands[0].projectID == "as" && v.bands[0].subtitle == "ios-app")
        #expect(v.bands[0].wallScope == .project("as"))
    }

    @Test func projectScopeGroupsByPackageBranchWorktree() {
        var w = agent("4", "/p/acme-apps", branch: "fix/x")
        w.worktree = "/wt/acme-apps/fix-x"
        let a = [agent("1", "/p/acme-apps", branch: "feature/push"), agent("2", "/p/acme-apps"), agent("3", "/p/acme-apps/apps/ios-app"), w,
                 agent("5", "/p/acme-apps", branch: "feature/push")]
        let v = ViewResolver.resolve(items(a), level: .branch, catalog: c, scope: .project("as"))
        #expect(v.bands.map(\.key) == ["b:main", "b:pkg:ios", "b:br:feature/push", "b:br:fix/x"])
        #expect(v.bands[2].members == ["L/1", "L/5"] && v.bands[2].title == "feature/push" && v.bands[2].subtitle == "branch")
        #expect(v.bands[3].subtitle == "worktree")
        #expect(v.bands[1].title == "ios-app" && v.bands[1].projectID == "ios")
    }

    @Test func oneBandIsAPlainWall() {
        let v = ViewResolver.resolve(items([agent("1", "/p/acme-apps"), agent("2", "/p/acme-apps")]), level: .project, catalog: c)
        #expect(!v.showsBands, "a single project draws no band frame")
        let cont = ViewResolver.resolve(items([agent("1", "/p/acme-apps")]), level: .project, catalog: c, continued: ["p:as"])
        #expect(cont.showsBands && cont.bands[0].displayTitle == "acme-apps (cont.)")
        #expect(!ViewResolver.resolve(items([agent("1", "/p/acme-apps")]), level: .none, catalog: c).showsBands)
    }

    func place(_ a: Agent) -> AgentPlace {
        let pid = c.projectID(for: a)
        return AgentPlace(projectID: pid, lineage: c.lineage(pid), groupIDs: Set(c.groupIDs(of: pid)), unit: ViewResolver.unit(projectID: pid, catalog: c))
    }

    @Test func groupProjectAndFilterScopes() {
        let a = [agent("1", "/p/acme-apps"), agent("2", "/p/acme-apps/apps/ios-app"), agent("3", "/p/hesper", machine: "M"),
                 agent("4", "/p/design-system", state: .approval), agent("5", "/s/trial")]
        let walls = [WallSlot(id: "main", scope: .group("g1"), capacity: 9, ownWalls: .full), WallSlot(id: "p", scope: .project("as"), capacity: 9),
                     WallSlot(id: "f", scope: .filter(WallFilter(groups: ["g2"])), capacity: 9),
                     WallSlot(id: "f2", scope: .filter(WallFilter(projects: ["as"], states: [.working])), capacity: 9, ownWalls: .full)]
        let d = ScopeMath.distribute(a, walls: walls, place: place, unitOrder: [])
        #expect(d.agents["main"]?.map(\.id) == ["L/1", "L/2", "L/4"])
        #expect(d.agents["p"]?.map(\.id) == ["L/1", "L/2"], "a project scope includes its packages")
        #expect(d.agents["f"]?.map(\.id) == ["M/3"])
        #expect(d.agents["f2"]?.map(\.id) == ["L/1", "L/2"])
        // Older files: filter projects by folder still match.
        #expect(WallFilter(projects: ["/p/hesper"]).matches(a[2], place: place(a[2])))
        // Old windows.json without "groups" in a filter still decodes.
        let old = try? JSONDecoder().decode(WallFilter.self, from: Data(#"{"projects":["/x"],"machines":[],"kinds":[],"states":[]}"#.utf8))
        #expect(old?.projects == ["/x"] && old?.groups.isEmpty == true)
        let saved = try? JSONDecoder().decode(SavedWall.self, from: Data(#"{"id":"w","isMain":true,"scope":{"all":{}},"arrangement":"shelf","minChars":80,"frame":{"x":0,"y":0,"width":1,"height":1},"fullScreen":false}"#.utf8))
        #expect(saved != nil && saved?.grouping == nil && saved?.collapsed == nil)
        let round = SavedWall(id: "w", isMain: false, scope: .project("as"), arrangement: .columns, minChars: 80, frame: SavedFrame(x: 0, y: 0, width: 1, height: 1),
                              screen: nil, fullScreen: false, grouping: .project, collapsed: ["p:as"], bandOrder: ["p:gh"], sidebar: true)
        let data = try! JSONEncoder().encode(round)
        #expect(try! JSONDecoder().decode(SavedWall.self, from: data) == round)
    }

    // MARK: Overflow by group

    @Test func overflowKeepsGroupsWhole() {
        // acme apps: 3, tools: 2, other: 1. Capacity 4: acme fits, tools doesn't (1 left) → next wall.
        let a = [agent("1", "/p/acme-apps"), agent("2", "/p/hesper"), agent("3", "/p/design-system"), agent("4", "/p/hesper"),
                 agent("5", "/p/acme-apps"), agent("6", "/p/loose")]
        let walls = [WallSlot(id: "main", scope: .all, capacity: 4), WallSlot(id: "o1", scope: .overflow, capacity: 4),
                     WallSlot(id: "o2", scope: .overflow, capacity: 4)]
        let d = ScopeMath.distribute(a, walls: walls, place: place, unitOrder: ViewResolver.unitOrder(catalog: c))
        #expect(d.agents["main"]?.map(\.id) == ["L/1", "L/3", "L/5"])
        #expect(d.agents["o1"]?.map(\.id) == ["L/2", "L/4", "L/6"])
        #expect(d.agents["o2"]?.isEmpty == true)
        #expect(d.continued.isEmpty)
    }

    @Test func overflowSplitsAGroupBiggerThanAWall() {
        // tools has 5 agents, capacity 3: it starts on a wall of its own and continues.
        let a = [agent("1", "/p/acme-apps")] + (2...6).map { agent("\($0)", "/p/hesper") } + [agent("7", "/s/trial")]
        let walls = [WallSlot(id: "main", scope: .all, capacity: 3), WallSlot(id: "o1", scope: .overflow, capacity: 3),
                     WallSlot(id: "o2", scope: .overflow, capacity: 3)]
        let d = ScopeMath.distribute(a, walls: walls, place: place, unitOrder: ViewResolver.unitOrder(catalog: c))
        #expect(d.agents["main"]?.map(\.id) == ["L/1"], "tools doesn't fit the room left: next wall")
        #expect(d.agents["o1"]?.map(\.id) == ["L/2", "L/3", "L/4"])
        #expect(d.agents["o2"]?.map(\.id) == ["L/5", "L/6", "L/7"])
        #expect(d.continued == ["o2": ["g:g2"]], "the remainder continues as tools (cont.)")
        // The continuation wall labels its band.
        let v = ViewResolver.resolve(items(d.agents["o2"]!), level: .group, catalog: c, continued: d.continued["o2"]!)
        #expect(v.bands.first { $0.key == "g:g2" }?.displayTitle == "tools (cont.)")
    }

    @Test func overflowAttentionStaysHome() {
        let a = [agent("1", "/p/hesper"), agent("2", "/p/hesper"), agent("3", "/p/acme-apps", state: .approval), agent("4", "/p/acme-apps")]
        let walls = [WallSlot(id: "main", scope: .all, capacity: 2), WallSlot(id: "o1", scope: .overflow, capacity: 2)]
        let d = ScopeMath.distribute(a, walls: walls, place: place, unitOrder: ViewResolver.unitOrder(catalog: c))
        #expect(d.agents["main"]?.map(\.id).contains("L/3") == true)
        #expect(d.agents["main"]?.count == 2, "the approval plus the 1-agent acme remainder fits")
        #expect(d.agents["o1"]?.map(\.id) == ["L/1", "L/2"])
        // Without project data every agent is its own unit (as before).
        let plain = ScopeMath.distribute([agent("1", "/x"), agent("2", "/x"), agent("3", "/x")], walls: walls)
        #expect(plain["main"]?.count == 2 && plain["o1"]?.count == 1)
    }

    // MARK: Band allocation, boxes, lanes, nested

    let table = FontFit.CellTable(scale: 2, ratio: FontFit.CellRatio(widthPerPoint: 0.6, heightPerPoint: 1.3))
    func cell(_ f: Double) -> CellSize { table.cell(f) }

    @Test func hysteresisGrowsAtOnceShrinksOnlyByTwo() {
        #expect(BandAllocator.weights(active: ["a": 3], previous: [:]) == ["a": 3])
        #expect(BandAllocator.weights(active: ["a": 4], previous: ["a": 3]) == ["a": 4])
        #expect(BandAllocator.weights(active: ["a": 2], previous: ["a": 3]) == ["a": 3], "one agent less: keeps its room")
        #expect(BandAllocator.weights(active: ["a": 1], previous: ["a": 3]) == ["a": 1])
        #expect(BandAllocator.weights(active: ["a": 0], previous: ["a": 3]) == ["a": 0])
        #expect(BandAllocator.weights(active: ["b": 2], previous: ["a": 3]) == ["b": 2])
    }

    func bandLayout(_ inputs: [WallTileInput], _ bands: [BandSlot], _ a: WallArrangement = .shelf, w: Double = 1400, h: Double = 1000,
                    prev: [String: Int] = [:]) -> WallLayout {
        WallLayout.make(inputs, bands: bands, arrangement: a, width: w, height: h, spec: WallSpec(), previousWeights: prev, cell: cell)
    }

    @Test func attentionNeverMovesABand() {
        var inputs = Array(repeating: WallTileInput(grid: GridSize(cols: 120, rows: 40), state: .working, band: 40), count: 5)
        let bands = [BandSlot(key: "a", items: [0, 1, 2]), BandSlot(key: "b", items: [3, 4])]
        let before = bandLayout(inputs, bands)
        inputs[1].state = .approval
        inputs[3].state = .question
        let after = bandLayout(inputs, bands, prev: before.bandWeights)
        #expect(before.tiles == after.tiles && before.bands == after.bands)
        #expect(before.bands.count == 2 && before.bands.allSatisfy { $0.style == .band })
    }

    @Test func oneAgentFinishingKeepsTheOtherBandsInPlace() {
        var inputs = Array(repeating: WallTileInput(grid: GridSize(cols: 120, rows: 40), state: .working, band: 40), count: 5)
        let bands = [BandSlot(key: "a", items: [0, 1, 2]), BandSlot(key: "b", items: [3, 4])]
        let before = bandLayout(inputs, bands)
        inputs[2].state = .done // to a's shelf
        let after = bandLayout(inputs, bands, prev: before.bandWeights)
        #expect(after.bandWeights["a"] == 3, "hysteresis keeps a's room")
        #expect(after.tiles[2].quiet)
        // a's grid keeps its rows; the shelf strip is new, so b moves down
        // but keeps its cards' sizes (no terminal resize in b).
        #expect(after.tiles[3].card.width == before.tiles[3].card.width)
        #expect(abs(after.tiles[3].card.height - before.tiles[3].card.height) < 80)
    }

    @Test func bandsOnALaptopBoxesOnAWideWall() {
        let inputs = Array(repeating: WallTileInput(grid: GridSize(cols: 120, rows: 40)), count: 6)
        let bands = [BandSlot(key: "a", items: [0, 1, 2]), BandSlot(key: "b", items: [3, 4, 5])]
        let minW = WallLayout.minCard(WallSpec(), cell: cell).width
        #expect(WallLayout.boxesPerRow(width: 1400, spec: WallSpec(), minCardWidth: minW) == 1)
        #expect(WallLayout.boxesPerRow(width: 3400, spec: WallSpec(), minCardWidth: minW) >= 2)
        let laptop = bandLayout(inputs, bands, w: 1500, h: 950)
        #expect(laptop.bands.allSatisfy { $0.style == .band && abs($0.frame.width - (1500 - 40)) < 1e-6 })
        let wide = bandLayout(inputs, bands, w: 3440, h: 1400)
        #expect(wide.bands.allSatisfy { $0.style == .box })
        #expect(wide.bands[0].frame.maxX < wide.bands[1].frame.x, "side by side")
        // Every box gets at least two minimum cards of width.
        #expect(wide.bands.allSatisfy { $0.frame.width >= 2 * minW + WallSpec().gap })
        // Cards stay inside their frame below the header.
        for (bi, b) in bands.enumerated() {
            for i in b.items {
                let t = wide.tiles[i].card, f = wide.bands[bi].frame
                #expect(t.x >= f.x - 1e-6 && t.maxX <= f.maxX + 1e-6 && t.y >= wide.bands[bi].header.maxY - 1e-6 && t.maxY <= f.maxY + 1e-6)
            }
        }
    }

    @Test func collapsedBandsAreOneHeaderLine() {
        let inputs = Array(repeating: WallTileInput(grid: GridSize(cols: 120, rows: 40)), count: 4)
        let bands = [BandSlot(key: "a", items: [0, 1]), BandSlot(key: "b", items: [2, 3], collapsed: true)]
        for arr in [WallArrangement.shelf, .columns, .treemap] {
            let l = bandLayout(inputs, bands, arr)
            let b = l.bands.first { $0.key == "b" }!
            #expect(b.collapsed && b.frame.height == WallSpec().bandHeader, "\(arr)")
            #expect(l.tiles[2].hidden && l.tiles[3].hidden && !l.tiles[0].hidden)
            #expect(l.tiles[2].card.width == 0, "hidden cards are no navigation targets")
        }
        let open = bandLayout(inputs, [BandSlot(key: "a", items: [0, 1]), BandSlot(key: "b", items: [2, 3])])
        let shut = bandLayout(inputs, bands)
        #expect(shut.tiles[0].card.height > open.tiles[0].card.height, "the open band takes the room")
    }

    @Test func lanesAndNestedTreemap() {
        var inputs = Array(repeating: WallTileInput(grid: GridSize(cols: 120, rows: 40)), count: 5)
        inputs[4].state = .approval
        let bands = [BandSlot(key: "a", items: [0, 1, 2]), BandSlot(key: "b", items: [3, 4])]
        let lanes = bandLayout(inputs, bands, .columns)
        #expect(lanes.bands.map(\.style) == [.lane, .lane])
        #expect(lanes.bands[0].frame.maxX < lanes.bands[1].frame.x)
        #expect(lanes.tiles[0..<3].allSatisfy { $0.card.x >= lanes.bands[0].frame.x && $0.card.maxX <= lanes.bands[0].frame.maxX })
        #expect(lanes.contentWidth > 1400, "the wall scrolls sideways")
        let nested = bandLayout(inputs, bands, .treemap)
        #expect(nested.bands.map(\.style) == [.nested, .nested])
        // Groups first: each band's agents stay inside its rect.
        for (bi, b) in bands.enumerated() {
            let f = nested.bands[bi].frame
            for i in b.items {
                let t = nested.tiles[i].card
                #expect(t.x >= f.x - 1e-6 && t.maxX <= f.maxX + 1e-6 && t.y >= f.y - 1e-6 && t.maxY <= f.maxY + 1e-6)
            }
        }
        // b weighs 2 + 4 = 6, a 3 × 2 = 6: equal areas.
        let area = { (r: Rect) in r.width * r.height }
        #expect(abs(area(nested.bands[0].frame) - area(nested.bands[1].frame)) / area(nested.bands[0].frame) < 0.05)
        // D: no frames.
        #expect(bandLayout(inputs, bands, .mainStack).bands.isEmpty)
    }

    // MARK: Navigation

    @Test func navigationCrossesBands() {
        let inputs = Array(repeating: WallTileInput(grid: GridSize(cols: 120, rows: 40)), count: 4)
        let l = bandLayout(inputs, [BandSlot(key: "a", items: [0, 1]), BandSlot(key: "b", items: [2, 3])], w: 1400, h: 1000)
        // Arrow ↓ from band a's card goes into band b (spatial, as on a plain wall).
        #expect([2, 3].contains(l.neighbor(of: 0, .down)))
        #expect([0, 1].contains(l.neighbor(of: 2, .up)))
        // ⌥↑ / ⌥↓: first card of the previous / next open band.
        let bands = [(key: "a", members: ["1", "2"]), (key: "b", members: [String]()), (key: "c", members: ["5", "6"])]
        #expect(BandNavigation.target(bands: bands, current: "2", delta: 1) == "5", "collapsed b skipped")
        #expect(BandNavigation.target(bands: bands, current: "6", delta: -1) == "1")
        #expect(BandNavigation.target(bands: bands, current: "6", delta: 1) == nil)
        #expect(BandNavigation.target(bands: bands, current: nil, delta: 1) == "1")
        // ⌥⌘← → in a single view: same project first, then the next.
        let proj = ["1": "as", "2": "gh", "3": "as", "4": "ed", "5": "gh"]
        let order = ProjectStep.order(["1", "2", "3", "4", "5"], projectOf: { proj[$0] }, projectRank: ["as", "gh", "ed"])
        #expect(order == ["1", "3", "2", "5", "4"])
        #expect(ProjectStep.step(from: "1", by: 1, in: order) == "3")
        #expect(ProjectStep.step(from: "3", by: 1, in: order) == "2")
        #expect(ProjectStep.step(from: "4", by: 1, in: order) == "1")
        #expect(ProjectStep.step(from: "1", by: -1, in: order) == "4")
    }

    // MARK: Sidebar and home wall

    @Test func sidebarListsGroupsProjectsAndScratch() {
        let a = [agent("1", "/p/acme-apps", state: .approval), agent("2", "/p/acme-apps/apps/ios-app"), agent("3", "/p/hesper"), agent("4", "/s/trial"),
                 agent("5", "/elsewhere")]
        let s = SidebarModel.make(catalog: c, agents: a)
        #expect(s.map(\.id) == ["all", "groups", "projects", "scratch"])
        #expect(s[0].rows[0].count == 5 && s[0].rows[0].needsYou)
        let g = s[1].rows
        #expect(g.map(\.title) == ["acme apps", "acme-apps", "ios-app", "design-system", "tools", "hesper"])
        #expect(g[0].count == 2 && g[0].needsYou && g[0].scope == .group("g1"))
        #expect(g[1].count == 2 && g[1].needsYou && g[1].depth == 1 && g[1].scope == .project("as"), "a repo counts its packages' agents")
        #expect(g[2].depth == 2 && g[2].count == 1, "a package shows once an agent works in it")
        #expect(g[3].count == 0 && !g[3].needsYou)
        #expect(s[2].rows.map(\.title) == ["loose"], "projects in no group")
        #expect(s[3].rows.map(\.title) == ["elsewhere", "hesper-trial"] && s[3].rows.allSatisfy(\.scratch), "scratch folders")
        #expect(Set(g.map(\.id)).count == g.count, "row ids unique")
    }

    @Test func homeWallNeedsYouSet() {
        let a = [agent("1", "/p/acme-apps", state: .approval), agent("2", "/p/hesper", state: .question), agent("3", "/p/hesper", state: .error),
                 agent("4", "/p/hesper")]
        let out = HomeWall.needsYou(a, visible: ["L/1", "L/4"])
        #expect(Set(out.map(\.id)) == ["L/2", "L/3"])
        #expect(out.first?.state == .question, "question before error (⌘J order)")
        #expect(!HomeWall.reservesStrip(scope: .all, collapsedBands: 0))
        #expect(HomeWall.reservesStrip(scope: .all, collapsedBands: 1))
        #expect(HomeWall.reservesStrip(scope: .project("as"), collapsedBands: 0))
    }
}
