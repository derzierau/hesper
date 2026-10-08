import Foundation

// Moving work across Macs: "Continue on mini" checkpoints an agent's
// worktree here, carries it (git bundle) and its conversation to the other
// Mac, resumes it there and closes it here; "Fork on mini" keeps it here
// too. hesperd does the work (agents.move, agents.checkpoint, the
// agents.moving progress); the pure rules the app needs live here,
// AppModel+Move does the calls.

// MARK: Checkpoints

/// An agent's worktree saved as a hidden ref (refs/hesper/checkpoints/…):
/// commits plus uncommitted and untracked files, never ignored ones.
/// hesperd takes one when an agent finishes a turn, closes or moves.
public struct Checkpoint: Codable, Equatable, Sendable, Hashable {
    public var ref: String
    public var commit: String
    public var at: Date?
    /// Files changed against the branch (uncommitted + untracked).
    public var changed: Int
    public var branch: String?

    public init(ref: String, commit: String = "", at: Date? = nil, changed: Int = 0, branch: String? = nil) {
        self.ref = ref; self.commit = commit; self.at = at; self.changed = changed; self.branch = branch
    }

    enum CodingKeys: String, CodingKey { case ref, commit, at, changed, branch }

    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        ref = (try? c.decodeIfPresent(String.self, forKey: .ref)) ?? ""
        commit = (try? c.decodeIfPresent(String.self, forKey: .commit)) ?? ""
        at = Agent.date((try? c.decodeIfPresent(String.self, forKey: .at)) ?? nil)
        changed = (try? c.decodeIfPresent(Int.self, forKey: .changed)) ?? 0
        branch = ((try? c.decodeIfPresent(String.self, forKey: .branch)) ?? nil).flatMap { $0.isEmpty ? nil : $0 }
    }

    public func encode(to encoder: any Encoder) throws {
        var c = encoder.container(keyedBy: CodingKeys.self)
        try c.encode(ref, forKey: .ref)
        try c.encode(commit, forKey: .commit)
        try c.encodeIfPresent(at.map(Agent.format), forKey: .at)
        try c.encode(changed, forKey: .changed)
        try c.encodeIfPresent(branch, forKey: .branch)
    }

    /// From a JSON object (sessions are decoded by hand); nil when it has
    /// neither ref nor commit.
    public init?(json v: JSONValue?) {
        guard let v, case .object = v, let cp = try? v.decode(Checkpoint.self), !(cp.ref.isEmpty && cp.commit.isEmpty) else { return nil }
        self = cp
    }

    public var json: JSONValue {
        var o: [String: JSONValue] = ["ref": .string(ref), "commit": .string(commit), "changed": .number(Double(changed))]
        if let at { o["at"] = .string(Agent.format(at)) }
        if let branch { o["branch"] = .string(branch) }
        return .object(o)
    }

    /// History's meta: "checkpoint · 3 files" ("checkpoint" when clean).
    public var meta: String {
        changed > 0 ? "checkpoint · \(changed) file\(changed == 1 ? "" : "s")" : "checkpoint"
    }
}

// MARK: Wire

public enum MoveRPC {
    public static let move = "agents.move"
    public static let checkpoint = "agents.checkpoint"
    /// Restores a gone agent's checkpoint into a worktree on its Mac.
    public static let restore = "checkpoints.restore"
    public static let progress = "agents.moving"
    /// The removed reason of a moved agent (`data.to`: the new agent).
    public static let movedReason = "moved"

    /// An older hesperd (here, or the agent's Mac through the relay): the
    /// move actions hide for that Mac.
    public static func missing(_ e: RPCError) -> Bool { CloseRPC.missing(e) }
}

/// What a move asks of hesperd beyond the target.
public struct MoveOptions: Equatable, Sendable, Hashable {
    /// Keep the agent here too (a fork).
    public var fork = false
    /// Interrupt a working agent first.
    public var interrupt = false
    /// Move although it started processes (dev servers) that stay here.
    public var leaveProcesses = false

    public init(fork: Bool = false, interrupt: Bool = false, leaveProcesses: Bool = false) {
        self.fork = fork; self.interrupt = interrupt; self.leaveProcesses = leaveProcesses
    }

