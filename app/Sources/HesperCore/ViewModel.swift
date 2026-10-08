import Foundation

// A wall is a view: scope (which agents) + grouping (bands one level below
// the scope) + layout (how the space is filled). docs/rebuild-contract.md
// "As built — projects (views)". Pure rules, unit-tested; the AppKit side
// is WallView / BandHeaderView / ProjectSidebar.

/// The grouping a wall asks for. `auto` goes one level below the scope:
/// All / Overflow / Filter → groups (projects when there are none), a
/// group → projects, a project → packages / branches / worktrees.
public enum Grouping: String, Codable, CaseIterable, Sendable {
    case auto, none, group, project, branch

    public var title: String {
        switch self {
        case .auto: return "Automatic"
        case .none: return "None"
        case .group: return "By group"
        case .project: return "By project"
        case .branch: return "By branch / package"
        }
    }
}

/// What the bands of a wall are.
public enum GroupLevel: String, Sendable, Equatable {
    case none, group, project, branch
}

public enum ViewGrouping {
    public static func level(_ g: Grouping, scope: WallScope, hasGroups: Bool) -> GroupLevel {
        switch g {
        case .none: return .none
        case .group: return hasGroups ? .group : .project
        case .project: return .project
        case .branch: return .branch
        case .auto:
            switch scope {
            case .group: return .project
            case .project: return .branch
            case .all, .overflow, .filter: return hasGroups ? .group : .project
            }
        }
    }
}

/// One card as the view resolver sees it (an agent or a draft).
public struct ViewItem: Equatable, Sendable {
    public var id: String
    public var projectID: String?
    public var branch: String?
    public var worktree: String?
    /// A draft opened without a project (⌘N, the toolbar, the menu bar):
    /// it sits in the wall's "New" area, in no band, until it starts.
    public var isNew: Bool
    public init(id: String, projectID: String?, branch: String? = nil, worktree: String? = nil, isNew: Bool = false) {
        self.id = id; self.projectID = projectID; self.branch = branch; self.worktree = worktree; self.isNew = isNew
    }
}

/// A band (E), lane (B), nested rect (C): one group, project or branch.
public struct Band: Equatable, Sendable, Identifiable {
    public var key: String
    public var title: String
    public var subtitle: String
    public var colorHex: String
    public var level: GroupLevel
    public var groupID: String?
    /// The project ＋ starts a new agent in (and ↗ opens).
    public var projectID: String?
    /// Branch level: the branch the band shows (＋ reuses it).
    public var branch: String?
    /// Card ids in wall order.
    public var members: [String]
    /// Continues a band an earlier wall of the overflow chain started.
    public var continued: Bool
    /// A pointer to the project's / group's own wall (desks): a collapsed
    /// header line with no cards (`members` is empty).
    public var pointer: WallPointer? = nil
    public var id: String { key }

    /// The agents a header counts: the members, or a pointer's agents.
    public var counted: [String] { pointer?.agents ?? members }

    public var displayTitle: String { continued ? "\(title) (cont.)" : title }

    /// The wall's "New" area: drafts without a project (no color, no
    /// project, always first).
    public var isNew: Bool { key == ViewResolver.newKey }

    /// The scope "↗ open as wall" gives this band.
    public var wallScope: WallScope? {
        switch level {
        case .group: return groupID.map(WallScope.group)
        case .project, .branch: return projectID.map(WallScope.project)
        case .none: return nil
        }
    }
}

public struct ResolvedView: Equatable, Sendable {
    public var level: GroupLevel
    public var bands: [Band]
    /// Card ids by band (band order, then wall order).
    public var order: [String]

    public static let flat = ResolvedView(level: .none, bands: [], order: [])

    /// Bands are drawn (frames, headers) only when they say something:
    /// two or more, or one that continues from another wall. One band is
    /// the plain wall of today.
    /// The "New" area (drafts without a project) doesn't count: with one
    /// project band it is still the plain wall, and a new draft sits in
    /// the wall order (right of the selected card).
    public var showsBands: Bool {
        level != .none && (bands.filter { !$0.isNew }.count >= 2 || bands.contains { $0.continued || $0.pointer != nil })
    }

