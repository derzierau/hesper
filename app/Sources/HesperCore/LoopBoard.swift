import Foundation

// The loops board (⌘L): every active loop in four columns by who acts
// next (`Loop.next`, computed by hesperd): You, Agents, Others, Parked.
// Closed and merged loops are hidden. The app builds a LoopBoard when the
// store's version or the filter changes, never per render.

public enum LoopColumn: String, Sendable, CaseIterable, Identifiable {
    case you, agents, others, parked

    public var id: String { rawValue }

    public var title: String {
        switch self {
        case .you: return "You"
        case .agents: return "Agents"
        case .others: return "Others"
        case .parked: return "Parked"
        }
    }

    /// The column of a loop; nil: hidden (closed or merged).
    public static func of(_ l: Loop) -> LoopColumn? {
        guard l.isActive else { return nil }
        if l.status == .parked { return .parked }
        switch l.next {
        case .you: return .you
        case .agents: return .agents
        case .others: return .others
        case .parked: return .parked
        case .closed: return nil
        }
    }
}

/// What the board shows: all nil keeps every loop. `milestone` ""
/// keeps only unfiled loops.
public struct LoopBoardFilter: Equatable, Hashable, Sendable {
    public var project: String?
    public var group: String?
    public var machine: String?
    public var milestone: String?

    public init(project: String? = nil, group: String? = nil, machine: String? = nil, milestone: String? = nil) {
        self.project = project; self.group = group; self.machine = machine; self.milestone = milestone
    }

    public var isEmpty: Bool { project == nil && group == nil && machine == nil && milestone == nil }

    /// The loop passes every filter set. A project filter keeps loops in
    /// the project or one of its packages; a group filter loops in any of
    /// the group's projects.
    public func matches(_ l: Loop, catalog: ProjectCatalog) -> Bool {
        if let project {
            guard l.projects.contains(where: { $0 == project || catalog.lineage($0).contains(project) }) else { return false }
        }
        if let group {
            guard l.projects.contains(where: { catalog.groupIDs(of: $0).contains(group) }) else { return false }
        }
        if let machine, !l.machines.contains(machine) { return false }
        if let milestone {
            if milestone.isEmpty { if l.milestone != nil { return false } } else if l.milestone != milestone { return false }
        }
        return true
    }
}

/// The board: columns, selection, counts and the loop-limit hint.
public struct LoopBoard: Equatable, Sendable {
    public private(set) var columns: [LoopColumn: [Loop]] = [:]
    public let filter: LoopBoardFilter
    public let loopLimit: Int
    /// Counts over every active loop, whatever the filter (the toolbar
    /// pill and the "N your move" count stay the same while filtering).
    public private(set) var totals: [LoopColumn: Int] = [:]
    /// Loops on the person that need a step (the You column without the
    /// ones that only look done), unfiltered.
    public private(set) var yourMove = 0
    /// You-column loops that look done (nothing open, every agent quiet),
    /// unfiltered.
    public private(set) var looksDone = 0
    /// The selected loop (kept while it is shown, else the first loop).
    public private(set) var selected: String?
    /// Filter choices: the projects, machines and milestones the active
    /// loops have (sorted; the app names them).
    public private(set) var projects: [String] = []
    public private(set) var machines: [String] = []
    public private(set) var milestones: [String] = []

    public init(loops: some Sequence<Loop>, filter: LoopBoardFilter = LoopBoardFilter(), catalog: ProjectCatalog = ProjectCatalog(),
                loopLimit: Int = LoopSettings.defaultLoopLimit, selected: String? = nil) {
        self.filter = filter
        self.loopLimit = loopLimit
        var cols: [LoopColumn: [Loop]] = [:]
        var p: Set<String> = [], m: Set<String> = [], ms: Set<String> = []
        for l in loops {
            guard let col = LoopColumn.of(l) else { continue }
            totals[col, default: 0] += 1
            if col == .you { if l.looksDone { looksDone += 1 } else { yourMove += 1 } }
            p.formUnion(l.projects); m.formUnion(l.machines)
            if let x = l.milestone { ms.insert(x) }
            guard filter.matches(l, catalog: catalog) else { continue }
            cols[col, default: []].append(l)
        }
        for (c, list) in cols { columns[c] = Self.sorted(list, in: c) }
        projects = p.sorted(); machines = m.sorted(); milestones = ms.sorted()
        let order = self.order
        self.selected = selected.flatMap { s in order.contains(s) ? s : nil } ?? order.first
    }

