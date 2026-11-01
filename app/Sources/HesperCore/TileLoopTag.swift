import Foundation

// What a tile's band says about the agent's loop, right of its one line:
// the loop's short title and who acts next (a StateMark and the word), or
// for an agent hesperd runs itself (Agent.service) its role and no loop.
// Built when the tile's band is refreshed (an agent or a chip changed),
// from LoopHub's precomputed chips; nothing per frame.

public struct TileLoopTag: Equatable, Sendable {
    /// The loop's title (cut, LoopChip) or "Steward" / "Planner".
    public var text: String
    /// "Your move", "Agents on it", … (nil: a service agent).
    public var next: String?
    /// Who acts next as a mark (nil: a service agent).
    public var mark: StateMarkKind?
    /// The tooltip: what is next, in words.
    public var help: String?
    /// The loop it shows (nil: a service agent).
    public var loop: String?

    /// The tag of an agent (nil: no loop, or a loop that is not active).
    public static func of(_ a: Agent, chips: [String: LoopChip]) -> TileLoopTag? {
        if let s = a.service { return TileLoopTag(text: serviceName(s), next: nil, mark: nil, help: nil, loop: nil) }
        guard let id = a.loop, let c = chips[id] else { return nil }
        return TileLoopTag(text: c.text, next: c.nextLabel, mark: mark(c), help: c.detail, loop: c.loop)
    }

    /// "steward" → "Steward", "planner" → "Planner"; another role as given,
    /// capitalized.
    public static func serviceName(_ s: String) -> String {
        switch s {
        case "steward": return "Steward"
        case "planner": return "Planner"
        default: return s.prefix(1).uppercased() + s.dropFirst()
        }
    }

    /// Who acts next as a mark: the person a question's (it waits on them,
    /// never signal: that is for approvals), agents working, looks done
    /// done, others, parked and closed quiet.
    public static func mark(_ c: LoopChip) -> StateMarkKind {
        if c.looksDone { return .done }
        switch c.next {
        case .you: return .question
        case .agents: return .working
        case .others, .parked: return .idle
        case .closed: return .done
        }
    }
}
