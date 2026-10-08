import Foundation

// Shared history (docs/rebuild-contract.md "As built — shared history
// (app)"): every Claude and Codex session hesperd indexed on any Mac, as
// the app gets it from `sessions.*`. Pure model, decoding and query
// building; formatting is in SessionFormat.swift.

public struct SessionTodo: Equatable, Sendable, Hashable {
    public var text: String
    public var done: Bool
    public init(text: String, done: Bool = false) { self.text = text; self.done = done }
}

/// A session with a live process: a Hesper agent (`agentId`), or a process
/// outside Hesper (`external`: a terminal, an IDE, a Remote Control bridge).
public struct SessionLive: Equatable, Sendable, Hashable {
    public var agentId: String?
    public var external: Bool
    public init(agentId: String? = nil, external: Bool = false) { self.agentId = agentId; self.external = external }
}

/// `sessions.search` items and `sessions.changed {session}`.
public struct Session: Equatable, Sendable, Identifiable, Hashable {
    public var id: String
    /// "claude" | "codex" (others shown as given).
    public var kind: String
    /// The CLI's own id (`claude --resume <id>`, `codex resume <id>`).
    public var sessionId: String
    /// The Mac that owns it (short name).
    public var machine: String
    public var cwd: String
    public var projectId: String?
    public var branch: String?
    public var title: String
    public var firstPrompt: String
    public var lastUser: String
    public var lastAssistant: String
    public var todos: [SessionTodo]
    public var turns: Int
    public var tokens: Int?
    public var startedAt: Date?
    public var lastActivity: Date?
    public var live: SessionLive?
    /// Started outside Hesper.
    public var external: Bool
    public var archived: Bool
    /// Other Macs holding a mirrored transcript.
    public var mirrored: [String]
    /// The search hit: hesperd marks the matched words `[like this]`
    /// (`SessionFormat.highlights`; `<b>…</b>` and \u{2}…\u{3} too).
    public var snippet: String?
    /// The Hesper agent that ran it was removed then (ghost cards).
    public var removedAt: Date?
    /// Continued on that machine: this entry is read-only history and
    /// resuming it goes to the new home (search leaves these out unless
    /// asked: `moved`).
    public var movedTo: String?
    /// What wrote it: "cli", "claude-desktop", "codex-tui", "Codex Desktop"…
    public var origin: String?
    /// The transcript's size on its home.
    public var bytes: Int?
    /// The agent's last worktree checkpoint (hesperd; "checkpoint · 3
    /// files", "Restore checkpoint" once the agent is gone).
    public var checkpoint: Checkpoint?

    public init(id: String, kind: String = "claude", sessionId: String = "", machine: String = "L", cwd: String = "", projectId: String? = nil,
                branch: String? = nil, title: String = "", firstPrompt: String = "", lastUser: String = "", lastAssistant: String = "",
                todos: [SessionTodo] = [], turns: Int = 0, tokens: Int? = nil, startedAt: Date? = nil, lastActivity: Date? = nil,
                live: SessionLive? = nil, external: Bool = false, archived: Bool = false, mirrored: [String] = [], snippet: String? = nil,
                removedAt: Date? = nil, movedTo: String? = nil, origin: String? = nil, bytes: Int? = nil, checkpoint: Checkpoint? = nil) {
        self.id = id; self.kind = kind; self.sessionId = sessionId; self.machine = machine; self.cwd = cwd; self.projectId = projectId
        self.branch = branch; self.title = title; self.firstPrompt = firstPrompt; self.lastUser = lastUser; self.lastAssistant = lastAssistant
        self.todos = todos; self.turns = turns; self.tokens = tokens; self.startedAt = startedAt; self.lastActivity = lastActivity
        self.live = live; self.external = external; self.archived = archived; self.mirrored = mirrored; self.snippet = snippet
        self.removedAt = removedAt; self.movedTo = movedTo; self.origin = origin; self.bytes = bytes
        self.checkpoint = checkpoint
    }

