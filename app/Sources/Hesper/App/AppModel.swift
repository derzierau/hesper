import AppKit
import HesperCore
import Observation

/// One card on the wall: an agent, or a draft (a new agent being written).
enum WallItem: Equatable {
    case agent(Agent)
    case draft(Draft)
    /// Shared history: a removed agent's ghost card (Grid + Shelf).
    case ghost(Session)

    var id: String {
        switch self {
        case .agent(let a): return a.id
        case .draft(let d): return d.id
        case .ghost(let s): return GhostCards.itemID(s)
        }
    }
    var agent: Agent? { if case .agent(let a) = self { return a } else { return nil } }
    var draft: Draft? { if case .draft(let d) = self { return d } else { return nil } }
}

/// The app's state: a view of the daemon plus what the user is looking at.
@MainActor
@Observable
final class AppModel {
    let env: AppEnvironment
    let client: DaemonClient
    let settings: AppSettings
    private(set) var registry = AgentRegistry()
    private(set) var connectionMessage: String? = "Connecting to hesperd…"

    var mode: AppMode = .wall
    /// The selected card (highlight ring; the wall's keys act on it).
    var selectedID: String? { didSet { if oldValue != selectedID, selectedID != nil { focusedBandKey = nil } } }
    /// A band header has the focus (clicked; no card selected since): ⌘N
    /// opens the draft in that band, with its project.
    var focusedBandKey: String? { didSet { if oldValue != focusedBandKey { onAgentsChanged?() } } }
    /// The wall tile the keyboard types into (one per window; nil: none).
    /// Two levels: a click / arrows select; a second click, ⏎ or typing a
    /// character makes the selected tile active; ⌘esc back to selected.
    var activeTileID: String?
    /// Main + stack's main card when nothing needs you: follows clicks,
    /// typing and focus, never the arrow keys (moving the selection must
    /// not resize cards, so no PTY changes size).
    @ObservationIgnored var mainPick: String?
    var focusedID: String?
    var toast: Toast?
    var showPalette = false
    var recentProjects: [RecentProject] = []
    /// Another window's model reads the folder, recent-project and profile
    /// lists from the main model, which keeps them current (a copy taken
    /// when the window opened went stale, often empty: an empty folder
    /// list in that window's drafts).
    @ObservationIgnored weak var listSource: AppModel?
    /// The model whose lists this window uses.
    var lists: AppModel { listSource ?? self }
    var profiles: ProfilesInfo?
    /// Folders under the projects root (composer completion).
    var projectFolders: [String] = []

    // MARK: Drafts (AppModel+Drafts)

    var drafts = DraftBook()
    /// The daemon keeps drafts (drafts.*); false: an older hesperd, drafts
    /// live only in this app session.
    var draftsSupported = true
    /// The draft whose editor has the keyboard.
    var editingDraftID: String?
    /// Drafts being started: the spawn they sent.
    var startingDrafts: [String: PendingStart] = [:]
    /// Agents started from a draft keep the draft's place: agent → the tile
    /// it sits right of ("^" front). Remembered (UserDefaults).
    var placements: [String: String] = [:] {
        didSet { if persistSettings { UserDefaults.standard.set(placements, forKey: Self.placementsKey) } }
    }
    /// agent id → the draft it was (the wall turns that card in place).
    @ObservationIgnored var adoptions: [String: String] = [:]
    /// The quick launch panel's composer state (survives closing it).
    var quickDraft = Draft(id: "d-quicklaunch", created: nil)
    @ObservationIgnored var draftSaveTask: Task<Void, Never>?
    @ObservationIgnored var composers: [String: ComposerModel] = [:]

    struct PendingStart: Equatable {
        var task: String
        var project: String
        var machine: String
        var after: String?
    }

    // MARK: Overlays (AppModel+Overlays)

