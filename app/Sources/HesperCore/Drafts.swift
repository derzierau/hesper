import Foundation

/// A new agent not started yet (contract: `drafts.*`, "As built — drafts").
public struct Draft: Codable, Equatable, Sendable, Identifiable {
    public var id: String
    public var text: String
    public var machine: String?
    /// The user chose `machine` (a chip, the palette, an @machine token).
    /// Otherwise the machine follows the folder (`DraftSync`). Drafts
    /// saved before this field read as false (contract: machineExplicit).
    public var machineExplicit: Bool
    public var project: String?
    /// The project id it was opened in (a band's ＋, ⌘N on a focused band
    /// heading, the sidebar): its band in every window and after restarts.
    /// nil: the "New" area.
    public var band: String? = nil
    /// The wall window it was made in (windows.json's wall id: "main",
    /// "wall-…"): that window shows it. nil, or a window that isn't open:
    /// the main wall (`DraftHome`).
    public var wall: String? = nil
    /// Made in a project window: the project is that window's (`band`),
    /// shown with a lock, until another folder is chosen (`DraftLock`).
    public var projectLocked = false
    public var profile: String?
    public var worktree: Bool?
    public var branch: String?
    public var attachments: [String]
    /// The wall tile it sits right of ("^": the front; nil: the end).
    public var after: String?
    /// Left with Esc: a quiet tile until edited again.
    public var parked: Bool
    public var created: Date?
    public var updated: Date?

    public init(id: String = Draft.newID(), text: String = "", machine: String? = nil, project: String? = nil, profile: String? = nil,
                worktree: Bool? = nil, branch: String? = nil, attachments: [String] = [], after: String? = nil, parked: Bool = false,
                created: Date? = Date(), updated: Date? = nil, machineExplicit: Bool = false) {
        self.id = id; self.text = text; self.machine = machine; self.machineExplicit = machineExplicit; self.project = project; self.profile = profile
        self.worktree = worktree; self.branch = branch; self.attachments = attachments; self.after = after
        self.parked = parked; self.created = created; self.updated = updated
    }

    public static func newID() -> String {
        let alphabet = Array("abcdefghijklmnopqrstuvwxyz0123456789")
        return "d-" + String((0..<8).map { _ in alphabet.randomElement()! })
    }

    public var choices: DraftChoices {
        DraftChoices(machine: machine, project: project, profile: profile, worktree: worktree, branch: branch)
    }

    public var isEmpty: Bool { text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty && attachments.isEmpty }

    /// The draft's same content (what is worth saving).
    public func sameContent(_ o: Draft) -> Bool {
        var a = self, b = o
        a.updated = nil; b.updated = nil; a.created = nil; b.created = nil
        return a == b
    }