    public func band(of id: String) -> Band? { bands.first { $0.members.contains(id) } }
    public func band(key: String) -> Band? { bands.first { $0.key == key } }
}

public enum ViewResolver {
    static let other = "~other", scratch = "~scratch", none = "~none"
    /// Scratch projects' agents: one "Scratch" band (by group or by
    /// project), at the end, collapsed until opened.
    public static let scratchKeys: Set<String> = ["g:" + scratch, "p:" + scratch]
    public static let scratchTitle = "Scratch"

    /// Bands that start collapsed on every wall (scratch).
    public static func collapsedByDefault(_ key: String) -> Bool { scratchKeys.contains(key) }

    static func isScratch(_ pid: String?, catalog: ProjectCatalog) -> Bool {
        catalog.project(catalog.root(pid))?.isScratch == true
    }
    /// The band of drafts without a project ("New"), at every level.
    public static let newKey = "new"
    /// Its neutral color (no project's).
    public static let newColor = "#565f89"

    /// The band key of a card at a level.
    public static func key(_ item: ViewItem, level: GroupLevel, catalog: ProjectCatalog, scope: WallScope = .all) -> String {
        let pid = item.projectID
        if item.isNew && level != .none { return newKey }
        switch level {
        case .none:
            return ""
        case .group:
            var gids = catalog.groupIDs(of: pid)
            if case .group(let g) = scope, gids.contains(g) { gids = [g] }
            if let g = gids.first { return "g:" + g }
            guard let pid else { return "g:" + none }
            return "g:" + (isScratch(pid, catalog: catalog) ? scratch : other)
        case .project:
            guard let pid else { return "p:" + none }
            if isScratch(pid, catalog: catalog) { return "p:" + scratch }
            return "p:" + (catalog.root(pid) ?? pid)
        case .branch:
            guard let pid else { return "b:" + none }
            if let root = catalog.root(pid), root != pid { return "b:pkg:" + pid }
            if let b = item.branch, !b.isEmpty { return "b:br:" + b }
            if let w = item.worktree, !w.isEmpty { return "b:wt:" + (w as NSString).lastPathComponent }
            return "b:main"
        }
    }

    /// Overflow keeps these whole: All's own grouping (groups, else projects).
    public static func unit(projectID: String?, catalog: ProjectCatalog) -> String {
        key(ViewItem(id: "", projectID: projectID), level: catalog.hasGroups ? .group : .project, catalog: catalog)
    }

    /// Overflow's unit order: groups in order, then the rest.
    public static func unitOrder(catalog: ProjectCatalog) -> [String] {
        if catalog.hasGroups { return catalog.orderedGroups.map { "g:" + $0.id } + ["g:" + other, "g:" + scratch, "g:" + none] }
        return []
    }

    /// The band key of a pointer line.
    public static func pointerKey(_ wall: String) -> String { "ptr:" + wall }