    var popover: PopoverKind?
    /// Where the popover points (root view coordinates), set when it opens.
    var popoverAnchor: CGRect = .zero
    /// Per popover kind: query and selection (state survives close).
    var popoverLists: [String: OverlayList] = [:]
    var paletteList = OverlayList()
    /// ⌘J inbox: agents answered from it (id → when), shown as answered
    /// until they leave the queue; a second key on them does nothing.
    var queueAnswered: [String: QueueAnswer] = [:]
    /// ⇥ on an agent in the palette: its actions only.
    var paletteScope: String?
    var undo = UndoStack<UndoAction>()
    /// The undo toast shown now (the newest undoable entry).
    var undoToast: UndoStack<UndoAction>.Entry?
    @ObservationIgnored var undoTask: Task<Void, Never>?
    @ObservationIgnored var anchorProvider: ((PopoverKind) -> CGRect?)?
    /// Moving work across Macs (AppModel+Move): shared by every window's model.
    let moveBook: MoveBook
    /// Agents moving to another machine (id → target), shown in place.
    var moving: [String: String] {
        get { moveBook.targets }
        set { moveBook.targets = newValue }
    }
    /// A moved agent continues as this new one: its selection / focus
    /// follows once it arrives (this window's; new id → was focused).
    @ObservationIgnored var moveFollow: [String: Bool] = [:]
    /// Closing agents (AppModel+Closing): shared by every window's model.
    let closeBook: CloseBook
    /// Scratch projects (AppModel+Scratch): shared by every window's model.
    let scratchBook: ScratchBook
    /// Bringing a draft's folder to another Mac (AppModel+Bring): shared.
    let bringBook: BringBook
    /// Closed, not gone from hesperd's agents yet (or queued for an
    /// offline Mac): on no wall.
    var pendingRemoval: Set<String> { closeBook.closing }
    /// A close that asks first, inline on the agent's tile (needs you, a
    /// running shell command, uncommitted work in its worktree).
    var closeConfirm: PendingClose? { didSet { if oldValue != closeConfirm { onAgentsChanged?() } } }
    /// What happens once an agent is closed (the focus view steps on, an
    /// agent window closes): agent id → action.
    @ObservationIgnored var afterClose: [String: @MainActor () -> Void] = [:]
    /// Agent windows: ⌘W closes the window with the agent (set by the
    /// window layer); nil: the focus view steps to the next agent.
    @ObservationIgnored var closeInFocus: ((String) -> Void)?
    /// A background agent finished: (name, reason) for the notification.
    @ObservationIgnored var onFinishedInBackground: ((String) -> Void)?
    /// Deny with a message: the agent whose band shows the field, and the
    /// message per agent (kept when the field closes).
    var denyOpen: String? { didSet { if oldValue != denyOpen { onAgentsChanged?() } } }
    var denyText: [String: String] = [:]
    var renameText: [String: String] = [:]

    /// How the wall arranges cards (toolbar, ⌥⌘1…5, ⌥⌘L, Settings: the front wall);
    /// remembered.
    var arrangement: WallArrangement = .shelf {
        didSet {
            guard oldValue != arrangement else { return }
            if persistSettings { UserDefaults.standard.set(arrangement.rawValue, forKey: Self.arrangementKey) }
            onLayoutSettingsChanged?()
        }
    }
    /// The narrowest a card gets, in characters at the tile font (80; 60
    /// for dense walls).
    var minChars: Int = 80 {
        didSet {
            guard oldValue != minChars else { return }
            if persistSettings { UserDefaults.standard.set(minChars, forKey: Self.minCharsKey) }
            onLayoutSettingsChanged?()
        }
    }
    // MARK: View (projects): scope + grouping + layout (AppModel+Projects)

    /// Which agents this wall shows (the window layer distributes; agent
    /// windows: unused).
    var scope: WallScope = .all { didSet { if oldValue != scope { onViewChanged?() } } }
    /// Bands one level below the scope (auto) or the user's choice.
    var grouping: Grouping = .auto { didSet { if oldValue != grouping { onLayoutSettingsChanged?(); onViewChanged?() } } }
    /// Collapsed bands (one header line each), by band key.
    var collapsedBands: Set<String> = [] { didSet { if oldValue != collapsedBands { onLayoutSettingsChanged?(); onViewChanged?() } } }
    /// Bands that start collapsed (Scratch) this wall opened.
    var expandedBands: Set<String> = [] { didSet { if oldValue != expandedBands { onLayoutSettingsChanged?(); onViewChanged?() } } }
    /// This wall's band order (header drag), band keys first in this order.
    var bandOrder: [String] = [] { didSet { if oldValue != bandOrder { onLayoutSettingsChanged?(); onViewChanged?() } } }
    /// The project sidebar (⌘0), per wall.
    var sidebarVisible = false { didSet { if oldValue != sidebarVisible { onViewChanged?() } } }
    /// Bands that continue from an earlier wall of the overflow chain.
    var continuedBands: Set<String> = []
    /// Desks: projects / groups that have their own visible wall — a
    /// collapsed pointer line (default), their full band, or hidden.
    var ownWallMode: OwnWallMode = .collapsed { didSet { if oldValue != ownWallMode { onOwnWallModeChanged?(); onViewChanged?() } } }
    /// The pointer lines this wall shows (set by the window layer).
    var pointers: [WallPointer] = []
    @ObservationIgnored var onOwnWallModeChanged: (() -> Void)?
    /// A pointer's window title ("Hesper — acme-apps"); a click on a pointer.
    @ObservationIgnored var pointerTitle: ((String) -> String?)?
    @ObservationIgnored var openPointer: ((String) -> Void)?
    /// Desks: the picker's and the name field's content; the name typed.
    @ObservationIgnored var deskContent: ((PopoverKind) -> PopoverContent)?
    @ObservationIgnored var deskNameSubmit: ((String, String) -> Void)?
    var deskNameText = ""
    /// Home wall: the strip's row is reserved (stable), and the agents it
    /// shows (needing you, not visible on this wall).
    var homeStripReserved = false { didSet { if oldValue != homeStripReserved { onLayoutSettingsChanged?() } } }
    var homeNeedsYou: [Agent] = []
    /// A draft stays in the band it was opened in until it starts (a
    /// changed #project moves it once, on start): draft id → project id.
    @ObservationIgnored var draftBands: [String: String] = [:]
    /// Grouping, collapse, band order or the sidebar changed (the window
    /// layer saves windows.json and re-reads the home wall).
    @ObservationIgnored var onViewChanged: (() -> Void)?
    /// A needs-you card or a sidebar row asks the window layer.
    @ObservationIgnored var openElsewhere: ((String) -> Void)?
    @ObservationIgnored var sidebarAction: ((SidebarAction) -> Void)?

