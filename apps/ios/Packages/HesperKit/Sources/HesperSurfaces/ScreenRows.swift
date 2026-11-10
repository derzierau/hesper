import Foundation
import HesperCore

/// An agent's screen as styled rows, read from the daemon's view stream
/// (relay/internal/ptyhost/view.go): one render per DATA frame, written
/// with a small escape subset. Each changed row arrives whole as
/// `ESC[<row>;1H ESC[0m ESC[2K <RowANSI> ESC[0m`, inside a synchronized
/// update `ESC[?2026h … ESC[?2026l`, then the cursor. Anything else the
/// daemon writes (the preamble's modes, the cursor shape) is read and
/// ignored, so the phone shows text instead of running a terminal.
public struct ScreenRows: Equatable, Sendable {
    /// One entry per grid row; an empty row is [].
    public private(set) var rows: [[StyledRun]]
    /// Zero-based; nil while hidden.
    public private(set) var cursor: (row: Int, col: Int)?
    public private(set) var size: GridSize

    private struct Cell: Equatable, Sendable {
        var text: String
        var style: RunStyle
        /// The right half of a wide character: no text of its own.
        var continuation = false

        static let blank = Cell(text: " ", style: .plain)
    }

    private var cells: [[Cell]]
    private var dirty: Set<Int> = []
    private var style = RunStyle.plain
    private var row = 0
    private var col = 0
    /// The column of the character a combining mark joins; nil after a
    /// move, an erase or a dropped character.
    private var joinTarget: Int?
    private var cursorVisible = false
    private var synchronized = false
    /// Bytes of an escape or UTF-8 sequence cut by the end of a frame.
    private var carry: [UInt8] = []
    private var changed = false

    /// An unterminated sequence longer than this is junk, not a cut frame.
    static let maxCarry = 1 << 16

    public init(size: GridSize) {
        let size = GridSize(cols: max(size.cols, 0), rows: max(size.rows, 0))
        self.size = size
        rows = Array(repeating: [], count: size.rows)
        cells = Array(repeating: Array(repeating: .blank, count: size.cols), count: size.rows)
    }

    /// Equal when they show the same: rows, cursor and size.
    public static func == (a: Self, b: Self) -> Bool {
        a.size == b.size && a.rows == b.rows && same(a.cursor, b.cursor)
    }

    /// Runs joined, for tests and LiveRows.
    public func text(row: Int) -> String {
        guard rows.indices.contains(row) else { return "" }
        return rows[row].map(\.text).joined()
    }

    /// Fits the grid to size, keeping what fits; a wide character cut in
    /// half becomes a blank.
    public mutating func resize(_ size: GridSize) {
        let new = GridSize(cols: max(size.cols, 0), rows: max(size.rows, 0))
        guard new != self.size else { return }
        cells = (0..<new.rows).map { y in
            guard y < cells.count else { return Array(repeating: .blank, count: new.cols) }
            var r = Array(cells[y].prefix(new.cols))
            if new.cols < cells[y].count, cells[y][new.cols].continuation, !r.isEmpty { r[r.count - 1] = .blank }
            return r + Array(repeating: .blank, count: new.cols - r.count)
        }
        self.size = new
        row = min(row, max(new.rows - 1, 0))
        col = min(col, new.cols)
        joinTarget = nil
        dirty = Set(0..<new.rows)
        // Inside a synchronized update the frame is half written; its end
        // publishes.
        if !synchronized { publish() }
    }

    /// Feeds daemon bytes. Changes inside ESC[?2026h … ESC[?2026l become
    /// visible only at the closing sequence; bytes after an incomplete
    /// escape wait for the next call. Returns true when rows or cursor
    /// changed.
    @discardableResult public mutating func apply(_ bytes: Data) -> Bool {
        changed = false
        var buf = carry
        buf.append(contentsOf: bytes)
        carry = []
        var i = 0
        while i < buf.count {
            guard let n = step(buf, at: i) else {
                carry = buf.count - i > Self.maxCarry ? [] : Array(buf[i...])
                break
            }
            i += n
        }
        if synchronized {
            // An update that never ends must not freeze the screen: past a
            // bound of frames it ends, as terminals end it after a timeout.
            pendingFrames += 1
            if pendingFrames > Self.maxPendingFrames { synchronized = false }
        }
        if !synchronized {
            pendingFrames = 0
            publish()
        }
        return changed
    }

    /// Frames a synchronized update may take before it is shown anyway.
    /// hesperd writes each update in one frame; a long one spans a few.
    static let maxPendingFrames = 16
    private var pendingFrames = 0

    // MARK: Parsing

    /// Reads the sequence at i; the bytes it took, nil when it is cut off.
    private mutating func step(_ buf: [UInt8], at i: Int) -> Int? {
        switch buf[i] {
        case 0x1B: return escape(buf, at: i)
        case 0..<0x20, 0x7F: return 1  // C0 controls: the view positions every row itself
        default: return character(buf, at: i)
        }
    }

