import Foundation

// The command registry: every user action once, with its title, menu
// place, default chord, the surfaces the chord is live in and when it is
// available. The menu bar, the palette, the key routers (KeyRouter,
// OverlayKeys, InboxKeys, SearchKeys, ReviewKeys), Settings › Shortcuts
// and the shortcuts sheet are all generated from the table in force,
// `CommandRegistry.active`: the shipped `all` with the user's
// `ShortcutOverrides` applied (`install`). `AppModel.perform` stays the
// one executor (KeyRouter.command(for:) maps an id to its AppCommand).
// CommandsTests holds the three invariants: no chord bound twice within
// a surface, every chord has a menu path or an exemption, and each
// router resolves every chord of its surface to the registered command.

/// Where a chord is live. `.everywhere` is every surface; a command bound
/// there conflicts with the same chord in any other surface.
public enum Surface: Hashable, Sendable, CaseIterable {
    case everywhere
    /// The wall with a selected tile (WallView is first responder).
    case wall
    /// The focus view: every plain key is the agent's.
    case focus
    /// An active tile on the wall: like focus, only the app's ⌘ chords.
    case activeTile
    /// Typing in a draft tile.
    case composer
    /// The quick launch panel (a composer in its own window).
    case quickLaunch
    case palette
    /// The toolbar's popovers, the move and rename popovers, the tray.
    case popover
    /// The Needs You inbox (⌘J).
    case inbox
    /// The History sheet (⌘Y).
    case history
    /// The Review sheet (⌘R).
    case review
    case agentWindow
    /// Settings, First Run, the Design Gallery: plain windows.
    case settings

    /// Every surface but `.everywhere`.
    public static var concrete: [Surface] { allCases.filter { $0 != .everywhere } }

    /// The heading in Settings › Shortcuts and the shortcuts sheet.
    public var title: String {
        switch self {
        case .everywhere: return "Everywhere"
        case .wall: return "Wall, on a selected tile"
        case .focus: return "Focus"
        case .activeTile: return "Active tile"
        case .composer: return "New agent"
        case .quickLaunch: return "Quick launch"
        case .palette: return "Command palette"
        case .popover: return "Popovers"
        case .inbox: return "Needs You"
        case .history: return "History"
        case .review: return "Review"
        case .agentWindow: return "Agent window"
        case .settings: return "Settings"
        }
    }
}

/// The menu bar's menus, in order.
public enum MenuBar: String, CaseIterable, Sendable {
    case hesper = "Hesper", file = "File", edit = "Edit", agents = "Agents", view = "View", window = "Window", help = "Help"
}

/// A command's place in a menu: sections are separated by a line, items
/// ordered within their section.
public struct MenuPath: Hashable, Sendable {
    public let menu: MenuBar
    public let section: Int
    public let order: Int
    public init(_ menu: MenuBar, _ section: Int, _ order: Int) { self.menu = menu; self.section = section; self.order = order }
}

public enum Availability: Equatable, Sendable {
    case enabled
    /// Shown disabled; the reason is the tooltip.
    case disabled(String)
    case hidden
}

/// The kind of key window a menu acts on.
public enum KeyWindowKind: Equatable, Sendable {
    case wall, agentWindow
    /// Settings, First Run, the Design Gallery, nothing.
    case other
}

/// What the availability predicates look at: a small picture of the key
/// window's model.
public struct CommandContext: Equatable, Sendable {
    /// The selected agent's state; nil when none is selected.
    public var selectedState: AgentState?
    /// The selected agent still runs (background, kill; not resume).
    public var selectedRunning: Bool
    public var mode: AppMode
    public var keyWindow: KeyWindowKind
    /// A draft is selected or being edited (⌘W discards it).
    public var hasDraft: Bool
    /// Agents ready to review, on every Mac.
    public var reviewCount: Int
    public var wallEmpty: Bool
    /// An agent was closed recently (⇧⌘T).
    public var canReopen: Bool
    /// An undo toast is up, or a text field has the keyboard.
    public var canUndo: Bool
    /// hesperd has sessions.* (the History items show).
    public var historySupported: Bool
    public var historyOpen: Bool
    public var reviewOpen: Bool
    public var paletteOpen: Bool
    public var popoverOpen: Bool
    /// The key window is the desk's first wall (Make Home Wall is done).
    public var isHomeWall: Bool
    /// The key window is in full screen (the View item says Exit).
    public var inFullScreen: Bool

    public init(selectedState: AgentState? = nil, selectedRunning: Bool = false, mode: AppMode = .wall, keyWindow: KeyWindowKind = .wall,
                hasDraft: Bool = false, reviewCount: Int = 0, wallEmpty: Bool = true, canReopen: Bool = false, canUndo: Bool = false,
                historySupported: Bool = true, historyOpen: Bool = false, reviewOpen: Bool = false, paletteOpen: Bool = false, popoverOpen: Bool = false,
                isHomeWall: Bool = false, inFullScreen: Bool = false) {
        self.selectedState = selectedState; self.selectedRunning = selectedRunning; self.mode = mode; self.keyWindow = keyWindow
        self.hasDraft = hasDraft; self.reviewCount = reviewCount; self.wallEmpty = wallEmpty; self.canReopen = canReopen; self.canUndo = canUndo
        self.historySupported = historySupported; self.historyOpen = historyOpen; self.reviewOpen = reviewOpen
        self.paletteOpen = paletteOpen; self.popoverOpen = popoverOpen; self.isHomeWall = isHomeWall; self.inFullScreen = inFullScreen
    }

