import Foundation

// Routines (⌘⇧R, docs "As built — routines"): saved agents.spawn calls that
// hesperd starts on a cadence; each run is an agent whose session stays in
// History. The wire types (`routines.*`, relay/pkg/wire/routines.go) are
// decoded by hand from JSONValue and lenient like review's: an unknown
// outcome keeps its text, missing policy fields take the daemon's
// defaults, and the spawn is kept as it came so a save sends it back
// whole. The pure rules the panel shows (cadence, next and last run,
// the chip's tone, the list's order) are here too, tested headless.

// MARK: Wire names

public enum RoutinesRPC {
    public static let list = "routines.list"
    public static let save = "routines.save"
    public static let remove = "routines.remove"
    public static let pause = "routines.pause"
    public static let run = "routines.run"
    public static let runs = "routines.runs"
    /// Notifications on agents.subscribe connections: `{routine}` (one per
    /// routine first; a run beginning or ending is one too) and `{id}`.
    public static let changed = "routines.changed"
    public static let removed = "routines.removed"

    /// The method is missing on this hesperd (-32601 / "Unknown method").
    public static func missing(_ e: RPCError) -> Bool { CloseRPC.missing(e) }
}

/// A run's outcome on the wire; anything else is kept as its text.
public enum RunOutcome {
    public static let running = "running"
    public static let done = "done"
    public static let needsYou = "needs-you"
    public static let exited = "exited"
    public static let error = "error"
    public static let timedOut = "timed-out"
    public static let skipped = "skipped"
    public static let failed = "failed"
}

// MARK: Values

/// How a routine's runs behave; the zero values are hesperd's defaults.
public struct RunPolicy: Equatable, Sendable {
    public var timeout: String
    public var overlap: String
    public var catchUp: String
    public var foreground: Bool
    public var keepRuns: Int

    public static let defaultTimeout = "1h"
    public static let defaultOverlap = "skip"
    public static let defaultCatchUp = "once"
    public static let defaultKeepRuns = 200

    public init(timeout: String = defaultTimeout, overlap: String = defaultOverlap, catchUp: String = defaultCatchUp, foreground: Bool = false,
                keepRuns: Int = defaultKeepRuns) {
        self.timeout = timeout
        self.overlap = overlap
        self.catchUp = catchUp
        self.foreground = foreground
        self.keepRuns = keepRuns
    }

    /// Missing or empty fields take the defaults.
    public init(json v: JSONValue?) {
        let timeout = v?["timeout"].flatMap(Session.str) ?? ""
        let overlap = v?["overlap"].flatMap(Session.str) ?? ""
        let catchUp = v?["catchUp"].flatMap(Session.str) ?? ""
        let keep = v?["keepRuns"].flatMap(Session.int) ?? 0
        self.init(timeout: timeout.isEmpty ? Self.defaultTimeout : timeout, overlap: overlap.isEmpty ? Self.defaultOverlap : overlap,
                  catchUp: catchUp.isEmpty ? Self.defaultCatchUp : catchUp, foreground: v?["foreground"]?.boolValue ?? false,
                  keepRuns: keep > 0 ? keep : Self.defaultKeepRuns)
    }

    var param: JSONValue {
        var o: [String: JSONValue] = ["timeout": .string(timeout), "overlap": .string(overlap), "catchUp": .string(catchUp),
                                      "keepRuns": .number(Double(keepRuns))]
        if foreground { o["foreground"] = .bool(true) }
        return .object(o)
    }
}

/// One run of a routine (`routines.runs`, `Routine.lastRun`).
public struct RoutineRun: Equatable, Sendable, Identifiable {
    /// From 1 within its routine.
    public var seq: Int
    public var started: Date?
    public var ended: Date?
    public var agent: String?
    public var session: String?
    /// `RunOutcome`; unknown values keep their text.
    public var outcome: String
    public var state: String?
    public var message: String?
    public var error: String?
    public var manual: Bool

    public var id: Int { seq }