    /// Lenient: missing fields get defaults, numbers may be strings, dates
    /// RFC 3339 (with or without fractions) or Unix seconds/milliseconds.
    /// Hand-written (no JSONEncoder round trip): pages of 100 decode in
    /// well under a millisecond, off the main thread.
    public init?(json v: JSONValue) {
        guard case .object(let o) = v, let id = o["id"].flatMap(Self.str), !id.isEmpty else { return nil }
        self.id = id
        kind = o["kind"].flatMap(Self.str) ?? "claude"
        sessionId = o["sessionId"].flatMap(Self.str) ?? ""
        machine = o["machine"].flatMap(Self.str) ?? ""
        cwd = o["cwd"].flatMap(Self.str) ?? ""
        projectId = o["projectId"].flatMap(Self.str).flatMap { $0.isEmpty ? nil : $0 }
        branch = o["branch"].flatMap(Self.str).flatMap { $0.isEmpty ? nil : $0 }
        firstPrompt = o["firstPrompt"].flatMap(Self.str) ?? ""
        let t = o["title"].flatMap(Self.str) ?? ""
        title = t.isEmpty ? Self.firstLine(firstPrompt) : t
        lastUser = o["lastUser"].flatMap(Self.str) ?? ""
        lastAssistant = o["lastAssistant"].flatMap(Self.str) ?? ""
        todos = (o["todos"]?.arrayValue ?? []).compactMap { t in
            if let s = t.stringValue { return SessionTodo(text: s) }
            guard let text = t["text"].flatMap(Self.str) ?? t["content"].flatMap(Self.str) else { return nil }
            let done = t["done"]?.boolValue ?? (t["status"]?.stringValue == "completed")
            return SessionTodo(text: text, done: done)
        }
        turns = o["turns"].flatMap(Self.int) ?? 0
        tokens = o["tokens"].flatMap(Self.int)
        startedAt = o["startedAt"].flatMap(Self.date)
        lastActivity = o["lastActivity"].flatMap(Self.date)
        if let l = o["live"], case .object(let lo) = l {
            live = SessionLive(agentId: lo["agentId"].flatMap(Self.str).flatMap { $0.isEmpty ? nil : $0 }, external: lo["external"]?.boolValue ?? false)
        } else if o["live"]?.boolValue == true {
            live = SessionLive()
        } else {
            live = nil
        }
        external = o["external"]?.boolValue ?? false
        archived = o["archived"]?.boolValue ?? false
        mirrored = (o["mirrored"]?.arrayValue ?? []).compactMap(\.stringValue)
        snippet = o["snippet"].flatMap(Self.str).flatMap { $0.isEmpty ? nil : $0 }
        removedAt = o["removedAt"].flatMap(Self.date)
        movedTo = o["movedTo"].flatMap(Self.str).flatMap { $0.isEmpty ? nil : $0 }
        origin = o["origin"].flatMap(Self.str).flatMap { $0.isEmpty ? nil : $0 }
        bytes = o["bytes"].flatMap(Self.int)
        checkpoint = Checkpoint(json: o["checkpoint"])
    }

    public var json: JSONValue {
        var o: [String: JSONValue] = [
            "id": .string(id), "kind": .string(kind), "sessionId": .string(sessionId), "machine": .string(machine), "cwd": .string(cwd),
            "title": .string(title), "firstPrompt": .string(firstPrompt), "lastUser": .string(lastUser), "lastAssistant": .string(lastAssistant),
            "todos": .array(todos.map { .object(["text": .string($0.text), "done": .bool($0.done)]) }),
            "turns": .number(Double(turns)), "external": .bool(external), "archived": .bool(archived),
            "mirrored": .array(mirrored.map(JSONValue.string)),
        ]
        if let projectId { o["projectId"] = .string(projectId) }
        if let branch { o["branch"] = .string(branch) }
        if let tokens { o["tokens"] = .number(Double(tokens)) }
        if let startedAt { o["startedAt"] = .string(Self.format(startedAt)) }
        if let lastActivity { o["lastActivity"] = .string(Self.format(lastActivity)) }
        if let live {
            var l: [String: JSONValue] = ["external": .bool(live.external)]
            if let a = live.agentId { l["agentId"] = .string(a) }
            o["live"] = .object(l)
        }
        if let snippet { o["snippet"] = .string(snippet) }
        if let removedAt { o["removedAt"] = .string(Self.format(removedAt)) }
        if let movedTo { o["movedTo"] = .string(movedTo) }
        if let origin { o["origin"] = .string(origin) }
        if let bytes { o["bytes"] = .number(Double(bytes)) }
        if let checkpoint { o["checkpoint"] = checkpoint.json }
        return .object(o)
    }