    static let arrangementKey = "wallArrangement"
    static let minCharsKey = "wallMinChars"
    static let placementsKey = "wallPlacements"
    @ObservationIgnored private(set) var persistSettings = false
    @ObservationIgnored var onLayoutSettingsChanged: (() -> Void)?

    /// Hooks for the window/wall (set by the UI layer).
    @ObservationIgnored var onTransitions: (([AgentRegistry.Transition]) -> Void)?
    @ObservationIgnored var onAgentsChanged: (() -> Void)?
    @ObservationIgnored var onModeChanged: (() -> Void)?
    @ObservationIgnored var onReconnected: (() -> Void)?
    /// The wall: where a card is (root view coordinates) for popovers.
    @ObservationIgnored var cardFrame: ((String) -> CGRect?)?
    /// The wall: frames of cards that need the user (overlays avoid them).
    @ObservationIgnored var attentionFrames: (() -> [CGRect])?
    /// The quick launch panel (AppDelegate).
    @ObservationIgnored var onQuickLaunch: (() -> Void)?
    @ObservationIgnored var onStarted: ((Agent, Bool) -> Void)?
    @ObservationIgnored var onShowSettings: (() -> Void)?

    struct Toast: Equatable, Identifiable {
        let id = UUID()
        var text: String
        var isError: Bool
        /// A button in the toast ("Show ↗": where a started agent went).
        var action: ToastAction? = nil
        static func == (l: Toast, r: Toast) -> Bool { l.id == r.id && l.text == r.text && l.isError == r.isError && l.action?.title == r.action?.title }
    }

    struct ToastAction {
        var title: String
        var run: @MainActor () -> Void
    }

    init(env: AppEnvironment, userFontSize: Double? = nil) {
        self.env = env
        closeBook = CloseBook()
        scratchBook = ScratchBook()
        bringBook = BringBook()
        moveBook = MoveBook()
        client = DaemonClient(socketPath: env.socketPath, clientName: "Hesper.app", clientVersion: AppInfo.version)
        // Automated runs (tests, screenshots) take the layout from flags and
        // never touch the user's defaults.
        let d = UserDefaults.standard
        let automated = env.values["perf-out"] != nil || env.values["selftest-out"] != nil || env.values["layout-out"] != nil || env.options.contains("ephemeral")
        settings = AppSettings(persist: !automated, defaultTileFont: env.values["tile-font"].flatMap(Double.init) ?? userFontSize ?? 12)
        if let a = env.values["arrangement"].flatMap(WallArrangement.init(rawValue:)) {
            arrangement = a
        } else if !automated {
            arrangement = d.string(forKey: Self.arrangementKey).flatMap(WallArrangement.init(rawValue:)) ?? .shelf
            minChars = d.object(forKey: Self.minCharsKey) as? Int ?? 80
            placements = d.dictionary(forKey: Self.placementsKey) as? [String: String] ?? [:]
            persistSettings = true
        }
        if let m = env.values["min-chars"].flatMap(Int.init) { minChars = m }
        settings.onTileFontChanged = { [weak self] in self?.onLayoutSettingsChanged?() }
    }

