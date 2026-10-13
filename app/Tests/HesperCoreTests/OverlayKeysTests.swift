import Foundation
import Testing
@testable import HesperCore

@Suite struct OverlayKeysTests {
    /// ⌫ in a filterable popover and in the inbox: the chord resolves to
    /// deleteBackward in both surfaces and takes a character off the list's
    /// query (the window routes it before any view).
    @Test func deleteBackwardReachesTheList() {
        let chord = KeyChord(.delete)
        #expect(OverlayKeys.route(chord, characters: "\u{7f}", surface: .popover) == .deleteBackward)
        #expect(OverlayKeys.route(chord, characters: "\u{7f}", surface: .inbox) == .deleteBackward)
        #expect(OverlayKeys.route(chord, characters: "\u{7f}", surface: .palette) == .deleteBackward)
        var l = OverlayList(query: "ab", index: 1)
        l.deleteBackward()
        #expect(l.query == "a" && l.index == 0)
        l.deleteBackward()
        l.deleteBackward()
        #expect(l.query == "")
        // The inbox treats it as its own (not a verb): the filter shrinks.
        #expect(InboxKeys.route(.deleteBackward) == .none)
    }

    /// The scoped palette (an agent's actions): "‹ Back" first with esc as
    /// its cap, the selection opening on the first action.
    @Test func paletteScopeStartsWithBack() {
        let rows = PaletteScope.rows(actions: ["open:a", "close:a"])
        #expect(rows == ["back", "open:a", "close:a"])
        #expect(PaletteScope.isBack(rows[0]) && !PaletteScope.isBack(rows[1]))
        #expect(PaletteScope.backKey == "esc" && PaletteScope.backTitle == "‹ Back")
        let list = OverlayList(index: PaletteScope.firstAction)
        #expect(list.selection(enabled: rows.map { _ in true }) == 1)
        // Moving up from the first action lands on Back; down from the last wraps to it.
        var l = list
        l.move(-1, enabled: rows.map { _ in true })
        #expect(PaletteScope.isBack(rows[l.index]))
        l = OverlayList(index: 2)
        l.move(1, enabled: rows.map { _ in true })
        #expect(PaletteScope.isBack(rows[l.index]))
    }
}
