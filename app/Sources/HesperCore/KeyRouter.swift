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
    /// A printable key on the selected tile: it becomes the active tile and
    /// gets the text ("just start typing"); on a selected draft, its editor.
    case typeInto(String)
    case answer(Decision)
    case denyWithMessage
    /// Drafts: start (⌘↩), start and open another (⌥↩), toggle the
    /// worktree (⌘⇧W), leave as a quiet tile (esc), edit the selected one.
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
/// Plain ← → in focus always belong to the agent; ⌥⌘← ⌥⌘→ (and ⌘[ ⌘])
/// step to the previous / next agent everywhere.
public enum KeyRouter {
    /// - Parameter typed: the text the key produced (`NSEvent.characters`);
    ///   a printable one on a selected agent or draft is `.typeInto`.
    /// - Parameter selectedReviewable: the selected agent is ready to
    ///   review (its footer says so): ⏎ opens the review sheet on it.
    public static func route(_ k: KeyChord, mode: AppMode, selectedState: AgentState?, selectedIsDraft: Bool = false, selectedTrust: Bool = false,
                             selectedAnswers: [Decision] = [], typed: String? = nil, selectedReviewable: Bool = false) -> AppCommand {
        if mode == .compose { return compose(k) }
        if k.command && k.option && !k.control && !k.shift {
            switch k.key {
            case .char(let c) where WallArrangement.numbered(Int(String(c)) ?? 0) != nil: return .arrangement(Int(String(c))! - 1)
            case .char("l"), .char("L"): return .cycleArrangement
            case .char("w"), .char("W"): return .backgroundAgent
            case .left: return .stepPrevious
            case .right: return .stepNext
            default: return mode == .focus ? .passThrough : .none
            }
        }
        if k.command && k.control && !k.option && !k.shift, k.key == .char("w") || k.key == .char("W") { return .killAgent }
        if k.command && !k.option && !k.control {
            switch k.key {
            case .char("n") where !k.shift: return .newAgent
            case .char("j") where !k.shift: return .nextAttention
            case .char("k") where !k.shift: return .palette
            case .enter: return .toggleFocus
            case .escape: return .exitFocus // focus: back to the wall; wall: the active tile back to selected
            case .char("["): return .stepPrevious
            case .char("]"): return .stepNext
            case .char("{"): return .stepPrevious
            case .char("}"): return .stepNext
            case .char("w") where !k.shift: return .closeAgent
            case .char("w"), .char("W"): return .tidyUp
            case .char("t") where k.shift, .char("T"): return .reopenClosed
            case .char("m") where k.shift: return .moveAgent
            case .char("M"): return .moveAgent
            case .char("z") where !k.shift: return .undo
            case .char("0") where !k.shift: return .toggleSidebar
            case .char("y") where !k.shift: return .history
            case .char("r") where !k.shift: return .review
            default: return mode == .focus ? .passThrough : .none
            }
        }
        if mode == .focus { return .passThrough }
        if k.option && !k.command && !k.control && !k.shift && (k.key == .up || k.key == .down) {
            return .jumpBand(k.key == .up ? -1 : 1)
        }
        if k.command || k.control { return .none }
        let hasTarget = selectedState != nil || selectedIsDraft
        let text = typed.flatMap { TerminalKeys.isPrintable($0) ? $0 : nil }
        // ⌥-keys only type (⌥L is @ on German keyboards).
        if k.option { return hasTarget ? text.map(AppCommand.typeInto) ?? .none : .none }
        let approval = selectedState == .approval
        switch k.key {
        case .left: return .select(.left)
        case .right: return .select(.right)
        case .up: return .select(.up)
        case .down: return .select(.down)
        case .home: return .select(.first)
        case .end: return .select(.last)
        case .tab: return k.shift ? .stepPrevious : .stepNext
        case .enter:
            // Precedence on the selected tile: a draft edits; a tile that
            // needs you answers (⏎ = Allow / the first answer); otherwise
            // ⏎ starts typing into it.
            if selectedIsDraft { return .editDraft }
            if let first = selectedAnswers.first, selectedState == .question { return .answer(first) }
            if selectedTrust && selectedState == .question { return .answer(.trust) }
            if selectedReviewable && !(selectedState?.needsAttention ?? false) { return .review }
            return approval ? .answer(.allow) : (selectedState == nil ? .none : .activateTile)
        case .char("a") where approval, .char("A") where approval: return .answer(.always)
        case .char("n") where approval, .char("N") where approval: return .denyWithMessage
        default: return hasTarget ? text.map(AppCommand.typeInto) ?? .none : .none
        }
    }

    /// The active wall tile has the keyboard: like focus, every key goes to
    /// the agent (Esc, ⏎, arrows, ⌃-keys, ⌘C/⌘V) except the app's ⌘
    /// shortcuts; ⌘Esc leaves the tile (exitFocus), ⌘↩ opens it full size.
    public static func routeActiveTile(_ k: KeyChord) -> AppCommand {
        route(k, mode: .focus, selectedState: nil)
    }

    /// Typing in a composer: only these keys are the app's; everything
    /// else edits the text.
    static func compose(_ k: KeyChord) -> AppCommand {
        if k.command && !k.option && !k.control {
            switch k.key {
            case .enter: return .startDraft
            case .char("w") where k.shift: return .toggleWorktree
            case .char("W"): return .toggleWorktree
            case .char("w"): return .closeAgent
            case .char("t") where k.shift, .char("T"): return .reopenClosed
            case .char("n") where !k.shift: return .newAgent
            case .char("j") where !k.shift: return .nextAttention
            case .char("k") where !k.shift: return .palette
            case .char("y") where !k.shift: return .history
            case .char("r") where !k.shift: return .review
            default: return .passThrough
            }
        }
        if k.command && k.option && !k.control && !k.shift {
            switch k.key {
            case .char(let c) where WallArrangement.numbered(Int(String(c)) ?? 0) != nil: return .arrangement(Int(String(c))! - 1)
            case .char("l"), .char("L"): return .cycleArrangement
            default: return .passThrough
            }
        }
        if k.option && !k.command && !k.control && k.key == .enter { return .startDraftAndNew }
        if k.key == .escape && !k.command && !k.option && !k.control && !k.shift { return .leaveDraft }
        return .passThrough
    }
}
