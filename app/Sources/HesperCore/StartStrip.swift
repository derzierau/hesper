import Foundation

// The composer's start strip (⌘N, a draft tile): under the task, what the
// start check found (StartCheckIndex.check) and the on-you meter, with
// three ways on. ⌘⏎ (Start) starts as always: a new loop. ⇧⌘⏎ starts it
// in the matched loop (and ticks off the loose end it would do); ⌘D files
// the draft as a step of that loop and leaves it parked. The meter is a
// hint only: red at the limit, never a block.

public struct StartStrip: Equatable, Sendable {
    public enum Choice: Equatable, Sendable {
        /// ⇧⌘⏎: spawn with `loop`; tick `looseEnd` off once it started.
        case addToLoop(loop: String, looseEnd: String?)
        /// ⌘D: the draft becomes a step of the loop (loops.item with
        /// `draft`), the composer is left.
        case draftInLoop(loop: String)
    }

    public var result: StartCheckResult

    public init(_ result: StartCheckResult) { self.result = result }

    /// Shown while the task has words and something is worth saying: a
    /// match, or loops already waiting on the person.
    public static func shows(_ r: StartCheckResult?, task: String) -> Bool {
        guard let r, !LoopTokens.of(task).isEmpty else { return false }
        return r.match != nil || r.onYou > 0
    }

    /// "This looks like part of Push provider".
    public var text: String? { result.match.map { "This looks like part of \($0.title)" } }
    /// "Ticks off: Write the brief" when the task would do a loose end.
    public var looseEnd: String? { result.match?.looseEndText.map { "Ticks off: \($0)" } }
    /// "3 / 5 on you".
    public var meter: String { "\(result.onYou) / \(result.loopLimit) on you" }
    public var meterHelp: String { result.limitHint ?? "Loops waiting on your move (limit \(result.loopLimit), a hint only)" }
    public var atLimit: Bool { result.atLimit }

    public var add: Choice? { result.match.map { .addToLoop(loop: $0.loop, looseEnd: $0.looseEnd) } }
    public var draft: Choice? { result.match.map { .draftInLoop(loop: $0.loop) } }

    /// The strip's key caps, from the registry (the person's shortcuts
    /// applied; nil: unbound there).
    public static func addKey(in surface: Surface = .composer, table: CommandTable = CommandRegistry.active) -> String? {
        cap(.composerAddToLoop, surface, table)
    }

    public static func draftKey(in surface: Surface = .composer, table: CommandTable = CommandRegistry.active) -> String? {
        cap(.composerDraftInLoop, surface, table)
    }

    private static func cap(_ id: CommandID, _ surface: Surface, _ table: CommandTable) -> String? {
        table.command(id)?.chords(in: surface).first?.label
    }

    /// The strip's keys (registry commands, ⇧⌘⏎ and ⌘D by default): add to
    /// the loop, save as a draft in it (nil: not the strip's; the
    /// composer's own keys go on).
    public func choice(for k: KeyChord, in surface: Surface = .composer, table: CommandTable = CommandRegistry.active) -> Choice? {
        switch table.resolve(k, in: surface) {
        case .composerAddToLoop: return add
        case .composerDraftInLoop: return draft
        default: return nil
        }
    }

    /// The item ⌘D files: the task's first line as the agents' step,
    /// pointing at the draft.
    public static func draftItem(task: String, draft: String) -> ItemPatch {
        ItemPatch(text: ComposerResolution.firstLine(task), owner: .agent, kind: .step, draft: draft)
    }
}
