import Foundation
import Testing
@testable import HesperCore

/// A plain click on a wall tile, and the input held while the active
/// tile's read-write terminal swaps in.
@Suite struct TileClickTests {
    func click(_ count: Int = 1, active: Bool = false, selected: Bool = false, running: Bool = true, onShelf: Bool = false,
               state: AgentState = .working, answers: [Decision] = []) -> TileClick {
        TileClick.resolve(clickCount: count, active: active, selected: selected, running: running, onShelf: onShelf, state: state, answers: answers)
    }

    @Test func oneClickMakesALiveTileActive() {
        #expect(click() == .activate)
        #expect(click(selected: true) == .activate)
        #expect(click(state: .idle) == .activate)
        #expect(click(state: .error) == .activate)
        #expect(click(state: .question) == .activate) // a question without answers: the agent's own prompt
    }

    @Test func doubleClickOpensFocus() {
        #expect(click(2) == .focus)
        #expect(click(2, selected: true) == .focus)
        #expect(click(3) == .focus)
        #expect(click(2, running: false) == .focus)
    }

    @Test func theActiveTileKeepsItsInput() {
        #expect(click(active: true) == .input)
        // The second click of the activating click still opens focus
        // (TileClick.claimsSecondClick routes it to the tile).
        #expect(click(2, active: true) == .focus)
    }

    @Test func noTerminalOnlySelects() {
        #expect(click(running: false) == .select)
        #expect(click(onShelf: true) == .select)
        #expect(click(selected: true, onShelf: true) == .select)
    }

    @Test func aTileThatNeedsAnAnswerSelectsFirst() {
        // ⏎ / A / N answer on the selected tile; a second click types.
        #expect(click(state: .approval) == .select)
        #expect(click(selected: true, state: .approval) == .activate)
        #expect(click(state: .question, answers: [.trust, .exit]) == .select)
        #expect(click(selected: true, state: .question, answers: [.trust]) == .activate)
    }

    @Test func theSecondClickOfTheActivatingClickGoesToTheTile() {
        let interval = 0.5
        #expect(TileClick.claimsSecondClick(clickCount: 2, sinceActivation: 0.2, doubleClickInterval: interval))
        #expect(!TileClick.claimsSecondClick(clickCount: 2, sinceActivation: 0.6, doubleClickInterval: interval))
        #expect(!TileClick.claimsSecondClick(clickCount: 1, sinceActivation: 0.2, doubleClickInterval: interval))
        #expect(!TileClick.claimsSecondClick(clickCount: 2, sinceActivation: nil, doubleClickInterval: interval))
        #expect(!TileClick.claimsSecondClick(clickCount: 2, sinceActivation: -0.1, doubleClickInterval: interval))
    }

    @Test func heldInputReplaysInOrder() {
        var h = HeldInput<Int>(capacity: 4, maxAge: 1)
        h.hold(1, at: 0)
        h.hold(2, at: 0.1)
        h.hold(3, at: 0.2)
        #expect(h.count == 3)
        #expect(h.drain(now: 0.5) == [1, 2, 3])
        #expect(h.isEmpty)
        #expect(h.drain(now: 0.6) == [])
    }

    @Test func heldInputDropsStaleEvents() {
        var h = HeldInput<Int>(capacity: 8, maxAge: 1)
        h.hold(1, at: 0)
        h.hold(2, at: 0.8)
        // Nothing older than maxAge is replayed: a late swap doesn't type
        // or click what the user has given up on.
        #expect(h.drain(now: 1.5) == [2])
    }

    @Test func heldInputKeepsTheFirstEventsWhenFull() {
        var h = HeldInput<Int>(capacity: 2, maxAge: 1)
        h.hold(1, at: 0)
        h.hold(2, at: 0)
        h.hold(3, at: 0) // dropped: the newest goes, never a gesture's start
        #expect(h.drain(now: 0) == [1, 2])
    }

    @Test func heldInputClears() {
        var h = HeldInput<Int>(capacity: 2, maxAge: 1)
        h.hold(1, at: 0)
        h.clear()
        #expect(h.isEmpty)
    }
}