    public init(seq: Int, started: Date? = nil, ended: Date? = nil, agent: String? = nil, session: String? = nil, outcome: String = RunOutcome.done,
                state: String? = nil, message: String? = nil, error: String? = nil, manual: Bool = false) {
        self.seq = seq
        self.started = started
        self.ended = ended
        self.agent = agent
        self.session = session
        self.outcome = outcome
        self.state = state
        self.message = message
        self.error = error
        self.manual = manual
    }

    public init?(json v: JSONValue) {
        guard case .object(let o) = v else { return nil }
        self.init(seq: o["seq"].flatMap(Session.int) ?? 0, started: o["started"].flatMap(Session.date), ended: o["ended"].flatMap(Session.date),
                  agent: ReviewItem.nonEmpty(o["agent"]), session: ReviewItem.nonEmpty(o["session"]),
                  outcome: ReviewItem.nonEmpty(o["outcome"]) ?? RunOutcome.running, state: ReviewItem.nonEmpty(o["state"]),
                  message: ReviewItem.nonEmpty(o["message"]), error: ReviewItem.nonEmpty(o["error"]), manual: o["manual"]?.boolValue ?? false)
    }

    public var isRunning: Bool { outcome == RunOutcome.running }
    public var needsYou: Bool { outcome == RunOutcome.needsYou }
    /// The run has an agent to show (running, or stopped at an approval).
    public var isLive: Bool { isRunning || needsYou }

    /// `routines.runs`'s answer (an array, or `{runs}`), newest first.
    public static func list(json v: JSONValue) -> [RoutineRun] {
        (v.arrayValue ?? v["runs"]?.arrayValue ?? []).compactMap(RoutineRun.init(json:))
    }

    var param: JSONValue {
        var o: [String: JSONValue] = ["seq": .number(Double(seq)), "outcome": .string(outcome)]
        if let started { o["started"] = .string(Session.format(started)) }
        if let ended { o["ended"] = .string(Session.format(ended)) }
        if let agent { o["agent"] = .string(agent) }
        if let session { o["session"] = .string(session) }
        if let state { o["state"] = .string(state) }
        if let message { o["message"] = .string(message) }
        if let error { o["error"] = .string(error) }
        if manual { o["manual"] = .bool(true) }
        return .object(o)
    }
}

/// A saved, recurring spawn (`wire.Routine`).
public struct Routine: Equatable, Sendable, Identifiable {
    public var id: String
    public var name: String
    /// The Mac that owns it ("" : this one).
    public var machine: String
    /// What each run starts with, as hesperd gave it (a save sends it back
    /// whole); `task`, `project` and `kind` read it.
    public var spawn: JSONValue
    public var every: String?
    public var offset: String?
    public var at: [String]
    public var policy: RunPolicy
    public var paused: Bool
    public var createdBy: String?
    public var created: Date?
    public var updated: Date?
    /// Every run begun so far.
    public var runs: Int
    public var lastRun: RoutineRun?
    /// nil while paused.
    public var nextRun: Date?

    public init(id: String, name: String = "", machine: String = "", spawn: JSONValue = .object([:]), every: String? = nil, offset: String? = nil,
                at: [String] = [], policy: RunPolicy = RunPolicy(), paused: Bool = false, createdBy: String? = nil, created: Date? = nil,
                updated: Date? = nil, runs: Int = 0, lastRun: RoutineRun? = nil, nextRun: Date? = nil) {
        self.id = id
        self.name = name.isEmpty ? Self.defaultName(id) : name
        self.machine = machine
        self.spawn = spawn
        self.every = every
        self.offset = offset
        self.at = at
        self.policy = policy
        self.paused = paused
        self.createdBy = createdBy
        self.created = created
        self.updated = updated
        self.runs = runs
        self.lastRun = lastRun
        self.nextRun = nextRun
    }

    /// hesperd's default: the id without "r-".
    static func defaultName(_ id: String) -> String { id.hasPrefix("r-") ? String(id.dropFirst(2)) : id }

