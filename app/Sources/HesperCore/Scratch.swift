import Foundation

// Scratch projects (the scratch contract): a lightweight project kind with
// a lifecycle. hesperd makes one from a task (`projects.scratch`, or
// `agents.spawn {scratch: true}`), in `~/scratch/<date>-<slug>` on the Mac
// it was made on, as a local git repo. Active while agents run in it,
// resting after, archived after `scratch.archiveAfterDays` (folder under
// `~/scratch/.archive`), deleted after `scratch.deleteAfterDays` more —
// unless kept. Pure rules here (naming, states, actions, the draft's
// "New scratch" default, the band); unit tested (ScratchTests).

/// `Project.scratch.state`.
public enum ScratchState: String, Codable, Sendable, CaseIterable {
    case active, resting, archived

    public init(from decoder: any Decoder) throws {
        let raw = try decoder.singleValueContainer().decode(String.self)
        self = ScratchState(rawValue: raw) ?? .resting
    }
}

/// `Project.scratch`: `{state, keep, archivedAt?, home}`.
public struct ScratchInfo: Codable, Equatable, Sendable {
    public var state: ScratchState
    /// Never archived or deleted.
    public var keep: Bool
    public var archivedAt: Date?
    /// The Mac it was made on (short name).
    public var home: String?

    public init(state: ScratchState = .resting, keep: Bool = false, archivedAt: Date? = nil, home: String? = nil) {
        self.state = state; self.keep = keep; self.archivedAt = archivedAt; self.home = home
    }

    enum CodingKeys: String, CodingKey { case state, keep, archivedAt, home }

    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        state = (try? c.decodeIfPresent(ScratchState.self, forKey: .state)) ?? .resting
        keep = (try? c.decodeIfPresent(Bool.self, forKey: .keep)) ?? false
        archivedAt = Agent.date(try? c.decodeIfPresent(String.self, forKey: .archivedAt))
        home = (try? c.decodeIfPresent(String.self, forKey: .home)).flatMap { $0.isEmpty ? nil : $0 }
    }

    public func encode(to encoder: any Encoder) throws {
        var c = encoder.container(keyedBy: CodingKeys.self)
        try c.encode(state, forKey: .state)
        if keep { try c.encode(true, forKey: .keep) }
        try c.encodeIfPresent(archivedAt.map(Agent.format), forKey: .archivedAt)
        try c.encodeIfPresent(home, forKey: .home)
    }
}

// MARK: Naming

/// The name hesperd gives a scratch made from a task (its `Slugify`: the
/// first line's lowercase words, a–z and 0–9, joined while ≤ 40
/// characters): "Clean up the CSV export" → "clean up the csv export",
/// folder "2026-10-08-clean-up-the-csv-export". The composer previews it.
public enum ScratchName {
    public static let limit = 40

    /// "clean-up-the-csv-export"; "task" for a task without words.
    public static func slug(_ text: String, limit: Int = limit) -> String {
        let first = text.trimmingCharacters(in: .whitespacesAndNewlines).split(separator: "\n", maxSplits: 1, omittingEmptySubsequences: false).first.map(String.init) ?? ""
        var words: [String] = []
        var cur = ""
        for ch in first.lowercased().unicodeScalars {
            if (ch >= "a" && ch <= "z") || (ch >= "0" && ch <= "9") { cur.unicodeScalars.append(ch) } else if !cur.isEmpty { words.append(cur); cur = "" }
        }
        if !cur.isEmpty { words.append(cur) }
        var slug = ""
        for w in words {
            let candidate = slug.isEmpty ? w : slug + "-" + w
            if candidate.count > limit {
                if slug.isEmpty { slug = String(w.prefix(limit)) }
                break
            }
            slug = candidate
        }
        return slug.isEmpty ? "task" : slug
    }

    /// The shown name: "clean up the csv export" (nil: no task yet).
    public static func preview(task: String) -> String? {
        guard task.contains(where: { $0.isLetter || $0.isNumber }) else { return nil }
        return slug(task).replacingOccurrences(of: "-", with: " ")
    }

    /// The folder's name on `date` (local calendar): "2026-10-08-clean-up".
    public static func folder(task: String, date: Date, calendar: Calendar = .current) -> String {
        let c = calendar.dateComponents([.year, .month, .day], from: date)
        return String(format: "%04d-%02d-%02d-", c.year ?? 0, c.month ?? 0, c.day ?? 0) + slug(task)
    }

