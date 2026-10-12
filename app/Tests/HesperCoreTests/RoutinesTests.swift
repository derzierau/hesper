import Foundation
import Testing
@testable import HesperCore

struct RoutinesTests {
    // MARK: Fixtures

    let now = Date(timeIntervalSince1970: 1_760_000_000) // 2025-10-09 08:53:20 UTC

    /// `wire.Routine` as hesperd sends it (every field).
    static let wireJSON = """
    {"id":"r-floor-prices","name":"floor-prices","machine":"L",
     "spawn":{"kind":"claude","project":"/Users/me/projects/acme-apps","task":"Sweep the floor prices.\\nRefuse any change over 15 %.","profile":"claude-unattended"},
     "every":"1h","offset":"5m",
     "policy":{"timeout":"10m","overlap":"replace","catchUp":"none","foreground":true,"keepRuns":50},
     "paused":false,"createdBy":"local","created":"2025-09-09T08:00:00Z","updated":"2025-10-08T07:00:00Z","runs":4,
     "lastRun":{"seq":4,"started":"2025-10-09T08:05:00Z","ended":"2025-10-09T08:09:30Z","agent":"L/a7f3k2","session":"L:claude:abc",
                "outcome":"needs-you","state":"approval","message":"Found 3 units over the limit.","manual":true},
     "nextRun":"2025-10-09T09:05:00Z"}
    """

    func run(_ seq: Int, _ outcome: String, started: TimeInterval = -3600, ended: TimeInterval? = -3000, message: String? = nil, error: String? = nil) -> RoutineRun {
        RoutineRun(seq: seq, started: now.addingTimeInterval(started), ended: ended.map { now.addingTimeInterval($0) }, outcome: outcome,
                   message: message, error: error)
    }

    func routine(_ id: String, every: String? = "1h", at: [String] = [], paused: Bool = false, next: TimeInterval? = 600, last: RoutineRun? = nil) -> Routine {
        Routine(id: id, every: every, at: at, paused: paused, lastRun: last, nextRun: next.map { now.addingTimeInterval($0) })
    }

    // MARK: Wire

    @Test func decodesTheWireRoutineAndRoundTrips() throws {
        let json = try JSONDecoder().decode(JSONValue.self, from: Data(Self.wireJSON.utf8))
        let r = try #require(Routine(json: json))
        #expect(r.id == "r-floor-prices" && r.name == "floor-prices" && r.machine == "L")
        #expect(r.task.hasPrefix("Sweep the floor prices.") && r.project == "/Users/me/projects/acme-apps" && r.kind == "claude")
        #expect(r.every == "1h" && r.offset == "5m" && r.at.isEmpty)
        #expect(r.policy == RunPolicy(timeout: "10m", overlap: "replace", catchUp: "none", foreground: true, keepRuns: 50))
        #expect(!r.paused && r.createdBy == "local" && r.runs == 4)
        #expect(r.created == Session.date("2025-09-09T08:00:00Z") && r.nextRun == Session.date("2025-10-09T09:05:00Z"))
        let l = try #require(r.lastRun)
        #expect(l.seq == 4 && l.outcome == "needs-you" && l.needsYou && l.isLive && !l.isRunning && l.manual)
        #expect(l.agent == "L/a7f3k2" && l.session == "L:claude:abc" && l.state == "approval" && l.message == "Found 3 units over the limit.")
        #expect(l.ended == Session.date("2025-10-09T08:09:30Z"))
        // The spawn goes back whole (the profile too), and every field survives a round trip.
        let again = try #require(Routine(json: r.param))
        #expect(again == r)
        #expect(r.param["spawn"]?["profile"] == "claude-unattended")
        #expect(r.param["policy"] == ["timeout": "10m", "overlap": "replace", "catchUp": "none", "foreground": true, "keepRuns": 50])
        #expect(Routine.list(json: .array([json, ["name": "no id"], ["id": "r-x"]])).map(\.id) == ["r-floor-prices", "r-x"])
        #expect(Routine.list(json: ["routines": .array([json])]).count == 1)
    }

