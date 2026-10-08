import Foundation

// The project sidebar (⌘0) as a calm navigator: what runs now, what ran
// lately, and a search over everything else (UI/ProjectSidebar.swift).
//
//   All agents                      ■2 ■5 ■3
//   ACTIVE      projects with agents (any Mac): needs you, working, recent
//   RECENT      ≤ 5 projects used in the last 7 days, no agents now
//   All projects (45) ›             the full list, in search mode
//   ■ laptop 6  ■ mini 4            machines (footer)
//
// Scratch folders (a date-prefixed name, hesperd's scratch kind) and agent
// worktrees (a folder under a project, or in a `worktrees` folder) fold
// under their parent project; never listed unless they run something or
// match a search. Pure rules, unit tested (ProjectNavTests).

/// Agents of a row as the three counts the sidebar shows: needs you
/// (approval, question, error), working (starting, working), done.
public struct StateTally: Equatable, Hashable, Sendable {
    public var needsYou = 0
    public var working = 0
    public var done = 0
    public var total = 0

    public init() {}

    public init(_ agents: [Agent]) {
        for a in agents { add(a) }
    }

    public mutating func add(_ a: Agent) {
        total += 1
        if a.state.needsAttention { needsYou += 1 } else if a.state == .working || a.state == .starting { working += 1 } else if a.state == .done { done += 1 }
    }

    public static func + (l: StateTally, r: StateTally) -> StateTally {
        var t = l
        t.needsYou += r.needsYou; t.working += r.working; t.done += r.done; t.total += r.total
        return t
    }

    public var isEmpty: Bool { total == 0 }

    /// The squares to draw, in order, only the non-zero ones.
    public var parts: [(kind: StateMarkKind, count: Int)] {
        [(StateMarkKind.needsYou, needsYou), (.working, working), (.done, done)].filter { $0.1 > 0 }
    }

    /// "2 need you, 5 working, 3 done" (VoiceOver).
    public var spoken: String {
        var out: [String] = []
        if needsYou > 0 { out.append("\(needsYou) need\(needsYou == 1 ? "s" : "") you") }
        if working > 0 { out.append("\(working) working") }
        if done > 0 { out.append("\(done) done") }
        return out.isEmpty ? (total == 0 ? "no agents" : "\(total) agents") : out.joined(separator: ", ")
    }
}

/// One line of the navigator.
public struct ProjectNavRow: Equatable, Sendable, Identifiable {
    public enum Kind: Equatable, Sendable {
        /// "All agents".
        case all
        /// A catalog group (expands to its projects).
        case group
        /// A project (or a scratch folder / worktree shown on its own).
        case project
        /// An action under the selected quiet project.
        case newAgent, history
        /// "All projects (n) ›": the full list.
        case allProjects
        /// The folded scratch folders of the full list.
        case scratch
        /// Search found nothing.
        case empty
    }

    public var id: String
    public var kind: Kind
    public var title: String
    /// Dim text after the title: a worktree's label, a duplicate's
    /// machine or folder.
    public var suffix: String?
    /// Mono meta on the right: a machine, a branch, an age.
    public var meta: String?
    public var tally = StateTally()
    public var projectID: String?
    public var groupID: String?
    public var depth = 0
    /// A caret (groups, the scratch fold).
    public var expandable = false
    public var expanded = false
    /// No live agents: quieter text.
    public var quiet = false
    public var colorHex = "#7aa2f7"
    public var lastActivity: Date?
    /// The row as the old sidebar row (menus, click, drag, tests); nil
    /// for actions, "All projects", the scratch fold and "no match".
    public var base: SidebarRow?

    /// What a click gives the wall.
    public var scope: WallScope? { base?.scope }
    /// Rows that act (everything but "no match").
    public var isInteractive: Bool { kind != .empty }

    public init(id: String, kind: Kind, title: String) {
        self.id = id; self.kind = kind; self.title = title
    }
}

public struct ProjectNavSection: Equatable, Sendable, Identifiable {
    public var id: String
    /// Mono caps heading; nil: no heading (All agents, the footer link).
    public var title: String?
    public var rows: [ProjectNavRow]
    public init(id: String, title: String?, rows: [ProjectNavRow]) { self.id = id; self.title = title; self.rows = rows }
}

