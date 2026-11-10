import Foundation
import HesperCore
import HesperSurfaces
import Testing

/// Frames shaped like the daemon's view renders (relay/internal/ptyhost/view.go).
@Suite struct ScreenRowsTests {
    static let esc = "\u{1B}"
    static let preamble =
        "\(esc)[?2026h\(esc)[0m\(esc)[?1049l\(esc)[r\(esc)[?6l\(esc)[?7l\(esc)[4l\(esc)(B\(esc))B\u{0F}"
        + "\(esc)[?2004l\(esc)[?1000l\(esc)[?1002l\(esc)[?1003l\(esc)[?1004l\(esc)[H\(esc)[2J"
    static let begin = "\(esc)[?2026h"
    static let end = "\(esc)[?2026l"
    static func row(_ n: Int, _ ansi: String) -> String { "\(esc)[\(n);1H\(esc)[0m\(esc)[2K\(ansi)\(esc)[0m" }
    static func cursor(_ r: Int, _ c: Int) -> String { "\(esc)[\(r);\(c)H\(esc)[2 q\(esc)[?25h" }
    /// A first render: preamble, two rows (the second as RowANSI writes SGR runs), the cursor at 2;5.
    static let first = preamble + row(1, "$ ls") + row(2, "\(esc)[0;1mbold\(esc)[0m and plain") + cursor(2, 5) + end
    static let size = GridSize(cols: 20, rows: 3)

    /// Feeds s; outside #expect, which cannot take a mutating call.
    static func feed(_ s: inout ScreenRows, _ text: String) -> Bool { s.apply(Data(text.utf8)) }

    /// A ZWJ sequence (family, rainbow flag) is one wide cell, as terminals draw it.
    @Test func testZWJSequenceIsOneCell() {
        var s = ScreenRows(size: Self.size)
        _ = Self.feed(&s, Self.preamble + Self.row(1, "👨\u{200D}👩\u{200D}👧x") + Self.end)
        #expect(s.text(row: 0) == "👨\u{200D}👩\u{200D}👧x")
        _ = Self.feed(&s, Self.begin + Self.cursor(1, 4) + Self.end)
        // The cursor after it is at cell 3 (one wide cell, then x).
        #expect(s.cursor?.col == 3)
        #expect(ScreenRows.cursorPosition(in: s.rows[0], col: 2)?.offset == 1)
    }

    /// A flag is two regional indicators, one cell each as hesperd's vt
    /// counts them; the cursor after it agrees.
    @Test func testFlagTakesTwoCells() {
        var s = ScreenRows(size: Self.size)
        _ = Self.feed(&s, Self.preamble + Self.row(1, "🇩🇪x") + Self.cursor(1, 4) + Self.end)
        #expect(s.text(row: 0) == "🇩🇪x")
        #expect(s.cursor?.col == 3)
        #expect(ScreenRows.cells("🇩🇪") == 2)
        #expect(ScreenRows.cursorPosition(in: s.rows[0], col: 2)?.offset == 1)
        #expect(ScreenRows.cursorPosition(in: s.rows[0], col: 3) == nil)
        // A full row (20 cells by hesperd's count) is not clipped.
        let full = "🇩🇪" + String(repeating: "x", count: 18)
        _ = Self.feed(&s, Self.begin + Self.row(1, full) + Self.end)
        #expect(s.text(row: 0) == full)
    }

    /// A resize inside a synchronized update publishes with its end.
    @Test func testResizeWaitsForSynchronizedUpdate() {
        var s = ScreenRows(size: Self.size)
        _ = Self.feed(&s, Self.first)
        _ = Self.feed(&s, Self.begin + Self.row(1, "half"))
        s.resize(GridSize(cols: 10, rows: 3))
        #expect(s.text(row: 0) == "$ ls")
        _ = Self.feed(&s, Self.end)
        #expect(s.text(row: 0) == "half")
    }

    /// An update that is never closed is shown after a bound of frames.
    @Test func testUnterminatedSynchronizedUpdateIsShown() {
        var s = ScreenRows(size: Self.size)
        _ = Self.feed(&s, Self.first)
        _ = Self.feed(&s, Self.begin + Self.row(1, "stuck"))
        #expect(s.text(row: 0) == "$ ls")
        for _ in 0..<16 { _ = Self.feed(&s, "") }
        #expect(s.text(row: 0) == "stuck")
        // A later, closed update works as before.
        _ = Self.feed(&s, Self.begin + Self.row(1, "next") + Self.end)
        #expect(s.text(row: 0) == "next")
    }