    /// Running somewhere: opening it shows that process, never a second one.
    public var isLive: Bool { live != nil }
    /// The Hesper agent running it now, if any.
    public var liveAgentID: String? { live?.agentId }
    public var isClaude: Bool { kind == "claude" }
    /// The kind "Continue in …" switches to.
    public var otherKind: String { kind == "codex" ? "claude" : "codex" }

    static func str(_ v: JSONValue) -> String? {
        switch v {
        case .string(let s): return s
        case .number(let n): return n == n.rounded() ? String(Int(n)) : String(n)
        default: return nil
        }
    }

    static func int(_ v: JSONValue) -> Int? {
        switch v {
        case .number(let n): return n.isFinite ? Int(n) : nil
        case .string(let s): return Int(s)
        default: return nil
        }
    }

    nonisolated(unsafe) private static let isoFrac: ISO8601DateFormatter = {
        let f = ISO8601DateFormatter()
        f.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        return f
    }()
    nonisolated(unsafe) private static let iso: ISO8601DateFormatter = {
        let f = ISO8601DateFormatter()
        f.formatOptions = [.withInternetDateTime]
        return f
    }()
    private static let dateLock = NSLock()

    public static func date(_ v: JSONValue) -> Date? {
        switch v {
        case .number(let n):
            guard n > 0 else { return nil }
            return Date(timeIntervalSince1970: n > 1e11 ? n / 1000 : n)
        case .string(let s):
            guard !s.isEmpty else { return nil }
            return dateLock.withLock { isoFrac.date(from: s) ?? iso.date(from: s) }
        default:
            return nil
        }
    }

    public static func format(_ d: Date) -> String { dateLock.withLock { isoFrac.string(from: d) } }

    static func firstLine(_ s: String) -> String {
        let line = s.split(whereSeparator: \.isNewline).first.map(String.init) ?? ""
        return line.trimmingCharacters(in: .whitespaces)
    }
}

/// `sessions.show {id}` → Session + changes (one git call on the session's
/// Mac when shown).
public struct SessionChanges: Equatable, Sendable {
    public struct File: Equatable, Sendable, Hashable {
        public var path: String
        public var added: Int
        public var removed: Int
        public init(path: String, added: Int, removed: Int) { self.path = path; self.added = added; self.removed = removed }
    }
    public var files: [File]
    /// Uncommitted work in the folder / worktree (a count when given).
    public var uncommitted: Bool
    public var uncommittedCount: Int?
    public var ahead: Int?
    public var behind: Int?
    /// The base `ahead`/`behind` count against (e.g. "main"), when given.
    public var base: String?
    public var worktreeExists: Bool

    public init(files: [File] = [], uncommitted: Bool = false, uncommittedCount: Int? = nil, ahead: Int? = nil, behind: Int? = nil,
                base: String? = nil, worktreeExists: Bool = true) {
        self.files = files; self.uncommitted = uncommitted; self.uncommittedCount = uncommittedCount
        self.ahead = ahead; self.behind = behind; self.base = base; self.worktreeExists = worktreeExists
    }

