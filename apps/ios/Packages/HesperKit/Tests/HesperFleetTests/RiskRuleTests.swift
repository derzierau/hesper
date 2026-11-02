import HesperCore
import HesperFixtures
import HesperFleet
import Testing

@Suite struct RiskRuleTests {
    func approval(_ tool: String, _ detail: String) -> Agent {
        Agent(id: "m/a", state: .approval, attention: Attention(kind: "approval", title: tool, detail: detail))
    }

    @Test func commandsThatChangeStateOnlyOpen() {
        #expect(RiskRule.isRisky(Samples.fleet.agents["mini/m1"]!))
        #expect(RiskRule.isRisky(approval("Bash", "git push origin main")))
        #expect(RiskRule.isRisky(approval("Bash", "rm -rf build")))
    }

    @Test func everythingElseCanBeAllowed() {
        #expect(!RiskRule.isRisky(Samples.fleet.agents["studio/s1"]!))
        #expect(!RiskRule.isRisky(approval("Bash", "go test ./...")))
        #expect(!RiskRule.isRisky(Agent(id: "m/b", state: .working)))
    }
}
