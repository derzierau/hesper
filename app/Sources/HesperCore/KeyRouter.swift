import Foundation

/// `compose`: typing in a draft tile (or the quick launch panel); the wall
/// stays live, only the composer's keys differ.
public enum AppMode: Sendable, Equatable { case wall, focus, compose }

public enum Key: Hashable, Sendable {
    case char(Character), enter, escape, left, right, up, down, home, end, tab, delete, other
}

public struct KeyChord: Hashable, Sendable {
    public var key: Key
    public var command = false, shift = false, option = false, control = false
    public init(_ key: Key, command: Bool = false, shift: Bool = false, option: Bool = false, control: Bool = false) {
        self.key = key; self.command = command; self.shift = shift; self.option = option; self.control = control
    }
    public var plain: Bool { !command && !option && !control }

    /// From a key event: `keyCode` is the physical key, `characters` what
    /// the layout makes of it without ⌘ (`charactersIgnoringModifiers`).
    /// Digits, brackets, braces and slash come from the key code so a chord
    /// means the same on every layout (on German ⌥5 types `[`, and ⌥⌘5
    /// must stay the Grid layout); ⌫ is `.delete`, not a control character.
    /// A letter is lowercase and ⇧ comes from the flags alone: with Caps
    /// Lock on the layout says "W" for ⌘W, which is not ⇧⌘W.
    public init(keyCode: UInt16, characters: String?, command: Bool = false, shift: Bool = false, option: Bool = false, control: Bool = false) {
        let key: Key
        switch keyCode {
        case 36, 76: key = .enter
        case 53: key = .escape
        case 51: key = .delete
        case 123: key = .left
        case 124: key = .right
        case 125: key = .down
        case 126: key = .up
        case 48: key = .tab
        case 115: key = .home
        case 119: key = .end
        case 29: key = .char("0")
        case 18: key = .char("1")
        case 19: key = .char("2")
        case 20: key = .char("3")
        case 21: key = .char("4")
        case 23: key = .char("5")
        case 22: key = .char("6")
        case 26: key = .char("7")
        case 28: key = .char("8")
        case 25: key = .char("9")
        case 33: key = .char(shift ? "{" : "[")
        case 30: key = .char(shift ? "}" : "]")
        case 44: key = .char("/")
        default:
            if let c = characters?.first { key = .char(c.isLetter ? Character(c.lowercased()) : c) } else { key = .other }
        }
        self.init(key, command: command, shift: shift, option: option, control: control)
    }
}

public enum AppCommand: Equatable, Sendable {
    case newAgent, nextAttention, palette
    case toggleFocus, exitFocus
    case stepPrevious, stepNext
    /// Closing agents: ⌘W close (a draft: discard), ⌥⌘W background,
    /// ⌃⌘W kill, ⇧⌘W tidy up, ⇧⌘T reopen the last closed one.
    case closeAgent, backgroundAgent, killAgent, tidyUp, reopenClosed
    case moveAgent
    /// The wall's selection: ← → ↑ ↓ spatially, Home / End (WallNavigation).
    /// Never attaches read-write or resizes anything.
    case select(WallMove)
    /// ⌃⌥ + arrow on the wall or in the active tile: the neighbor that way
    /// becomes the active tile (read-write; typing goes there).
    case activate(WallMove)
    /// A printable key on the selected tile: it becomes the active tile and
    /// gets the text ("just start typing"); on a selected draft, its editor.
    case typeInto(String)
    case answer(Decision)
    case denyWithMessage
    /// Drafts: start (⌘↩), start and open another (⌥↩), toggle the
    /// worktree (⌥⌘B), leave as a quiet tile (esc), edit the selected one.
    case startDraft, startDraftAndNew, toggleWorktree, leaveDraft, editDraft
    /// ⌘Z outside a text field: the newest undo toast.
    case undo
    /// ⏎ on a selected tile: type into it (the active tile).
    case activateTile
    /// The wall's arrangement: by number (⌥⌘1…5, WallArrangement order) or
    /// the next one (⌥⌘L).
    case arrangement(Int)
    case cycleArrangement
    /// ⌥↑ / ⌥↓ on the wall: the first card of the previous / next band.
    case jumpBand(Int)
    /// ⌘0: the wall's project sidebar.
    case toggleSidebar
    /// ⌘Y: shared history (the History panel).
    case history
    /// ⌘R: the review sheet (finished work on every Mac); ⏎ on a finished
    /// tile that is ready to review opens it there.
    case review
    /// ⌘L the loops board, ⇧⌘L the product page of the wall's scope, ⇧⌘E
    /// the evening wrap-up.
    case loops, product, wrapUp
    /// Belongs to the terminal (focus mode).
    case passThrough
    case none
}

