import HesperCore

/// Approvals treated as risky. Today Home's Yes on one first asks a
/// confirmation dialog (`DecisionOptions.needsConfirmation`,
/// docs/iphone/neon.md); concept.md flow 1 gives them Open only. A
/// stand-in until hesperd flags risk itself.
public enum RiskRule {
    static let patterns = ["migrate", "rm ", "git push", "drop ", "reset --hard"]

    public static func isRisky(_ agent: Agent) -> Bool {
        guard agent.state == .approval, agent.attention?.title == "Bash", let command = agent.attention?.detail else { return false }
        return patterns.contains { command.contains($0) }
    }
}
