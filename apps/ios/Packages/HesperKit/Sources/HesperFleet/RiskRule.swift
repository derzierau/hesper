import HesperCore

/// Approvals that never get a blind Allow from a row or a notification
/// (docs/iphone/concept.md, flow 1): they only open the agent. A stand-in
/// until hesperd flags risk itself.
public enum RiskRule {
    static let patterns = ["migrate", "rm ", "git push", "drop ", "reset --hard"]

    public static func isRisky(_ agent: Agent) -> Bool {
        guard agent.state == .approval, agent.attention?.title == "Bash", let command = agent.attention?.detail else { return false }
        return patterns.contains { command.contains($0) }
    }
}