/// Maps keys to app commands. One table for both modes, so the wall and the
/// focus view agree on every ⌘ shortcut and only plain keys differ: on the
/// wall they drive selection and inline approvals; in focus they go to the
/// agent. Esc always belongs to the agent (claude/codex use it to interrupt),
/// so focus is left with ⌘↩ or ⌘Esc.
///
/// The wall has two levels of keyboard focus on a tile: **selected** (a
/// highlight ring; keys are the wall's: ← → ↑ ↓ Home End move, ⏎ types,
/// and on a tile that needs you ⏎ / A / N answer first) and **active**
/// (`routeActiveTile`: the agent gets every key but the app's ⌘ chords).
/// Plain ← → in focus always belong to the agent; ⌥⌘← ⌥⌘→ step to the
/// previous / next agent everywhere.
public enum KeyRouter {
    /// The AppCommand a registry command runs (nil: not the router's, the
    /// menu or a panel handles it).
    public static func command(for id: CommandID) -> AppCommand? {
        switch id {
        case .newAgent: return .newAgent
        case .nextAttention: return .nextAttention
        case .palette: return .palette
        case .toggleFocus: return .toggleFocus
        case .exitFocus: return .exitFocus
        case .stepPrevious: return .stepPrevious
        case .stepNext: return .stepNext
        case .closeAgent: return .closeAgent
        case .backgroundAgent: return .backgroundAgent
        case .killAgent: return .killAgent
        case .tidyUp: return .tidyUp
        case .reopenClosed: return .reopenClosed
        case .moveAgent: return .moveAgent
        case .undo: return .undo
        case .arrangement(let i): return .arrangement(i)
        case .cycleArrangement: return .cycleArrangement
        case .jumpBand(let d): return .jumpBand(d)
        case .toggleSidebar: return .toggleSidebar
        case .history: return .history
        case .review: return .review
        case .loops: return .loops
        case .product: return .product
        case .wrapUp: return .wrapUp
        case .select(let m): return .select(m)
        case .activate(let m): return .activate(m)
        case .activateTile: return .activateTile
        case .answer(let d): return .answer(d)
        case .denyWithMessage: return .denyWithMessage
        case .startDraft: return .startDraft
        case .startDraftAndNew: return .startDraftAndNew
        case .toggleWorktree: return .toggleWorktree
        case .leaveDraft: return .leaveDraft
        default: return nil
        }
    }

    /// - Parameter typed: the text the key produced (`NSEvent.characters`);
    ///   a printable one on a selected agent or draft is `.typeInto`.
    /// - Parameter selectedReviewable: the selected agent is ready to
    ///   review (its footer says so): ⏎ opens the review sheet on it.
    public static func route(_ k: KeyChord, mode: AppMode, selectedState: AgentState?, selectedIsDraft: Bool = false, selectedTrust: Bool = false,
                             selectedAnswers: [Decision] = [], typed: String? = nil, selectedReviewable: Bool = false) -> AppCommand {
        if mode == .compose { return compose(k) }
        if let id = CommandRegistry.resolve(k, in: mode == .focus ? .focus : .wall), let cmd = command(for: id) {
            switch cmd {
            case .activateTile:
                // Precedence on the selected tile: a draft edits; a tile that
                // needs you answers (⏎ = Allow / the first answer); otherwise
                // ⏎ starts typing into it.
                if selectedIsDraft { return .editDraft }
                if let first = selectedAnswers.first, selectedState == .question { return .answer(first) }
                if selectedTrust && selectedState == .question { return .answer(.trust) }
                if selectedReviewable && !(selectedState?.needsAttention ?? false) { return .review }
                return selectedState == .approval ? .answer(.allow) : (selectedState == nil ? .none : .activateTile)
            case .answer, .denyWithMessage:
                if selectedState == .approval { return cmd } // else A / N type into the tile
            default:
                return cmd
            }
        }
        // Not the app's. In focus everything else is the agent's; on the
        // wall a printable key types into the selected tile (activates it).
        if mode == .focus { return .passThrough }
        if k.command || k.control { return .none }
        let hasTarget = selectedState != nil || selectedIsDraft
        let text = typed.flatMap { TerminalKeys.isPrintable($0) ? $0 : nil }
        return hasTarget ? text.map(AppCommand.typeInto) ?? .none : .none
    }

    /// The active wall tile has the keyboard: like focus, every key goes to
    /// the agent (Esc, ⏎, arrows, ⌃-keys, ⌘C/⌘V) except the app's ⌘
    /// shortcuts; ⌘Esc leaves the tile (exitFocus), ⌘↩ opens it full size.
    public static func routeActiveTile(_ k: KeyChord) -> AppCommand {
        CommandRegistry.resolve(k, in: .activeTile).flatMap(command(for:)) ?? .passThrough
    }

    /// Typing in a composer: only the registry's composer keys are the
    /// app's; everything else edits the text. ⌘Z is the text's undo (Edit ▸
    /// Undo reaches the field through the responder chain).
    static func compose(_ k: KeyChord) -> AppCommand {
        guard let id = CommandRegistry.resolve(k, in: .composer), let cmd = command(for: id), cmd != .undo else { return .passThrough }
        return cmd
    }
}