    /// agents.move's params.
    public func params(id: String, to: String) -> JSONValue {
        var p: [String: JSONValue] = ["id": .string(id), "to": .string(to)]
        if fork { p["fork"] = true }
        if interrupt { p["interrupt"] = true }
        if leaveProcesses { p["leaveProcesses"] = true }
        return .object(p)
    }
}

/// A process the agent started that would not move (`data.processes`).
public struct MoveProcess: Equatable, Sendable, Hashable {
    public var pid: Int
    public var command: String
    public init(pid: Int, command: String) { self.pid = pid; self.command = command }
}

// MARK: Preflight

/// Why hesperd refused a move before touching anything; asked inline on
/// the agent's tile (the close strip's place).
public enum MovePreflight: Equatable, Sendable {
    /// It is working: "Interrupt and move".
    case busy
    /// It started processes that stay here: "Move anyway".
    case processes([MoveProcess])
    /// The tool (claude / codex) isn't on the target's PATH.
    case toolMissing(String)
    /// The project folder isn't on the target and has no git remote.
    case noRemote
    /// The target Mac is offline.
    case offline
    /// Over hesperd's 200 MB bundle cap.
    case tooLarge
    /// Anything else hesperd said (its message).
    case other(String)

    /// The refusal behind an agents.move error; nil: an older hesperd
    /// (the caller hides the actions for that Mac).
    public init?(_ e: RPCError, tool: String = "") {
        if MoveRPC.missing(e) { return nil }
        switch e.dataCode {
        case "busy": self = .busy
        case "processes":
            let list = (e.data?["processes"]?.arrayValue ?? []).compactMap { p -> MoveProcess? in
                guard let cmd = p["command"]?.stringValue ?? p["cmd"]?.stringValue else { return nil }
                return MoveProcess(pid: Int(p["pid"]?.doubleValue ?? 0), command: cmd)
            }
            self = .processes(list)
        case "tool-missing", "tool_missing":
            self = .toolMissing(e.data?["tool"]?.stringValue ?? tool)
        case "no-remote", "no_remote": self = .noRemote
        case "offline": self = .offline
        case "too-large", "too_large": self = .tooLarge
        default:
            self = e.code == CloseRPC.machineOffline ? .offline : .other(e.message)
        }
    }

    /// The strip's text.
    public func message(name: String, target: String, project: String? = nil) -> String {
        switch self {
        case .busy: return "\(name) is working"
        case .processes(let ps):
            let cmds = ps.map { MoveText.shortCommand($0.command) }
            switch cmds.count {
            case 0: return "Processes it started keep running here"
            case 1: return "\(cmds[0]) is still running here"
            default: return "\(cmds[0]) and \(cmds.count - 1) more are still running here"
            }
        case .toolMissing(let t):
            let tool = t.isEmpty ? "The tool" : Self.toolName(t)
            return "\(tool) isn't installed on \(target)"
        case .noRemote:
            let p = project.map { "\($0) isn't" } ?? "The project isn't"
            return "\(p) on \(target) and has no git remote to clone"
        case .offline: return "\(target) is offline"
        case .tooLarge: return "Too much to carry (over 200 MB of changes)"
        case .other(let m): return "Could not move: \(m)"
        }
    }

    static func toolName(_ t: String) -> String {
        switch t {
        case "claude": return "Claude Code"
        case "codex": return "Codex"
        default: return t
        }
    }

    /// ⏎ on the strip, when the move can go on: its title and what it
    /// adds; nil: only a message (esc dismisses).
    public func primary(tight: Bool = false) -> (title: String, options: MoveOptions)? {
        switch self {
        case .busy: return ("Interrupt and move", MoveOptions(interrupt: true))
        case .processes: return (tight ? "Move anyway" : "Move anyway (leave them running)", MoveOptions(leaveProcesses: true))
        default: return nil
        }
    }

    /// The full list for the strip's tooltip (processes).
    public var detail: String? {
        guard case .processes(let ps) = self, !ps.isEmpty else { return nil }
        return ps.map { "\($0.pid > 0 ? "\($0.pid)  " : "")\($0.command)" }.joined(separator: "\n")
    }

    /// The strip's mark: working when it can go on, else an error.
    public var actionable: Bool { primary() != nil }
}

