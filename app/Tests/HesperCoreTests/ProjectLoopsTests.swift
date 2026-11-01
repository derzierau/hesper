import Foundation
import Testing
@testable import HesperCore

struct ProjectLoopsTests {
    typealias F = LoopFixtures

    static let catalog = ProjectCatalog(projects: [
        Project(id: "p-api", name: "api", paths: ["L": "/tmp/fake/api"]),
        Project(id: "p-web", name: "web", paths: ["L": "/tmp/fake/web"]),
        Project(id: "p-docs", name: "docs", paths: ["L": "/tmp/fake/docs"]),
    ], groups: [ProjectGroup(id: "g-acme", name: "acme", projectIds: ["p-api", "p-web"])])

    static let loops = [
        F.loop("a", next: .you, items: [F.item("i", "Pick")], projects: ["p-api", "p-web"]), // on you, in both
        F.loop("b", next: .agents, projects: ["p-api"]),
        F.loop("c", next: .you, looksDone: true, projects: ["p-web"]), // looks done: open, not your move
        F.loop("p", next: .you, status: .parked, projects: ["p-web"]), // parked: open, not your move
        F.loop("d", next: .closed, status: .done, projects: ["p-api"]), // closed: not counted
        F.loop("n", next: .you), // no project: All only
    ]

    @Test func countsPerProjectGroupAndAll() {
        let pl = ProjectLoops(loops: Self.loops)
        #expect(pl.tally(["p-api"]) == LoopTally(open: 2, onYou: 1))
        #expect(pl.tally(["p-web"]) == LoopTally(open: 3, onYou: 1))
        #expect(pl.tally(["p-api", "p-web"]) == LoopTally(open: 4, onYou: 1)) // "a" once
        #expect(pl.tally(["p-docs"]).isEmpty)
        #expect(pl.total == LoopTally(open: 5, onYou: 2))
        #expect(LoopTally(open: 3, onYou: 1).spoken == "3 open loops, 1 your move")
        #expect(LoopTally(open: 1).spoken == "1 open loop")
    }

    @Test func appliedToTheNavigatorRows() {
        let agents = [Agent(id: "L/1", state: .working, project: "/tmp/fake/api"), Agent(id: "L/2", state: .working, project: "/tmp/fake/web")]
        var state = ProjectNavState()
        state.showAll = true
        state.expandedGroups = ["g-acme"]
        let sections = ProjectNav.make(catalog: Self.catalog, agents: agents, local: "L", state: state)
        let rows = ProjectNav.flat(ProjectLoops(loops: Self.loops).apply(sections, catalog: Self.catalog))
        #expect(rows.first { $0.kind == .all }?.loops == LoopTally(open: 5, onYou: 2))
        #expect(rows.first { $0.kind == .group }?.loops == LoopTally(open: 4, onYou: 1))
        #expect(rows.first { $0.kind == .project && $0.projectID == "p-api" }?.loops == LoopTally(open: 2, onYou: 1))
        #expect(rows.first { $0.kind == .project && $0.projectID == "p-docs" }?.loops.isEmpty == true)
        // A loop change alone changes the rows (the sidebar's reload compares them).
        #expect(ProjectLoops(loops: Self.loops.dropLast()).apply(sections, catalog: Self.catalog) != ProjectLoops(loops: Self.loops).apply(sections, catalog: Self.catalog))
    }

    @Test func duplicateDecodesLenientlyAndReachesTheRow() throws {
        let p = try F.decode(Project.self, #"{"id":"p-dup","name":"api","duplicateOf":"p-api","paths":{"L":"/tmp/fake/api"}}"#)
        #expect(p.duplicateOf == "p-api")
        #expect(try F.decode(Project.self, #"{"id":"p-x","duplicateOf":""}"#).duplicateOf == nil)
        #expect(try F.decode(Project.self, #"{"id":"p-x","duplicateOf":7}"#).duplicateOf == nil)
        #expect(try F.decode(Project.self, #"{"id":"p-x"}"#).duplicateOf == nil)
        let back = try F.decode(Project.self, String(decoding: JSONEncoder().encode(p), as: UTF8.self))
        #expect(back.duplicateOf == "p-api")
        var state = ProjectNavState()
        state.showAll = true
        let cat = ProjectCatalog(projects: Self.catalog.projects.values.sorted { $0.id < $1.id } + [p])
        let rows = ProjectNav.flat(ProjectNav.make(catalog: cat, agents: [], local: "L", state: state))
        #expect(rows.first { $0.projectID == "p-dup" }?.duplicateOf == "p-api")
        #expect(rows.first { $0.projectID == "p-api" }?.duplicateOf == nil)
    }
}