    /// No window in front: what the palette and the shortcuts list use.
    public static let none = CommandContext(keyWindow: .other)

    /// A wall or agent window is in front with an agent selected.
    var hasSelection: Bool { selectedState != nil && keyWindow != .other }

    /// What File ▸ Close (⌘W) acts on: the selected agent or draft on a
    /// wall; the window everywhere else (an agent window closes and its
    /// agent keeps running on its wall; Settings, First Run and the
    /// gallery just close).
    public var closeTarget: CloseTarget { keyWindow == .wall ? .agent : .window }
}

public enum CloseTarget: Equatable, Sendable { case agent, window }


/// Every command. Cases named after the `AppCommand` they run where one
/// exists; surface verbs carry the surface's name.
public enum CommandID: Hashable, Sendable {
    // The Hesper menu and the standard items.
    case about, settings, services, hide, hideOthers, showAll, quit
    case undo, redo, cut, copy, paste, selectAll, delete, find
    case minimize, zoom, bringAllToFront, enterFullScreen
    // Agents and windows (AppCommand names).
    case newAgent, quickLaunch, newWallWindow, openInNewWindow, openInNewTab, closeAgent
    case toggleFocus, exitFocus, nextAttention
    case answer(Decision), denyWithMessage
    case backgroundAgent, killAgent, tidyUp, reopenClosed, moveAgent, renameAgent, resumeAgent
    case palette, history, review, routines, toggleSidebar, wallScope
    case arrangement(Int), cycleArrangement, toggleDense
    case stepPrevious, stepNext, jumpBand(Int)
    case showWall, makeHomeWall, desks, deskPicker, saveDeskAs
    case help, showShortcuts, hesperctlReference, reportProblem
    // The wall's selection and the tile's ⏎.
    case select(WallMove), activateTile
    // Composer.
    case startDraft, startDraftAndNew, toggleWorktree, leaveDraft
    // The overlay family (palette, popovers, inbox, History, Review).
    case moveUp, moveDown, closeOverlay
    case overlayActivate, overlayAlternate, overlayActOn, overlayBack, overlayDeleteBackward, closeTrayItem
    // Inbox verbs.
    case inboxPrimary, inboxOpen, inboxAnswer(Int), inboxMoveElsewhere
    // History verbs.
    case historyResume, historyOpenFullSize, historyFork, historyContinueOnOtherMac, historyContinueOther, historyArchive, historyDelete
    case historyRestoreCheckpoint, historySwitchScope, historyToggleFocus, historyFirst, historyLast
    // Review verbs.
    case reviewAccept, reviewSendBack, reviewProvenance, reviewToggleFold
    case reviewNextHunk, reviewPreviousHunk, reviewNextFile, reviewPreviousFile
    case reviewSeen, reviewReject, reviewNote, reviewBulkAccept

    /// A stable string ("arrangement(2)"): palette row ids, accessibility
    /// identifiers.
    public var key: String { String(describing: self) }
}

/// A second chord for a command, live in its own surfaces (Tab steps on
/// the wall; ⌃P moves up in overlays).
public struct KeyBinding: Hashable, Sendable {
    public let chord: KeyChord
    public let surfaces: Set<Surface>
    public init(_ chord: KeyChord, in surfaces: Set<Surface>) { self.chord = chord; self.surfaces = surfaces }
}

public struct Command: Identifiable, Sendable {
    public let id: CommandID
    public let title: String
    public let menu: MenuPath?
    /// The default chord (the menu's key equivalent when it has ⌘).
    public let chord: KeyChord?
    /// Where `chord` is live.
    public let surfaces: Set<Surface>
    public let aliases: [KeyBinding]
    public let availability: @Sendable (CommandContext) -> Availability
    /// Offered as an action row in the ⌘K palette.
    public let palette: Bool

    public init(_ id: CommandID, _ title: String, menu: MenuPath? = nil, chord: KeyChord? = nil, surfaces: Set<Surface> = [],
                aliases: [KeyBinding] = [], palette: Bool = false,
                availability: @escaping @Sendable (CommandContext) -> Availability = { _ in .enabled }) {
        self.id = id; self.title = title; self.menu = menu; self.chord = chord; self.surfaces = surfaces
        self.aliases = aliases; self.availability = availability; self.palette = palette
    }

    /// The chord and its aliases.
    public var bindings: [KeyBinding] {
        (chord.map { [KeyBinding($0, in: surfaces)] } ?? []) + aliases
    }

    /// The chords live in `surface` (its own or `.everywhere` ones).
    public func chords(in surface: Surface) -> [KeyChord] {
        bindings.filter { $0.surfaces.contains(surface) || $0.surfaces.contains(.everywhere) }.map(\.chord)
    }

