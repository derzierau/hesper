import Foundation
import Testing
@testable import HesperCore

// The command registry's invariants: no chord is bound twice within a
// surface, every chord has a menu path or a reason not to, the README
// agrees with the table, and the predicates say disabled or hidden where
// the menus must show it.

@Suite struct CommandRegistryTests {
    /// A chord bound in `.everywhere` conflicts with the same chord in
    /// any surface; two commands never share a chord in one surface.
    @Test func noChordIsBoundTwiceWithinASurface() {
        for s in Surface.concrete {
            var seen: [KeyChord: CommandID] = [:]
            for c in CommandRegistry.all {
                for b in c.bindings where CommandRegistry.live(b, in: s) {
                    if let other = seen[b.chord] {
                        Issue.record("\(b.chord.label) in \(s) is both \(other) and \(c.id)")
                    }
                    seen[b.chord] = c.id
                }
            }
        }
    }

    /// Commands with a chord but no menu item, each with its reason: the
    /// key cap sits on a control of the surface instead.
    static let menuExemptions: [CommandID: String] = {
        var e: [CommandID: String] = [:]
        for m in [WallMove.left, .right, .up, .down, .first, .last] { e[.select(m)] = "wall selection: the tile ring shows it" }
        for m in [WallMove.left, .right, .up, .down] { e[.activate(m)] = "move and type: a click on the tile does the same" }
        e[.activateTile] = "the tile's primary button carries ⏎"
        for id in [CommandID.startDraft, .startDraftAndNew, .toggleWorktree, .leaveDraft] { e[id] = "the composer's Start button and chips carry the keys" }
        for id in [CommandID.moveUp, .moveDown, .closeOverlay, .overlayActivate, .overlayAlternate, .overlayActOn, .overlayBack, .overlayDeleteBackward] {
            e[id] = "overlay family: the footer hints and rows carry the keys"
        }
        e[.closeTrayItem] = "the background tray's rows carry ⌘W"
        for id in [CommandID.inboxPrimary, .inboxOpen] { e[id] = "inbox rows carry the key caps" }
        for id in [CommandID.historyResume, .historyOpenFullSize, .historyFork, .historyContinueOnOtherMac, .historyContinueOther, .historyArchive,
                   .historyDelete, .historyRestoreCheckpoint, .historySwitchScope, .historyToggleFocus, .historyFirst, .historyLast] {
            e[id] = "History's toolbar, preview buttons, row menu and footer carry the keys"
        }
        for id in [CommandID.reviewAccept, .reviewSendBack, .reviewProvenance, .reviewToggleFold, .reviewNextHunk, .reviewPreviousHunk,
                   .reviewNextFile, .reviewPreviousFile, .reviewSeen, .reviewReject, .reviewNote, .reviewBulkAccept] {
            e[id] = "Review's toolbar and hunk bars carry the keys"
        }
        return e
    }()

    @Test func everyChordHasAMenuPathOrAnExemption() {
        for c in CommandRegistry.all where c.chord != nil && c.menu == nil {
            #expect(Self.menuExemptions[c.id] != nil, "\(c.id) (\(c.chord!.label)) has no menu item and no exemption")
        }
        // Exemptions are not a dumping ground: each names a real chord-only command.
        for (id, reason) in Self.menuExemptions {
            let c = CommandRegistry.command(id)
            #expect(c != nil && c?.menu == nil && c?.chord != nil, "\(id) is exempt (\(reason)) but is not a chord-only command")
        }
    }

