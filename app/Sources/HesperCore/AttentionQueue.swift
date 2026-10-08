import Foundation

/// The ⌘J queue: approval, then question, then error; oldest first.
public enum AttentionQueue {
    public static func priority(_ s: AgentState) -> Int? {
        switch s {
        case .approval: return 0
        case .question: return 1
        case .error: return 2
        default: return nil
        }
    }

    public static func ordered<S: Sequence>(_ agents: S) -> [Agent] where S.Element == Agent {
        agents.compactMap { a in priority(a.state).map { (a, $0) } }
            .sorted { l, r in
                if l.1 != r.1 { return l.1 < r.1 }
                let ld = l.0.stateSince ?? .distantPast, rd = r.0.stateSince ?? .distantPast
                if ld != rd { return ld < rd }
                return l.0.id < r.0.id
            }
            .map(\.0)
    }

    /// The agent ⌘J goes to. From an agent in the queue it moves to the next
    /// one (wrapping); otherwise to the head of the queue.
    public static func next<S: Sequence>(_ agents: S, after current: String?) -> Agent? where S.Element == Agent {
        let q = ordered(agents)
        guard !q.isEmpty else { return nil }
        if let current, let i = q.firstIndex(where: { $0.id == current }) {
            return q.count == 1 ? q[0] : q[(i + 1) % q.count]
        }
        return q[0]
    }
}
