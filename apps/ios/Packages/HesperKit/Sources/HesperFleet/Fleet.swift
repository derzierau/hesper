import Foundation
import HesperCore

/// How the phone reaches one Mac right now.
public enum Reachability: Equatable, Sendable {
    case connecting
    /// Linked; `direct` is the LAN path, otherwise through the relay.
    case live(direct: Bool)
    /// Lost since this time. Its agents stay listed, dimmed.
    case unreachable(since: Date)

    public var isLive: Bool { if case .live = self { true } else { false } }
}

/// One paired Mac as the phone sees it.
public struct MacStatus: Equatable, Sendable, Identifiable {
    public var machine: Machine
    public var reachability: Reachability
    public var id: String { machine.short }

    public init(machine: Machine, reachability: Reachability = .connecting) {
        self.machine = machine
        self.reachability = reachability
    }
}

/// Agents of one project, the phone's version of the Mac's bands.
public struct ProjectGroup: Equatable, Sendable, Identifiable {
    /// The project's name; nil for agents without one (scratch).
    public var project: String?
    public var agents: [Agent]
    public var id: String { project ?? "" }
}

/// A task kept for later: a parked draft on one Mac. It shows on that
/// Mac's wall too, and starts from either.
public struct KeptTask: Equatable, Sendable, Identifiable {
    public var machine: String
    public var draft: Draft
    public var id: String { "\(machine)/\(draft.id)" }

    public init(machine: String, draft: Draft) {
        self.machine = machine
        self.draft = draft
    }
}

/// Where a new task runs: a project on a Mac.
public struct Destination: Hashable, Sendable {
    public var project: String
    public var machine: String

    public init(project: String, machine: String) {
        self.project = project
        self.machine = machine
    }
}

/// Everything the phone knows about every paired Mac: the result of
/// folding each Mac's link events. A value: the app's model owns one and
/// the extensions read its counts.
public struct Fleet: Equatable, Sendable {
    public private(set) var macs: [String: MacStatus] = [:]
    public private(set) var agents: [String: Agent] = [:]
    /// Every Mac's drafts, by `KeptTask.id`.
    public private(set) var drafts: [String: KeptTask] = [:]

    public init(macs: [Machine] = []) {
        for m in macs { self.macs[m.short] = MacStatus(machine: m) }
    }

    /// Folds one event from `machine`'s link. A full list replaces that
    /// Mac's agents and leaves the other Macs' alone.
    public mutating func apply(_ event: LinkEvent, from machine: String) {
        switch event {
        case .list(_, let list, let full):
            if full { agents = agents.filter { $0.value.machine != machine } }
            for a in list { agents[a.id] = a }
        case .changed(let a):
            agents[a.id] = a
        case .removed(let id):
            agents[id] = nil
        }
    }

    /// A Mac's whole draft list replaces what it had.
    public mutating func setDrafts(_ list: [Draft], on machine: String) {
        drafts = drafts.filter { $0.value.machine != machine }
        for d in list { saveDraft(d, on: machine) }
    }

    public mutating func saveDraft(_ draft: Draft, on machine: String) {
        let kept = KeptTask(machine: machine, draft: draft)
        drafts[kept.id] = kept
    }

    public mutating func removeDraft(id: String, on machine: String) {
        drafts[KeptTask(machine: machine, draft: Draft(id: id)).id] = nil
    }

    /// Tasks kept for later on every Mac: parked drafts, newest first.
    /// Drafts still open on a Mac's wall are that Mac's, not listed.
    public var later: [KeptTask] {
        func when(_ k: KeptTask) -> Date { k.draft.updated ?? k.draft.created ?? .distantPast }
        return drafts.values.filter(\.draft.parked).sorted { (when($0), $0.id) > (when($1), $1.id) }
    }

    /// Where a new task can run: projects with agents on a reachable Mac,
    /// the busiest first. A stand-in for `projects.recent` across Macs.
    public var destinations: [Destination] {
        var seen: [Destination: Int] = [:]
        for a in agents.values where isReachable(a) {
            if let p = a.project, !p.isEmpty { seen[Destination(project: p, machine: a.machine), default: 0] += 1 }
        }
        return seen.sorted { l, r in
            (r.value, l.key.project, l.key.machine) < (l.value, r.key.project, r.key.machine)
        }.map(\.key)
    }

