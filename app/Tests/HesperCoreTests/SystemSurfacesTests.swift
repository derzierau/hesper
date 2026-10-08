import Foundation
import Testing
@testable import HesperCore

@Suite struct SystemSurfacesTests {
    private func at(_ s: Double) -> Date { Date(timeIntervalSince1970: s) }

    @Test func menuGroupsInOrder() {
        let agents = [
            Agent(id: "L/1", name: "api", state: .working, stateSince: at(20)),
            Agent(id: "L/2", name: "docs", state: .question, stateSince: at(5)),
            Agent(id: "M/3", name: "mig", state: .approval, stateSince: at(10)),
            Agent(id: "L/4", name: "old", state: .done, stateSince: at(1)),
            Agent(id: "L/5", name: "new", state: .idle, stateSince: at(30)),
            Agent(id: "L/6", name: "boot", state: .starting, stateSince: at(25)),
        ]
        let g = StatusMenuGroups.groups(agents)
        #expect(g.map(\.group) == [.needsYou, .working, .recent])
        #expect(g.map(\.group.title) == ["Needs you", "Working", "Recent"])
        #expect(g[0].agents.map(\.name) == ["mig", "docs"]) // approval before question
        #expect(g[1].agents.map(\.name) == ["api", "boot"]) // longest running first
        #expect(g[2].agents.map(\.name) == ["new", "old"])  // most recent first
    }

    @Test func menuGroupsSkipEmptyAndCapRecent() {
        let done = (0..<8).map { Agent(id: "L/\($0)", state: .done, stateSince: at(Double($0))) }
        let g = StatusMenuGroups.groups(done)
        #expect(g.map(\.group) == [.recent])
        #expect(g[0].agents.count == StatusMenuGroups.recentLimit)
        #expect(g[0].agents.first?.id == "L/7")
        #expect(StatusMenuGroups.groups([Agent]()).isEmpty)
    }

    @Test func approvalNotification() {
        let a = Agent(id: "M/1", machine: "M", name: "migrations", state: .approval,
                      attention: Attention(kind: "approval", title: "Bash", detail: "git push", options: ["allow", "always", "deny"]))
        let n = AgentNotificationText(a, machineName: "mini")
        #expect(n.title == "migrations on mini needs you")
        #expect(n.body == "Allow Bash?\ngit push")
        #expect(n.category == AgentNotificationText.approvalCategory)
        #expect(n.thread == "M")
    }

    @Test func questionAndErrorNotifications() {
        let q = Agent(id: "L/2", name: "docs", state: .question,
                      attention: Attention(kind: "question", title: "Question", detail: "Which tone should the README use?", options: nil))
        let nq = AgentNotificationText(q, machineName: "laptop")
        #expect(nq.title == "docs on laptop needs you" && nq.body == "Which tone should the README use?" && nq.category == nil && nq.thread == "L")
        let e = Agent(id: "L/3", name: "api", state: .error, attention: Attention(kind: "error", title: "Crashed", detail: "exit 1", options: nil))
        let ne = AgentNotificationText(e, machineName: "laptop")
        #expect(ne.title == "api on laptop stopped with an error" && ne.body == "Crashed: exit 1" && ne.category == nil)
        let bare = AgentNotificationText(Agent(id: "L/4", name: "x", state: .approval), machineName: "laptop")
        #expect(bare.body == "Allow?")
    }

    @Test func firstRunSteps() {
        #expect(FirstRunSteps.shouldShow(alreadyShown: false, otherMachines: 0, agents: 0))
        #expect(!FirstRunSteps.shouldShow(alreadyShown: true, otherMachines: 0, agents: 0))
        #expect(!FirstRunSteps.shouldShow(alreadyShown: false, otherMachines: 1, agents: 0))
        #expect(!FirstRunSteps.shouldShow(alreadyShown: false, otherMachines: 0, agents: 3))
        var s = FirstRunSteps(connected: false, otherMachines: 0, agents: 0)
        #expect(s.current == .pairThisMac)
        s.connected = true
        #expect(s.current == .addAnotherMac && s.isDone(.pairThisMac) && !s.isDone(.addAnotherMac))
        s.otherMachines = 1
        #expect(s.current == .startFirstAgent)
        s.agents = 1
        #expect(s.current == nil)
        // Another Mac is optional: an agent started without one finishes it.
        #expect(FirstRunSteps(connected: true, otherMachines: 0, agents: 1).current == nil)
        #expect(FirstRunSteps.Step.allCases.map(\.title) == ["Pair this Mac", "Add another Mac", "Start your first agent"])
    }

    @Test func focusSlideDirection() {
        let o = ["a", "b", "c", "d"]
        #expect(FocusSlide.direction(from: "a", to: "b", order: o) == 1)
        #expect(FocusSlide.direction(from: "b", to: "a", order: o) == -1)
        #expect(FocusSlide.direction(from: "d", to: "a", order: o) == 1)  // wraps forward
        #expect(FocusSlide.direction(from: "a", to: "d", order: o) == -1) // wraps back
        #expect(FocusSlide.direction(from: "a", to: "x", order: o) == 1)  // unknown: forward
    }
}
