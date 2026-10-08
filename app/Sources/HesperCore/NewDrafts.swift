import Foundation

// New drafts and their folder (docs/rebuild-contract.md "As built —
// drafts" › "New drafts have no project"). ⌘N, the toolbar's New Agent,
// the menu bar item and quick launch open a draft WITHOUT a project, in the
// wall's "New" area; only explicit project actions (a band's ＋, the
// sidebar's "New Agent in <project>", ⌘N on a focused band header, a
// project in the palette) preset one. Pure rules, unit-tested.

public enum DraftSeed {
    /// Where a draft in project `id` runs and in which folder: an explicit
    /// machine when the project has a folder there, else the project's
    /// default machine, else this Mac, else the first machine with a
    /// folder — always a machine and the folder ON that machine (never
    /// this Mac with another Mac's path). `machine` nil: this Mac.
    public static func place(project id: String?, catalog: ProjectCatalog, local: String, prefer: String? = nil) -> (machine: String?, path: String?) {
        guard let p = catalog.project(id) else { return (prefer == local ? nil : prefer, nil) }
        if p.synthesized { return (prefer == local ? nil : prefer, p.paths["*"]) }
        var order: [String] = []
        for m in [prefer, p.defaults?.machine, local] + p.paths.keys.sorted().map(Optional.some) {
            if let m, !order.contains(m) { order.append(m) }
        }
        for m in order {
            if let path = p.paths[m] { return (m == local ? nil : m, path) }
        }
        if let path = p.paths["*"] { return (prefer == local ? nil : prefer, path) }
        return (prefer == local ? nil : prefer, nil)
    }

    /// The draft's folder changed (a #token, the chip, the folder picker,
    /// a clone): the project follows silently. The folder lists offer this
    /// Mac's folders, so a machine the user didn't choose (a default)
    /// goes back to this Mac — the start then never sends this Mac's path
    /// to another machine ("no directory …").
    public static func folderChanged(_ d: Draft, to path: String?, machinePinned: Bool? = nil) -> Draft {
        var n = d
        n.project = path
        if !(machinePinned ?? d.machineExplicit) { n.machine = nil }
        return n
    }

    /// A folder on this Mac that isn't there (the chip row's "Folder
    /// doesn't exist — Create"); nil: fine, or not checkable (another
    /// machine, no folder).
    public static func missingFolder(project: String?, machine: String, local: String, exists: (String) -> Bool) -> String? {
        guard let project, !project.isEmpty, machine == local, project.hasPrefix("/") else { return nil }
        return exists(project) ? nil : project
    }

    /// Typed folder paths in the project chip ("/Users/x/p", "~/p"): the
    /// absolute path, else nil.
    public static func typedFolder(_ q: String, home: String = NSHomeDirectory()) -> String? {
        let t = q.trimmingCharacters(in: .whitespaces)
        if t.hasPrefix("~/") { return (home as NSString).appendingPathComponent(String(t.dropFirst(2))) }
        if t == "~" { return home }
        guard t.hasPrefix("/"), t.count > 1 else { return nil }
        return (t as NSString).standardizingPath
    }
}

// Machine/folder consistency (contract "As built — drafts" › "Machine and
// folder agree"): a #token always decides the draft's project, a machine
// the user didn't choose follows the folder, and a folder that isn't on
// the target machine is said in the chip row before anything starts.
public enum DraftSync {
    /// The text's tokens into the draft's stored choices. The last
    /// resolved #token is the project (a token naming the chosen folder
    /// keeps that folder); an edit that removed the last one clears it.
    /// An @machine token is an explicit machine; removing it lets the
    /// machine follow the folder again. `previousText` nil: a restore
    /// (nothing was removed).
    public static func sync(_ d: Draft, previousText: String?, context: ComposerContext) -> Draft {
        var n = d
        let now = ComposerParser.resolve(d.text, context: context)
        let before = previousText.map { ComposerParser.resolve($0, context: context) } ?? []
        if let t = now.last(where: { $0.token.kind == .project }) {
            let keep = n.project.map { $0 == t.value || ComposerResolution.token(t.token.query, names: $0, context: context) } ?? false
            if !keep { n.project = t.value }
        } else if before.contains(where: { $0.token.kind == .project }) {
            n.project = nil
        }
        if let t = now.last(where: { $0.token.kind == .machine }) {
            n.machine = t.value == context.localMachine ? nil : t.value
            n.machineExplicit = true
        } else if before.contains(where: { $0.token.kind == .machine }) {
            n.machine = nil
            n.machineExplicit = false
        }
        return n
    }