    /// The column's loops in order: You oldest waiting first (looks-done
    /// loops after the ones needing a step), Agents and Others most recent
    /// activity first, Parked by the date they come back (none last).
    public static func sorted(_ list: [Loop], in column: LoopColumn) -> [Loop] {
        switch column {
        case .you:
            return list.sorted { a, b in
                if a.looksDone != b.looksDone { return !a.looksDone }
                if a.waitingSince != b.waitingSince { return a.waitingSince < b.waitingSince }
                return a.id < b.id
            }
        case .agents, .others:
            return list.sorted { a, b in a.lastChange != b.lastChange ? a.lastChange > b.lastChange : a.id < b.id }
        case .parked:
            return list.sorted { a, b in
                let x = a.parkedUntil ?? .distantFuture, y = b.parkedUntil ?? .distantFuture
                if x != y { return x < y }
                return a.lastChange != b.lastChange ? a.lastChange > b.lastChange : a.id < b.id
            }
        }
    }

    public func loops(_ c: LoopColumn) -> [Loop] { columns[c] ?? [] }
    /// The filtered count of a column.
    public func count(_ c: LoopColumn) -> Int { columns[c]?.count ?? 0 }
    public var isEmpty: Bool { columns.values.allSatisfy(\.isEmpty) }

    /// Every shown loop, column by column (keyboard order).
    public var order: [String] { LoopColumn.allCases.flatMap { loops($0).map(\.id) } }

    public var selectedLoop: Loop? {
        guard let selected else { return nil }
        for c in LoopColumn.allCases { if let l = loops(c).first(where: { $0.id == selected }) { return l } }
        return nil
    }

    public var selectedColumn: LoopColumn? { selectedLoop.flatMap(LoopColumn.of) }

    public mutating func select(_ id: String?) {
        guard let id, order.contains(id) else { return }
        selected = id
    }

    /// ↑/↓: the previous or next loop in the column (stops at the ends).
    public mutating func move(_ delta: Int) {
        guard let col = selectedColumn ?? LoopColumn.allCases.first(where: { count($0) > 0 }) else { return }
        let list = loops(col)
        guard let i = list.firstIndex(where: { $0.id == selected }) else { selected = list.first?.id; return }
        selected = list[max(0, min(list.count - 1, i + delta))].id
    }

    /// ←/→: the nearest non-empty column that way, at the same row (or its
    /// last loop).
    public mutating func moveColumn(_ delta: Int) {
        let all = LoopColumn.allCases
        guard let col = selectedColumn, let ci = all.firstIndex(of: col) else { selected = order.first; return }
        let row = loops(col).firstIndex(where: { $0.id == selected }) ?? 0
        var i = ci + delta
        while i >= 0, i < all.count {
            let list = loops(all[i])
            if !list.isEmpty { selected = list[min(row, list.count - 1)].id; return }
            i += delta
        }
    }

    /// Over the limit: more loops need the person than `loopLimit` (a hint
    /// only, never a block).
    public var overLimit: Bool { yourMove > loopLimit }

    /// "7 loops wait on you (limit 5)" when over the limit.
    public var limitHint: String? {
        overLimit ? "\(yourMove) loops wait on you (limit \(loopLimit))" : nil
    }

    /// The toolbar pill: "3 your move", nil when nothing waits.
    public var pill: String? { yourMove > 0 ? "\(yourMove) your move" : nil }
}