    public init?(json v: JSONValue) {
        guard case .object(let o) = v else { return nil }
        files = (o["files"]?.arrayValue ?? []).compactMap { f in
            guard let p = f["path"].flatMap(Session.str) else { return nil }
            return File(path: p, added: f["added"].flatMap(Session.int) ?? 0, removed: f["removed"].flatMap(Session.int) ?? 0)
        }
        if let b = o["uncommitted"]?.boolValue {
            uncommitted = b
            uncommittedCount = nil
        } else if let n = o["uncommitted"].flatMap(Session.int) {
            uncommitted = n > 0
            uncommittedCount = n
        } else {
            uncommitted = false
            uncommittedCount = nil
        }
        ahead = o["ahead"].flatMap(Session.int)
        behind = o["behind"].flatMap(Session.int)
        base = o["base"].flatMap(Session.str)
        worktreeExists = o["worktreeExists"]?.boolValue ?? true
    }
}

public struct SessionDetail: Equatable, Sendable {
    public var session: Session
    public var changes: SessionChanges?
    public init(session: Session, changes: SessionChanges? = nil) { self.session = session; self.changes = changes }
    public init?(json v: JSONValue) {
        guard let s = Session(json: v) else { return nil }
        session = s
        changes = v["changes"].flatMap(SessionChanges.init(json:))
    }
}

/// One page of `sessions.search`.
public struct SessionPage: Equatable, Sendable {
    public var items: [Session]
    public var cursor: String?
    public init(items: [Session], cursor: String? = nil) { self.items = items; self.cursor = cursor }
    public init(json v: JSONValue) {
        let list = v["items"]?.arrayValue ?? v.arrayValue ?? []
        items = list.compactMap(Session.init(json:))
        cursor = v["cursor"].flatMap(Session.str).flatMap { $0.isEmpty ? nil : $0 }
    }
}

/// `sessions.stats` → counts for the sidebar and the chips; lenient about
/// the shape (`projects` or `byProject`, …).
public struct SessionStats: Equatable, Sendable {
    public var total: Int
    public var byProject: [String: Int]
    public var scratch: Int
    public var elsewhere: Int
    public var byMachine: [String: Int]
    public var byKind: [String: Int]
    public var indexing: IndexingProgress?

    public init(total: Int = 0, byProject: [String: Int] = [:], scratch: Int = 0, elsewhere: Int = 0, byMachine: [String: Int] = [:],
                byKind: [String: Int] = [:], indexing: IndexingProgress? = nil) {
        self.total = total; self.byProject = byProject; self.scratch = scratch; self.elsewhere = elsewhere
        self.byMachine = byMachine; self.byKind = byKind; self.indexing = indexing
    }

    public init(json v: JSONValue) {
        func counts(_ keys: [String]) -> [String: Int] {
            for k in keys {
                if case .object(let o)? = v[k] { return o.compactMapValues(Session.int) }
            }
            return [:]
        }
        byProject = counts(["byProject", "projects"])
        byMachine = counts(["byMachine", "machines"])
        byKind = counts(["byKind", "kinds"])
        scratch = v["scratch"].flatMap(Session.int) ?? 0
        elsewhere = v["elsewhere"].flatMap(Session.int) ?? 0
        total = v["total"].flatMap(Session.int) ?? v["count"].flatMap(Session.int) ?? (byProject.values.reduce(0, +) + scratch + elsewhere)
        indexing = v["indexing"].flatMap(IndexingProgress.init(json:))
    }
}

/// `sessions.indexing {done, total}`: the first index runs (progress bar).
public struct IndexingProgress: Equatable, Sendable {
    public var done: Int
    public var total: Int
    public init(done: Int, total: Int) { self.done = done; self.total = total }
    public init?(json v: JSONValue) {
        guard let d = v["done"].flatMap(Session.int), let t = v["total"].flatMap(Session.int) else { return nil }
        done = d; total = t
    }
    public var finished: Bool { total <= 0 || done >= total }
    public var fraction: Double { total <= 0 ? 1 : min(1, Double(done) / Double(total)) }
}

// MARK: Query

/// Which projects the list shows (the sidebar's selection).
public enum HistoryScope: Equatable, Sendable, Hashable {
    case all
    case project(String)
    /// Sessions in scratch folders (hesperd's "scratch:<folder>" projects).
    case scratch
    /// Sessions whose folder is gone (no project anymore).
    case elsewhere

