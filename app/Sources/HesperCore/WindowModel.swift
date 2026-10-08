import Foundation

// Multi-window model (docs/rebuild-contract.md "As built — windows"): wall
// scopes and how agents are distributed over walls, where ⌘J and
// notification clicks go, what gets restored on relaunch, and the one
// agent window per agent rule. Pure, so it is unit-tested; the AppKit side
// lives in app/Sources/Hesper/Windows.

// MARK: Scopes

/// What a filter wall shows. Dimensions combine with AND, values within a
/// dimension with OR; an empty dimension matches everything. `projects`
/// holds project ids (older window files: folders; both match).
public struct WallFilter: Codable, Equatable, Hashable, Sendable {
    public var projects: Set<String> = []
    public var groups: Set<String> = []
    public var machines: Set<String> = []
    public var kinds: Set<String> = []
    public var states: Set<StateGroup> = []

    public init(projects: Set<String> = [], groups: Set<String> = [], machines: Set<String> = [], kinds: Set<String> = [], states: Set<StateGroup> = []) {
        self.projects = projects; self.groups = groups; self.machines = machines; self.kinds = kinds; self.states = states
    }

    enum CodingKeys: String, CodingKey { case projects, groups, machines, kinds, states }
    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        projects = try c.decodeIfPresent(Set<String>.self, forKey: .projects) ?? []
        groups = try c.decodeIfPresent(Set<String>.self, forKey: .groups) ?? []
        machines = try c.decodeIfPresent(Set<String>.self, forKey: .machines) ?? []
        kinds = try c.decodeIfPresent(Set<String>.self, forKey: .kinds) ?? []
        states = try c.decodeIfPresent(Set<StateGroup>.self, forKey: .states) ?? []
    }

    public var isEmpty: Bool { projects.isEmpty && groups.isEmpty && machines.isEmpty && kinds.isEmpty && states.isEmpty }

    /// Without project data: projects match the agent's folder.
    public func matches(_ a: Agent) -> Bool {
        matches(a, place: AgentPlace(projectID: a.project, lineage: Set([a.project].compactMap { $0 }), groupIDs: [], unit: ""))
    }

    public func matches(_ a: Agent, place: AgentPlace) -> Bool {
        if !projects.isEmpty && projects.isDisjoint(with: place.lineage) && !projects.contains(a.project ?? "") { return false }
        if !groups.isEmpty && groups.isDisjoint(with: place.groupIDs) { return false }
        if !machines.isEmpty && !machines.contains(a.machine) { return false }
        if !kinds.isEmpty && !kinds.contains(a.kind) { return false }
        if !states.isEmpty && !states.contains(StateGroup(a.state)) { return false }
        return true
    }

    /// Short description for the scope pill ("Filter · M, codex").
    public func summary(names: (String) -> String) -> String {
        var parts: [String] = []
        parts += groups.map(names).sorted()
        parts += projects.map(names).sorted()
        parts += machines.sorted()
        parts += kinds.sorted()
        parts += StateGroup.allCases.filter { states.contains($0) }.map(\.title)
        return parts.isEmpty ? "everything" : parts.joined(separator: ", ")
    }
    public var summary: String { summary(names: { ($0 as NSString).lastPathComponent }) }
}

/// Where an agent sits in the project data, for scopes and Overflow.
public struct AgentPlace: Equatable, Sendable {
    public var projectID: String?
    /// The project and, for a package, its repository.
    public var lineage: Set<String>
    public var groupIDs: Set<String>
    /// What Overflow keeps whole (the band key at All's grouping level).
    public var unit: String
    public init(projectID: String?, lineage: Set<String>, groupIDs: Set<String>, unit: String) {
        self.projectID = projectID; self.lineage = lineage; self.groupIDs = groupIDs; self.unit = unit
    }
}

/// States as the filter offers them.
public enum StateGroup: String, Codable, CaseIterable, Hashable, Sendable {
    case needsYou, working, quiet, ended

