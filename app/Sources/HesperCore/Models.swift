import Foundation

/// Agent states from the contract. Unknown future states decode as `.unknown`
/// so a newer daemon never breaks the app.
public enum AgentState: String, Codable, Sendable, CaseIterable {
    case starting, working, approval, question, done, idle, error, exited, unknown

    public init(from decoder: any Decoder) throws {
        let raw = try decoder.singleValueContainer().decode(String.self)
        self = AgentState(rawValue: raw) ?? .unknown
    }

    /// States that need the user (ring, queue, notification).
    public var needsAttention: Bool { self == .approval || self == .question || self == .error }
    /// States whose tile shows the summary line.
    public var showsSummary: Bool { self == .done || self == .idle }
}

public struct Attention: Codable, Equatable, Sendable {
    public var kind: String
    public var title: String?
    public var detail: String?
    public var options: [String]?

    public init(kind: String, title: String? = nil, detail: String? = nil, options: [String]? = nil) {
        self.kind = kind; self.title = title; self.detail = detail; self.options = options
    }
}

public struct GridSize: Codable, Hashable, Sendable {
    public var cols: Int
    public var rows: Int
    public init(cols: Int, rows: Int) { self.cols = cols; self.rows = rows }
}

public struct Agent: Codable, Equatable, Sendable, Identifiable {
    public var id: String
    public var machine: String
    public var kind: String
    public var profile: String?
    public var name: String
    public var task: String?
    public var project: String?
    /// The daemon's project (projects.list); nil from an older daemon.
    public var projectId: String?
    public var worktree: String?
    public var branch: String?
    public var state: AgentState
    public var stateSince: Date?
    public var attention: Attention?
    public var summary: String?
    /// What it does right now (the running tool), from hooks.
    public var activity: String?
    public var sessionId: String?
    public var size: GridSize?
    public var created: Date?
    public var pid: Int?
    /// Set once the process ended (`error` with an exit, or `exited`).
    public var exit: ExitInfo?
    /// Sent to the background (⌥⌘W): running, on no wall (hesperd keeps it).
    public var background = false
    /// How it ended when hesperd says ("killed": ⌃⌘W).
    public var ended: String?
    /// Its worktree's last checkpoint (hesperd, on done / close / move).
    public var checkpoint: Checkpoint?

    /// The agent's process is alive (attach shows it live).
    public var isRunning: Bool { exit == nil && state != .exited }

    public init(id: String, machine: String? = nil, kind: String = "claude", name: String? = nil, state: AgentState = .working,
                stateSince: Date? = nil, attention: Attention? = nil, summary: String? = nil, size: GridSize? = nil,
                created: Date? = nil, project: String? = nil, projectId: String? = nil, branch: String? = nil) {
        self.id = id
        self.machine = machine ?? String(id.split(separator: "/").first ?? "L")
        self.kind = kind
        self.name = name ?? id
        self.state = state
        self.stateSince = stateSince
        self.attention = attention
        self.summary = summary
        self.size = size
        self.created = created
        self.project = project
        self.projectId = projectId
        self.branch = branch
    }

