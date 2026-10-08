import Foundation

// Projects and groups (docs/rebuild-contract.md "As built — projects
// (views)"): the daemon's data (`projects.list`, `groups.list` and their
// events) as the app keeps it, plus a fallback for a daemon without
// projects: every agent folder becomes a synthesized project.

public enum ProjectKind: String, Codable, Sendable, CaseIterable {
    case repo, package, folder, reference, scratch, unknown

    public init(from decoder: any Decoder) throws {
        let raw = try decoder.singleValueContainer().decode(String.self)
        self = ProjectKind(rawValue: raw) ?? .unknown
    }
}

public struct ProjectDefaults: Codable, Equatable, Hashable, Sendable {
    public var profile: String?
    public var machine: String?
    public init(profile: String? = nil, machine: String? = nil) { self.profile = profile; self.machine = machine }
}

/// `projects.list` → `[Project]` (wire, data agent's step 1).
public struct Project: Codable, Equatable, Sendable, Identifiable {
    public var id: String
    public var name: String
    /// "#rrggbb" (hesperd: the user's, else its automatic pick; used as
    /// given) or a palette name; nil: picked from the id.
    public var color: String?
    /// The user set the color (else `color` is hesperd's automatic one).
    public var colorSet = false
    public var kind: ProjectKind
    /// Normalized git remote (`identity.remote`, else `package`, else
    /// `local` of hesperd's identity object).
    public var identity: String?
    /// A package's repository project.
    public var parentId: String?
    /// The folder per machine (short name → path).
    public var paths: [String: String]
    public var groups: [String]
    public var defaults: ProjectDefaults?
    public var detectedPackages: [String]
    public var lastUsed: Date?
    /// Made up by the app (a daemon without projects): not editable.
    public var synthesized = false
    /// A scratch's lifecycle (`kind: "scratch"`, Scratch.swift); nil from
    /// an older hesperd.
    public var scratch: ScratchInfo?

    public init(id: String, name: String, color: String? = nil, kind: ProjectKind = .repo, identity: String? = nil, parentId: String? = nil,
                paths: [String: String] = [:], groups: [String] = [], defaults: ProjectDefaults? = nil, detectedPackages: [String] = [],
                lastUsed: Date? = nil, synthesized: Bool = false, scratch: ScratchInfo? = nil) {
        self.id = id; self.name = name; self.color = color; self.kind = kind; self.identity = identity; self.parentId = parentId
        self.paths = paths; self.groups = groups; self.defaults = defaults; self.detectedPackages = detectedPackages
        self.lastUsed = lastUsed; self.synthesized = synthesized; self.scratch = scratch
    }

    enum CodingKeys: String, CodingKey { case id, name, color, colorSet, kind, identity, parentId, paths, groups, defaults, detectedPackages, lastUsed, scratch }

    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        id = try c.decode(String.self, forKey: .id)
        name = try c.decodeIfPresent(String.self, forKey: .name) ?? id
        color = try c.decodeIfPresent(String.self, forKey: .color).flatMap { $0.isEmpty ? nil : $0 }
        colorSet = (try? c.decodeIfPresent(Bool.self, forKey: .colorSet)) ?? false
        kind = (try? c.decodeIfPresent(ProjectKind.self, forKey: .kind)) ?? .repo
        if let s = try? c.decodeIfPresent(String.self, forKey: .identity) {
            identity = s
        } else if let o = try? c.decodeIfPresent([String: String].self, forKey: .identity) {
            identity = [o["remote"], o["package"].map { "#" + $0 }, o["local"]].compactMap { $0 }.joined()
        }
        parentId = try c.decodeIfPresent(String.self, forKey: .parentId).flatMap { $0.isEmpty ? nil : $0 }
        paths = (try? c.decodeIfPresent([String: String].self, forKey: .paths)) ?? [:]
        groups = (try? c.decodeIfPresent([String].self, forKey: .groups)) ?? []
        defaults = try? c.decodeIfPresent(ProjectDefaults.self, forKey: .defaults)
        // Packages: names/paths, or objects with a path (or name).
        if let s = try? c.decodeIfPresent([String].self, forKey: .detectedPackages) {
            detectedPackages = s
        } else if let o = try? c.decodeIfPresent([[String: JSONValue]].self, forKey: .detectedPackages) {
            detectedPackages = o.compactMap { $0["path"]?.stringValue ?? $0["name"]?.stringValue }
        } else {
            detectedPackages = []
        }
        lastUsed = Agent.date(try? c.decodeIfPresent(String.self, forKey: .lastUsed)).flatMap { $0.timeIntervalSince1970 < 0 ? nil : $0 }
        scratch = try? c.decodeIfPresent(ScratchInfo.self, forKey: .scratch)
    }

    public func encode(to encoder: any Encoder) throws {
        var c = encoder.container(keyedBy: CodingKeys.self)
        try c.encode(id, forKey: .id)
        try c.encode(name, forKey: .name)
        try c.encodeIfPresent(color, forKey: .color)
        if colorSet { try c.encode(true, forKey: .colorSet) }
        try c.encode(kind, forKey: .kind)
        try c.encodeIfPresent(identity, forKey: .identity)
        try c.encodeIfPresent(parentId, forKey: .parentId)
        try c.encode(paths, forKey: .paths)
        try c.encode(groups, forKey: .groups)
        try c.encodeIfPresent(defaults, forKey: .defaults)
        if !detectedPackages.isEmpty { try c.encode(detectedPackages, forKey: .detectedPackages) }
        try c.encodeIfPresent(lastUsed.map(Agent.format), forKey: .lastUsed)
        try c.encodeIfPresent(scratch, forKey: .scratch)
    }

    /// The folder on `machine`, else on the local machine, else any.
    public func path(on machine: String?, local: String? = nil) -> String? {
        if let machine, let p = paths[machine] { return p }
        if let local, let p = paths[local] { return p }
        return paths.keys.sorted().first.flatMap { paths[$0] }
    }

    public var isScratch: Bool { kind == .scratch }
}