    public init(_ s: AgentState) {
        switch s {
        case .approval, .question, .error: self = .needsYou
        case .starting, .working, .unknown: self = .working
        case .done, .idle: self = .quiet
        case .exited: self = .ended
        }
    }

    public var title: String {
        switch self {
        case .needsYou: return "Needs you"
        case .working: return "Working"
        case .quiet: return "Done / idle"
        case .ended: return "Exited"
        }
    }
}

/// Which agents a wall shows.
public enum WallScope: Codable, Equatable, Hashable, Sendable {
    /// Everyone (a mirror with its own layout). The first wall with this
    /// scope heads the overflow chain when Overflow walls exist.
    case all
    /// Continues where the earlier walls of the chain are full.
    case overflow
    case filter(WallFilter)
    /// One group's projects (the sidebar's group row).
    case group(String)
    /// One project, its packages included (the sidebar's project row).
    case project(String)

    public var title: String {
        switch self {
        case .all: return "All"
        case .overflow: return "Overflow"
        case .filter: return "Filter"
        case .group: return "Group"
        case .project: return "Project"
        }
    }

    public var isFilter: Bool { if case .filter = self { return true } else { return false } }
}

/// One wall as the distribution sees it, in window order (the main wall first).
public struct WallSlot: Equatable, Sendable {
    public var id: String
    public var scope: WallScope
    /// How many cards fit without scrolling at the wall's minimum card size.
    public var capacity: Int
    /// How this wall shows projects / groups that have their own visible
    /// wall (desks: pointers).
    public var ownWalls: OwnWallMode
    /// The window is really on screen (open, not minimized, its display
    /// attached). Only visible project / group walls make pointers.
    public var visible: Bool
    public init(id: String, scope: WallScope, capacity: Int, ownWalls: OwnWallMode = .collapsed, visible: Bool = true) {
        self.id = id; self.scope = scope; self.capacity = capacity; self.ownWalls = ownWalls; self.visible = visible
    }
}

/// How a wall shows a project or group that has its own wall window.
public enum OwnWallMode: String, Codable, CaseIterable, Sendable {
    /// One collapsed pointer line ("acme-apps · in … window ↗"), no tiles.
    case collapsed
    /// Its tiles as before (a duplicate of the other window).
    case full
    /// Neither tiles nor a pointer.
    case hidden

    public var title: String {
        switch self {
        case .collapsed: return "Collapsed pointer"
        case .full: return "Full band"
        case .hidden: return "Hidden"
        }
    }
}

/// A project / group shown on this wall as a pointer to its own wall.
public struct WallPointer: Equatable, Sendable {
    /// The project / group wall it points to.
    public var wall: String
    public var scope: WallScope
    /// Its agents this wall would otherwise show (wall order).
    public var agents: [String]
    public init(wall: String, scope: WallScope, agents: [String]) { self.wall = wall; self.scope = scope; self.agents = agents }
}

public enum OwnWalls {
    /// Whether a wall with scope `outer` shows a wall with scope `inner`
    /// as a pointer: only project and group walls are "own walls", and
    /// only walls whose scope is wider point to them (All, Overflow,
    /// Filter: projects and groups; a group wall: projects). A project
    /// wall never points, nor does a wall to one with the same scope.
    public static func points(_ outer: WallScope, to inner: WallScope) -> Bool {
        guard outer != inner else { return false }
        switch inner {
        case .project:
            switch outer {
            case .all, .overflow, .filter, .group: return true
            case .project: return false
            }
        case .group:
            switch outer {
            case .all, .overflow, .filter: return true
            case .group, .project: return false
            }
        case .all, .overflow, .filter:
            return false
        }
    }

    /// Whether an agent belongs to a project / group wall's scope.
    public static func matches(_ scope: WallScope, place: AgentPlace) -> Bool {
        switch scope {
        case .project(let p): return place.lineage.contains(p)
        case .group(let g): return place.groupIDs.contains(g)
        default: return false
        }
    }