    /// The command with its default chord replaced (nil: unbound); the
    /// aliases stay. A menu command that never had a chord gets it
    /// app-wide, like every menu key equivalent.
    func rebound(to chord: KeyChord?) -> Command {
        Command(id, title, menu: menu, chord: chord, surfaces: surfaces.isEmpty && menu != nil && chord != nil ? [.everywhere] : surfaces,
                aliases: aliases, palette: palette, availability: availability)
    }
}

// MARK: Chords

extension KeyChord {
    /// Modifier-free shorthand for the table.
    static func cmd(_ k: Key, shift: Bool = false, option: Bool = false, control: Bool = false) -> KeyChord {
        KeyChord(k, command: true, shift: shift, option: option, control: control)
    }
    static func cmd(_ c: Character, shift: Bool = false, option: Bool = false, control: Bool = false) -> KeyChord {
        cmd(.char(c), shift: shift, option: option, control: control)
    }
    static func plain(_ k: Key, shift: Bool = false, option: Bool = false) -> KeyChord { KeyChord(k, shift: shift, option: option) }
    static func plain(_ c: Character, shift: Bool = false, option: Bool = false) -> KeyChord { plain(.char(c), shift: shift, option: option) }

    /// The table's form: letters lowercase with ⇧ as a flag (an event's
    /// `.char("W")` and `.char("w")` + shift are one chord).
    public var normalized: KeyChord {
        guard case .char(let c) = key, c.isLetter, c.isUppercase else { return self }
        var k = self
        k.key = .char(Character(c.lowercased()))
        k.shift = true
        return k
    }

    /// Whether `key` is a letter (⇧ is then only its case).
    var isLetter: Bool { if case .char(let c) = key { return c.isLetter }; return false }

    /// Modifiers in macOS order (⌃⌥⇧⌘), then the key: "⇧⌘W", "⌥⌘←",
    /// "⌘⏎", "esc", "A". The same form the UI's Kbd shows.
    public var label: String {
        var s = ""
        if control { s += "⌃" }
        if option { s += "⌥" }
        if shift { s += "⇧" }
        if command { s += "⌘" }
        return s + Self.keyName(key)
    }

    public static func keyName(_ k: Key) -> String {
        switch k {
        case .char(" "): return "Space"
        case .char(let c): return String(c).uppercased()
        case .enter: return "⏎"
        case .escape: return "esc"
        case .left: return "←"
        case .right: return "→"
        case .up: return "↑"
        case .down: return "↓"
        case .home: return "Home"
        case .end: return "End"
        case .tab: return "⇥"
        case .delete: return "⌫"
        case .other: return "?"
        }
    }

    /// The inverse of `label` (the README table, rebinding later); nil
    /// for anything that is not one chord.
    public init?(label: String) {
        var rest = Substring(label)
        var command = false, shift = false, option = false, control = false
        loop: while let c = rest.first {
            switch c {
            case "⌃": control = true
            case "⌥": option = true
            case "⇧": shift = true
            case "⌘": command = true
            default: break loop
            }
            rest.removeFirst()
        }
        let key: Key
        switch rest {
        case "": return nil
        case "⏎", "↩", "↵": key = .enter
        case "esc", "Esc", "⎋": key = .escape
        case "←": key = .left
        case "→": key = .right
        case "↑": key = .up
        case "↓": key = .down
        case "Home": key = .home
        case "End": key = .end
        case "⇥", "Tab": key = .tab
        case "⌫": key = .delete
        case "Space", "␣": key = .char(" ")
        default:
            guard rest.count == 1, let c = rest.first else { return nil }
            key = .char(c.isLetter ? Character(c.lowercased()) : c)
        }
        self.init(key, command: command, shift: shift, option: option, control: control)
    }
}

// MARK: The table

/// A command table with its chord index: the shipped one
/// (`CommandRegistry.defaults`) or one with the user's overrides applied
/// (`CommandRegistry.active`, what the routers look up at key time).
public struct CommandTable: Sendable {
    public let commands: [Command]
    /// Chord → indices into `commands` of the commands binding it.
    private let byChord: [KeyChord: [Int]]

    public init(_ commands: [Command]) {
        self.commands = commands
        var m: [KeyChord: [Int]] = [:]
        for (i, c) in commands.enumerated() { for b in c.bindings { m[b.chord, default: []].append(i) } }
        byChord = m
    }

    /// The command `chord` runs in `surface`: an exact match, else, for a
    /// shifted plain letter, the unshifted one (⇧A on an approval is
    /// Always; Review's N and ⇧N are both bound, so each keeps its
    /// meaning). ⌘ chords match exactly: ⇧⌘N is not ⌘N.
    public func resolve(_ chord: KeyChord, in surface: Surface) -> CommandID? {
        let k = chord.normalized
        if let id = exact(k, in: surface) { return id }
        if k.isLetter, k.shift, !k.command, !k.control {
            var unshifted = k
            unshifted.shift = false
            return exact(unshifted, in: surface)
        }
        return nil
    }

    private func exact(_ k: KeyChord, in surface: Surface) -> CommandID? {
        byChord[k]?.first { c in commands[c].bindings.contains { $0.chord == k && CommandRegistry.live($0, in: surface) } }.map { commands[$0].id }
    }

    public func command(_ id: CommandID) -> Command? { commands.first { $0.id == id } }
}

