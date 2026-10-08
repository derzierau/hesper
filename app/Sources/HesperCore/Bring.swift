import Foundation

// Bringing the folder along when a task starts on another Mac: the draft's
// folder is on this Mac, not on the Mac it runs on. The chip row's note
// offers "Bring it to mini" (with uncommitted changes; the default, ⌘↩),
// "Bring clean (last commit)", "Run on laptop" and "Use mini's copy".
// hesperd does the work (agents.spawn `bring`, the agents.bringing
// progress); the pure rules live here, AppModel+Drafts does the calls.

// MARK: Wire

public enum BringRPC {
    /// agents.bringing `{draft?, step, percent?, to}`.
    public static let progress = "agents.bringing"

    /// The hesperd (here or on the target) can't bring: an unknown method
    /// (-32601, "Unknown method" through the relay), an unknown param, or
    /// an older hesperd that ignored `bring` and said "no directory …".
    /// That Mac gets today's notes from then on.
    public static func unsupported(_ e: RPCError) -> Bool {
        if CloseRPC.missing(e) { return true }
        let m = e.message.lowercased()
        if m.contains("unknown param") || m.contains("unknown field") || m.contains("unknown bring") { return true }
        if e.dataCode == "exists" || e.dataCode == "too-large" || e.dataCode == "too_large" { return false }
        return DraftSync.missingDirectory(in: e.message) != nil
    }
}

/// What comes along: the folder with its uncommitted and untracked files,
/// or the last commit only.
public enum BringChanges: String, Equatable, Sendable, Hashable {
    case with, clean
}

/// agents.spawn's `bring`: the folder on `from` (this Mac) to recreate on
/// the spawn's machine before the agent starts there.
public struct BringRequest: Equatable, Sendable, Hashable {
    public var from: String
    public var path: String
    public var changes: BringChanges
    /// The draft being started: hesperd echoes it in agents.bringing, so
    /// two brings to the same Mac each get their own progress line.
    public var draft: String?

    public init(from: String, path: String, changes: BringChanges = .with, draft: String? = nil) {
        self.from = from; self.path = path; self.changes = changes; self.draft = draft
    }

    public var params: JSONValue {
        var o: [String: JSONValue] = ["from": .string(from), "path": .string(path), "changes": .string(changes.rawValue)]
        if let draft { o["draft"] = .string(draft) }
        return .object(o)
    }
}

/// Per Mac: its hesperd brings folders (nil: not known yet, assumed so
/// until a start says otherwise).
public struct BringSupport: Equatable, Sendable {
    public var byMachine: [String: Bool] = [:]
    public init() {}
    public func offered(_ machine: String) -> Bool { byMachine[machine] != false }
}

// MARK: The note's actions

/// One button in the chip row's folder note.
public enum FolderAction: Equatable, Sendable, Hashable {
    /// Start, bringing the folder from this Mac.
    case bring(BringChanges)
    /// Run on this Mac instead.
    case runHere
    /// The same project's folder on the target (another path).
    case useCopy(String)
    /// The target has the folder after all (a bring said "exists"): start there.
    case useExisting

    public func title(target: String, here: String) -> String {
        switch self {
        case .bring(.with): return "Bring it to \(target)"
        case .bring(.clean): return "Bring clean (last commit)"
        case .runHere: return "Run on \(here)"
        case .useCopy: return "Use \(target)'s copy"
        case .useExisting: return "Use it"
        }
    }

    /// The button's tooltip.
    public func help(folder: String, target: String) -> String {
        switch self {
        case .bring(.with): return "Copy \(folder) to \(target) with its uncommitted changes, then start there (⌘⏎)"
        case .bring(.clean): return "Copy \(folder) to \(target) as of its last commit, then start there"
        case .runHere: return "Start on this Mac instead"
        case .useCopy(let p): return p
        case .useExisting: return "Start in \(target)'s folder"
        }
    }

    /// Accessibility identifiers (UI tests).
    public var id: String {
        switch self {
        case .bring(.with): return "draft.folder.bring"
        case .bring(.clean): return "draft.folder.bringClean"
        case .runHere: return "draft.folder.runHere"
        case .useCopy: return "draft.folder.useCopy"
        case .useExisting: return "draft.folder.useExisting"
        }
    }
}