    /// `pointers`: projects / groups with their own wall, each with its
    /// agents as items (not among `items`). Each becomes a collapsed
    /// pointer band right after the band its agents would have been in
    /// (or where that band would be). Not with grouping None (no bands).
    public static func resolve(_ items: [ViewItem], level: GroupLevel, catalog: ProjectCatalog, scope: WallScope = .all,
                               bandOrder: [String] = [], continued: Set<String> = [],
                               pointers: [(pointer: WallPointer, items: [ViewItem])] = []) -> ResolvedView {
        guard level != .none else { return ResolvedView(level: .none, bands: [], order: items.map(\.id)) }
        var bands: [String: Band] = [:]
        var seen: [String] = []
        for it in items {
            let k = key(it, level: level, catalog: catalog, scope: scope)
            if bands[k] == nil {
                bands[k] = makeBand(k, first: it, level: level, catalog: catalog)
                seen.append(k)
            }
            bands[k]!.members.append(it.id)
            if bands[k]!.projectID == nil, !scratchKeys.contains(k) { bands[k]!.projectID = it.projectID.map { level == .project ? (catalog.root($0) ?? $0) : $0 } }
        }
        let subtitleItems = Dictionary(grouping: items) { key($0, level: level, catalog: catalog, scope: scope) }
        for k in seen {
            bands[k]!.subtitle = subtitle(bands[k]!, items: subtitleItems[k] ?? [], catalog: catalog)
            bands[k]!.continued = continued.contains(unitKey(k, level: level))
        }
        guard !pointers.isEmpty else {
            let ordered = order(seen, level: level, catalog: catalog, user: bandOrder).compactMap { bands[$0] }
            return ResolvedView(level: level, bands: ordered, order: ordered.flatMap(\.members))
        }
        var anchors: [String: [Band]] = [:]
        var anchorKeys: [String] = []
        for (p, its) in pointers {
            guard let first = its.first else { continue }
            let k = key(first, level: level, catalog: catalog, scope: scope)
            if !anchorKeys.contains(k) { anchorKeys.append(k) }
            var b = Band(key: pointerKey(p.wall), title: "", subtitle: "", colorHex: "#565f89", level: level, groupID: nil, projectID: nil,
                         branch: nil, members: [], continued: false, pointer: p)
            switch p.scope {
            case .project(let pid):
                b.projectID = pid
                b.title = catalog.name(project: pid)
                b.colorHex = catalog.colorHex(project: pid)
            case .group(let g):
                b.groupID = g
                b.title = catalog.groups[g]?.name ?? g
                b.colorHex = catalog.colorHex(group: g)
            default:
                b.title = p.scope.title
            }
            anchors[k, default: []].append(b)
        }
        let all = order(seen + anchorKeys.filter { bands[$0] == nil }, level: level, catalog: catalog, user: bandOrder)
        var ordered: [Band] = []
        for k in all {
            if let b = bands[k] { ordered.append(b) }
            ordered += anchors[k] ?? []
        }
        return ResolvedView(level: level, bands: ordered, order: ordered.flatMap(\.members))
    }

    /// Overflow continues units (All's level); a band continues when its
    /// key is that unit.
    static func unitKey(_ k: String, level: GroupLevel) -> String { k }

    static func makeBand(_ k: String, first: ViewItem, level: GroupLevel, catalog: ProjectCatalog) -> Band {
        var b = Band(key: k, title: "", subtitle: "", colorHex: "#565f89", level: level, groupID: nil, projectID: nil, branch: nil, members: [], continued: false)
        if k == newKey {
            b.title = "New"
            b.colorHex = newColor
            return b
        }
        let rest = String(k.drop { $0 != ":" }.dropFirst())
        switch level {
        case .group:
            switch rest {
            case other: b.title = "Other projects"
            case scratch: b.title = scratchTitle; b.colorHex = "#73daca"
            case none: b.title = "No project"
            default:
                b.groupID = rest
                b.title = catalog.groups[rest]?.name ?? rest
                b.colorHex = catalog.colorHex(group: rest)
                if let first = catalog.groups[rest]?.projectIds.first { b.projectID = first }
            }
        case .project:
            if rest == none { b.title = "No project" } else if rest == scratch { b.title = scratchTitle; b.colorHex = "#73daca" } else {
                b.projectID = rest
                b.title = catalog.name(project: rest)
                b.colorHex = catalog.colorHex(project: rest)
            }
        case .branch:
            let pid = first.projectID
            b.colorHex = catalog.colorHex(project: pid)
            if rest == none {
                b.title = "No project"
            } else if rest.hasPrefix("pkg:") {
                b.projectID = String(rest.dropFirst(4))
                b.title = catalog.name(project: b.projectID)
            } else if rest.hasPrefix("br:") {
                b.branch = String(rest.dropFirst(3))
                b.title = b.branch!
                b.projectID = catalog.root(pid) ?? pid
            } else if rest.hasPrefix("wt:") {
                b.title = String(rest.dropFirst(3))
                b.projectID = catalog.root(pid) ?? pid
            } else {
                b.title = "main"
                b.projectID = catalog.root(pid) ?? pid
            }
        case .none:
            break
        }
        return b
    }

