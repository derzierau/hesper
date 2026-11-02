import HesperCore
import HesperFixtures
import HesperFleet
import Testing

@Suite struct AgentCopyTests {
    let fleet = Samples.fleet

    @Test func approvalsSayWhatTheyWouldDo() {
        #expect(AgentCopy.headline(fleet.agents["mini/m1"]!) == "Run npm run db:migrate")
        #expect(AgentCopy.headline(fleet.agents["studio/s1"]!) == "Edit internal/relay/hub.go")
        #expect(AgentCopy.headline(Agent(id: "m/a", state: .approval)) == "Wants your approval")
    }

    @Test func questionsAndTheirChoices() {
        let q = fleet.agents["studio/s2"]!
        #expect(AgentCopy.headline(q) == "Which format should the API docs use?")
        #expect(AgentCopy.choices(q).map(\.number) == [1, 2])
        #expect(AgentCopy.choices(q).map(\.title) == ["OpenAPI", "Markdown"])
        #expect(AgentCopy.choices(fleet.agents["mini/m1"]!).isEmpty)
    }

    @Test func otherStates() {
        #expect(AgentCopy.headline(fleet.agents["mini/m2"]!) == "Wiring index into search")
        #expect(AgentCopy.headline(fleet.agents["mini/m4"]!) == "Fixed the token refresh race; added a test.")
        #expect(AgentCopy.headline(fleet.agents["mini/m6"]!) == "Exited, exit 0")
        #expect(AgentCopy.headline(Agent(id: "m/b", state: .starting)) == "Starting")
    }
}