/// What the person did to the navigator (per wall, not persisted).
public struct ProjectNavState: Equatable, Sendable {
    /// The filter field; non-empty: results replace the sections.
    public var query = ""
    /// "All projects ›" was opened (the full list, filterable).
    public var showAll = false
    public var expandedGroups: Set<String> = []
    public var scratchExpanded = false
    /// The quiet project selected last (its actions show under it).
    public var selectedProject: String?
    public init() {}

    public var searching: Bool { !query.trimmingCharacters(in: .whitespaces).isEmpty }
}

public enum ProjectNav {
    /// Sizes the sidebar uses (pure, so they're tested and named).
    public enum Metrics {
        public static let width: Double = 232
        public static func rowHeight(_ d: DesignTokens.Density) -> Double { d == .comfortable ? 28 : 24 }
        /// Above a section heading.
        public static let sectionGap: Double = 16
        /// The selected / hovered row's project color, on its left edge.
        public static let accentBar: Double = 3
        public static let recentLimit = 5
        public static let recentWindow: TimeInterval = 7 * 24 * 3600
        public static let searchLimit = 60
    }

    public static let allRow = "all"
    public static let allProjectsRow = "nav:all-projects"
    public static let scratchRow = "nav:scratch"

    // MARK: Building

    public static func make(catalog: ProjectCatalog, agents: [Agent], machines: [Machine] = [], local: String? = nil,
                            now: Date = Date(), state: ProjectNavState = ProjectNavState(),
                            historyAvailable: Bool = false) -> [ProjectNavSection] {
        let ix = Index(catalog: catalog, agents: agents, machines: machines, local: local)
        var all = ProjectNavRow(id: allRow, kind: .all, title: "All agents")
        all.tally = StateTally(agents)
        all.base = SidebarRow(id: allRow, kind: .all, title: "All agents", colorHex: "#7aa2f7", count: agents.count,
                              needsYou: all.tally.needsYou > 0, depth: 0, groupID: nil, projectID: nil, scratch: false, scope: .all)
        var out = [ProjectNavSection(id: "all", title: nil, rows: [all])]
        if state.searching {
            out.append(search(state.query, ix, now: now))
            return out
        }
        if state.showAll {
            out += full(ix, state: state, now: now)
            return out
        }
        let active = activeRows(ix, state: state)
        if !active.isEmpty { out.append(ProjectNavSection(id: "active", title: "Active", rows: active)) }
        var recent = recentRows(ix, now: now)
        // The selected quiet project offers what fits an empty wall.
        if let sel = state.selectedProject, let i = recent.firstIndex(where: { $0.projectID == sel }) {
            recent.insert(contentsOf: actions(for: recent[i], history: historyAvailable), at: i + 1)
        }
        if !recent.isEmpty { out.append(ProjectNavSection(id: "recent", title: "Recent", rows: recent)) }
        var more = ProjectNavRow(id: allProjectsRow, kind: .allProjects, title: "All projects")
        more.meta = "\(ix.listed.count)"
        more.quiet = true
        out.append(ProjectNavSection(id: "more", title: nil, rows: [more]))
        return out
    }

    /// Active: the units with agents, groups first-class, most urgent first.
    static func activeRows(_ ix: Index, state: ProjectNavState) -> [ProjectNavRow] {
        var byGroup: [String: [ProjectNavRow]] = [:]
        var loose: [ProjectNavRow] = []
        for (unit, list) in ix.byUnit {
            let gid = ix.isFolded(unit) ? nil : ix.catalog.groupIDs(of: unit).first
            var r = ix.projectRow(unit, agents: list, group: gid, depth: gid == nil ? 0 : 1)
            r.meta = meta(list, ix, folded: ix.isFolded(unit))
            if r.meta == r.suffix { r.meta = nil }
            if let gid { byGroup[gid, default: []].append(r) } else { loose.append(r) }
        }
        var top: [(row: ProjectNavRow, children: [ProjectNavRow])] = loose.map { ($0, []) }
        for (gid, kids) in byGroup {
            guard let g = ix.catalog.groups[gid] else { continue }
            var r = ix.groupRow(g, depth: 0)
            r.tally = kids.reduce(StateTally()) { $0 + $1.tally }
            r.lastActivity = kids.compactMap(\.lastActivity).max()
            r.base?.count = r.tally.total
            r.base?.needsYou = r.tally.needsYou > 0
            r.expanded = state.expandedGroups.contains(gid)
            top.append((r, sortActive(kids)))
        }
        let order = sortActive(top.map(\.row)).map(\.id)
        let byID = Dictionary(top.map { ($0.row.id, $0) }, uniquingKeysWith: { a, _ in a })
        return order.flatMap { id -> [ProjectNavRow] in
            guard let e = byID[id] else { return [] }
            return [e.row] + (e.row.expanded ? e.children : [])
        }
    }

