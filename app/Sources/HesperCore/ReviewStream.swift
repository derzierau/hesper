import Foundation

// The review sheet's diff body as one stream of rows over every file (a
// multibuffer): file headers, hunk headers, code lines, notes and the
// line under each hunk (provenance when focused). Code rows have one
// fixed height; notes and the hunk's last row are the only variable ones,
// so the table measures only those. Plus the sheet's keys, the hunk
// cursor, what the reviewer has seen, and the notes sent back.

// MARK: Rows

public enum ReviewRow: Hashable, Sendable {
    /// A file's header: path, status, churn, risk.
    case file(Int)
    /// A folded file's one line (formatting-only, generated, binary).
    case folded(Int)
    /// A hunk's header ("@@ -12,6 +12,8 @@").
    case hunk(Int, Int)
    /// A code line: file, hunk, line.
    case line(Int, Int, Int)
    /// A note under its line (or under the file header).
    case note(ReviewNoteAnchor)
    /// Under a hunk's last line: its provenance while focused, else a gap.
    case hunkEnd(Int, Int)

    /// Code geometry: every row but notes and hunk ends has one height.
    public var fixedHeight: Bool {
        switch self {
        case .note, .hunkEnd: return false
        default: return true
        }
    }
}

public struct HunkRef: Equatable, Hashable, Sendable {
    public var file: Int
    public var hunk: Int
    /// What review.accept / reject take (a position: it shifts when
    /// another hunk goes).
    public var id: String
    /// What the reviewer's marks hang on (`ReviewHunk.fingerprint`):
    /// stays when other hunks are rejected and the diff reloads.
    public var key: String
    public init(file: Int, hunk: Int, id: String, key: String? = nil) {
        self.file = file
        self.hunk = hunk
        self.id = id
        self.key = key ?? id
    }
}

public struct ReviewStream: Sendable {
    public let files: [ReviewFile]
    public let rows: [ReviewRow]
    /// Every hunk in reading order (folded files' too: the cursor opens them).
    public let hunks: [HunkRef]
    /// Row → the hunk ordinal it belongs to (-1: a file row).
    private let rowHunk: [Int32]
    /// Row → its file.
    private let rowFile: [Int32]
    private let hunkHeaderRow: [String: Int]
    private let fileHeaderRow: [Int]
    private let noteRow: [ReviewNoteAnchor: Int]