    @Test func decodesLeniently() throws {
        let r = try #require(Routine(json: ["id": "r-digest", "at": ["06:30"], "policy": ["timeout": ""]]))
        // No name: the id without "r-"; missing policy fields: the defaults; no spawn: empty task.
        #expect(r.name == "digest" && r.task.isEmpty && r.project == nil && r.kind == nil)
        #expect(r.policy == RunPolicy() && r.policy.timeout == "1h" && r.policy.overlap == "skip" && r.policy.catchUp == "once" && r.policy.keepRuns == 200)
        #expect(r.every == nil && r.at == ["06:30"] && r.lastRun == nil && r.nextRun == nil && r.runs == 0)
        #expect(RunPolicy(json: nil) == RunPolicy())
        // An unknown outcome keeps its text and is neutral; an empty one reads as running.
        let odd = try #require(RoutineRun(json: ["seq": 7, "outcome": "paused-by-hand"]))
        #expect(odd.outcome == "paused-by-hand" && RoutineText.tone(odd.outcome) == .neutral && !odd.isLive)
        #expect(RoutineRun(json: ["seq": 1])?.isRunning == true)
        #expect(RoutineRun(json: "nope") == nil)
        #expect(RoutineRun.list(json: ["runs": .array([["seq": 2, "outcome": "done"], ["seq": 1, "outcome": "done"]])]).map(\.seq) == [2, 1])
        // Paused: the minimal save param carries only what is set.
        let p = Routine(id: "r-x", every: "15m", paused: true).param
        #expect(p["paused"] == true && p["at"] == nil && p["offset"] == nil && p["runs"] == nil && p["lastRun"] == nil && p["machine"] == nil)
    }

    @Test func runParamsRoundTrip() throws {
        let r = run(3, "timed-out", message: "half done", error: "timed out after 10m0s")
        var m = r
        m.manual = true
        m.agent = "L/a1"
        let again = try #require(RoutineRun(json: m.param))
        #expect(again == m)
        #expect(m.param["manual"] == true && r.param["manual"] == nil && r.param["agent"] == nil)
    }

    // MARK: Support

    @Test func missingMethodMeansNoRoutines() {
        var s = RoutinesSupport()
        #expect(!s.available && s.local == nil)
        let missing = s.note(RPCError(code: -32601, message: "method not found: routines.list", kind: .notFound))
        #expect(missing && s.local == false && !s.available)
        var ok = RoutinesSupport(local: true)
        #expect(ok.available)
        let busy = ok.note(RPCError(code: -32000, message: "busy", kind: .remote))
        #expect(!busy && ok.available) // another error changes nothing
        let relayed = ok.note(RPCError(code: -32000, message: "Unknown method routines.list", kind: .remote)) // through the relay
        #expect(relayed && !ok.available)
    }

    // MARK: Texts

    @Test func cadenceText() {
        #expect(RoutineText.cadence(Routine(id: "r-a", every: "1h", offset: "5m")) == "every 1h +5m")
        #expect(RoutineText.cadence(Routine(id: "r-a", every: "15m")) == "every 15m")
        #expect(RoutineText.cadence(Routine(id: "r-a", every: "24h", offset: "0s")) == "every 24h") // hesperd fills a zero offset in
        #expect(RoutineText.cadence(Routine(id: "r-a", at: ["07:30", "18:00"])) == "daily 07:30, 18:00")
        #expect(RoutineText.cadence(Routine(id: "r-a")) == "no cadence")
    }

    @Test func nextRunText() {
        #expect(RoutineText.nextRun(routine("r-a", next: 12 * 60 + 30), now: now) == "in 12m")
        #expect(RoutineText.nextRun(routine("r-a", next: 3 * 3600 + 7), now: now) == "in 3h")
        #expect(RoutineText.nextRun(routine("r-a", next: 2 * 86400 + 60), now: now) == "in 2d")
        #expect(RoutineText.nextRun(routine("r-a", next: 20), now: now) == "in <1m")
        #expect(RoutineText.nextRun(routine("r-a", next: -5), now: now) == "due")
        #expect(RoutineText.nextRun(routine("r-a", next: 0), now: now) == "due")
        #expect(RoutineText.nextRun(routine("r-a", paused: true, next: nil), now: now) == "paused")
        #expect(RoutineText.nextRun(routine("r-a", paused: true, next: 60), now: now) == "paused") // paused wins over a stale nextRun
        #expect(RoutineText.nextRun(routine("r-a", next: nil), now: now) == "unscheduled")
    }