/// `groups.list` → `[Group]`: a named set of projects (one level only).
public struct ProjectGroup: Codable, Equatable, Sendable, Identifiable {
    public var id: String
    public var name: String
    public var projectIds: [String]
    public var order: Int
    public var color: String?

    public init(id: String, name: String, projectIds: [String] = [], order: Int = 0, color: String? = nil) {
        self.id = id; self.name = name; self.projectIds = projectIds; self.order = order; self.color = color
    }

    enum CodingKeys: String, CodingKey { case id, name, projectIds, order, color }
    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        id = try c.decode(String.self, forKey: .id)
        name = try c.decodeIfPresent(String.self, forKey: .name) ?? id
        projectIds = (try? c.decodeIfPresent([String].self, forKey: .projectIds)) ?? []
        order = (try? c.decodeIfPresent(Int.self, forKey: .order)) ?? Int((try? c.decodeIfPresent(Double.self, forKey: .order)) ?? 0)
        color = try c.decodeIfPresent(String.self, forKey: .color).flatMap { $0.isEmpty ? nil : $0 }
    }
}

/// Project colors: the daemon's "#rrggbb" or a palette name; without one,
/// a stable pick from the Tokyo Night palette by id.
public enum ProjectColor {
    public static let palette: [(name: String, hex: String)] = [
        ("blue", "#7aa2f7"), ("purple", "#bb9af7"), ("teal", "#73daca"), ("orange", "#ff9e64"),
        ("cyan", "#7dcfff"), ("green", "#9ece6a"), ("yellow", "#e0af68"), ("rose", "#f7768e"),
    ]

    public static func hex(_ color: String?, key: String) -> String {
        if let c = color?.trimmingCharacters(in: .whitespaces).lowercased(), !c.isEmpty {
            if c.hasPrefix("#"), c.count == 7, c.dropFirst().allSatisfy(\.isHexDigit) { return c }
            if let p = palette.first(where: { $0.name == c }) { return p.hex }
            if c == "magenta" || c == "violet" { return "#bb9af7" }
            if c == "red" || c == "pink" { return "#f7768e" }
        }
        return palette[Int(stableHash(key) % UInt64(palette.count))].hex
    }

    /// FNV-1a: the same color for the same id in every run.
    public static func stableHash(_ s: String) -> UInt64 {
        var h: UInt64 = 0xcbf29ce484222325
        for b in s.utf8 { h ^= UInt64(b); h = h &* 0x100000001b3 }
        return h
    }
}

/// The app's copy of the daemon's projects and groups (rebuilt from
/// events like the agents), and how an agent or a folder maps to one.
public struct ProjectCatalog: Equatable, Sendable {
    public private(set) var projects: [String: Project] = [:]
    public private(set) var groups: [String: ProjectGroup] = [:]
    /// The daemon answers `projects.list` (false: synthesized projects only).
    public private(set) var supported = false

    public init(projects: [Project] = [], groups: [ProjectGroup] = [], supported: Bool = true) {
        for p in projects { self.projects[p.id] = p }
        for g in groups { self.groups[g.id] = g }
        self.supported = supported
    }

    // MARK: Events

    public mutating func listed(projects: [Project]?, groups: [ProjectGroup]?) {
        guard let projects else { supported = false; self.projects = [:]; self.groups = [:]; return }
        supported = true
        self.projects = Dictionary(projects.map { ($0.id, $0) }, uniquingKeysWith: { _, b in b })
        self.groups = Dictionary((groups ?? []).map { ($0.id, $0) }, uniquingKeysWith: { _, b in b })
    }

    public mutating func changed(_ p: Project) { supported = true; projects[p.id] = p }
    public mutating func removed(project id: String) { projects.removeValue(forKey: id) }
    public mutating func changed(_ g: ProjectGroup) { supported = true; groups[g.id] = g }
    public mutating func removed(group id: String) { groups.removeValue(forKey: id) }

    // MARK: Lookup