public enum CommandRegistry {
    /// `.everywhere` is also every concrete surface.
    static func live(_ b: KeyBinding, in surface: Surface) -> Bool {
        b.surfaces.contains(surface) || b.surfaces.contains(.everywhere)
    }

    /// The shipped table, indexed.
    public static let defaults = CommandTable(all)

    /// The table in force: the shipped one with the user's overrides
    /// (`install`). Swapped whole under `activeLock` and never mutated,
    /// so the routers read it at key time from any thread; the menu bar
    /// is rebuilt by the owner of the overrides (AppDelegate).
    nonisolated(unsafe) private static var activeTable = defaults
    private static let activeLock = NSLock()

    public static var active: CommandTable { activeLock.withLock { activeTable } }

    public static func install(_ overrides: ShortcutOverrides) {
        let t = overrides.isEmpty ? defaults : CommandTable(resolved(with: overrides))
        activeLock.withLock { activeTable = t }
    }

    /// `registry` with `overrides` applied: a command's default chord
    /// replaced or taken away (aliases stay); an override naming a
    /// command the table doesn't have does nothing.
    public static func resolved(with overrides: ShortcutOverrides, from registry: [Command] = all) -> [Command] {
        guard !overrides.isEmpty else { return registry }
        return registry.map { c in
            switch overrides[c.id] {
            case nil: return c
            case .unbound: return c.rebound(to: nil)
            case .chord(let k): return c.rebound(to: k)
            }
        }
    }

    /// `CommandTable.resolve` on the active table.
    public static func resolve(_ chord: KeyChord, in surface: Surface) -> CommandID? { active.resolve(chord, in: surface) }

    /// The command in the active table.
    public static func command(_ id: CommandID) -> Command? { active.command(id) }

    /// Chord labels for `id`: its chord first, then the aliases live in
    /// `surface` (nil: every alias).
    public static func labels(_ id: CommandID, in surface: Surface? = nil) -> [String] {
        guard let c = command(id) else { return [] }
        let chords = surface.map { c.chords(in: $0) } ?? c.bindings.map(\.chord)
        return chords.map(\.label)
    }

    /// The key cap a control shows for `id` ("⌘N"): its first chord live
    /// in `surface` (nil: its default chord). IconButton tooltips and
    /// KeyButton caps read it rather than spelling the chord.
    public static func cap(_ id: CommandID, in surface: Surface? = nil) -> String? { labels(id, in: surface).first }

    /// The commands with a chord live in `surface`, in table order; for
    /// `.everywhere` only those bound everywhere.
    public static func bound(in surface: Surface) -> [Command] {
        active.commands.filter { c in c.bindings.contains { surface == .everywhere ? $0.surfaces.contains(.everywhere) : live($0, in: surface) } }
    }

    // Surface groups.
    private static let agentViews: Set<Surface> = [.wall, .focus, .activeTile, .agentWindow]
    private static let composers: Set<Surface> = [.composer, .quickLaunch]
    private static let overlaySurfaces: Set<Surface> = [.palette, .popover]
    private static let lists: Set<Surface> = [.palette, .popover, .inbox, .history, .review]

    // Availability.
    private static let always: @Sendable (CommandContext) -> Availability = { _ in .enabled }
    private static let onWall: @Sendable (CommandContext) -> Availability = { $0.keyWindow == .other ? .disabled("Bring a wall in front") : .enabled }
    private static let selected: @Sendable (CommandContext) -> Availability = { $0.hasSelection ? .enabled : .disabled("Select an agent first") }
    private static let awaitingApproval: @Sendable (CommandContext) -> Availability = {
        $0.hasSelection && $0.selectedState == .approval ? .enabled : .disabled("The selected agent isn't waiting for an approval")
    }

    public static let all: [Command] = [hesper, file, edit, agents, view, layouts, viewRest, window, help, wall, composer, overlays, inboxAnswers, listVerbs].flatMap { $0 }

    private static let hesper: [Command] = [
        Command(.about, "About Hesper", menu: MenuPath(.hesper, 0, 0)),
        Command(.settings, "Settings…", menu: MenuPath(.hesper, 1, 0), chord: .cmd(","), surfaces: [.everywhere]),
        Command(.services, "Services", menu: MenuPath(.hesper, 2, 0)),
        Command(.hide, "Hide Hesper", menu: MenuPath(.hesper, 3, 0), chord: .cmd("h"), surfaces: [.everywhere]),
        Command(.hideOthers, "Hide Others", menu: MenuPath(.hesper, 3, 1), chord: .cmd("h", option: true), surfaces: [.everywhere]),
        Command(.showAll, "Show All", menu: MenuPath(.hesper, 3, 2)),
        Command(.quit, "Quit Hesper", menu: MenuPath(.hesper, 4, 0), chord: .cmd("q"), surfaces: [.everywhere]),
    ]

