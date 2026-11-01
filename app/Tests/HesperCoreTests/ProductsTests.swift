import Foundation
import Testing
@testable import HesperCore

struct ProductsTests {
    typealias F = LoopFixtures

    var catalog: ProjectCatalog {
        ProjectCatalog(projects: [Project(id: "p-ios", name: "ios", paths: ["L": "/tmp/fake/ios"], groups: ["g-demo"]),
                                  Project(id: "p-api", name: "api", paths: ["L": "/tmp/fake/api"]),
                                  Project(id: "p-pkg", name: "pkg", kind: .package, parentId: "p-ios", paths: ["L": "/tmp/fake/ios/pkg"]),
                                  Project(id: "p-other", name: "other", paths: ["L": "/tmp/fake/other"])],
                       groups: [ProjectGroup(id: "g-demo", name: "Demo app", projectIds: ["p-ios", "p-api"])])
    }

    func store() -> LoopStore {
        let loops = [
            F.loop("lp-push", next: .agents, projects: ["p-ios"], milestone: "ms-beta"),
            F.loop("lp-listing", next: .you, items: [F.item("i", "Write copy")], projects: ["p-ios"], milestone: "ms-launch"),
            F.loop("lp-blocked", next: .others, links: [LoopLink(kind: .loop, ref: "lp-push", rel: .blockedBy)], projects: ["p-api"], milestone: "ms-rc"),
            F.loop("lp-stale", next: .agents, projects: ["p-api"], milestone: "ms-later"),
            F.loop("lp-unfiled", next: .agents, projects: ["p-pkg"]),
            F.loop("lp-unfiled-done", next: .closed, status: .done, projects: ["p-api"]),
            F.loop("lp-elsewhere", next: .agents, projects: ["p-other"]),
            F.loop("lp-shipped", next: .closed, status: .done, projects: ["p-ios"], milestone: "ms-alpha"),
        ]
        let ms = [
            Milestone(id: "ms-alpha", scope: "g-demo", title: "Alpha", status: .done, order: 0, loops: ["lp-shipped"]),
            Milestone(id: "ms-beta", scope: "g-demo", goal: "gl-1", title: "Beta", order: 1,
                      items: [F.item("c1", "Push works", owner: .agent, state: .done, kind: .condition), F.item("c2", "Crash free", owner: .agent, kind: .condition)]),
            Milestone(id: "ms-rc", scope: "g-demo", title: "RC", order: 2, loops: ["lp-blocked"]),
            Milestone(id: "ms-launch", scope: "g-demo", title: "Launch", order: 3),
            Milestone(id: "ms-later", scope: "g-demo", title: "Later", order: 4, stalled: true),
            Milestone(id: "ms-dropped", scope: "g-demo", title: "Dropped", status: .dropped, order: 5),
            Milestone(id: "ms-p2", scope: "g-demo", title: "Proposed two", status: .proposed, order: 7),
            Milestone(id: "ms-p1", scope: "g-demo", title: "Proposed one", status: .proposed, order: 6),
            Milestone(id: "ms-ios", scope: "p-ios", title: "Project milestone"),
        ]
        let goals = [Goal(id: "gl-2", scope: "g-demo", title: "Later goal", order: 2), Goal(id: "gl-1", scope: "g-demo", title: "In the stores", order: 1),
                     Goal(id: "gl-x", scope: "g-demo", title: "Old", status: .dropped), Goal(id: "gl-ios", scope: "p-ios", title: "iOS")]
        return LoopStore(loops: loops, goals: goals, milestones: ms)
    }

