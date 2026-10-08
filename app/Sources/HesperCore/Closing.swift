import Foundation

// Closing agents: an agent is on a wall, in the background (running, off
// every wall) or closed (gone from the agents, its session in History and
// resumable). ⌘W closes, ⌥⌘W sends to the background, ⌃⌘W kills (the pane
// stays, "Killed"), ⇧⌘W tidies up finished agents, ⌘⇧T reopens the last
// closed one. The pure rules live here; AppModel+Closing does the calls.

// MARK: Wire codes

public enum CloseRPC {
    /// JSON-RPC "no method": an older hesperd (fall back).
    public static let noMethod = -32601
    /// The agent's Mac is offline: the close is queued in the app.
    public static let machineOffline = -32010
}

// MARK: The close decision

/// What closing an agent asks first, inline on its tile (or the focus view).
public enum CloseConfirm: Equatable, Sendable {
    /// Approval or question pending: closing would drop the decision.
    case needsYou
    /// A shell whose foreground command still runs ("npm run dev").
    case foreground(String)
    /// Its own worktree has uncommitted changes (files).
    case worktree(files: Int)

    public var kind: CloseAcks {
        switch self {
        case .needsYou: return .needsYou
        case .foreground: return .foreground
        case .worktree: return .worktree
        }
    }

    /// The strip's text.
    public var message: String {
        switch self {
        case .needsYou: return "It's waiting for you"
        case .foreground(let cmd): return "\(cmd) is still running"
        case .worktree(let n): return "\(n) file\(n == 1 ? "" : "s") changed in its worktree"
        }
    }

    /// ⏎: continue closing ("Close anyway"; the worktree: keep it).
    public var primary: String {
        switch self {
        case .needsYou, .foreground: return "Close anyway"
        case .worktree: return "Keep worktree"
        }
    }

    /// The second choice (the worktree: discard it); Esc always cancels.
    public var secondary: String? {
        if case .worktree = self { return "Discard" }
        return nil
    }
}

/// Confirms already given for one close (each is asked once).
public struct CloseAcks: OptionSet, Sendable, Hashable {
    public let rawValue: Int
    public init(rawValue: Int) { self.rawValue = rawValue }
    public static let needsYou = CloseAcks(rawValue: 1)
    public static let foreground = CloseAcks(rawValue: 2)
    public static let worktree = CloseAcks(rawValue: 4)
}

/// The next step of a close.
public enum CloseStep: Equatable, Sendable {
    /// ⌘W on a draft: discard it (undo brings it back), as before.
    case discardDraft
    /// Ask on the tile first.
    case confirm(CloseConfirm)
    /// Look at its worktree (git status, async), then decide again.
    case checkWorktree
    /// Close it now.
    case close
    /// Nothing selected.
    case nothing
}

public enum CloseRules {
    /// Finished: what tidy-up and auto-tidy close (never working or needs-you).
    public static let finishedStates: Set<AgentState> = [.done, .idle, .exited]

    public static func isFinished(_ a: Agent) -> Bool { finishedStates.contains(a.state) }

    /// A shell's running foreground command, when hesperd knows it (its
    /// activity while working); nil otherwise.
    public static func foregroundCommand(_ a: Agent) -> String? {
        guard a.kind == "shell", a.isRunning, a.state == .working || a.state == .starting,
              let cmd = a.activity?.trimmingCharacters(in: .whitespacesAndNewlines), !cmd.isEmpty else { return nil }
        return cmd
    }

    /// Whether closing it asks because it waits for an answer.
    public static func waitsForYou(_ a: Agent) -> Bool { a.state == .approval || a.state == .question }