    /// - Parameters:
    ///   - files: in reading order.
    ///   - notes: anchors with a note.
    ///   - unfolded: paths of folded-by-default files the reviewer opened.
    public init(files: [ReviewFile], notes: Set<ReviewNoteAnchor> = [], unfolded: Set<String> = []) {
        self.files = files
        var rows: [ReviewRow] = []
        var rowHunk: [Int32] = []
        var rowFile: [Int32] = []
        var hunks: [HunkRef] = []
        var hunkHeaderRow: [String: Int] = [:]
        var fileHeaderRow: [Int] = []
        var noteRow: [ReviewNoteAnchor: Int] = [:]
        rows.reserveCapacity(files.reduce(0) { $0 + 2 + $1.hunks.reduce(0) { $0 + $1.lines.count + 2 } })

        func add(_ r: ReviewRow, _ hunk: Int32, _ file: Int) {
            rows.append(r)
            rowHunk.append(hunk)
            rowFile.append(Int32(file))
        }
        let byPath = Dictionary(grouping: notes, by: \.path)
        for (fi, f) in files.enumerated() {
            fileHeaderRow.append(rows.count)
            add(.file(fi), -1, fi)
            var fileNotes = byPath[f.path] ?? []
            if let o = f.oldPath { fileNotes += byPath[o] ?? [] }
            // Notes on lines this diff shows go under them; the rest under the header.
            var placed = Set<ReviewNoteAnchor>()
            var lineNotes: [String: [ReviewNoteAnchor]] = [:] // "side:line"
            for a in fileNotes {
                if let l = a.line { lineNotes["\(a.side.rawValue):\(l)", default: []].append(a) }
            }
            let shown = Self.shownLines(f)
            for a in fileNotes.sorted(by: Self.noteOrder) where a.line == nil || !shown.contains("\(a.side.rawValue):\(a.line!)") {
                noteRow[a] = rows.count
                add(.note(a), -1, fi)
                placed.insert(a)
            }
            let folded = f.foldedByDefault && !unfolded.contains(f.path)
            var seenPrints: [String: Int] = [:] // the same change twice in a file: "#2"
            for (hi, h) in f.hunks.enumerated() {
                let ordinal = Int32(hunks.count)
                let print = h.fingerprint(path: f.path)
                let n = seenPrints[print, default: 0]
                seenPrints[print] = n + 1
                hunks.append(HunkRef(file: fi, hunk: hi, id: h.id, key: n == 0 ? print : "\(print)#\(n + 1)"))
                guard !folded else { continue }
                hunkHeaderRow[h.id] = rows.count
                add(.hunk(fi, hi), ordinal, fi)
                for (li, l) in h.lines.enumerated() {
                    add(.line(fi, hi, li), ordinal, fi)
                    for key in Self.keys(l) {
                        for a in (lineNotes[key] ?? []).sorted(by: Self.noteOrder) where !placed.contains(a) {
                            noteRow[a] = rows.count
                            add(.note(a), ordinal, fi)
                            placed.insert(a)
                        }
                    }
                }
                add(.hunkEnd(fi, hi), ordinal, fi)
            }
            if folded {
                add(.folded(fi), -1, fi)
            }
        }
        self.rows = rows
        self.rowHunk = rowHunk
        self.rowFile = rowFile
        self.hunks = hunks
        self.hunkHeaderRow = hunkHeaderRow
        self.fileHeaderRow = fileHeaderRow
        self.noteRow = noteRow
    }

    /// "new:42" / "old:40" for each side the line has.
    static func keys(_ l: ReviewLine) -> [String] {
        var k: [String] = []
        if l.kind != .removed, let n = l.new { k.append("new:\(n)") }
        if l.kind != .added, let o = l.old { k.append("old:\(o)") }
        return k
    }

    static func shownLines(_ f: ReviewFile) -> Set<String> {
        var s = Set<String>()
        for h in f.hunks { for l in h.lines { s.formUnion(keys(l)) } }
        return s
    }

    static func noteOrder(_ a: ReviewNoteAnchor, _ b: ReviewNoteAnchor) -> Bool {
        if (a.line ?? -1) != (b.line ?? -1) { return (a.line ?? -1) < (b.line ?? -1) }
        return a.side == .old && b.side == .new
    }

    public var isEmpty: Bool { files.isEmpty }

    public func hunkOrdinal(atRow row: Int) -> Int? {
        guard rows.indices.contains(row), rowHunk[row] >= 0 else { return nil }
        return Int(rowHunk[row])
    }

    public func fileIndex(atRow row: Int) -> Int? {
        guard rows.indices.contains(row) else { return nil }
        return Int(rowFile[row])
    }

    /// The header row of a hunk (nil: its file is folded).
    public func headerRow(ofHunk id: String) -> Int? { hunkHeaderRow[id] }
    public func endRow(ofHunk ref: HunkRef) -> Int? {
        guard let h = hunkHeaderRow[ref.id] else { return nil }
        return h + files[ref.file].hunks[ref.hunk].lines.count + 1
    }
    public func headerRow(ofFile i: Int) -> Int? { fileHeaderRow.indices.contains(i) ? fileHeaderRow[i] : nil }
    public func row(ofNote a: ReviewNoteAnchor) -> Int? { noteRow[a] }
    public func ordinal(ofHunk id: String) -> Int? { hunks.firstIndex { $0.id == id } }
    public func ordinal(ofKey key: String) -> Int? { hunks.firstIndex { $0.key == key } }