    /// Needs you first, then working, then the most recent activity.
    public static func sortActive(_ rows: [ProjectNavRow]) -> [ProjectNavRow] {
        rows.sorted { l, r in
            let a = (l.tally.needsYou > 0 ? 0 : 1, l.tally.working > 0 ? 0 : 1), b = (r.tally.needsYou > 0 ? 0 : 1, r.tally.working > 0 ? 0 : 1)
            if a != b { return a < b }
            let la = l.lastActivity ?? .distantPast, ra = r.lastActivity ?? .distantPast
            if la != ra { return la > ra }
            return (l.title.lowercased(), l.id) < (r.title.lowercased(), r.id)
        }
    }

    /// The Mono meta of an active row, only when it says something: the
    /// machine when every agent runs on another Mac, else the branch when
    /// they all share one that isn't the main line.
    static func meta(_ list: [Agent], _ ix: Index, folded: Bool = false) -> String? {
        let ms = Set(list.map(\.machine))
        if ms.count == 1, let m = ms.first, m != ix.local, ix.local != nil { return ix.machineName(m) }
        if folded { return nil } // a worktree's label already says it
        let branches = Set(list.map { $0.branch ?? "" })
        if branches.count == 1, let b = branches.first, !b.isEmpty, !mainBranches.contains(b) { return b }
        return nil
    }

    static let mainBranches: Set<String> = ["main", "master", "trunk", "develop", "HEAD"]

    /// Recent: projects used in the last 7 days with no agents now.
    static func recentRows(_ ix: Index, now: Date) -> [ProjectNavRow] {
        let live = Set(ix.byUnit.keys)
        let picks = ix.listed.compactMap { pid -> (String, Date)? in
            guard !live.contains(pid), let d = ix.catalog.projects[pid]?.lastUsed, now.timeIntervalSince(d) <= Metrics.recentWindow else { return nil }
            return (pid, d)
        }.sorted { $0.1 != $1.1 ? $0.1 > $1.1 : $0.0 < $1.0 }.prefix(Metrics.recentLimit)
        return picks.map { pid, d in
            var r = ix.projectRow(pid, agents: [], group: nil, depth: 0)
            r.meta = age(d, now: now)
            r.quiet = true
            return r
        }
    }

    static func actions(for r: ProjectNavRow, history: Bool) -> [ProjectNavRow] {
        guard let pid = r.projectID else { return [] }
        var new = ProjectNavRow(id: "nav:new/" + pid, kind: .newAgent, title: "New agent here")
        new.projectID = pid; new.depth = 1
        guard history else { return [new] }
        var h = ProjectNavRow(id: "nav:history/" + pid, kind: .history, title: "Show history")
        h.projectID = pid; h.depth = 1; h.meta = nil
        return [new, h]
    }

