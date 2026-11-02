import Foundation

// The loops map (Loops sheet, map tab): the work graph of one loop or of
// every active loop. One lane per machine (this Mac first), project
// regions inside a lane, nodes for agents (the loop's agent links and the
// live agents in it), files and the person's open steps, and typed edges
// (started, continued, handoff, your step, blocks, broken parent).
// Layout is deterministic, left to right by tree depth: every node sits in
// a (lane, region, depth, row) slot of its own, so nodes never overlap.
// The output is plain geometry (no AppKit, no CoreGraphics); `LoopMapCache`
// rebuilds it only when its inputs' version moves.

// MARK: Geometry

public struct MapPoint: Equatable, Hashable, Sendable {
    public var x: Double
    public var y: Double
    public init(x: Double, y: Double) { self.x = x; self.y = y }
}

public struct MapRect: Equatable, Hashable, Sendable {
    public var x: Double
    public var y: Double
    public var width: Double
    public var height: Double

    public init(x: Double, y: Double, width: Double, height: Double) { self.x = x; self.y = y; self.width = width; self.height = height }

    public var minX: Double { x }
    public var minY: Double { y }
    public var maxX: Double { x + width }
    public var maxY: Double { y + height }
    public var midX: Double { x + width / 2 }
    public var midY: Double { y + height / 2 }

    /// The interiors overlap (touching edges don't).
    public func intersects(_ o: MapRect) -> Bool { x < o.maxX && o.x < maxX && y < o.maxY && o.y < maxY }

    public func contains(_ o: MapRect) -> Bool { o.x >= x && o.y >= y && o.maxX <= maxX && o.maxY <= maxY }
}

/// Sizes and gaps (points), from the spacing scale by default.
public struct LoopMapMetrics: Equatable, Hashable, Sendable {
    public var nodeWidth: Double = 176
    public var nodeHeight: Double = 44
    public var columnGap: Double = 56
    public var rowGap = DesignTokens.Spacing.m
    public var regionHeader: Double = 20
    public var regionPadding = DesignTokens.Spacing.m
    public var laneHeader: Double = 24
    public var laneGap = DesignTokens.Spacing.m
    /// Left margin of the columns (room for the lane's title).
    public var inset = DesignTokens.Spacing.xl

    public init() {}
}

// MARK: Model

public enum LoopMapScope: Equatable, Hashable, Sendable {
    case all
    case loop(String)
}

public enum MapNodeKind: String, Sendable, CaseIterable {
    /// An agent of the loop (live or gone).
    case agent
    /// A file the work went through (a brief, a handoff).
    case file
    /// An open step of the person's.
    case step
    /// Another loop (blocks / blocked by) not drawn here.
    case loop
    /// A parent the loop names but no Mac here knows (a link left behind).
    case missing
}

public enum MapEdgeKind: String, Sendable, CaseIterable {
    case started, continued, handoff, youStep, blocks, brokenParent
}

public struct MapNode: Equatable, Sendable, Identifiable {
    public var id: String
    public var kind: MapNodeKind
    public var title: String
    /// Second line: the agent's summary or ending, the file's folder.
    public var detail: String?
    /// The agent's live state (nil: gone, or not an agent).
    public var state: AgentState?
    public var ending: LoopEnding?
    /// Its agent id here while it is live (open the tile).
    public var agent: String?
    public var loop: String
    /// The lane's machine (nil: the "Other loops" lane).
    public var machine: String?
    public var project: String?
    public var depth: Int
    public var frame: MapRect
}

public struct MapEdge: Equatable, Sendable, Identifiable {
    public var id: String { "\(kind.rawValue)|\(from)|\(to)" }
    public var from: String
    public var to: String
    public var kind: MapEdgeKind
    /// From the right middle of `from` to the left middle of `to`.
    public var start: MapPoint
    public var end: MapPoint
    /// The edge points left or straight down (blocks between rows): the
    /// app draws it as a curve around the nodes.
    public var backward: Bool { end.x <= start.x }
}