    @Test func lastRunText() {
        #expect(RoutineText.lastRun(routine("r-a", last: nil), now: now) == "never ran")
        #expect(RoutineText.lastRun(routine("r-a", last: run(1, "done", ended: -48 * 60)), now: now) == "done 48m ago")
        #expect(RoutineText.lastRun(routine("r-a", last: run(1, "running", ended: nil)), now: now) == "running")
        #expect(RoutineText.lastRun(routine("r-a", last: run(1, "needs-you", ended: -60)), now: now) == "needs you")
        #expect(RoutineText.lastRun(routine("r-a", last: run(1, "timed-out", ended: -2 * 3600)), now: now) == "timed out 2h ago")
        #expect(RoutineText.lastRun(routine("r-a", last: run(1, "skipped", started: -3 * 86400, ended: nil)), now: now) == "skipped 3d ago") // no end: its start
        #expect(RoutineText.lastRun(routine("r-a", last: run(1, "failed", ended: -10)), now: now) == "failed now")
        #expect(RoutineText.lastRun(routine("r-a", last: RoutineRun(seq: 1, outcome: "exited")), now: now) == "exited") // no times at all
    }

    @Test func outcomeTones() {
        #expect(RoutineText.tone("done") == .good)
        #expect(RoutineText.tone("needs-you") == .attention)
        #expect(RoutineText.tone("error") == .critical)
        #expect(RoutineText.tone("timed-out") == .critical)
        #expect(RoutineText.tone("failed") == .critical)
        #expect(RoutineText.tone("running") == .neutral)
        #expect(RoutineText.tone("exited") == .neutral)
        #expect(RoutineText.tone("skipped") == .neutral)
        #expect(RoutineText.tone("whatever") == .neutral)
        #expect(RoutineText.outcome("needs-you") == "needs you" && RoutineText.outcome("timed-out") == "timed out" && RoutineText.outcome("done") == "done")
    }

    @Test func summaryLine() {
        #expect(RoutineText.summaryLine(run(1, "done", message: "Swept 48 units; raised 3.\nDetails follow…")) == "Swept 48 units; raised 3.")
        #expect(RoutineText.summaryLine(run(1, "done", message: "\n\n  second line is first  \nmore")) == "second line is first")
        #expect(RoutineText.summaryLine(run(1, "failed", error: "agents.spawn: claude not found")) == "agents.spawn: claude not found")
        // A message wins over an error; an empty message falls back to the error; nothing is "".
        #expect(RoutineText.summaryLine(run(1, "timed-out", message: "half", error: "timed out")) == "half")
        #expect(RoutineText.summaryLine(run(1, "timed-out", message: "", error: "timed out")) == "timed out")
        #expect(RoutineText.summaryLine(run(1, "running")) == "")
        let long = String(repeating: "x", count: 200)
        let cut = RoutineText.summaryLine(run(1, "done", message: long))
        #expect(cut.count == 81 && cut.hasSuffix("…"))
    }

    @Test func durationAndPolicyText() {
        #expect(RoutineText.duration(run(1, "done", started: -600, ended: -408), now: now) == "3m 12s")
        #expect(RoutineText.duration(run(1, "done", started: -3900 - 300, ended: -300), now: now) == "1h 05m")
        #expect(RoutineText.duration(run(1, "done", started: -100, ended: -58), now: now) == "42s")
        #expect(RoutineText.duration(run(1, "running", started: -90, ended: nil), now: now) == "1m 30s") // still going: until now
        #expect(RoutineText.duration(run(1, "skipped", started: -90, ended: nil), now: now) == "") // never ran
        #expect(RoutineText.duration(RoutineRun(seq: 1), now: now) == "")
        #expect(RoutineText.policy(RunPolicy()) == "timeout 1h · overlap skip · catch up once · background · keeps 200 runs")
        #expect(RoutineText.policy(RunPolicy(timeout: "10m", overlap: "replace", foreground: true, keepRuns: 1)) == "timeout 10m · overlap replace · catch up once · foreground · keeps 1 run")
    }

