import Foundation
import Testing
@testable import HesperCore

struct TileLoopTagTests {
    typealias F = LoopFixtures

    @Test func tagFromTheAgentsLoop() {
        let store = LoopStore(loops: [
            F.loop("a", "Push provider", next: .you, nextText: "Pick the provider"),
            F.loop("b", next: .agents),
            F.loop("c", next: .you, looksDone: true),
            F.loop("p", next: .you, status: .parked),
            F.loop("d", next: .closed, status: .done),
        ])
        let chips = LoopChip.all(store)
        func tag(_ loop: String?, service: String? = nil) -> TileLoopTag? {
            var a = Agent(id: "L/x", state: .working)
            a.loop = loop
            a.service = service
            return TileLoopTag.of(a, chips: chips)
        }
        #expect(tag("a") == TileLoopTag(text: "Push provider", next: "Your move", mark: .question, help: "Pick the provider", loop: "a"))
        #expect(tag("b")?.mark == .working && tag("b")?.next == "Agents on it")
        #expect(tag("c")?.mark == .done && tag("c")?.next == "Looks done")
        #expect(tag("p")?.mark == .idle && tag("p")?.next == "Parked")
        // No loop, an unknown one, a closed one: no tag.
        #expect(tag(nil) == nil && tag("zz") == nil && tag("d") == nil)
        // A service agent shows its role, never a loop, and never red.
        #expect(tag("a", service: "steward") == TileLoopTag(text: "Steward", next: nil, mark: nil, help: nil, loop: nil))
        #expect(tag(nil, service: "planner")?.text == "Planner")
        #expect(TileLoopTag.serviceName("archivist") == "Archivist")
        #expect(StateMarkKind.allCases.filter { k in [tag("a"), tag("b"), tag("c"), tag("p")].contains { $0?.mark == k } }.allSatisfy { $0.tone != .signal })
    }
}
