import Foundation
import Testing
@testable import HesperCore

struct LoopBoardTests {
    typealias F = LoopFixtures

    var catalog: ProjectCatalog {
        ProjectCatalog(projects: [Project(id: "p-app", name: "app", paths: ["L": "/tmp/fake/app"], groups: ["g-1"]),
                                  Project(id: "p-pkg", name: "pkg", kind: .package, parentId: "p-app", paths: ["L": "/tmp/fake/app/pkg"]),
                                  Project(id: "p-site", name: "site", paths: ["M": "/tmp/fake/site"])],
                       groups: [ProjectGroup(id: "g-1", name: "apps", projectIds: ["p-app"])])
    }

    func sample() -> [Loop] {
        [
            F.loop("you-old", next: .you, items: [F.item("i", "Pick", created: F.date(-5))], projects: ["p-app"], updated: F.date(1)),
            F.loop("you-new", next: .you, items: [F.item("i", "Pick", created: F.date(-1))], projects: ["p-site"], updated: F.date(-1)),
            F.loop("you-done", next: .you, looksDone: true, updated: F.date(-10)),
            F.loop("ag-old", next: .agents, links: [F.agent("n/a1", machine: "M")], projects: ["p-pkg"], updated: F.date(1)),
            F.loop("ag-new", next: .agents, links: [F.agent("n/a2", machine: "L")], projects: ["p-app"], milestone: "ms-1", updated: F.date(3)),
            F.loop("others", next: .others, updated: F.date(0)),
            F.loop("park-late", next: .parked, status: .parked, parkedUntil: F.date(48)),
            F.loop("park-soon", next: .parked, status: .parked, parkedUntil: F.date(2)),
            F.loop("park-none", next: .parked, status: .parked),
            F.loop("closed", next: .closed, status: .done),
            F.loop("dropped", next: .you, status: .dropped),
        ] + [{ var l = F.loop("merged", next: .you); l.mergedInto = "you-old"; return l }()]
    }

    @Test func columnsSortedAndClosedHidden() {
        let b = LoopBoard(loops: sample())
        #expect(b.loops(.you).map(\.id) == ["you-old", "you-new", "you-done"]) // oldest waiting first, looks done last
        #expect(b.loops(.agents).map(\.id) == ["ag-new", "ag-old"]) // most recent first
        #expect(b.loops(.others).map(\.id) == ["others"])
        #expect(b.loops(.parked).map(\.id) == ["park-soon", "park-late", "park-none"])
        #expect(!b.order.contains("closed") && !b.order.contains("dropped") && !b.order.contains("merged"))
        #expect(LoopColumn.allCases.map(\.title) == ["You", "Agents", "Others", "Parked"])
    }

    @Test func countsPillAndLimit() {
        var b = LoopBoard(loops: sample(), loopLimit: 2)
        #expect(b.totals[.you] == 3 && b.yourMove == 2 && b.looksDone == 1 && b.count(.agents) == 2)
        #expect(b.pill == "2 your move" && !b.overLimit && b.limitHint == nil)
        b = LoopBoard(loops: sample(), loopLimit: 1)
        #expect(b.overLimit && b.limitHint == "2 loops wait on you (limit 1)")
        #expect(LoopBoard(loops: [F.loop("a")]).pill == nil)
    }

    @Test func filters() {
        let all = sample()
        // A project keeps its packages' loops.
        let byProject = LoopBoard(loops: all, filter: LoopBoardFilter(project: "p-app"), catalog: catalog)
        #expect(Set(byProject.order) == ["you-old", "ag-old", "ag-new"])
        // Totals ignore the filter.
        #expect(byProject.yourMove == 2 && byProject.count(.you) == 1)
        let byGroup = LoopBoard(loops: all, filter: LoopBoardFilter(group: "g-1"), catalog: catalog)
        #expect(Set(byGroup.order) == ["you-old", "ag-old", "ag-new"])
        #expect(LoopBoard(loops: all, filter: LoopBoardFilter(machine: "M")).order == ["ag-old"])
        #expect(LoopBoard(loops: all, filter: LoopBoardFilter(milestone: "ms-1")).order == ["ag-new"])
        // "" milestone: unfiled only.
        #expect(!LoopBoard(loops: all, filter: LoopBoardFilter(milestone: "")).order.contains("ag-new"))
        let both = LoopBoard(loops: all, filter: LoopBoardFilter(project: "p-app", machine: "L"), catalog: catalog)
        #expect(both.order == ["ag-new"])
        #expect(LoopBoardFilter().isEmpty && !LoopBoardFilter(machine: "L").isEmpty)
        // Choices for the pickers.
        let b = LoopBoard(loops: all)
        #expect(b.projects == ["p-app", "p-pkg", "p-site"] && b.machines == ["L", "M"] && b.milestones == ["ms-1"])
    }

    @Test func selectionFollowsTheBoard() {
        var b = LoopBoard(loops: sample())
        #expect(b.selected == "you-old" && b.selectedColumn == .you)
        b.move(1); #expect(b.selected == "you-new")
        b.move(5); #expect(b.selected == "you-done")
        b.moveColumn(1); #expect(b.selected == "ag-old") // same row, clamped
        b.moveColumn(1); #expect(b.selected == "others")
        b.moveColumn(1); #expect(b.selected == "park-soon")
        b.moveColumn(1); #expect(b.selected == "park-soon") // the last column
        b.select("nope"); #expect(b.selected == "park-soon")
        b.select("ag-new"); #expect(b.selectedLoop?.id == "ag-new")
        // A kept selection survives a rebuild; a gone one falls back.
        #expect(LoopBoard(loops: sample(), selected: "others").selected == "others")
        #expect(LoopBoard(loops: sample(), selected: "closed").selected == "you-old")
        // Skips empty columns.
        var sparse = LoopBoard(loops: [F.loop("y", next: .you), F.loop("p", next: .parked, status: .parked)])
        sparse.moveColumn(1); #expect(sparse.selected == "p")
        sparse.moveColumn(-1); #expect(sparse.selected == "y")
        #expect(LoopBoard(loops: []).selected == nil && LoopBoard(loops: []).isEmpty)
    }
}