    enum CodingKeys: String, CodingKey {
        case id, machine, kind, profile, name, task, project, projectId, worktree, branch, state, stateSince, attention, summary, activity, sessionId, size, created, pid, exit
        case background, ended, checkpoint
    }

    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        id = try c.decode(String.self, forKey: .id)
        machine = try c.decodeIfPresent(String.self, forKey: .machine) ?? String(id.split(separator: "/").first ?? "")
        kind = try c.decodeIfPresent(String.self, forKey: .kind) ?? "shell"
        profile = try c.decodeIfPresent(String.self, forKey: .profile)
        name = try c.decodeIfPresent(String.self, forKey: .name) ?? id
        task = try c.decodeIfPresent(String.self, forKey: .task)
        project = try c.decodeIfPresent(String.self, forKey: .project)
        projectId = try c.decodeIfPresent(String.self, forKey: .projectId).flatMap { $0.isEmpty ? nil : $0 }
        worktree = try c.decodeIfPresent(String.self, forKey: .worktree)
        branch = try c.decodeIfPresent(String.self, forKey: .branch)
        state = try c.decodeIfPresent(AgentState.self, forKey: .state) ?? .unknown
        stateSince = Self.date(try c.decodeIfPresent(String.self, forKey: .stateSince))
        attention = try c.decodeIfPresent(Attention.self, forKey: .attention)
        summary = try c.decodeIfPresent(String.self, forKey: .summary)
        activity = try c.decodeIfPresent(String.self, forKey: .activity).flatMap { $0.isEmpty ? nil : $0 }
        sessionId = try c.decodeIfPresent(String.self, forKey: .sessionId)
        size = try c.decodeIfPresent(GridSize.self, forKey: .size)
        created = Self.date(try c.decodeIfPresent(String.self, forKey: .created))
        pid = try? c.decodeIfPresent(Int.self, forKey: .pid)
        exit = try? c.decodeIfPresent(ExitInfo.self, forKey: .exit)
        background = (try? c.decodeIfPresent(Bool.self, forKey: .background)) ?? false
        ended = (try? c.decodeIfPresent(String.self, forKey: .ended)).flatMap { $0.isEmpty ? nil : $0 }
        checkpoint = (try? c.decodeIfPresent(Checkpoint.self, forKey: .checkpoint)).flatMap { $0 }.flatMap { $0.ref.isEmpty && $0.commit.isEmpty ? nil : $0 }
    }

    public func encode(to encoder: any Encoder) throws {
        var c = encoder.container(keyedBy: CodingKeys.self)
        try c.encode(id, forKey: .id)
        try c.encode(machine, forKey: .machine)
        try c.encode(kind, forKey: .kind)
        try c.encodeIfPresent(profile, forKey: .profile)
        try c.encode(name, forKey: .name)
        try c.encodeIfPresent(task, forKey: .task)
        try c.encodeIfPresent(project, forKey: .project)
        try c.encodeIfPresent(projectId, forKey: .projectId)
        try c.encodeIfPresent(worktree, forKey: .worktree)
        try c.encodeIfPresent(branch, forKey: .branch)
        try c.encode(state, forKey: .state)
        try c.encodeIfPresent(stateSince.map(Self.format), forKey: .stateSince)
        try c.encodeIfPresent(attention, forKey: .attention)
        try c.encodeIfPresent(summary, forKey: .summary)
        try c.encodeIfPresent(activity, forKey: .activity)
        try c.encodeIfPresent(sessionId, forKey: .sessionId)
        try c.encodeIfPresent(size, forKey: .size)
        try c.encodeIfPresent(created.map(Self.format), forKey: .created)
        try c.encodeIfPresent(pid, forKey: .pid)
        try c.encodeIfPresent(exit, forKey: .exit)
        if background { try c.encode(true, forKey: .background) }
        try c.encodeIfPresent(ended, forKey: .ended)
        try c.encodeIfPresent(checkpoint, forKey: .checkpoint)
    }

    static func date(_ s: String?) -> Date? {
        guard let s, !s.isEmpty else { return nil }
        let f = ISO8601DateFormatter()
        f.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        if let d = f.date(from: s) { return d }
        f.formatOptions = [.withInternetDateTime]
        return f.date(from: s)
    }

    static func format(_ d: Date) -> String {
        let f = ISO8601DateFormatter()
        f.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        return f.string(from: d)
    }

    /// The agent's local part of the id ("a7f3k2" of "L/a7f3k2").
    public var localID: String { String(id.split(separator: "/").last ?? Substring(id)) }
}

/// `{code, signal}` of an ended process (code null when killed by a signal).
public struct ExitInfo: Codable, Equatable, Sendable {
    public var code: Int?
    public var signal: String?
    public init(code: Int? = nil, signal: String? = nil) { self.code = code; self.signal = signal }

    public var label: String {
        if let signal { return signal }
        if let code { return "exit \(code)" }
        return "ended"
    }
}

public struct Machine: Codable, Equatable, Sendable {
    public var short: String
    public var name: String
    public var online: Bool
    public var rttMs: Double?
    public var route: String?

    public init(short: String, name: String, online: Bool = true, rttMs: Double? = nil, route: String? = nil) {
        self.short = short; self.name = name; self.online = online; self.rttMs = rttMs; self.route = route
    }

    /// What the app calls it everywhere: the short name ("mini",
    /// "laptop"), never a host name (MachineLabel).
    public var displayName: String { MachineLabel.name(self) }