    /// The wire's `projectId` (sentinels for scratch / elsewhere).
    public var projectParam: String? {
        switch self {
        case .all: return nil
        case .project(let p): return p
        case .scratch: return HistoryQuery.scratchSentinel
        case .elsewhere: return HistoryQuery.elsewhereSentinel
        }
    }
}

/// The time chip: cycles any → 24 h → 7 days → 30 days.
public enum HistoryTime: String, CaseIterable, Sendable {
    case any, day, week, month
    public var label: String {
        switch self {
        case .any: return "any time"
        case .day: return "last 24 h"
        case .week: return "last 7 days"
        case .month: return "last 30 days"
        }
    }
    public var seconds: TimeInterval? {
        switch self {
        case .any: return nil
        case .day: return 86_400
        case .week: return 7 * 86_400
        case .month: return 30 * 86_400
        }
    }
    public var next: HistoryTime { let all = Self.allCases; return all[(all.firstIndex(of: self)! + 1) % all.count] }
}

/// Everything the panel filters by; `params` is the `sessions.search`
/// request. Empty sets mean "no filter".
public struct HistoryQuery: Equatable, Sendable {
    public static let scratchSentinel = "~scratch"
    public static let elsewhereSentinel = "~elsewhere"
    /// hesperd's default and maximum.
    public static let pageSize = 50

    public var text: String = ""
    public var scope: HistoryScope = .all
    public var kinds: Set<String> = []
    public var machines: Set<String> = []
    public var time: HistoryTime = .any
    /// Only sessions running in Hesper now.
    public var liveOnly = false
    /// Only sessions started outside Hesper.
    public var externalOnly = false
    /// Archived sessions instead of the others.
    public var archived = false
    /// Include sessions continued on another Mac (movedTo).
    public var moved = false
    public var limit: Int = HistoryQuery.pageSize

    public init(text: String = "", scope: HistoryScope = .all, kinds: Set<String> = [], machines: Set<String> = [], time: HistoryTime = .any,
                liveOnly: Bool = false, externalOnly: Bool = false, archived: Bool = false, moved: Bool = false, limit: Int = HistoryQuery.pageSize) {
        self.text = text; self.scope = scope; self.kinds = kinds; self.machines = machines; self.time = time
        self.liveOnly = liveOnly; self.externalOnly = externalOnly; self.archived = archived; self.moved = moved; self.limit = limit
    }

    /// The FTS query as typed, trimmed (empty: newest first).
    public var trimmedText: String { text.trimmingCharacters(in: .whitespacesAndNewlines) }

    public func params(cursor: String? = nil, now: Date = Date()) -> JSONValue {
        // hesperd caps a page at 50 (and treats 0 as 50).
        var p: [String: JSONValue] = ["limit": .number(Double(min(max(limit, 1), Self.pageSize)))]
        let q = trimmedText
        if !q.isEmpty { p["query"] = .string(q) }
        if let pid = scope.projectParam { p["projectId"] = .string(pid) }
        if !kinds.isEmpty { p["kinds"] = .array(kinds.sorted().map(JSONValue.string)) }
        if !machines.isEmpty { p["machines"] = .array(machines.sorted().map(JSONValue.string)) }
        if let s = time.seconds { p["since"] = .string(Session.format(now.addingTimeInterval(-s))) }
        if liveOnly { p["live"] = .bool(true) }
        if externalOnly { p["external"] = .bool(true) }
        if archived { p["archived"] = .bool(true) }
        if moved { p["moved"] = .bool(true) }
        if let cursor { p["cursor"] = .string(cursor) }
        return .object(p)
    }