public struct MapLane: Equatable, Sendable, Identifiable {
    public var id: String { machine ?? "" }
    /// nil: other loops (blocks), drawn last.
    public var machine: String?
    public var title: String
    public var frame: MapRect
}

public struct MapRegion: Equatable, Sendable, Identifiable {
    public var id: String { "\(machine ?? "")|\(project ?? "")" }
    public var machine: String?
    public var project: String?
    public var title: String
    public var frame: MapRect
}

public struct LoopMap: Equatable, Sendable {
    public var scope: LoopMapScope
    public var lanes: [MapLane] = []
    public var regions: [MapRegion] = []
    public var nodes: [MapNode] = []
    public var edges: [MapEdge] = []
    public var width: Double = 0
    public var height: Double = 0

    public var isEmpty: Bool { nodes.isEmpty }

    public func node(_ id: String) -> MapNode? { nodes.first { $0.id == id } }
}

// MARK: Build

extension LoopMap {
    /// The map of `scope` from the store's loops and the live agents.
    /// `local` is this Mac's short name (its lane comes first).
    public static func build(scope: LoopMapScope, loops: [String: Loop], agents: [Agent], catalog: ProjectCatalog = ProjectCatalog(),
                             local: String? = nil, metrics: LoopMapMetrics = LoopMapMetrics()) -> LoopMap {
        var b = Builder(loops: loops, agents: agents, catalog: catalog, local: local, metrics: metrics)
        let drawn: [Loop]
        switch scope {
        case .all: drawn = loops.values.filter(\.isActive).sorted { ($0.created ?? .distantPast, $0.id) < ($1.created ?? .distantPast, $1.id) }
        case .loop(let id): drawn = loops[id].map { [$0] } ?? []
        }
        for (i, l) in drawn.enumerated() { b.add(l, index: i) }
        b.addBlocks(drawn)
        return b.layout(scope: scope)
    }
}

private struct Builder {
    struct Pending {
        var id: String
        var kind: MapNodeKind
        var title: String
        var detail: String?
        var state: AgentState?
        var ending: LoopEnding?
        var agent: String?
        var loop: String
        var loopIndex: Int
        var machine: String?
        var project: String?
        var added: Date
    }

    let loops: [String: Loop]
    let catalog: ProjectCatalog
    let local: String?
    let m: LoopMapMetrics
    let live: [String: Agent]
    var nodes: [String: Pending] = [:]
    var edges: [(from: String, to: String, kind: MapEdgeKind)] = []
    var edgeKeys: Set<String> = []
    /// Agent id here → its node (live agents name parents by id).
    var nodeOfAgent: [String: String] = [:]
    /// The first agent node of each loop (blocks edges start there).
    var rootOf: [String: String] = [:]

    init(loops: [String: Loop], agents: [Agent], catalog: ProjectCatalog, local: String?, metrics: LoopMapMetrics) {
        self.loops = loops; self.catalog = catalog; self.local = local; self.m = metrics
        live = Dictionary(agents.filter { !$0.isService }.map { ($0.id, $0) }, uniquingKeysWith: { a, _ in a })
    }

    mutating func node(_ p: Pending) {
        guard nodes[p.id] == nil else { return }
        var p = p
        // Only other loops go to the machine-less lane.
        if p.kind != .loop, p.machine == nil { p.machine = local ?? "?" }
        nodes[p.id] = p
    }

    mutating func edge(_ from: String, _ to: String, _ kind: MapEdgeKind) {
        guard from != to, edgeKeys.insert("\(from)|\(to)").inserted else { return }
        edges.append((from, to, kind))
    }

    func fileMachine(_ ref: String) -> String? {
        guard let i = ref.firstIndex(of: ":"), i != ref.startIndex else { return nil }
        return String(ref[..<i])
    }

    func knownProject(_ id: String?) -> String? {
        guard let id, catalog.projects[id] != nil else { return nil }
        return id
    }

