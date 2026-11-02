import Foundation
import HesperCore
import HesperFixtures
import HesperFleet
import HesperSurfaces
import Testing

@Suite struct LinkEventTests {
    @Test func decodesAFullList() throws {
        let e = try LinkEvent.decode(Data(#"{"machine":"mini","full":true,"list":[{"id":"mini/a","state":"working","name":"x"}]}"#.utf8))
        guard case .list(let machine, let agents, let full) = e else {
            Issue.record("not a list: \(e)")
            return
        }
        #expect(machine == "mini")
        #expect(full)
        #expect(agents.map(\.id) == ["mini/a"])
        #expect(agents[0].machine == "mini")
    }

    @Test func decodesChangedAndRemoved() throws {
        let changed = try LinkEvent.decode(Data(#"{"changed":{"id":"studio/b","state":"approval"}}"#.utf8))
        #expect(changed == .changed(Agent(id: "studio/b", kind: "shell", state: .approval)))
        #expect(try LinkEvent.decode(Data(#"{"removed":"studio/b"}"#.utf8)) == .removed(id: "studio/b"))
    }

    @Test func aNewerStateDecodesAsUnknown() throws {
        let e = try LinkEvent.decode(Data(#"{"changed":{"id":"m/a","state":"thinking"}}"#.utf8))
        guard case .changed(let a) = e else {
            Issue.record("not a change")
            return
        }
        #expect(a.state == .unknown)
    }

    @Test func refusesSomethingElse() {
        #expect(throws: DecodingError.self) { try LinkEvent.decode(Data(#"{"hello":1}"#.utf8)) }
    }

    @Test func everySampleDecodes() throws {
        for (machine, json) in Samples.listEvents {
            guard case .list(let m, let agents, _) = try LinkEvent.decode(Data(json.utf8)) else {
                Issue.record("\(machine): not a list")
                continue
            }
            #expect(m == machine)
            #expect(!agents.isEmpty)
            #expect(agents.allSatisfy { $0.machine == machine && $0.stateSince != nil })
        }
    }
}

@Suite struct FleetTests {
    let fleet = Samples.fleet

    @Test func needsYouIsTheMacsQueue() {
        // Approvals oldest first, then the question.
        #expect(fleet.needsYou.map(\.name) == ["migrations", "relay-tests", "api-docs"])
    }

    @Test func readyToReviewIsNewestFirst() {
        #expect(fleet.readyToReview.map(\.name) == ["fix-login", "pricing-copy"])
    }

    @Test func groupsByProjectWithScratchLast() {
        #expect(fleet.groups.map(\.project) == ["hesper", "website", nil])
        let hesper = fleet.groups[0].agents.map(\.name)
        #expect(Array(hesper.prefix(3)) == ["migrations", "relay-tests", "api-docs"])
    }

    @Test func countsAreCountsOnly() {
        #expect(fleet.counts == FleetCounts(working: 3, needsYou: 3, done: 2))
        #expect(fleet.counts.summary == "3 working · 3 need you")
        #expect(FleetCounts(needsYou: 1).summary == "1 needs you")
        #expect(FleetCounts().summary == "Nothing needs you")
        #expect(FleetCounts(done: 2).isQuiet)
    }

    @Test func aFullListReplacesOnlyThatMac() {
        var f = fleet
        f.apply(.list(machine: "mini", agents: [Agent(id: "mini/z", name: "z")], full: true), from: "mini")
        #expect(f.agents.values.filter { $0.machine == "mini" }.map(\.id) == ["mini/z"])
        #expect(f.agents["studio/s1"] != nil)
    }

    @Test func changeAndRemove() {
        var f = fleet
        var a = f.agents["studio/s1"]!
        a.state = .working
        f.apply(.changed(a), from: "studio")
        #expect(!f.needsYou.contains { $0.id == a.id })
        f.apply(.removed(id: a.id), from: "studio")
        #expect(f.agents[a.id] == nil)
    }

    @Test func nextSkipsTheCurrentAgent() {
        #expect(fleet.next(after: "mini/m1")?.name == "relay-tests")
        #expect(fleet.next(after: nil)?.name == "migrations")
        var f = Fleet()
        f.apply(.changed(Agent(id: "m/a", state: .approval)), from: "m")
        #expect(f.next(after: "m/a") == nil)
    }

    @Test func anUnreachableMacKeepsItsAgents() {
        #expect(fleet.agents["air/a1"] != nil)
        #expect(!fleet.isReachable(fleet.agents["air/a1"]!))
        #expect(fleet.isReachable(fleet.agents["mini/m2"]!))
        #expect(fleet.macList.map(\.id) == ["air", "mini", "studio"])
    }

    @Test func laterListsParkedDraftsNewestFirst() {
        var f = fleet
        #expect(f.later.map(\.draft.text) == ["Look into the flaky relay reconnect test"])
        let old = Date(timeIntervalSince1970: 1_000)
        f.saveDraft(Draft(id: "d-new", text: "Newer", parked: true, created: old, updated: .now), on: "mini")
        f.saveDraft(Draft(id: "d-open", text: "Still on the wall", parked: false), on: "mini")
        #expect(f.later.map(\.id) == ["mini/d-new", "studio/d-flaky1"])
        f.removeDraft(id: "d-new", on: "mini")
        #expect(f.later.map(\.id) == ["studio/d-flaky1"])
    }

    @Test func aDraftListReplacesOnlyThatMac() {
        var f = fleet
        f.saveDraft(Draft(id: "d-a", text: "a", parked: true), on: "mini")
        f.setDrafts([], on: "studio")
        #expect(f.later.map(\.id) == ["mini/d-a"])
    }

    @Test func destinationsAreReachableProjectsBusiestFirst() {
        // hesper has 4 agents on mini and 2 on studio; website is only on
        // air, which is unreachable.
        #expect(
            fleet.destinations == [
                Destination(project: "hesper", machine: "mini"),
                Destination(project: "hesper", machine: "studio"),
            ])
    }

    @Test func searchKeepsMatchingAgentsInTheirGroups() {
        #expect(fleet.groups(matching: "").map(\.project) == fleet.groups.map(\.project))
        let hits = fleet.groups(matching: "pricing")
        #expect(hits.map(\.project) == ["website"])
        #expect(hits[0].agents.map(\.name) == ["pricing-copy"])
        #expect(fleet.groups(matching: "fix/hub").flatMap(\.agents).map(\.name) == ["relay-tests"])
        #expect(fleet.groups(matching: "nothing like this").isEmpty)
    }
}

@Suite struct SharedCountsTests {
    @Test func roundTripsThroughTheSuite() {
        let suite = "hesper.tests.\(UUID().uuidString)"
        defer { UserDefaults().removePersistentDomain(forName: suite) }
        let store = SharedCounts(suite: suite)
        #expect(store.read() == nil)
        let at = Date(timeIntervalSince1970: 1_800_000_000)
        store.write(FleetCounts(working: 2, needsYou: 1), at: at)
        #expect(store.read()?.counts == FleetCounts(working: 2, needsYou: 1))
        #expect(store.read()?.at == at)
    }

    @Test func withoutAGroupNothingIsStored() {
        let store = SharedCounts(suite: nil)
        store.write(FleetCounts(working: 1))
        #expect(store.read() == nil)
    }
}