    /// A project's scratch name as the app shows it: its name without the
    /// folder's date ("2026-10-08-csv-cleanup" → "csv-cleanup").
    public static func display(_ p: Project) -> String { ProjectNav.label(p.name) }
}

// MARK: States

public enum ScratchLifecycle {
    /// A scratch with a lifecycle: hesperd's scratch kind (not one the app
    /// made up for a folder).
    public static func isLifecycle(_ p: Project?) -> Bool {
        guard let p else { return false }
        return p.kind == .scratch && !p.synthesized
    }

    /// Its state now: live agents make it active whatever hesperd last
    /// said; else hesperd's state, else resting. nil: not a scratch.
    public static func state(_ p: Project?, live: Bool) -> ScratchState? {
        guard let p, p.kind == .scratch else { return nil }
        if p.scratch?.state == .archived { return .archived }
        if live { return .active }
        return p.scratch?.state == .active ? .resting : (p.scratch?.state ?? .resting)
    }

    public static func isArchived(_ p: Project?) -> Bool { p?.kind == .scratch && p?.scratch?.state == .archived }
    public static func isKept(_ p: Project?) -> Bool { p?.scratch?.keep == true }
}

// MARK: Actions

/// What a scratch offers (the sidebar's context menu, ⌘K, a tile's menu).
public enum ScratchAction: String, Sendable, CaseIterable {
    case rename, keep, unkeep, promote, archive, restore, delete

    public var title: String {
        switch self {
        case .rename: return "Rename…"
        case .keep: return "Keep"
        case .unkeep: return "Don't Keep"
        case .promote: return "Promote to Project…"
        case .archive: return "Archive"
        case .restore: return "Restore"
        case .delete: return "Delete…"
        }
    }
}

public struct ScratchActionItem: Equatable, Sendable {
    public var action: ScratchAction
    public var enabled: Bool
    /// Why it is off ("Stop its agents first").
    public var reason: String?
    public init(_ action: ScratchAction, enabled: Bool = true, reason: String? = nil) {
        self.action = action; self.enabled = enabled; self.reason = reason
    }
}

public enum ScratchActions {
    public static let busyReason = "Stop its agents first"

    /// The actions of `p` with `live` agents in it. Empty: not a scratch
    /// with a lifecycle, or hesperd has no scratch methods (`supported`).
    /// Archived: restore or delete. Otherwise rename, keep / don't keep,
    /// promote, archive, delete — the last three only without live agents
    /// (hesperd refuses moving or removing a folder agents run in).
    public static func available(_ p: Project?, live: Int, supported: Bool) -> [ScratchActionItem] {
        guard supported, ScratchLifecycle.isLifecycle(p), let p else { return [] }
        if ScratchLifecycle.isArchived(p) { return [ScratchActionItem(.restore), ScratchActionItem(.delete)] }
        let idle = live == 0
        let busy = idle ? nil : busyReason
        return [
            ScratchActionItem(.rename),
            ScratchActionItem(ScratchLifecycle.isKept(p) ? .unkeep : .keep),
            ScratchActionItem(.promote, enabled: idle, reason: busy),
            ScratchActionItem(.archive, enabled: idle, reason: busy),
            ScratchActionItem(.delete, enabled: idle, reason: busy),
        ]
    }

    /// The delete confirmation's text.
    public static func deleteMessage(_ p: Project) -> String {
        let folder = p.paths.values.sorted().first.map { " (\(($0 as NSString).abbreviatingWithTildeInPath))" } ?? ""
        return "Delete the scratch “\(ScratchName.display(p))”? Its folder\(folder) is removed; its sessions stay in History."
    }
}

// MARK: The draft's "New scratch"

/// A draft with no folder chosen starts a new scratch (the project chip
/// says "New scratch") where hesperd makes them; typing a #folder or
/// choosing a project replaces it. A project window's draft keeps its
/// locked project; a Mac whose hesperd has no scratches gets today's
/// "Choose folder".
public enum ScratchDraft {
    public static let chipTitle = "New scratch"

    public static func starts(project: String?, cloneURL: String?, locked: Bool, supported: Bool) -> Bool {
        supported && !locked && project == nil && cloneURL == nil
    }

    /// The chip's detail: the name the task gives it ("csv cleanup").
    public static func detail(task: String) -> String? { ScratchName.preview(task: task) }
}

// MARK: Support per Mac

/// What a Mac's hesperd can do with scratches, learned per machine (an
/// updated laptop and an older mini differ): nil unknown (this Mac's
/// answer stands in), true / false once known.
public struct ScratchSupport: Equatable, Sendable {
    public var byMachine: [String: Bool] = [:]
    public init() {}

