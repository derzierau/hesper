import Foundation

// The inbox (⌘J) with loops: first the attention queue exactly as before
// (approval → question → error, oldest first: AttentionQueue), then "Your
// move": one entry per loop that waits on the person for something the
// queue does not show: an open item of theirs, or a quiet agent whose turn
// ended asking them something or naming their step. Newest first. Offers
// never come here (decision 2): they are listed dimmed inside the loop
// (`YourMove.offers`). Each entry carries its quick actions as pure
// intents; the app turns them into calls.

/// A quick action of a "Your move" entry.
public enum YourMoveIntent: Equatable, Hashable, Sendable {
    /// Type this answer to the agent and submit it (agents.input).
    case answer(agent: String, text: String)
    /// Not now: park the loop until tomorrow morning.
    case later(loop: String)
    /// Copy this command to the clipboard.
    case copy(String)
    /// Tick the item off (loops.item state done).
    case checkOff(loop: String, item: String)
    /// Open the loop (its detail in the Loops sheet).
    case open(loop: String)
}

public struct YourMoveAction: Equatable, Sendable, Identifiable {
    public var id: String { title }
    /// Yes, No, Later, Copy command, Did it, Open.
    public var title: String
    public var intent: YourMoveIntent
}

public struct YourMoveEntry: Equatable, Sendable, Identifiable {
    public enum Reason: Equatable, Sendable {
        /// An open item of the person's.
        case item(LoopItem)
        /// A quiet agent's turn ended asking the person (`asks`).
        case asks(LoopLink)
        /// A quiet agent's turn ended naming the person's step.
        case yourStep(LoopLink)
    }

    public var id: String { loop.id }
    public var loop: Loop
    public var reason: Reason
    /// What it asks: the item's text, the agent's summary.
    public var text: String
    /// Who it comes from (the agent's name; nil for the person's own item).
    public var from: String?
    public var at: Date
    /// The quick actions; the first is the primary (⏎).
    public var actions: [YourMoveAction]
}

public enum InboxSection: Equatable, Sendable {
    /// The attention queue, unchanged (AttentionQueue.ordered).
    case queue([Agent])
    case yourMove([YourMoveEntry])

    public var title: String {
        switch self {
        case .queue: return "Needs you"
        case .yourMove: return "Your move"
        }
    }

    public var isEmpty: Bool {
        switch self {
        case .queue(let a): return a.isEmpty
        case .yourMove(let e): return e.isEmpty
        }
    }
}

public enum YourMove {
    /// The inbox's sections, empty ones left out.
    public static func sections(agents: [Agent], loops: some Sequence<Loop>) -> [InboxSection] {
        let queue = AttentionQueue.ordered(agents)
        let inQueue = Set(queue.map(\.id))
        return [InboxSection.queue(queue), .yourMove(entries(loops: loops, excluding: inQueue))].filter { !$0.isEmpty }
    }

    /// One entry per active loop that waits on the person, newest first.
    /// An agent in the queue (`excluding`) is not repeated here. An ask
    /// beats a named step, which beats the person's own first item.
    public static func entries(loops: some Sequence<Loop>, excluding: Set<String> = []) -> [YourMoveEntry] {
        var out: [YourMoveEntry] = []
        for l in loops where l.isActive && l.status != .parked {
            if let e = entry(l, excluding: excluding) { out.append(e) }
        }
        return out.sorted { $0.at != $1.at ? $0.at > $1.at : $0.loop.id < $1.loop.id }
    }