    mutating func add(_ l: Loop, index: Int) {
        let base = l.projects.first
        func pending(_ id: String, _ kind: MapNodeKind, _ title: String, machine: String?, project: String?, added: Date?) -> Pending {
            Pending(id: id, kind: kind, title: title, loop: l.id, loopIndex: index, machine: machine, project: project, added: added ?? l.created ?? .distantPast)
        }
        var refNode: [String: String] = [:] // link refs → node ids in this loop
        // Agents and files first, so every `from` can be resolved.
        for k in l.links where k.kind == .agent || k.kind == .file {
            if k.kind == .agent {
                let a = k.agent.flatMap { live[$0] }
                let id = "a:" + k.ref
                var p = pending(id, .agent, a?.name ?? k.title, machine: k.machine ?? a?.machine, project: a.flatMap { catalog.projectID(for: $0) } ?? base, added: k.added)
                p.state = k.isLive ? (k.state ?? a?.state) : nil
                p.ending = k.ending ?? a?.ending
                p.detail = k.summary ?? a?.summary ?? k.closedReason
                p.agent = k.isLive ? k.agent : nil
                node(p)
                refNode[k.ref] = id
                if let ag = k.agent { nodeOfAgent[ag] = id }
            } else {
                let id = "f:" + k.ref
                let mach = fileMachine(k.ref) ?? k.machine
                let path = k.ref.split(separator: ":", maxSplits: 1).last.map(String.init) ?? k.ref
                var p = pending(id, .file, k.title, machine: mach, project: knownProject(catalog.projectID(path: path, machine: mach)) ?? base, added: k.added)
                p.detail = ((path as NSString).deletingLastPathComponent as NSString).lastPathComponent
                node(p)
                refNode[k.ref] = id
            }
        }
        // Live agents in the loop that it does not link yet.
        for a in live.values.sorted(by: { $0.id < $1.id }) where a.loop == l.id && nodeOfAgent[a.id] == nil {
            let id = "a:live:" + a.id
            var p = pending(id, .agent, a.name, machine: a.machine, project: catalog.projectID(for: a) ?? base, added: a.created)
            p.state = a.state; p.ending = a.ending; p.detail = a.summary; p.agent = a.id
            node(p)
            nodeOfAgent[a.id] = id
        }
        // Edges between agents and files.
        for k in l.links where (k.kind == .agent || k.kind == .file) {
            guard let to = refNode[k.ref], let from = k.from else { continue }
            if let f = refNode[from] {
                let kind: MapEdgeKind
                if nodes[f]?.kind == .file || k.kind == .file || k.rel == .handoff { kind = .handoff }
                else if k.rel == .continued { kind = .continued }
                else { kind = .started }
                edge(f, to, kind)
            } else if k.kind == .agent, k.rel == .started || k.rel == .continued || k.rel == .member {
                // The parent is named but nowhere here: a dangling link.
                let mid = "m:" + from
                let child = nodes[to]
                var p = pending(mid, .missing, String(from.split(separator: "/").last ?? Substring(from)), machine: child?.machine, project: child?.project, added: .distantPast)
                p.detail = "not found"
                node(p)
                edge(mid, to, .brokenParent)
            }
        }
        // Live agents' parents (the agent tree), when the links have no edge.
        for a in live.values.sorted(by: { $0.id < $1.id }) where a.loop == l.id {
            guard let to = nodeOfAgent[a.id], let parent = a.parent, !edges.contains(where: { $0.to == to }) else { continue }
            if let f = nodeOfAgent[parent] { edge(f, to, .started) }
            else if live[parent] == nil {
                let mid = "m:" + parent
                var p = pending(mid, .missing, String(parent.split(separator: "/").last ?? Substring(parent)), machine: a.machine, project: nodes[to]?.project, added: .distantPast)
                p.detail = "not found"
                node(p)
                edge(mid, to, .brokenParent)
            }
        }
        // The person's open steps, after the agent they came from.
        let firstAgent = l.links.first(where: { $0.kind == .agent }).flatMap { refNode[$0.ref] }
            ?? nodes.values.filter { $0.loop == l.id && $0.kind == .agent }.map(\.id).sorted().first
        for it in l.yourItems {
            let id = "s:\(l.id):\(it.id)"
            let src = it.source.flatMap { refNode[$0] ?? nodeOfAgent[$0] }
            let anchor = src ?? firstAgent
            var p = pending(id, .step, it.text, machine: anchor.flatMap { nodes[$0]?.machine } ?? local, project: anchor.flatMap { nodes[$0]?.project } ?? base, added: it.created)
            p.detail = it.kind == .step ? nil : it.kind.rawValue
            node(p)
            if let src { edge(src, id, .youStep) }
        }
        rootOf[l.id] = firstAgent ?? nodes.values.filter { $0.loop == l.id }.map(\.id).sorted().first
    }