    // MARK: Window layer integration (docs: "As built — windows")
    //
    // Extra walls and agent windows each get their own AppModel (their own
    // mode, selection, layout, overlays) that shares the main model's
    // DaemonClient (one control connection, one agents.subscribe) and is fed
    // its events (`onEvent` → `ingest`). The wall shows `wallScope(all)`.

    /// A model for another window: same client, a copy of the registry,
    /// never persists layout settings.
    init(env: AppEnvironment, sharing primary: AppModel) {
        self.env = env
        listSource = primary
        client = primary.client
        closeBook = primary.closeBook
        scratchBook = primary.scratchBook
        bringBook = primary.bringBook
        moveBook = primary.moveBook
        settings = primary.settings
        registry = primary.registry
        drafts = primary.drafts
        draftsSupported = primary.draftsSupported
        projectFolders = primary.projectFolders
        connectionMessage = primary.connectionMessage
        recentProjects = primary.recentProjects
        profiles = primary.profiles
        arrangement = primary.arrangement
        minChars = primary.minChars
        selectedID = primary.selectedID
        onQuickLaunch = primary.onQuickLaunch
        onQuickLaunchPreset = primary.onQuickLaunchPreset
        onShowSettings = primary.onShowSettings
        onStarted = primary.onStarted
        isMirror = true
    }

    @ObservationIgnored private(set) var isMirror = false
    /// Every daemon event after this model applied it (the window layer
    /// fans it out to the other windows' models).
    @ObservationIgnored var onEvent: ((DaemonEvent) -> Void)?
    /// Applies an event the main model received.
    func ingest(_ e: DaemonEvent) { handle(e) }
    /// The wall's scope: which of all agents (wall order) this wall shows.
    @ObservationIgnored var wallScope: (([Agent]) -> [Agent])?
    /// The scope popover's rows (PopoverKind.scope).
    @ObservationIgnored var scopeContent: (() -> PopoverContent)?
    /// This window's wall id (windows.json): drafts made here name it
    /// (`Draft.wall`); nil: no window layer (one window, tests).
    @ObservationIgnored var wallID: String?
    /// The drafts this window shows (`DraftHome`: its own, plus, on the
    /// main wall, those without an open window); nil: all of them.
    @ObservationIgnored var draftsShown: (([Draft]) -> [Draft])?
    /// Shared history's ghost cards: the home wall only.
    @ObservationIgnored var showsGhosts: (() -> Bool)?
    /// A draft of this window started (the window layer says where it
    /// went when this window doesn't show it).
    @ObservationIgnored var onDraftStarted: ((Agent) -> Void)?
    /// Quick launch preset to a folder and machine (⌘N in an agent window
    /// or the focus view).
    @ObservationIgnored var onQuickLaunchPreset: ((String?, String?) -> Void)?
    /// ⌘J: true when the window layer took the agent elsewhere (its own
    /// window, another wall).
    @ObservationIgnored var routeAttention: ((Agent) -> Bool)?
    /// Tile clicks (⇧/⌘-click, ⌥⇧-click, ⌃-click): true when handled.
    @ObservationIgnored var tileClick: ((String, NSEvent, NSView) -> Bool)?
    /// The tile's context menu.
    @ObservationIgnored var tileMenu: ((String) -> NSMenu?)?
    /// Whether the agent has its own window (the tile's ↗ marker).
    @ObservationIgnored var hasAgentWindow: ((String) -> Bool)?

    func start() {
        client.start()
        let events = client.events
        Task { [weak self] in
            for await e in events {
                self?.handle(e)
            }
        }
        refreshFolders()
    }

    // MARK: Daemon events