/// Why a bring didn't start (agents.spawn's error with `bring`), said
/// inline under the chips.
public enum BringFailure: Equatable, Sendable {
    /// Over hesperd's 100 MB cap (a folder without git).
    case tooLarge
    case offline
    /// The tool (claude / codex) isn't on the target's PATH.
    case toolMissing(String)
    /// The target has the folder (hesperd never overwrites).
    case exists
    case other(String)

    /// nil: the hesperd can't bring (`BringRPC.unsupported`; today's notes).
    public init?(_ e: RPCError, tool: String = "") {
        if BringRPC.unsupported(e) { return nil }
        switch e.dataCode {
        case "too-large", "too_large": self = .tooLarge
        case "offline": self = .offline
        case "tool-missing", "tool_missing": self = .toolMissing(e.data?["tool"]?.stringValue ?? tool)
        case "exists": self = .exists
        default:
            if e.code == CloseRPC.machineOffline { self = .offline } else { self = .other(e.message) }
        }
    }

    public func message(target: String) -> String {
        switch self {
        case .tooLarge: return "Too large to bring (over 100 MB) —"
        case .offline: return "\(target) is offline —"
        case .toolMissing(let t):
            return "\(t.isEmpty ? "The tool" : MovePreflight.toolName(t)) isn't installed on \(target) —"
        case .exists: return "\(target) already has it —"
        case .other(let m): return "Could not bring it: \(m) —"
        }
    }
}

extension FolderNote {
    /// The note's buttons, the default (⌘⏎) first. A folder on this Mac
    /// whose target can bring: "Bring it to mini", "Bring clean (last
    /// commit)", "Run on laptop", "Use mini's copy". Otherwise today's:
    /// "Run on laptop" (folder here), "Use mini's copy" (one exists).
    public func actions(bring: Bool, failure: BringFailure? = nil) -> [FolderAction] {
        let copyAction = copy.map { [FolderAction.useCopy($0)] } ?? []
        let here: [FolderAction] = kind == .elsewhere ? [.runHere] : []
        guard bring, kind == .elsewhere else { return here + copyAction }
        switch failure {
        case nil: return [.bring(.with), .bring(.clean)] + here + copyAction
        case .exists: return [.useExisting] + here
        case .other, .offline: return [.bring(.with)] + here + copyAction // try again
        case .tooLarge, .toolMissing: return here + copyAction
        }
    }

    /// The note's text with bringing in mind.
    public func text(bring: Bool, failure: BringFailure? = nil) -> String {
        guard bring, kind == .elsewhere else { return text }
        if let failure { return failure.message(target: target) }
        return "Folder isn't on \(target) —"
    }

    /// What ⌘⏎ does when the folder isn't on the target: bring it (no
    /// failure, or one worth trying again), start in the target's folder
    /// ("exists"), or nothing (the note says why).
    public func startAction(bring: Bool, failure: BringFailure? = nil) -> FolderAction? {
        guard let first = actions(bring: bring, failure: failure).first else { return nil }
        switch first {
        case .bring, .useExisting: return first
        default: return nil
        }
    }
}

// MARK: Progress (agents.bringing)

public enum BringStep: String, CaseIterable, Sendable {
    case checkpoint, transfer, unpack, spawn

    public init?(wire s: String) {
        let w = s.lowercased().split(whereSeparator: { $0 == " " || $0 == ":" }).first.map(String.init) ?? ""
        switch w {
        case "checkpoint", "checkpointing": self = .checkpoint
        case "transfer", "transferring", "bundle", "upload", "tar": self = .transfer
        case "unpack", "unpacking", "clone", "restore", "extract": self = .unpack
        case "spawn", "spawning", "start", "starting": self = .spawn
        default: return nil
        }
    }

    public var title: String {
        switch self {
        case .checkpoint: return "checkpoint"
        case .transfer: return "transfer"
        case .unpack: return "unpack"
        case .spawn: return "starting"
        }
    }

    /// The steps a bring shows: a clean one takes no checkpoint.
    public static func shown(_ changes: BringChanges) -> [BringStep] {
        changes == .clean ? allCases.filter { $0 != .checkpoint } : allCases
    }
}

/// One agents.bringing notification.
public struct BringProgress: Equatable, Sendable {
    public enum Phase: Equatable, Sendable { case running, done, failed(String?) }