    /// Blocks edges between the drawn loops; a loop not drawn gets a node
    /// in the "Other loops" lane.
    mutating func addBlocks(_ drawn: [Loop]) {
        for (i, l) in drawn.enumerated() {
            for k in l.links where k.kind == .loop && (k.rel == .blocks || k.rel == .blockedBy) {
                guard let here = rootOf[l.id] else { continue }
                let there: String
                if let r = rootOf[k.ref] {
                    there = r
                } else {
                    there = "l:" + k.ref
                    var p = Pending(id: there, kind: .loop, title: loops[k.ref]?.displayTitle ?? k.title, loop: k.ref, loopIndex: drawn.count + i,
                                    machine: nil, project: nil, added: .distantPast)
                    p.detail = loops[k.ref].map { $0.status.isClosed ? $0.status.rawValue : $0.next.rawValue }
                    node(p)
                    rootOf[k.ref] = there
                }
                if k.rel == .blocks { edge(here, there, .blocks) } else { edge(there, here, .blocks) }
            }
        }
    }

    /// Longest path from a root, over every edge but blocks (cycles are
    /// cut where they close).
    func depths() -> [String: Int] {
        var preds: [String: [String]] = [:]
        for e in edges where e.kind != .blocks { preds[e.to, default: []].append(e.from) }
        var depth: [String: Int] = [:]
        var visiting: Set<String> = []
        func visit(_ id: String) -> Int {
            if let d = depth[id] { return d }
            guard visiting.insert(id).inserted else { return 0 }
            let d = (preds[id] ?? []).map { visit($0) + 1 }.max() ?? 0
            visiting.remove(id)
            depth[id] = d
            return d
        }
        for id in nodes.keys.sorted() { _ = visit(id) }
        return depth
    }

    func laneOrder(_ machines: Set<String?>) -> [String?] {
        let named = machines.compactMap { $0 }.sorted { a, b in
            if a == local { return true }
            if b == local { return false }
            return a.lowercased() != b.lowercased() ? a.lowercased() < b.lowercased() : a < b
        }
        return named.map(Optional.some) + (machines.contains(nil) ? [nil] : [])
    }