    static func subtitle(_ b: Band, items: [ViewItem], catalog: ProjectCatalog) -> String {
        if b.isNew { return "no project yet · choose a folder" }
        func list(_ xs: [String]) -> String {
            var seen: [String] = []
            for x in xs where !x.isEmpty && !seen.contains(x) { seen.append(x) }
            if seen.count <= 3 { return seen.joined(separator: ", ") }
            return seen.prefix(2).joined(separator: ", ") + " +\(seen.count - 2)"
        }
        if scratchKeys.contains(b.key) {
            // The scratches' names, without their folder dates.
            return list(items.map { it in
                let id = catalog.root(it.projectID) ?? it.projectID
                return catalog.project(id).map(ScratchName.display) ?? catalog.name(project: id)
            })
        }
        switch b.level {
        case .group:
            return list(items.map { catalog.name(project: catalog.root($0.projectID) ?? $0.projectID) })
        case .project:
            let pkgs = items.compactMap { it -> String? in
                guard let p = it.projectID, let r = catalog.root(p), r != p else { return nil }
                return catalog.name(project: p)
            }
            let branches = items.compactMap { $0.branch }
            let s = list(pkgs + branches)
            if !s.isEmpty { return s }
            return catalog.project(b.projectID).map { kindLabel($0.kind) } ?? ""
        case .branch:
            if b.key.hasPrefix("b:pkg:") { return "package" }
            if b.key.hasPrefix("b:br:") { return items.contains { $0.worktree != nil } ? "worktree" : "branch" }
            if b.key.hasPrefix("b:wt:") { return "worktree" }
            return "project folder"
        case .none:
            return ""
        }
    }

    static func kindLabel(_ k: ProjectKind) -> String {
        switch k {
        case .repo: return "repository"
        case .package: return "package"
        case .folder: return "folder"
        case .reference: return "reference"
        case .scratch: return "scratch"
        case .unknown: return ""
        }
    }

    /// Default band order, then the wall's own order (header drag) first.
    static func order(_ keys: [String], level: GroupLevel, catalog: ProjectCatalog, user: [String]) -> [String] {
        let groupRank = Dictionary(catalog.orderedGroups.enumerated().map { ($1.id, $0) }, uniquingKeysWith: { a, _ in a })
        func rank(_ k: String) -> (Int, String) {
            let rest = String(k.drop { $0 != ":" }.dropFirst())
            switch level {
            case .group:
                if let r = groupRank[rest] { return (r, "") }
                return (10_000 + ([other, scratch, none].firstIndex(of: rest) ?? 3), rest)
            case .project:
                if rest == none { return (30_000, "") }
                if rest == scratch { return (25_000, "") }
                let p = catalog.project(rest)
                if p?.isScratch == true { return (20_000, (p?.name ?? rest).lowercased()) }
                let g = catalog.groupIDs(of: rest).compactMap { groupRank[$0] }.min() ?? 10_000
                return (g, (p?.name ?? rest).lowercased())
            case .branch:
                if rest == "main" { return (0, "") }
                if rest.hasPrefix("pkg:") { return (1, catalog.name(project: String(rest.dropFirst(4))).lowercased()) }
                if rest.hasPrefix("br:") { return (2, rest) }
                if rest == none { return (9, "") }
                return (3, rest)
            case .none:
                return (0, "")
            }
        }
        let byDefault = keys.sorted { a, b in
            let ra = rank(a), rb = rank(b)
            return ra != rb ? ra < rb : a < b
        }
        // "New" is always first (not part of the wall's own order).
        let new = keys.contains(newKey) ? [newKey] : []
        let mine = user.filter { keys.contains($0) && $0 != newKey }
        return new + mine + byDefault.filter { !mine.contains($0) && $0 != newKey }
    }
}

