import Foundation
import Testing
@testable import HesperCore

struct LoopsTests {
    typealias F = LoopFixtures

    // MARK: Wire

    @Test func decodesTheWireLoop() throws {
        let l = try F.decode(Loop.self, F.wireLoop)
        #expect(l.id == "lp-0a1b2c3d4e5f" && l.title == "Push provider for the demo app" && l.goal == "Ship push on both platforms")
        #expect(l.doneWhen == "PR merged" && l.status == .open && l.next == .you && l.nextText == "Pick the provider" && !l.looksDone)
        #expect(l.milestone == "ms-00aa11bb22cc" && l.parkedUntil != nil && l.mergedInto == nil)
        #expect(l.projects == ["p-demo"] && l.created != nil && l.updated != nil && l.closed == nil)
        #expect(l.counts == LoopCounts(agents: 1, working: 0, openYou: 1, sessions: 2))
        // Items come sorted by order; Go's zero time is nil.
        #expect(l.items.map(\.id) == ["it-0000bbbb", "it-0000aaaa"])
        let q = l.items[1]
        #expect(q.owner == .you && q.kind == .question && q.after == ["it-0000bbbb"] && q.ready && q.draft == "d-123")
        #expect(q.source == "n-mini/a1b2c3" && q.check == ItemCheck(kind: "pr", ref: "https://example.com/acme/demo/pull/7") && q.check?.knownKind == .pr)
        #expect(q.created != nil && q.done == nil)
        #expect(l.items[0].state == .done && l.items[0].done != nil)
        #expect(l.links.count == 3)
        let a = l.links[0]
        #expect(a.kind == .agent && a.rel == .started && a.from == "n-mini/root01" && a.label == "push-provider" && a.machine == "mini")
        #expect(a.agent == "mini/a1b2c3" && a.state == .done && a.ending == .asks && a.summary == "Which provider?")
        // Empty strings are nil.
        #expect(a.closedReason == nil && a.result == nil && a.isLive && a.isQuiet)
        #expect(l.links[1].kind == .file && l.links[1].rel == .handoff && l.links[1].title == "brief.md")
        #expect(l.links[2].kind == .loop && l.links[2].rel == .blockedBy)
        #expect(l.yourItems.map(\.id) == ["it-0000aaaa"] && l.machines == ["mini"])
    }