    public init?(json v: JSONValue) {
        guard case .object(let o) = v, let id = o["id"].flatMap(Session.str), !id.isEmpty else { return nil }
        self.init(id: id, name: o["name"].flatMap(Session.str) ?? "", machine: o["machine"].flatMap(Session.str) ?? "",
                  spawn: o["spawn"] ?? .object([:]), every: ReviewItem.nonEmpty(o["every"]), offset: ReviewItem.nonEmpty(o["offset"]),
                  at: (o["at"]?.arrayValue ?? []).compactMap(\.stringValue), policy: RunPolicy(json: o["policy"]),
                  paused: o["paused"]?.boolValue ?? false, createdBy: ReviewItem.nonEmpty(o["createdBy"]), created: o["created"].flatMap(Session.date),
                  updated: o["updated"].flatMap(Session.date), runs: o["runs"].flatMap(Session.int) ?? 0,
                  lastRun: o["lastRun"].flatMap(RoutineRun.init(json:)), nextRun: o["nextRun"].flatMap(Session.date))
    }

    public var task: String { spawn["task"]?.stringValue ?? "" }
    public var project: String? { ReviewItem.nonEmpty(spawn["project"]) }
    public var kind: String? { ReviewItem.nonEmpty(spawn["kind"]) }

    /// `routines.list`'s answer (an array, or `{routines}`).
    public static func list(json v: JSONValue) -> [Routine] {
        (v.arrayValue ?? v["routines"]?.arrayValue ?? []).compactMap(Routine.init(json:))
    }

    /// What `routines.save {routine}` takes: the whole routine (hesperd
    /// keeps `created`, `runs` and `lastRun` of an existing one itself).
    public var param: JSONValue {
        var o: [String: JSONValue] = ["id": .string(id), "name": .string(name), "spawn": spawn, "policy": policy.param]
        if !machine.isEmpty { o["machine"] = .string(machine) }
        if let every { o["every"] = .string(every) }
        if let offset { o["offset"] = .string(offset) }
        if !at.isEmpty { o["at"] = .array(at.map(JSONValue.string)) }
        if paused { o["paused"] = .bool(true) }
        if let createdBy { o["createdBy"] = .string(createdBy) }
        if let created { o["created"] = .string(Session.format(created)) }
        if let updated { o["updated"] = .string(Session.format(updated)) }
        if runs > 0 { o["runs"] = .number(Double(runs)) }
        if let lastRun { o["lastRun"] = lastRun.param }
        if let nextRun { o["nextRun"] = .string(Session.format(nextRun)) }
        return .object(o)
    }
}

// MARK: Support

/// Whether this hesperd has `routines.*` (-32601 says no, like History's
/// `sessions.stats`); nil until the first list answered.
public struct RoutinesSupport: Equatable, Sendable {
    public var local: Bool?

    public init(local: Bool? = nil) { self.local = local }

    public var available: Bool { local == true }

    /// Notes a refusal; true when it was "missing" (no routines here).
    @discardableResult
    public mutating func note(_ error: any Error) -> Bool {
        guard let e = error as? RPCError, RoutinesRPC.missing(e) else { return false }
        local = false
        return true
    }
}

// MARK: The list

public enum RoutineList {
    /// The order the panel shows: a run waiting for someone first (the
    /// newest first), then the running ones (newest start first), then by
    /// the next run (soonest first; paused and unscheduled last); then by
    /// name, then id.
    public static func ranked(_ routines: [Routine]) -> [Routine] {
        routines.sorted { a, b in
            let ga = group(a), gb = group(b)
            if ga != gb { return ga < gb }
            switch ga {
            case 0, 1:
                let ta = a.lastRun?.ended ?? a.lastRun?.started ?? .distantPast, tb = b.lastRun?.ended ?? b.lastRun?.started ?? .distantPast
                if ta != tb { return ta > tb }
            default:
                let na = a.paused ? Date.distantFuture : a.nextRun ?? .distantFuture, nb = b.paused ? Date.distantFuture : b.nextRun ?? .distantFuture
                if na != nb { return na < nb }
            }
            let c = a.name.localizedStandardCompare(b.name)
            return c == .orderedAscending || (c == .orderedSame && a.id < b.id)
        }
    }

