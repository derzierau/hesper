import Foundation

// Products (⇧⌘L): a product is a project or a group with at least one goal
// or milestone (decision 7; no new type). Its page shows the goal, the
// milestones in order with their progress and status, the loops filed
// under each, the scope's unfiled loops and, for the planning view, the
// milestones a planning session proposed (accept with ⌘↩, or drop).

/// Where a milestone stands, worst first for the person.
public enum MilestoneStanding: Equatable, Hashable, Sendable {
    case done
    /// A filed loop or a condition waits on the person.
    case onYou
    /// Its loops wait for loops of another, unfinished milestone.
    case waiting(on: String)
    /// Nothing changed for `LoopSettings.stallDays` (hesperd's flag).
    case stalled
    case onTrack

    public var title: String {
        switch self {
        case .done: return "Done"
        case .onYou: return "On you"
        case .waiting: return "Waiting"
        case .stalled: return "Stalled"
        case .onTrack: return "On track"
        }
    }
}

public struct ProductMilestone: Equatable, Sendable, Identifiable {
    public var id: String { milestone.id }
    public var milestone: Milestone
    public var standing: MilestoneStanding
    /// Conditions done of total (0…1).
    public var progress: Double
    /// Its loops: active ones first (by who acts next), then closed.
    public var loops: [Loop]
}

/// A planning view's intents for proposed milestones.
public enum PlanningIntent: Equatable, Hashable, Sendable {
    /// goals.accept for these (all: every proposed milestone of the scope).
    case accept(scope: String, ids: [String]?)
    /// goals.remove: the proposal goes away.
    case drop(milestone: String)
}

public struct ProductPage: Equatable, Sendable {
    public let scope: String
    public let title: String
    public let isGroup: Bool
    /// The scope's goals in order (open ones first); `goal` the first.
    public let goals: [Goal]
    public var goal: Goal? { goals.first }
    /// Accepted milestones in order (done ones last).
    public let milestones: [ProductMilestone]
    /// The scope's active loops filed under no milestone (of any product).
    public let unfiled: [Loop]
    /// Proposed milestones (the planning view), in order.
    public let proposed: [Milestone]

    /// The page of a project or group id.
    public init(scope: String, store: LoopStore, catalog: ProjectCatalog = ProjectCatalog()) {
        self.init(scope: scope, goals: Array(store.goals.values), milestones: Array(store.milestones.values), loops: store.loops, catalog: catalog)
    }

    public init(scope: String, goals: [Goal], milestones: [Milestone], loops: [String: Loop], catalog: ProjectCatalog = ProjectCatalog()) {
        self.scope = scope
        isGroup = catalog.groups[scope] != nil
        title = catalog.groups[scope]?.name ?? catalog.project(scope)?.name ?? scope
        self.goals = goals.filter { $0.scope == scope && $0.status != .dropped && $0.status != .proposed }
            .sorted { ($0.status == .done ? 1 : 0, $0.order, $0.id) < ($1.status == .done ? 1 : 0, $1.order, $1.id) }
        let mine = milestones.filter { $0.scope == scope }.sorted { ($0.order, $0.title, $0.id) < ($1.order, $1.title, $1.id) }
        proposed = mine.filter { $0.status == .proposed }
        let accepted = mine.filter { $0.status == .open || $0.status == .done }
        // Which open milestone each loop is filed under (the loop's own
        // field, else the milestone's list).
        var milestoneOf: [String: String] = [:]
        for m in accepted { for id in m.loops { milestoneOf[id] = m.id } }
        for l in loops.values { if let m = l.milestone, accepted.contains(where: { $0.id == m }) { milestoneOf[l.id] = m } }
        let openIDs = Set(accepted.filter { $0.status == .open }.map(\.id))
        var rows: [ProductMilestone] = []
        for m in accepted {
            let filed = loops.values.filter { milestoneOf[$0.id] == m.id && !$0.isMerged }.sorted(by: Self.loopOrder)
            let standing = Self.standing(m, loops: filed, all: loops, milestoneOf: milestoneOf, open: openIDs)
            rows.append(ProductMilestone(milestone: m, standing: standing, progress: m.status == .done ? 1 : m.progress, loops: filed))
        }
        self.milestones = rows.filter { $0.standing != .done } + rows.filter { $0.standing == .done }
        let scopeProjects = Self.projects(of: scope, catalog: catalog)
        unfiled = loops.values.filter { l in
            l.isActive && l.milestone == nil && milestoneOf[l.id] == nil && l.projects.contains { !scopeProjects.isDisjoint(with: catalog.lineage($0)) }
        }.sorted(by: Self.loopOrder)
    }