    /// A machine the user didn't choose follows the folder: kept where
    /// the catalog has the folder on it, else this Mac when the folder is
    /// here (or there is no folder); a folder only another machine has
    /// (the catalog's path there) takes the draft there.
    public static func followFolder(_ d: Draft, catalog: ProjectCatalog, local: String, existsLocally: (String) -> Bool) -> Draft {
        guard !d.machineExplicit else { return d }
        var n = d
        let m = d.machine.flatMap { $0 == local ? nil : $0 }
        guard let p = d.project, p.hasPrefix("/") else {
            n.machine = nil
            return n
        }
        let holders = machines(holding: p, catalog: catalog)
        if let m {
            if holders.contains(m) { return d }
            if existsLocally(p) || holders.contains(local) { n.machine = nil }
            return n
        }
        if !existsLocally(p), !holders.contains(local), holders.count == 1, let only = holders.first { n.machine = only }
        return n
    }

    /// Both: tokens, then the machine.
    public static func normalize(_ d: Draft, previousText: String?, context: ComposerContext, catalog: ProjectCatalog,
                                 existsLocally: (String) -> Bool) -> Draft {
        followFolder(sync(d, previousText: previousText, context: context), catalog: catalog, local: context.localMachine, existsLocally: existsLocally)
    }

    /// The machines the catalog has `path` on (exactly that folder).
    public static func machines(holding path: String, catalog: ProjectCatalog) -> Set<String> {
        var out: Set<String> = []
        for p in catalog.projects.values {
            for (m, q) in p.paths where q == path && m != "*" { out.insert(m) }
        }
        return out
    }

    /// The same project's folder on `machine` (another path), for "Use
    /// mini's copy".
    public static func copy(of path: String, on machine: String, catalog: ProjectCatalog) -> String? {
        for p in catalog.projects.values.sorted(by: { $0.id < $1.id }) where p.paths.values.contains(path) {
            if let q = p.paths[machine], q != path { return q }
        }
        return nil
    }

    /// hesperd's spawn error for a folder that isn't there ("no directory
    /// <path>"): the path, else nil. Never shown as such: the chip row's
    /// note says it.
    public static func missingDirectory(in message: String) -> String? {
        let prefix = "no directory "
        guard let r = message.range(of: prefix) else { return nil }
        let p = String(message[r.upperBound...]).trimmingCharacters(in: .whitespaces)
        return p.isEmpty ? nil : p
    }
}

/// The chip row's note when the draft's folder isn't on the machine it
/// will run on.
public struct FolderNote: Equatable, Sendable {
    public enum Kind: Equatable, Sendable {
        /// The folder is on this Mac, not on the target: "This folder is on
        /// laptop, not on mini — Run on laptop".
        case elsewhere
        /// Neither here nor there: "Folder isn't on mini".
        case missing
    }
    public var kind: Kind
    public var folder: String
    /// Display names.
    public var target: String
    public var here: String
    /// The target machine's copy of the same project (another path).
    public var copy: String?

    public init(kind: Kind, folder: String, target: String, here: String, copy: String? = nil) {
        self.kind = kind; self.folder = folder; self.target = target; self.here = here; self.copy = copy
    }

    public var text: String {
        switch kind {
        case .elsewhere: return "This folder is on \(here), not on \(target) —"
        case .missing: return "Folder isn't on \(target)" + (copy == nil ? "" : " —")
        }
    }

    /// nil: no note (this Mac — its own "Create" note —, no folder, not
    /// checked yet, or there).
    public static func make(project: String?, machine: String, local: String, onTarget: Bool?, existsLocally: Bool,
                            catalog: ProjectCatalog, targetName: String, hereName: String) -> FolderNote? {
        guard let project, project.hasPrefix("/"), machine != local, onTarget == false else { return nil }
        return FolderNote(kind: existsLocally ? .elsewhere : .missing, folder: project, target: targetName, here: hereName,
                          copy: DraftSync.copy(of: project, on: machine, catalog: catalog))
    }
}