    public var draft: String?
    public var to: String
    /// nil: sent, nothing heard yet.
    public var step: BringStep?
    public var percent: Int?
    /// The transfer's size in bytes as it travels (`total`), when known.
    public var total: Int64?
    public var phase: Phase = .running

    public init(draft: String? = nil, to: String, step: BringStep? = nil, percent: Int? = nil, phase: Phase = .running) {
        self.draft = draft; self.to = to; self.step = step; self.percent = percent; self.phase = phase
    }

    /// `{draft?, step, percent?, to}`; step "done" / "failed" (with
    /// `error` or `message`) end it. The percent may also come as
    /// `progress` (0–1 or 0–100), `bytes`/`total`, or in the step.
    public init?(params p: JSONValue) {
        let raw = p["step"]?.stringValue ?? ""
        let w = raw.lowercased()
        draft = p["draft"]?.stringValue.flatMap { $0.isEmpty ? nil : $0 }
        to = p["to"]?.stringValue ?? ""
        if w.hasPrefix("done") {
            phase = .done
            step = .spawn
        } else if w.hasPrefix("fail") || w.hasPrefix("error") {
            phase = .failed(p["error"]?.stringValue ?? p["message"]?.stringValue)
            step = nil
        } else {
            guard let s = BringStep(wire: raw) else { return nil }
            step = s
        }
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

    public func steps(_ changes: BringChanges = .with) -> [(step: BringStep, status: Status)] {
        let shown = BringStep.shown(changes)
        let cur = step.flatMap { s in shown.firstIndex(of: s) ?? (s == .checkpoint ? -1 : nil) }
        return shown.enumerated().map { i, s in
            guard let cur else { return (s, .pending) }
            if phase == .done { return (s, .done) }
            return (s, i < cur ? .done : i == cur ? .current : .pending)
        }
    }

    /// "transfer 42%" while it runs ("transfer 42% of 1.4 GB" once its
    /// size is known).
    public func label(_ s: BringStep) -> String {
        guard s == .transfer, step == .transfer else { return s.title }
        let size = total.map { " of " + TransferSize.text($0) } ?? ""
        if let percent { return "transfer \(percent)%" + size }
        return total == nil ? s.title : "transfer" + size
    }

    /// "Bringing to mini · checkpoint → transfer 42% → unpack → starting"
    /// (nothing heard yet: "Bringing to mini…").
    public func line(target: String, changes: BringChanges = .with) -> String {
        let head = "Bringing to \(target)"
        guard step != nil else { return head + "…" }
        return head + " · " + BringStep.shown(changes).map(label).joined(separator: " → ")
    }
}

/// The brings in flight, by draft: which notification belongs to which
/// draft tile (by `draft`, else the one bring to that Mac).
public struct BringTracker: Equatable, Sendable {
    public struct Pending: Equatable, Sendable {
        public var to: String
        public var changes: BringChanges
        public var progress: BringProgress
    }

    public private(set) var pending: [String: Pending] = [:]
    public init() {}

    public mutating func start(draft: String, to: String, changes: BringChanges) {
        pending[draft] = Pending(to: to, changes: changes, progress: BringProgress(draft: draft, to: to))
    }

    public mutating func finish(draft: String) { pending[draft] = nil }

    public func progress(draft: String) -> Pending? { pending[draft] }

    /// The draft `p` is about (its progress kept), nil: none of ours.
    @discardableResult
    public mutating func apply(_ p: BringProgress) -> String? {
        let id: String?
        if let d = p.draft {
            id = pending[d] != nil ? d : nil
        } else {
            let matches = pending.filter { p.to.isEmpty || $0.value.to == p.to }.keys.sorted()
            id = matches.count == 1 ? matches[0] : nil
        }
        guard let id, var entry = pending[id] else { return nil }
        var n = p
        n.draft = id
        if n.to.isEmpty { n.to = entry.to }
        // A late note never takes the line back (transfer after unpack).
        if let old = entry.progress.step, let new = n.step, n.phase == .running,
           let oi = BringStep.allCases.firstIndex(of: old), let ni = BringStep.allCases.firstIndex(of: new), ni < oi {
            return id
        }
        entry.progress = n
        pending[id] = entry
        return id
    }
}
