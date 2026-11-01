import Foundation

// The ⌘J panel's list once loops exist: the attention queue (approval,
// question, error) as before, then "Your move" (YourMove.entries). One
// selection runs through both sections; the panel is sized from the rows
// before SwiftUI lays it out (placement), so the height is computed here.
// The menu bar and notifications keep counting the queue alone.

public struct InboxList: Equatable, Sendable {
    /// A selectable row: a queue agent or a "Your move" loop.
    public enum Item: Equatable, Hashable, Sendable {
        case agent(String)
        case loop(String)
    }

    public static let queueTitle = "Needs you"
    public static let yourMoveTitle = "Your move"

    /// The queue's agent ids (filtered), in AttentionQueue order.
    public var queue: [String]
    /// The "Your move" entries (filtered), newest first.
    public var yourMove: [YourMoveEntry]

    public init(queue: [String], yourMove: [YourMoveEntry]) {
        self.queue = queue
        self.yourMove = yourMove
    }

    /// Every row in order: the queue, then "Your move".
    public var items: [Item] { queue.map(Item.agent) + yourMove.map { .loop($0.loop.id) } }
    public var count: Int { queue.count + yourMove.count }
    public var isEmpty: Bool { count == 0 }

    /// The panel's title: "Inbox · 3".
    public var title: String { "Inbox · \(count)" }

    /// Section headers are drawn only when "Your move" has entries (a
    /// queue alone looks as it always did): "Needs you" over a non-empty
    /// queue, "Your move" over its entries.
    public var headers: Int { yourMove.isEmpty ? 0 : (queue.isEmpty ? 1 : 2) }
    public var showsQueueHeader: Bool { !yourMove.isEmpty && !queue.isEmpty }

    public func item(at i: Int?) -> Item? {
        guard let i, i >= 0, i < count else { return nil }
        return i < queue.count ? .agent(queue[i]) : .loop(yourMove[i - queue.count].loop.id)
    }

    public func index(of item: Item) -> Int? { items.firstIndex(of: item) }

    public struct Metrics: Equatable, Sendable {
        public var item: Double
        public var header: Double
        public var spacing: Double
        public var padding: Double
        public var empty: Double

        public init(item: Double, header: Double, spacing: Double, padding: Double, empty: Double) {
            self.item = item; self.header = header; self.spacing = spacing; self.padding = padding; self.empty = empty
        }
    }

    /// The list's height: every row (queue and "Your move" rows are the
    /// same height: a Row with a subtitle and a line of buttons), the
    /// headers, the gaps between them and the padding; `empty` when there
    /// are none.
    public func height(_ m: Metrics) -> Double {
        let rows = count + headers
        guard count > 0 else { return m.empty }
        return Double(count) * m.item + Double(headers) * m.header + Double(rows - 1) * m.spacing + 2 * m.padding
    }

    /// The filter on "Your move": every word in the loop's title, the
    /// entry's text or who it comes from.
    public static func filter(_ entries: [YourMoveEntry], query: String) -> [YourMoveEntry] {
        let words = query.lowercased().split(whereSeparator: \.isWhitespace)
        guard !words.isEmpty else { return entries }
        return entries.filter { e in
            let fields = [e.loop.displayTitle, e.text, e.from ?? ""].map { $0.lowercased() }
            return words.allSatisfy { w in fields.contains { $0.contains(w) } }
        }
    }
}

/// What a "Your move" quick action does, as hesperd calls (the app runs
/// them in order and stops at the first error).
public enum YourMoveCall: Equatable, Sendable {
    /// agents.input with submit: types the answer into the agent, the
    /// way the inbox answers a free-text question.
    case input(agent: String, text: String)
    /// loops.item on the loop (no id: adds one).
    case item(loop: String, ItemPatch)
    case save(LoopPatch)
    /// The clipboard, no call.
    case copy(String)
    /// The loop's detail (the Loops sheet), no call.
    case open(loop: String)

    /// Later files the request in its loop when it is still only an
    /// agent's ending (so it is not lost when the agent moves on), then
    /// parks the loop until `parkUntil` (tomorrow morning): parked loops
    /// leave "Your move".
    public static func calls(_ intent: YourMoveIntent, entry: YourMoveEntry, parkUntil: Date) -> [YourMoveCall] {
        switch intent {
        case .answer(let a, let text): return [.input(agent: a, text: text)]
        case .copy(let c): return [.copy(c)]
        case .open(let l): return [.open(loop: l)]
        case .checkOff(let l, let item): return [.item(loop: l, .done(item))]
        case .later(let l):
            var out: [YourMoveCall] = []
            switch entry.reason {
            // With its source: YourMove then shows the item, not the ask again.
            case .asks(let k): out.append(.item(loop: l, ItemPatch(text: entry.text, owner: .you, kind: .question, source: k.ref)))
            case .yourStep(let k): out.append(.item(loop: l, ItemPatch(text: entry.text, owner: .you, kind: .step, source: k.ref)))
            case .item: break
            }
            out.append(.save(LoopPatch(id: l, status: .parked, parkedUntil: parkUntil)))
            return out
        }
    }

    /// When "Later" ends: tomorrow, 09:00 local (the wrap-up's park).
    public static func parkUntil(now: Date = Date(), calendar: Calendar = .current) -> Date {
        WrapUp(loops: [Loop](), now: now, calendar: calendar).parkUntil
    }
}
