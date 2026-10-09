import Foundation

// Review (⌘R, docs/review/concept.md): finished agents' work on every Mac,
// its diff in reading order, the evidence the agent left and where each
// hunk came from. The wire types (`review.*`) are decoded by hand from
// JSONValue and lenient, like shared history's: a missing field gets its
// safest value (unknown risk is medium, unknown evidence is missing), so
// nothing becomes bulk-acceptable by accident.

// MARK: Wire names

public enum ReviewRPC {
    public static let list = "review.list"
    public static let diff = "review.diff"
    public static let accept = "review.accept"
    public static let reject = "review.reject"
    public static let sendBack = "review.sendBack"
    public static let evidence = "review.evidence"
    public static let provenance = "review.provenance"
    /// Notification `{id}`: an agent became ready for review, or its
    /// review state changed.
    public static let changed = "review.changed"

    /// The method is missing on that Mac (-32601 here, "Unknown method"
    /// through the relay): no review there.
    public static func missing(_ e: RPCError) -> Bool { CloseRPC.missing(e) }
}

// MARK: Values

public enum ReviewRisk: String, Sendable, Comparable, CaseIterable {
    case low, medium, high

    /// Unknown values are medium: never low by accident.
    public init(wire: String?) { self = wire.flatMap(ReviewRisk.init(rawValue:)) ?? .medium }

    var rank: Int {
        switch self {
        case .low: return 0
        case .medium: return 1
        case .high: return 2
        }
    }

    public static func < (l: ReviewRisk, r: ReviewRisk) -> Bool { l.rank < r.rank }
}

/// Whether a test or build proves the latest edit: fresh (one passed
/// after it), stale (edits since), missing (none ran), none (no changes).
public enum EvidenceFreshness: String, Sendable, CaseIterable {
    case fresh, stale, missing, none

    /// Unknown values are missing: never fresh by accident.
    public init(wire: String?) { self = wire.flatMap(EvidenceFreshness.init(rawValue:)) ?? .missing }
}

public enum ReviewFileStatus: String, Sendable {
    case added = "A", modified = "M", deleted = "D", renamed = "R"

    public init(wire: String?) { self = wire.flatMap(ReviewFileStatus.init(rawValue:)) ?? .modified }
}

public enum ReviewLineKind: String, Sendable {
    case context = " ", added = "+", removed = "-"

    public init(wire: String?) { self = wire.flatMap(ReviewLineKind.init(rawValue:)) ?? .context }
}

public enum ReviewSide: String, Sendable, Hashable {
    case old, new
}

// MARK: review.list

/// An agent with changes ready for review (`review.list`).
public struct ReviewItem: Equatable, Sendable, Identifiable {
    /// The agent's id.
    public var id: String
    public var machine: String
    public var name: String
    public var kind: String
    public var project: String?
    public var branch: String?
    public var worktree: String?
    public var state: AgentState
    public var files: Int
    public var added: Int
    public var removed: Int
    public var risk: ReviewRisk
    public var riskNotes: [String]
    public var evidence: EvidenceFreshness
    public var readyAt: Date?
    /// The point the last send-back marked (stored for the interdiff).
    public var reviewedAt: String?

    public init(id: String, machine: String = "", name: String = "", kind: String = "claude", project: String? = nil, branch: String? = nil,
                worktree: String? = nil, state: AgentState = .done, files: Int = 0, added: Int = 0, removed: Int = 0, risk: ReviewRisk = .medium,
                riskNotes: [String] = [], evidence: EvidenceFreshness = .missing, readyAt: Date? = nil, reviewedAt: String? = nil) {
        self.id = id
        self.machine = machine
        self.name = name.isEmpty ? id : name
        self.kind = kind
        self.project = project
        self.branch = branch
        self.worktree = worktree
        self.state = state
        self.files = files
        self.added = added
        self.removed = removed
        self.risk = risk
        self.riskNotes = riskNotes
        self.evidence = evidence
        self.readyAt = readyAt
        self.reviewedAt = reviewedAt
    }

