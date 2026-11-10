import Foundation

/// One parsed line of an agent's transcript (`agents.transcript`): what
/// the person typed, the agent's reply, a tool call, its result, or a
/// note such as "Context compacted".
public struct TranscriptItem: Codable, Hashable, Sendable, Identifiable {
    public enum Kind: String, Codable, Sendable {
        case user, assistant, tool, result, system
    }

    /// The byte offset of the item's line in the transcript file plus the
    /// item's index within the line: unique per item, monotonic and
    /// stable, so pages and the stream continue from it. Negative for a
    /// marker ("Transcript restarted"), which is not a line.
    public var seq: Int64
    /// The line's time (Unix ms on the wire); nil when it had none.
    public var at: Date?
    public var kind: Kind
    public var text: String?
    /// A tool call's name ("Read", "Bash").
    public var tool: String?
    /// A tool call's one short line: a file name, a command, a pattern.
    public var summary: String?
    /// A tool call's id (`id` on the wire); its result names it in `of`.
    public var toolID: String?
    /// A result's tool call.
    public var of: String?
    /// Text clipped by the Mac: user and assistant previews (4 KiB),
    /// or tool results (60 lines or 4 KiB).
    public var truncated: Bool
    /// A result's outcome: false when the tool reported an error.
    public var ok: Bool?

    public var id: Int64 { seq }

    public init(
        seq: Int64, at: Date? = nil, kind: Kind, text: String? = nil, tool: String? = nil, summary: String? = nil,
        toolID: String? = nil, of: String? = nil, truncated: Bool = false, ok: Bool? = nil
    ) {
        self.seq = seq
        self.at = at
        self.kind = kind
        self.text = text
        self.tool = tool
        self.summary = summary
        self.toolID = toolID
        self.of = of
        self.truncated = truncated
        self.ok = ok
    }

    enum CodingKeys: String, CodingKey {
        case seq, at, kind, text, tool, summary
        case toolID = "id"
        case of, truncated, ok
    }

    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        seq = try c.decode(Int64.self, forKey: .seq)
        at = try c.decodeIfPresent(Int64.self, forKey: .at).map { Date(timeIntervalSince1970: Double($0) / 1000) }
        // A kind from a newer daemon shows as a note rather than breaking
        // the list.
        kind = Kind(rawValue: try c.decode(String.self, forKey: .kind)) ?? .system
        text = try c.decodeIfPresent(String.self, forKey: .text)
        tool = try c.decodeIfPresent(String.self, forKey: .tool)
        summary = try c.decodeIfPresent(String.self, forKey: .summary)
        toolID = try c.decodeIfPresent(String.self, forKey: .toolID)
        of = try c.decodeIfPresent(String.self, forKey: .of)
        truncated = try c.decodeIfPresent(Bool.self, forKey: .truncated) ?? false
        ok = try c.decodeIfPresent(Bool.self, forKey: .ok)
    }

    public func encode(to encoder: any Encoder) throws {
        var c = encoder.container(keyedBy: CodingKeys.self)
        try c.encode(seq, forKey: .seq)
        try c.encodeIfPresent(at.map { Int64(($0.timeIntervalSince1970 * 1000).rounded()) }, forKey: .at)
        try c.encode(kind, forKey: .kind)
        try c.encodeIfPresent(text, forKey: .text)
        try c.encodeIfPresent(tool, forKey: .tool)
        try c.encodeIfPresent(summary, forKey: .summary)
        try c.encodeIfPresent(toolID, forKey: .toolID)
        try c.encodeIfPresent(of, forKey: .of)
        if truncated { try c.encode(truncated, forKey: .truncated) }
        try c.encodeIfPresent(ok, forKey: .ok)
    }
}

/// A page of transcript items, oldest first; `more` when older ones are
/// left.
public struct TranscriptPage: Codable, Sendable {
    public var items: [TranscriptItem]
    public var more: Bool

    public init(items: [TranscriptItem], more: Bool) {
        self.items = items
        self.more = more
    }
}

/// One bounded part of a user or assistant message. `next` is a UTF-8
/// byte offset in the full message, not a character count.
public struct TranscriptTextChunk: Codable, Equatable, Sendable {
    public var text: String
    public var next: Int
    public var more: Bool

    public init(text: String, next: Int, more: Bool) {
        self.text = text
        self.next = next
        self.more = more
    }
}

/// An agent's terminal as plain text (the `agents.screen` reply): the
/// screen and up to `scrollback` rows above it, for shell agents' history.
public struct ScreenText: Codable, Sendable {
    public var text: String
    public var rows: Int
    public var cols: Int
    /// The terminal shows its alternate screen (a full-screen program).
    public var alt: Bool

    public init(text: String, rows: Int, cols: Int, alt: Bool) {
        self.text = text
        self.rows = rows
        self.cols = cols
        self.alt = alt
    }

    enum CodingKeys: String, CodingKey { case text, rows, cols, alt }

    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        text = try c.decode(String.self, forKey: .text)
        rows = try c.decode(Int.self, forKey: .rows)
        cols = try c.decode(Int.self, forKey: .cols)
        // hesperd omits `alt` when it is false (`omitempty`).
        alt = try c.decodeIfPresent(Bool.self, forKey: .alt) ?? false
    }
}

/// Where a watched terminal's view is scrolled (the scroll frame, type
/// 4): rows above the bottom, the most it can go, and rows printed below
/// since it left the bottom.
public struct ScrollState: Equatable, Sendable {
    public var offset: Int
    public var max: Int
    public var new: Int

    public init(offset: Int, max: Int, new: Int) {
        self.offset = offset
        self.max = max
        self.new = new
    }
}