    public mutating func setReachability(_ r: Reachability, for machine: String) {
        if macs[machine] == nil { macs[machine] = MacStatus(machine: Machine(short: machine, name: machine)) }
        macs[machine]?.reachability = r
    }

    /// The Mac's own name ("mini"), or the id when unknown.
    public func macName(_ short: String) -> String {
        macs[short].map { MachineLabel.name($0.machine) } ?? short
    }

    public func isReachable(_ agent: Agent) -> Bool {
        macs[agent.machine]?.reachability.isLive ?? true
    }

    /// Approvals, then questions, then errors; oldest first, as the Mac's
    /// attention queue.
    public var needsYou: [Agent] { AttentionQueue.ordered(agents.values) }

    /// Finished work waiting for a verdict, newest first.
    public var readyToReview: [Agent] {
        agents.values.filter { $0.state == .done }
            .sorted { ($0.stateSince ?? .distantPast, $0.id) > ($1.stateSince ?? .distantPast, $1.id) }
    }

    /// The agent the dock offers after `current` is answered.
    public func next(after current: String?) -> Agent? {
        let next = AttentionQueue.next(agents.values, after: current)
        return next?.id == current ? nil : next
    }

    /// Every agent grouped by project; named projects A–Z, agents without
    /// one last. Within a group, what needs you first, then by name.
    public var groups: [ProjectGroup] {
        let byProject = Dictionary(grouping: agents.values) { $0.project.flatMap { $0.isEmpty ? nil : $0 } }
        return byProject.map { project, list in
            ProjectGroup(
                project: project,
                agents: list.sorted { l, r in
                    let lp = AttentionQueue.priority(l.state) ?? 9
                    let rp = AttentionQueue.priority(r.state) ?? 9
                    return (lp, l.name.lowercased(), l.id) < (rp, r.name.lowercased(), r.id)
                })
        }
        .sorted { l, r in
            switch (l.project, r.project) {
            case (let a?, let b?): return a.lowercased() < b.lowercased()
            case (nil, _): return false
            case (_, nil): return true
            }
        }
    }

    /// `groups` with only the agents whose name, project, branch or task
    /// contains `query`; empty groups dropped. An empty query keeps all.
    public func groups(matching query: String) -> [ProjectGroup] {
        let q = query.trimmingCharacters(in: .whitespaces)
        guard !q.isEmpty else { return groups }
        return groups.compactMap { g in
            let hits = g.agents.filter { a in
                [a.name, a.project, a.branch, a.task].compactMap(\.self).contains { $0.localizedStandardContains(q) }
            }
            return hits.isEmpty ? nil : ProjectGroup(project: g.project, agents: hits)
        }
    }

    public var macList: [MacStatus] {
        macs.values.sorted { MachineLabel.name($0.machine) < MachineLabel.name($1.machine) }
    }

    public var counts: FleetCounts {
        FleetCounts(
            working: agents.values.count { $0.state == .working || $0.state == .starting },
            needsYou: needsYou.count,
            done: agents.values.count { $0.state == .done }
        )
    }
}

/// The counts the lock screen, the Live Activity and the tab badge show.
/// Counts only: the relay can send them without seeing any content.
public struct FleetCounts: Codable, Equatable, Hashable, Sendable {
    public var working: Int
    public var needsYou: Int
    public var done: Int

    public init(working: Int = 0, needsYou: Int = 0, done: Int = 0) {
        self.working = working
        self.needsYou = needsYou
        self.done = done
    }

    /// Nothing runs and nothing waits: the Live Activity can end.
    public var isQuiet: Bool { working == 0 && needsYou == 0 }

    /// "5 working · 2 need you", the Live Activity's line.
    public var summary: String {
        var parts: [String] = []
        if working > 0 { parts.append("\(working) working") }
        if needsYou > 0 { parts.append(needsYou == 1 ? "1 needs you" : "\(needsYou) need you") }
        if parts.isEmpty { return done > 0 ? "\(done) ready to review" : "Nothing needs you" }
        return parts.joined(separator: " · ")
    }
}