    func layout(scope: LoopMapScope) -> LoopMap {
        var out = LoopMap(scope: scope)
        guard !nodes.isEmpty else { return out }
        let depth = depths()
        let maxDepth = depth.values.max() ?? 0
        let colW = m.nodeWidth + m.columnGap
        out.width = m.inset + Double(maxDepth + 1) * colW - m.columnGap + m.inset
        let rowH = m.nodeHeight + m.rowGap
        var preds: [String: [String]] = [:]
        for e in edges { preds[e.to, default: []].append(e.from) }
        var row: [String: Int] = [:]
        var y = 0.0
        var placed: [MapNode] = []
        let byLane = Dictionary(grouping: nodes.values, by: \.machine)
        for lane in laneOrder(Set(byLane.keys)) {
            let laneTop = y
            y += m.laneHeader
            let inLane = byLane[lane] ?? []
            let byRegion = Dictionary(grouping: inLane, by: \.project)
            let regionKeys = byRegion.keys.sorted { a, b in
                // No project last; else by name.
                guard let a else { return false }
                guard let b else { return true }
                let na = catalog.name(project: a).lowercased(), nb = catalog.name(project: b).lowercased()
                return na != nb ? na < nb : a < b
            }
            for key in regionKeys {
                let regionTop = y
                let members = byRegion[key] ?? []
                var rows = 0
                // Column by column, children near their parent's row.
                for d in 0...maxDepth {
                    let col = members.filter { depth[$0.id] == d }.sorted { a, b in
                        func parentRow(_ p: Pending) -> Int { (preds[p.id] ?? []).compactMap { row[$0] }.min() ?? Int.max }
                        let ka = (a.loopIndex, parentRow(a)), kb = (b.loopIndex, parentRow(b))
                        if ka != kb { return ka < kb }
                        if a.added != b.added { return a.added < b.added }
                        return a.id < b.id
                    }
                    for (r, p) in col.enumerated() {
                        row[p.id] = r
                        let frame = MapRect(x: m.inset + Double(d) * colW, y: regionTop + m.regionHeader + Double(r) * rowH, width: m.nodeWidth, height: m.nodeHeight)
                        placed.append(MapNode(id: p.id, kind: p.kind, title: p.title, detail: p.detail, state: p.state, ending: p.ending, agent: p.agent,
                                              loop: p.loop, machine: p.machine, project: p.project, depth: d, frame: frame))
                    }
                    rows = max(rows, col.count)
                }
                let h = m.regionHeader + Double(rows) * rowH - m.rowGap + m.regionPadding
                out.regions.append(MapRegion(machine: lane, project: key, title: key.map { catalog.name(project: $0) } ?? (lane == nil ? "" : "No project"),
                                             frame: MapRect(x: m.inset - m.regionPadding, y: regionTop, width: out.width - 2 * m.inset + 2 * m.regionPadding, height: h)))
                y = regionTop + h + m.regionPadding
            }
            out.lanes.append(MapLane(machine: lane, title: lane ?? "Other loops", frame: MapRect(x: 0, y: laneTop, width: out.width, height: y - laneTop)))
            y += m.laneGap
        }
        out.height = y - m.laneGap
        out.nodes = placed.sorted { ($0.frame.y, $0.frame.x, $0.id) < ($1.frame.y, $1.frame.x, $1.id) }
        let frames = Dictionary(placed.map { ($0.id, $0.frame) }, uniquingKeysWith: { a, _ in a })
        out.edges = edges.compactMap { e in
            guard let f = frames[e.from], let t = frames[e.to] else { return nil }
            return MapEdge(from: e.from, to: e.to, kind: e.kind, start: MapPoint(x: f.maxX, y: f.midY), end: MapPoint(x: t.minX, y: t.midY))
        }.sorted { $0.id < $1.id }
        return out
    }
}

// MARK: Cache

/// Keeps the last map; `map` rebuilds it only when the scope, the metrics
/// or the inputs' version (the store's and the agents') changed.
public struct LoopMapCache: Sendable {
    public struct Key: Equatable, Hashable, Sendable {
        public var scope: LoopMapScope
        public var version: Int
        public var metrics: LoopMapMetrics
        public init(scope: LoopMapScope, version: Int, metrics: LoopMapMetrics = LoopMapMetrics()) {
            self.scope = scope; self.version = version; self.metrics = metrics
        }
    }

    private var key: Key?
    private var last: LoopMap?
    /// How often it built (tests and perf counters).
    public private(set) var builds = 0

    public init() {}

    public mutating func map(_ key: Key, build: () -> LoopMap) -> LoopMap {
        if key == self.key, let last { return last }
        let m = build()
        self.key = key; last = m; builds += 1
        return m
    }

    public mutating func invalidate() { key = nil; last = nil }
}