    private static let file: [Command] = [
        Command(.newAgent, "New Agent", menu: MenuPath(.file, 0, 0), chord: .cmd("n"), surfaces: [.everywhere], palette: true),
        // The global hotkey (Settings; the system binds it, no window does).
        Command(.quickLaunch, "Quick Launch…", menu: MenuPath(.file, 0, 1), chord: KeyChord(.char(" "), option: true, control: true), palette: true),
        Command(.newWallWindow, "New Wall Window", menu: MenuPath(.file, 0, 2), chord: .cmd("n", option: true), surfaces: [.everywhere]),
        Command(.openInNewWindow, "Open in New Window", menu: MenuPath(.file, 1, 0), chord: .cmd(.enter, option: true), surfaces: [.everywhere],
                availability: { $0.hasSelection && $0.keyWindow == .wall ? .enabled : .disabled("Select an agent on a wall first") }),
        Command(.openInNewTab, "Open in New Tab", menu: MenuPath(.file, 1, 1),
                availability: { $0.hasSelection && $0.keyWindow == .wall ? .enabled : .disabled("Select an agent on a wall first") }),
        // The agent on a wall (a draft: discard); the window in Settings,
        // First Run, the gallery and agent windows (the agent keeps running).
        Command(.closeAgent, "Close", menu: MenuPath(.file, 2, 0), chord: .cmd("w"),
                surfaces: agentViews.union(composers).union([.settings]),
                availability: { $0.keyWindow != .wall || $0.selectedState != nil || $0.hasDraft ? .enabled : .disabled("Select an agent first") }),
    ]

    private static let edit: [Command] = [
        // The title names the action ("Undo Close"); a text field's own undo first.
        Command(.undo, "Undo", menu: MenuPath(.edit, 0, 0), chord: .cmd("z"), surfaces: [.everywhere],
                availability: { $0.canUndo ? .enabled : .disabled("Nothing to undo") }),
        Command(.redo, "Redo", menu: MenuPath(.edit, 0, 1), chord: .cmd("z", shift: true), surfaces: [.everywhere]),
        Command(.cut, "Cut", menu: MenuPath(.edit, 1, 0), chord: .cmd("x"), surfaces: [.everywhere]),
        // History: the selected text, or the session id when none is selected.
        Command(.copy, "Copy", menu: MenuPath(.edit, 1, 1), chord: .cmd("c"), surfaces: [.everywhere]),
        Command(.paste, "Paste", menu: MenuPath(.edit, 1, 2), chord: .cmd("v"), surfaces: [.everywhere]),
        Command(.selectAll, "Select All", menu: MenuPath(.edit, 1, 3), chord: .cmd("a"), surfaces: [.everywhere]),
        Command(.delete, "Delete", menu: MenuPath(.edit, 1, 4)),
        Command(.find, "Find", menu: MenuPath(.edit, 2, 0), chord: .cmd("f"), surfaces: [.everywhere],
                availability: { $0.historyOpen ? .enabled : .disabled("Open History to search sessions") }),
    ]

    private static let agents: [Command] = [
        Command(.toggleFocus, "Open / Back to Wall", menu: MenuPath(.agents, 0, 0), chord: .cmd(.enter), surfaces: agentViews,
                availability: { $0.mode == .focus || $0.hasSelection ? .enabled : .disabled("Select an agent first") }),
        Command(.exitFocus, "Leave Focus", menu: MenuPath(.agents, 0, 1), chord: .cmd(.escape), surfaces: agentViews,
                availability: { $0.mode == .focus ? .enabled : .disabled("Not in focus") }),
        Command(.nextAttention, "Needs You…", menu: MenuPath(.agents, 1, 0), chord: .cmd("j"), surfaces: [.everywhere], palette: true),
        Command(.answer(.allow), "Allow", menu: MenuPath(.agents, 2, 0), availability: awaitingApproval),
        Command(.answer(.always), "Always Allow", menu: MenuPath(.agents, 2, 1), chord: .plain("a"), surfaces: [.wall], availability: awaitingApproval),
        Command(.denyWithMessage, "Deny…", menu: MenuPath(.agents, 2, 2), chord: .plain("n"), surfaces: [.wall], availability: awaitingApproval),
        Command(.backgroundAgent, "Send to Background", menu: MenuPath(.agents, 3, 0), chord: .cmd("w", option: true), surfaces: [.everywhere],
                availability: selected),
        Command(.killAgent, "Kill", menu: MenuPath(.agents, 3, 1), chord: .cmd("w", control: true), surfaces: [.everywhere],
                availability: { !$0.hasSelection ? .disabled("Select an agent first") : $0.selectedRunning ? .enabled : .disabled("The selected agent isn't running") }),
        Command(.tidyUp, "Tidy Up Finished Agents", menu: MenuPath(.agents, 3, 2), chord: .cmd("w", shift: true), surfaces: [.everywhere], palette: true,
                availability: { $0.keyWindow == .other ? .disabled("Bring a wall in front") : $0.wallEmpty ? .disabled("Nothing to tidy up") : .enabled }),
        Command(.reopenClosed, "Reopen Closed Agent", menu: MenuPath(.agents, 3, 3), chord: .cmd("t", shift: true), surfaces: [.everywhere], palette: true,
                availability: { $0.keyWindow == .other ? .disabled("Bring a wall in front") : $0.canReopen ? .enabled : .disabled("Nothing was closed recently") }),
        Command(.moveAgent, "Move to Another Mac…", menu: MenuPath(.agents, 4, 0), chord: .cmd("m", shift: true), surfaces: [.everywhere], availability: selected),
        Command(.renameAgent, "Rename…", menu: MenuPath(.agents, 4, 1), availability: selected),
        Command(.resumeAgent, "Resume", menu: MenuPath(.agents, 4, 2),
                availability: { $0.hasSelection && !$0.selectedRunning ? .enabled : .disabled("The selected agent is still running") }),
    ]