    private func handle(_ e: DaemonEvent) {
        let before = closingBefore(e) // closing agents: the state / name before this event
        let transitions = registry.apply(e)
        if !isMirror { closingAfter(e, before: before) }
        switch e {
        case .connected:
            connectionMessage = nil
            if !isMirror { Task { await refreshLists(); await probeScratch() } }
            onReconnected?()
        case .helloRefreshed:
            break // the registry keeps the fresh hello (machines online)
        case .disconnected(let why):
            if connectionMessage == nil || registry.agents.isEmpty {
                connectionMessage = "hesperd is not reachable — retrying (\(why))"
            }
        case .removed(let id, _):
            if closeConfirm?.id == id { closeConfirm = nil }
            if selectedID == id { selectedID = wall.first?.id }
            if focusedID == id { exitFocus() }
            moveGone(id)
        case .changed(let a):
            adoptIfStarting(a)
            followMoved(a.id)
            if !isMirror && registry.catalog.needsRefresh(for: a) { scheduleProjectsRefresh() }
        case .reconciled(let ids):
            // Forget places of agents that are gone.
            let gone = placements.keys.filter { !ids.contains($0) && registry.agents[$0] == nil }
            if !gone.isEmpty { for id in gone { placements.removeValue(forKey: id) } }
        case .draftChanged(let d):
            drafts.applyRemote(d)
        case .draftRemoved(let id):
            if drafts.applyRemoved(id) { draftGone(id) }
        case .draftsListed(let list):
            if let list {
                draftsSupported = true
                _ = drafts.reconcile(list)
                scheduleDraftSave() // edits made while disconnected
            } else {
                draftsSupported = false
            }
        case .projectsListed, .projectChanged, .projectRemoved, .groupChanged, .groupRemoved:
            break // the registry's catalog; the views re-read it
        case .moving(let p):
            if !isMirror { moveProgressed(p) } // the shared book, once
        case .bringing(let p):
            if !isMirror { bringProgressed(p) } // the shared book, once
        case .moved(let from, let to):
            moved(from, to: to)
        }
        switch e {
        case .draftChanged, .draftsListed, .projectsListed, .projectChanged: normalizeDrafts()
        default: break
        }
        if selectedID == nil || !wall.contains(where: { $0.id == selectedID }) {
            // Shared history: a selected ghost card keeps the selection.
            if !(selectedID?.hasPrefix("ghost:") == true && wallItems.contains(where: { $0.id == selectedID })) { selectedID = wall.first?.id }
        }
        if !transitions.isEmpty { onTransitions?(transitions) }
        onAgentsChanged?()
        onEvent?(e)
    }

    @ObservationIgnored private var projectsRefresh: Task<Void, Never>?
    /// An agent named a project we don't know (hesperd's scratch folders
    /// have no notifications): projects.list, debounced; the result goes
    /// through `handle` so every window's model gets it.
    func scheduleProjectsRefresh() {
        guard projectsRefresh == nil else { return }
        projectsRefresh = Task { @MainActor [weak self] in
            try? await Task.sleep(nanoseconds: 400_000_000)
            guard let self else { return }
            if let (p, g) = try? await self.client.listProjects() { self.handle(.projectsListed(p, g)) }
            self.projectsRefresh = nil
        }
    }

    func refreshLists() async {
        if let p = try? await client.recentProjects() { recentProjects = p }
        if let p = try? await client.profiles() { profiles = p }
        normalizeDrafts() // #tokens resolve against the lists
    }

    func refreshFolders() {
        let root = ProcessInfo.processInfo.environment["HESPER_PROJECT_ROOT"] ?? NSHomeDirectory() + "/projects"
        Task.detached {
            let fm = FileManager.default
            let names = (try? fm.contentsOfDirectory(atPath: root)) ?? []
            var out: [String] = []
            for n in names.sorted() where !n.hasPrefix(".") {
                var isDir: ObjCBool = false
                let p = root + "/" + n
                if fm.fileExists(atPath: p, isDirectory: &isDir), isDir.boolValue { out.append(p) }
            }
            let folders = out
            await MainActor.run {
                self.projectFolders = folders
                self.normalizeDrafts()
            }
        }
    }

    // MARK: Derived

    /// Window layer: the cards this wall shows (its scope). A draft shows
    /// in the window it was made in (`DraftHome`).
    var scopedWallItems: [WallItem] {
        let items = unscopedWallItems
        guard let wallScope else { return items }
        let shown = Set(wallScope(items.compactMap(\.agent)).map(\.id))
        let all = items.compactMap { item -> Draft? in if case .draft(let d) = item { return d } else { return nil } }
        let drafts = Set((draftsShown?(all) ?? all).map(\.id))
        let ghosts = showsGhosts?() ?? true
        return items.filter {
            switch $0 {
            case .agent(let a): return shown.contains(a.id)
            case .ghost(let s): return ghosts && ghostInScope(s) // shared history
            case .draft(let d): return drafts.contains(d.id)
            }
        }
    }

    /// The cards this wall shows, by band when it draws bands (projects),
    /// else in wall order.
    var wallItems: [WallItem] {
        let items = scopedWallItems
        let v = resolveView(items)
        guard v.showsBands else { return items }
        var byID: [String: WallItem] = [:]
        for i in items { byID[i.id] = i }
        return v.order.compactMap { byID[$0] }
    }