    public func hunk(_ ref: HunkRef) -> ReviewHunk { files[ref.file].hunks[ref.hunk] }

    /// Where a note on this code row anchors: the new side for context
    /// and added lines, the old side for removed ones.
    public func anchor(atRow row: Int) -> ReviewNoteAnchor? {
        guard rows.indices.contains(row) else { return nil }
        switch rows[row] {
        case .line(let fi, let hi, let li):
            let f = files[fi], l = f.hunks[hi].lines[li]
            if l.kind == .removed { return ReviewNoteAnchor(path: f.oldPath ?? f.path, line: l.old, side: .old) }
            return ReviewNoteAnchor(path: f.path, line: l.new, side: .new)
        case .file(let fi), .folded(let fi):
            return ReviewNoteAnchor(path: files[fi].path, line: nil, side: .new)
        case .hunk(let fi, let hi), .hunkEnd(let fi, let hi):
            let h = files[fi].hunks[hi]
            let first = h.lines.first { $0.kind != .context } ?? h.lines.first
            if let first, first.kind == .removed { return ReviewNoteAnchor(path: files[fi].oldPath ?? files[fi].path, line: first.old, side: .old) }
            return ReviewNoteAnchor(path: files[fi].path, line: first?.new ?? h.newStart, side: .new)
        case .note(let a):
            return a
        }
    }

    /// The line provenance asks about for a hunk: its first changed new line.
    public func provenanceLine(_ ref: HunkRef) -> Int {
        let h = hunk(ref)
        return h.lines.first { $0.kind == .added }?.new ?? h.newStart
    }
}

// MARK: The hunk cursor

/// J / K move hunk by hunk, N file by file; after a decision (seen,
/// rejected) the cursor goes on to the next undecided hunk.
public enum ReviewCursor {
    public static func next(_ i: Int?, count: Int) -> Int? {
        guard count > 0 else { return nil }
        guard let i else { return 0 }
        return min(i + 1, count - 1)
    }

    public static func previous(_ i: Int?, count: Int) -> Int? {
        guard count > 0 else { return nil }
        guard let i else { return 0 }
        return max(i - 1, 0)
    }

    /// The first hunk of the next file (or of the previous one, `back`).
    public static func nextFile(_ i: Int?, hunks: [HunkRef], back: Bool = false) -> Int? {
        guard !hunks.isEmpty else { return nil }
        guard let i, hunks.indices.contains(i) else { return 0 }
        let file = hunks[i].file
        if back {
            // The start of this file, or of the one before when already there.
            let start = hunks.firstIndex { $0.file == file } ?? 0
            if start < i { return start }
            guard let prev = hunks[..<start].last?.file else { return start }
            return hunks.firstIndex { $0.file == prev }
        }
        return hunks.firstIndex { $0.file > file } ?? i
    }

    /// After a decision on hunk `i`: the next undecided hunk after it,
    /// else the first undecided one before it; nil: every hunk decided.
    public static func afterDecision(_ i: Int, hunks: [HunkRef], decided: Set<String>) -> Int? {
        if let n = hunks.indices.first(where: { $0 > i && !decided.contains(hunks[$0].key) }) { return n }
        return hunks.indices.first { $0 < i && !decided.contains(hunks[$0].key) }
    }
}

// MARK: Attention

/// What the reviewer has looked at (V) and rejected (X), per item, by
/// hunk key. The attention strip draws one cell per hunk.
public struct ReviewAttention: Equatable, Sendable {
    public private(set) var seen: Set<String> = []
    public private(set) var rejected: Set<String> = []

    public init() {}

    public enum Mark: Equatable, Sendable { case unseen, seen, rejected, noted }

    public mutating func toggleSeen(_ id: String) {
        if seen.contains(id) { seen.remove(id) } else { seen.insert(id) }
    }

    public mutating func markSeen(_ ids: [String]) { seen.formUnion(ids) }

