import HesperCore
import HesperFixtures
import HesperFleet
import Testing

@Suite struct HomeTests {
    @Test func allPutsTheOldestApprovalFirstAndTheRestInTheGrid() {
        let h = Samples.fleet.home(.all)
        #expect(h.priority?.id == Samples.fleet.needsYou.first?.id)
        #expect(h.needsYou.map(\.id) == Samples.fleet.needsYou.dropFirst().map(\.id))
    }

    @Test func readyToReviewHoldsDoneAndIdleButNotExited() {
        let ids = Set(Samples.fleet.home(.all).readyToReview.map(\.id))
        #expect(ids.contains("air/a2") && ids.contains("mini/m5") && !ids.contains("mini/m6"))
    }

    @Test func workingHoldsWorkingAndStarting() {
        let ids = Set(Samples.fleet.home(.all).working.map(\.id))
        #expect(ids.contains("air/a1") && ids.contains("mini/m3"))
    }

    @Test func scopesNarrowTheSections() {
        let needs = Samples.fleet.home(.needsYou)
        #expect(needs.priority != nil && needs.readyToReview.isEmpty && needs.working.isEmpty)
        let working = Samples.fleet.home(.working)
        #expect(working.priority == nil && working.needsYou.isEmpty && !working.working.isEmpty)
    }

    @Test func countsMatchTheAgentsScreen() {
        let c = Samples.fleet.homeCounts
        #expect(c.all == Samples.fleet.agents.count)
        #expect(c.needsYou == Samples.fleet.needsYou.count)
        #expect(c.working == Samples.fleet.counts.working)
    }

    @Test func allCountsExitedAgentsThatHomeLeavesOut() {
        let f = Samples.fleet
        let exited = f.agents.values.filter { $0.state == .exited }.map(\.id)
        #expect(!exited.isEmpty)
        let h = f.home(.all)
        let shown = Set(([h.priority].compactMap { $0 } + h.needsYou + h.readyToReview + h.working).map(\.id))
        #expect(shown.isDisjoint(with: exited))
        #expect(f.homeCounts.all == f.agents.count)
    }

    @Test func needsYouScopeKeepsTheAttentionQueueOrder() {
        let h = Samples.fleet.home(.needsYou)
        #expect(([h.priority].compactMap { $0 } + h.needsYou).map(\.id) == Samples.fleet.needsYou.map(\.id))
    }

    @Test func anEmptyFleetIsEmptyEverywhere() {
        let f = Fleet()
        for s in HomeScope.allCases { #expect(f.home(s).isEmpty) }
        #expect(f.homeCounts == HomeCounts(all: 0, needsYou: 0, working: 0))
    }
}