// MARK: Progress (agents.moving)

public enum MoveStep: String, CaseIterable, Sendable {
    case checkpoint, transfer, worktree, resume

    public init?(wire s: String) {
        let w = s.lowercased().split(whereSeparator: { $0 == " " || $0 == ":" }).first.map(String.init) ?? ""
        switch w {
        case "checkpoint", "checkpointing": self = .checkpoint
        case "transfer", "transferring", "bundle", "upload", "fetch": self = .transfer
        case "worktree", "replica", "clone": self = .worktree
        case "resume", "resuming", "resumed", "handover", "spawn", "done": self = .resume
        default: return nil
        }
    }

    public var title: String {
        switch self {
        case .checkpoint: return "checkpoint"
        case .transfer: return "transfer"
        case .worktree: return "worktree"
        case .resume: return "resuming"
        }
    }
}

/// One agents.moving notification: where a move is.
public struct MoveProgress: Equatable, Sendable {
    public var id: String
    public var to: String
    /// nil: sent, nothing heard yet (preflight).
    public var step: MoveStep?
    /// The transfer's 0–100, when known.
    public var percent: Int?
    /// The transfer's size in bytes as it travels (`total`), when known.
    public var total: Int64?

    public init(id: String, to: String, step: MoveStep? = nil, percent: Int? = nil, total: Int64? = nil) {
        self.id = id; self.to = to; self.step = step; self.percent = percent; self.total = total
    }

    /// `{id, step, to, percent?}`; the percent may also ride in the step
    /// ("transfer 42%") or come as `progress` (0–1 or 0–100) or
    /// `bytes`/`total`.
    public init?(params p: JSONValue) {
        guard let id = p["id"]?.stringValue, !id.isEmpty else { return nil }
        self.id = id
        to = p["to"]?.stringValue ?? ""
        let raw = p["step"]?.stringValue ?? ""
        step = MoveStep(wire: raw)
        var pct: Double?
        if let n = p["percent"]?.doubleValue {
            pct = n
        } else if let n = p["progress"]?.doubleValue {
            pct = n <= 1 ? n * 100 : n
        } else if let b = p["bytes"]?.doubleValue, let t = p["total"]?.doubleValue, t > 0 {
            pct = b / t * 100
        } else if let r = raw.range(of: #"\d+(?=\s*%)"#, options: .regularExpression) {
            pct = Double(raw[r])
        }
        percent = pct.map { Int(max(0, min(100, $0)).rounded()) }
        total = p["total"]?.doubleValue.flatMap { $0 > 0 ? Int64($0) : nil }
    }

    public enum Status: Equatable, Sendable { case done, current, pending }

    /// The steps in order with where each is.
    public var steps: [(step: MoveStep, status: Status)] {
        let cur = step.flatMap { MoveStep.allCases.firstIndex(of: $0) }
        return MoveStep.allCases.enumerated().map { i, s in
            guard let cur else { return (s, .pending) }
            return (s, i < cur ? .done : i == cur ? .current : .pending)
        }
    }

    /// A step's label ("transfer 42%" while it runs, "transfer 42% of
    /// 1.4 GB" once its size is known).
    public func label(_ s: MoveStep) -> String {
        guard s == .transfer, step == .transfer else { return s.title }
        let size = total.map { " of " + TransferSize.text($0) } ?? ""
        if let percent { return "transfer \(percent)%" + size }
        return total == nil ? s.title : "transfer" + size
    }

    /// The tile's progress line: "Moving to mini · checkpoint → transfer
    /// 42% → worktree → resuming" (nothing heard yet: "Moving to mini…").
    public func line(target: String, fork: Bool = false) -> String {
        let head = "\(fork ? "Forking" : "Moving") to \(target)"
        guard step != nil else { return head + "…" }
        return head + " · " + MoveStep.allCases.map(label).joined(separator: " → ")
    }
}

/// A transfer's size for people: "82.0 MB", "1.4 GB".
public enum TransferSize {
    public static func text(_ n: Int64) -> String {
        let d = Double(n)
        if n >= 1 << 30 { return String(format: "%.1f GB", d / Double(1 << 30)) }
        if n >= 1 << 20 { return String(format: "%.1f MB", d / Double(1 << 20)) }
        if n >= 1 << 10 { return String(format: "%.0f KB", d / Double(1 << 10)) }
        return "\(n) B"
    }
}