    /// "All projects ›": groups, every listed project A–Z, then the
    /// scratch folders no project claims, folded.
    static func full(_ ix: Index, state: ProjectNavState, now: Date) -> [ProjectNavSection] {
        var out: [ProjectNavSection] = []
        let groups = ix.catalog.orderedGroups
        if !groups.isEmpty {
            var rows: [ProjectNavRow] = []
            for g in groups {
                var r = ix.groupRow(g, depth: 0)
                let members = g.projectIds.filter { ix.catalog.projects[$0] != nil }
                r.tally = members.reduce(StateTally()) { $0 + StateTally(ix.byUnit[$1] ?? []) }
                r.expanded = state.expandedGroups.contains(g.id)
                r.quiet = r.tally.isEmpty
                rows.append(r)
                if r.expanded {
                    rows += sortByName(members.map { pid in
                        var c = ix.projectRow(pid, agents: ix.byUnit[pid] ?? [], group: g.id, depth: 1)
                        if c.tally.isEmpty { c.meta = ix.catalog.projects[pid]?.lastUsed.map { age($0, now: now) }; c.quiet = true }
                        return c
                    })
                }
            }
            out.append(ProjectNavSection(id: "groups", title: "Groups", rows: rows))
        }
        let rows = sortByName(ix.listed.map { pid in
            var r = ix.projectRow(pid, agents: ix.byUnit[pid] ?? [], group: nil, depth: 0)
            // Ages only while they still say something (two weeks).
            if r.tally.isEmpty {
                r.meta = ix.catalog.projects[pid]?.lastUsed.flatMap { now.timeIntervalSince($0) <= 2 * Metrics.recentWindow ? age($0, now: now) : nil }
                r.quiet = true
            }
            return r
        })
        out.append(ProjectNavSection(id: "projects", title: "All projects", rows: rows))
        let orphans = ix.scratchIDs.filter { ix.parent[$0] == nil }
        if !orphans.isEmpty {
            var fold = ProjectNavRow(id: scratchRow, kind: .scratch, title: "Scratch")
            fold.meta = "\(orphans.count)"
            fold.expandable = true
            fold.expanded = state.scratchExpanded
            fold.quiet = true
            var rows = [fold]
            if state.scratchExpanded {
                rows += orphans.map { pid -> ProjectNavRow in
                    var r = ix.projectRow(pid, agents: ix.byUnit[pid] ?? [], group: nil, depth: 1)
                    if r.tally.isEmpty { r.meta = ix.catalog.project(pid)?.lastUsed.map { age($0, now: now) }; r.quiet = true }
                    return r
                }.sorted { ($0.lastActivity ?? ix.lastUsed($0.projectID), $0.id) > ($1.lastActivity ?? ix.lastUsed($1.projectID), $1.id) }
            }
            out.append(ProjectNavSection(id: "scratch", title: nil, rows: rows))
        }
        return out
    }

    static func sortByName(_ rows: [ProjectNavRow]) -> [ProjectNavRow] {
        rows.sorted { ($0.title.lowercased(), $0.suffix ?? "", $0.id) < ($1.title.lowercased(), $1.suffix ?? "", $1.id) }
    }

    /// The filter: every catalog project and group, best matches first.
    static func search(_ query: String, _ ix: Index, now: Date) -> ProjectNavSection {
        let q = query.trimmingCharacters(in: .whitespaces)
        var hits: [(score: Int, row: ProjectNavRow)] = []
        for g in ix.catalog.orderedGroups {
            guard let s = score(q, name: g.name, paths: []) else { continue }
            var r = ix.groupRow(g, depth: 0)
            r.tally = g.projectIds.reduce(StateTally()) { $0 + StateTally(ix.byUnit[$1] ?? []) }
            r.quiet = r.tally.isEmpty
            hits.append((s, r))
        }
        for pid in ix.allIDs {
            guard let p = ix.catalog.project(pid) else { continue }
            let shown = ix.title(pid)
            let names = [p.name, shown.title, shown.suffix ?? ""]
            let best = names.compactMap { score(q, name: $0, paths: []) }.min()
            guard let s = best ?? score(q, name: "", paths: Array(p.paths.values)) else { continue }
            var r = ix.projectRow(pid, agents: ix.byUnit[pid] ?? [], group: nil, depth: 0)
            if r.tally.isEmpty { r.meta = p.lastUsed.map { age($0, now: now) }; r.quiet = true } else { r.meta = meta(ix.byUnit[pid] ?? [], ix, folded: ix.isFolded(pid)); if r.meta == r.suffix { r.meta = nil } }
            hits.append((s, r))
        }
        let rows = hits.sorted { l, r in
            if l.score != r.score { return l.score < r.score }
            if l.row.tally.isEmpty != r.row.tally.isEmpty { return !l.row.tally.isEmpty }
            return (l.row.title.lowercased(), l.row.suffix ?? "", l.row.id) < (r.row.title.lowercased(), r.row.suffix ?? "", r.row.id)
        }.prefix(Metrics.searchLimit).map(\.row)
        if rows.isEmpty {
            return ProjectNavSection(id: "results", title: "No matches", rows: [ProjectNavRow(id: "nav:empty", kind: .empty, title: "No project matches “\(q)”")])
        }
        return ProjectNavSection(id: "results", title: rows.count == 1 ? "1 match" : "\(rows.count) matches", rows: Array(rows))
    }

