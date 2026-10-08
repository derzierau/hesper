import Foundation

// New agents per window (docs/rebuild-contract.md "As built — new agents
// per window"): a draft belongs to the wall window it was made in and is
// shown there; a window presets what it is about (its scope); a draft whose
// window is gone shows on the main wall; an agent started for a project
// this window doesn't show says where it went. Pure rules, unit-tested.

/// One open wall window as the draft rules see it, in window order.
public struct OpenWall: Equatable, Sendable {
    public var id: String
    public var scope: WallScope
    public init(id: String, scope: WallScope) { self.id = id; self.scope = scope }
}

public enum DraftHome {
    /// The wall standing in for the main wall: the main wall while its
    /// window is open, else the first open All wall, else the first open
    /// wall; nil when no wall is open.
    public static func actingMain(openWalls: [OpenWall], main: String) -> String? {
        if openWalls.contains(where: { $0.id == main }) { return main }
        return openWalls.first { $0.scope == .all }?.id ?? openWalls.first?.id
    }

    /// The wall that shows `d`: its own (`d.wall`) while open, else the
    /// (acting) main wall — drafts without a wall (older ones, the single
    /// window), and drafts whose window was closed.
    public static func home(of d: Draft, openWalls: [String], mainWall: String?) -> String? {
        if let w = d.wall, openWalls.contains(w) { return w }
        return mainWall
    }

    /// The drafts wall `this` shows. With `mainWall` one of `openWalls`,
    /// every draft is shown by exactly one open wall.
    public static func visible(drafts: [Draft], openWalls: [String], mainWall: String?, this: String) -> [Draft] {
        drafts.filter { home(of: $0, openWalls: openWalls, mainWall: mainWall) == this }
    }

    /// A wall window closed: the drafts it owned no longer name it (they
    /// show on the main wall from now on, also after a restart); they keep
    /// their band (the project's band there, if the main wall shows it).
    public static func released(_ drafts: [Draft], closing wall: String) -> [Draft] {
        drafts.filter { $0.wall == wall }.map { d in
            var n = d
            n.wall = nil
            return n
        }
    }
}

/// What a window presets on a new draft (⌘N, the toolbar's New, the
/// palette's New agent) from its scope.
public struct DraftPreset: Equatable, Sendable {
    /// The project (band) the draft opens in; nil: the "New" area.
    public var projectID: String?
    /// An explicit machine (a machine-filtered wall).
    public var machine: String?
    /// The project is the window's: shown with a lock.
    public var locked: Bool
    /// The projects the folder chip offers (a group window); nil: all.
    public var choices: [String]?

    public init(projectID: String? = nil, machine: String? = nil, locked: Bool = false, choices: [String]? = nil) {
        self.projectID = projectID; self.machine = machine; self.locked = locked; self.choices = choices
    }

    /// A project window: that project, locked. A group window: the
    /// group's projects, the most recently used first (the default). A
    /// filter with machines: the first machine, explicit. All / Overflow:
    /// nothing (the "New" area).
    public static func make(scope: WallScope, catalog: ProjectCatalog) -> DraftPreset {
        switch scope {
        case .project(let p):
            return DraftPreset(projectID: p, locked: true)
        case .group(let g):
            let ids = groupProjects(g, catalog: catalog)
            return DraftPreset(projectID: ids.first, choices: ids)
        case .filter(let f):
            return DraftPreset(machine: f.machines.sorted().first)
        case .all, .overflow:
            return DraftPreset()
        }
    }

    /// A group's projects (packages through their repository), most
    /// recently used first, then by name.
    public static func groupProjects(_ g: String, catalog: ProjectCatalog) -> [String] {
        catalog.projects.values.filter { catalog.groupIDs(of: $0.id).contains(g) }
            .sorted { a, b in
                let la = a.lastUsed ?? .distantPast, lb = b.lastUsed ?? .distantPast
                if la != lb { return la > lb }
                return (a.name.lowercased(), a.id) < (b.name.lowercased(), b.id)
            }
            .map(\.id)
    }

    /// Whether a draft opened in `projectID` in a window with `scope` has
    /// the window's project (a band's ＋ in a project window, a branch
    /// band, a package of it).
    public static func locks(projectID: String?, scope: WallScope, catalog: ProjectCatalog) -> Bool {
        guard case .project(let p) = scope, let projectID else { return false }
        return catalog.lineage(projectID).contains(p)
    }
}

public enum DraftLock {
    /// A locked draft whose folder belongs to another project (a #token,
    /// the folder chip, a typed path) is unlocked: the lock only ever says
    /// "this window's project". No folder yet keeps the lock.
    public static func check(_ d: Draft, folderProject: String?, catalog: ProjectCatalog) -> Draft {
        guard d.projectLocked, let band = d.band, d.project != nil else { return d }
        if let fp = folderProject, catalog.lineage(fp).contains(band) { return d }
        var n = d
        n.projectLocked = false
        return n
    }
}

/// Where an agent started from a draft lands, seen from the window it was
/// started in.
public enum StartLanding {
    /// nil: this wall shows it (nothing to say). Else the wall to point
    /// to: the first wall (window order) that shows it, else the main wall.
    public static func elsewhere(agent: String, this: String, walls: [(id: String, shows: Set<String>)], mainWall: String) -> String? {
        if walls.first(where: { $0.id == this })?.shows.contains(agent) == true { return nil }
        return walls.first { $0.shows.contains(agent) }?.id ?? mainWall
    }
}