/// agents.removed of a moved agent: the agent that continues it.
public enum MoveRemoval {
    /// The new agent's id when `params` say the agent moved (`reason`
    /// "moved", `data.to` or `to`); nil otherwise.
    public static func newAgent(_ p: JSONValue) -> String? {
        guard p["reason"]?.stringValue == MoveRPC.movedReason else { return nil }
        let to = p["data"]?["to"]?.stringValue ?? p["to"]?.stringValue
        return to.flatMap { $0.isEmpty ? nil : $0 }
    }

    /// agents.move's result: `{agent: "<new id>"}` (or the agent itself).
    public static func newAgent(result r: JSONValue) -> String? {
        if let s = r["agent"]?.stringValue, !s.isEmpty { return s }
        if let s = r["agent"]?["id"]?.stringValue, !s.isEmpty { return s }
        if let s = r["id"]?.stringValue, !s.isEmpty { return s }
        return nil
    }
}

// MARK: Which actions show

/// Where an agent can go, and where the move actions show.
public enum MoveRules {
    /// The states hesperd moves at once (a working agent asks first).
    public static let settled: Set<AgentState> = [.done, .idle, .question, .approval, .error, .exited]

    /// Whether this agent can move at all: a Claude / Codex agent (a shell
    /// has no session to carry), not starting, its Mac's hesperd can.
    /// - Parameter supported: its Mac's hesperd has agents.move (nil: not
    ///   known yet, assumed so until it says otherwise).
    public static func movable(_ a: Agent, supported: Bool?, moving: Bool = false) -> Bool {
        guard supported != false, !moving else { return false }
        guard a.kind == "claude" || a.kind == "codex" else { return false }
        return a.state != .starting && a.state != .unknown
    }

    /// The Macs it can go to: online, other than its own, in the hello's
    /// order. Empty: no move action shows.
    public static func targets(_ a: Agent, machines: [Machine], supported: Bool?, moving: Bool = false) -> [Machine] {
        guard movable(a, supported: supported, moving: moving) else { return [] }
        return machines.filter { $0.online && $0.short != a.machine }
    }

    /// The tile's band offers "Continue on <Mac>" for finished agents
    /// (done, idle); others only in the menu, palette, ⌘J and History.
    public static func showsOnStrip(_ a: Agent) -> Bool { a.state == .done || a.state == .idle }

    /// The ⌘J inbox offers it for items that wait (approval, question,
    /// error), which are all settled.
    public static func showsInInbox(_ a: Agent) -> Bool { settled.contains(a.state) }
}

public enum MoveText {
    /// "Continue on mini" (tile, menu), "Continue migrations on mini" (palette).
    public static func continueTitle(_ target: String, name: String? = nil) -> String {
        name.map { "Continue \($0) on \(target)" } ?? "Continue on \(target)"
    }

    public static func forkTitle(_ target: String, name: String? = nil) -> String {
        name.map { "Fork \($0) on \(target)" } ?? "Fork on \(target)"
    }

    /// The toast after a move: "Moved migrations to mini" (⌘Z moves it back).
    public static func moved(_ name: String, to target: String, fork: Bool = false) -> String {
        fork ? "Forked \(name) on \(target)" : "Moved \(name) to \(target)"
    }

    /// An older hesperd on that Mac.
    public static func needsUpdate(_ machine: String) -> String {
        "Moving needs a newer hesperd on \(machine)"
    }

    /// "npm run dev" from "/usr/local/bin/node /x/y/vite --port 5173": the
    /// first word's last path part plus the next words, at most 32 chars.
    public static func shortCommand(_ cmd: String) -> String {
        let words = cmd.split(separator: " ", omittingEmptySubsequences: true).map(String.init)
        guard let first = words.first else { return cmd }
        let head = (first as NSString).lastPathComponent
        var out = ([head] + words.dropFirst().prefix(2).map { $0.hasPrefix("/") ? ($0 as NSString).lastPathComponent : $0 }).joined(separator: " ")
        if out.count > 32 { out = String(out.prefix(31)) + "…" }
        return out
    }
}
