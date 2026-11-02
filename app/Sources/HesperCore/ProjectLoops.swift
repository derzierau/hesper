import Foundation

// The sidebar's loop counts (⌘0): per project, how many loops are open
// and how many wait on the person's move; a group counts each loop once
// across its projects, "All agents" every active loop. Built once per
// loop store version (LoopHub), applied to the navigator's rows when the
// sidebar reloads; the rows compare equal only when the counts do, so a
// loop change alone reloads the sidebar.

/// A row's loop counts.
public struct LoopTally: Equatable, Hashable, Sendable {
    /// Active loops (not done, dropped or merged; parked ones count).
    public var open = 0
    /// Of those, the ones on the person (LoopColumn.you, looks done and
    /// parked left out: the board's "your move").
    public var onYou = 0

    public init(open: Int = 0, onYou: Int = 0) { self.open = open; self.onYou = onYou }

    public var isEmpty: Bool { open == 0 }

    /// "3 open loops, 1 your move" (VoiceOver).
    public var spoken: String {
        let o = "\(open) open loop\(open == 1 ? "" : "s")"
        return onYou > 0 ? "\(o), \(onYou) your move" : o
    }
}

public struct ProjectLoops: Equatable, Sendable {
    var open: [String: Set<String>] = [:]
    var onYou: [String: Set<String>] = [:]
    public private(set) var total = LoopTally()

    public init() {}

    public init(loops: some Sequence<Loop>) {
        for l in loops where l.isActive {
            let you = LoopColumn.of(l) == .you && !l.looksDone
            total.open += 1
            if you { total.onYou += 1 }
            for p in Set(l.projects) {
                open[p, default: []].insert(l.id)
                if you { onYou[p, default: []].insert(l.id) }
            }
        }
    }

    /// The counts over these projects, each loop once.
    public func tally(_ projects: some Sequence<String>) -> LoopTally {
        var o: Set<String> = [], y: Set<String> = []
        for p in projects {
            o.formUnion(open[p] ?? [])
            y.formUnion(onYou[p] ?? [])
        }
        return LoopTally(open: o.count, onYou: y.count)
    }

    /// The navigator's sections with every project, group and All row's
    /// loop counts filled in.
    public func apply(_ sections: [ProjectNavSection], catalog: ProjectCatalog) -> [ProjectNavSection] {
        sections.map { s in
            var s = s
            s.rows = s.rows.map { r in
                var r = r
                switch r.kind {
                case .all: r.loops = total
                case .project: r.loops = r.projectID.map { tally([$0]) } ?? LoopTally()
                case .group: r.loops = r.groupID.flatMap { catalog.groups[$0] }.map { tally($0.projectIds) } ?? LoopTally()
                default: break
                }
                return r
            }
            return s
        }
    }
}