    private static let view: [Command] = [
        Command(.palette, "Command Palette", menu: MenuPath(.view, 0, 0), chord: .cmd("k"), surfaces: [.everywhere]),
        Command(.history, "History", menu: MenuPath(.view, 0, 1), chord: .cmd("y"), surfaces: [.everywhere], palette: true,
                availability: { $0.historySupported ? .enabled : .hidden }),
        Command(.review, "Review", menu: MenuPath(.view, 0, 2), chord: .cmd("r"), surfaces: [.everywhere], palette: true),
        // A placeholder for the routines branch (hidden until it fills it in).
        Command(.routines, "Routines", menu: MenuPath(.view, 0, 3), chord: .cmd("r", shift: true), surfaces: [.everywhere], availability: { _ in .hidden }),
        Command(.toggleSidebar, "Project Sidebar", menu: MenuPath(.view, 1, 0), chord: .cmd("0"), surfaces: [.everywhere], palette: true, availability: onWall),
        Command(.wallScope, "Wall Scope…", menu: MenuPath(.view, 1, 1), chord: .cmd("s", control: true), surfaces: [.everywhere]),
    ]

    private static let layouts: [Command] = WallArrangement.allCases.enumerated().map { i, a in
        Command(.arrangement(i), a.title, menu: MenuPath(.view, 2, i), chord: .cmd(.char(Character(String(i + 1))), option: true), surfaces: [.everywhere])
    }

    private static let viewRest: [Command] = [
        Command(.cycleArrangement, "Next Layout", menu: MenuPath(.view, 2, 10), chord: .cmd("l", option: true), surfaces: [.everywhere]),
        Command(.toggleDense, "Dense Cards", menu: MenuPath(.view, 2, 11)),
        Command(.stepPrevious, "Previous Agent", menu: MenuPath(.view, 3, 0), chord: .cmd(.left, option: true), surfaces: [.everywhere],
                aliases: [KeyBinding(.plain(.tab, shift: true), in: [.wall])], availability: onWall),
        Command(.stepNext, "Next Agent", menu: MenuPath(.view, 3, 1), chord: .cmd(.right, option: true), surfaces: [.everywhere],
                aliases: [KeyBinding(.plain(.tab), in: [.wall])], availability: onWall),
        Command(.jumpBand(-1), "Previous Band", menu: MenuPath(.view, 3, 2), chord: .cmd(.up, option: true), surfaces: [.everywhere],
                aliases: [KeyBinding(.plain(.up, option: true), in: [.wall])], availability: onWall),
        Command(.jumpBand(1), "Next Band", menu: MenuPath(.view, 3, 3), chord: .cmd(.down, option: true), surfaces: [.everywhere],
                aliases: [KeyBinding(.plain(.down, option: true), in: [.wall])], availability: onWall),
        Command(.enterFullScreen, "Enter Full Screen", menu: MenuPath(.view, 4, 0), chord: .cmd("f", control: true), surfaces: [.everywhere]),
    ]

    private static let window: [Command] = [
        Command(.minimize, "Minimize", menu: MenuPath(.window, 0, 0), chord: .cmd("m"), surfaces: [.everywhere]),
        Command(.zoom, "Zoom", menu: MenuPath(.window, 0, 1)),
        Command(.showWall, "Show Wall", menu: MenuPath(.window, 1, 0), chord: .cmd("0", shift: true), surfaces: [.everywhere]),
        Command(.makeHomeWall, "Make Home Wall", menu: MenuPath(.window, 1, 1),
                availability: { $0.keyWindow != .wall ? .disabled("Bring a wall in front") : $0.isHomeWall ? .disabled("This is the home wall") : .enabled }),
        Command(.desks, "Desks", menu: MenuPath(.window, 2, 0)),
        Command(.deskPicker, "Desk Picker…", menu: MenuPath(.window, 2, 1), chord: .cmd("d", shift: true), surfaces: [.everywhere]),
        Command(.saveDeskAs, "Save Desk As…", menu: MenuPath(.window, 2, 2)),
        Command(.bringAllToFront, "Bring All to Front", menu: MenuPath(.window, 3, 0)),
    ]

    private static let help: [Command] = [
        Command(.help, "Hesper Help", menu: MenuPath(.help, 0, 0)),
        Command(.showShortcuts, "Keyboard Shortcuts…", menu: MenuPath(.help, 0, 1), chord: .cmd("/"), surfaces: [.everywhere]),
        Command(.hesperctlReference, "hesperctl Reference", menu: MenuPath(.help, 0, 2)),
        Command(.reportProblem, "Report a Problem", menu: MenuPath(.help, 0, 3)),
    ]