    public init?(json v: JSONValue) {
        guard case .object(let o) = v, let id = o["id"].flatMap(Session.str), !id.isEmpty else { return nil }
        let machine = o["machine"].flatMap(Session.str) ?? String(id.split(separator: "/").first ?? "")
        self.init(id: id, machine: machine, name: o["name"].flatMap(Session.str) ?? "", kind: o["kind"].flatMap(Session.str) ?? "claude",
                  project: Self.nonEmpty(o["project"]), branch: Self.nonEmpty(o["branch"]), worktree: Self.nonEmpty(o["worktree"]),
                  state: AgentState(rawValue: o["state"]?.stringValue ?? "") ?? .done,
                  files: o["files"].flatMap(Session.int) ?? 0, added: o["added"].flatMap(Session.int) ?? 0,
                  removed: o["removed"].flatMap(Session.int) ?? 0, risk: ReviewRisk(wire: o["risk"]?.stringValue),
                  riskNotes: (o["riskNotes"]?.arrayValue ?? []).compactMap(\.stringValue),
                  evidence: EvidenceFreshness(wire: o["evidence"]?.stringValue), readyAt: o["readyAt"].flatMap(Session.date),
                  reviewedAt: Self.nonEmpty(o["reviewedAt"]))
    }

    static func nonEmpty(_ v: JSONValue?) -> String? { v.flatMap(Session.str).flatMap { $0.isEmpty ? nil : $0 } }

    /// `review.list`'s answer (an array, or `{items}`).
    public static func list(json v: JSONValue) -> [ReviewItem] {
        (v.arrayValue ?? v["items"]?.arrayValue ?? []).compactMap(ReviewItem.init(json:))
    }
}

// MARK: review.diff

public struct ReviewLine: Equatable, Sendable {
    public var kind: ReviewLineKind
    public var text: String
    public var old: Int?
    public var new: Int?
    /// Changed words, as UTF-16 ranges into `text` (the wire's offsets
    /// are UTF-8 bytes; converted on decode, clamped to the text).
    public var words: [NSRange]

    public init(kind: ReviewLineKind, text: String, old: Int? = nil, new: Int? = nil, words: [NSRange] = []) {
        self.kind = kind
        self.text = text
        self.old = old
        self.new = new
        self.words = words
    }

    init(json v: JSONValue) {
        let text = v["text"].flatMap(Session.str) ?? ""
        let pairs: [(Int, Int)] = (v["words"]?.arrayValue ?? []).compactMap { w in
            guard let a = w.arrayValue, a.count == 2, let s = Session.int(a[0]), let e = Session.int(a[1]) else { return nil }
            return (s, e)
        }
        self.init(kind: ReviewLineKind(wire: v["kind"]?.stringValue), text: text, old: v["old"].flatMap(Session.int),
                  new: v["new"].flatMap(Session.int), words: Self.utf16Ranges(text, utf8: pairs))
    }

    /// UTF-8 byte ranges → UTF-16 ranges, snapped back to the start of a
    /// character's bytes and clamped to the text.
    static func utf16Ranges(_ text: String, utf8 pairs: [(Int, Int)]) -> [NSRange] {
        guard !pairs.isEmpty else { return [] }
        let bytes = Array(text.utf8)
        func boundary(_ i: Int) -> Int {
            var i = max(0, min(i, bytes.count))
            while i > 0, i < bytes.count, bytes[i] & 0xC0 == 0x80 { i -= 1 }
            return i
        }
        func utf16(_ i: Int) -> Int { String(decoding: bytes[0..<i], as: UTF8.self).utf16.count }
        return pairs.compactMap { s, e in
            let lo = boundary(s), hi = boundary(e)
            guard hi > lo else { return nil }
            let a = utf16(lo)
            return NSRange(location: a, length: utf16(hi) - a)
        }
    }
}

public struct ReviewHunk: Equatable, Sendable, Identifiable {
    /// "<file-index>:<hunk-index>" (what review.accept / reject take).
    public var id: String
    public var oldStart: Int
    public var oldLines: Int
    public var newStart: Int
    public var newLines: Int
    public var formattingOnly: Bool
    public var moved: Bool
    public var lines: [ReviewLine]

