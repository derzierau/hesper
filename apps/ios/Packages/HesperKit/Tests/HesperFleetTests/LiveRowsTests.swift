import HesperSurfaces
import Testing

@Suite struct LiveRowsTests {
    /// count rows of text, blank at the given rows.
    static func rows(_ count: Int, blank: Set<Int>) -> [String] {
        (0..<count).map { blank.contains($0) ? "" : "line \($0)" }
    }

    @Test func testWorkingShowsAll() {
        let rows = Self.rows(20, blank: [12])
        #expect(LiveRows.visible(rowTexts: rows, cursorRow: 16, working: true) == 0..<20)
        #expect(LiveRows.visible(rowTexts: rows, cursorRow: nil, working: true) == 0..<20)
    }

    @Test func testIdleFromBlankAboveCursor() {
        let rows = Self.rows(20, blank: [12])
        #expect(LiveRows.visible(rowTexts: rows, cursorRow: 16, working: false) == 13..<20)
        // Spaces only count as blank.
        var spaced = rows
        spaced[12] = "   "
        #expect(LiveRows.visible(rowTexts: spaced, cursorRow: 16, working: false) == 13..<20)
    }

    @Test func testIdleClampsToMaxIdle() {
        let rows = Self.rows(20, blank: [2])
        #expect(LiveRows.visible(rowTexts: rows, cursorRow: 19, working: false) == 8..<20)
    }

    @Test func testIdleFallbackNoCursor() {
        let rows = Self.rows(20, blank: [12])
        #expect(LiveRows.visible(rowTexts: rows, cursorRow: nil, working: false) == 12..<20)
        // No blank row above the cursor, and a cursor off the grid.
        #expect(LiveRows.visible(rowTexts: Self.rows(20, blank: []), cursorRow: 16, working: false) == 12..<20)
        #expect(LiveRows.visible(rowTexts: rows, cursorRow: 20, working: false) == 12..<20)
    }

    @Test func testIdleShortScreens() {
        #expect(LiveRows.visible(rowTexts: [], cursorRow: nil, working: false) == 0..<0)
        #expect(LiveRows.visible(rowTexts: Self.rows(3, blank: []), cursorRow: nil, working: false) == 0..<3)
        // The cursor row itself blank: the walk starts above it.
        #expect(LiveRows.visible(rowTexts: Self.rows(5, blank: [1, 4]), cursorRow: 4, working: false) == 2..<5)
    }

    @Test func cursorRowIsPinnedWithoutDuplicatingOutputOrStatus() {
        let result = LiveRows.partition(rowTexts: Self.rows(8, blank: [2]), cursorRow: 4, working: false, pinInput: true)
        #expect(result.input == 4)
        #expect(result.scrolling == [3, 5, 6, 7])
    }

    @Test func workingScreenKeepsItsCursorRowFixed() {
        let result = LiveRows.partition(rowTexts: Self.rows(4, blank: []), cursorRow: 2, working: true, pinInput: true)
        #expect(result.input == 2)
        #expect(result.scrolling == [0, 1, 3])
    }

    @Test func missingOrInvalidCursorLeavesVisibleRowsScrolling() {
        let rows = Self.rows(4, blank: [])
        let hidden = LiveRows.partition(rowTexts: rows, cursorRow: nil, working: false, pinInput: true)
        let offGrid = LiveRows.partition(rowTexts: rows, cursorRow: 4, working: false, pinInput: true)
        #expect(hidden.input == nil && hidden.scrolling == [0, 1, 2, 3])
        #expect(offGrid.input == nil && offGrid.scrolling == [0, 1, 2, 3])
    }

    @Test func readOnlyScreenDoesNotPinTheCursor() {
        let result = LiveRows.partition(rowTexts: Self.rows(4, blank: []), cursorRow: 2, working: false, pinInput: false)
        #expect(result.input == nil)
        #expect(result.scrolling == [0, 1, 2, 3])
    }

    @Test func cursorOutsideVisibleFallbackStillPinsItsInputRow() {
        let result = LiveRows.partition(rowTexts: Self.rows(20, blank: []), cursorRow: 2, working: false, pinInput: true)
        #expect(result.input == 2)
        #expect(result.scrolling == Array(12..<20))
    }

    @Test func oneRowScreenHasOnlyThePinnedInput() {
        let result = LiveRows.partition(rowTexts: ["prompt"], cursorRow: 0, working: false, pinInput: true)
        #expect(result.input == 0)
        #expect(result.scrolling.isEmpty)
    }
}
