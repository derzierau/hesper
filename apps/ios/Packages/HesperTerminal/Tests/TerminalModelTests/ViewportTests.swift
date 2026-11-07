import CoreGraphics
import Testing

@testable import TerminalModel

/// How a Mac's grid is scaled and panned on the phone.
@Suite struct ViewportTests {
    // An iPhone 17 in portrait, and a 13 pt cell (points).
    let phone = CGSize(width: 402, height: 700)
    let cell = CGSize(width: 8, height: 17)

    @Test func narrowGridsFitTheWidthWideOnesStayReadable() {
        #expect(Viewport.defaultMode(cols: 80) == .fitWidth)
        #expect(Viewport.defaultMode(cols: 90) == .fitWidth)
        #expect(Viewport.defaultMode(cols: 91) == .readable)
        #expect(Viewport.defaultMode(cols: 200) == .readable)
    }

    @Test func fitWidthScalesTheColumnsToTheViewWidth() {
        let v = Viewport(grid: (cols: 97, rows: 56), view: phone, cell: cell, mode: .fitWidth)
        let expected = 402 / (97 * 8.0)
        #expect(abs(v.scale - expected) < 1e-9)
        #expect(abs(v.contentSize.width - 402) < 1e-9)
        #expect(v.panRange.x == 0...0)
    }

    @Test func fitWidthNeverMagnifies() {
        let v = Viewport(grid: (cols: 20, rows: 10), view: phone, cell: cell, mode: .fitWidth)
        #expect(v.scale == 1)
    }

    @Test func readableDrawsANinePointFont() {
        let v = Viewport(grid: (cols: 200, rows: 56), view: phone, cell: cell, mode: .readable)
        #expect(abs(v.scale - 5.4 / 8) < 1e-9)
        #expect(abs(v.contentSize.width - 200 * 5.4) < 1e-9)
        // 200 columns at 5.4 pt are wider than the phone: it pans sideways.
        #expect(abs(v.panRange.x.upperBound - (200 * 5.4 - 402)) < 1e-9)
        #expect(v.panRange.x.lowerBound == 0)
    }

    @Test func readableIsSetByTheCellWidthNotItsLineHeight() {
        // The surface's real cell (13 pt JetBrains Mono, 1.32 em lines):
        // 97 columns at a 9 pt tall cell would fit the phone; at a 9 pt
        // font they pan.
        let tall = CGSize(width: 31 / 3.0, height: 23)
        let v = Viewport(grid: (cols: 97, rows: 56), view: phone, cell: tall, mode: .readable)
        #expect(abs(v.scale - 5.4 / tall.width) < 1e-9)
        #expect(v.panRange.x.upperBound > 100)
    }

    @Test func readableIsNeverSmallerThanFitWidth() {
        // A tiny cell: 5.4 pt wide would be a magnification, fit width is larger still.
        let v = Viewport(grid: (cols: 40, rows: 10), view: phone, cell: CGSize(width: 4, height: 6), mode: .readable)
        #expect(v.scale == 1)
    }

    @Test func zoomClampsBetweenFitWidthAndOne() {
        var v = Viewport(grid: (cols: 200, rows: 56), view: phone, cell: cell, mode: .readable)
        let fit = 402 / (200 * 8.0)
        v.zoom(to: 0.01)
        #expect(abs(v.scale - fit) < 1e-9)
        v.zoom(to: 5)
        #expect(v.scale == 1)
        v.zoom(to: 0.5)
        #expect(v.scale == 0.5)
        // Choosing a mode again drops the pinch.
        v.mode = .fitWidth
        #expect(abs(v.scale - fit) < 1e-9)
    }

    @Test func panRangesAreZeroWhenTheContentFits() {
        let v = Viewport(grid: (cols: 40, rows: 20), view: phone, cell: cell, mode: .fitWidth)
        #expect(v.panRange.x == 0...0)
        #expect(v.panRange.y == 0...0)
    }

    @Test func tallContentPansVertically() {
        let v = Viewport(grid: (cols: 97, rows: 120), view: phone, cell: cell, mode: .fitWidth)
        let height = 120 * 17 * (402 / (97 * 8.0))
        #expect(abs(v.panRange.y.upperBound - (height - 700)) < 1e-9)
    }

    @Test func aGridChangeRecomputesTheScale() {
        var v = Viewport(grid: (cols: 80, rows: 24), view: phone, cell: cell, mode: .fitWidth)
        let before = v.scale
        v.zoom(to: 0.9)
        v.setGrid(cols: 160, rows: 50)
        #expect(v.cols == 160 && v.rows == 50)
        #expect(abs(v.scale - 402 / (160 * 8.0)) < 1e-9)
        #expect(v.scale < before)
        // The view rotates: the fit follows the new width.
        v.view = CGSize(width: 874, height: 402)
        #expect(abs(v.scale - min(1, 874 / (160 * 8.0))) < 1e-9)
    }

    @Test func anEmptyGridIsHarmless() {
        let v = Viewport(grid: (cols: 0, rows: 0), view: phone, cell: .zero, mode: .fitWidth)
        #expect(v.scale == 1)
        #expect(v.contentSize == .zero)
        #expect(v.panRange.x == 0...0)
    }

    @Test func followKeepsTheLastWrittenRowAboveTheDock() {
        // 56 rows at 9 pt (cell 8x9 at scale 1) are 504 pt; the view shows 300.
        let v = Viewport(
            grid: (cols: 40, rows: 56), view: CGSize(width: 402, height: 300), cell: CGSize(width: 8, height: 9), mode: .fitWidth)
        #expect(v.panRange.y.upperBound == 204)
        // Text down to row 13: the grid's top shows, nothing to move.
        #expect(v.followOffset(lastTextRow: 13) == 0)
        // Row 39's bottom (360 pt) sits at the view's bottom.
        #expect(v.followOffset(lastTextRow: 39) == 60)
        // A full screen, or a blank one, follows the grid's bottom.
        #expect(v.followOffset(lastTextRow: 55) == 204)
        #expect(v.followOffset(lastTextRow: nil) == 204)
    }
}