    // MARK: Matching

    /// How well `query` matches (lower is better; nil: no match):
    /// 0 the name starts with it, 1 a word in the name does, 2 the name
    /// contains it, 3 its letters appear in order in the name, 4 a folder
    /// path contains it. Case-insensitive.
    public static func score(_ query: String, name: String, paths: [String]) -> Int? {
        let q = query.lowercased().trimmingCharacters(in: .whitespaces)
        guard !q.isEmpty else { return 0 }
        let n = name.lowercased()
        if !n.isEmpty {
            if n.hasPrefix(q) { return 0 }
            if let r = n.range(of: q) {
                let before = n[n.index(before: r.lowerBound)]
                return "-_ ./".contains(before) ? 1 : 2
            }
            if q.count >= 2, isSubsequence(q, of: n) { return 3 }
        }
        if paths.contains(where: { $0.lowercased().contains(q) }) { return 4 }
        return nil
    }

    static func isSubsequence(_ q: String, of s: String) -> Bool {
        var it = s.makeIterator()
        outer: for c in q where c != " " {
            while let d = it.next() { if d == c { continue outer } }
            return false
        }
        return true
    }

    // MARK: Scratch folders and worktrees

    /// "2026-10-06-please-understand" → true.
    public static func isDatePrefixed(_ name: String) -> Bool {
        let c = Array(name.utf8)
        guard c.count > 11 else { return false }
        let digits: [Int] = [0, 1, 2, 3, 5, 6, 8, 9]
        return digits.allSatisfy { c[$0] >= 48 && c[$0] <= 57 } && c[4] == 45 && c[7] == 45 && c[10] == 45
    }

    /// The name without its date: "please-understand".
    public static func label(_ name: String) -> String {
        isDatePrefixed(name) ? String(name.dropFirst(11)) : name
    }

    static let worktreeDirs: Set<String> = ["worktrees", ".worktrees"]

    /// A folder inside an agent worktrees folder (`…/.claude/worktrees/x`,
    /// `…/worktrees/repo/x`, `repo.worktrees/x`).
    public static func isWorktreePath(_ path: String) -> Bool {
        let parts = path.split(separator: "/").map(String.init)
        for (i, c) in parts.enumerated().dropLast() {
            if c.hasSuffix("-worktrees") || (c.hasSuffix(".worktrees") && c != ".worktrees") { return true }
            // `worktrees` itself: inside a tool's folder (.claude, .git,
            // a repo's .worktrees), or a repo's folder under it.
            if worktreeDirs.contains(c), c == ".worktrees" || (i > 0 && parts[i - 1].hasPrefix(".")) || i + 2 < parts.count { return true }
        }
        return false
    }

    /// A scratch folder or a worktree: folded, never listed on its own.
    public static func isScratch(_ p: Project) -> Bool {
        p.kind == .scratch || isDatePrefixed(p.name) || p.paths.values.contains(where: isWorktreePath)
    }

    /// The project a scratch folder or worktree belongs to: its parent
    /// id, else the project whose folder holds it (on that machine), else
    /// the repository its `worktrees` folder is named after, else the one
    /// with the same identity (git remote); nil: no parent.
    public static func parent(of p: Project, in candidates: [Project]) -> String? {
        let real = candidates.filter { $0.id != p.id && !isScratch($0) }
        if let pid = p.parentId, real.contains(where: { $0.id == pid }) { return pid }
        var best: (id: String, len: Int)?
        for (m, path) in p.paths {
            for q in real {
                let roots = m == "*" ? Array(q.paths.values) : q.paths[m].map { [$0] } ?? []
                for root in roots where !root.isEmpty {
                    let prefix = root.hasSuffix("/") ? root : root + "/"
                    if path.hasPrefix(prefix), best == nil || root.count > best!.len { best = (q.id, root.count) }
                }
            }
        }
        if let best { return best.id }
        for path in p.paths.values {
            let parts = path.split(separator: "/").map(String.init)
            for (i, c) in parts.enumerated().dropLast() {
                var base: String?
                if worktreeDirs.contains(c), i + 2 < parts.count { base = parts[i + 1] }
                for suffix in ["-worktrees", ".worktrees"] where c.hasSuffix(suffix) && c.count > suffix.count { base = String(c.dropLast(suffix.count)) }
                guard let base else { continue }
                let hit = real.filter { q in q.paths.values.contains { ($0 as NSString).lastPathComponent == base } }
                    .sorted { $0.id < $1.id }.first
                if let hit { return hit.id }
            }
        }
        if let ident = p.identity, !ident.isEmpty {
            let same = real.filter { $0.identity == ident }.sorted { ($0.parentId != nil ? 1 : 0, $0.id) < ($1.parentId != nil ? 1 : 0, $1.id) }
            if let first = same.first { return first.id }
        }
        return nil
    }

