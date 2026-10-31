import Foundation

// The evening wrap-up (⇧⌘E): today's loops, one suggested decision each,
// the day's tallies and how far the person got. A loop is today's when it
// changed since local midnight or still waits on the person. Closed and
// merged loops are not listed (finished ones count in the tallies).

/// What the wrap-up suggests for a loop.
public enum WrapUpDecision: Equatable, Hashable, Sendable {
    /// Mark it done (it looks done).
    case done
    /// The person's next step (the text of their first open item).
    case nextStep(String)
    /// Park it (no change for days).
    case park
    /// Drop it (only failed or exited agents, nothing open).
    case drop
    /// Leave it running (agents or others are on it).
    case keep

    public var title: String {
        switch self {
        case .done: return "Done"
        case .nextStep: return "Next step"
        case .park: return "Park"
        case .drop: return "Drop"
        case .keep: return "Keep"
        }
    }

    /// The loops.save that does it (nil: nothing to save; the next step
    /// opens the loop, keep leaves it). Parking lasts until tomorrow
    /// morning (`parkUntil`).
    public func patch(loop id: String, parkUntil: Date?) -> LoopPatch? {
        switch self {
        case .done: return LoopPatch(id: id, status: .done)
        case .drop: return LoopPatch(id: id, status: .dropped)
        case .park: return LoopPatch(id: id, status: .parked, parkedUntil: parkUntil)
        case .nextStep, .keep: return nil
        }
    }
}

public struct WrapUpEntry: Equatable, Sendable, Identifiable {
    public var id: String { loop.id }
    public var loop: Loop
    public var suggestion: WrapUpDecision
    /// Why, in a few words ("Nothing open and every agent is quiet").
    public var reason: String
}

public struct WrapUpTallies: Equatable, Sendable {
    public var startedToday = 0
    public var finishedToday = 0
    /// Active loops waiting on the person (looks-done ones included).
    public var onYou = 0
    public var parked = 0
}

public struct WrapUp: Equatable, Sendable {
    /// Days without change after which the wrap-up suggests parking.
    public static let parkAfterDays = 3

    public private(set) var entries: [WrapUpEntry] = []
    public private(set) var tallies = WrapUpTallies()
    /// The person's decisions so far (loop id → decision).
    public private(set) var decided: [String: WrapUpDecision] = [:]
    public let startOfDay: Date
    /// When a park from tonight ends: tomorrow, 09:00 local.
    public let parkUntil: Date

    public init(loops: some Sequence<Loop>, now: Date = Date(), calendar: Calendar = .current) {
        startOfDay = calendar.startOfDay(for: now)
        let tomorrow = calendar.date(byAdding: .day, value: 1, to: startOfDay) ?? now
        parkUntil = calendar.date(bySettingHour: 9, minute: 0, second: 0, of: tomorrow) ?? tomorrow
        var list: [WrapUpEntry] = []
        for l in loops where !l.isMerged {
            if let c = l.created, c >= startOfDay { tallies.startedToday += 1 }
            if l.status == .done, let c = l.closed ?? l.updated, c >= startOfDay { tallies.finishedToday += 1 }
            guard l.isActive else { continue }
            if l.status == .parked { tallies.parked += 1 }
            let onYou = l.status != .parked && l.next == .you
            if onYou { tallies.onYou += 1 }
            guard onYou || l.lastChange >= startOfDay else { continue }
            let (s, r) = Self.suggest(l, now: now)
            list.append(WrapUpEntry(loop: l, suggestion: s, reason: r))
        }
        entries = list.sorted { a, b in
            let ra = Self.rank(a.loop), rb = Self.rank(b.loop)
            if ra != rb { return ra < rb }
            if a.loop.lastChange != b.loop.lastChange { return a.loop.lastChange > b.loop.lastChange }
            return a.loop.id < b.loop.id
        }
    }

    /// You first, then looks done, then agents, others, parked.
    static func rank(_ l: Loop) -> Int {
        if l.status == .parked || l.next == .parked { return 4 }
        switch l.next {
        case .you: return l.looksDone ? 1 : 0
        case .agents: return 2
        case .others: return 3
        case .parked, .closed: return 4
        }
    }

    /// One decision per loop: done when it looks done; the next step when
    /// the person has open items; drop for debris (only agents that ended
    /// in error or exited, nothing open, nothing else linked); park when
    /// nothing changed in three days; else keep.
    public static func suggest(_ l: Loop, now: Date) -> (WrapUpDecision, String) {
        if l.looksDone { return (.done, "Nothing open and every agent is quiet") }
        if let first = l.yourItems.first {
            let n = l.yourItems.count
            return (.nextStep(first.text), n == 1 ? "One step is yours" : "\(n) steps are yours")
        }
        if isDebris(l) { return (.drop, "Only failed or exited agents, nothing open") }
        if l.status != .parked, now.timeIntervalSince(l.lastChange) >= Double(parkAfterDays) * 86_400 {
            return (.park, "No change in \(parkAfterDays) days")
        }
        switch l.next {
        case .agents: return (.keep, l.counts.working == 1 ? "An agent is working" : "Agents are working")
        case .others: return (.keep, "Waiting on others")
        case .parked: return (.keep, "Parked")
        case .you: return (.keep, l.nextText ?? "Waiting on you")
        case .closed: return (.keep, "Closed")
        }
    }

    /// Error/exited-only debris: at least one agent, every live agent in
    /// error or exited and every gone one left nothing (no ending, summary
    /// or result), no open items, no files, URLs or other loops linked.
    public static func isDebris(_ l: Loop) -> Bool {
        let agents = l.agentLinks
        guard !agents.isEmpty, l.openItems.isEmpty else { return false }
        guard !l.links.contains(where: { $0.kind == .file || $0.kind == .url || $0.kind == .loop }) else { return false }
        return agents.allSatisfy { k in
            if let s = k.state, k.isLive { return s == .error || s == .exited }
            return k.ending == nil && k.summary == nil && k.result == nil
        }
    }

    // MARK: Progress

    public mutating func decide(_ id: String, _ d: WrapUpDecision) {
        guard entries.contains(where: { $0.id == id }) else { return }
        decided[id] = d
    }

    /// ⏎ on an entry: its suggestion.
    public mutating func accept(_ id: String) {
        guard let e = entries.first(where: { $0.id == id }) else { return }
        decided[id] = e.suggestion
    }

    public mutating func undo(_ id: String) { decided.removeValue(forKey: id) }

    /// Decided of total, 0…1 (1 with nothing to decide).
    public var progress: Double { entries.isEmpty ? 1 : Double(decidedCount) / Double(entries.count) }
    public var decidedCount: Int { entries.filter { decided[$0.id] != nil }.count }
    public var isFinished: Bool { decidedCount == entries.count }
    /// The first entry still undecided.
    public var nextUndecided: WrapUpEntry? { entries.first { decided[$0.id] == nil } }

    /// The saves the decisions so far make.
    public var patches: [LoopPatch] {
        entries.compactMap { e in decided[e.id].flatMap { $0.patch(loop: e.id, parkUntil: parkUntil) } }
    }

    /// The wrap-up is due: the evening time has passed today.
    public static func isDue(now: Date, settings: LoopSettings, calendar: Calendar = .current) -> Bool {
        now >= settings.evening(on: now, calendar: calendar)
    }
}
