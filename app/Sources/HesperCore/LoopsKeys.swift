import Foundation

// Keys of the Loops (⌘L), Wrap-up (⇧⌘E) and Product (⇧⌘L) sheets, from
// the registry's `.loops`, `.wrapUp` and `.product` surfaces. Plain
// letters are the sheets' verbs; while a field has the keyboard (adding a
// step, a trigger, the merge or file picker's filter) only esc and ⏎ are
// the sheet's.

public enum LoopsKeyAction: Equatable, Sendable {
    case up, down, left, right
    /// ⏎: open the loop (the board, the product page).
    case open
    case done, park, drop, merge, nextAgent, startDraft, map, check, addStep, trigger, switchScope
    /// The wrap-up: ⏎ takes the suggestion, N the next step, U undoes,
    /// ⌘↩ saves the decisions.
    case accept, nextStep, undo, apply
    /// The product page: Y files, P plans, ⌘↩ saves the plan, X drops a
    /// proposal, ⇥ the next product.
    case file, plan, savePlan, dropProposal, nextScope
    /// Esc (back from a detail first), or the sheet's own chord again.
    case close
    /// In a field: ⏎ commits, esc cancels.
    case commitField, cancelField
    /// Not the sheet's: the focused field (or the window) gets it.
    case pass
}

public enum LoopsKeys {
    /// The sheet's action for a registry command in `surface` (nil: not
    /// the sheet's, it passes; esc and the sheet's own chord close).
    public static func action(for id: CommandID, in surface: Surface) -> LoopsKeyAction? {
        switch id {
        case .moveUp: return .up
        case .moveDown: return .down
        case .closeOverlay: return .close
        case .loops: return surface == .loops ? .close : nil
        case .wrapUp: return surface == .wrapUp ? .close : nil
        case .product: return surface == .product ? .close : nil
        case .loopsOpen: return .open
        case .loopsDone: return .done
        case .loopsPark: return .park
        case .loopsDrop: return .drop
        case .loopsMerge: return .merge
        case .loopsNextAgent: return .nextAgent
        case .loopsStartDraft: return .startDraft
        case .loopsMap: return .map
        case .loopsCheck: return .check
        case .loopsAddStep: return .addStep
        case .loopsTrigger: return .trigger
        case .loopsPreviousColumn: return .left
        case .loopsNextColumn: return .right
        case .loopsSwitchScope: return .switchScope
        case .wrapUpAccept: return .accept
        case .wrapUpNextStep: return .nextStep
        case .wrapUpUndo: return .undo
        case .wrapUpApply: return .apply
        case .productFile: return .file
        case .productPlan: return .plan
        case .productSavePlan: return .savePlan
        case .productDropProposal: return .dropProposal
        case .productNextScope: return .nextScope
        default: return nil
        }
    }

    public static func route(_ k: KeyChord, in surface: Surface, editing: Bool = false) -> LoopsKeyAction {
        if editing {
            let bare = !k.command && !k.option && !k.control
            if k.key == .escape && bare { return .cancelField }
            if k.key == .enter && !k.option && !k.control && !k.shift { return .commitField }
            if k.key == .up && bare { return .up }
            if k.key == .down && bare { return .down }
            return .pass
        }
        return CommandRegistry.resolve(k, in: surface).flatMap { action(for: $0, in: surface) } ?? .pass
    }

    /// Another sheet's or overlay's chord (⌘K, ⌘Y, ⌘J, ⌘R and the other
    /// loops sheets): this sheet closes and lets the window run it.
    public static func passesThrough(_ k: KeyChord, in surface: Surface) -> Bool {
        switch CommandRegistry.resolve(k, in: surface) {
        case .palette, .history, .nextAttention, .review: return true
        case .loops: return surface != .loops
        case .wrapUp: return surface != .wrapUp
        case .product: return surface != .product
        default: return false
        }
    }
}
