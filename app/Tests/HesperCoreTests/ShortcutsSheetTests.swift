import Foundation
import Testing
@testable import HesperCore

// The shortcuts sheet's sections: every bound command once, the current
// surface first, the query over titles and key caps, the conflict pill.

@Suite struct ShortcutsSheetTests {
    private func rows(_ sections: [ShortcutsSheet.Section]) -> [ShortcutsSheet.Row] { sections.flatMap(\.rows) }

    @Test func everyBoundCommandAppearsExactlyOnce() {
        for current in [Surface.wall, .review, .settings] {
            let all = rows(ShortcutsSheet.sections(current: current, hotKey: "⌃⌥Space"))
            var counts: [CommandID: Int] = [:]
            for r in all { counts[r.id, default: 0] += 1 }
            for c in CommandRegistry.all where !c.bindings.isEmpty && c.availability(.none) != .hidden {
                #expect(counts[c.id] == 1, "\(c.id) appears \(counts[c.id] ?? 0) times with current \(current)")
            }
            for c in CommandRegistry.all where c.bindings.isEmpty && c.id != .quickLaunch {
                #expect(counts[c.id] == nil, "\(c.id) has no chord but a row")
            }
            #expect(counts[.routines] == nil)
            #expect(all.allSatisfy { $0.conflicts.isEmpty })
        }
    }

    @Test func currentSurfaceComesFirstAndIsExpanded() {
        let sections = ShortcutsSheet.sections(current: .review)
        #expect(sections.first?.surface == .review)
        #expect(sections.first?.expanded == true)
        #expect(sections.dropFirst().allSatisfy { !$0.expanded })
        // Its rows are the surface's own; app-wide chords stay under Everywhere.
        #expect(sections[0].rows.contains { $0.id == .reviewAccept && $0.chord == "⌘⏎" })
        #expect(sections[0].rows.contains { $0.id == .closeOverlay })
        #expect(sections.first { $0.surface == .everywhere }?.rows.contains { $0.id == .newAgent && $0.chord == "⌘N" } == true)
        // Not in Review: Close (esc) sits with the first list surface that binds it.
        #expect(ShortcutsSheet.sections(current: .wall).first { $0.surface == .palette }?.rows.contains { $0.id == .closeOverlay } == true)
    }

    @Test func aliasesShowWithTheChord() {
        let everywhere = ShortcutsSheet.sections(current: .wall).first { $0.surface == .everywhere }
        #expect(everywhere?.rows.first { $0.id == .stepNext }?.chord == "⌥⌘→ ⇥")
    }

    @Test func queryFindsCloseAndNothingUnrelated() {
        let sections = ShortcutsSheet.sections(current: .wall, query: "close")
        let found = rows(sections)
        #expect(found.contains { $0.id == .closeAgent })
        #expect(found.contains { $0.id == .closeOverlay })
        #expect(found.allSatisfy { $0.title.lowercased().contains("close") || $0.chord.lowercased().contains("close") })
        #expect(!found.contains { $0.id == .newAgent })
        #expect(sections.allSatisfy { $0.expanded })
        // Key caps match too.
        #expect(rows(ShortcutsSheet.sections(current: .wall, query: "⌥⌘L")).map(\.id) == [.cycleArrangement])
        #expect(rows(ShortcutsSheet.sections(current: .wall, query: "zzzz")).isEmpty)
    }

    @Test func quickLaunchRowCarriesTheHotKeyAndItsConflict() {
        let off = rows(ShortcutsSheet.sections(current: .wall, hotKey: nil))
        #expect(!off.contains { $0.id == .quickLaunch })
        let on = rows(ShortcutsSheet.sections(current: .wall, hotKey: "⌃⌥Space", hotKeyConflict: "macOS input source switching"))
        let row = on.first { $0.id == .quickLaunch }
        #expect(row?.chord == "⌃⌥Space")
        #expect(row?.conflicts == ["macOS input source switching"])
    }

    @Test func aChordBoundTwiceInOneSurfaceNamesTheOther() {
        let registry = [
            Command(.tidyUp, "Tidy Up", chord: .cmd("x"), surfaces: [.wall]),
            Command(.killAgent, "Kill", chord: .cmd("x"), surfaces: [.everywhere]),
            Command(.renameAgent, "Rename", chord: .cmd("x"), surfaces: [.history]),
        ]
        let found = rows(ShortcutsSheet.sections(registry: registry, current: .wall))
        #expect(found.first { $0.id == .tidyUp }?.conflicts == ["Kill"])
        #expect(found.first { $0.id == .killAgent }?.conflicts == ["Tidy Up", "Rename"])
        #expect(found.first { $0.id == .renameAgent }?.conflicts == ["Kill"])
    }
}