/// How much room each band (E) gets: by the number of its ACTIVE agents
/// (working, starting, needing you: never by attention), with
/// hysteresis, so a band's height changes only when agents start, stop
/// (go to the shelf) or move — and a single agent flipping between
/// working and done doesn't shrink and regrow its band (and resize every
/// terminal on the wall).
///
/// Rule: a band grows at once to its count; it shrinks only when it lost
/// two or more active agents since its weight was set, or none are left.
public enum BandAllocator {
    public static func weights(active: [String: Int], previous: [String: Int]) -> [String: Int] {
        var out: [String: Int] = [:]
        for (k, n) in active {
            guard let w = previous[k] else { out[k] = n; continue }
            if n >= w || n == 0 || n <= w - 2 { out[k] = n } else { out[k] = w }
        }
        return out
    }
}

/// ⌥↑ / ⌥↓: the first card of the previous / next band that has cards
/// (collapsed bands are skipped); nil: no band that way.
public enum BandNavigation {
    public static func target(bands: [(key: String, members: [String])], current: String?, delta: Int) -> String? {
        let open = bands.filter { !$0.members.isEmpty }
        guard !open.isEmpty, delta != 0 else { return nil }
        guard let current, let i = open.firstIndex(where: { $0.members.contains(current) }) else {
            return (delta > 0 ? open.first : open.last)?.members.first
        }
        let j = i + (delta > 0 ? 1 : -1)
        guard open.indices.contains(j) else { return nil }
        return open[j].members.first
    }
}

/// ⌥⌘← ⌥⌘→ in a single view: the agents of the same project first, then
/// the next project's (projects in band order), wrapping.
public enum ProjectStep {
    /// Wall order, stably sorted so each project's agents are together, in
    /// `projectRank` order (unknown projects after, by first appearance).
    public static func order(_ ids: [String], projectOf: (String) -> String?, projectRank: [String]) -> [String] {
        let rank = Dictionary(projectRank.enumerated().map { ($1, $0) }, uniquingKeysWith: { a, _ in a })
        var first: [String: Int] = [:]
        for (i, id) in ids.enumerated() { let p = projectOf(id) ?? ""; if first[p] == nil { first[p] = i } }
        return ids.enumerated().sorted { l, r in
            let pl = projectOf(l.element) ?? "", pr = projectOf(r.element) ?? ""
            let a = (rank[pl] ?? Int.max, first[pl] ?? 0), b = (rank[pr] ?? Int.max, first[pr] ?? 0)
            return a != b ? a < b : l.offset < r.offset
        }.map(\.element)
    }

    public static func step(from current: String?, by d: Int, in order: [String]) -> String? {
        guard !order.isEmpty, d != 0 else { return nil }
        guard let current, let i = order.firstIndex(of: current) else { return d > 0 ? order.first : order.last }
        return order[((i + (d > 0 ? 1 : -1)) % order.count + order.count) % order.count]
    }
}

// MARK: Sidebar

public struct SidebarRow: Equatable, Sendable, Identifiable {
    public enum Kind: Equatable, Sendable { case all, group, project }
    public var id: String
    public var kind: Kind
    public var title: String
    public var colorHex: String
    public var count: Int
    public var needsYou: Bool
    public var depth: Int
    public var groupID: String?
    public var projectID: String?
    public var scratch: Bool
    /// What a click gives the wall.
    public var scope: WallScope
}

public struct SidebarSection: Equatable, Sendable, Identifiable {
    public var id: String
    public var title: String?
    public var rows: [SidebarRow]
}

