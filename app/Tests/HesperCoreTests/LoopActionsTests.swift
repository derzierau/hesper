import Foundation
import Testing
@testable import HesperCore

struct LoopActionsTests {
    typealias F = LoopFixtures
    var utc: Calendar { var c = Calendar(identifier: .gregorian); c.timeZone = TimeZone(identifier: "UTC")!; return c }

    @Test func parkUntilTomorrowMorning() {
        let now = F.date(0) // 2026-10-26 08:53 UTC
        let until = LoopActions.parkUntil(now: now, calendar: utc)
        #expect(utc.component(.hour, from: until) == 9)
        #expect(utc.dateComponents([.day], from: utc.startOfDay(for: now), to: utc.startOfDay(for: until)).day == 1)
        let parked = F.loop("p", status: .parked)
        #expect(LoopActions.park(parked) == LoopPatch(id: "p", status: .open))
        #expect(LoopActions.park(F.loop("o"), now: now, calendar: utc) == LoopPatch(id: "o", status: .parked, parkedUntil: until))
        #expect(LoopActions.done("a").status == .done && LoopActions.drop("a").status == .dropped)
    }

    @Test func checkToggles() {
        #expect(LoopActions.check(F.item("i", "x")) == ItemPatch(id: "i", state: .done))
        #expect(LoopActions.check(F.item("i", "x", state: .done)) == ItemPatch(id: "i", state: .open))
    }

    @Test func typedSteps() {
        #expect(LoopActions.step("Pick a provider", order: 3) == ItemPatch(text: "Pick a provider", owner: .you, kind: .step, order: 3))
        #expect(LoopActions.step("@agent write the brief", order: 1)?.owner == .agent)
        #expect(LoopActions.step("@others  legal sign-off", order: 1)?.text == "legal sign-off")
        #expect(LoopActions.step("@AGENT", order: 1) == nil)
        #expect(LoopActions.step("   ", order: 1) == nil)
        #expect(LoopActions.nextOrder(F.loop("l", items: [F.item("a", "a", order: 2), F.item("b", "b", order: 5)])) == 6)
    }

    @Test func typedTriggers() {
        let steps = [F.item("a", "one"), F.item("b", "two"), F.item("c", "three")]
        let c = steps[2]
        #expect(LoopActions.trigger("after 1 #2", item: c, steps: steps) == ItemPatch(id: "c", after: ["a", "b"]))
        #expect(LoopActions.trigger("after 3", item: c, steps: steps) == nil) // not itself
        #expect(LoopActions.trigger("after 9", item: c, steps: steps) == nil)
        // "none" (or nothing) clears what it waits on: its steps and its check.
        #expect(LoopActions.trigger("", item: c, steps: steps) == ItemPatch(id: "c", after: [], check: .clear))
        #expect(LoopActions.trigger("none", item: c, steps: steps) == ItemPatch(id: "c", after: [], check: .clear))
        #expect(LoopActions.trigger("tag v1.2", item: c, steps: steps)?.check == .set(ItemCheck(kind: "tag", ref: "v1.2")))
        #expect(LoopActions.trigger("https://example.com/acme/demo/pull/7", item: c, steps: steps)?.check.value?.kind == "pr")
        #expect(LoopActions.trigger("https://example.com/status", item: c, steps: steps)?.check.value?.kind == "url")
        // "after" keeps the check.
        #expect(LoopActions.trigger("after 1", item: c, steps: steps)?.check == .keep)
        #expect(LoopActions.trigger("soon", item: c, steps: steps) == nil)
    }

    @Test func triggerWords() {
        var c = F.item("c", "three")
        let steps = [F.item("a", "one"), F.item("b", "two"), c]
        #expect(LoopActions.triggerText(c, steps: steps) == nil)
        c.after = ["a", "b"]
        #expect(LoopActions.triggerText(c, steps: steps) == "after 1, 2")
        #expect(LoopActions.triggerField(c, steps: steps) == "after 1 2")
        c.after = ["gone"]
        #expect(LoopActions.triggerText(c, steps: steps) == "after a removed step")
        c.after = []
        c.check = ItemCheck(kind: "pr", ref: "https://example.com/p/1")
        #expect(LoopActions.triggerText(c, steps: steps) == "PR merged")
        #expect(LoopActions.triggerField(c, steps: steps) == "pr https://example.com/p/1")
    }

    @Test func rollUpAndDraft() {
        var waiting = F.item("w", "Ship", owner: .agent, order: 3)
        waiting.ready = false
        var drafted = F.item("d", "Write tests", owner: .agent, order: 2)
        drafted.draft = "d-1"
        let l = F.loop("l", items: [F.item("a", "Brief", state: .done, order: 0), F.item("b", "Pick the provider", order: 1), drafted, waiting,
                                    F.item("o", "Maybe docs", owner: .agent, kind: .offer, order: 4)])
        #expect(LoopActions.steps(l).map(\.id) == ["a", "b", "d", "w"])
        #expect(LoopActions.rollUp(l) == "1 of 4 done · 1 yours · 1 waiting · next: Pick the provider")
        #expect(LoopActions.rollUp(F.loop("e")) == nil)
        #expect(LoopActions.draftedNext(l)?.id == "d")
    }

    @Test func mergeTargetsMatchWords() {
        let me = F.loop("me", "Push provider")
        let list = [me, F.loop("a", "Push docs", updated: F.date(1)), F.loop("b", "Provider push tests", updated: F.date(2)),
                    F.loop("c", "Unrelated"), F.loop("d", "Push old", status: .done)]
        #expect(LoopActions.mergeTargets(for: me, in: list, query: "").map(\.id) == ["b", "a", "c"])
        #expect(LoopActions.mergeTargets(for: me, in: list, query: "push PROV").map(\.id) == ["b"])
    }
}