    private static let wall: [Command] = [
        Command(.select(.left), "Select the tile to the left", chord: .plain(.left), surfaces: [.wall]),
        Command(.select(.right), "Select the tile to the right", chord: .plain(.right), surfaces: [.wall]),
        Command(.select(.up), "Select the tile above", chord: .plain(.up), surfaces: [.wall]),
        Command(.select(.down), "Select the tile below", chord: .plain(.down), surfaces: [.wall]),
        Command(.select(.first), "Select the first tile", chord: .plain(.home), surfaces: [.wall]),
        Command(.select(.last), "Select the last tile", chord: .plain(.end), surfaces: [.wall]),
        // ⏎: the tile's default action (edit a draft, the first answer,
        // allow, open the review, resume, or type into it).
        Command(.activateTile, "Allow, answer, resume or type into the tile", chord: .plain(.enter), surfaces: [.wall]),
    ]

    private static let composer: [Command] = [
        Command(.startDraft, "Start the agent", chord: .cmd(.enter), surfaces: composers),
        Command(.startDraftAndNew, "Start and write the next one", chord: .plain(.enter, option: true), surfaces: composers),
        Command(.toggleWorktree, "Worktree on / off", chord: .cmd("b", option: true), surfaces: composers),
        Command(.leaveDraft, "Leave the draft", chord: .plain(.escape), surfaces: composers),
    ]

    private static let overlays: [Command] = [
        // J K move in the inbox and History (Review's step hunks).
        Command(.moveUp, "Move up", chord: .plain(.up), surfaces: lists,
                aliases: [KeyBinding(KeyChord(.char("p"), control: true), in: overlaySurfaces.union([.inbox])),
                          KeyBinding(.plain("k"), in: [.inbox, .history])]),
        Command(.moveDown, "Move down", chord: .plain(.down), surfaces: lists,
                aliases: [KeyBinding(KeyChord(.char("n"), control: true), in: overlaySurfaces.union([.inbox])),
                          KeyBinding(.plain("j"), in: [.inbox, .history])]),
        Command(.closeOverlay, "Close", chord: .plain(.escape), surfaces: lists),
        Command(.overlayActivate, "Choose", chord: .plain(.enter), surfaces: overlaySurfaces),
        Command(.overlayAlternate, "Open full size, or keep the popover open", chord: .cmd(.enter), surfaces: overlaySurfaces),
        Command(.overlayActOn, "The agent's actions", chord: .plain(.tab), surfaces: overlaySurfaces),
        Command(.overlayBack, "Back", chord: .plain(.tab, shift: true), surfaces: overlaySurfaces),
        Command(.overlayDeleteBackward, "Delete a character", chord: .plain(.delete), surfaces: overlaySurfaces.union([.inbox])),
        Command(.closeTrayItem, "Close the selected background agent", chord: .cmd("w"), surfaces: [.popover]),

        Command(.inboxPrimary, "The first answer", chord: .plain(.enter), surfaces: [.inbox]),
        Command(.inboxOpen, "Open the agent", chord: .cmd(.enter), surfaces: [.inbox]),
    ]

    private static let inboxAnswers: [Command] = (1...9).map { n in
        Command(.inboxAnswer(n - 1), "Answer \(n)", chord: .plain(.char(Character(String(n)))), surfaces: [.inbox])
    }

    private static let listVerbs: [Command] = [
        Command(.inboxMoveElsewhere, "Continue on the other Mac", chord: .plain("m"), surfaces: [.inbox]),

        Command(.historyResume, "Resume", chord: .plain(.enter), surfaces: [.history]),
        Command(.historyOpenFullSize, "Resume and open full size", chord: .cmd(.enter), surfaces: [.history]),
        Command(.historyFork, "Fork", chord: .plain(.enter, option: true), surfaces: [.history], aliases: [KeyBinding(.plain("f"), in: [.history])]),
        Command(.historyContinueOnOtherMac, "Continue on the other Mac", chord: .plain("m"), surfaces: [.history]),
        Command(.historyContinueOther, "Continue in the other tool", chord: .plain("c"), surfaces: [.history]),
        Command(.historyArchive, "Archive", chord: .plain("a"), surfaces: [.history]),
        Command(.historyDelete, "Delete", chord: .plain(.delete), surfaces: [.history]),
        Command(.historyRestoreCheckpoint, "Restore the checkpoint", chord: .plain("r"), surfaces: [.history]),
        Command(.historySwitchScope, "All scope", chord: .plain(.tab), surfaces: [.history]),
        Command(.historyToggleFocus, "Search field / list", chord: .plain(.tab, shift: true), surfaces: [.history]),
        Command(.historyFirst, "First session", chord: .cmd(.up), surfaces: [.history], aliases: [KeyBinding(.plain(.home), in: [.history])]),
        Command(.historyLast, "Last session", chord: .cmd(.down), surfaces: [.history], aliases: [KeyBinding(.plain(.end), in: [.history])]),

        Command(.reviewAccept, "Accept and commit", chord: .cmd(.enter), surfaces: [.review]),
        Command(.reviewSendBack, "Send back", chord: .plain(.enter, shift: true), surfaces: [.review]),
        Command(.reviewProvenance, "Where it came from", chord: .plain(.enter, option: true), surfaces: [.review]),
        Command(.reviewToggleFold, "Fold / unfold the file", chord: .plain(.enter), surfaces: [.review]),
        Command(.reviewNextHunk, "Next hunk", chord: .plain("j"), surfaces: [.review]),
        Command(.reviewPreviousHunk, "Previous hunk", chord: .plain("k"), surfaces: [.review]),
        Command(.reviewNextFile, "Next file", chord: .plain("n"), surfaces: [.review]),
        Command(.reviewPreviousFile, "Previous file", chord: .plain("n", shift: true), surfaces: [.review]),
        Command(.reviewSeen, "Seen", chord: .plain("v"), surfaces: [.review]),
        Command(.reviewReject, "Reject the hunk", chord: .plain("x"), surfaces: [.review]),
        Command(.reviewNote, "Note", chord: .plain("c"), surfaces: [.review]),
        Command(.reviewBulkAccept, "Accept everything eligible", chord: .plain("a", shift: true), surfaces: [.review]),
    ]
}