    /// Every card in wall order: agents in the registry's order, drafts and
    /// agents started from drafts right of the tile they were created next
    /// to.
    var unscopedWallItems: [WallItem] {
        // Closing and background agents are on no wall.
        let agents = registry.wallOrder.filter { !pendingRemoval.contains($0.id) && !isBackground($0) }
        let present = Set(agents.map(\.id))
        let adopted = Set(adoptions.filter { present.contains($0.key) }.values)
        let visibleDrafts = drafts.all.filter { !adopted.contains($0.id) }
        var byID: [String: WallItem] = [:]
        for a in agents { byID[a.id] = .agent(a) }
        for d in visibleDrafts { byID[d.id] = .draft(d) }
        let base = agents.filter { placements[$0.id] == nil }.map(\.id)
        var anchored: [(id: String, after: String?, created: Date)] = visibleDrafts.map { ($0.id, $0.after, $0.created ?? .distantPast) }
        for a in agents { if let p = placements[a.id] { anchored.append((a.id, p, a.created ?? .distantPast)) } }
        anchored.sort { $0.created < $1.created }
        return withGhosts(WallOrder.arrange(base: base, anchored: anchored.map { ($0.id, $0.after) }).compactMap { byID[$0] }) // shared history: ghost cards
    }

    var wall: [Agent] { wallItems.compactMap(\.agent) }
    /// Every agent in wall order, whatever this wall's scope.
    var unscopedWall: [Agent] { unscopedWallItems.compactMap(\.agent) }
    var counts: StateCounts { registry.counts }
    /// From the main model in every window (another window's copy of the
    /// hello was taken when it opened, often before the app connected).
    var machines: [Machine] { lists.registry.hello?.machines ?? [] }
    var localMachine: String { lists.registry.hello?.machine ?? "L" }
    var isConnected: Bool { lists.registry.connected }
    func agent(_ id: String?) -> Agent? { id.flatMap { registry.agents[$0] } }
    func draft(_ id: String?) -> Draft? { id.flatMap { drafts[$0] } }
    func machine(_ short: String) -> Machine? { machines.first { $0.short == short } }

    /// The agent commands act on: the focused one, else the selected tile.
    var current: Agent? { agent(mode == .focus ? focusedID : selectedID) }
    var selectedDraft: Draft? { mode == .focus ? nil : draft(selectedID) }

    // MARK: Commands

    func perform(_ cmd: AppCommand) {
        switch cmd {
        case .newAgent:
            // ⌘N on a focused band header: in that band; otherwise a
            // draft without a project in the "New" area.
            if let k = focusedBandKey, mode != .focus, resolvedView.showsBands, resolvedView.band(key: k)?.pointer == nil,
               resolvedView.band(key: k) != nil {
                newDraft(band: k)
            } else {
                newDraft()
            }
        case .palette:
            togglePalette()
        case .nextAttention:
            toggleAttentionQueue() // AppModel+Overlays: the ⌘J inbox
        case .toggleFocus:
            if mode == .focus { exitFocus() } else if let d = selectedDraft { editDraft(d.id) } else if let id = activeTileID ?? selectedID { focus(id) }
        case .exitFocus:
            if mode == .focus { exitFocus() } else { deactivateTile() }
        case .activateTile:
            // ⏎ on a killed or ended agent: resume it (the strip's "Resume ⏎").
            if let a = agent(selectedID), !a.isRunning { resume(a) } else if let id = selectedID { activate(id) }
        case .typeInto(let text):
            typeIntoSelected(text)
        case .stepPrevious, .stepNext:
            leaveComposer()
            step(cmd == .stepNext ? 1 : -1)
        case .closeAgent:
            if let id = editingDraftID ?? selectedDraft?.id { discardDraft(id); return }
            guard let a = current else { return }
            requestClose(a)
        case .backgroundAgent:
            guard let a = current else { return }
            sendToBackground(a)
        case .killAgent:
            guard let a = current else { return }
            kill(a)
        case .tidyUp:
            tidyUp()
        case .reopenClosed:
            reopenLast()
        case .moveAgent:
            guard let a = current else { return }
            openPopover(.move(a.id))
        case .select(let move):
            moveSelection(move)
        case .answer(let d):
            guard let a = current else { return }
            answer(a, d)
        case .denyWithMessage:
            guard let a = current else { return }
            denyOpen = a.id
        case .arrangement(let i):
            mainPick = selectedID
            if WallArrangement.allCases.indices.contains(i) { arrangement = WallArrangement.allCases[i] }
        case .cycleArrangement:
            mainPick = selectedID
            let all = WallArrangement.allCases
            arrangement = all[((all.firstIndex(of: arrangement) ?? 0) + 1) % all.count]
        case .startDraft, .startDraftAndNew:
            if let id = editingDraftID { startDraft(id, openNext: cmd == .startDraftAndNew) }
        case .toggleWorktree:
            if let id = editingDraftID { composer(for: id).toggleWorktree() }
        case .leaveDraft:
            if let id = editingDraftID { parkDraft(id) }
        case .editDraft:
            if let d = selectedDraft { editDraft(d.id) }
        case .undo:
            undoLast()
        case .jumpBand(let d):
            jumpBand(d)
        case .toggleSidebar:
            sidebarVisible.toggle()
        case .history: // shared history (⌘Y)
            toggleHistory()
        case .passThrough, .none:
            break
        }
    }