    /// The projects a scope covers: a group's (and their packages'), or
    /// the project and its packages.
    static func projects(of scope: String, catalog: ProjectCatalog) -> Set<String> {
        var roots: Set<String> = catalog.groups[scope].map { Set($0.projectIds) } ?? [scope]
        for p in catalog.projects.values where p.groups.contains(scope) { roots.insert(p.id) }
        return roots
    }

    /// Active loops first, by who acts next (you, agents, others, parked),
    /// then the most recent.
    static func loopOrder(_ a: Loop, _ b: Loop) -> Bool {
        func rank(_ l: Loop) -> Int {
            guard l.isActive else { return 9 }
            return LoopColumn.of(l).flatMap { LoopColumn.allCases.firstIndex(of: $0) } ?? 8
        }
        let ra = rank(a), rb = rank(b)
        if ra != rb { return ra < rb }
        return a.lastChange != b.lastChange ? a.lastChange > b.lastChange : a.id < b.id
    }

    /// Done; on you (a filed loop waits on the person, or an open
    /// condition is theirs); waiting (its open loops are all blocked by
    /// loops of another open milestone, or a condition checks such a loop);
    /// stalled; on track.
    static func standing(_ m: Milestone, loops filed: [Loop], all: [String: Loop], milestoneOf: [String: String], open: Set<String>) -> MilestoneStanding {
        if m.status == .done { return .done }
        if filed.contains(where: { $0.isActive && $0.next == .you && !$0.looksDone }) || m.items.contains(where: \.isYourMove) { return .onYou }
        func other(_ loopID: String) -> String? {
            guard let ms = milestoneOf[loopID], ms != m.id, open.contains(ms), all[loopID]?.isActive ?? false else { return nil }
            return ms
        }
        let active = filed.filter(\.isActive)
        let blockers = active.map { l in l.links.filter { $0.kind == .loop && $0.rel == .blockedBy }.compactMap { other($0.ref) } }
        if !active.isEmpty, blockers.allSatisfy({ !$0.isEmpty }), let first = blockers.first?.first { return .waiting(on: first) }
        if let ms = m.items.first(where: { $0.isOpen && $0.check?.knownKind == .loop }).flatMap({ $0.check.flatMap { other($0.ref) } }) {
            return .waiting(on: ms)
        }
        if m.stalled { return .stalled }
        return .onTrack
    }

    /// The planning view's intents: accept all, accept one, drop one.
    public var acceptAll: PlanningIntent? { proposed.isEmpty ? nil : .accept(scope: scope, ids: nil) }
    public func accept(_ id: String) -> PlanningIntent { .accept(scope: scope, ids: [id]) }
    public func drop(_ id: String) -> PlanningIntent { .drop(milestone: id) }

    /// The planner already running or starting for `scope` (P shows it
    /// instead of starting another: hesperd starts one per call): the one
    /// this app started (`remembered`) while it runs, else hesperd's
    /// planner for the scope by the name it gives it ("Plan <scope>").
    public static func runningPlanner(scope: String, remembered: String?, agents: some Sequence<Agent>) -> Agent? {
        let live = agents.filter { $0.service == "planner" && $0.isRunning && $0.state != .error }
        if let id = remembered, let a = live.first(where: { $0.id == id }) { return a }
        return live.filter { $0.name == "Plan \(scope)" }.min { $0.id < $1.id }
    }

    /// Every product: the scopes with a goal or a milestone that is not
    /// dropped, groups first, then by name.
    public static func scopes(goals: [Goal], milestones: [Milestone], catalog: ProjectCatalog = ProjectCatalog()) -> [String] {
        var set: Set<String> = []
        for g in goals where g.status != .dropped && !g.scope.isEmpty { set.insert(g.scope) }
        for m in milestones where m.status != .dropped && !m.scope.isEmpty { set.insert(m.scope) }
        return set.sorted { a, b in
            let ga = catalog.groups[a] != nil, gb = catalog.groups[b] != nil
            if ga != gb { return ga }
            let na = (catalog.groups[a]?.name ?? catalog.project(a)?.name ?? a).lowercased()
            let nb = (catalog.groups[b]?.name ?? catalog.project(b)?.name ?? b).lowercased()
            return na != nb ? na < nb : a < b
        }
    }
}