    /// Whether a session (a `sessions.changed`) belongs in this list: the
    /// client-side mirror of the daemon's filters (text aside).
    public func matches(_ s: Session, now: Date = Date()) -> Bool {
        switch scope {
        case .all: break
        case .project(let p): if s.projectId != p { return false }
        case .scratch: if !(s.projectId?.hasPrefix(ProjectCatalog.scratchPrefix) ?? false) { return false }
        case .elsewhere: if s.projectId != nil && s.projectId != Self.elsewhereSentinel { return false }
        }
        if !kinds.isEmpty && !kinds.contains(s.kind) { return false }
        if !machines.isEmpty && !machines.contains(s.machine) { return false }
        if let sec = time.seconds, let t = s.lastActivity, t < now.addingTimeInterval(-sec) { return false }
        if liveOnly && s.liveAgentID == nil { return false }
        if externalOnly && !s.external { return false }
        if s.archived != archived { return false }
        if s.movedTo != nil && !moved { return false }
        return true
    }
}

/// The filter chips over the list (one row, in order). Claude/Codex and
/// the Macs are independent toggles; all on (or all off) means no filter.
public struct HistoryChip: Equatable, Sendable, Identifiable {
    public enum Kind: Equatable, Sendable, Hashable {
        case thisProject, allProjects, kind(String), machine(String), time, live, external, archived, moved
    }
    public var kind: Kind
    public var title: String
    public var on: Bool
    public var id: String {
        switch kind {
        case .thisProject: return "this"
        case .allProjects: return "all"
        case .kind(let k): return "kind:\(k)"
        case .machine(let m): return "mac:\(m)"
        case .time: return "time"
        case .live: return "live"
        case .external: return "external"
        case .archived: return "archived"
        case .moved: return "moved"
        }
    }
}

public enum HistoryChips {
    /// - Parameter context: the project "this project" means (the wall's).
    public static func make(_ q: HistoryQuery, context: String?, contextName: String?, machines: [(short: String, name: String)]) -> [HistoryChip] {
        var out: [HistoryChip] = []
        if let context {
            out.append(HistoryChip(kind: .thisProject, title: contextName ?? "this project", on: q.scope == .project(context)))
        }
        out.append(HistoryChip(kind: .allProjects, title: "all projects", on: q.scope == .all))
        for (k, t) in [("claude", "Claude"), ("codex", "Codex")] {
            out.append(HistoryChip(kind: .kind(k), title: t, on: q.kinds.isEmpty || q.kinds.contains(k)))
        }
        for m in machines {
            out.append(HistoryChip(kind: .machine(m.short), title: m.name, on: q.machines.contains(m.short)))
        }
        out.append(HistoryChip(kind: .time, title: q.time.label, on: q.time != .any))
        out.append(HistoryChip(kind: .live, title: "live in Hesper", on: q.liveOnly))
        out.append(HistoryChip(kind: .external, title: "external", on: q.externalOnly))
        out.append(HistoryChip(kind: .archived, title: "archived", on: q.archived))
        out.append(HistoryChip(kind: .moved, title: "moved to another Mac", on: q.moved))
        return out
    }

    /// A click on a chip.
    public static func toggle(_ chip: HistoryChip.Kind, in q: HistoryQuery, context: String?, allKinds: [String] = ["claude", "codex"]) -> HistoryQuery {
        var q = q
        switch chip {
        case .thisProject:
            if let context { q.scope = q.scope == .project(context) ? .all : .project(context) }
        case .allProjects:
            q.scope = .all
        case .kind(let k):
            // Shown on = no filter or listed. Turning one off from "all"
            // keeps the others; turning the last one off means all again.
            var shown = q.kinds.isEmpty ? Set(allKinds) : q.kinds
            if shown.contains(k) { shown.remove(k) } else { shown.insert(k) }
            q.kinds = shown.isEmpty || shown == Set(allKinds) ? [] : shown
        case .machine(let m):
            if q.machines.contains(m) { q.machines.remove(m) } else { q.machines.insert(m) }
        case .time:
            q.time = q.time.next
        case .live:
            q.liveOnly.toggle()
        case .external:
            q.externalOnly.toggle()
        case .archived:
            q.archived.toggle()
        case .moved:
            q.moved.toggle()
        }
        return q
    }
}

// MARK: The list