    // MARK: Duplicate names

    /// A dim suffix for projects sharing a name: the machine when only
    /// that one is on it, else the folder it sits in ("~/old"). Projects
    /// with a unique name get none.
    public static func disambiguate(_ projects: [Project], machines: [Machine] = []) -> [String: String] {
        var out: [String: String] = [:]
        let byName = Dictionary(grouping: projects) { $0.name.lowercased() }
        for (_, same) in byName where same.count > 1 {
            let sets = same.map { Set($0.paths.keys.filter { $0 != "*" }) }
            for (i, p) in same.enumerated() {
                let mine = sets[i]
                let others = sets.enumerated().filter { $0.offset != i }.map(\.element)
                if !mine.isEmpty, others.allSatisfy({ $0.isDisjoint(with: mine) }), mine.count == 1, let m = mine.first {
                    out[p.id] = MachineLabel.name(short: m, in: machines)
                } else if let path = p.path(on: nil), !path.isEmpty {
                    out[p.id] = folderLabel((path as NSString).deletingLastPathComponent)
                }
            }
            // Still the same (same folder on two machines): the machines.
            let labels = same.map { out[$0.id] ?? "" }
            if Set(labels).count < labels.count {
                for (i, p) in same.enumerated() {
                    let ms = sets[i].sorted().map { MachineLabel.name(short: $0, in: machines) }
                    if !ms.isEmpty { out[p.id] = [out[p.id], ms.joined(separator: "+")].compactMap { $0 }.joined(separator: " · ") }
                }
            }
        }
        return out
    }

    /// A folder as a short label: "~/old", "…/clones".
    public static func folderLabel(_ path: String) -> String {
        var parts = path.split(separator: "/").map(String.init)
        var home = false
        if parts.count >= 2, parts[0] == "Users" || parts[0] == "home" { parts.removeFirst(2); home = true }
        if parts.isEmpty { return home ? "~" : "/" }
        if parts.count == 1 { return (home ? "~/" : "/") + parts[0] }
        return "…/" + parts[parts.count - 1]
    }

    // MARK: Ages

    /// "now", "12m", "2h", "3d", "2w".
    public static func age(_ d: Date, now: Date) -> String {
        let s = max(0, now.timeIntervalSince(d))
        if s < 60 { return "now" }
        if s < 3600 { return "\(Int(s / 60))m" }
        if s < 86400 { return "\(Int(s / 3600))h" }
        if s < 14 * 86400 { return "\(Int(s / 86400))d" }
        return "\(Int(s / (7 * 86400)))w"
    }

    // MARK: Index

    /// Everything the rules look up, computed once per build.
    struct Index {
        let catalog: ProjectCatalog
        let machines: [Machine]
        let local: String?
        /// Agents by the row they show under (a package's under its
        /// repository; a scratch folder or worktree under itself).
        var byUnit: [String: [Agent]] = [:]
        /// Every project id known (catalog + agents' synthesized ones).
        var allIDs: [String] = []
        /// Real top-level projects (the full list; Recent picks from it).
        var listed: [String] = []
        var scratchIDs: [String] = []
        /// A scratch folder / worktree → its project.
        var parent: [String: String] = [:]
        var suffix: [String: String] = [:]

