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
}