    public func supported(_ machine: String, local: String) -> Bool {
        byMachine[machine] ?? (machine == local ? false : byMachine[local] ?? false)
    }

    /// An error meaning "no scratches here": an unknown method (-32601,
    /// "Unknown method" through the relay), or an older hesperd's spawn
    /// refusing an empty project.
    public static func missing(_ e: RPCError) -> Bool {
        CloseRPC.missing(e) || e.message.localizedCaseInsensitiveContains("project must be an absolute path")
    }
}

// MARK: Settings

/// hesperd's scratch settings (`scratch.archiveAfterDays`,
/// `scratch.deleteAfterDays`), read with `settings.get` and written with
/// `settings.set`; nil from a hesperd without them (Settings hides the
/// controls).
public struct ScratchSettings: Equatable, Sendable {
    public static let archiveKey = "scratch.archiveAfterDays"
    public static let deleteKey = "scratch.deleteAfterDays"
    public static let keys = [archiveKey, deleteKey]
    public static let range = 1...365

    public var archiveAfterDays: Int
    public var deleteAfterDays: Int

    public init(archiveAfterDays: Int = 14, deleteAfterDays: Int = 30) {
        self.archiveAfterDays = archiveAfterDays; self.deleteAfterDays = deleteAfterDays
    }

    /// From `settings.get`: flat keys ({"scratch.archiveAfterDays": 14}),
    /// nested ({"scratch": {"archiveAfterDays": 14}}), or under "settings"
    /// / "values". nil: neither key.
    public init?(json v: JSONValue?) {
        guard let v else { return nil }
        for inner in [v["settings"], v["values"]] where inner != nil {
            if let s = ScratchSettings(json: inner) { self = s; return }
        }
        func int(_ x: JSONValue?) -> Int? { x?.doubleValue.map { Int($0) } ?? x?.stringValue.flatMap(Int.init) }
        let a = int(v[Self.archiveKey]) ?? int(v["scratch"]?["archiveAfterDays"])
        let d = int(v[Self.deleteKey]) ?? int(v["scratch"]?["deleteAfterDays"])
        guard a != nil || d != nil else { return nil }
        self.init(archiveAfterDays: a ?? 14, deleteAfterDays: d ?? 30)
    }

    /// `settings.set {values: {key: n}}` for one changed value.
    public static func setParams(_ key: String, days: Int) -> JSONValue {
        .object(["values": .object([key: .number(Double(clamp(days)))])])
    }

    public static func clamp(_ d: Int) -> Int { min(range.upperBound, max(range.lowerBound, d)) }
}

// MARK: Collapse

/// A wall's collapsed bands: the user's (`collapsed`) plus the bands that
/// start collapsed (`ViewResolver.collapsedByDefault`, Scratch) unless
/// this wall opened them (`expanded`).
public enum BandCollapse {
    public static func isCollapsed(_ key: String, collapsed: Set<String>, expanded: Set<String>) -> Bool {
        collapsed.contains(key) || (ViewResolver.collapsedByDefault(key) && !expanded.contains(key))
    }

    /// A click on the header: (collapsed, expanded) after it.
    public static func toggle(_ key: String, collapsed: Set<String>, expanded: Set<String>) -> (collapsed: Set<String>, expanded: Set<String>) {
        var c = collapsed, e = expanded
        if isCollapsed(key, collapsed: c, expanded: e) {
            c.remove(key)
            if ViewResolver.collapsedByDefault(key) { e.insert(key) }
        } else {
            c.insert(key)
            e.remove(key)
        }
        return (c, e)
    }

    /// Opened (a draft or a revealed card in it).
    public static func open(_ key: String, collapsed: Set<String>, expanded: Set<String>) -> (collapsed: Set<String>, expanded: Set<String>) {
        var c = collapsed, e = expanded
        c.remove(key)
        if ViewResolver.collapsedByDefault(key) { e.insert(key) }
        return (c, e)
    }
}

/// The scratch RPC names (the contract).
public enum ScratchRPC {
    public static let create = "projects.scratch"
    public static let keep = "projects.scratchKeep"
    public static let archive = "projects.scratchArchive"
    public static let restore = "projects.scratchRestore"
    public static let promote = "projects.promote"
    /// Not in the contract's RPC list (hesperctl's `scratch rm`): the app
    /// assumes this name and says so when hesperd doesn't know it.
    public static let delete = "projects.scratchDelete"
    public static let settingsGet = "settings.get"
    public static let settingsSet = "settings.set"
}