    @Test func decodesLeniently() throws {
        // Only an id: every other field defaulted.
        let bare = try F.decode(Loop.self, #"{"id":"lp-1"}"#)
        #expect(bare.title == "" && bare.displayTitle == "Untitled loop" && bare.status == .open && bare.next == .others)
        #expect(bare.items.isEmpty && bare.links.isEmpty && bare.counts == LoopCounts() && bare.created == nil)
        // Unknown fields ignored, unknown values safe, bad entries skipped.
        let odd = try F.decode(Loop.self, """
        {"id":"lp-2","status":"archived","next":"robots","future":{"x":1},"counts":{"agents":"two","working":1.0},
         "items":[{"id":"it-1","owner":"martians","state":"??","kind":"riddle","order":"x"},7,
                  {"id":"it-2","text":"t","after":["it-1"]}],
         "links":[{"kind":"teleport","ref":"x","rel":"befriends","state":"dreaming","ending":"maybe"},"junk"]}
        """)
        #expect(odd.status == .open && odd.next == .others)
        #expect(odd.counts.agents == 0 && odd.counts.working == 1)
        #expect(odd.items.count == 2)
        #expect(odd.items[0].owner == .agent && odd.items[0].state == .open && odd.items[0].kind == .step && odd.items[0].order == 0)
        // No after: ready; after without "ready": not ready.
        #expect(odd.items[0].ready && !odd.items[1].ready)
        #expect(odd.links.count == 1 && odd.links[0].kind == .other("teleport") && odd.links[0].rel == .member)
        #expect(odd.links[0].state == .unknown && odd.links[0].ending == nil)
        // A missing next follows the status.
        #expect(try F.decode(Loop.self, #"{"id":"x","status":"parked"}"#).next == .parked)
        #expect(try F.decode(Loop.self, #"{"id":"x","status":"done"}"#).next == .closed)
        // No id: an error (the list skips it).
        #expect(throws: (any Error).self) { try F.decode(Loop.self, #"{"title":"x"}"#) }
    }

    @Test func decodesGoalsMilestonesStewardSettings() throws {
        let m = try F.decode(Milestone.self, F.wireMilestone)
        #expect(m.scope == "g-demo" && m.goal == "gl-00aa11bb22cc" && m.title == "Beta" && m.status == .open && m.order == 1)
        #expect(m.due != nil && m.autoFile && m.items.count == 2 && m.items[0].kind == .condition && m.loops == ["lp-0a1b2c3d4e5f"])
        #expect(m.done == 1 && m.total == 2 && m.progress == 0.5 && m.stalled && m.nextText == "Store listing")
        // Without done/total: counted from the items.
        let counted = try F.decode(Milestone.self, #"{"id":"ms-1","items":[{"id":"a","state":"done"},{"id":"b"},{"id":"c","state":"dropped"}]}"#)
        #expect(counted.done == 1 && counted.total == 2 && counted.status == .open)
        #expect(try F.decode(Milestone.self, #"{"id":"ms-2","status":"proposed"}"#).status == .proposed)

        let g = try F.decode(Goal.self, F.wireGoal)
        #expect(g == Goal(id: "gl-00aa11bb22cc", scope: "g-demo", title: "Demo app in the stores", order: 1, status: .open))
        let list = try F.decode(GoalsList.self, "{\"goals\":[\(F.wireGoal),{\"no\":\"id\"}],\"milestones\":[\(F.wireMilestone)]}")
        #expect(list.goals.count == 1 && list.milestones.count == 1)
        #expect(try F.decode(GoalsList.self, "{}") == GoalsList())

        let s = try F.decode(StewardStatus.self, F.wireSteward)
        #expect(s.enabled && s.level == .act && s.node == "n-mini" && s.machine == "mini" && s.agent == "mini/st0001")
        #expect(s.queued == 3 && s.wakes == 12 && s.lastWake != nil && s.session == "s-0001")
        // Missing level: the default (suggest); unknown: watch.
        #expect(try F.decode(StewardStatus.self, "{}") == StewardStatus())
        #expect(try F.decode(StewardStatus.self, #"{"level":"overlord"}"#).level == .watch)

        let set = try F.decode(LoopSettings.self, F.wireSettings)
        #expect(set == LoopSettings(loopLimit: 4, stallDays: 10, eveningAt: "19:30", maxWakesPerDay: 20))
        #expect(try F.decode(LoopSettings.self, #"{"loopLimit":0,"eveningAt":"late"}"#) == LoopSettings())
        #expect(LoopSettings.clock("07:05")! == (7, 5) && LoopSettings.clock("24:00") == nil && LoopSettings.clock("x") == nil)
    }

    @Test func eveningIsLocalTime() {
        var cal = Calendar(identifier: .gregorian)
        cal.timeZone = TimeZone(identifier: "Europe/Berlin")!
        let s = LoopSettings(eveningAt: "19:30")
        let e = s.evening(on: F.t0, calendar: cal)
        #expect(cal.component(.hour, from: e) == 19 && cal.component(.minute, from: e) == 30)
    }

    @Test func agentGainsLoopFields() throws {
        let a = try F.decode(Agent.self, """
        {"id":"L/a1","state":"done","parent":"L/root","loop":"lp-1","service":"","reportToParent":true,"ending":"yourStep"}
        """)
        #expect(a.parent == "L/root" && a.loop == "lp-1" && a.service == nil && !a.isService && a.reportToParent && a.ending == .yourStep)
        let s = try F.decode(Agent.self, #"{"id":"L/s1","service":"steward","ending":"telepathy"}"#)
        #expect(s.isService && s.ending == nil && s.loop == nil && !s.reportToParent)
        // Round trip keeps them.
        let back = try JSONDecoder().decode(Agent.self, from: JSONEncoder().encode(a))
        #expect(back == a)
    }

    // MARK: Notifications

    @Test func decodesEveryNotification() throws {
        let loop = try F.value("{\"loop\":\(F.wireLoop)}")
        guard case .loopChanged(let l) = LoopsEvent.decode(method: "loops.changed", params: loop) else { Issue.record("loops.changed"); return }
        #expect(l.id == "lp-0a1b2c3d4e5f")
        #expect(LoopsEvent.decode(method: "loops.removed", params: ["id": "lp-1", "reason": "merged"]) == .loopRemoved("lp-1"))
        guard case .milestoneChanged(let m) = LoopsEvent.decode(method: "goals.changed", params: try F.value("{\"milestone\":\(F.wireMilestone)}")) else {
            Issue.record("goals.changed milestone"); return
        }
        #expect(m.id == "ms-00aa11bb22cc")
        guard case .goalChanged(let g) = LoopsEvent.decode(method: "goals.changed", params: try F.value("{\"goal\":\(F.wireGoal)}")) else {
            Issue.record("goals.changed goal"); return
        }
        #expect(g.title == "Demo app in the stores")
        #expect(LoopsEvent.decode(method: "goals.removed", params: ["id": "gl-1"]) == .goalRemoved("gl-1"))
        guard case .stewardChanged(let s) = LoopsEvent.decode(method: "steward.changed", params: try F.value("{\"steward\":\(F.wireSteward)}")) else {
            Issue.record("steward.changed"); return
        }
        #expect(s.wakes == 12)
        // Unknown methods and missing payloads: nil.
        #expect(LoopsEvent.decode(method: "loops.exploded", params: [:]) == nil)
        #expect(LoopsEvent.decode(method: "loops.changed", params: [:]) == nil)
        #expect(LoopsEvent.decode(method: "loops.removed", params: ["id": ""]) == nil)
    }

    @Test func storeAppliesEventsAndCountsVersions() {
        var s = LoopStore()
        #expect(s.version == 0 && s.supported)
        s.apply(.loopChanged(F.loop("lp-1")))
        s.apply(.loopChanged(F.loop("lp-2", next: .closed, status: .done)))
        s.apply(.milestoneChanged(Milestone(id: "ms-1", scope: "p", title: "M")))
        s.apply(.goalChanged(Goal(id: "gl-1", scope: "p", title: "G")))
        #expect(s.loops.count == 2 && s.activeLoops.map(\.id) == ["lp-1"] && s.version == 4)
        s.apply(.goalRemoved("ms-1"))
        s.apply(.goalRemoved("gl-1"))
        #expect(s.milestones.isEmpty && s.goals.isEmpty)
        s.apply(.loopRemoved("lp-2"))
        s.apply(.stewardChanged(StewardStatus(enabled: true)))
        #expect(s.loops.keys.sorted() == ["lp-1"] && s.steward.enabled && s.version == 8)
        s.setSettings(LoopSettings())
        #expect(s.version == 8) // no change, no new version
        s.setSettings(LoopSettings(loopLimit: 2))
        #expect(s.version == 9 && s.settings.loopLimit == 2)
        var a = Agent(id: "L/a1"); a.loop = "lp-1"
        #expect(s.loop(of: a)?.id == "lp-1")
        s.listed(loops: nil)
        #expect(!s.supported && s.loops.isEmpty)
        s.listed(loops: [F.loop("lp-3"), F.loop("lp-3", "again")])
        #expect(s.supported && s.loops["lp-3"]?.title == "again")
    }

    @Test func listResultsDecodeBothShapes() throws {
        #expect(DaemonClient.loopList(try F.value("[\(F.wireLoop),{\"bad\":1}]")).map(\.id) == ["lp-0a1b2c3d4e5f"])
        #expect(DaemonClient.loopList(try F.value("{\"loops\":[\(F.wireLoop)]}")).count == 1)
        #expect(DaemonClient.loopList(.null).isEmpty)
    }

    // MARK: Params

    @Test func patchesSendOnlyTheFieldsGiven() {
        #expect(LoopPatch(id: "lp-1", status: .parked).params == ["id": "lp-1", "status": "parked"])
        let p = LoopPatch(title: "New", milestone: "", parkedUntil: F.t0).params
        #expect(p["title"] == "New" && p["milestone"] == "" && p["parkedUntil"]?.stringValue?.hasSuffix("Z") == true && p["id"] == nil)
        #expect(ItemPatch.done("it-1").json == ["id": "it-1", "state": "done"])
        let item = ItemPatch(text: "x", owner: .others, kind: .looseEnd, order: 1.5, after: ["a"], check: ItemCheck(kind: "url", ref: "https://example.com")).json
        #expect(item == ["text": "x", "owner": "others", "kind": "looseEnd", "order": .number(1.5), "after": ["a"],
                         "check": ["kind": "url", "ref": "https://example.com"]])
        #expect(GoalPatch(scope: "g", title: "T").params == ["goal": ["scope": "g", "title": "T"]])
        #expect(MilestonePatch(id: "ms-1", status: .done, autoFile: false).params == ["milestone": ["id": "ms-1", "status": "done", "autoFile": false]])
        #expect(StewardPatch(enabled: true, level: .watch).params == ["enabled": true, "level": "watch"])
        #expect(StewardPatch().isEmpty)
        let link = LoopLink(kind: .url, ref: "https://example.com/pr/1", rel: .touches, label: "PR").json
        #expect(link == ["kind": "url", "ref": "https://example.com/pr/1", "rel": "touches", "label": "PR"])
        #expect(LoopSettings().json == ["loopLimit": 5, "stallDays": 7, "eveningAt": "18:00", "maxWakesPerDay": 40])
    }

    @Test func rpcNamesMatchTheWire() {
        // relay/pkg/wire/loops.go's Method* and Note* constants.
        let names = [LoopsRPC.list, LoopsRPC.get, LoopsRPC.save, LoopsRPC.link, LoopsRPC.item, LoopsRPC.merge, LoopsRPC.split,
                     LoopsRPC.settingsGet, LoopsRPC.settingsSet, LoopsRPC.goalsList, LoopsRPC.goalsSave, LoopsRPC.goalsRemove,
                     LoopsRPC.goalsItem, LoopsRPC.goalsFile, LoopsRPC.goalsAccept, LoopsRPC.stewardStatus, LoopsRPC.stewardSet,
                     LoopsRPC.stewardInbox, LoopsRPC.stewardPlan]
        #expect(names == ["loops.list", "loops.get", "loops.save", "loops.link", "loops.item", "loops.merge", "loops.split",
                          "loops.settings.get", "loops.settings.set", "goals.list", "goals.save", "goals.remove", "goals.item",
                          "goals.file", "goals.accept", "steward.status", "steward.set", "steward.inbox", "steward.plan"])
        #expect([LoopsRPC.loopChanged, LoopsRPC.loopRemoved, LoopsRPC.goalChanged, LoopsRPC.goalRemoved, LoopsRPC.stewardChanged]
            == ["loops.changed", "loops.removed", "goals.changed", "goals.removed", "steward.changed"])
        #expect(LoopsRPC.missing(RPCError(code: -32601, message: "method not found", kind: .notFound)))
    }
}
