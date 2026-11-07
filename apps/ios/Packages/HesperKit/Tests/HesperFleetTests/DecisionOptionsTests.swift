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

    @Test func onlyRiskyAllowsAskTwice() {
        let risky = Samples.fleet.agents["mini/m1"]!
        let plain = Samples.fleet.agents["studio/s1"]!
        #expect(DecisionOptions.needsConfirmation(risky, .allow) && DecisionOptions.needsConfirmation(risky, .always))
        #expect(!DecisionOptions.needsConfirmation(risky, .denyWithMessage))
        #expect(!DecisionOptions.needsConfirmation(plain, .allow))
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