    // MARK: Ranking

    @Test func ranksNeedsYouThenRunningThenByNextRun() {
        let list = [
            routine("r-paused", paused: true, next: nil, last: run(1, "done")),
            routine("r-later", next: 7200, last: run(1, "done")),
            routine("r-running-old", next: 60, last: run(2, "running", started: -1200, ended: nil)),
            routine("r-needs-old", next: 30, last: run(3, "needs-you", ended: -3000)),
            routine("r-soon", next: 300, last: run(1, "timed-out")),
            routine("r-needs-new", next: 86400, last: run(3, "needs-you", ended: -60)),
            routine("r-running-new", next: 3600, last: run(2, "running", started: -30, ended: nil)),
            routine("r-never", next: 500, last: nil),
            routine("r-unscheduled", next: nil, last: run(1, "exited")),
        ]
        #expect(RoutineList.ranked(list).map(\.id) == ["r-needs-new", "r-needs-old", "r-running-new", "r-running-old", "r-soon", "r-never", "r-later",
                                                      "r-paused", "r-unscheduled"])
        // Ties on everything: by name, then id (the order given does not matter).
        let tie = [Routine(id: "r-b", name: "same"), Routine(id: "r-a", name: "same"), Routine(id: "r-c", name: "Other")]
        #expect(RoutineList.ranked(tie).map(\.id) == ["r-c", "r-a", "r-b"])
        #expect(RoutineList.ranked(tie.reversed()).map(\.id) == ["r-c", "r-a", "r-b"])
    }

    // MARK: Keys

    @Test func keys() {
        #expect(RoutineKeys.route(KeyChord(.up)) == .previous)
        #expect(RoutineKeys.route(KeyChord(.down)) == .next)
        #expect(RoutineKeys.route(KeyChord(.enter)) == .runNow)
        #expect(RoutineKeys.route(KeyChord(.char("p"))) == .togglePause)
        #expect(RoutineKeys.route(KeyChord(.char("P"), shift: true)) == .togglePause)
        #expect(RoutineKeys.route(KeyChord(.char("o"))) == .open)
        #expect(RoutineKeys.route(KeyChord(.delete)) == .remove)
        #expect(RoutineKeys.route(KeyChord(.char("\u{7f}")), keyCode: 51) == .remove) // ⌫ as AppKit sends it
        #expect(RoutineKeys.route(KeyChord(.char("\u{7f}"), command: true), keyCode: 51) == .pass)
        #expect(RoutineKeys.route(KeyChord(.escape)) == .close)
        #expect(RoutineKeys.route(KeyChord(.char("r"), command: true, shift: true)) == .close)
        #expect(RoutineKeys.route(KeyChord(.char("R"), command: true, shift: true)) == .close)
        // Other ⌘ keys (⌘K, ⌘Y, ⌘R, ⌘J), ⌥ and ⌃ chords and letters the panel has no use for pass.
        #expect(RoutineKeys.route(KeyChord(.char("k"), command: true)) == .pass)
        #expect(RoutineKeys.route(KeyChord(.char("r"), command: true)) == .pass)
        #expect(RoutineKeys.route(KeyChord(.enter, option: true)) == .pass)
        #expect(RoutineKeys.route(KeyChord(.char("p"), control: true)) == .pass)
        #expect(RoutineKeys.route(KeyChord(.char("x"))) == .pass)
    }

    @Test func routerHasTheShortcut() {
        #expect(KeyRouter.route(KeyChord(.char("r"), command: true, shift: true), mode: .wall, selectedState: nil) == .routines)
        #expect(KeyRouter.route(KeyChord(.char("R"), command: true, shift: true), mode: .focus, selectedState: .working) == .routines)
        #expect(KeyRouter.route(KeyChord(.char("r"), command: true, shift: true), mode: .compose, selectedState: nil) == .routines)
        #expect(KeyRouter.route(KeyChord(.char("r"), command: true), mode: .wall, selectedState: nil) == .review) // ⌘R stays Review's
    }
}
