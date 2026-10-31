import Foundation
import Testing
@testable import HesperCore

struct WrapUpTests {
    typealias F = LoopFixtures

    var cal: Calendar {
        var c = Calendar(identifier: .gregorian)
        c.timeZone = TimeZone(identifier: "Europe/Berlin")!
        return c
    }

    /// 20:00 local on the fixture day.
    var evening: Date { cal.date(bySettingHour: 20, minute: 0, second: 0, of: F.t0)! }
    var morning: Date { cal.date(bySettingHour: 8, minute: 0, second: 0, of: F.t0)! }
    var midnight: Date { cal.startOfDay(for: F.t0) }

    func days(_ n: Double) -> Date { evening.addingTimeInterval(-n * 86_400) }

    func sample() -> [Loop] {
        [
            F.loop("agents", next: .agents, links: [F.agent("n/a", agent: "L/a", state: .working, added: morning)], created: days(2), updated: morning),
            F.loop("step", next: .you, items: [F.item("i1", "Merge the PR"), F.item("i2", "Tag it", order: 1)], created: days(5), updated: days(5)),
            F.loop("looks", next: .you, looksDone: true, created: morning, updated: morning),
            F.loop("others", next: .others, created: days(1), updated: morning.addingTimeInterval(60)),
            F.loop("parked", next: .parked, status: .parked, created: days(9), updated: morning),
            F.loop("debris", next: .agents, links: [F.agent("n/x", agent: "L/x", state: .error), F.agent("n/y", closed: days(0.5))],
                   created: morning, updated: morning),
            F.loop("stale", next: .agents, links: [F.agent("n/s", summary: "did some")], created: days(10), updated: days(4)),
            F.loop("yesterday", next: .agents, created: days(1), updated: days(1)),
            F.loop("done-today", next: .closed, status: .done, created: days(3), updated: morning, closed: morning),
            F.loop("done-before", next: .closed, status: .done, created: days(3), updated: days(2), closed: days(2)),
        ]
    }

    @Test func todaysLoopsInOrderWithOneSuggestionEach() {
        let w = WrapUp(loops: sample(), now: evening, calendar: cal)
        // Not touched today and not on you: out ("stale", "yesterday"); closed: out.
        #expect(w.entries.map(\.id) == ["step", "looks", "agents", "debris", "others", "parked"])
        let s = Dictionary(w.entries.map { ($0.id, ($0.suggestion, $0.reason)) }, uniquingKeysWith: { a, _ in a })
        #expect(s["looks"]! == (.done, "Nothing open and every agent is quiet"))
        #expect(s["step"]! == (.nextStep("Merge the PR"), "2 steps are yours"))
        #expect(s["debris"]! == (.drop, "Only failed or exited agents, nothing open"))
        #expect(s["agents"]! == (.keep, "Agents are working"))
        #expect(s["others"]! == (.keep, "Waiting on others"))
        #expect(s["parked"]! == (.keep, "Parked"))
    }

    @Test func suggestionRules() {
        // Park after three days without change (and not when it looks done or has your step).
        let stale = F.loop("s", next: .agents, links: [F.agent("n/s", summary: "x")], updated: days(3))
        #expect(WrapUp.suggest(stale, now: evening).0 == .park)
        #expect(WrapUp.suggest(F.loop("s", next: .agents, updated: days(2.9)), now: evening).0 == .keep)
        #expect(WrapUp.suggest(F.loop("s", next: .you, looksDone: true, updated: days(30)), now: evening).0 == .done)
        // hesperd marks debris "looks done" too (every agent quiet): drop wins.
        let debris = F.loop("d", next: .you, looksDone: true, links: [F.agent("n/x", agent: "L/x", state: .error)])
        #expect(WrapUp.suggest(debris, now: evening).0 == .drop)
        // One step.
        #expect(WrapUp.suggest(F.loop("s", next: .you, items: [F.item("i", "Do it")]), now: evening) == (.nextStep("Do it"), "One step is yours"))
        // Debris needs every agent failed or gone without a trace, and nothing else linked.
        #expect(WrapUp.isDebris(F.loop("d", links: [F.agent("n/x", agent: "L/x", state: .exited)])))
        #expect(!WrapUp.isDebris(F.loop("d", links: [F.agent("n/x", agent: "L/x", state: .error), F.agent("n/y", agent: "L/y", state: .done)])))
        #expect(!WrapUp.isDebris(F.loop("d", links: [F.agent("n/x", closed: F.t0, added: nil)].map { var k = $0; k.result = "shipped"; return k })))
        #expect(!WrapUp.isDebris(F.loop("d", links: [F.agent("n/x", agent: "L/x", state: .error), LoopLink(kind: .url, ref: "https://example.com")])))
        #expect(!WrapUp.isDebris(F.loop("d", items: [F.item("i", "x", owner: .agent)], links: [F.agent("n/x", agent: "L/x", state: .error)])))
        #expect(!WrapUp.isDebris(F.loop("d")))
    }

    @Test func tallies() {
        let w = WrapUp(loops: sample(), now: evening, calendar: cal)
        // Started today: looks, debris. Finished today: done-today. On you: step, looks. Parked: parked.
        #expect(w.tallies.startedToday == 2 && w.tallies.finishedToday == 1 && w.tallies.onYou == 2 && w.tallies.parked == 1)
        #expect(w.startOfDay == midnight)
        // Parking lasts until tomorrow 09:00 local.
        #expect(cal.component(.hour, from: w.parkUntil) == 9 && cal.dateComponents([.day], from: midnight, to: w.parkUntil).day == 1)
    }

    @Test func progressAndPatches() {
        var w = WrapUp(loops: sample(), now: evening, calendar: cal)
        #expect(w.progress == 0 && w.nextUndecided?.id == "step" && !w.isFinished)
        w.accept("looks")
        w.decide("debris", .drop)
        w.decide("others", .park)
        w.decide("nope", .done) // not an entry: ignored
        #expect(w.decidedCount == 3 && w.progress == 0.5)
        #expect(w.patches.map(\.params) == [
            LoopPatch(id: "looks", status: .done).params,
            LoopPatch(id: "debris", status: .dropped).params,
            LoopPatch(id: "others", status: .parked, parkedUntil: w.parkUntil).params,
        ])
        w.undo("others")
        #expect(w.decidedCount == 2)
        for e in w.entries { w.accept(e.id) }
        #expect(w.isFinished && w.progress == 1 && w.nextUndecided == nil)
        // The next step and keep save nothing.
        #expect(WrapUpDecision.nextStep("x").patch(loop: "a", parkUntil: nil) == nil && WrapUpDecision.keep.patch(loop: "a", parkUntil: nil) == nil)
        #expect(WrapUp(loops: [], now: evening, calendar: cal).progress == 1)
    }

    @Test func dueAfterTheEveningTime() {
        let s = LoopSettings(eveningAt: "18:00")
        #expect(WrapUp.isDue(now: evening, settings: s, calendar: cal))
        #expect(!WrapUp.isDue(now: morning, settings: s, calendar: cal))
    }
}