    static func group(_ r: Routine) -> Int {
        guard let l = r.lastRun else { return 2 }
        return l.needsYou ? 0 : l.isRunning ? 1 : 2
    }
}

// MARK: Texts

/// The chip's color role: good (done), attention (needs you), critical
/// (error, timed out, failed), neutral (running, exited, skipped, unknown).
public enum RoutineTone: Equatable, Sendable {
    case good, attention, critical, neutral
}

public enum RoutineText {
    /// "every 1h +5m", "every 15m", "daily 07:30, 18:00".
    public static func cadence(_ r: Routine) -> String {
        if let e = r.every {
            let off = r.offset.flatMap { Self.isZero($0) ? nil : $0 }
            return "every \(e)" + (off.map { " +\($0)" } ?? "")
        }
        if !r.at.isEmpty { return "daily " + r.at.joined(separator: ", ") }
        return "no cadence"
    }

    static func isZero(_ d: String) -> Bool { d.isEmpty || d.allSatisfy { $0 == "0" || $0.isLetter } }

    /// "in 12m", "in 3h", "due", "paused".
    public static func nextRun(_ r: Routine, now: Date) -> String {
        if r.paused { return "paused" }
        guard let n = r.nextRun else { return "unscheduled" }
        let s = n.timeIntervalSince(now)
        return s <= 0 ? "due" : "in \(span(s))"
    }

    /// "done 48m ago", "running", "needs you", "timed out 2h ago",
    /// "skipped 3d ago", "never ran".
    public static func lastRun(_ r: Routine, now: Date) -> String {
        guard let l = r.lastRun else { return "never ran" }
        if l.isRunning || l.needsYou { return outcome(l.outcome) }
        guard let t = l.ended ?? l.started else { return outcome(l.outcome) }
        return "\(outcome(l.outcome)) \(ago(t, now: now))"
    }

    /// The outcome as words: "needs you", "timed out"; unknown ones as they are.
    public static func outcome(_ o: String) -> String {
        switch o {
        case RunOutcome.needsYou: return "needs you"
        case RunOutcome.timedOut: return "timed out"
        default: return o
        }
    }

    public static func tone(_ outcome: String) -> RoutineTone {
        switch outcome {
        case RunOutcome.done: return .good
        case RunOutcome.needsYou: return .attention
        case RunOutcome.error, RunOutcome.timedOut, RunOutcome.failed: return .critical
        default: return .neutral
        }
    }

    /// The run's one line: its message's first line, else its error; at
    /// most 80 characters.
    public static func summaryLine(_ run: RoutineRun) -> String {
        if let m = run.message, let first = m.split(whereSeparator: \.isNewline).first(where: { !$0.trimmingCharacters(in: .whitespaces).isEmpty }) {
            return SessionFormat.oneLine(String(first), max: 80)
        }
        if let e = run.error, !e.isEmpty { return SessionFormat.oneLine(e, max: 80) }
        return ""
    }

    /// How long a run took (or has been running): "3m 12s", "1h 05m".
    public static func duration(_ run: RoutineRun, now: Date) -> String {
        guard let s = run.started else { return "" }
        let end = run.ended ?? (run.isRunning ? now : nil)
        guard let end else { return "" }
        let d = max(0, Int(end.timeIntervalSince(s).rounded()))
        if d < 60 { return "\(d)s" }
        if d < 3600 { return "\(d / 60)m \(String(format: "%02d", d % 60))s" }
        return "\(d / 3600)h \(String(format: "%02d", (d % 3600) / 60))m"
    }

