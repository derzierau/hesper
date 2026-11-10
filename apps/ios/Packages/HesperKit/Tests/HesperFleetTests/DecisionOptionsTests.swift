import Foundation
import HesperCore
import HesperFixtures
import HesperFleet
import Testing

@Suite struct DecisionOptionsTests {
    @Test func approvalHasTheThreeClaudeRows() {
        let rows = DecisionOptions.rows(for: Samples.fleet.agents["studio/s1"]!)
        #expect(rows.map(\.label) == ["Yes…", "Yes, and don't ask again", "No, and tell Claude what to do instead"])
        #expect(rows[1].detail == "asks once more")
        #expect(rows.map(\.isDestructive) == [false, false, true])
        #expect(rows.map(\.action) == [.allow, .always, .denyWithMessage])
    }

    @Test func questionRowsAreItsChoicesNumbered() {
        let a = Samples.fleet.agents["studio/s2"]!
        let rows = DecisionOptions.rows(for: a)
        #expect(rows.map(\.number) == AgentCopy.choices(a).map(\.number))
        #expect(rows.allSatisfy { !$0.isDestructive })
    }

    @Test func aQuestionWithoutOptionsHasNoRows() {
        let a = Agent(id: "m/q", state: .question, attention: Attention(kind: "question", title: "Which?"))
        #expect(DecisionOptions.rows(for: a).isEmpty)
        #expect(DecisionOptions.gridActions(for: a).map(\.label) == ["Open"])
    }

    @Test func allowAsksTwiceOnlyWhenRiskyAlwaysAlwaysAsks() {
        let risky = Samples.fleet.agents["mini/m1"]!
        let plain = Samples.fleet.agents["studio/s1"]!
        #expect(DecisionOptions.needsConfirmation(risky, .allow) && DecisionOptions.needsConfirmation(risky, .always))
        #expect(!DecisionOptions.needsConfirmation(risky, .denyWithMessage))
        #expect(!DecisionOptions.needsConfirmation(plain, .allow))
        #expect(DecisionOptions.needsConfirmation(plain, .always))
    }

    @Test func confirmationTitleNamesTheRiskOrTheLastAsk() {
        let risky = Samples.fleet.agents["mini/m1"]!
        let plain = Samples.fleet.agents["studio/s1"]!
        #expect(DecisionOptions.confirmationTitle(risky, .allow) == DecisionOptions.confirmationTitle)
        #expect(DecisionOptions.confirmationTitle(risky, .always) == DecisionOptions.confirmationTitle)
        #expect(DecisionOptions.confirmationTitle(plain, .always) == "Allow this without asking again?")
    }

    @Test func theConfirmedPromptIsStillCurrent() {
        let fleet = Samples.fleet
        let confirmed = fleet.agents["studio/s1"]!
        #expect(DecisionOptions.isStillCurrent(confirmed, in: fleet))
    }

    @Test func aNewPromptIsNotTheConfirmedOne() {
        let confirmed = Samples.fleet.agents["studio/s1"]!
        var live = confirmed
        live.attention?.detail = "internal/relay/other.go"
        var fleet = Samples.fleet
        fleet.apply(.changed(live), from: live.machine)
        #expect(!DecisionOptions.isStillCurrent(confirmed, in: fleet))
    }

    @Test func aRepeatedIdenticalPromptIsNotTheConfirmedOne() {
        // Answered on the Mac, then the agent asks the same thing again:
        // state and attention match, only stateSince tells them apart.
        var confirmed = Samples.fleet.agents["studio/s1"]!
        confirmed.stateSince = Date(timeIntervalSince1970: 1_000)
        var live = confirmed
        live.stateSince = Date(timeIntervalSince1970: 2_000)
        var fleet = Samples.fleet
        fleet.apply(.changed(live), from: live.machine)
        #expect(!DecisionOptions.isStillCurrent(confirmed, in: fleet))
    }

    @Test func anAnsweredPromptIsNotCurrent() {
        let confirmed = Samples.fleet.agents["studio/s1"]!
        var live = confirmed
        live.state = .working
        var fleet = Samples.fleet
        fleet.apply(.changed(live), from: live.machine)
        #expect(!DecisionOptions.isStillCurrent(confirmed, in: fleet))
    }

    @Test func aGoneAgentIsNotCurrent() {
        let confirmed = Samples.fleet.agents["studio/s1"]!
        #expect(!DecisionOptions.isStillCurrent(confirmed, in: Fleet()))
    }

    @Test func commandsMapOneToOne() {
        let a = Samples.fleet.agents["studio/s1"]!
        #expect(DecisionOptions.command(a, .allow) == .answer(agent: a.id, .allow))
        #expect(DecisionOptions.command(a, .choose(2)) == .choose(agent: a.id, option: 2))
        #expect(DecisionOptions.command(a, .denyWithMessage) == nil)
    }

    @Test func gridActionsPerState() {
        #expect(DecisionOptions.gridActions(for: Samples.fleet.agents["air/a2"]!).map(\.label) == ["Open", "Close"])
        #expect(DecisionOptions.gridActions(for: Samples.fleet.agents["air/a1"]!).map(\.label) == ["Stop", "Open"])
        #expect(DecisionOptions.gridActions(for: Samples.fleet.agents["studio/s1"]!).map(\.label) == ["Yes", "No…"])
    }
}