    enum CodingKeys: String, CodingKey { case short, name, online, rttMs, route }
    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        short = try c.decode(String.self, forKey: .short)
        name = try c.decodeIfPresent(String.self, forKey: .name) ?? short
        online = try c.decodeIfPresent(Bool.self, forKey: .online) ?? false
        rttMs = try? c.decodeIfPresent(Double.self, forKey: .rttMs)
        route = try c.decodeIfPresent(String.self, forKey: .route)
    }
}

public struct HelloInfo: Codable, Equatable, Sendable {
    public var daemon: String
    public var version: String
    public var machine: String
    public var machines: [Machine]
}

public struct RecentProject: Codable, Equatable, Sendable, Identifiable {
    public var path: String
    public var name: String
    public var lastUsed: String?
    public var id: String { path }
    public init(path: String, name: String, lastUsed: String? = nil) { self.path = path; self.name = name; self.lastUsed = lastUsed }
}

public struct Profile: Codable, Equatable, Sendable {
    public var kind: String
    public var argv: [String]?
}

public struct ProfilesInfo: Codable, Equatable, Sendable {
    public var profiles: [String: Profile]
    public var defaults: ProfileDefaults?

    /// The profile hesperd would pick for a project (or the default kind).
    public func defaultProfile(project: String?, kind: String? = nil) -> String? {
        if let project, let p = defaults?.projects?[project] {
            if kind == nil || profiles[p]?.kind == kind { return p }
        }
        return defaults?.kinds?[kind ?? defaults?.kind ?? "claude"]
    }
}

/// `profiles.list` → `defaults`: `{kind, kinds: {kind → profile}, projects: {path → profile}}`.
public struct ProfileDefaults: Codable, Equatable, Sendable {
    public var kind: String?
    public var kinds: [String: String]?
    public var projects: [String: String]?
    public init(kind: String? = nil, kinds: [String: String]? = nil, projects: [String: String]? = nil) {
        self.kind = kind; self.kinds = kinds; self.projects = projects
    }
}

/// agents.spawn params. `worktree` is true (daemon picks a path) or a path.
public struct SpawnRequest: Equatable, Sendable {
    public var machine: String?
    public var profile: String?
    public var kind: String?
    public var project: String
    public var task: String
    public var name: String?
    public var worktree: Worktree?
    public var branch: String?

    public enum Worktree: Equatable, Sendable { case auto, path(String) }

    public init(machine: String? = nil, profile: String? = nil, kind: String? = nil, project: String, task: String,
                name: String? = nil, worktree: Worktree? = nil, branch: String? = nil) {
        self.machine = machine; self.profile = profile; self.kind = kind; self.project = project; self.task = task
        self.name = name; self.worktree = worktree; self.branch = branch
    }

    public var params: JSONValue {
        var o: [String: JSONValue] = ["project": .string(project), "task": .string(task)]
        if let machine { o["machine"] = .string(machine) }
        if let profile { o["profile"] = .string(profile) }
        if let kind { o["kind"] = .string(kind) }
        if let name, !name.isEmpty { o["name"] = .string(name) }
        switch worktree {
        case .auto: o["worktree"] = .bool(true)
        case .path(let p): o["worktree"] = .string(p)
        case nil: break
        }
        if let branch, !branch.isEmpty { o["branch"] = .string(branch) }
        return .object(o)
    }
}

/// agents.answer decisions: approvals (allow, always, deny) and the trust
/// question hesperd finds on a first-run screen (trust, exit).
/// agents.answer decisions: approvals (allow, always, deny), the trust
/// question of a first run (trust, exit), Codex's update screen (skip,
/// update).
public enum Decision: String, Sendable, CaseIterable { case allow, always, deny, trust, exit, skip, update

    public var title: String {
        switch self {
        case .allow: return "Allow"
        case .always: return "Always"
        case .deny: return "Deny"
        case .trust: return "Trust"
        case .exit: return "Exit"
        case .skip: return "Skip"
        case .update: return "Update"
        }
    }
}

extension Attention {
    /// A question hesperd answers itself (trust/exit, skip/update): its
    /// options as decisions, the first one the default (⏎).
    public var answers: [Decision] {
        guard kind == "question" else { return [] }
        return (options ?? []).compactMap(Decision.init(rawValue:))
    }
    /// "Trust ~/folder?": answered with trust / exit.
    public var isTrust: Bool { answers.contains(.trust) }
}
