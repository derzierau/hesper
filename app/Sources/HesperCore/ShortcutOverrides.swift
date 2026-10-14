import Foundation

/// The user's shortcut overrides (Settings › Shortcuts): command id → a
/// chord, or `unbound`. An override replaces the command's default chord
/// in the surfaces it is live in; aliases (⇥ on the wall, J K in lists)
/// stay. `CommandRegistry.resolved(with:)` applies them to the shipped
/// table, `ShortcutConflicts.check` says where the result binds a chord
/// twice, and `rebinding` is the one way Settings changes them: reserved,
/// fixed and conflicting chords are refused with the reason, never
/// stored. Stored in AppSettings as one JSON object keyed by
/// `CommandID.key`, the chord as its label, null for unbound; an entry
/// for a command this version doesn't have is kept but not applied.
public struct ShortcutOverrides: Equatable, Sendable {
    public enum Binding: Equatable, Sendable {
        case chord(KeyChord)
        case unbound
    }

    /// `CommandID.key` → binding.
    private var entries: [String: Binding] = [:]

    public init() {}

    public var isEmpty: Bool { entries.isEmpty }
    public var count: Int { entries.count }

    public subscript(_ id: CommandID) -> Binding? { entries[id.key] }

    /// Stores `binding` (a chord normalised) without checking it; Settings
    /// goes through `rebinding`.
    public mutating func set(_ id: CommandID, to binding: Binding) {
        if case .chord(let k) = binding { entries[id.key] = .chord(k.normalized) } else { entries[id.key] = binding }
    }

    /// Back to the default chord.
    public mutating func reset(_ id: CommandID) { entries[id.key] = nil }

    public mutating func resetAll() { entries = [:] }

    // MARK: Rules

    /// macOS's own chords: never bound to a command of ours.
    public static let reserved: [KeyChord] = [.cmd("q"), .cmd("h"), .cmd("m"), .cmd(",")]

    /// The standard items keep macOS's chords (AppKit runs them up the
    /// responder chain); Quick Launch's hotkey is Settings › General's.
    public static let fixed: Set<CommandID> = [
        .about, .settings, .services, .hide, .hideOthers, .showAll, .quit,
        .undo, .redo, .cut, .copy, .paste, .selectAll, .delete, .find,
        .minimize, .zoom, .bringAllToFront, .enterFullScreen, .quickLaunch,
    ]

    public static func rebindable(_ id: CommandID) -> Bool { !fixed.contains(id) }

    /// The surfaces where the agent has the keyboard: a chord there needs
    /// ⌘, as the shipped table's do (principle 4: nothing is taken from
    /// the agent).
    static let agentSurfaces: Set<Surface> = [.everywhere, .focus, .activeTile, .agentWindow]

    /// The surfaces with a field the user types into (a draft, quick
    /// launch, a filter or search): a key without ⌘ there would fire
    /// instead of typing, so a rebound chord needs ⌘ too.
    static let typingSurfaces: Set<Surface> = [.composer, .quickLaunch, .palette, .popover, .inbox, .history]

    /// `self` with `id` bound to `binding`, or the reason it can't be:
    /// a reserved chord, a fixed command, a key the table can't hold, a
    /// chord without ⌘ where the agent or the user types, or a chord
    /// another command has in a surface both are live in. Binding the
    /// default chord resets instead.
    public func rebinding(_ id: CommandID, to binding: Binding, registry: [Command] = CommandRegistry.all) -> RebindOutcome {
        guard let c = registry.first(where: { $0.id == id }) else { return .refused("Not a command") }
        guard Self.rebindable(id) else { return .refused("\(c.title) keeps macOS's shortcut") }
        var out = self
        switch binding {
        case .unbound:
            if c.chord == nil { out.reset(id) } else { out.set(id, to: .unbound) }
            return .applied(out)
        case .chord(let raw):
            let k = raw.normalized
            if k.key == .other { return .refused("Not a key Hesper can bind") }
            if Self.reserved.contains(k) { return .refused("macOS reserves \(k.label)") }
            if !k.command, !c.surfaces.isDisjoint(with: Self.agentSurfaces) || (c.surfaces.isEmpty && c.menu != nil) {
                return .refused("Needs ⌘ here: plain keys belong to the agent")
            }
            if !k.command, k != c.chord, !c.surfaces.isDisjoint(with: Self.typingSurfaces) {
                return .refused("Needs ⌘ here: plain keys type in its field")
            }
            if k == c.chord { out.reset(id) } else { out.set(id, to: .chord(k)) }
            let clashes = ShortcutConflicts.check(registry, out).filter { $0.chord == k && $0.commands.contains(id) }
            if !clashes.isEmpty {
                let names = ShortcutConflicts.titles(for: id, in: clashes, registry: registry)
                return .refused("Conflicts with \(names.joined(separator: ", "))")
            }
            return .applied(out)
        }
    }

    // MARK: JSON

    public func json() throws -> Data {
        var object: [String: String?] = [:]
        for (key, b) in entries {
            switch b {
            case .chord(let k): object[key] = k.label
            case .unbound: object[key] = .some(nil)
            }
        }
        return try JSONEncoder().encode(object)
    }

    /// Entries whose value is neither a chord label nor null are dropped.
    public init(json: Data) throws {
        let object = try JSONDecoder().decode([String: String?].self, from: json)
        for (key, value) in object {
            if let value {
                if let k = KeyChord(label: value) { entries[key] = .chord(k.normalized) }
            } else {
                entries[key] = .unbound
            }
        }
    }
}

public enum RebindOutcome: Equatable, Sendable {
    case applied(ShortcutOverrides)
    /// The inline reason Settings shows next to the recorder.
    case refused(String)
}

/// Where a table binds one chord to two commands within one surface.
public enum ShortcutConflicts {
    public struct Conflict: Equatable, Sendable {
        public let surface: Surface
        public let chord: KeyChord
        /// In table order.
        public let commands: [CommandID]
        public init(surface: Surface, chord: KeyChord, commands: [CommandID]) { self.surface = surface; self.chord = chord; self.commands = commands }
    }

    /// The conflicts of `registry` with `overrides` applied, per surface
    /// in `Surface.concrete` order (a chord bound everywhere clashes in
    /// every surface the other command is live in). Empty for the shipped
    /// table without overrides (CommandsTests keeps it so).
    public static func check(_ registry: [Command], _ overrides: ShortcutOverrides) -> [Conflict] {
        let table = CommandRegistry.resolved(with: overrides, from: registry)
        var out: [Conflict] = []
        for s in Surface.concrete {
            var byChord: [KeyChord: [CommandID]] = [:]
            var order: [KeyChord] = []
            for c in table {
                for b in c.bindings where CommandRegistry.live(b, in: s) {
                    if byChord[b.chord] == nil { order.append(b.chord) }
                    if byChord[b.chord]?.contains(c.id) != true { byChord[b.chord, default: []].append(c.id) }
                }
            }
            for k in order where byChord[k]!.count > 1 { out.append(Conflict(surface: s, chord: k, commands: byChord[k]!)) }
        }
        return out
    }

    /// The titles of the other commands `id` clashes with, each once.
    public static func titles(for id: CommandID, in conflicts: [Conflict], registry: [Command]) -> [String] {
        var out: [String] = []
        for c in conflicts where c.commands.contains(id) {
            for other in c.commands where other != id {
                if let t = registry.first(where: { $0.id == other })?.title, !out.contains(t) { out.append(t) }
            }
        }
        return out
    }
}