    @Test func pageOfAGroup() {
        let p = ProductPage(scope: "g-demo", store: store(), catalog: catalog)
        #expect(p.title == "Demo app" && p.isGroup && p.goal?.id == "gl-1" && p.goals.map(\.id) == ["gl-1", "gl-2"])
        // In order, done last; dropped and proposed not here.
        #expect(p.milestones.map(\.id) == ["ms-beta", "ms-rc", "ms-launch", "ms-later", "ms-alpha"])
        let st = Dictionary(p.milestones.map { ($0.id, $0.standing) }, uniquingKeysWith: { a, _ in a })
        #expect(st["ms-beta"] == .onTrack && st["ms-rc"] == .waiting(on: "ms-beta") && st["ms-launch"] == .onYou)
        #expect(st["ms-later"] == .stalled && st["ms-alpha"] == .done)
        #expect(p.milestones[0].progress == 0.5 && p.milestones.last?.progress == 1)
        // Loops per milestone (the loop's field or the milestone's list).
        #expect(p.milestones.first { $0.id == "ms-beta" }?.loops.map(\.id) == ["lp-push"])
        #expect(p.milestones.first { $0.id == "ms-alpha" }?.loops.map(\.id) == ["lp-shipped"])
        // Unfiled: active loops in the group's projects (a package counts); not other projects, not closed.
        #expect(p.unfiled.map(\.id) == ["lp-unfiled"])
        #expect(p.proposed.map(\.id) == ["ms-p1", "ms-p2"])
        #expect(MilestoneStanding.waiting(on: "x").title == "Waiting" && MilestoneStanding.onYou.title == "On you")
    }

    @Test func pageOfAProject() {
        let p = ProductPage(scope: "p-ios", store: store(), catalog: catalog)
        #expect(p.title == "ios" && !p.isGroup && p.goal?.title == "iOS" && p.milestones.map(\.id) == ["ms-ios"])
        #expect(p.unfiled.map(\.id) == ["lp-unfiled"] && p.proposed.isEmpty && p.acceptAll == nil)
    }

    @Test func onYouFromAConditionAndWaitingFromACheck() {
        let ms = [Milestone(id: "a", scope: "s", title: "A", order: 0, items: [F.item("c", "Sign the contract", kind: .condition)]),
                  Milestone(id: "b", scope: "s", title: "B", order: 1,
                            items: [F.item("c", "Push loop done", owner: .agent, kind: .condition, check: ItemCheck(kind: "loop", ref: "lp-x"))])]
        let loops = [F.loop("lp-x", next: .agents, milestone: "a")]
        let p = ProductPage(scope: "s", goals: [], milestones: ms, loops: Dictionary(uniqueKeysWithValues: loops.map { ($0.id, $0) }))
        #expect(p.milestones.map(\.standing) == [.onYou, .waiting(on: "a")])
    }

    @Test func planningIntentsAndScopes() {
        let p = ProductPage(scope: "g-demo", store: store(), catalog: catalog)
        #expect(p.acceptAll == .accept(scope: "g-demo", ids: nil))
        #expect(p.accept("ms-p1") == .accept(scope: "g-demo", ids: ["ms-p1"]) && p.drop("ms-p2") == .drop(milestone: "ms-p2"))
        let s = store()
        #expect(ProductPage.scopes(goals: Array(s.goals.values), milestones: Array(s.milestones.values), catalog: catalog) == ["g-demo", "p-ios"])
        // Only dropped records: not a product.
        #expect(ProductPage.scopes(goals: [Goal(id: "g", scope: "x", title: "t", status: .dropped)], milestones: []).isEmpty)
    }

    @Test func aRunningPlannerIsShownNotStartedAgain() {
        func planner(_ id: String, _ name: String, _ state: AgentState = .working) -> Agent {
            var a = Agent(id: id, name: name, state: state)
            a.service = "planner"
            return a
        }
        let mine = planner("L/p1", "Renamed by the planner", .starting)
        let named = planner("L/p2", "Plan g-demo", .idle)
        let other = planner("L/p3", "Plan p-ios")
        var plain = Agent(id: "L/x", name: "Plan g-demo")
        plain.service = nil
        // The one this app started wins while it runs, whatever its name.
        #expect(ProductPage.runningPlanner(scope: "g-demo", remembered: "L/p1", agents: [named, mine, other, plain])?.id == "L/p1")
        // Else hesperd's planner for the scope, by the name it gives it.
        #expect(ProductPage.runningPlanner(scope: "g-demo", remembered: nil, agents: [plain, other, named])?.id == "L/p2")
        // Exited or failed ones do not count; a plain agent never does.
        let gone = planner("L/p1", "Plan g-demo", .exited), failed = planner("L/p2", "Plan g-demo", .error)
        #expect(ProductPage.runningPlanner(scope: "g-demo", remembered: "L/p1", agents: [gone, failed, plain]) == nil)
    }
}