    /// Set by the wall: the card a keyboard move selects (its laid-out
    /// cards, WallNavigation); nil: none.
    @ObservationIgnored var neighbor: ((String?, WallMove) -> String?)?
    /// Set by the wall: whether the agent's card has a terminal (shelf
    /// cards don't: activating one opens the focus view instead).
    @ObservationIgnored var tileHasTerminal: ((String) -> Bool)?
    /// Set by the wall: types into a draft's editor (typing on a selected
    /// draft).
    @ObservationIgnored var insertIntoDraft: ((String, String) -> Void)?

    /// Arrows / Home / End on the wall: only the selection moves (no
    /// read-write attach, no resize; main + stack keeps its main card).
    private func moveSelection(_ move: WallMove) {
        let w = wallItems
        guard !w.isEmpty else { return }
        let ids = w.map(\.id)
        let cur = ids.contains(selectedID ?? "") ? selectedID : nil
        let next: String
        if let n = neighbor?(cur, move) {
            next = n
        } else {
            let i = cur.flatMap { ids.firstIndex(of: $0) } ?? 0
            switch move {
            case .first: next = ids[0]
            case .last: next = ids[ids.count - 1]
            case .left, .up: next = ids[max(0, i - 1)]
            case .right, .down: next = ids[min(ids.count - 1, i + 1)]
            }
        }
        if activeTileID != nil && activeTileID != next { activeTileID = nil }
        if editingDraftID != nil && editingDraftID != next { leaveComposer() }
        selectedID = next
        onModeChanged?()
    }

    /// ⌥⌘← ⌥⌘→ / ⌘[ ⌘] (Tab on the wall): the previous / next agent in wall
    /// order. Focus shows it; on the wall the selection moves, and typing
    /// moves along with it when a tile was active (like switching tabs).
    private func step(_ d: Int) {
        let hidden = hiddenIDs
        let w = mode == .focus ? projectStepOrder(wall.map(\.id)) : wallItems.map(\.id).filter { !hidden.contains($0) }
        guard !w.isEmpty else { return }
        let cur = mode == .focus ? focusedID : selectedID
        let i = w.firstIndex(where: { $0 == cur }) ?? (d > 0 ? w.count - 1 : 0)
        let j = (i + d + w.count) % w.count
        if mode == .focus {
            focus(w[j])
        } else if activeTileID != nil, agent(w[j])?.isRunning == true {
            activate(w[j])
        } else {
            if activeTileID != nil { activeTileID = nil }
            selectedID = w[j]
            onModeChanged?()
        }
    }

    /// The tile becomes the one the keyboard types into. A shelf card has
    /// no terminal: it opens the focus view instead.
    func activate(_ id: String) {
        bringBackIfNeeded(id) // a background agent comes back onto its wall
        reveal(id)
        guard let a = registry.agents[id], a.isRunning else { select(id); return }
        if tileHasTerminal?(id) == false { focus(id); return }
        if editingDraftID != nil { leaveComposer() }
        closePopover()
        selectedID = id
        mainPick = id
        activeTileID = id
        onModeChanged?()
    }

    /// A printable key on the selected card: an agent becomes the active
    /// tile and gets the text at once (agents.input: its read-write
    /// terminal takes the keyboard a moment later); a draft opens its
    /// editor with the text.
    func typeIntoSelected(_ text: String) {
        guard mode != .focus, let id = selectedID else { return }
        if draft(id) != nil {
            editDraft(id)
            insertIntoDraft?(id, text)
            return
        }
        guard let a = agent(id), a.isRunning else { return }
        if activeTileID != id { activate(id) }
        sendInput(id, text)
    }

    @ObservationIgnored private var pendingInput: [(id: String, text: String, paste: Bool)] = []
    @ObservationIgnored private var inputTask: Task<Void, Never>?
    /// The last drop's pastes (tests).
    @ObservationIgnored var lastDrop: (id: String, pastes: [String])?