/// The panel's rows: pages appended by cursor, `sessions.changed` merged
/// in place (never reordering under the user while they read).
public struct HistoryList: Equatable, Sendable {
    public private(set) var items: [Session] = []
    public private(set) var cursor: String?
    /// The first page arrived (else: loading).
    public private(set) var loaded = false
    private var index: [String: Int] = [:]

    public init() {}

    public var hasMore: Bool { cursor != nil }

    public mutating func replace(with page: SessionPage) {
        items = []
        index = [:]
        loaded = true
        append(page)
    }

    /// Appends a page (drops ids already shown: a session that moved up
    /// between pages stays where it is).
    @discardableResult
    public mutating func append(_ page: SessionPage) -> Range<Int> {
        let start = items.count
        for s in page.items where index[s.id] == nil {
            index[s.id] = items.count
            items.append(s)
        }
        cursor = page.cursor
        return start..<items.count
    }

    public enum Change: Equatable, Sendable { case none, updated(Int), removed(Int), inserted(Int) }

    /// A `sessions.changed`: updated in place; dropped when it no longer
    /// matches; a new matching session (no text query) goes on top.
    @discardableResult
    public mutating func apply(_ s: Session, query: HistoryQuery, now: Date = Date()) -> Change {
        let matches = query.matches(s, now: now)
        if let i = index[s.id] {
            if matches {
                var n = s
                if n.snippet == nil { n.snippet = items[i].snippet }
                if items[i] == n { return .none }
                items[i] = n
                return .updated(i)
            }
            remove(at: i)
            return .removed(i)
        }
        guard matches, query.trimmedText.isEmpty, loaded else { return .none }
        items.insert(s, at: 0)
        reindex()
        return .inserted(0)
    }

    public struct Merge: Equatable, Sendable {
        /// Rows updated in place (no row moved).
        public var updated: [Int] = []
        /// Rows came or went: the list must be re-read.
        public var structural = false
    }

    /// A coalesced burst of `sessions.changed` / `sessions.removed`, merged
    /// in one pass (one reindex, however many): updates in place, rows
    /// that stop matching or were deleted leave, and a matching session
    /// not shown goes on top only when it is newer than the top row (no
    /// text query): a busy daemon never grows the list under the reader.
    public mutating func merge(changed: [Session], removed: [String], query: HistoryQuery, now: Date = Date()) -> Merge {
        var m = Merge()
        var drop = Set<Int>()
        for id in removed { if let i = index[id] { drop.insert(i) } }
        var top: [Session] = []
        let newest = items.first?.lastActivity ?? .distantPast
        for s in changed {
            let matches = query.matches(s, now: now)
            if let i = index[s.id] {
                if matches {
                    var n = s
                    if n.snippet == nil { n.snippet = items[i].snippet }
                    if items[i] != n { items[i] = n; m.updated.append(i) }
                } else {
                    drop.insert(i)
                }
            } else if matches, loaded, query.trimmedText.isEmpty, (s.lastActivity ?? .distantPast) >= newest {
                top.append(s)
            }
        }
        guard !drop.isEmpty || !top.isEmpty else { return m }
        m.structural = true
        m.updated = []
        if !drop.isEmpty { items = items.enumerated().filter { !drop.contains($0.offset) }.map(\.element) }
        if !top.isEmpty { items.insert(contentsOf: top.sorted { ($0.lastActivity ?? .distantPast) > ($1.lastActivity ?? .distantPast) }, at: 0) }
        reindex()
        return m
    }

    /// An undone delete (or a failed one): back in its place by activity.
    @discardableResult
    public mutating func restore(_ s: Session, query: HistoryQuery, now: Date = Date()) -> Int? {
        guard index[s.id] == nil, query.matches(s, now: now) else { return nil }
        let t = s.lastActivity ?? .distantPast
        let at = items.firstIndex { ($0.lastActivity ?? .distantPast) < t } ?? items.count
        items.insert(s, at: at)
        reindex()
        return at
    }

    @discardableResult
    public mutating func remove(id: String) -> Int? {
        guard let i = index[id] else { return nil }
        remove(at: i)
        return i
    }

