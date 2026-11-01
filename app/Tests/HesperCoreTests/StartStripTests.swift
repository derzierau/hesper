import Foundation
import Testing
@testable import HesperCore

struct StartStripTests {
    typealias F = LoopFixtures

    static let loops = [
        F.loop("lp-push", "Push provider for the demo app", next: .agents,
               items: [F.item("le1", "Write the provider brief", owner: .agent, kind: .looseEnd)], projects: ["p-demo"]),
        F.loop("lp-you", "Docs", next: .you, items: [F.item("i", "Merge the PR")]),
    ]

    @Test func matchLooseEndAndMeter() {
        let ix = StartCheckIndex(loops: Self.loops, loopLimit: 1)
        let r = ix.check(task: "write the provider brief for push", project: "p-demo")
        let s = StartStrip(r)
        #expect(s.text == "This looks like part of Push provider for the demo app")
        #expect(s.looseEnd == "Ticks off: Write the provider brief")
        #expect(s.meter == "1 / 1 on you" && s.atLimit)
        #expect(s.add == .addToLoop(loop: "lp-push", looseEnd: "le1"))
        #expect(s.draft == .draftInLoop(loop: "lp-push"))
        #expect(StartStrip.shows(r, task: "write the provider brief"))
        // Nothing typed (or only stop words): no strip.
        #expect(!StartStrip.shows(r, task: "the and of"))
        #expect(!StartStrip.shows(nil, task: "anything"))
        // No match, nobody waiting: no strip; no match but loops on you: the meter alone.
        let none = StartCheckIndex(loops: [Self.loops[0]]).check(task: "rotate the signing keys")
        #expect(none.match == nil && !StartStrip.shows(none, task: "rotate the signing keys"))
        let meterOnly = ix.check(task: "rotate the signing keys")
        #expect(StartStrip.shows(meterOnly, task: "rotate the signing keys"))
        #expect(StartStrip(meterOnly).add == nil && StartStrip(meterOnly).text == nil)
        #expect(!StartStrip(StartCheckIndex(loops: Self.loops).check(task: "x")).atLimit) // 1 of 5
    }

    @Test func keysDoNotTakeTheComposersOwn() {
        let s = StartStrip(StartCheckIndex(loops: Self.loops).check(task: "write the provider brief", project: "p-demo"))
        #expect(s.choice(for: KeyChord(.enter, command: true, shift: true)) == .addToLoop(loop: "lp-push", looseEnd: "le1"))
        #expect(s.choice(for: KeyChord(.char("d"), command: true)) == .draftInLoop(loop: "lp-push"))
        // ⌘⏎ is Start, ⌥⏎ start-and-next, ⇧⌘D the desk picker: never the strip's.
        #expect(s.choice(for: KeyChord(.enter, command: true)) == nil)
        #expect(s.choice(for: KeyChord(.enter, option: true)) == nil)
        #expect(s.choice(for: KeyChord(.char("d"), command: true, shift: true)) == nil)
        #expect(s.choice(for: KeyChord(.char("d"))) == nil)
        // The window's router leaves both to the composer's text view.
        #expect(KeyRouter.route(KeyChord(.enter, command: true, shift: true), mode: .compose, selectedState: nil) == .passThrough)
        #expect(KeyRouter.route(KeyChord(.char("d"), command: true), mode: .compose, selectedState: nil) == .passThrough)
        // No match: no keys.
        #expect(StartStrip(StartCheckResult(match: nil, onYou: 3, loopLimit: 5)).choice(for: KeyChord(.char("d"), command: true)) == nil)
    }

    @Test func draftItemAndSpawnLoop() {
        #expect(StartStrip.draftItem(task: "Write the brief\nmore detail", draft: "d-1") ==
                ItemPatch(text: "Write the brief", owner: .agent, kind: .step, draft: "d-1"))
        var req = SpawnRequest(project: "/tmp/fake/demo", task: "go")
        #expect(req.params.objectValue?["loop"] == nil)
        req.loop = "lp-push"
        #expect(req.params.objectValue?["loop"] == .string("lp-push"))
    }
}