        init(catalog: ProjectCatalog, agents: [Agent], machines: [Machine], local: String?) {
            self.catalog = catalog; self.machines = machines; self.local = local
            var known = Set(catalog.projects.keys)
            var scratch: Set<String> = []
            for a in agents {
                guard let pid = catalog.projectID(for: a) else { continue }
                known.insert(pid)
                if let p = catalog.project(pid), ProjectNav.isScratch(p) { scratch.insert(pid) }
            }
            allIDs = known.sorted()
            let real = catalog.projects.values.filter { !ProjectNav.isScratch($0) }
            for id in allIDs {
                guard let p = catalog.project(id) else { continue }
                if ProjectNav.isScratch(p) {
                    scratch.insert(id)
                    if let par = ProjectNav.parent(of: p, in: Array(real)) { parent[id] = catalog.root(par) ?? par }
                }
            }
            scratchIDs = scratch.sorted()
            listed = real.filter { p in p.parentId == nil || catalog.projects[p.parentId!] == nil }.map(\.id).sorted()
            for a in agents {
                guard let pid = catalog.projectID(for: a) else { continue }
                let unit = scratch.contains(pid) ? pid : (catalog.root(pid) ?? pid)
                byUnit[unit, default: []].append(a)
            }
            suffix = ProjectNav.disambiguate(real.filter { $0.parentId == nil || catalog.projects[$0.parentId!] == nil }, machines: machines)
        }

        func isFolded(_ pid: String) -> Bool { scratchIDs.contains(pid) }

        func machineName(_ short: String) -> String { MachineLabel.name(short: short, in: machines) }

        func lastUsed(_ pid: String?) -> Date { catalog.project(pid)?.lastUsed ?? .distantPast }

        /// The shown name: a worktree as "parent · label", a duplicate
        /// with its machine or folder.
        func title(_ pid: String) -> (title: String, suffix: String?) {
            guard let p = catalog.project(pid) else { return (pid, nil) }
            if isFolded(pid) {
                let l = ProjectNav.label(p.name)
                if let par = parent[pid], let pp = catalog.projects[par] { return (pp.name, l == pp.name ? nil : l) }
                return (l, nil)
            }
            return (p.name, suffix[pid])
        }

        func projectRow(_ pid: String, agents: [Agent], group: String?, depth: Int) -> ProjectNavRow {
            let t = title(pid)
            var r = ProjectNavRow(id: (group.map { "g:\($0)/" } ?? "") + "p:" + pid, kind: .project, title: t.title)
            r.suffix = t.suffix
            r.tally = StateTally(agents)
            r.projectID = pid
            r.groupID = group
            r.depth = depth
            r.quiet = agents.isEmpty
            r.colorHex = catalog.colorHex(project: parent[pid] ?? pid)
            r.lastActivity = agents.compactMap { $0.stateSince ?? $0.created }.max()
            r.base = SidebarRow(id: r.id, kind: .project, title: catalog.project(pid)?.name ?? pid, colorHex: r.colorHex, count: agents.count,
                                needsYou: r.tally.needsYou > 0, depth: depth, groupID: group, projectID: pid,
                                scratch: isFolded(pid), scope: .project(pid))
            return r
        }

        func groupRow(_ g: ProjectGroup, depth: Int) -> ProjectNavRow {
            var r = ProjectNavRow(id: "g:" + g.id, kind: .group, title: g.name)
            r.groupID = g.id
            r.depth = depth
            r.expandable = true
            r.colorHex = catalog.colorHex(group: g.id)
            r.base = SidebarRow(id: r.id, kind: .group, title: g.name, colorHex: r.colorHex, count: 0, needsYou: false, depth: depth,
                                groupID: g.id, projectID: nil, scratch: false, scope: .group(g.id))
            return r
        }
    }

    /// The rows in order (for keyboard navigation): every row that acts.
    public static func flat(_ sections: [ProjectNavSection]) -> [ProjectNavRow] {
        sections.flatMap(\.rows).filter(\.isInteractive)
    }

    /// ↑ / ↓ from the current row (nil: from the top or bottom).
    public static func step(_ rows: [ProjectNavRow], from current: String?, by d: Int) -> String? {
        guard !rows.isEmpty, d != 0 else { return current }
        guard let current, let i = rows.firstIndex(where: { $0.id == current }) else { return (d > 0 ? rows.first : rows.last)?.id }
        return rows[max(0, min(rows.count - 1, i + d))].id
    }
}
