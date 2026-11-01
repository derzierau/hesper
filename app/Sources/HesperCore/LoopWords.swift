import Foundation

// The words the loops sheets show: ages, owners, relations, a member's
// line and a loop's timeline. Pure, so the sheets only lay them out.

public enum LoopWords {
    /// "4m", "3h", "2d": how long since the loop last changed (or, on the
    /// person, since it waits on them).
    public static func age(_ l: Loop, now: Date = Date()) -> String? {
        let since = l.next == .you && !l.looksDone ? l.waitingSince : l.lastChange
        guard since > .distantPast else { return nil }
        return short(now.timeIntervalSince(since))
    }

    public static func short(_ seconds: TimeInterval) -> String {
        let s = max(0, Int(seconds))
        if s < 60 { return "now" }
        if s < 3600 { return "\(s / 60)m" }
        if s < 86_400 { return "\(s / 3600)h" }
        return "\(s / 86_400)d"
    }

    /// A member's short id: its local agent id ("a7f3k2").
    public static func shortID(_ k: LoopLink) -> String {
        let id = k.agent ?? k.ref
        return String(id.split(separator: "/").last ?? Substring(id))
    }

    /// "Your move", "Looks done", "Agents on it", "Parked until Tue 09:00".
    public static func nextLabel(_ l: Loop, calendar: Calendar = .current) -> String {
        if l.status == .parked {
            guard let until = l.parkedUntil else { return "Parked" }
            let f = DateFormatter()
            f.calendar = calendar
            f.timeZone = calendar.timeZone
            f.setLocalizedDateFormatFromTemplate("EEE HH:mm")
            return "Parked until \(f.string(from: until))"
        }
        if l.next == .you && l.looksDone { return "Looks done" }
        return LoopNextText.label(l.next)
    }

    /// The square for who acts next: question for the person (signal stays
    /// for approvals), working for agents, idle for others and parked,
    /// done for looks done.
    public static func mark(_ l: Loop) -> StateMarkKind {
        if l.status == .parked { return .idle }
        switch l.next {
        case .you: return l.looksDone ? .done : .question
        case .agents: return .working
        case .others, .parked: return .idle
        case .closed: return .done
        }
    }

    public static func owner(_ o: ItemOwner) -> String {
        switch o {
        case .you: return "You"
        case .agent: return "Agent"
        case .others: return "Others"
        }
    }

    public static func rel(_ r: LinkRel) -> String {
        switch r {
        case .member: return "member"
        case .started: return "started"
        case .continued: return "continued"
        case .handoff: return "handoff"
        case .blocks: return "blocks"
        case .blockedBy: return "blocked by"
        case .touches: return "touches"
        }
    }

    /// An SF Symbol per link kind.
    public static func symbol(_ k: LinkKind) -> String {
        switch k {
        case .agent: return "terminal"
        case .session: return "clock"
        case .project: return "folder"
        case .url: return "link"
        case .file: return "doc"
        case .loop: return "arrow.triangle.2.circlepath"
        case .other: return "questionmark.square"
        }
    }

    /// A member's line: what it ended with, its summary, or why it closed.
    public static func memberDetail(_ k: LoopLink) -> String? {
        var parts: [String] = []
        if let e = k.ending { parts.append(ending(e)) }
        if let s = k.summary, !s.isEmpty { parts.append(s) }
        if !k.isLive, let r = k.closedReason, !r.isEmpty { parts.append("closed: \(r)") }
        return parts.isEmpty ? nil : parts.joined(separator: " · ")
    }

    public static func ending(_ e: LoopEnding) -> String {
        switch e {
        case .asks: return "asks you"
        case .yourStep: return "named a step for you"
        case .offer: return "offers more"
        case .waits: return "waits"
        case .done: return "done"
        }
    }

    public struct Event: Equatable, Sendable {
        public var at: Date
        public var when: String
        public var text: String
    }

    /// The loop's history, newest first: links as they were added (an
    /// agent started, a handoff, a block), items done, members closed.
    public static func timeline(_ l: Loop, now: Date = Date(), limit: Int = 12) -> [Event] {
        var out: [(Date, String)] = []
        if let c = l.created { out.append((c, "loop opened")) }
        for k in l.links {
            if let a = k.added {
                let what: String
                switch (k.kind, k.rel) {
                case (.agent, .started): what = "started \(k.title)"
                case (.agent, .continued): what = "\(k.title) continued the work"
                case (.agent, _): what = "\(k.title) joined"
                case (.file, .handoff): what = "handed off \(k.title)"
                case (.loop, .blockedBy): what = "blocked by another loop"
                case (.loop, .blocks): what = "blocks another loop"
                default: what = "linked \(k.title)"
                }
                out.append((a, what))
            }
            if let c = k.closed { out.append((c, "\(k.title) closed" + (k.closedReason.map { " (\($0))" } ?? ""))) }
        }
        for it in l.items { if let d = it.done, it.state == .done { out.append((d, "done: \(it.text)")) } }
        if let c = l.closed { out.append((c, "loop \(l.status.rawValue)")) }
        return out.sorted { $0.0 > $1.0 }.prefix(limit).map { Event(at: $0.0, when: short(now.timeIntervalSince($0.0)), text: $0.1) }
    }
}