    static func entry(_ l: Loop, excluding: Set<String>) -> YourMoveEntry? {
        func quiet(_ k: LoopLink) -> Bool { k.isQuiet && !(k.agent.map(excluding.contains) ?? false) }
        let byNewest = l.agentLinks.sorted { ($0.added ?? .distantPast) > ($1.added ?? .distantPast) }
        // An item that came from the asking agent is the same request:
        // prefer the item (it can be ticked off).
        let items = l.yourItems
        if let k = byNewest.first(where: { $0.ending == .asks && quiet($0) }), !items.contains(where: { $0.source == k.ref }) {
            let text = k.summary ?? l.nextText ?? "\(k.title) asks you"
            return YourMoveEntry(loop: l, reason: .asks(k), text: text, from: k.title, at: l.lastChange, actions: actions(asks: k, loop: l))
        }
        if let k = byNewest.first(where: { $0.ending == .yourStep && quiet($0) }), !items.contains(where: { $0.source == k.ref }) {
            let text = k.summary ?? l.nextText ?? "A step for you"
            return YourMoveEntry(loop: l, reason: .yourStep(k), text: text, from: k.title, at: l.lastChange, actions: actions(step: text, item: nil, agent: k, loop: l))
        }
        if let it = items.first {
            let src = it.source.flatMap { s in l.links.first { $0.ref == s } }
            return YourMoveEntry(loop: l, reason: .item(it), text: it.text, from: src?.title, at: it.created ?? l.lastChange,
                                 actions: actions(step: it.text, item: it, agent: src, loop: l))
        }
        return nil
    }

    /// An ask: Yes / No when the agent is live, Later, Open.
    static func actions(asks k: LoopLink, loop l: Loop) -> [YourMoveAction] {
        var out: [YourMoveAction] = []
        if let a = k.agent, k.isLive {
            out.append(YourMoveAction(title: "Yes", intent: .answer(agent: a, text: "Yes")))
            out.append(YourMoveAction(title: "No", intent: .answer(agent: a, text: "No")))
        }
        return finish(out, loop: l)
    }

    /// A step: Did it (ticks the item off, or tells the live agent), Copy
    /// command when the step names one, Later, Open.
    static func actions(step text: String, item: LoopItem?, agent k: LoopLink?, loop l: Loop) -> [YourMoveAction] {
        var out: [YourMoveAction] = []
        if let item {
            out.append(YourMoveAction(title: "Did it", intent: .checkOff(loop: l.id, item: item.id)))
        } else if let k, let a = k.agent, k.isLive {
            out.append(YourMoveAction(title: "Did it", intent: .answer(agent: a, text: "Done.")))
        }
        let open = out.isEmpty
        if open { out.append(YourMoveAction(title: "Open", intent: .open(loop: l.id))) }
        if let cmd = command(in: text) { out.append(YourMoveAction(title: "Copy command", intent: .copy(cmd))) }
        out.append(YourMoveAction(title: "Later", intent: .later(loop: l.id)))
        if !open { out.append(YourMoveAction(title: "Open", intent: .open(loop: l.id))) }
        return out
    }

    /// Later and Open after the answers. With no answer (the agent is
    /// gone) Open comes first: ⏎ never parks a loop.
    private static func finish(_ answers: [YourMoveAction], loop l: Loop) -> [YourMoveAction] {
        let later = YourMoveAction(title: "Later", intent: .later(loop: l.id)), open = YourMoveAction(title: "Open", intent: .open(loop: l.id))
        return answers.isEmpty ? [open, later] : answers + [later, open]
    }

    /// The command a step names: the first `code span`, else a line
    /// starting with "$ "; nil when there is none.
    public static func command(in text: String) -> String? {
        if let open = text.firstIndex(of: "`") {
            let rest = text[text.index(after: open)...]
            if let close = rest.firstIndex(of: "`") {
                let c = rest[..<close].trimmingCharacters(in: .whitespaces)
                if !c.isEmpty { return c }
            }
        }
        for line in text.split(separator: "\n") {
            let t = line.trimmingCharacters(in: .whitespaces)
            if t.hasPrefix("$ ") {
                let c = t.dropFirst(2).trimmingCharacters(in: .whitespaces)
                if !c.isEmpty { return c }
            }
        }
        return nil
    }

    /// A loop's open offers and the agents whose turn ended offering more:
    /// shown dimmed in the loop, never in the inbox.
    public static func offers(in l: Loop) -> (items: [LoopItem], agents: [LoopLink]) {
        (l.offers, l.agentLinks.filter { $0.ending == .offer })
    }

    /// The "Your move" count (the section's entries).
    public static func count(loops: some Sequence<Loop>, excluding: Set<String> = []) -> Int {
        entries(loops: loops, excluding: excluding).count
    }
}
