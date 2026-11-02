import Foundation
import HesperCore
import HesperFixtures
import HesperFleet
import Testing

@MainActor
@Suite struct FleetModelTests {
    /// Waits until `condition` holds, at most a second.
    func eventually(_ condition: () -> Bool) async {
        for _ in 0..<100 where !condition() {
            try? await Task.sleep(for: .milliseconds(10))
        }
    }

    @Test func followsTheLink() async {
        let model = FleetModel(link: FixtureLink())
        model.start()
        await eventually { model.fleet.agents.count == 10 }
        #expect(model.fleet.agents.count == 10)
        #expect(model.fleet.macs["air"]?.reachability == .unreachable(since: Samples.airLost))
        model.stop()
    }

    @Test func anAnswerIsPendingUntilTheEvent() async {
        let link = FixtureLink()
        let model = FleetModel(link: link)
        model.start()
        await eventually { model.fleet.needsYou.count == 3 }
        await model.perform(.answer(agent: "studio/s1", .allow))
        #expect(link.sent == [.answer(agent: "studio/s1", .allow)])
        await eventually { model.fleet.agents["studio/s1"]?.state == .working }
        #expect(model.fleet.agents["studio/s1"]?.state == .working)
        #expect(model.pending.isEmpty)
        #expect(model.fleet.needsYou.count == 2)
        model.stop()
    }

    @Test func aRefusedCommandSaysSo() async {
        let model = FleetModel(link: FixtureLink())
        await model.perform(.stop(agent: "nowhere/x"))
        #expect(model.pending.isEmpty)
        #expect(model.failure == "No agent nowhere/x on the sample Macs.")
    }

    @Test func spawnAddsAStartingAgent() async {
        let model = FleetModel(link: FixtureLink())
        model.start()
        await eventually { !model.fleet.agents.isEmpty }
        await model.perform(.spawn(SpawnRequest(machine: "mini", project: "hesper", task: "Benchmark the history search")))
        await eventually { model.fleet.agents["mini/new1"] != nil }
        let a = model.fleet.agents["mini/new1"]
        #expect(a?.state == .starting)
        #expect(a?.name == "benchmark-the-history")
        #expect(a?.project == "hesper")
        model.stop()
    }

    @Test func aKeptTaskIsAParkedDraftOnThatMac() async throws {
        let link = FixtureLink()
        let model = FleetModel(link: link)
        model.start()
        await eventually { model.fleet.later.count == 1 }
        let kept = try #require(await model.keep("Add a dark mode toggle", at: Destination(project: "hesper", machine: "mini")))
        #expect(kept.draft.parked)
        #expect(kept.draft.machine == "mini")
        await eventually { model.fleet.later.count == 2 }
        #expect(model.fleet.later.first?.id == kept.id)
        model.stop()
    }

    @Test func startingAKeptTaskSpawnsItAndRemovesTheDraft() async throws {
        let link = FixtureLink()
        let model = FleetModel(link: link)
        model.start()
        await eventually { model.fleet.later.count == 1 }
        let kept = try #require(model.fleet.later.first)
        await model.start(kept)
        await eventually { model.fleet.later.isEmpty }
        #expect(model.fleet.later.isEmpty)
        #expect(link.sent.last == .removeDraft(id: "d-flaky1", machine: "studio"))
        let spawned = model.fleet.agents["studio/new1"]
        #expect(spawned?.project == "hesper")
        #expect(spawned?.task == "Look into the flaky relay reconnect test")
        model.stop()
    }

    @Test func discardingAMissingDraftSaysSo() async {
        let model = FleetModel(link: FixtureLink())
        await model.discard(KeptTask(machine: "mini", draft: Draft(id: "d-gone")))
        #expect(model.failure == "No draft d-gone on mini.")
    }
}