/// The project sidebar (⌘0): All, then groups → their projects, the
/// projects in no group, and the scratch folders; counts of agents
/// everywhere (not just this wall), a rose dot where something needs you.
/// Packages show under their repository once an agent works in one.
public enum SidebarModel {
    public static func make(catalog: ProjectCatalog, agents: [Agent]) -> [SidebarSection] {
        var byProject: [String: [Agent]] = [:]
        var known = Set(catalog.projects.keys)
        for a in agents {
            guard let pid = catalog.projectID(for: a) else { continue }
            for p in catalog.lineage(pid) { byProject[p, default: []].append(a) }
            known.insert(pid)
        }
        func row(project pid: String, group: String?, depth: Int) -> SidebarRow {
            let list = byProject[pid] ?? []
            let p = catalog.project(pid)
            return SidebarRow(id: (group.map { "g:\($0)/" } ?? "") + "p:" + pid, kind: .project, title: p?.name ?? pid,
                              colorHex: catalog.colorHex(project: pid), count: list.count, needsYou: list.contains { $0.state.needsAttention },
                              depth: depth, groupID: group, projectID: pid, scratch: p?.isScratch ?? false, scope: .project(pid))
        }
        func children(_ pid: String, group: String?, depth: Int) -> [SidebarRow] {
            known.filter { catalog.projects[$0]?.parentId == pid && (byProject[$0]?.isEmpty == false) }
                .sorted { catalog.name(project: $0).lowercased() < catalog.name(project: $1).lowercased() }
                .map { row(project: $0, group: group, depth: depth) }
        }
        let tops = known.filter { catalog.projects[$0]?.parentId == nil || catalog.projects[catalog.projects[$0]!.parentId!] == nil }
        func sorted(_ ids: [String]) -> [String] { ids.sorted { catalog.name(project: $0).lowercased() < catalog.name(project: $1).lowercased() } }

        var sections: [SidebarSection] = []
        let total = agents.count
        sections.append(SidebarSection(id: "all", title: nil, rows: [
            SidebarRow(id: "all", kind: .all, title: "All", colorHex: "#7aa2f7", count: total, needsYou: agents.contains { $0.state.needsAttention },
                       depth: 0, groupID: nil, projectID: nil, scratch: false, scope: .all),
        ]))
        var grouped: Set<String> = []
        var groupRows: [SidebarRow] = []
        for g in catalog.orderedGroups {
            let members = tops.filter { catalog.groupIDs(of: $0).contains(g.id) }
            let list = agents.filter { catalog.groupIDs(of: catalog.projectID(for: $0)).contains(g.id) }
            groupRows.append(SidebarRow(id: "g:" + g.id, kind: .group, title: g.name, colorHex: catalog.colorHex(group: g.id), count: list.count,
                                        needsYou: list.contains { $0.state.needsAttention }, depth: 0, groupID: g.id, projectID: nil, scratch: false,
                                        scope: .group(g.id)))
            for pid in sorted(Array(members)) {
                grouped.insert(pid)
                groupRows.append(row(project: pid, group: g.id, depth: 1))
                groupRows += children(pid, group: g.id, depth: 2)
            }
        }
        if !groupRows.isEmpty { sections.append(SidebarSection(id: "groups", title: "Groups", rows: groupRows)) }
        let loose = sorted(tops.filter { !grouped.contains($0) && catalog.project($0)?.isScratch != true })
        if !loose.isEmpty {
            sections.append(SidebarSection(id: "projects", title: catalog.hasGroups ? "Other projects" : "Projects",
                                           rows: loose.flatMap { [row(project: $0, group: nil, depth: 0)] + children($0, group: nil, depth: 1) }))
        }
        let scratch = sorted(tops.filter { !grouped.contains($0) && catalog.project($0)?.isScratch == true })
        if !scratch.isEmpty {
            sections.append(SidebarSection(id: "scratch", title: "scratch", rows: scratch.map { row(project: $0, group: nil, depth: 0) }))
        }
        return sections
    }
}

// MARK: Home wall

/// The first wall of the window set is home: anything needing you shows
/// there, as a compact "needs you" card when it isn't visible on that wall
/// (scoped out, or in a collapsed band).
public enum HomeWall {
    public static func needsYou(_ agents: [Agent], visible: Set<String>) -> [Agent] {
        AttentionQueue.ordered(agents.filter { $0.state.needsAttention && !visible.contains($0.id) })
    }

    /// The strip's row is reserved whenever the home wall can hide an
    /// agent (a scope other than All, or collapsed bands), so a card
    /// appearing never resizes the wall's terminals.
    public static func reservesStrip(scope: WallScope, collapsedBands: Int, pointers: Int = 0) -> Bool {
        scope != .all || collapsedBands > 0 || pointers > 0
    }
}