    public mutating func reject(_ ids: [String]) {
        rejected.formUnion(ids)
        seen.formUnion(ids)
    }

    /// Undo a reject the daemon refused.
    public mutating func unreject(_ ids: [String]) { rejected.subtract(ids) }

    public var decided: Set<String> { seen.union(rejected) }

    /// Hunks that are gone from a fresh diff are forgotten.
    public mutating func keep(_ ids: Set<String>) {
        seen.formIntersection(ids)
        rejected.formIntersection(ids)
    }

    public func marks(_ hunks: [HunkRef], noted: Set<String>) -> [Mark] {
        hunks.map { h in
            if rejected.contains(h.key) { return .rejected }
            if noted.contains(h.key) { return .noted }
            return seen.contains(h.key) ? .seen : .unseen
        }
    }

    /// "4 of 12 hunks seen".
    public func summary(total: Int) -> String {
        let n = min(total, seen.count)
        return "\(n) of \(total) hunk\(total == 1 ? "" : "s") seen"
    }

    public func unseen(_ hunks: [HunkRef]) -> Int { hunks.filter { !seen.contains($0.key) && !rejected.contains($0.key) }.count }
}

// MARK: Notes

/// The notes on one item, by anchor (one note per line and side).
public struct ReviewNoteBook: Equatable, Sendable {
    public private(set) var notes: [ReviewNoteAnchor: String] = [:]

    public init() {}

    public subscript(_ a: ReviewNoteAnchor) -> String? { notes[a] }

    /// Empty text removes the note.
    public mutating func set(_ a: ReviewNoteAnchor, _ text: String) {
        let t = text.trimmingCharacters(in: .whitespacesAndNewlines)
        if t.isEmpty { notes[a] = nil } else { notes[a] = t }
    }

    public mutating func removeAll() { notes = [:] }

    public var isEmpty: Bool { notes.isEmpty }
    public var count: Int { notes.count }
    public var anchors: Set<ReviewNoteAnchor> { Set(notes.keys) }

    /// In reading order: by file (`paths` order; unknown last), then line.
    public func ordered(paths: [String]) -> [ReviewNote] {
        let rank = Dictionary(paths.enumerated().map { ($1, $0) }, uniquingKeysWith: { a, _ in a })
        return notes.map { ReviewNote(anchor: $0.key, text: $0.value) }.sorted { a, b in
            let ra = rank[a.anchor.path] ?? Int.max, rb = rank[b.anchor.path] ?? Int.max
            if ra != rb { return ra < rb }
            if a.anchor.path != b.anchor.path { return a.anchor.path < b.anchor.path }
            return ReviewStream.noteOrder(a.anchor, b.anchor)
        }
    }

    /// The hunks (keys) that carry a note (attention strip).
    public func notedHunks(_ stream: ReviewStream) -> Set<String> {
        var out = Set<String>()
        for a in notes.keys {
            if let r = stream.row(ofNote: a), let o = stream.hunkOrdinal(atRow: r) { out.insert(stream.hunks[o].key) }
        }
        return out
    }
}

/// All notes as one instruction (what `review.sendBack` types into the
/// agent; shown before sending).
public enum ReviewNotes {
    public static func instruction(_ notes: [ReviewNote], message: String? = nil) -> String {
        var lines: [String] = []
        let m = message?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        if !m.isEmpty { lines.append(m) }
        if !notes.isEmpty {
            lines.append("Review notes (\(notes.count)):")
            for (i, n) in notes.enumerated() { lines.append("\(i + 1). \(where_(n.anchor)): \(n.text)") }
            lines.append("Address each note, then stop for another review.")
        }
        return lines.joined(separator: "\n")
    }

    /// "src/users.ts:42", "src/users.ts:40 (removed line)", "src/users.ts".
    public static func where_(_ a: ReviewNoteAnchor) -> String {
        guard let l = a.line else { return a.path }
        return a.side == .old ? "\(a.path):\(l) (removed line)" : "\(a.path):\(l)"
    }
}