    /// "timeout 1h · overlap skip · catch up once · background · keeps 200 runs".
    public static func policy(_ p: RunPolicy) -> String {
        ["timeout \(p.timeout)", "overlap \(p.overlap)", "catch up \(p.catchUp)", p.foreground ? "foreground" : "background",
         "keeps \(p.keepRuns) run\(p.keepRuns == 1 ? "" : "s")"].joined(separator: " · ")
    }

    /// "48m ago", "now".
    public static func ago(_ d: Date, now: Date) -> String {
        let a = ProjectNav.age(d, now: now)
        return a == "now" ? a : "\(a) ago"
    }

    /// A span ahead: "<1m", "12m", "3h", "2d".
    static func span(_ s: TimeInterval) -> String {
        if s < 60 { return "<1m" }
        if s < 3600 { return "\(Int(s / 60))m" }
        if s < 86400 { return "\(Int(s / 3600))h" }
        return "\(Int(s / 86400))d"
    }

    /// The empty panel.
    public static let empty = "No routines yet. Save one with hesperctl routine save; it runs here on its cadence and its runs show up in this list."
}

// MARK: Keys

public enum RoutineKeyAction: Equatable, Sendable {
    /// ↑ ↓: the list's selection.
    case previous, next
    /// ⏎ run now, P pause / resume, O open the last run, ⌫ remove (twice).
    case runNow, togglePause, open, remove
    /// Esc, ⌘⇧R.
    case close
    /// Not the panel's.
    case pass
}

public enum RoutineKeys {
    /// `keyCode`: ⌫ (51) and ⌦ (117) arrive as characters, not as `.delete`.
    public static func route(_ k: KeyChord, keyCode: UInt16? = nil) -> RoutineKeyAction {
        if k.command && !k.option && !k.control {
            if k.shift, k.key == .char("r") || k.key == .char("R") { return .close }
            return .pass
        }
        if k.command || k.control || k.option { return .pass }
        if keyCode == 51 || keyCode == 117 { return .remove }
        switch k.key {
        case .escape: return .close
        case .up: return .previous
        case .down: return .next
        case .enter: return .runNow
        case .delete: return .remove
        case .char("p"), .char("P"): return .togglePause
        case .char("o"), .char("O"): return .open
        default: return .pass
        }
    }
}

// MARK: Calls

// Every call decodes on the calling task (callers run them off the main
// actor). -32601 / "Unknown method": `RoutinesRPC.missing`.
extension DaemonClient {
    public func routinesList() async throws -> [Routine] { Routine.list(json: try await call(RoutinesRPC.list, .object([:]), timeout: 30)) }

    public func routinesSave(_ r: Routine) async throws -> Routine {
        let v = try await call(RoutinesRPC.save, ["routine": r.param], timeout: 30)
        guard let saved = Routine(json: v["routine"] ?? v) else { throw RPCError(code: -32000, message: "routines.save returned no routine", kind: .unknown) }
        return saved
    }

    public func routinesRemove(_ id: String) async throws { _ = try await call(RoutinesRPC.remove, ["id": .string(id)], timeout: 30) }

    public func routinesPause(_ id: String, paused: Bool) async throws -> Routine? {
        Routine(json: try await call(RoutinesRPC.pause, ["id": .string(id), "paused": .bool(paused)], timeout: 30))
    }

    /// A run by hand; hesperd answers `exists` (data.code) while one is live.
    public func routinesRun(_ id: String) async throws -> RoutineRun? {
        RoutineRun(json: try await call(RoutinesRPC.run, ["id": .string(id)], timeout: 60))
    }

    /// Newest first, at most `limit` (hesperd: 50 when unset).
    public func routinesRuns(_ id: String, limit: Int? = nil) async throws -> [RoutineRun] {
        var p: [String: JSONValue] = ["id": .string(id)]
        if let limit { p["limit"] = .number(Double(limit)) }
        return RoutineRun.list(json: try await call(RoutinesRPC.runs, .object(p), timeout: 30))
    }
}