    @Test func idsAreUnique() {
        var seen: Set<CommandID> = []
        for c in CommandRegistry.all { #expect(seen.insert(c.id).inserted, "\(c.id) twice") }
    }

    // MARK: README

    /// The "Using it" table's key column, as chords.
    static func readmeChords() throws -> [(String, KeyChord)] {
        let url = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent()
            .deletingLastPathComponent().deletingLastPathComponent().appendingPathComponent("README.md")
        let text = try String(contentsOf: url, encoding: .utf8)
        guard let start = text.range(of: "\n## Using it\n") else { throw ReadmeError.noTable }
        var out: [(String, KeyChord)] = []
        var inTable = false
        for line in text[start.upperBound...].split(separator: "\n", omittingEmptySubsequences: false) {
            if line.hasPrefix("| Key") || line.hasPrefix("|---") { inTable = true; continue }
            guard inTable else { continue }
            guard line.hasPrefix("|") else { if !out.isEmpty { break } else { continue } }
            let cell = line.split(separator: "|", omittingEmptySubsequences: false)[1].trimmingCharacters(in: .whitespaces)
            // "⌥⌘W / ⌃⌘W" are alternatives; a word that is one chord ("⌘/") stays whole.
            for word in cell.split(separator: " ") where word != "/" {
                for token in KeyChord(label: String(word)) != nil ? [word] : word.split(separator: "/") {
                    // "⌥⌘1–5": a range of digits.
                    if let dash = token.firstIndex(of: "–"), token.index(after: dash) < token.endIndex,
                       let lo = token[token.index(before: dash)].wholeNumberValue, let hi = token[token.index(after: dash)].wholeNumberValue {
                        let mods = String(token[..<token.index(before: dash)])
                        for d in lo...hi {
                            guard let k = KeyChord(label: mods + String(d)) else { throw ReadmeError.badToken(String(token)) }
                            out.append((String(token), k))
                        }
                        continue
                    }
                    guard let k = KeyChord(label: String(token)) else { throw ReadmeError.badToken(String(token)) }
                    out.append((String(token), k))
                }
            }
        }
        if out.isEmpty { throw ReadmeError.noTable }
        return out
    }

    enum ReadmeError: Error { case noTable, badToken(String) }

    @Test func everyReadmeShortcutIsRegistered() throws {
        let bound = Set(CommandRegistry.all.flatMap { $0.bindings.map(\.chord) })
        for (token, chord) in try Self.readmeChords() {
            #expect(bound.contains(chord.normalized), "README lists \(token), the registry doesn't")
        }
    }

    // MARK: Availability

    @Test func closeKillMoveNeedASelectedAgent() {
        let none = CommandContext(selectedState: nil, keyWindow: .wall)
        for id in [CommandID.closeAgent, .killAgent, .moveAgent, .backgroundAgent] {
            guard case .disabled = CommandRegistry.command(id)!.availability(none) else { Issue.record("\(id) enabled with nothing selected"); continue }
        }
        let selected = CommandContext(selectedState: .working, selectedRunning: true, keyWindow: .wall)
        for id in [CommandID.closeAgent, .killAgent, .moveAgent, .backgroundAgent] {
            #expect(CommandRegistry.command(id)!.availability(selected) == .enabled)
        }
        // ⌘W discards a selected draft; in a plain window it closes the window.
        #expect(CommandRegistry.command(.closeAgent)!.availability(CommandContext(hasDraft: true)) == .enabled)
        #expect(CommandRegistry.command(.closeAgent)!.availability(CommandContext(keyWindow: .other)) == .enabled)
        #expect(CommandRegistry.command(.closeAgent)!.availability(CommandContext(selectedState: .working, keyWindow: .agentWindow)) == .enabled)
        // Under a sheet or an overlay on a wall ⌘W is nobody's: File ▸ Close
        // is disabled, so the key can't reach the agent beneath (the tray's
        // ⌘W is closeTrayItem; an agent window's ⌘W closes the window).
        for open in [CommandContext(selectedState: .working, keyWindow: .wall, historyOpen: true),
                     CommandContext(selectedState: .working, keyWindow: .wall, reviewOpen: true),
                     CommandContext(selectedState: .working, keyWindow: .wall, paletteOpen: true),
                     CommandContext(selectedState: .working, keyWindow: .wall, popoverOpen: true),
                     CommandContext(keyWindow: .wall, hasDraft: true, popoverOpen: true),
                     CommandContext(selectedState: .working, keyWindow: .wall, shortcutsOpen: true)] {
            guard case .disabled = CommandRegistry.command(.closeAgent)!.availability(open) else { Issue.record("Close enabled under a sheet: \(open)"); continue }
        }
        #expect(CommandRegistry.command(.closeAgent)!.availability(CommandContext(selectedState: .working, keyWindow: .agentWindow, popoverOpen: true)) == .enabled)
        // Agent-lifecycle chords do nothing off the wall.
        guard case .disabled = CommandRegistry.command(.killAgent)!.availability(CommandContext(selectedState: .working, keyWindow: .other)) else {
            Issue.record("Kill enabled in Settings"); return
        }
    }

    @Test func routinesIsHidden() {
        #expect(CommandRegistry.command(.routines)!.availability(.none) == .hidden)
        #expect(CommandRegistry.command(.routines)!.availability(CommandContext(selectedState: .working)) == .hidden)
    }

    @Test func approvalsNeedAnApproval() {
        for id in [CommandID.answer(.allow), .answer(.always), .denyWithMessage] {
            #expect(CommandRegistry.command(id)!.availability(CommandContext(selectedState: .approval)) == .enabled)
            guard case .disabled = CommandRegistry.command(id)!.availability(CommandContext(selectedState: .working)) else {
                Issue.record("\(id) enabled on a working agent"); continue
            }
        }
    }

    // The generated menu bar against the spec's table: MenuModelTests.

    // MARK: Routers


    /// Every registry chord, in every surface it is live in, comes out of
    /// that surface's router as the registered command (or as "not mine"
    /// where the surface has no action for it: the menu runs it).
    @Test func routersResolveEveryRegisteredChord() {
        for c in CommandRegistry.all {
            for b in c.bindings {
                for s in Surface.concrete where CommandRegistry.live(b, in: s) {
                    check(c.id, b.chord, in: s)
                }
            }
        }
    }

    private func check(_ id: CommandID, _ k: KeyChord, in s: Surface) {
        let typed: String? = { if case .char(let ch) = k.key, k.plain { return k.shift ? String(ch).uppercased() : String(ch) }; return nil }()
        let app = KeyRouter.command(for: id)
        let where_ = Comment(rawValue: "\(id) \(k.label) in \(s)")
        switch s {
        case .wall:
            let state: AgentState = { if case .answer = id { return .approval }; return id == .denyWithMessage ? .approval : .working }()
            #expect(KeyRouter.route(k, mode: .wall, selectedState: state) == (app ?? .none), where_)
        case .focus, .agentWindow:
            #expect(KeyRouter.route(k, mode: .focus, selectedState: .working) == (app ?? .passThrough), where_)
        case .activeTile:
            #expect(KeyRouter.routeActiveTile(k) == (app ?? .passThrough), where_)
        case .composer, .quickLaunch:
            let expected: AppCommand = id == .undo ? .passThrough : (app ?? .passThrough) // ⌘Z is the text's undo
            #expect(KeyRouter.route(k, mode: .compose, selectedState: nil) == expected, where_)
        case .palette, .popover:
            #expect(OverlayKeys.route(k, characters: typed, surface: s) == (OverlayKeys.action(for: id) ?? .pass), where_)
        case .inbox:
            #expect(InboxKeys.route(OverlayKeys.route(k, characters: typed, surface: .inbox)) == (InboxKeys.action(for: id) ?? .none), where_)
        case .history:
            #expect(SearchKeys.history(k, characters: typed, focus: .list) == (SearchKeys.action(for: id) ?? .history(.pass)), where_)
        case .review:
            #expect(ReviewKeys.route(k) == (ReviewKeys.action(for: id) ?? .pass), where_)
        case .settings, .everywhere:
            break // no router: the menu's key equivalents
        }
    }

    // MARK: Chords

    @Test func labelsRoundTrip() {
        for c in CommandRegistry.all {
            for b in c.bindings {
                #expect(KeyChord(label: b.chord.label) == b.chord.normalized, "\(c.id): \(b.chord.label)")
            }
        }
        #expect(KeyChord(.char("w"), command: true, shift: true).label == "⇧⌘W")
        #expect(KeyChord(.left, command: true, option: true).label == "⌥⌘←")
        #expect(KeyChord(.escape, command: true).label == "⌘esc")
        #expect(KeyChord(label: "⌘↩") == KeyChord(.enter, command: true))
        #expect(KeyChord(label: "⌘⇧M") == KeyChord(.char("m"), command: true, shift: true))
        #expect(KeyChord(label: "") == nil && KeyChord(label: "⌘") == nil && KeyChord(label: "mini") == nil)
    }

    @Test func resolveNormalizesLetters() {
        #expect(CommandRegistry.resolve(KeyChord(.char("W"), command: true, shift: true), in: .wall) == .tidyUp)
        #expect(CommandRegistry.resolve(KeyChord(.char("W"), command: true), in: .wall) == .tidyUp)
        #expect(CommandRegistry.resolve(KeyChord(.char("w"), command: true), in: .wall) == .closeAgent)
        // A shifted plain letter falls back to the unshifted one; ⌘ chords don't.
        #expect(CommandRegistry.resolve(KeyChord(.char("A"), shift: true), in: .wall) == .answer(.always))
        #expect(CommandRegistry.resolve(KeyChord(.char("n"), command: true, shift: true), in: .wall) == nil)
        // Surfaces: ⌘W is the tray's in a popover, ⌘⏎ differs per surface.
        #expect(CommandRegistry.resolve(KeyChord(.char("w"), command: true), in: .popover) == .closeTrayItem)
        #expect(CommandRegistry.resolve(KeyChord(.enter, command: true), in: .wall) == .toggleFocus)
        #expect(CommandRegistry.resolve(KeyChord(.enter, command: true), in: .composer) == .startDraft)
        #expect(CommandRegistry.resolve(KeyChord(.enter, command: true), in: .review) == .reviewAccept)
        #expect(CommandRegistry.resolve(KeyChord(.char("k"), command: true), in: .history) == .palette, "everywhere reaches every surface")
        #expect(CommandRegistry.resolve(KeyChord(.char("j")), in: .wall) == nil)
        // The inbox binds no letter or digit: they type into its filter.
        for c in "anmjk19" { #expect(CommandRegistry.resolve(KeyChord(.char(c)), in: .inbox) == nil, "\(c)") }
        #expect(CommandRegistry.resolve(KeyChord(.char("n")), in: .wall) == .denyWithMessage)
    }
}
