import Foundation
import Testing
@testable import HesperCore

struct YourMoveTests {
    typealias F = LoopFixtures

    func agent(_ id: String, _ state: AgentState, since: Double) -> Agent {
        Agent(id: id, state: state, stateSince: F.date(since))
    }

    @Test func queueUnchangedThenYourMove() {
        let agents = [agent("L/e", .error, since: 0), agent("L/q", .question, since: 1), agent("L/a", .approval, since: 2), agent("L/w", .working, since: 0)]
        let loops = [
            F.loop("lp-item", next: .you, items: [F.item("i1", "Merge the PR", created: F.date(3))], updated: F.date(3)),
            F.loop("lp-ask", next: .you, links: [F.agent("n/x", label: "api", agent: "L/x", state: .done, ending: .asks, summary: "Ship it now?")], updated: F.date(5)),
            // The agent waiting is already in the queue: no second entry.
            F.loop("lp-queued", next: .you, links: [F.agent("n/q", agent: "L/q", state: .question, ending: .asks)], updated: F.date(9)),
        ]
        let s = YourMove.sections(agents: agents, loops: loops)
        #expect(s.count == 2 && s.map(\.title) == ["Needs you", "Your move"])
        guard case .queue(let q) = s[0], case .yourMove(let y) = s[1] else { Issue.record("sections"); return }
        #expect(q.map(\.id) == ["L/a", "L/q", "L/e"]) // AttentionQueue.ordered, unchanged
        #expect(y.map(\.id) == ["lp-ask", "lp-item"]) // newest first
        #expect(y[0].text == "Ship it now?" && y[0].from == "api")
        // Nothing waits: no sections.
        #expect(YourMove.sections(agents: [agent("L/w", .working, since: 0)], loops: []).isEmpty)
    }

    @Test func oneEntryPerLoopAskBeatsStepBeatsItem() {
        let both = F.loop("lp", next: .you, items: [F.item("i1", "Read the brief")], links: [
            F.agent("n/s", agent: "L/s", state: .idle, ending: .yourStep, summary: "Run `make deploy` on the mini", added: F.date(1)),
            F.agent("n/a", agent: "L/a", state: .done, ending: .asks, summary: "Use the new key?", added: F.date(0)),
        ])
        let e = YourMove.entries(loops: [both])
        #expect(e.count == 1)
        guard case .asks(let k) = e[0].reason else { Issue.record("ask first"); return }
        #expect(k.ref == "n/a")
        // Without the ask: the named step.
        var step = both; step.links.removeLast()
        guard case .yourStep = YourMove.entries(loops: [step])[0].reason else { Issue.record("step second"); return }
        // A working agent's old ending doesn't count; the item does.
        var working = both; working.links = working.links.map { var k = $0; k.state = .working; return k }
        guard case .item(let it) = YourMove.entries(loops: [working])[0].reason else { Issue.record("item"); return }
        #expect(it.id == "i1")
        // An ask that already became an item: the item (it can be ticked off).
        let dup = F.loop("lp", next: .you, items: [F.item("i9", "Use the new key?", source: "n/a")], links: [F.agent("n/a", agent: "L/a", state: .done, ending: .asks)])
        guard case .item = YourMove.entries(loops: [dup])[0].reason else { Issue.record("dedupe"); return }
    }

    @Test func offersStayInTheLoop() {
        let l = F.loop("lp", next: .agents, items: [F.item("o", "Want me to add tests?", kind: .offer)],
                       links: [F.agent("n/a", agent: "L/a", state: .done, ending: .offer, summary: "I could also refactor")])
        #expect(YourMove.entries(loops: [l]).isEmpty)
        let o = YourMove.offers(in: l)
        #expect(o.items.map(\.id) == ["o"] && o.agents.map(\.ref) == ["n/a"])
        // Parked and closed loops never show.
        #expect(YourMove.entries(loops: [F.loop("p", next: .parked, status: .parked, items: [F.item("i", "x")]),
                                         F.loop("c", next: .closed, status: .done, items: [F.item("i", "x")])]).isEmpty)
    }

    @Test func quickActions() {
        let ask = F.loop("lp-a", next: .you, links: [F.agent("n/a", agent: "L/a", state: .done, ending: .asks, summary: "Ship?")])
        #expect(YourMove.entries(loops: [ask])[0].actions.map(\.intent) == [
            .answer(agent: "L/a", text: "Yes"), .answer(agent: "L/a", text: "No"), .later(loop: "lp-a"), .open(loop: "lp-a"),
        ])
        // A gone agent can't be answered; ⏎ (the first) opens, never parks.
        let gone = F.loop("lp-g", next: .you, links: [F.agent("n/a", ending: .asks, closed: F.t0)])
        #expect(YourMove.entries(loops: [gone])[0].actions.map(\.title) == ["Open", "Later"])
        let goneStep = F.loop("lp-gs", next: .you, links: [F.agent("n/s", ending: .yourStep, summary: "Run `make check`", closed: F.t0)])
        #expect(YourMove.entries(loops: [goneStep])[0].actions.map(\.title) == ["Open", "Copy command", "Later"])
        let item = F.loop("lp-i", next: .you, items: [F.item("i1", "Run:\n$ hesperctl loops ls")])
        #expect(YourMove.entries(loops: [item])[0].actions.map(\.intent) == [
            .checkOff(loop: "lp-i", item: "i1"), .copy("hesperctl loops ls"), .later(loop: "lp-i"), .open(loop: "lp-i"),
        ])
        #expect(YourMove.entries(loops: [item])[0].actions.map(\.title) == ["Did it", "Copy command", "Later", "Open"])
        let named = F.loop("lp-s", next: .you, links: [F.agent("n/s", agent: "L/s", state: .idle, ending: .yourStep, summary: "Run `make check` please")])
        #expect(YourMove.entries(loops: [named])[0].actions.map(\.intent) == [
            .answer(agent: "L/s", text: "Done."), .copy("make check"), .later(loop: "lp-s"), .open(loop: "lp-s"),
        ])
    }

    @Test func commandsInSteps() {
        #expect(YourMove.command(in: "Run `brew upgrade hesper` then restart") == "brew upgrade hesper")
        #expect(YourMove.command(in: "Then:\n  $ make -C app test-unit\nok") == "make -C app test-unit")
        #expect(YourMove.command(in: "Open the store page") == nil)
        #expect(YourMove.command(in: "empty `` ticks") == nil)
        #expect(YourMove.count(loops: [F.loop("a", next: .you, items: [F.item("i", "x")])]) == 1)
    }
}