    public init(id: String, oldStart: Int = 0, oldLines: Int = 0, newStart: Int = 0, newLines: Int = 0, formattingOnly: Bool = false,
                moved: Bool = false, lines: [ReviewLine] = []) {
        self.id = id
        self.oldStart = oldStart
        self.oldLines = oldLines
        self.newStart = newStart
        self.newLines = newLines
        self.formattingOnly = formattingOnly
        self.moved = moved
        self.lines = lines
    }

    init(json v: JSONValue, fallbackID: String) {
        self.init(id: v["id"].flatMap(Session.str) ?? fallbackID, oldStart: v["oldStart"].flatMap(Session.int) ?? 0,
                  oldLines: v["oldLines"].flatMap(Session.int) ?? 0, newStart: v["newStart"].flatMap(Session.int) ?? 0,
                  newLines: v["newLines"].flatMap(Session.int) ?? 0, formattingOnly: v["formattingOnly"]?.boolValue ?? false,
                  moved: v["moved"]?.boolValue ?? false, lines: (v["lines"]?.arrayValue ?? []).map(ReviewLine.init(json:)))
    }

    /// "@@ -12,6 +12,8 @@".
    public var header: String { "@@ -\(oldStart),\(oldLines) +\(newStart),\(newLines) @@" }
    public var added: Int { lines.reduce(0) { $0 + ($1.kind == .added ? 1 : 0) } }
    public var removed: Int { lines.reduce(0) { $0 + ($1.kind == .removed ? 1 : 0) } }

    /// The file and the changed lines (not their numbers or position), so
    /// marks on a hunk survive other hunks being rejected. FNV-1a, 64 bit.
    public func fingerprint(path: String) -> String {
        var h: UInt64 = 0xcbf2_9ce4_8422_2325
        func mix(_ s: String) {
            for b in s.utf8 { h = (h ^ UInt64(b)) &* 0x0000_0100_0000_01b3 }
            h = (h ^ 0x0a) &* 0x0000_0100_0000_01b3
        }
        mix(path)
        for l in lines where l.kind != .context { mix(l.kind.rawValue + l.text) }
        return String(h, radix: 16)
    }
}

public struct ReviewFile: Equatable, Sendable {
    public var path: String
    public var oldPath: String?
    public var status: ReviewFileStatus
    public var binary: Bool
    public var formattingOnly: Bool
    public var generated: Bool
    /// The reading order hesperd chose (lower first).
    public var order: Int?
    public var risk: ReviewRisk
    public var hunks: [ReviewHunk]

    public init(path: String, oldPath: String? = nil, status: ReviewFileStatus = .modified, binary: Bool = false, formattingOnly: Bool = false,
                generated: Bool = false, order: Int? = nil, risk: ReviewRisk = .medium, hunks: [ReviewHunk] = []) {
        self.path = path
        self.oldPath = oldPath
        self.status = status
        self.binary = binary
        self.formattingOnly = formattingOnly
        self.generated = generated
        self.order = order
        self.risk = risk
        self.hunks = hunks
    }

    init?(json v: JSONValue, index: Int) {
        guard let path = v["path"].flatMap(Session.str), !path.isEmpty else { return nil }
        let hunks = (v["hunks"]?.arrayValue ?? []).enumerated().map { ReviewHunk(json: $1, fallbackID: "\(index):\($0)") }
        self.init(path: path, oldPath: ReviewItem.nonEmpty(v["oldPath"]), status: ReviewFileStatus(wire: v["status"]?.stringValue),
                  binary: v["binary"]?.boolValue ?? false, formattingOnly: v["formattingOnly"]?.boolValue ?? false,
                  generated: v["generated"]?.boolValue ?? false, order: v["order"].flatMap(Session.int),
                  risk: ReviewRisk(wire: v["risk"]?.stringValue), hunks: hunks)
    }

    public var added: Int { hunks.reduce(0) { $0 + $1.added } }
    public var removed: Int { hunks.reduce(0) { $0 + $1.removed } }
    /// Shown folded (one line) until opened: formatting-only, generated
    /// and binary files.
    public var foldedByDefault: Bool { formattingOnly || generated || binary }
}