    private mutating func escape(_ buf: [UInt8], at i: Int) -> Int? {
        guard i + 1 < buf.count else { return nil }
        switch buf[i + 1] {
        case UInt8(ascii: "["):
            return csi(buf, at: i)
        case UInt8(ascii: "]"), UInt8(ascii: "P"), UInt8(ascii: "X"), UInt8(ascii: "^"), UInt8(ascii: "_"):
            // OSC, DCS and friends: up to BEL or ST, ignored.
            var j = i + 2
            while j < buf.count {
                if buf[j] == 0x07 { return j + 1 - i }
                if buf[j] == 0x1B {
                    guard j + 1 < buf.count else { return nil }
                    return buf[j + 1] == UInt8(ascii: "\\") ? j + 2 - i : j - i
                }
                j += 1
            }
            return nil
        case 0x20...0x2F:
            // `ESC ( B` and other designations: intermediates, then a final byte.
            var j = i + 1
            while j < buf.count, (0x20...0x2F).contains(buf[j]) { j += 1 }
            return j < buf.count ? j + 1 - i : nil
        default:
            return 2
        }
    }

    private mutating func csi(_ buf: [UInt8], at i: Int) -> Int? {
        var j = i + 2
        while j < buf.count, (0x30...0x3F).contains(buf[j]) { j += 1 }
        let paramsEnd = j
        while j < buf.count, (0x20...0x2F).contains(buf[j]) { j += 1 }
        guard j < buf.count else { return nil }
        // A control inside the sequence ends it unread.
        guard (0x40...0x7E).contains(buf[j]) else { return j - i }
        dispatch(params: buf[(i + 2)..<paramsEnd], intermediates: paramsEnd != j, final: buf[j])
        return j + 1 - i
    }

    private mutating func dispatch(params: ArraySlice<UInt8>, intermediates: Bool, final: UInt8) {
        // `ESC[<n> q` (cursor shape) is the only one with intermediates; the phone draws one cursor.
        guard !intermediates else { return }
        let marker = params.first.flatMap { (0x3C...0x3F).contains($0) ? $0 : nil }
        let values = Self.parameters(marker == nil ? params : params.dropFirst())
        switch (marker, final) {
        case (nil, UInt8(ascii: "H")):
            moveTo(row: Self.number(values, 0), col: Self.number(values, 1))
        case (nil, UInt8(ascii: "K")) where values.first?.first == 2:
            erase(row: row)
        case (nil, UInt8(ascii: "J")) where values.first?.first == 2:
            for y in 0..<size.rows { erase(row: y) }
        case (nil, UInt8(ascii: "m")):
            style.applySGR(values)
        case (UInt8(ascii: "?"), UInt8(ascii: "h")), (UInt8(ascii: "?"), UInt8(ascii: "l")):
            let on = final == UInt8(ascii: "h")
            for v in values {
                switch v.first ?? nil {
                case 25: cursorVisible = on
                case 2026:
                    synchronized = on
                    if !on { publish() }
                default: break
                }
            }
        default:
            break  // modes, scroll region and the rest of the preamble
        }
    }

    /// The parameter bytes as parameters of colon sub-parameters.
    private static func parameters(_ bytes: ArraySlice<UInt8>) -> [[Int?]] {
        guard !bytes.isEmpty else { return [] }
        var out: [[Int?]] = [[]]
        var cur: Int?
        for b in bytes {
            switch b {
            case 0x30...0x39: cur = min((cur ?? 0) * 10 + Int(b - 0x30), 0xFFFF)
            case UInt8(ascii: ":"):
                out[out.count - 1].append(cur)
                cur = nil
            case UInt8(ascii: ";"):
                out[out.count - 1].append(cur)
                out.append([])
                cur = nil
            default: break
            }
        }
        out[out.count - 1].append(cur)
        return out
    }

    /// Parameter i as a one-based position; missing or 0 is 1.
    private static func number(_ values: [[Int?]], _ i: Int) -> Int {
        let v = i < values.count ? values[i].first ?? nil : nil
        return max(v ?? 1, 1)
    }

    /// Decodes one UTF-8 character; nil when its bytes are cut off.
    private mutating func character(_ buf: [UInt8], at i: Int) -> Int? {
        let b = buf[i]
        let (len, bits): (Int, UInt8) =
            b < 0x80
            ? (1, b)
            : b >> 5 == 0b110 ? (2, b & 0x1F) : b >> 4 == 0b1110 ? (3, b & 0x0F) : b >> 3 == 0b11110 ? (4, b & 0x07) : (0, 0)
        guard len > 0 else {
            put("\u{FFFD}")
            return 1
        }
        var v = UInt32(bits)
        for k in 1..<len {
            guard i + k < buf.count else { return nil }
            let c = buf[i + k]
            guard c & 0xC0 == 0x80 else {
                put("\u{FFFD}")
                return k
            }
            v = v << 6 | UInt32(c & 0x3F)
        }
        put(Unicode.Scalar(v) ?? "\u{FFFD}")
        return len
    }

    // MARK: The grid

    private mutating func moveTo(row r: Int, col c: Int) {
        row = min(r - 1, max(size.rows - 1, 0))
        col = min(c - 1, max(size.cols - 1, 0))
        joinTarget = nil
    }