    /// The pointers of wall `w` over the agents it would show: every
    /// visible project / group wall it points to, with its agents among
    /// `agents` (an agent in several: the first such wall in window
    /// order). Empty pointers are dropped. `.full`: none.
    public static func pointers(for w: WallSlot, walls: [WallSlot], agents: [Agent], place: (Agent) -> AgentPlace) -> [WallPointer] {
        guard w.ownWalls != .full else { return [] }
        let own = walls.filter { $0.id != w.id && $0.visible && points(w.scope, to: $0.scope) }
        guard !own.isEmpty else { return [] }
        var out = own.map { WallPointer(wall: $0.id, scope: $0.scope, agents: []) }
        for a in agents {
            let p = place(a)
            if let i = own.firstIndex(where: { matches($0.scope, place: p) }) { out[i].agents.append(a.id) }
        }
        return out.filter { !$0.agents.isEmpty }
    }
}

/// What each wall shows, and which of its bands continue a band an
/// earlier wall of the overflow chain started ("acme-apps (cont.)").
public struct Distribution: Equatable, Sendable {
    public var agents: [String: [Agent]] = [:]
    /// Wall id → units (band keys) continued from an earlier wall.
    public var continued: [String: Set<String>] = [:]
    /// Wall id → projects / groups shown as pointers to their own wall
    /// (their agents are not in `agents`; a `.hidden` wall has none).
    public var pointers: [String: [WallPointer]] = [:]
    public init() {}
}

public enum ScopeMath {
    /// The agents each wall shows, in wall order, without project data
    /// (every agent in one unit: Overflow splits by capacity alone).
    public static func distribute(_ agents: [Agent], walls: [WallSlot]) -> [String: [Agent]] {
        distribute(agents, walls: walls, place: { a in AgentPlace(projectID: a.project, lineage: Set([a.project].compactMap { $0 }), groupIDs: [], unit: a.id) },
                   unitOrder: []).agents
    }