    private mutating func remove(at i: Int) {
        items.remove(at: i)
        reindex()
    }

    private mutating func reindex() {
        index = [:]
        for (i, s) in items.enumerated() { index[s.id] = i }
    }

    public func row(of id: String) -> Int? { index[id] }

    /// Fetch the next page once the user is within `ahead` rows of the end.
    public func needsMore(lastVisibleRow: Int, ahead: Int = 40) -> Bool {
        hasMore && lastVisibleRow >= items.count - ahead
    }
}

// MARK: Coalescing

/// Bursts of `sessions.changed` / `sessions.indexing` become at most
/// `maxPerSecond` UI updates: the first goes at once, the rest wait for
/// the next slot. Pure (the caller supplies the clock).
public struct UpdateCoalescer: Sendable {
    public let interval: TimeInterval
    private var lastFlush: TimeInterval = -.infinity
    private(set) public var pending = false

    public init(maxPerSecond: Double = 4) { interval = 1 / maxPerSecond }

    public enum Decision: Equatable, Sendable { case flushNow, scheduled(after: TimeInterval), alreadyScheduled }

    /// An event arrived at `now`.
    public mutating func event(at now: TimeInterval) -> Decision {
        if pending { return .alreadyScheduled }
        let wait = lastFlush + interval - now
        if wait <= 0 {
            lastFlush = now
            return .flushNow
        }
        pending = true
        return .scheduled(after: wait)
    }

    /// The scheduled flush ran.
    public mutating func flushed(at now: TimeInterval) {
        pending = false
        lastFlush = now
    }
}

// MARK: Ghost cards

/// Agents removed in the last day stay on their band's shelf as dashed
/// cards (⏎ resume, ⌫ forget): the newest `perProject` per project.
public enum GhostCards {
    public static let window: TimeInterval = 24 * 3600
    public static let perProject = 3

    /// The wall item id of a ghost card.
    public static func itemID(_ s: Session) -> String { "ghost:" + (s.sessionId.isEmpty ? s.id : s.sessionId) }

    public static func visible(_ sessions: [Session], now: Date = Date(), dismissed: Set<String> = [], liveAgents: Set<String> = [],
                               perProject: Int = GhostCards.perProject) -> [Session] {
        var seen: Set<String> = []
        var count: [String: Int] = [:]
        var out: [Session] = []
        let sorted = sessions.sorted { ($0.removedAt ?? .distantPast) > ($1.removedAt ?? .distantPast) }
        for s in sorted {
            guard let r = s.removedAt, now.timeIntervalSince(r) < window, now.timeIntervalSince(r) >= -60 else { continue }
            let key = itemID(s)
            guard !seen.contains(key), !dismissed.contains(key), !dismissed.contains(s.id) else { continue }
            // Resumed (live again): not a ghost anymore.
            if s.isLive || (s.liveAgentID.map(liveAgents.contains) ?? false) { continue }
            let p = s.projectId ?? "~none"
            guard count[p, default: 0] < perProject else { continue }
            count[p, default: 0] += 1
            seen.insert(key)
            out.append(s)
        }
        return out
    }

    /// When the next ghost expires (to drop it then), nil: none.
    public static func nextExpiry(_ ghosts: [Session]) -> Date? {
        ghosts.compactMap { $0.removedAt?.addingTimeInterval(window) }.min()
    }

    /// A ghost for an agent being removed (shown at once, in the agent's
    /// place, before hesperd's `sessions.changed` arrives).
    public static func pending(from a: Agent, removedAt: Date = Date()) -> Session {
        Session(id: a.sessionId.map { "pending:" + $0 } ?? "pending:" + a.id, kind: a.kind, sessionId: a.sessionId ?? "", machine: a.machine,
                cwd: a.worktree ?? a.project ?? "", projectId: a.projectId, branch: a.branch, title: a.name,
                firstPrompt: a.task ?? "", lastAssistant: a.summary ?? "", lastActivity: a.stateSince, removedAt: removedAt)
    }
}