    enum CodingKeys: String, CodingKey { case id, text, machine, machineExplicit, project, band, wall, projectLocked, profile, worktree, branch, attachments, after, parked, created, updated }

    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        id = try c.decode(String.self, forKey: .id)
        text = try c.decodeIfPresent(String.self, forKey: .text) ?? ""
        machine = try c.decodeIfPresent(String.self, forKey: .machine).flatMap { $0.isEmpty ? nil : $0 }
        machineExplicit = try c.decodeIfPresent(Bool.self, forKey: .machineExplicit) ?? false
        project = try c.decodeIfPresent(String.self, forKey: .project).flatMap { $0.isEmpty ? nil : $0 }
        band = try c.decodeIfPresent(String.self, forKey: .band).flatMap { $0.isEmpty ? nil : $0 }
        wall = try c.decodeIfPresent(String.self, forKey: .wall).flatMap { $0.isEmpty ? nil : $0 }
        projectLocked = try c.decodeIfPresent(Bool.self, forKey: .projectLocked) ?? false
        profile = try c.decodeIfPresent(String.self, forKey: .profile).flatMap { $0.isEmpty ? nil : $0 }
        worktree = try c.decodeIfPresent(Bool.self, forKey: .worktree)
        branch = try c.decodeIfPresent(String.self, forKey: .branch).flatMap { $0.isEmpty ? nil : $0 }
        attachments = try c.decodeIfPresent([String].self, forKey: .attachments) ?? []
        after = try c.decodeIfPresent(String.self, forKey: .after).flatMap { $0.isEmpty ? nil : $0 }
        parked = try c.decodeIfPresent(Bool.self, forKey: .parked) ?? false
        created = Agent.date(try c.decodeIfPresent(String.self, forKey: .created))
        updated = Agent.date(try c.decodeIfPresent(String.self, forKey: .updated))
    }

    public func encode(to encoder: any Encoder) throws {
        var c = encoder.container(keyedBy: CodingKeys.self)
        try c.encode(id, forKey: .id)
        try c.encode(text, forKey: .text)
        try c.encodeIfPresent(machine, forKey: .machine)
        if machineExplicit { try c.encode(true, forKey: .machineExplicit) }
        try c.encodeIfPresent(project, forKey: .project)
        try c.encodeIfPresent(band, forKey: .band)
        try c.encodeIfPresent(wall, forKey: .wall)
        if projectLocked { try c.encode(true, forKey: .projectLocked) }
        try c.encodeIfPresent(profile, forKey: .profile)
        try c.encodeIfPresent(worktree, forKey: .worktree)
        try c.encodeIfPresent(branch, forKey: .branch)
        if !attachments.isEmpty { try c.encode(attachments, forKey: .attachments) }
        try c.encodeIfPresent(after, forKey: .after)
        if parked { try c.encode(true, forKey: .parked) }
        try c.encodeIfPresent(created.map(Agent.format), forKey: .created)
        try c.encodeIfPresent(updated.map(Agent.format), forKey: .updated)
    }

    public var params: JSONValue {
        let data = (try? JSONEncoder().encode(self)) ?? Data("{}".utf8)
        let v = (try? JSONDecoder().decode(JSONValue.self, from: data)) ?? .object([:])
        return .object(["draft": v])
    }
}

/// The app's drafts: what the daemon has, plus local edits not saved yet.
/// Saving is continuous but coalesced: a draft is written 300 ms after the
/// last keystroke, and at least every 2 s while typing goes on. While a
/// draft has unsaved edits, the daemon's echo of an older save never
/// overwrites it (local edits win until saved).
public struct DraftBook: Sendable {
    public private(set) var drafts: [String: Draft] = [:]
    /// Unsaved edits: first and last edit time per draft.
    public private(set) var dirty: [String: (first: Date, last: Date)] = [:]
    /// A save on its way (the version sent).
    public private(set) var inFlight: [String: Draft] = [:]
    /// Removed here; the daemon's late echoes are ignored.
    private var removed: Set<String> = []
    public var quiet: TimeInterval = 0.3
    public var maxDelay: TimeInterval = 2

    public init() {}

    public subscript(id: String) -> Draft? { drafts[id] }
    public var all: [Draft] { drafts.values.sorted { ($0.created ?? .distantPast, $0.id) < ($1.created ?? .distantPast, $1.id) } }

    /// A local change (typing, a chip, parking).
    public mutating func edit(_ d: Draft, now: Date = Date()) {
        if let old = drafts[d.id], old.sameContent(d) { return }
        drafts[d.id] = d
        removed.remove(d.id)
        let first = dirty[d.id]?.first ?? now
        dirty[d.id] = (first, now)
    }

    /// Drafts whose save is due at `now` (marked in flight).
    public mutating func due(now: Date = Date()) -> [Draft] {
        var out: [Draft] = []
        for (id, t) in dirty where now.timeIntervalSince(t.last) >= quiet || now.timeIntervalSince(t.first) >= maxDelay {
            guard inFlight[id] == nil, let d = drafts[id] else { continue }
            out.append(d)
            inFlight[id] = d
            dirty.removeValue(forKey: id)
        }
        return out.sorted { $0.id < $1.id }
    }