    /// The agents each wall shows.
    ///
    /// The overflow chain is the first wall (when its scope is All)
    /// followed by every Overflow wall in order. It is filled **by unit**
    /// (a group, or a project when there are no groups: `place.unit`), in
    /// `unitOrder`: a wall of the chain takes whole units while they fit
    /// its capacity (minimum cards without scrolling); a unit that doesn't
    /// fit goes to the next wall. A unit alone larger than a wall's
    /// capacity is split: it starts on a wall of its own, takes its
    /// capacity, and its remainder continues on the next wall (reported in
    /// `continued`). The last wall of the chain takes the rest. Agents in
    /// approval / question / error always go to the head of the chain
    /// (even past its capacity). Without Overflow walls the first wall shows
    /// everyone. All walls outside the chain mirror everyone; Filter, Group
    /// and Project walls show what matches. Order inside a wall is the
    /// input order (wall order, by band).
    public static func distribute(_ agents: [Agent], walls: [WallSlot], place: (Agent) -> AgentPlace, unitOrder: [String]) -> Distribution {
        var out = Distribution()
        var chain: [WallSlot] = []
        var places: [String: AgentPlace] = [:]
        for a in agents { places[a.id] = place(a) }
        let placeOf: (Agent) -> AgentPlace = { places[$0.id]! }
        /// Pointers of a wall over the agents it would show; those agents leave it.
        func pointed(_ w: WallSlot, _ shown: [Agent]) -> [Agent] {
            let ps = OwnWalls.pointers(for: w, walls: walls, agents: shown, place: placeOf)
            guard !ps.isEmpty else { return shown }
            if w.ownWalls == .collapsed { out.pointers[w.id] = ps }
            let gone = Set(ps.flatMap(\.agents))
            return shown.filter { !gone.contains($0.id) }
        }
        for (i, w) in walls.enumerated() {
            switch w.scope {
            case .all:
                if i == 0 { chain.append(w) } else { out.agents[w.id] = pointed(w, agents) }
            case .overflow:
                chain.append(w)
            case .filter(let f):
                out.agents[w.id] = pointed(w, agents.filter { f.matches($0, place: places[$0.id]!) })
            case .group(let g):
                out.agents[w.id] = pointed(w, agents.filter { places[$0.id]!.groupIDs.contains(g) })
            case .project(let p):
                out.agents[w.id] = agents.filter { places[$0.id]!.lineage.contains(p) }
            }
        }
        guard !chain.isEmpty else { return out }
        // The overflow chain: projects / groups with their own wall leave
        // the chain (pointers on its head) and take no capacity.
        let agents = pointed(chain[0], agents)
        if chain.count == 1 {
            out.agents[chain[0].id] = agents
            return out
        }
        // Units in unit order (unknown units after, by first appearance).
        let rank = Dictionary(unitOrder.enumerated().map { ($1, $0) }, uniquingKeysWith: { a, _ in a })
        var firstSeen: [String: Int] = [:]
        for (i, a) in agents.enumerated() where firstSeen[places[a.id]!.unit] == nil { firstSeen[places[a.id]!.unit] = i }
        let urgent = agents.filter { $0.state.needsAttention }
        let urgentIDs = Set(urgent.map(\.id))
        var units: [(key: String, agents: [Agent])] = []
        for a in agents where !urgentIDs.contains(a.id) {
            let u = places[a.id]!.unit
            if let i = units.firstIndex(where: { $0.key == u }) { units[i].agents.append(a) } else { units.append((u, [a])) }
        }
        units.sort { l, r in
            let lr = rank[l.key] ?? Int.max, rr = rank[r.key] ?? Int.max
            return lr != rr ? lr < rr : (firstSeen[l.key] ?? 0) < (firstSeen[r.key] ?? 0)
        }
        var placedBefore: Set<String> = []
        for (k, w) in chain.enumerated() {
            var take: [String] = []
            var cont: Set<String> = []
            if k == 0 { take = urgent.map(\.id) }
            if k == chain.count - 1 {
                for u in units {
                    take += u.agents.map(\.id)
                    if placedBefore.contains(u.key) { cont.insert(u.key) }
                }
                units = []
            } else {
                let cap = max(0, w.capacity)
                var room = max(0, cap - take.count)
                var fresh = true
                while let u = units.first {
                    if u.agents.count <= room {
                        take += u.agents.map(\.id)
                        if placedBefore.contains(u.key) { cont.insert(u.key) }
                        room -= u.agents.count
                        units.removeFirst()
                        fresh = false
                    } else if u.agents.count > cap && fresh && room > 0 {
                        // Alone bigger than a wall: split, the rest continues.
                        take += u.agents.prefix(room).map(\.id)
                        if placedBefore.contains(u.key) { cont.insert(u.key) }
                        placedBefore.insert(u.key)
                        units[0].agents.removeFirst(room)
                        room = 0
                        break
                    } else {
                        break
                    }
                }
            }
            let set = Set(take)
            out.agents[w.id] = agents.filter { set.contains($0.id) }
            if !cont.isEmpty { out.continued[w.id] = cont }
        }
        return out
    }

    /// The scope a new wall starts with: Overflow when the first wall can't
    /// fit everyone at its minimum card size, else All.
    public static func defaultScope(firstWallCapacity: Int, agentCount: Int) -> WallScope {
        agentCount > firstWallCapacity ? .overflow : .all
    }
}

/// How many cards of the minimum size fit a wall's visible area without
/// scrolling (at least 1). Columns are one row of full-height cards.
public enum WallCapacity {
    public static func count(width: Double, height: Double, minCard: (width: Double, height: Double),
                             spec: WallSpec, arrangement: WallArrangement) -> Int {
        let m = spec.margin, g = spec.gap
        let cols = Int(((width - 2 * m + g) / (minCard.width + g)).rounded(.down))
        let rows = arrangement == .columns ? 1 : Int(((height - 2 * m + g) / (minCard.height + g)).rounded(.down))
        return max(1, max(0, cols) * max(0, rows))
    }
}