    private mutating func erase(row y: Int) {
        guard y < size.rows else { return }
        cells[y] = Array(repeating: Cell(text: " ", style: RunStyle(bg: style.bg)), count: size.cols)
        dirty.insert(y)
        joinTarget = nil
    }

    /// Writes s at the cursor in the current style; what does not fit
    /// before the last column is dropped (the view turns autowrap off).
    private mutating func put(_ s: Unicode.Scalar) {
        guard row < size.rows else { return }
        if Self.joins(s) {
            guard let x = joinTarget else { return }
            cells[row][x].text.unicodeScalars.append(s)
            dirty.insert(row)
            return
        }
        let width = Self.isWide(s) ? 2 : 1
        guard col + width <= size.cols else {
            col = size.cols
            joinTarget = nil
            return
        }
        clearWide(at: col)
        if width == 2 { clearWide(at: col + 1) }
        cells[row][col] = Cell(text: String(Character(s)), style: style)
        if width == 2 { cells[row][col + 1] = Cell(text: "", style: style, continuation: true) }
        joinTarget = col
        col += width
        dirty.insert(row)
    }

    /// Blanks the other half of a wide character that x is part of.
    private mutating func clearWide(at x: Int) {
        if cells[row][x].continuation, x > 0 { cells[row][x - 1] = .blank }
        if x + 1 < size.cols, cells[row][x + 1].continuation { cells[row][x + 1] = .blank }
    }

    /// Rebuilds the dirty rows and the cursor, noting what changed.
    private mutating func publish() {
        // After a resize (every row is dirty then).
        if rows.count != size.rows {
            rows = Array(repeating: [], count: size.rows)
            changed = true
        }
        for y in dirty.sorted() where y < size.rows {
            let runs = Self.runs(cells[y])
            if runs != rows[y] {
                rows[y] = runs
                changed = true
            }
        }
        dirty.removeAll()
        let c: (row: Int, col: Int)? =
            cursorVisible && size.rows > 0 && size.cols > 0 ? (row, min(col, size.cols - 1)) : nil
        if !Self.same(c, cursor) {
            cursor = c
            changed = true
        }
    }

    /// A row's cells as runs, trailing default blanks left out (like RowANSI).
    private static func runs(_ cells: [Cell]) -> [StyledRun] {
        var end = cells.count
        while end > 0, cells[end - 1] == .blank { end -= 1 }
        var out: [StyledRun] = []
        for c in cells[..<end] where !c.continuation {
            if let last = out.last, last.style == c.style {
                out[out.count - 1].text += c.text
            } else {
                out.append(StyledRun(text: c.text, style: c.style))
            }
        }
        return out
    }

    private static func same(_ a: (row: Int, col: Int)?, _ b: (row: Int, col: Int)?) -> Bool {
        a?.row == b?.row && a?.col == b?.col
    }

    /// Combining marks, variation selectors, the joiner and skin tones
    /// belong to the character before them.
    private static func joins(_ s: Unicode.Scalar) -> Bool {
        switch s.properties.generalCategory {
        case .nonspacingMark, .enclosingMark, .spacingMark: return true
        default: break
        }
        switch s.value {
        case 0x200D, 0xFE00...0xFE0F, 0x1F3FB...0x1F3FF, 0xE0020...0xE007F: return true
        default: return false
        }
    }

    /// The cells a published character takes: 2 when wide, as `put` laid
    /// it out.
    public static func cells(_ c: Character) -> Int {
        // A flag's two regional indicators take a cell each (`put` does
        // not join them); otherwise the first scalar decides.
        let ri = c.unicodeScalars.filter { (0x1F1E6...0x1F1FF).contains($0.value) }.count
        if ri > 0 { return ri }
        return c.unicodeScalars.first.map { isWide($0) ? 2 : 1 } ?? 1
    }

    /// Where the cursor's cell `col` falls in a published row: the run and
    /// the character offset in it, or nil past the row's end. `col` counts
    /// cells, so a wide character before it counts twice.
    public static func cursorPosition(in runs: [StyledRun], col: Int) -> (run: Int, offset: Int)? {
        var x = 0
        for (r, run) in runs.enumerated() {
            for (k, c) in run.text.enumerated() {
                let w = cells(c)
                if col < x + w { return (r, k) }
                x += w
            }
        }
        return nil
    }

    /// Two columns: emoji shown as emoji and the East Asian wide blocks.
    private static func isWide(_ s: Unicode.Scalar) -> Bool {
        // Regional indicators are one cell each in hesperd's vt (a flag
        // is two), though Unicode gives them emoji presentation.
        if (0x1F1E6...0x1F1FF).contains(s.value) { return false }
        if s.properties.isEmojiPresentation { return true }
        switch s.value {
        case 0x1100...0x115F, 0x2E80...0x303E, 0x3041...0x33FF, 0x3400...0x4DBF, 0x4E00...0x9FFF, 0xA000...0xA4CF,
            0xAC00...0xD7A3, 0xF900...0xFAFF, 0xFE30...0xFE4F, 0xFF00...0xFF60, 0xFFE0...0xFFE6, 0x20000...0x3FFFD:
            return true
        default: return false
        }
    }
}