// MARK: Keys

public enum ReviewKeyAction: Equatable, Sendable {
    /// ↑ ↓: the inbox's selection.
    case previousItem, nextItem
    /// J / K hunks, N / ⇧N files.
    case nextHunk, previousHunk, nextFile, previousFile
    /// V: seen (toggle), X: reject the hunk, C: a note on it.
    case seen, reject, note
    /// ⏎: open or fold a folded file.
    case toggleFold
    /// ⇧↩ send the notes back, ⌘↩ accept and commit, ⌥↩ where it came
    /// from (History), ⇧A accept every eligible item.
    case sendBack, accept, provenance, bulkAccept
    /// In a note: ⌘↩ / ⇧↩ saves, esc cancels.
    case saveNote, cancelNote
    /// Esc, ⌘R.
    case close
    /// Not the sheet's: the focused field (or nothing) gets it.
    case pass
}

public enum ReviewKeys {
    public static func route(_ k: KeyChord, editingNote: Bool = false) -> ReviewKeyAction {
        if editingNote {
            if k.key == .escape && !k.command && !k.option && !k.control { return .cancelNote }
            if k.key == .enter && (k.command || k.shift) && !k.option && !k.control { return .saveNote }
            return .pass
        }
        if k.command && !k.option && !k.control && !k.shift {
            switch k.key {
            case .enter: return .accept
            case .char("r"), .char("R"): return .close
            default: return .pass
            }
        }
        if k.command || k.control { return .pass }
        if k.option {
            return k.key == .enter && !k.shift ? .provenance : .pass
        }
        switch k.key {
        case .escape: return .close
        case .up: return .previousItem
        case .down: return .nextItem
        case .enter: return k.shift ? .sendBack : .toggleFold
        case .char(let c):
            switch (c, k.shift) {
            case ("j", false): return .nextHunk
            case ("k", false): return .previousHunk
            case ("n", false): return .nextFile
            case ("N", _), ("n", true): return .previousFile
            case ("v", false), ("V", _): return .seen
            case ("x", false), ("X", _): return .reject
            case ("c", false), ("C", _): return .note
            case ("A", _), ("a", true): return .bulkAccept
            default: return .pass
            }
        default: return .pass
        }
    }
}

// MARK: Line text

/// A code line as drawn: tabs expanded, very long lines cut (one CTLine
/// per row stays cheap), the changed-word ranges moved along.
public enum ReviewLineText {
    public static let tabWidth = 4
    /// UTF-16 units drawn at most; the rest is "…".
    public static let maxLength = 1_000

    public static func display(_ text: String, words: [NSRange]) -> (text: String, words: [NSRange]) {
        let u = Array(text.utf16)
        guard u.contains(9) || u.contains(13) || u.count > maxLength else { return (text, words) }
        var out: [UInt16] = []
        out.reserveCapacity(min(u.count, maxLength) + 8)
        var map: [Int] = [] // source index → output index
        map.reserveCapacity(u.count + 1)
        for c in u {
            map.append(out.count)
            if out.count >= maxLength { continue }
            switch c {
            case 9: out.append(contentsOf: repeatElement(32, count: tabWidth - out.count % tabWidth))
            case 13: break
            default: out.append(c)
            }
        }
        map.append(out.count)
        let limit = min(out.count, maxLength)
        var s = String(decoding: out.prefix(limit), as: UTF16.self)
        if out.count > maxLength || (u.count > 0 && map[u.count - 1] >= maxLength) { s += "…" }
        let moved = words.compactMap { r -> NSRange? in
            guard r.location >= 0, r.location < u.count else { return nil }
            let a = min(map[r.location], limit), b = min(map[min(r.location + r.length, u.count)], limit)
            return b > a ? NSRange(location: a, length: b - a) : nil
        }
        return (s, moved)
    }
}