    /// The ITU colon form may carry a color space and trailing tolerance
    /// sub-parameters: r, g, b are the three after the space.
    @Test func testColonTruecolorWithExtraSubParameters() {
        var s = ScreenRows(size: Self.size)
        let e = Self.esc
        _ = Self.feed(&s, Self.preamble + Self.row(1, "\(e)[38:2:0:9:8:7:0:1mz") + Self.end)
        #expect(s.rows[0].first?.style.fg == .rgb(9, 8, 7))
        // The three forms: empty space id, a space id, legacy without one
        // (exactly r:g:b; with more values the second is the space id).
        for form in ["38:2::1:2:3", "38:2:5:1:2:3", "38:2:1:2:3"] {
            _ = Self.feed(&s, Self.begin + Self.row(1, "\(e)[\(form)mz") + Self.end)
            #expect(s.rows[0].first?.style.fg == .rgb(1, 2, 3), "\(form)")
        }
    }

    /// The cursor's column counts cells: a wide character before it takes two.
    @Test func testCursorPositionCountsCells() {
        let runs = [StyledRun(text: "日本", style: .plain), StyledRun(text: "ab", style: .plain)]
        #expect(ScreenRows.cursorPosition(in: runs, col: 0)! == (0, 0))
        #expect(ScreenRows.cursorPosition(in: runs, col: 3)! == (0, 1))
        #expect(ScreenRows.cursorPosition(in: runs, col: 4)! == (1, 0))
        #expect(ScreenRows.cursorPosition(in: runs, col: 5)! == (1, 1))
        #expect(ScreenRows.cursorPosition(in: runs, col: 6) == nil)
        #expect(ScreenRows.cells("日") == 2 && ScreenRows.cells("a") == 1)
    }

