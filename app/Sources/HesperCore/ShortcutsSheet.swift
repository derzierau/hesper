import Foundation

/// Help ▸ Keyboard Shortcuts (⌘/) and Settings › Shortcuts: the registry
/// as one section per surface, each a list of rows (title, key caps,
/// command id). A command appears once: in the section of the surface
/// the user is in when its chord is live there, else in the first
/// surface of `order` that binds it (app-wide chords always under
/// Everywhere). The current surface's
/// section comes first and is the one expanded; a query filters the rows
/// by title and key text and expands every section that still has rows.
/// Hidden commands (Routines) are left out.
public enum ShortcutsSheet {
    public struct Row: Equatable, Sendable {
        public let id: CommandID
        public let title: String
        /// Every chord of the command, as Kbd shows them ("⌥⌘→ ⇥").
        public let chord: String
        /// The titles of the other commands bound to one of this
        /// command's chords in a surface they share: the "Conflicts
        /// with …" pill. Empty for the registry as shipped (CommandsTests
        /// keeps it so); an override stored before a later version bound
        /// its chord fills it (`ShortcutConflicts`).
        public var conflicts: [String]
        /// The default chord's label (what Settings' recorder shows);
        /// nil: none, the command is unbound (or has aliases only).
        public var primary: String?
        /// The aliases' labels (the Row's own key caps next to the recorder).
        public var aliases: [String]
        /// An override is stored for it (Settings shows Reset).
        public var overridden: Bool
        /// Settings offers a recorder (`ShortcutOverrides.rebindable`).
        public var rebindable: Bool

        public init(id: CommandID, title: String, chord: String, conflicts: [String] = [], primary: String? = nil, aliases: [String] = [],
                    overridden: Bool = false, rebindable: Bool = false) {
            self.id = id; self.title = title; self.chord = chord; self.conflicts = conflicts
            self.primary = primary; self.aliases = aliases; self.overridden = overridden; self.rebindable = rebindable
        }
    }

    public struct Section: Equatable, Sendable {
        public let surface: Surface
        public var title: String { surface.title }
        public let rows: [Row]
        /// Open in the sheet (the current surface; every section while a
        /// query is typed).
        public let expanded: Bool
    }

    /// The display order of the surfaces.
    public static let order: [Surface] = [.everywhere, .wall, .focus, .activeTile, .composer, .quickLaunch, .palette, .popover, .inbox,
                                          .history, .review, .agentWindow, .settings]

    /// The accessibility identifier of a row ("shortcuts.row.closeAgent").
    public static func rowIdentifier(_ id: CommandID) -> String { "shortcuts.row.\(id.key)" }

    /// - Parameters:
    ///   - current: the surface the user is in (its section first).
    ///   - query: text typed into the search field; empty shows everything.
    ///   - hotKey: the configured quick launch chord's label (nil: quick
    ///     launch is off, the row is left out).
    ///   - hotKeyConflict: what macOS binds the hotkey to, when it does
    ///     (the Quick Launch row's pill).
    ///   - overrides: Settings' stored overrides (the ⌘/ sheet passes
    ///     none): a command they unbind keeps its row, with no chord,
    ///     in the surface its default chord lives in (`defaults`), so
    ///     it can be reset.
    ///   - scanConflicts: look for chords bound twice (`Row.conflicts`);
    ///     callers pass false while no override is stored, since the
    ///     shipped table has none and the scan is quadratic.
    public static func sections(registry: [Command] = CommandRegistry.active.commands, current: Surface, query: String = "",
                                hotKey: String? = nil, hotKeyConflict: String? = nil, context: CommandContext = .none,
                                overrides: ShortcutOverrides = ShortcutOverrides(), defaults: [Command] = CommandRegistry.all,
                                scanConflicts: Bool = true) -> [Section] {
        let order = [current] + Self.order.filter { $0 != current }
        var rows: [Surface: [Row]] = [:]
        for c in registry where c.availability(context) != .hidden {
            var row: Row
            let home: Surface
            if c.id == .quickLaunch {
                guard let hotKey else { continue }
                row = Row(id: c.id, title: c.title, chord: hotKey, conflicts: hotKeyConflict.map { [$0] } ?? [])
                home = .everywhere
            } else {
                let overridden = overrides[c.id] != nil
                // An unbound command is placed by its default.
                let placing = c.bindings.isEmpty && overridden ? defaults.first { $0.id == c.id }?.bindings ?? [] : c.bindings
                guard !placing.isEmpty else { continue }
                if placing.contains(where: { $0.surfaces.contains(.everywhere) }) {
                    home = .everywhere
                } else if let h = order.first(where: { s in placing.contains { $0.surfaces.contains(s) } }) {
                    home = h
                } else { continue }
                row = Row(id: c.id, title: c.title, chord: c.bindings.map(\.chord.label).joined(separator: " "),
                          conflicts: scanConflicts ? conflicts(of: c, in: registry) : [], primary: c.chord?.label, aliases: c.aliases.map(\.chord.label),
                          overridden: overridden, rebindable: ShortcutOverrides.rebindable(c.id))
            }
            rows[home, default: []].append(row)
        }
        let q = query.trimmingCharacters(in: .whitespaces).lowercased()
        return order.compactMap { s in
            guard var list = rows[s] else { return nil }
            if !q.isEmpty { list = list.filter { $0.title.lowercased().contains(q) || $0.chord.lowercased().contains(q) } }
            guard !list.isEmpty else { return nil }
            return Section(surface: s, rows: list, expanded: s == current || !q.isEmpty)
        }
    }

    /// The other commands sharing one of `c`'s chords in a surface both
    /// are live in, in table order, each once.
    static func conflicts(of c: Command, in registry: [Command]) -> [String] {
        var out: [String] = []
        for other in registry where other.id != c.id {
            let clash = c.bindings.contains { mine in
                other.bindings.contains { theirs in
                    theirs.chord == mine.chord && Surface.concrete.contains { CommandRegistry.live(mine, in: $0) && CommandRegistry.live(theirs, in: $0) }
                }
            }
            if clash, !out.contains(other.title) { out.append(other.title) }
        }
        return out
    }
}
