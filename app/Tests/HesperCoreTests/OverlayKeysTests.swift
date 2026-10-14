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

    /// The inbox's J and K are letters for its filter (an agent named
    /// "jobs" can be filtered for), as every other letter is.
    @Test func lettersTypeIntoTheInboxFilter() {
        #expect(OverlayKeys.route(KeyChord(.char("j")), characters: "j", surface: .inbox) == .type("j"))
        #expect(OverlayKeys.route(KeyChord(.char("k")), characters: "k", surface: .inbox, typing: true) == .type("k"))
        #expect(OverlayKeys.route(KeyChord(.char("j")), characters: "j", surface: .inbox, typing: true) == .type("j"))
        #expect(OverlayKeys.route(KeyChord(.char("m")), characters: "m", surface: .inbox, typing: true) == .type("m"))
        // Arrows, ⌃N and ⌃P still move; esc and ⏎ keep their roles.
        #expect(OverlayKeys.route(KeyChord(.down), surface: .inbox, typing: true) == .down)
        #expect(OverlayKeys.route(KeyChord(.char("n"), control: true), surface: .inbox, typing: true) == .down)
        #expect(OverlayKeys.route(KeyChord(.escape), surface: .inbox, typing: true) == .close)
        #expect(OverlayKeys.route(KeyChord(.enter), surface: .inbox, typing: true) == .activate)
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