    @Test func testFirstRenderRowsAndCursor() {
        var s = ScreenRows(size: Self.size)
        let changed = Self.feed(&s, Self.first)
        #expect(changed)
        #expect(s.rows.count == 3)
        #expect(s.text(row: 0) == "$ ls")
        #expect(s.text(row: 1) == "bold and plain")
        #expect(
            s.rows[1] == [
                StyledRun(text: "bold", style: RunStyle(bold: true)), StyledRun(text: " and plain", style: .plain),
            ])
        #expect(s.rows[2] == [])
        #expect(s.cursor?.row == 1)
        #expect(s.cursor?.col == 4)
    }

    @Test func testSplitFrame() throws {
        // Cut in the middle of a row's text, and in the middle of the cursor's escape.
        let mid = try #require(Self.first.range(of: " and")).lowerBound
        let inEscape = try #require(Self.first.range(of: "[2;5H")).upperBound
        for cut in [mid, Self.first.index(inEscape, offsetBy: -2)] {
            var s = ScreenRows(size: Self.size)
            let partOne = Self.feed(&s, String(Self.first[..<cut]))
            #expect(!partOne)
            #expect(s.rows.allSatisfy { $0.isEmpty })
            #expect(s.cursor == nil)
            let partTwo = Self.feed(&s, String(Self.first[cut...]))
            #expect(partTwo)
            #expect(s.text(row: 0) == "$ ls")
            #expect(s.text(row: 1) == "bold and plain")
            #expect(s.cursor?.row == 1 && s.cursor?.col == 4)
        }
    }

    @Test func testSGRColors() {
        let e = Self.esc
        var s = ScreenRows(size: GridSize(cols: 40, rows: 2))
        let frame =
            Self.begin
            + Self.row(1, "\(e)[1;31mred\(e)[38;5;208morange\(e)[38;2;1;2;3mrgb\(e)[0mplain")
            + Self.row(2, "\(e)[0;2;3;4;7;9;94;101mx\(e)[0;48;2;4;5;6;39my\(e)[0;4:3;38:2::9:8:7mz") + Self.end
        let changed = Self.feed(&s, frame)
        #expect(changed)
        #expect(
            s.rows[0] == [
                StyledRun(text: "red", style: RunStyle(fg: .indexed(1), bold: true)),
                StyledRun(text: "orange", style: RunStyle(fg: .indexed(208), bold: true)),
                StyledRun(text: "rgb", style: RunStyle(fg: .rgb(1, 2, 3), bold: true)),
                StyledRun(text: "plain", style: .plain),
            ])
        #expect(
            s.rows[1] == [
                StyledRun(
                    text: "x",
                    style: RunStyle(
                        fg: .indexed(12), bg: .indexed(9), dim: true, italic: true, underline: true, reverse: true,
                        strike: true)),
                StyledRun(text: "y", style: RunStyle(bg: .rgb(4, 5, 6))),
                StyledRun(text: "z", style: RunStyle(fg: .rgb(9, 8, 7), underline: true)),
            ])
    }

    @Test func testCurlyUnderlineIsAnUnderline() {
        var s = ScreenRows(size: GridSize(cols: 20, rows: 1))
        _ = Self.feed(&s, Self.begin + Self.row(1, "\(Self.esc)[4:3mwavy\(Self.esc)[4:0mflat") + Self.end)
        #expect(
            s.rows[0] == [
                StyledRun(text: "wavy", style: RunStyle(underline: true)), StyledRun(text: "flat", style: .plain),
            ])
    }

    @Test func testUnderlineColourIsConsumed() {
        let e = Self.esc
        var s = ScreenRows(size: GridSize(cols: 20, rows: 1))
        // Colon and semicolon forms; the codes after them still apply.
        let ansi = "\(e)[58:2::1:2:3mab\(e)[58;5;208;1mcd\(e)[58;2;4;5;6;31mef"
        let changed = Self.feed(&s, Self.begin + Self.row(1, ansi) + Self.end)
        #expect(changed)
        #expect(
            s.rows[0] == [
                StyledRun(text: "ab", style: .plain),
                StyledRun(text: "cd", style: RunStyle(bold: true)),
                StyledRun(text: "ef", style: RunStyle(fg: .indexed(1), bold: true)),
            ])
    }

    @Test func testChangedRowOnly() {
        var s = ScreenRows(size: Self.size)
        _ = Self.feed(&s, Self.first)
        let second = Self.begin + Self.row(2, "next") + Self.cursor(2, 5) + Self.end
        let changed = Self.feed(&s, second)
        #expect(changed)
        #expect(s.text(row: 0) == "$ ls")
        #expect(s.text(row: 1) == "next")
        // The same frame again changes nothing.
        let again = Self.feed(&s, second)
        #expect(!again)
    }

    @Test func testHiddenCursor() {
        var s = ScreenRows(size: Self.size)
        _ = Self.feed(&s, Self.first)
        let changed = Self.feed(&s, Self.begin + "\(Self.esc)[?25l" + Self.end)
        #expect(changed)
        #expect(s.cursor == nil)
        #expect(s.text(row: 1) == "bold and plain")
    }

    @Test func testIgnoresUnknownCSI() {
        var s = ScreenRows(size: Self.size)
        let preamble = Self.feed(&s, Self.preamble + Self.end)
        #expect(!preamble)
        let others = Self.feed(&s, "\(Self.esc)]0;title\u{07}\(Self.esc)[?1049h\(Self.esc)[5n\(Self.esc)[3;7r")
        #expect(!others)
        #expect(s.rows.allSatisfy { $0.isEmpty })
        #expect(s.cursor == nil)
        #expect(s == ScreenRows(size: Self.size))
    }

    @Test func testTextPastTheLastColumnIsDroppedAndResizeKeepsRows() {
        var s = ScreenRows(size: GridSize(cols: 4, rows: 2))
        _ = Self.feed(&s, Self.begin + Self.row(1, "abcdef") + Self.row(2, "x世") + Self.cursor(1, 1) + Self.end)
        #expect(s.text(row: 0) == "abcd")
        #expect(s.text(row: 1) == "x世")
        s.resize(GridSize(cols: 2, rows: 3))
        #expect(s.size == GridSize(cols: 2, rows: 3))
        #expect(s.rows.count == 3)
        #expect(s.text(row: 0) == "ab")
        #expect(s.text(row: 1) == "x")  // the wide character no longer fits whole
        #expect(s.rows[2] == [])
    }
}
