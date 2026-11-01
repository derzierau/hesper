import Foundation
import Testing
@testable import HesperCore

struct ServiceShelfTests {
    @Test func serviceAgentsLiveOnTheShelf() {
        func a(_ state: AgentState, service: String?) -> Agent {
            var a = Agent(id: "L/s", state: state)
            a.service = service
            return a
        }
        let g = GridSize(cols: 120, rows: 40)
        func input(_ x: Agent) -> WallTileInput { WallTileInput(grid: g, state: x.state, shelved: WallTileInput.shelves(x)) }
        // Working or idle: the Steward is quiet; a plain working agent is not.
        #expect(input(a(.working, service: "steward")).isQuiet)
        #expect(input(a(.idle, service: "planner")).isQuiet)
        #expect(!input(a(.working, service: nil)).isQuiet)
        // Needing the person brings it off the shelf.
        for s in [AgentState.approval, .question, .error] { #expect(!input(a(s, service: "steward")).isQuiet) }
        // The default stays: no shelving unless asked.
        #expect(!WallTileInput(grid: g).isQuiet)
    }
}