// MARK: Routing

/// Where ⌘J, a notification click or a menu bar item takes an agent.
public enum WindowRoute: Equatable, Sendable {
    case agentWindow(String)
    /// A wall showing the agent (the frontmost one).
    case wall(String)
    /// No wall shows it: the main wall (opens it there).
    case mainWall(String)
}

public enum WindowRouting {
    /// The agent's own window if open, else the frontmost wall showing it,
    /// else the main wall. `walls` is front to back.
    public static func route(agent: String, agentWindows: Set<String>, walls: [(id: String, shows: Set<String>)], mainWall: String) -> WindowRoute {
        if agentWindows.contains(agent) { return .agentWindow(agent) }
        if let w = walls.first(where: { $0.shows.contains(agent) }) { return .wall(w.id) }
        return .mainWall(mainWall)
    }
}

// MARK: One window per agent

/// The agent windows by agent id: opening one that exists returns it.
public struct AgentWindowBook<Handle: AnyObject> {
    public private(set) var byAgent: [String: Handle] = [:]
    public init() {}

    /// The agent's window, made with `make` only when it has none.
    public mutating func openOrFocus(_ agent: String, make: () -> Handle) -> (handle: Handle, created: Bool) {
        if let h = byAgent[agent] { return (h, false) }
        let h = make()
        byAgent[agent] = h
        return (h, true)
    }

    public func handle(for agent: String) -> Handle? { byAgent[agent] }
    public var agents: Set<String> { Set(byAgent.keys) }

    public mutating func remove(_ agent: String) { byAgent.removeValue(forKey: agent) }

    /// The window of `from` shows `to` instead (⌥⌘← ⌥⌘→ in an untabbed
    /// agent window). Refused when `to` has a window already (one window
    /// per agent) or `from` has none.
    @discardableResult
    public mutating func retarget(_ from: String, to: String) -> Bool {
        guard from != to, byAgent[to] == nil, let h = byAgent.removeValue(forKey: from) else { return false }
        byAgent[to] = h
        return true
    }

    /// The agent an untabbed agent window steps to: the previous / next in
    /// wall order (wrapping) that has no window of its own; nil when every
    /// other agent has one (or `current` is not on the wall).
    public func stepTarget(from current: String, by delta: Int, order: [String]) -> String? {
        guard let i = order.firstIndex(of: current), order.count > 1, delta != 0 else { return nil }
        let n = order.count, d = delta > 0 ? 1 : -1
        for k in 1..<n {
            let id = order[((i + d * k) % n + n) % n]
            if byAgent[id] == nil { return id }
        }
        return nil
    }

    /// Removes the entry for exactly this handle (window closed).
    public mutating func remove(handle: Handle) {
        for (k, v) in byAgent where v === handle { byAgent.removeValue(forKey: k) }
    }
}

// MARK: Restoration

public struct SavedFrame: Codable, Equatable, Sendable {
    public var x, y, width, height: Double
    public init(x: Double, y: Double, width: Double, height: Double) { self.x = x; self.y = y; self.width = width; self.height = height }
    public init(_ r: Rect) { self.init(x: r.x, y: r.y, width: r.width, height: r.height) }
    public var rect: Rect { Rect(x: x, y: y, width: width, height: height) }
}