public struct ReviewDiff: Equatable, Sendable {
    public var base: String
    public var head: String
    /// In reading order (`ReviewOrder.apply`).
    public var files: [ReviewFile]

    public init(base: String = "", head: String = "worktree", files: [ReviewFile] = []) {
        self.base = base
        self.head = head
        self.files = files
    }

    /// Decodes and puts the files in reading order.
    public init(json v: JSONValue) {
        let files = (v["files"]?.arrayValue ?? []).enumerated().compactMap { ReviewFile(json: $1, index: $0) }
        self.init(base: v["base"].flatMap(Session.str) ?? "", head: v["head"].flatMap(Session.str) ?? "worktree", files: ReviewOrder.apply(files))
    }

    public var hunkCount: Int { files.reduce(0) { $0 + $1.hunks.count } }
    public var lineCount: Int { files.reduce(0) { $0 + $1.hunks.reduce(0) { $0 + $1.lines.count } } }
    public var paths: [String] { files.flatMap { [$0.path] + ($0.oldPath.map { [$0] } ?? []) } }
}

// MARK: review.evidence / review.provenance

public enum EvidenceKind: String, Sendable {
    case test, build, lint, run, other
    public init(wire: String?) { self = wire.flatMap(EvidenceKind.init(rawValue:)) ?? .other }
    /// Proves an edit (counts for freshness).
    public var proves: Bool { self == .test || self == .build }
}

public struct EvidenceCommand: Equatable, Sendable {
    public var command: String
    public var kind: EvidenceKind
    public var exitCode: Int?
    public var startedAt: Date?
    public var endedAt: Date?

    public init(command: String, kind: EvidenceKind = .other, exitCode: Int? = nil, startedAt: Date? = nil, endedAt: Date? = nil) {
        self.command = command
        self.kind = kind
        self.exitCode = exitCode
        self.startedAt = startedAt
        self.endedAt = endedAt
    }

    public var passed: Bool { exitCode == 0 }
    public var failed: Bool { (exitCode ?? 0) != 0 }
    public var running: Bool { endedAt == nil && exitCode == nil }
}

public struct EvidenceAttachment: Equatable, Sendable {
    public enum Kind: String, Sendable { case image, video, log }
    public var path: String
    public var kind: Kind

    public init(path: String, kind: Kind) {
        self.path = path
        self.kind = kind
    }
}

public struct ReviewEvidence: Equatable, Sendable {
    public var freshness: EvidenceFreshness
    public var lastEditAt: Date?
    public var commands: [EvidenceCommand]
    public var attachments: [EvidenceAttachment]

    public init(freshness: EvidenceFreshness = .missing, lastEditAt: Date? = nil, commands: [EvidenceCommand] = [], attachments: [EvidenceAttachment] = []) {
        self.freshness = freshness
        self.lastEditAt = lastEditAt
        self.commands = commands
        self.attachments = attachments
    }

    public init(json v: JSONValue) {
        let commands: [EvidenceCommand] = (v["commands"]?.arrayValue ?? []).compactMap { c in
            guard let cmd = c["command"].flatMap(Session.str), !cmd.isEmpty else { return nil }
            return EvidenceCommand(command: cmd, kind: EvidenceKind(wire: c["kind"]?.stringValue), exitCode: c["exitCode"].flatMap(Session.int),
                                   startedAt: c["startedAt"].flatMap(Session.date), endedAt: c["endedAt"].flatMap(Session.date))
        }
        let attachments: [EvidenceAttachment] = (v["attachments"]?.arrayValue ?? []).compactMap { a in
            guard let p = a["path"].flatMap(Session.str), !p.isEmpty else { return nil }
            return EvidenceAttachment(path: p, kind: EvidenceAttachment.Kind(rawValue: a["kind"]?.stringValue ?? "") ?? .log)
        }
        self.init(freshness: EvidenceFreshness(wire: v["freshness"]?.stringValue), lastEditAt: v["lastEditAt"].flatMap(Session.date),
                  commands: commands, attachments: attachments)
    }
}