// MARK: Menu model

/// The menu bar as a tree of titles, chords and command ids, built from
/// the registry without AppKit (MenuBuilder turns it into NSMenus; the
/// test compares it to the spec's table).
public struct MenuModel: Equatable, Sendable {
    public enum Item: Equatable, Sendable {
        case command(CommandID, title: String, chord: KeyChord?)
        case separator
    }
    public struct Menu: Equatable, Sendable {
        public let bar: MenuBar
        public let items: [Item]
        public var title: String { bar.rawValue }
    }

    public let menus: [Menu]

    public static func build(from registry: [Command] = CommandRegistry.active.commands) -> MenuModel {
        var menus: [Menu] = []
        for bar in MenuBar.allCases {
            let placed = registry.compactMap { c in c.menu.map { ($0, c) } }.filter { $0.0.menu == bar }
                .sorted { ($0.0.section, $0.0.order) < ($1.0.section, $1.0.order) }
            var items: [Item] = []
            var section: Int?
            for (path, c) in placed {
                if let s = section, s != path.section { items.append(.separator) }
                section = path.section
                items.append(.command(c.id, title: c.title, chord: c.chord))
            }
            menus.append(Menu(bar: bar, items: items))
        }
        return MenuModel(menus: menus)
    }

    /// The menu's items (none for an unknown menu).
    public subscript(_ bar: MenuBar) -> [Item] { menus.first { $0.bar == bar }?.items ?? [] }

    /// The Dock menu: the app's main ways in, with the registry's titles.
    public static let dock: [CommandID] = [.newAgent, .nextAttention, .history, .review]

    public static func dockItems(registry: [Command] = CommandRegistry.active.commands, context: CommandContext = .none) -> [Item] {
        items(dock, registry: registry, context: context)
    }

    /// The status-bar menu's fixed section, after the agent groups: the
    /// labels it always had, the key equivalent only where the chord is
    /// the app-wide one (⌘N, ⌘Y, ⌘R, ⇧⌘0, ⌘, and ⌘Q; Quick Launch shows
    /// the hotkey in its label instead, since no window binds it).
    public static let statusLabels: [(CommandID, String)] = [
        (.showWall, "Open Hesper"), (.newAgent, "New agent…"), (.history, "History…"), (.review, "Review…"),
        (.quickLaunch, "Quick Launch…"), (.settings, "Settings…"), (.quit, "Quit Hesper"),
    ]

    public static func statusItems(registry: [Command] = CommandRegistry.active.commands, hotKey: String, context: CommandContext = .none) -> [Item] {
        var out: [Item] = []
        for (id, label) in statusLabels {
            guard let c = registry.first(where: { $0.id == id }), c.availability(context) != .hidden else { continue }
            if id == .quickLaunch || id == .quit { out.append(.separator) }
            let title = id == .quickLaunch ? "\(label)  \(hotKey)" : label
            out.append(.command(id, title: title, chord: c.surfaces.contains(.everywhere) ? c.chord : nil))
        }
        return out
    }

    /// `ids` as items with their registry titles and chords; a hidden one
    /// (History while hesperd has no sessions.*) is left out.

    static func items(_ ids: [CommandID], registry: [Command], context: CommandContext) -> [Item] {
        ids.compactMap { id in
            guard let c = registry.first(where: { $0.id == id }), c.availability(context) != .hidden else { return nil }
            return .command(id, title: c.title, chord: c.chord)
        }
    }


    /// Edit ▸ Undo's title names what ⌘Z would undo ("Undo Close Agent",
    /// "Undo Move"); "Undo" with nothing on the app's stack, and while a
    /// text view has the keyboard (its own undo, up the responder chain).
    public static func undoTitle(_ kind: UndoKind?) -> String {
        kind.map { "Undo \($0.verb)" } ?? "Undo"
    }

    /// View ▸ Enter Full Screen reads Exit while the key window is in it.
    public static func fullScreenTitle(inFullScreen: Bool) -> String {
        inFullScreen ? "Exit Full Screen" : "Enter Full Screen"
    }
}


/// What the app's undo stack can hold, for the Undo title.
public enum UndoKind: Equatable, Sendable {
    case closeAgent, tidyUp, move, discardDraft, deleteSession

    public var verb: String {
        switch self {
        case .closeAgent: return "Close Agent"
        case .tidyUp: return "Tidy Up"
        case .move: return "Move"
        case .discardDraft: return "Discard Draft"
        case .deleteSession: return "Delete Session"
        }
    }
}
