import Foundation

/// The app's copy of the daemon's agents, rebuilt purely from DaemonEvents.
/// The app never decides state; it only orders and counts what it is told.
public struct AgentRegistry: Sendable {
    public private(set) var agents: [String: Agent] = [:]
    public private(set) var hello: HelloInfo?
    public private(set) var connected = false
    /// Projects and groups (projects.list / groups.list and their events).
    public private(set) var catalog = ProjectCatalog(supported: false)
    private var seenSinceConnect: Set<String> = []

    public init() {}

    /// A change worth telling the user about (notification on entering a
    /// state that needs them).
    public struct Transition: Equatable, Sendable {
        public var agent: Agent
        public var from: AgentState?
    }

    /// Applies an event; returns transitions into approval/question/error.
    @discardableResult
    public mutating func apply(_ event: DaemonEvent) -> [Transition] {
        switch event {
        case .connected(let h):
            hello = h
            connected = true
            seenSinceConnect = []
            return []
        case .helloRefreshed(let h):
            hello = h
            return []
        case .disconnected:
            connected = false
            return []
        case .changed(let a):
            seenSinceConnect.insert(a.id)
            let old = agents[a.id]
            agents[a.id] = a
            if a.state.needsAttention && (old?.state != a.state || old?.attention != a.attention) {
                return [Transition(agent: a, from: old?.state)]
            }
            return []
        case .removed(let id, _):
            agents.removeValue(forKey: id)
            return []
        case .reconciled(let ids):
            for id in agents.keys where !ids.contains(id) && !seenSinceConnect.contains(id) {
                agents.removeValue(forKey: id)
            }
            return []
        case .draftChanged, .draftRemoved, .draftsListed:
            return []
        case .projectsListed(let p, let g):
            catalog.listed(projects: p, groups: g)
            return []
        case .projectChanged(let p):
            catalog.changed(p)
            return []
        case .projectRemoved(let id):
            catalog.removed(project: id)
            return []
        case .groupChanged(let g):
            catalog.changed(g)
            return []
        case .groupRemoved(let id):
            catalog.removed(group: id)
            return []
        }
    }

    /// Wall order: this machine first, then other machines by short name;
    /// within a machine by creation time, then id. Stable as states change,
    /// so tiles never jump around.
    public var wallOrder: [Agent] {
        let local = hello?.machine
        return agents.values.sorted { a, b in
            let al = a.machine == local, bl = b.machine == local
            if al != bl { return al }
            if a.machine != b.machine { return a.machine < b.machine }
            let ac = a.created ?? .distantPast, bc = b.created ?? .distantPast
            if ac != bc { return ac < bc }
            return a.id < b.id
        }
    }

    public var counts: StateCounts { StateCounts(agents.values) }
}

public struct StateCounts: Equatable, Sendable {
    public var total = 0, approval = 0, question = 0, error = 0, working = 0, done = 0
    public init() {}
    public init<S: Sequence>(_ agents: S) where S.Element == Agent {
        for a in agents {
            total += 1
            switch a.state {
            case .approval: approval += 1
            case .question: question += 1
            case .error: error += 1
            case .working, .starting: working += 1
            case .done, .idle: done += 1
            default: break
            }
        }
    }
    public var needingYou: Int { approval + question + error }

    /// The menu bar title: "3" style counts, empty when nothing needs you.
    public var menuBarTitle: String {
        var parts: [String] = []
        if approval > 0 { parts.append("\(approval)!") }
        if question > 0 { parts.append("\(question)?") }
        if error > 0 { parts.append("\(error)×") }
        return parts.joined(separator: " ")
    }
}

/// Whether entering a state should post a macOS notification.
public func shouldNotify(_ t: AgentRegistry.Transition, focusedID: String?, appActive: Bool) -> Bool {
    guard t.agent.state.needsAttention else { return false }
    // Not while the user is looking at exactly that agent.
    if appActive && focusedID == t.agent.id { return false }
    return true
}