/// The turn and tool call that last wrote a line (`review.provenance`).
public struct ReviewProvenance: Equatable, Sendable {
    public var sessionId: String?
    public var turn: Int?
    public var tool: String?
    public var prompt: String?
    public var at: Date?

    public init(sessionId: String? = nil, turn: Int? = nil, tool: String? = nil, prompt: String? = nil, at: Date? = nil) {
        self.sessionId = sessionId
        self.turn = turn
        self.tool = tool
        self.prompt = prompt
        self.at = at
    }

    public init(json v: JSONValue) {
        self.init(sessionId: ReviewItem.nonEmpty(v["sessionId"]), turn: v["turn"].flatMap(Session.int), tool: ReviewItem.nonEmpty(v["tool"]),
                  prompt: ReviewItem.nonEmpty(v["prompt"]), at: v["at"].flatMap(Session.date))
    }

    public var isEmpty: Bool { sessionId == nil && turn == nil && tool == nil && prompt == nil }
}

// MARK: review.sendBack notes

/// Where a note sits: a file, a line on one side (nil: the whole file).
public struct ReviewNoteAnchor: Hashable, Sendable {
    public var path: String
    public var line: Int?
    public var side: ReviewSide

    public init(path: String, line: Int? = nil, side: ReviewSide = .new) {
        self.path = path
        self.line = line
        self.side = side
    }
}

public struct ReviewNote: Equatable, Sendable {
    public var anchor: ReviewNoteAnchor
    public var text: String

    public init(anchor: ReviewNoteAnchor, text: String) {
        self.anchor = anchor
        self.text = text
    }

    var param: JSONValue {
        var o: [String: JSONValue] = ["path": .string(anchor.path), "text": .string(text), "side": .string(anchor.side.rawValue)]
        if let l = anchor.line { o["line"] = .number(Double(l)) }
        return .object(o)
    }
}

// MARK: Calls

// Every call decodes on the calling task (callers run them off the main
// actor). -32601 / "Unknown method": `ReviewRPC.missing`.
extension DaemonClient {
    public func reviewList() async throws -> [ReviewItem] { ReviewItem.list(json: try await call(ReviewRPC.list, .object([:]), timeout: 30)) }

    public func reviewDiff(_ id: String, context: Int = 3) async throws -> ReviewDiff {
        ReviewDiff(json: try await call(ReviewRPC.diff, ["id": .string(id), "context": .number(Double(context))], timeout: 60))
    }

    /// Commits the accepted hunks (nil: all) in the agent's folder; returns the commit.
    @discardableResult
    public func reviewAccept(_ id: String, hunks: [String]? = nil, message: String? = nil) async throws -> String? {
        var p: [String: JSONValue] = ["id": .string(id)]
        if let hunks { p["hunks"] = .array(hunks.map(JSONValue.string)) }
        if let message, !message.isEmpty { p["message"] = .string(message) }
        return try await call(ReviewRPC.accept, .object(p), timeout: 60)["commit"]?.stringValue
    }

    public func reviewReject(_ id: String, hunks: [String]) async throws {
        _ = try await call(ReviewRPC.reject, ["id": .string(id), "hunks": .array(hunks.map(JSONValue.string))], timeout: 60)
    }

    public func reviewSendBack(_ id: String, notes: [ReviewNote], message: String? = nil) async throws {
        var p: [String: JSONValue] = ["id": .string(id), "notes": .array(notes.map(\.param))]
        if let message, !message.isEmpty { p["message"] = .string(message) }
        _ = try await call(ReviewRPC.sendBack, .object(p), timeout: 30)
    }

    public func reviewEvidence(_ id: String) async throws -> ReviewEvidence {
        ReviewEvidence(json: try await call(ReviewRPC.evidence, ["id": .string(id)], timeout: 30))
    }

    public func reviewProvenance(_ id: String, path: String, line: Int) async throws -> ReviewProvenance {
        ReviewProvenance(json: try await call(ReviewRPC.provenance, ["id": .string(id), "path": .string(path), "line": .number(Double(line))], timeout: 30))
    }
}
