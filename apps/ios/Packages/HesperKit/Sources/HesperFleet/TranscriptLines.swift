import HesperCore

/// One row of the transcript view: an item, and for a tool call its
/// result, which the view folds under the call instead of showing it as a
/// row of its own.
public struct TranscriptLine: Identifiable, Equatable, Sendable {
    public let item: TranscriptItem
    /// A tool call's result, once it arrived.
    public let result: TranscriptItem?

    public var id: Int64 { item.seq }
}

extension TranscriptLog {
    /// The items as rows. A result joins the tool call it names (`of`); a
    /// result whose call is on a page not loaded yet stays a row, so
    /// nothing the Mac sent is hidden.
    public var lines: [TranscriptLine] {
        var results: [String: TranscriptItem] = [:]
        var calls: Set<String> = []
        for item in items {
            if item.kind == .result, let of = item.of { results[of] = item }
            if item.kind == .tool, let id = item.toolID { calls.insert(id) }
        }
        return items.compactMap { item in
            if item.kind == .result, let of = item.of, calls.contains(of) { return nil }
            let result = item.kind == .tool ? item.toolID.flatMap { results[$0] } : nil
            return TranscriptLine(item: item, result: result)
        }
    }
}