    /// - Parameters:
    ///   - worktreeCheckable: it has its own worktree the app can look at
    ///     (this Mac's agent).
    ///   - worktreeChanges: the files changed there; nil: not looked yet.
    public static func decide(isDraft: Bool, agent: Agent?, acks: CloseAcks = [], worktreeCheckable: Bool = false,
                              worktreeChanges: Int? = nil) -> CloseStep {
        if isDraft { return .discardDraft }
        guard let a = agent else { return .nothing }
        if !acks.contains(.needsYou), waitsForYou(a) { return .confirm(.needsYou) }
        if !acks.contains(.foreground), let cmd = foregroundCommand(a) { return .confirm(.foreground(cmd)) }
        if worktreeCheckable, !acks.contains(.worktree) {
            guard let n = worktreeChanges else { return .checkWorktree }
            if n > 0 { return .confirm(.worktree(files: n)) }
        }
        return .close
    }
}

// MARK: Tidy up and auto-tidy

public enum TidyUp {
    /// ⇧⌘W: the finished agents (done, idle, exited) among `agents` (the
    /// wall's, in order), only `band`'s when a band is focused; never
    /// working or needing you.
    public static func select(_ agents: [Agent], band: String?, bandOf: (Agent) -> String?) -> [Agent] {
        agents.filter { CloseRules.isFinished($0) && (band == nil || bandOf($0) == band) }
    }
}

/// Settings › Agents › "Close finished agents after 30 min" (off by
/// default): a finished agent closes itself once it has been finished that
/// long. Killed agents stay (their pane is there to be read).
public enum AutoTidy {
    public static let after: TimeInterval = 30 * 60

    static func eligible(_ a: Agent) -> Bool { CloseRules.isFinished(a) && !a.isKilled && a.stateSince != nil }

    public static func due(_ agents: [Agent], now: Date, after: TimeInterval = after, exclude: Set<String> = []) -> [Agent] {
        agents.filter { a in
            guard eligible(a), !exclude.contains(a.id), let since = a.stateSince else { return false }
            return now.timeIntervalSince(since) >= after
        }
    }

    /// When the next one becomes due (the timer's fire date); nil: none.
    public static func nextDue(_ agents: [Agent], now: Date, after: TimeInterval = after, exclude: Set<String> = []) -> Date? {
        agents.filter { eligible($0) && !exclude.contains($0.id) }
            .compactMap { $0.stateSince?.addingTimeInterval(after) }
            .map { max($0, now) }
            .min()
    }
}

// MARK: The reopen stack (⌘⇧T)

/// A closed agent, enough to bring it back (sessions.resume on its Mac).
public struct ClosedAgent: Equatable, Sendable {
    public var agentID: String
    public var name: String
    /// The History session id ("<machine>:<kind>:<tool session>"); nil:
    /// nothing to resume (a shell).
    public var session: String?
    public var machine: String
    /// The band (project) it sat in.
    public var band: String?
    public var closedAt: Date

    public init(agentID: String, name: String, session: String?, machine: String, band: String? = nil, closedAt: Date = Date()) {
        self.agentID = agentID; self.name = name; self.session = session; self.machine = machine; self.band = band; self.closedAt = closedAt
    }

    /// hesperd's History id for an agent's session, when it has one.
    public static func sessionID(for a: Agent) -> String? {
        guard let s = a.sessionId, !s.isEmpty else { return nil }
        return "\(a.machine):\(a.kind):\(s)"
    }

    public init(_ a: Agent, session: String? = nil, band: String? = nil, closedAt: Date = Date()) {
        self.init(agentID: a.id, name: a.name, session: session ?? Self.sessionID(for: a), machine: a.machine, band: band, closedAt: closedAt)
    }

    var key: String { session ?? agentID }
}

/// The last `capacity` closes, newest on top; one entry per session.
public struct ReopenStack: Equatable, Sendable {
    public static let capacity = 10
    public private(set) var items: [ClosedAgent] = []
    public init() {}

    public mutating func push(_ c: ClosedAgent) {
        items.removeAll { $0.key == c.key || $0.agentID == c.agentID }
        items.append(c)
        if items.count > Self.capacity { items.removeFirst(items.count - Self.capacity) }
    }

    public mutating func pop() -> ClosedAgent? { items.popLast() }
    public var top: ClosedAgent? { items.last }
    public var isEmpty: Bool { items.isEmpty }

