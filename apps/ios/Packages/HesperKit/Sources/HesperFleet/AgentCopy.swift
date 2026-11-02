import HesperCore

/// The words a row says about an agent: what it asks or what it does, in
/// plain words (docs/iphone/concept.md, "Key flows").
public enum AgentCopy {
    /// The row's second line.
    public static func headline(_ a: Agent) -> String {
        switch a.state {
        case .approval:
            return [verb(a.attention?.title), a.attention?.detail].compactMap(\.self).joined(separator: " ")
                .nonEmpty ?? "Wants your approval"
        case .question:
            return a.attention?.detail?.nonEmpty ?? a.attention?.title?.nonEmpty ?? "Asks a question"
        case .error:
            return a.exit.map { "Stopped with \($0.label)" } ?? a.attention?.detail?.nonEmpty ?? "Hit an error"
        case .working:
            return a.activity ?? "Working"
        case .starting:
            return "Starting"
        case .done:
            return a.summary?.nonEmpty ?? "Done"
        case .idle:
            return a.summary?.nonEmpty ?? "Idle"
        case .exited:
            return a.exit.map { "Exited, \($0.label)" } ?? "Exited"
        case .unknown:
            return "Unknown state"
        }
    }

    /// The choices a question offers as buttons, numbered as the agent's
    /// menu; empty when it has none (then the row says "Open to answer").
    public static func choices(_ a: Agent) -> [(number: Int, title: String)] {
        guard a.state == .question, let options = a.attention?.options else { return [] }
        return options.enumerated().map { ($0.offset + 1, $0.element) }
    }

    /// Claude's tool names as the verb the owner reads.
    static func verb(_ tool: String?) -> String? {
        switch tool {
        case "Bash": "Run"
        case nil: nil
        default: tool
        }
    }
}

extension String {
    var nonEmpty: String? { isEmpty ? nil : self }
}
