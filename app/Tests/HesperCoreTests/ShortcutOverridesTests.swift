import Foundation
import Testing
@testable import HesperCore

// Per-command shortcut overrides: an override moves a chord, a
// conflicting one is reported and not applied, `unbound` takes the chord
// away, reset restores the default, and the stored JSON round-trips.
// None of these install a table: `CommandRegistry.active` is process
// state the other suites read in parallel.

@Suite struct ShortcutOverridesTests {
    private func table(_ o: ShortcutOverrides) -> CommandTable { CommandTable(CommandRegistry.resolved(with: o)) }

    @Test func anOverrideMovesTheChord() {
        let o = ShortcutOverrides().rebinding(.tidyUp, to: .chord(.cmd("u", shift: true)))
        guard case .applied(let overrides) = o else { Issue.record("refused: \(o)"); return }
        let t = table(overrides)
        #expect(t.resolve(.cmd("u", shift: true), in: .wall) == .tidyUp)
        #expect(t.resolve(.cmd("u", shift: true), in: .review) == .tidyUp, "an everywhere chord stays everywhere")
        #expect(t.resolve(.cmd("w", shift: true), in: .wall) == nil, "the default chord is free")
        #expect(t.command(.tidyUp)?.chord == .cmd("u", shift: true))
        let rows = ShortcutsSheet.sections(registry: t.commands, current: .everywhere, overrides: overrides).flatMap(\.rows)
        #expect(rows.first { $0.id == .tidyUp } == ShortcutsSheet.Row(id: .tidyUp, title: "Tidy Up Finished Agents", chord: "⇧⌘U", primary: "⇧⌘U",
                                                                       overridden: true, rebindable: true))
        #expect(rows.first { $0.id == .stepNext } == ShortcutsSheet.Row(id: .stepNext, title: "Next Agent", chord: "⌥⌘→ ⇥", primary: "⌥⌘→",
                                                                         aliases: ["⇥"], rebindable: true))
        #expect(rows.first { $0.id == .quit }?.rebindable == false)
        #expect(MenuModel.build(from: t.commands)[.agents].contains(.command(.tidyUp, title: "Tidy Up Finished Agents", chord: .cmd("u", shift: true))),
                "the menu bar follows the override")
        // Aliases are not the override's: ⇥ still steps on the wall.
        let step = ShortcutOverrides().rebinding(.stepNext, to: .chord(.cmd("]")))
        guard case .applied(let o2) = step else { Issue.record("refused: \(step)"); return }
        #expect(table(o2).resolve(.plain(.tab), in: .wall) == .stepNext)
        #expect(table(o2).resolve(.cmd("]"), in: .focus) == .stepNext)
        #expect(ShortcutConflicts.check(CommandRegistry.all, o2).isEmpty)
    }

    @Test func aConflictingOverrideIsReportedNotApplied() {
        // ⌘N is New Agent everywhere: Tidy Up can't take it.
        let o = ShortcutOverrides().rebinding(.tidyUp, to: .chord(.cmd("n")))
        guard case .refused(let reason) = o else { Issue.record("applied: \(o)"); return }
        #expect(reason.contains("New Agent"))
        // A surface chord conflicts only within its surface: Review's A is
        // free on the wall, taken in Review.
        guard case .applied = ShortcutOverrides().rebinding(.answer(.always), to: .chord(.plain("v"))) else { Issue.record("V on the wall refused"); return }
        guard case .refused(let r2) = ShortcutOverrides().rebinding(.reviewReject, to: .chord(.plain("v"))) else { Issue.record("V in Review applied"); return }
        #expect(r2.contains("Seen"))
        // The check names the surface and both commands for a table that
        // was stored with a clash (a later version bound the chord).
        var stale = ShortcutOverrides()
        stale.set(.reviewReject, to: .chord(.plain("v")))
        let found = ShortcutConflicts.check(CommandRegistry.all, stale)
        #expect(found == [ShortcutConflicts.Conflict(surface: .review, chord: .plain("v"), commands: [.reviewSeen, .reviewReject])])
        #expect(ShortcutConflicts.titles(for: .reviewReject, in: found, registry: CommandRegistry.all) == ["Seen"])
        #expect(ShortcutConflicts.titles(for: .reviewSeen, in: found, registry: CommandRegistry.all) == ["Reject the hunk"])
        // The sheet's pill shows it.
        let rows = ShortcutsSheet.sections(registry: CommandRegistry.resolved(with: stale), current: .review).flatMap(\.rows)
        #expect(rows.first { $0.id == .reviewReject }?.conflicts == ["Seen"])
    }

    @Test func reservedAndFixedChordsAreRefused() {
        for k in [KeyChord.cmd("q"), .cmd("h"), .cmd("m"), .cmd(",")] {
            guard case .refused(let reason) = ShortcutOverrides().rebinding(.tidyUp, to: .chord(k)) else { Issue.record("\(k.label) applied"); return }
            #expect(reason.contains("macOS"), "\(k.label): \(reason)")
        }
        // The standard items keep macOS's chords; the hotkey is Settings › General's.
        for id in [CommandID.quit, .copy, .minimize, .quickLaunch] {
            #expect(!ShortcutOverrides.rebindable(id))
            guard case .refused = ShortcutOverrides().rebinding(id, to: .chord(.cmd("u", shift: true))) else { Issue.record("\(id) applied"); return }
        }
        #expect(ShortcutOverrides.rebindable(.tidyUp) && ShortcutOverrides.rebindable(.reviewSeen))
        // Where the agent has the keyboard a chord needs ⌘.
        guard case .refused(let plain) = ShortcutOverrides().rebinding(.tidyUp, to: .chord(.plain("t"))) else { Issue.record("plain T everywhere applied"); return }
        #expect(plain.contains("⌘"))
        guard case .refused = ShortcutOverrides().rebinding(.newAgent, to: .chord(KeyChord(.other, command: true))) else { Issue.record("an unknown key applied"); return }
    }

    @Test func unboundTakesTheChordAwayAndResetRestoresIt() {
        guard case .applied(let o) = ShortcutOverrides().rebinding(.tidyUp, to: .unbound) else { Issue.record("unbound refused"); return }
        let t = table(o)
        #expect(t.resolve(.cmd("w", shift: true), in: .wall) == nil)
        #expect(t.command(.tidyUp)?.chord == nil)
        #expect(!ShortcutsSheet.sections(registry: t.commands, current: .wall).flatMap(\.rows).contains { $0.id == .tidyUp },
                "an unbound command has no row in the ⌘/ sheet")
        // Settings keeps the row (under Everywhere, where ⇧⌘W lived) so it can be reset.
        let settings = ShortcutsSheet.sections(registry: t.commands, current: .everywhere, overrides: o)
        let row = settings.first { $0.surface == .everywhere }?.rows.first { $0.id == .tidyUp }
        #expect(row == ShortcutsSheet.Row(id: .tidyUp, title: "Tidy Up Finished Agents", chord: "", primary: nil, overridden: true, rebindable: true))
        #expect(MenuModel.build(from: t.commands)[.agents].contains(.command(.tidyUp, title: "Tidy Up Finished Agents", chord: nil)))
        // The chord is free for another command now.
        guard case .applied(let o2) = o.rebinding(.killAgent, to: .chord(.cmd("w", shift: true))) else { Issue.record("the freed chord refused"); return }
        #expect(table(o2).resolve(.cmd("w", shift: true), in: .wall) == .killAgent)
        // Reset one: back to the default; the other override stays.
        var o3 = o2
        o3.reset(.killAgent)
        #expect(o3[.killAgent] == nil && o3[.tidyUp] == .unbound)
        #expect(table(o3).resolve(.cmd("w", control: true), in: .wall) == .killAgent)
        // Rebinding to the default is a reset.
        guard case .applied(let o4) = o3.rebinding(.tidyUp, to: .chord(.cmd("w", shift: true))) else { Issue.record("the default refused"); return }
        #expect(o4.isEmpty)
        #expect(table(o4).commands.map(\.chord) == CommandRegistry.all.map(\.chord))
        // Reset all.
        var o5 = o2
        o5.resetAll()
        #expect(o5.isEmpty)
    }

    @Test func jsonRoundTripsAndKeepsUnknownCommands() throws {
        var o = ShortcutOverrides()
        o.set(.tidyUp, to: .chord(KeyChord(.char("u"), command: true, shift: true)))
        o.set(.reviewSeen, to: .unbound)
        o.set(.arrangement(2), to: .chord(.cmd(.left, option: true, control: true)))
        let data = try o.json()
        let object = try #require(JSONSerialization.jsonObject(with: data) as? [String: Any])
        #expect(object["tidyUp"] as? String == "⇧⌘U")
        #expect(object["reviewSeen"] is NSNull)
        #expect(object["arrangement(2)"] as? String == "⌃⌥⌘←")
        #expect(try ShortcutOverrides(json: data) == o)
        // A key of a command this version doesn't have is kept, not applied.
        let stale = Data(#"{"tidyUp":"⇧⌘U","notACommand":"⌘1","bad":"nonsense"}"#.utf8)
        let s = try ShortcutOverrides(json: stale)
        #expect(s[.tidyUp] == .chord(KeyChord(.char("u"), command: true, shift: true)))
        #expect(s.count == 2, "an unparsable chord is dropped")
        #expect(table(s).resolve(.cmd("1"), in: .wall) == nil)
        #expect(try ShortcutOverrides(json: s.json()).count == 2)
    }
}
