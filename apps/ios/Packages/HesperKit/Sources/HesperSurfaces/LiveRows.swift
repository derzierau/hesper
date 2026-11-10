import Foundation

/// Which rows of an agent's live screen the phone shows. While the agent
/// works, all of them; otherwise only the bottom of the screen, where a
/// coding agent keeps its prompt box and status line, so the transcript
/// above stays readable.
public enum LiveRows {
    public static let maxIdle = 12, fallback = 8
    /// How long the view waits after work stops before it shrinks to the
    /// idle rows, so a short pause does not make it jump.
    public static let shrinkDelay: Duration = .milliseconds(500)

    public struct Partition: Equatable, Sendable {
        public let scrolling: [Int]
        public let input: Int?
    }

    /// Keep the terminal's active cursor row outside the scrolling rows.
    /// With no valid typing cursor, all visible rows stay in the scroll.
    public static func partition(rowTexts: [String], cursorRow: Int?, working: Bool, pinInput: Bool) -> Partition {
        let scrolling = Array(visible(rowTexts: rowTexts, cursorRow: cursorRow, working: working))
        guard pinInput, let cursorRow, rowTexts.indices.contains(cursorRow) else {
            return Partition(scrolling: scrolling, input: nil)
        }
        return Partition(scrolling: scrolling.filter { $0 != cursorRow }, input: cursorRow)
    }

    /// The rows of `rowTexts` to show. Working: all. Otherwise from the row
    /// below the first blank row above the cursor to the end, keeping the
    /// cursor row and at most `maxIdle` rows (the last ones). No cursor, a
    /// cursor off the grid, no blank row above it, or a cursor more than
    /// `maxIdle` rows from the end → the last `fallback` rows.
    public static func visible(rowTexts: [String], cursorRow: Int?, working: Bool) -> Range<Int> {
        let n = rowTexts.count
        guard !working else { return 0..<n }
        let last = max(n - fallback, 0)..<n
        guard let cursor = cursorRow, rowTexts.indices.contains(cursor) else { return last }
        // The cursor row is the prompt itself, so the walk starts above it.
        guard let blank = rowTexts[..<cursor].lastIndex(where: isBlank) else { return last }
        let start = max(blank + 1, n - maxIdle)
        return start <= cursor ? start..<n : last
    }

    private static func isBlank(_ s: String) -> Bool {
        s.allSatisfy(\.isWhitespace)
    }
}
