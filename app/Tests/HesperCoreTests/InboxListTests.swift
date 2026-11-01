import Foundation
import Testing
@testable import HesperCore

struct InboxListTests {
    typealias F = LoopFixtures

    let ask = F.loop("lp-ask", "Push provider", next: .you,
                     links: [F.agent("n/x", label: "api", agent: "L/x", state: .done, ending: .asks, summary: "Use the new key?")], updated: F.date(5))
    let step = F.loop("lp-step", "Deploy", next: .you,
                      links: [F.agent("n/s", label: "ops", agent: "L/s", state: .idle, ending: .yourStep, summary: "Run `make deploy`")], updated: F.date(4))
    let item = F.loop("lp-item", "Docs", next: .you, items: [F.item("i1", "Merge the PR")], updated: F.date(3))

    var entries: [YourMoveEntry] { YourMove.entries(loops: [ask, step, item]) }

    @Test func rowsRunThroughBothSections() {
        let l = InboxList(queue: ["L/a", "L/q"], yourMove: entries)
        #expect(l.items == [.agent("L/a"), .agent("L/q"), .loop("lp-ask"), .loop("lp-step"), .loop("lp-item")])
        #expect(l.count == 5 && l.title == "Inbox · 5")
        #expect(l.item(at: 1) == .agent("L/q") && l.item(at: 2) == .loop("lp-ask") && l.item(at: 5) == nil && l.item(at: nil) == nil)
        #expect(l.index(of: .loop("lp-item")) == 4)
        #expect(l.headers == 2 && l.showsQueueHeader)
        // "Your move" alone: one header; the queue alone: none (as before).
        #expect(InboxList(queue: [], yourMove: entries).headers == 1)
        let q = InboxList(queue: ["L/a"], yourMove: [])
        #expect(q.headers == 0 && !q.showsQueueHeader && q.title == "Inbox · 1")
        #expect(InboxList(queue: [], yourMove: []).isEmpty)
    }

    @Test func heightCountsRowsHeadersAndGaps() {
        let m = InboxList.Metrics(item: 70, header: 20, spacing: 2, padding: 8, empty: 56)
        #expect(InboxList(queue: [], yourMove: []).height(m) == 56)
        // Two queue rows: 2 × 70 + 1 gap + padding (unchanged from the flat list).
        #expect(InboxList(queue: ["a", "b"], yourMove: []).height(m) == 158) // 140 + 2 + 16
        // Two queue rows, three entries, two headers: 7 rows, 6 gaps.
        #expect(InboxList(queue: ["a", "b"], yourMove: entries).height(m) == 418) // 5 × 70 + 2 × 20 + 6 × 2 + 16
    }

    @Test func filterMatchesTitleTextAndSender() {
        #expect(InboxList.filter(entries, query: "").count == 3)
        #expect(InboxList.filter(entries, query: "push").map(\.id) == ["lp-ask"])   // loop title
        #expect(InboxList.filter(entries, query: "make DEPLOY").map(\.id) == ["lp-step"]) // text, any case
        #expect(InboxList.filter(entries, query: "api key").map(\.id) == ["lp-ask"]) // sender and text
        #expect(InboxList.filter(entries, query: "nothing").isEmpty)
    }

    @Test func intentsBecomeCalls() {
        let e = entries
        let until = F.date(24)
        #expect(YourMoveCall.calls(.answer(agent: "L/x", text: "Yes"), entry: e[0], parkUntil: until) == [.input(agent: "L/x", text: "Yes")])
        #expect(YourMoveCall.calls(.copy("make deploy"), entry: e[1], parkUntil: until) == [.copy("make deploy")])
        #expect(YourMoveCall.calls(.checkOff(loop: "lp-item", item: "i1"), entry: e[2], parkUntil: until) == [.item(loop: "lp-item", .done("i1"))])
        #expect(YourMoveCall.calls(.open(loop: "lp-ask"), entry: e[0], parkUntil: until) == [.open(loop: "lp-ask")])
        // Later files an agent's ask or step as the person's item, then parks.
        let park = LoopPatch(id: "lp-ask", status: .parked, parkedUntil: until)
        #expect(YourMoveCall.calls(.later(loop: "lp-ask"), entry: e[0], parkUntil: until) == [
            .item(loop: "lp-ask", ItemPatch(text: "Use the new key?", owner: .you, kind: .question)), .save(park),
        ])
        #expect(YourMoveCall.calls(.later(loop: "lp-step"), entry: e[1], parkUntil: until) == [
            .item(loop: "lp-step", ItemPatch(text: "Run `make deploy`", owner: .you, kind: .step)),
            .save(LoopPatch(id: "lp-step", status: .parked, parkedUntil: until)),
        ])
        // An item is already in the loop: only the park.
        #expect(YourMoveCall.calls(.later(loop: "lp-item"), entry: e[2], parkUntil: until) == [
            .save(LoopPatch(id: "lp-item", status: .parked, parkedUntil: until)),
        ])
    }

    @Test func laterEndsTomorrowMorning() throws {
        var cal = Calendar(identifier: .gregorian)
        cal.timeZone = try #require(TimeZone(identifier: "UTC"))
        let evening = try #require(cal.date(from: DateComponents(year: 2026, month: 10, day: 31, hour: 19)))
        let morning = try #require(cal.date(from: DateComponents(year: 2026, month: 11, day: 1, hour: 9)))
        #expect(YourMoveCall.parkUntil(now: evening, calendar: cal) == morning)
    }
}