    /// Everything unsaved, now (app quit, start).
    public mutating func flushAll() -> [Draft] {
        let ids = dirty.keys.sorted()
        dirty = [:]
        return ids.compactMap { id in drafts[id].map { inFlight[id] = $0; return $0 } }
    }

    /// When the next save is due (nil: nothing to save).
    public func nextDue() -> Date? {
        dirty.values.map { min($0.last.addingTimeInterval(quiet), $0.first.addingTimeInterval(maxDelay)) }.min()
    }

    /// The daemon confirmed a save (its reply).
    public mutating func saved(_ d: Draft) {
        inFlight.removeValue(forKey: d.id)
        guard !removed.contains(d.id) else { return }
        if dirty[d.id] == nil, var cur = drafts[d.id] {
            cur.created = d.created ?? cur.created
            cur.updated = d.updated
            drafts[d.id] = cur
        }
    }

    public mutating func saveFailed(_ id: String, now: Date = Date()) {
        guard inFlight.removeValue(forKey: id) != nil, drafts[id] != nil, dirty[id] == nil else { return }
        dirty[id] = (now, now)
    }

    /// drafts.changed / drafts.list from the daemon.
    @discardableResult
    public mutating func applyRemote(_ d: Draft) -> Bool {
        guard !removed.contains(d.id) else { return false }
        if dirty[d.id] != nil || inFlight[d.id] != nil { return false } // local edits win until saved
        if let cur = drafts[d.id], cur == d { return false }
        drafts[d.id] = d
        return true
    }

    @discardableResult
    public mutating func applyRemoved(_ id: String) -> Bool {
        dirty.removeValue(forKey: id)
        return drafts.removeValue(forKey: id) != nil
    }

    /// After drafts.list: drop drafts the daemon no longer has (unless
    /// edited here since).
    public mutating func reconcile(_ list: [Draft]) -> Bool {
        var changed = false
        let ids = Set(list.map(\.id))
        for id in drafts.keys where !ids.contains(id) && dirty[id] == nil && inFlight[id] == nil {
            drafts.removeValue(forKey: id)
            changed = true
        }
        for d in list where applyRemote(d) { changed = true }
        return changed
    }

    /// Removed here (start or discard).
    public mutating func remove(_ id: String) -> Draft? {
        dirty.removeValue(forKey: id)
        inFlight.removeValue(forKey: id)
        removed.insert(id)
        return drafts.removeValue(forKey: id)
    }

    /// Brought back (undo of a discard).
    public mutating func restore(_ d: Draft, now: Date = Date()) {
        removed.remove(d.id)
        edit(d, now: now)
    }
}

/// Wall order with anchors: drafts (and agents started from them) sit
/// right of the tile they were created next to; everything else keeps the
/// registry's order.
public enum WallOrder {
    /// - Parameters:
    ///   - base: ids in the registry's order (unanchored).
    ///   - anchored: (id, after) in creation order; after "^" = front, nil or
    ///     an unknown id = the end.
    public static func arrange(base: [String], anchored: [(id: String, after: String?)]) -> [String] {
        var out = base
        var pending = anchored.filter { a in !base.contains(a.id) }
        var progress = true
        while !pending.isEmpty && progress {
            progress = false
            var rest: [(id: String, after: String?)] = []
            for p in pending {
                if p.after == "^" {
                    // Several at the front keep their creation order.
                    let i = out.prefix { id in anchored.contains { $0.id == id && $0.after == "^" } }.count
                    out.insert(p.id, at: i)
                    progress = true
                } else if let a = p.after, let i = out.firstIndex(of: a) {
                    out.insert(p.id, at: i + 1)
                    progress = true
                } else if let a = p.after, pending.contains(where: { $0.id == a }) {
                    rest.append(p) // its anchor is placed in a later pass
                } else {
                    out.append(p.id)
                    progress = true
                }
            }
            pending = rest
        }
        return out + pending.map(\.id)
    }
}
