import Foundation
import Testing
@testable import HesperCore

struct LoopChipTests {
    typealias F = LoopFixtures

    @Test func chipsForActiveLoopsOnly() {
        let store = LoopStore(loops: [
            F.loop("a", "Push provider", next: .you, items: [F.item("i", "Pick"), F.item("o", "Maybe", kind: .offer)], nextText: "Pick"),
            F.loop("b", next: .agents),
            F.loop("c", next: .you, looksDone: true),
            F.loop("p", next: .you, status: .parked),
            F.loop("d", next: .closed, status: .done),
        ])
        let chips = LoopChip.all(store)
        #expect(Set(chips.keys) == ["a", "b", "c", "p"])
        #expect(chips["a"]?.text == "Push provider")
        #expect(chips["a"]?.detail == "Pick")
        #expect(chips["a"]?.openYou == 1) // the offer is not the person's move
        #expect(chips["a"]?.needsYou == true && chips["a"]?.nextLabel == "Your move")
        #expect(chips["b"]?.nextLabel == "Agents on it")
        #expect(chips["c"]?.needsYou == false && chips["c"]?.nextLabel == "Looks done")
        #expect(chips["p"]?.next == .parked) // a parked status wins over next
        #expect(LoopChip.onYouCount(store) == 1)
    }

    @Test func longTitlesAreCut() {
        let c = LoopChip(F.loop("a", String(repeating: "word ", count: 12)))
        #expect(c.text.count == LoopChip.maxTitle)
        #expect(c.text.hasSuffix("…"))
        #expect(LoopChip(F.loop("b", "Short")).text == "Short")
    }

    @Test func errorTexts() {
        let forbidden = RPCError(code: -32000, message: "agents may not", kind: .forbidden)
        #expect(LoopsErrorText.of(forbidden).hasPrefix("Only a person can do this"))
        let missing = RPCError(code: -32601, message: "no such method", kind: .unknown)
        #expect(LoopsErrorText.of(missing).contains("no loops yet"))
        #expect(LoopsErrorText.of(RPCError(code: -32000, message: "bad title", kind: .invalid)) == "bad title")
    }

    @Test func spawnNamesTheLoopOnlyWhenSet() {
        var r = SpawnRequest(project: "/tmp/fake/p", task: "t")
        #expect(r.params["loop"] == nil)
        r.loop = "lp-1"
        #expect(r.params["loop"] == "lp-1")
        r.loop = ""
        #expect(r.params["loop"] == nil)
    }
}
