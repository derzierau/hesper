import Foundation
import Testing
@testable import HesperCore

// The band heading's ⋯ menu and the band-jump commands: the menu names
// every header gesture, and ⌥⌘↑ ⌥⌘↓ (View menu, everywhere) and ⌥↑ ⌥↓
// (the wall) both reach `jumpBand`.

@Suite struct BandMenuTests {
    private func titles(_ s: [[BandMenu.Item]]) -> [[String]] { s.map { $0.map(\.title) } }

    @Test func anOpenBandInTheMiddleOffersEverything() {
        let s = BandMenu.sections(collapsed: false, index: 1, count: 3, canOpenWall: true)
        #expect(titles(s) == [["Collapse", "Collapse Others"], ["New Agent Here", "Open as Wall"], ["Move Up", "Move Down"]])
        #expect(s.flatMap { $0 }.allSatisfy { $0.enabled })
        #expect(s[1][0].chord == CommandRegistry.command(.newAgent)?.chord, "New Agent Here shows the registry's ⌘N")
    }

    @Test func collapsedShowsExpandAndTheEndsLoseAMove() {
        let first = BandMenu.sections(collapsed: true, index: 0, count: 3, canOpenWall: true)
        #expect(first[0][0].title == "Expand")
        #expect(first[2].map(\.enabled) == [false, true])
        let last = BandMenu.sections(collapsed: false, index: 2, count: 3, canOpenWall: true)
        #expect(last[2].map(\.enabled) == [true, false])
    }

    @Test func aLoneBandCannotCollapseOthers() {
        let s = BandMenu.sections(collapsed: false, index: 0, count: 1, canOpenWall: true)
        #expect(s[0][1].title == "Collapse Others" && !s[0][1].enabled)
        #expect(s[2].map(\.enabled) == [false, false])
    }

    @Test func noScopeNoOpenAsWall() {
        let s = BandMenu.sections(collapsed: false, index: 0, count: 2, canOpenWall: false)
        #expect(titles(s)[1] == ["New Agent Here"])
    }

    @Test func aPointerLineShowsItsWallAndMoves() {
        let s = BandMenu.sections(collapsed: true, index: 1, count: 2, canOpenWall: true, pointer: true)
        #expect(titles(s) == [["Show Its Wall"], ["Move Up", "Move Down"]])
        #expect(s[0][0].action == .openAsWall)
    }

    // MARK: Band jump

    @Test func bothChordsReachJumpBand() {
        let up = KeyChord(.up, command: true, option: true), down = KeyChord(.down, command: true, option: true)
        let altUp = KeyChord(.up, option: true), altDown = KeyChord(.down, option: true)
        #expect(CommandRegistry.resolve(up, in: .wall) == .jumpBand(-1))
        #expect(CommandRegistry.resolve(down, in: .wall) == .jumpBand(1))
        #expect(CommandRegistry.resolve(altUp, in: .wall) == .jumpBand(-1))
        #expect(CommandRegistry.resolve(altDown, in: .wall) == .jumpBand(1))
        // ⌥⌘ is everywhere, ⌥ alone stays the wall's (an agent's key elsewhere).
        #expect(CommandRegistry.resolve(up, in: .focus) == .jumpBand(-1))
        #expect(CommandRegistry.resolve(altUp, in: .focus) == nil)
        #expect(KeyRouter.command(for: .jumpBand(-1)) == .jumpBand(-1))
        #expect(KeyRouter.command(for: .jumpBand(1)) == .jumpBand(1))
        #expect(KeyRouter.route(altDown, mode: .wall, selectedState: .working) == .jumpBand(1))
    }

    @Test func bandJumpIsInTheViewMenu() {
        let view = MenuModel.build()[.view]
        #expect(view.contains(.command(.jumpBand(-1), title: "Previous Band", chord: KeyChord(.up, command: true, option: true))))
        #expect(view.contains(.command(.jumpBand(1), title: "Next Band", chord: KeyChord(.down, command: true, option: true))))
    }
}