    /// Groups by their order, then name.
    public var orderedGroups: [ProjectGroup] {
        groups.values.sorted { ($0.order, $0.name.lowercased(), $0.id) < ($1.order, $1.name.lowercased(), $1.id) }
    }

    public var hasGroups: Bool { !groups.isEmpty }

    static let pathPrefix = "path:"
    /// hesperd's virtual scratch projects ("scratch:<folder>"): no
    /// notifications, listed by projects.list while agents are there.
    public static let scratchPrefix = "scratch:"

    /// A project for every id: the daemon's, else a synthesized one for a
    /// folder ("path:<folder>", or hesperd's "scratch:<folder>").
    public func project(_ id: String?) -> Project? {
        guard let id else { return nil }
        if let p = projects[id] { return p }
        if id.hasPrefix(Self.scratchPrefix) {
            let path = String(id.dropFirst(Self.scratchPrefix.count))
            return Project(id: id, name: (path as NSString).lastPathComponent, kind: .scratch, paths: ["*": path], synthesized: true)
        }
        guard id.hasPrefix(Self.pathPrefix) else { return nil }
        let path = String(id.dropFirst(Self.pathPrefix.count))
        return Project(id: id, name: (path as NSString).lastPathComponent, kind: .folder, paths: ["*": path], synthesized: true)
    }

    /// The project an agent belongs to: its `projectId` when known (or a
    /// scratch folder), else the project whose folder holds its project
    /// path on its machine (longest match), else a synthesized one for
    /// that folder; nil without a folder.
    public func projectID(for a: Agent) -> String? {
        if let id = a.projectId, !id.isEmpty, projects[id] != nil || !supported || id.hasPrefix(Self.scratchPrefix) { return id }
        return projectID(path: a.project, machine: a.machine)
    }

    /// The agent names a project this catalog doesn't know (a scratch
    /// folder, or a projects.changed still on its way): the app asks for
    /// projects.list (debounced).
    public func needsRefresh(for a: Agent) -> Bool {
        guard supported, let id = a.projectId, !id.isEmpty else { return false }
        return projects[id] == nil
    }

    public func projectID(path: String?, machine: String?) -> String? {
        guard let path, !path.isEmpty else { return nil }
        var best: (id: String, len: Int, kindRank: Int)?
        for p in projects.values {
            let candidates = machine.flatMap { p.paths[$0] }.map { [$0] } ?? Array(p.paths.values)
            for root in candidates where path == root || path.hasPrefix(root.hasSuffix("/") ? root : root + "/") {
                let rank = p.kind == .package ? 1 : 0 // a package beats its repo at the same length
                if best == nil || root.count > best!.len || (root.count == best!.len && rank > best!.kindRank) { best = (p.id, root.count, rank) }
            }
        }
        // No project: what hesperd calls it (scratch), or our own folder id.
        return best?.id ?? (supported ? Self.scratchPrefix : Self.pathPrefix) + path
    }

    /// The project itself and, for a package, its repository.
    public func lineage(_ id: String?) -> Set<String> {
        guard let id else { return [] }
        var out: Set<String> = [id]
        var cur = projects[id]?.parentId
        while let c = cur, !out.contains(c) { out.insert(c); cur = projects[c]?.parentId }
        return out
    }

    /// The top project (a package's repository).
    public func root(_ id: String?) -> String? {
        guard var cur = id else { return nil }
        var seen: Set<String> = []
        while let p = projects[cur]?.parentId, !seen.contains(p) { seen.insert(cur); cur = p }
        return cur
    }

    /// Groups a project is in (its own list, every group listing it, and
    /// its repository's for a package), in group order.
    public func groupIDs(of projectID: String?) -> [String] {
        guard let projectID else { return [] }
        let ids = lineage(projectID)
        var set: Set<String> = []
        for id in ids { for g in projects[id]?.groups ?? [] where groups[g] != nil { set.insert(g) } }
        for g in groups.values where !ids.isDisjoint(with: g.projectIds) { set.insert(g.id) }
        return orderedGroups.map(\.id).filter(set.contains)
    }

    public func colorHex(project id: String?) -> String {
        guard let id else { return "#565f89" }
        let p = project(id)
        if p?.color == nil, let parent = p?.parentId, let pp = projects[parent] { return ProjectColor.hex(pp.color, key: parent) }
        return ProjectColor.hex(p?.color, key: id)
    }

    public func colorHex(group id: String) -> String {
        guard let g = groups[id] else { return "#565f89" }
        if let c = g.color { return ProjectColor.hex(c, key: id) }
        // A group without a color: its first project's.
        if let first = g.projectIds.first(where: { projects[$0] != nil }) { return colorHex(project: first) }
        return ProjectColor.hex(nil, key: id)
    }

    public func name(project id: String?) -> String { project(id)?.name ?? "No project" }

    /// The folder to start a new agent of this project in.
    public func path(project id: String?, machine: String?, local: String?) -> String? {
        guard let p = project(id) else { return nil }
        if p.synthesized { return p.paths["*"] }
        return p.path(on: machine, local: local)
    }
}