public struct SavedWall: Codable, Equatable, Sendable {
    public var id: String
    public var isMain: Bool
    public var scope: WallScope
    public var arrangement: WallArrangement
    public var minChars: Int
    public var frame: SavedFrame
    /// The display it was on: its stable key (desks: `DisplaySetup.keys`;
    /// windows.json had CGDirectDisplayIDs, migrated).
    public var screen: String?
    public var fullScreen: Bool
    /// The view (projects): grouping override, collapsed bands, the
    /// wall's band order (header drag), the project sidebar. Optional:
    /// older files have none.
    public var grouping: Grouping?
    public var collapsed: [String]?
    public var bandOrder: [String]?
    public var sidebar: Bool?
    /// Projects / groups that have their own wall (desks): nil = the
    /// collapsed pointer line.
    public var ownWalls: OwnWallMode?
    public init(id: String, isMain: Bool, scope: WallScope, arrangement: WallArrangement, minChars: Int, frame: SavedFrame, screen: String?, fullScreen: Bool,
                grouping: Grouping? = nil, collapsed: [String]? = nil, bandOrder: [String]? = nil, sidebar: Bool? = nil, ownWalls: OwnWallMode? = nil) {
        self.id = id; self.isMain = isMain; self.scope = scope; self.arrangement = arrangement; self.minChars = minChars
        self.frame = frame; self.screen = screen; self.fullScreen = fullScreen
        self.grouping = grouping; self.collapsed = collapsed; self.bandOrder = bandOrder; self.sidebar = sidebar
        self.ownWalls = ownWalls
    }
}

public struct SavedAgentWindow: Codable, Equatable, Sendable {
    public var agent: String
    public var frame: SavedFrame
    public var screen: String?
    /// Windows with the same group were tabs of one window, in array order.
    public var tabGroup: Int?
    public var selectedTab: Bool
    public var fullScreen: Bool
    public init(agent: String, frame: SavedFrame, screen: String?, tabGroup: Int?, selectedTab: Bool, fullScreen: Bool) {
        self.agent = agent; self.frame = frame; self.screen = screen; self.tabGroup = tabGroup; self.selectedTab = selectedTab; self.fullScreen = fullScreen
    }
}

public struct SavedWindows: Codable, Equatable, Sendable {
    public var version = 1
    public var walls: [SavedWall] = []
    public var agentWindows: [SavedAgentWindow] = []
    public init(walls: [SavedWall] = [], agentWindows: [SavedAgentWindow] = []) { self.walls = walls; self.agentWindows = agentWindows }

    public func encoded() throws -> Data {
        let e = JSONEncoder()
        e.outputFormatting = [.prettyPrinted, .sortedKeys]
        return try e.encode(self)
    }

    public static func decode(_ d: Data) -> SavedWindows? {
        guard let s = try? JSONDecoder().decode(SavedWindows.self, from: d), s.version == 1 else { return nil }
        return s
    }

    /// Agent windows whose agents no longer exist don't come back.
    public func keeping(agents: Set<String>) -> SavedWindows {
        var s = self
        s.agentWindows.removeAll { !agents.contains($0.agent) }
        return s
    }
}

public struct ScreenArea: Equatable, Sendable {
    public var id: String
    /// The visible frame (without menu bar and Dock), AppKit coordinates.
    public var visible: Rect
    public init(id: String, visible: Rect) { self.id = id; self.visible = visible }
}

public enum FrameClamp {
    /// Fits a saved frame onto the screens there are now: the saved display
    /// if it is still attached, else the one it overlaps most, else the
    /// first (main) one. The frame shrinks to the visible area and moves
    /// fully onto it.
    public static func clamp(_ f: Rect, screen: String?, screens: [ScreenArea], minSize: (Double, Double) = (360, 240)) -> Rect {
        guard !screens.isEmpty else { return f }
        func overlap(_ s: ScreenArea) -> Double {
            let v = s.visible
            let w = min(f.maxX, v.maxX) - max(f.x, v.x), h = min(f.maxY, v.maxY) - max(f.y, v.y)
            return w > 0 && h > 0 ? w * h : 0
        }
        let target = screens.first { $0.id == screen && screen != nil }
            ?? screens.max { overlap($0) < overlap($1) }.flatMap { overlap($0) > 0 ? $0 : nil }
            ?? screens[0]
        let v = target.visible
        let w = min(max(f.width, min(minSize.0, v.width)), v.width)
        let h = min(max(f.height, min(minSize.1, v.height)), v.height)
        let x = min(max(f.x, v.x), v.maxX - w)
        let y = min(max(f.y, v.y), v.maxY - h)
        return Rect(x: x, y: y, width: w, height: h)
    }
}