    /// Text for an agent's terminal through hesperd (agents.input), in
    /// order: one call at a time, consecutive keys to one agent coalesced.
    /// `paste`: a bracketed paste (dropped files; never coalesced, never
    /// submitted).
    func sendInput(_ id: String, _ text: String, paste: Bool = false) {
        if !paste, let last = pendingInput.last, last.id == id, !last.paste { pendingInput[pendingInput.count - 1].text += text } else { pendingInput.append((id, text, paste)) }
        guard inputTask == nil else { return }
        inputTask = Task { @MainActor [weak self] in
            while let self, !self.pendingInput.isEmpty {
                let next = self.pendingInput.removeFirst()
                do { try await self.client.input(next.id, text: next.text, paste: next.paste) } catch { self.showToast("Could not type: \(self.describe(error))", error: true) }
                // Separate pastes (one per image): give the agent a moment
                // to turn each into an attachment.
                if next.paste, self.pendingInput.first?.paste == true { try? await Task.sleep(nanoseconds: 150_000_000) }
            }
            self?.inputTask = nil
        }
    }

    func deactivateTile() {
        guard activeTileID != nil else { return }
        activeTileID = nil
        onModeChanged?()
    }

    func focus(_ id: String) {
        guard registry.agents[id] != nil else { return }
        bringBackIfNeeded(id)
        activeTileID = nil
        leaveComposer()
        closePopover()
        selectedID = id
        mainPick = id
        focusedID = id
        mode = .focus
        onModeChanged?()
    }

    func exitFocus() {
        guard mode == .focus else { return }
        mode = .wall
        focusedID = nil
        onModeChanged?()
    }

    func select(_ id: String) {
        focusedBandKey = nil
        reveal(id)
        if editingDraftID != nil && editingDraftID != id { leaveComposer() }
        if activeTileID != id { activeTileID = nil }
        selectedID = id
        mainPick = id
        onModeChanged?()
    }

    // MARK: Daemon calls

    func answer(_ a: Agent, _ d: Decision, message: String? = nil) {
        if d == .deny { denyOpen = nil; denyText[a.id] = nil }
        run("answer") { try await self.client.answer(a.id, decision: d, message: message) }
    }

    /// A plain spawn (tests, scripts); nil or the error.
    func spawn(_ req: SpawnRequest) async -> String? {
        do {
            let a = try await client.spawn(req)
            selectedID = a.id
            onModeChanged?()
            Task { await refreshLists() }
            return nil
        } catch {
            return describe(error)
        }
    }

    func resume(_ a: Agent) {
        closeBook.localKilled.remove(a.id)
        run("resume") { _ = try await self.client.resume(a.id) }
    }

    func rename(_ a: Agent, to name: String) {
        closePopover()
        renameText[a.id] = nil
        run("rename") { _ = try await self.client.rename(a.id, name: name) }
    }

    func clone(url: String, machine: String?) async -> CloneResult {
        var p: [String: JSONValue] = ["url": .string(url)]
        if let machine { p["machine"] = .string(machine) }
        do {
            let r = try await client.call("projects.clone", .object(p))
            if let path = r["path"]?.stringValue {
                if !recentProjects.contains(where: { $0.path == path }) {
                    recentProjects.insert(RecentProject(path: path, name: (path as NSString).lastPathComponent), at: 0)
                }
                Task { await refreshLists() }
                return .success(path)
            }
            return .failure("hesperd returned no path")
        } catch let e as RPCError where e.code == -32601 {
            return .failure("This hesperd cannot clone (projects.clone). Clone it yourself and use #folder.")
        } catch {
            return .failure(describe(error))
        }
    }

    func run(_ what: String, unavailable: String? = nil, _ body: @escaping @MainActor () async throws -> Void) {
        Task { @MainActor in
            do { try await body() } catch {
                if let e = error as? RPCError, e.kind == .unavailable, let unavailable {
                    showToast("\(unavailable): \(e.message)", error: true)
                } else {
                    showToast("Could not \(what): \(describe(error))", error: true)
                }
            }
        }
    }

    func describe(_ error: any Error) -> String {
        if let e = error as? RPCError { return e.message }
        if let e = error as? ConnectionError { return e.description }
        return error.localizedDescription
    }

    func showToast(_ text: String, error: Bool = false, action: ToastAction? = nil) {
        let t = Toast(text: text, isError: error, action: action)
        toast = t
        Task { @MainActor in
            try? await Task.sleep(nanoseconds: action == nil ? 4_000_000_000 : 6_000_000_000)
            if self.toast?.id == t.id { self.toast = nil }
        }
    }
}

enum CloneResult { case success(String), failure(String) }

enum AppInfo {
    static var version: String { Bundle.main.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String ?? "0.1.0-dev" }
}
