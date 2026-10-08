import Foundation

// Pure logic behind the system surfaces (phase 5 of the redesign): the
// menu-bar menu's groups, the notification text, the first-run steps and
// the focus view's slide direction. The app turns these into NSMenu,
// UNNotification and SwiftUI; the rules live here so they can be tested.

/// The menu-bar menu: agents grouped Needs you · Working · Recent.
public enum StatusMenuGroups {
    public enum Group: String, CaseIterable, Sendable {
        case needsYou, working, background, recent

        public var title: String {
            switch self {
            case .needsYou: return "Needs you"
            case .working: return "Working"
            case .background: return "Background"
            case .recent: return "Recent"
            }
        }
    }

    /// At most this many agents under Recent (done, idle, exited).
    public static let recentLimit = 5

    /// Non-empty groups in menu order. Needs you is the ⌘J order
    /// (approval, question, error; oldest first); Working is starting and
    /// working, longest running first; Recent is done, idle, exited and
    /// unknown, most recent first, capped at `recentLimit`.
    /// Background agents (⌥⌘W) that don't need you have their own group
    /// after Working; one that needs you stays under Needs you.
    public static func groups<S: Sequence>(_ agents: S, isBackground: (Agent) -> Bool = { _ in false }) -> [(group: Group, agents: [Agent])] where S.Element == Agent {
        var all = Array(agents)
        let needs = AttentionQueue.ordered(all)
        let needIDs = Set(needs.map(\.id))
        let background = BackgroundTray.agents(all.filter { isBackground($0) && !needIDs.contains($0.id) }, isBackground: { _ in true })
        let bgIDs = Set(background.map(\.id))
        all.removeAll { bgIDs.contains($0.id) }
        let since = { (a: Agent) in a.stateSince ?? a.created ?? .distantPast }
        let working = all.filter { $0.state == .working || $0.state == .starting }
            .sorted { since($0) != since($1) ? since($0) < since($1) : $0.id < $1.id }
        let recent = all.filter { AttentionQueue.priority($0.state) == nil && $0.state != .working && $0.state != .starting }
            .sorted { since($0) != since($1) ? since($0) > since($1) : $0.id < $1.id }
            .prefix(recentLimit)
        return [(.needsYou, needs), (.working, working), (.background, background), (.recent, Array(recent))].filter { !$0.1.isEmpty }
    }
}

/// What a macOS notification for an agent says.
public struct AgentNotificationText: Equatable, Sendable {
    public var title: String
    public var body: String
    /// Notifications group per machine.
    public var thread: String
    /// The actionable category (Allow / Deny) for approvals, else nil.
    public var category: String?

    public static let approvalCategory = "hesper.approval"
    public static let allowAction = "hesper.allow"
    public static let denyAction = "hesper.deny"

    /// - Parameter machineName: the machine's display name ("mini").
    public init(_ a: Agent, machineName: String) {
        let who = "\(a.name) on \(machineName)"
        let t = a.attention?.title.flatMap { $0.isEmpty ? nil : $0 }
        let d = a.attention?.detail.flatMap { $0.isEmpty ? nil : $0 }
        switch a.state {
        case .approval:
            title = "\(who) needs you"
            // The exact request: "Allow Bash?" and what it runs.
            body = [t.map { "Allow \($0)?" } ?? "Allow?", d].compactMap { $0 }.joined(separator: "\n")
            category = Self.approvalCategory
        case .question:
            title = "\(who) needs you"
            // The question itself; the title only when there is no text.
            body = d ?? t ?? "It asks a question."
            category = nil
        default:
            title = "\(who) stopped with an error"
            body = [t, d].compactMap { $0 }.joined(separator: ": ")
            category = nil
        }
        thread = a.machine
    }
}

/// The first run: three steps, each done by facts the app already knows.
public struct FirstRunSteps: Equatable, Sendable {
    public enum Step: Int, CaseIterable, Sendable {
        case pairThisMac, addAnotherMac, startFirstAgent

        public var title: String {
            switch self {
            case .pairThisMac: return "Pair this Mac"
            case .addAnotherMac: return "Add another Mac"
            case .startFirstAgent: return "Start your first agent"
            }
        }
    }

    public var connected: Bool
    public var otherMachines: Int
    public var agents: Int

    public init(connected: Bool, otherMachines: Int, agents: Int) {
        self.connected = connected; self.otherMachines = otherMachines; self.agents = agents
    }

    public func isDone(_ s: Step) -> Bool {
        switch s {
        case .pairThisMac: return connected
        case .addAnotherMac: return otherMachines > 0
        case .startFirstAgent: return agents > 0
        }
    }

    /// The first step not done yet (another Mac is optional: it never
    /// blocks starting an agent once this Mac is paired).
    public var current: Step? {
        if !isDone(.pairThisMac) { return .pairThisMac }
        if !isDone(.startFirstAgent) { return isDone(.addAnotherMac) ? .startFirstAgent : .addAnotherMac }
        return nil
    }

    /// Whether the first run should show at all: never once it ran; only
    /// for a fresh setup (no agents, no other Mac paired).
    public static func shouldShow(alreadyShown: Bool, otherMachines: Int, agents: Int) -> Bool {
        !alreadyShown && otherMachines == 0 && agents == 0
    }
}

/// The focus view's slide when it switches agents: +1 the new one comes
/// from the right (next), -1 from the left (previous).
public enum FocusSlide {
    public static func direction(from old: String, to new: String, order: [String]) -> Int {
        guard let i = order.firstIndex(of: old), let j = order.firstIndex(of: new), i != j else { return 1 }
        let n = order.count
        let forward = (j - i + n) % n
        return forward <= n - forward ? 1 : -1
    }
}