    /// hesperd named the session after the close.
    public mutating func update(agentID: String, session: String) {
        for i in items.indices where items[i].agentID == agentID { items[i].session = session }
    }

    /// It came back another way (undo, History): not reopened twice.
    public mutating func remove(agentID: String? = nil, session: String? = nil) {
        items.removeAll { (agentID != nil && $0.agentID == agentID) || (session != nil && $0.session == session) }
    }
}

// MARK: Background

public struct BackgroundSummary: Equatable, Sendable {
    public var count = 0
    public var needsYou = 0
    public init(count: Int = 0, needsYou: Int = 0) { self.count = count; self.needsYou = needsYou }
    /// The tray pill shows at all.
    public var visible: Bool { count > 0 }
    public var title: String { "Background · \(count)" }
}

public enum BackgroundTray {
    /// The background agents, needing you first (⌘J order), then the
    /// longest running first.
    public static func agents<S: Sequence>(_ all: S, isBackground: (Agent) -> Bool) -> [Agent] where S.Element == Agent {
        let bg = all.filter(isBackground)
        let needs = AttentionQueue.ordered(bg)
        let ids = Set(needs.map(\.id))
        let rest = bg.filter { !ids.contains($0.id) }.sorted { l, r in
            let a = l.created ?? .distantPast, b = r.created ?? .distantPast
            return a != b ? a < b : l.id < r.id
        }
        return needs + rest
    }

    public static func summary<S: Sequence>(_ all: S, isBackground: (Agent) -> Bool) -> BackgroundSummary where S.Element == Agent {
        var s = BackgroundSummary()
        for a in all where isBackground(a) {
            s.count += 1
            if CloseRules.waitsForYou(a) || a.state == .error { s.needsYou += 1 }
        }
        return s
    }

    /// The row's meta: "mini · codex · 12m" (running for).
    public static func meta(_ a: Agent, machine: String, now: Date = Date()) -> String {
        [machine, a.kind, age(since: a.created ?? a.stateSince, now: now)].compactMap { $0 }.joined(separator: " · ")
    }

    public static func age(since d: Date?, now: Date = Date()) -> String? {
        guard let d else { return nil }
        let s = max(0, Int(now.timeIntervalSince(d)))
        if s < 60 { return "\(s)s" }
        if s < 3600 { return "\(s / 60)m" }
        if s < 86400 { return "\(s / 3600)h" }
        return "\(s / 86400)d"
    }

    /// A background agent just finished (the app closes it itself when
    /// hesperd can't): it entered done / idle / exited.
    public static func justFinished(_ a: Agent, from old: AgentState?) -> Bool {
        CloseRules.isFinished(a) && (old.map { !CloseRules.finishedStates.contains($0) } ?? false)
    }
}

// MARK: Text

/// Why hesperd removed an agent (`agents.removed {reason}`).
public enum RemovalReason: String, Sendable {
    case closed
    case finishedInBackground = "finished-in-background"
    case removed
}

public enum CloseText {
    /// The notification for a removal; nil: none.
    public static func notification(_ reason: RemovalReason?, name: String) -> String? {
        reason == .finishedInBackground ? "\(name) finished in the background" : nil
    }

    /// The close toast's label (its keys come with it: ⌘Z undo, ⌘⇧T reopen).
    public static func closed(_ name: String, queuedFor machine: String? = nil) -> String {
        machine.map { "Closed \(name) · will stop when \($0) is back" } ?? "Closed \(name)"
    }

    public static func tidied(_ names: [String]) -> String {
        names.count == 1 ? "Closed \(names[0])" : "Closed \(names.count) finished agents"
    }

    public static let needsUpdate = "this Mac's hesperd needs an update"
    public static let killed = "Killed"
}

extension Agent {
    /// Ended by ⌃⌘W (`ended: "killed"`): the pane stays, "Killed".
    public var isKilled: Bool { ended == "killed" }
}
