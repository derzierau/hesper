import AppKit
import HesperCore
import SwiftUI

/// The window layer (docs/rebuild-contract.md "As built — windows"): walls
/// with scopes, agent windows (one per agent, tabbable), routing of ⌘J /
/// notification / menu bar clicks, size ownership between windows, menus,
/// the Dock badge and state restoration.
///
/// Every window has its own AppModel. The main model owns the one
/// DaemonClient (one control connection, one agents.subscribe); every
/// other model shares that client and is fed the main model's events.
@MainActor
final class WindowManager: NSObject {
    let primary: AppModel
    let env: AppEnvironment
    let userFontSize: Double
    private(set) var walls: [WallEntry] = []
    private var book = AgentWindowBook<AgentWindowController>()
    let ownership = SizeOwnership()
    let restorer: WindowRestorer
    /// Desks: the arrangement per display setup (desks.json, switching).
    private(set) var desks: DeskController!
    private var nextWallNumber = 2
    /// The wall in front (Settings › Wall edits its layout).
    let front: FrontWall
    /// Every daemon event after all windows applied it (tests).
    var eventTap: ((DaemonEvent) -> Void)?

    /// Call before the main window (and its toolbar) is created.
    static func prepare() { ScopePill.install() }

    init(primary: AppModel, main: MainWindowController, env: AppEnvironment, userFontSize: Double) {
        self.primary = primary
        self.env = env
        self.userFontSize = userFontSize
        restorer = WindowRestorer(env: env)
        front = FrontWall(primary)
        super.init()
        desks = DeskController(manager: self, env: env)
        restorer.desks = desks
        let mainWall = WallEntry(id: "main", isMain: true, model: primary, root: main.root, window: main.window!, scope: .all)
        walls = [mainWall]
        attach(mainWall)
        primary.onEvent = { [weak self] e in self?.fanOut(e) }
        // Closing agents: a close / background in one window redraws them all.
        primary.closeBook.onChange = { [weak self] in self?.markersChanged(); self?.updatePills() }
        ScopePill.onClick = { [weak self] m in self?.showScopePopover(for: m) }
        ownership.isOurs = { [weak self] w in self?.isOurs(w) ?? false }
        ownership.terminals = { [weak self] in self?.focusTerminals() ?? [] }
        // Every surface asks: a terminal in a window that isn't key never
        // attaches as a second owner (SizeOwner).
        AgentTerminal.ownershipResolver = { [weak self] t in self?.ownership.wouldOwn(t) ?? t.ownsSize }
        restorer.manager = self
        observeChanges { [weak self] in
            guard let self else { return }
            let n = self.primary.counts.needingYou
            NSApp.dockTile.badgeLabel = n > 0 ? "\(n)" : nil
        }
    }

    // MARK: Models

    /// Wires a wall's model into the window layer.
    private func attach(_ w: WallEntry) {
        let m = w.model
        m.wallScope = { [weak self, weak w] all in
            guard let self, let w else { return all }
            return self.agents(for: w, all: all)
        }
        m.routeAttention = { [weak self, weak m] a in self?.routeAttention(a, from: m) ?? false }
        m.tileClick = { [weak self, weak m] id, e, v in self?.tileClick(id, e, v, model: m) ?? false }
        m.tileMenu = { [weak self, weak m] id in m.flatMap { self?.tileMenu(id, model: $0) } }
        m.hasAgentWindow = { [weak self] id in self?.book.handle(for: id) != nil }
        m.scopeContent = { [weak self, weak w] in
            guard let self, let w else { return PopoverContent(title: nil, items: [], note: nil, hints: []) }
            return self.scopeContent(for: w)
        }
        // New agents per window: a draft shows in the window it was made
        // in (the main wall: also those whose window is gone); history
        // cards on the home wall only.
        m.wallID = w.id
        m.draftsShown = { [weak self, weak w] ds in
            guard let self, let w else { return ds }
            return self.drafts(ds, shownOn: w)
        }
        m.showsGhosts = { [weak self, weak w] in self?.walls.first === w }
        m.onDraftStarted = { [weak self, weak w] a in if let w { self?.started(a, from: w) } }
        // Projects (views): the sidebar and ↗ ask for walls; needs-you
        // cards and sidebar rows route like ⌘J; view changes are saved.
        m.sidebarAction = { [weak self, weak w] action in
            guard let self, let w else { return }
            self.sidebar(action, from: w)
        }
        m.openElsewhere = { [weak self] id in self?.open(id, focus: false) }
        // Desks: pointer lines to project / group walls.
        m.pointerTitle = { [weak self] id in self?.walls.first { $0.id == id }?.window.title }
        m.openPointer = { [weak self] id in
            guard let self, let target = self.walls.first(where: { $0.id == id }) else { return }
            self.bringForward(target.window)
        }
        m.onOwnWallModeChanged = { [weak self] in self?.resyncWalls() }
        desks.attach(m)
        m.onViewChanged = { [weak self] in
            guard let self else { return }
            self.restorer.scheduleSave()
            self.updateHome()
            self.updatePills()
        }
        // The wall in front: what Settings › Wall edits.
        NotificationCenter.default.addObserver(forName: NSWindow.didBecomeMainNotification, object: w.window, queue: .main) { [weak self, weak w] _ in
            MainActor.assumeIsolated { if let self, let w, self.front.model !== w.model { self.front.model = w.model } }
        }
        // Capacity follows the window size and the wall's layout settings.
        NotificationCenter.default.addObserver(forName: NSWindow.didResizeNotification, object: w.window, queue: .main) { [weak self, weak w] _ in
            MainActor.assumeIsolated { if let w { self?.scheduleCapacity(w) } }
        }
        for name in [NSWindow.didMoveNotification, NSWindow.didResizeNotification, NSWindow.didEnterFullScreenNotification,
                     NSWindow.didExitFullScreenNotification] {
            NotificationCenter.default.addObserver(forName: name, object: w.window, queue: .main) { [weak self] _ in
                MainActor.assumeIsolated { self?.restorer.scheduleSave() }
            }
        }
        // Desks: a project / group wall that is minimized, hidden, shown
        // again or moved to another display changes the other walls'
        // pointers (occlusion changes also come with ordering out / in).
        for name in [NSWindow.didMiniaturizeNotification, NSWindow.didDeminiaturizeNotification, NSWindow.didChangeScreenNotification,
                     NSWindow.didChangeOcclusionStateNotification] {
            NotificationCenter.default.addObserver(forName: name, object: w.window, queue: .main) { [weak self] _ in
                MainActor.assumeIsolated { self?.scheduleVisibility() }
            }
        }
        observeChanges { [weak self, weak w] in
            guard let self, let w else { return }
            _ = (w.model.arrangement, w.model.minChars)
            self.scheduleCapacity(w)
            self.restorer.scheduleSave()
        }
        DispatchQueue.main.async { [weak self, weak w] in if let w { self?.updateCapacity(w) } }
    }

    private func fanOut(_ e: DaemonEvent) {
        for w in walls where !w.isMain { w.model.ingest(e) }
        for c in book.byAgent.values { c.model.ingest(e) }
        updateContinued()
        updateHome()
        updatePills()
        if case .reconciled = e { restorer.agentsKnown() }
        eventTap?(e)
    }

    private func mirrorModel() -> AppModel {
        AppModel(env: env, sharing: primary)
    }

    // MARK: Scopes

    func agents(for w: WallEntry, all: [Agent]) -> [Agent] {
        distribution(all, catalog: w.model.catalog).agents[w.id] ?? all
    }

    /// Every wall's agents and the overflow chain's continued bands:
    /// Overflow keeps groups (projects without groups) whole.
    func distribution(_ all: [Agent], catalog: ProjectCatalog, override: (WallEntry, WallScope)? = nil) -> Distribution {
        let slots = walls.map { WallSlot(id: $0.id, scope: override?.0 === $0 ? override!.1 : $0.scope, capacity: $0.capacity,
                                         ownWalls: $0.model.ownWallMode, visible: isReallyVisible($0)) }
        let unitLevel: GroupLevel = catalog.hasGroups ? .group : .project
        let unitOrder = ViewResolver.resolve(all.map { ViewItem(id: $0.id, projectID: catalog.projectID(for: $0)) }, level: unitLevel, catalog: catalog)
            .bands.map(\.key)
        return ScopeMath.distribute(all, walls: slots, place: { a in
            let pid = catalog.projectID(for: a)
            return AgentPlace(projectID: pid, lineage: catalog.lineage(pid), groupIDs: Set(catalog.groupIDs(of: pid)),
                              unit: ViewResolver.unit(projectID: pid, catalog: catalog))
        }, unitOrder: unitOrder)
    }

    /// The overflow chain's "(cont.)" bands and every wall's pointers.
    private func updateContinued() {
        let d = distribution(primary.unscopedWall, catalog: primary.catalog)
        for w in walls {
            let c = d.continued[w.id] ?? []
            let p = d.pointers[w.id] ?? []
            if w.model.continuedBands != c || w.model.pointers != p {
                w.model.continuedBands = c
                w.model.pointers = p
                w.model.onAgentsChanged?()
            }
        }
    }

    /// Desks: a wall makes pointers only while it is really on screen:
    /// open (ordered in), not minimized, on an attached display. Spaces
    /// can't be told apart with public API: a wall on another Space
    /// counts as visible. Occlusion (covered by another window) does not
    /// count, or every click between overlapping windows would re-lay out.
    func isReallyVisible(_ w: WallEntry) -> Bool {
        let win = w.window
        guard win.isVisible, !win.isMiniaturized, win.screen != nil else { return false }
        return desks?.isOnAttachedDisplay(win.frame) ?? true
    }

    private var visibilityPending = false
    private var lastVisible: [String: Bool] = [:]
    func scheduleVisibility() {
        guard !visibilityPending else { return }
        visibilityPending = true
        DispatchQueue.main.asyncAfter(deadline: .now() + 0.05) { [weak self] in
            guard let self else { return }
            self.visibilityPending = false
            var now: [String: Bool] = [:]
            for w in self.walls { now[w.id] = self.isReallyVisible(w) }
            guard now != self.lastVisible else { return }
            self.lastVisible = now
            self.resyncWalls()
        }
    }

    /// The home wall (the first): its needs-you strip — agents needing
    /// you that it doesn't show. The row is reserved whenever it can hide
    /// agents, so a card appearing resizes nothing.
    func updateHome() {
        for (i, w) in walls.enumerated() {
            let m = w.model
            guard i == 0 else {
                if m.homeStripReserved { m.homeStripReserved = false; m.homeNeedsYou = []; m.onAgentsChanged?() }
                continue
            }
            let v = m.resolvedView
            let collapsed = v.showsBands ? v.bands.filter { m.isCollapsed(key: $0.key) }.count : 0
            let reserved = HomeWall.reservesStrip(scope: w.scope, collapsedBands: collapsed, pointers: m.pointers.count)
            let visible = Set(m.wall.map(\.id)).subtracting(m.hiddenIDs)
            let needs = reserved ? HomeWall.needsYou(m.unscopedWall, visible: visible) : []
            if m.homeStripReserved != reserved || m.homeNeedsYou != needs {
                m.homeStripReserved = reserved
                m.homeNeedsYou = needs
                m.onAgentsChanged?()
            }
        }
    }

    /// The project sidebar: scope this wall, a new wall (⌥-click, ↗), or
    /// a wall where a row was dragged to (another wall: it takes the
    /// scope; elsewhere on a display: a new wall there).
    func sidebar(_ action: SidebarAction, from w: WallEntry) {
        switch action {
        case .scope(let s):
            setScope(s, for: w)
        case .newWall(let s):
            newWall(scope: s)
        case .wallAt(let s, let p):
            if let target = walls.first(where: { $0 !== w && $0.window.isVisible && $0.window.frame.contains(p) }) {
                setScope(s, for: target)
                bringForward(target.window)
                return
            }
            if w.window.frame.contains(p) { return } // dropped back on its own window
            let screen = NSScreen.screens.first { $0.frame.contains(p) } ?? w.window.screen ?? NSScreen.main
            let vis = screen?.visibleFrame ?? NSRect(x: 0, y: 0, width: 1400, height: 900)
            let size = NSSize(width: min(1200, vis.width), height: min(800, vis.height))
            var f = NSRect(x: p.x - size.width / 2, y: p.y - size.height + 40, width: size.width, height: size.height)
            f.origin.x = min(max(f.minX, vis.minX), vis.maxX - f.width)
            f.origin.y = min(max(f.minY, vis.minY), vis.maxY - f.height)
            newWall(scope: s, frame: f)
        }
    }

    /// A scope's name for titles and pills ("acme apps", "acme-apps").
    func scopeName(_ s: WallScope) -> String? {
        let c = primary.catalog
        switch s {
        case .group(let g): return c.groups[g]?.name ?? g
        case .project(let p): return c.name(project: p)
        default: return nil
        }
    }

    private var capacityPending: Set<String> = []
    private func scheduleCapacity(_ w: WallEntry) {
        guard capacityPending.insert(w.id).inserted else { return }
        DispatchQueue.main.asyncAfter(deadline: .now() + 0.15) { [weak self, weak w] in
            guard let self, let w else { return }
            self.capacityPending.remove(w.id)
            self.updateCapacity(w)
        }
    }

    /// How many minimum cards fit the wall's visible area.
    static func capacity(of root: RootView, model: AppModel) -> Int {
        let size = root.wall.viewport // without the room under the toolbar glass
        guard size.width > 10, size.height > 10 else { return 1 }
        let scale = Double(root.window?.backingScaleFactor ?? 2)
        let spec = root.wall.spec()
        let minCard = WallLayout.minCard(spec) { CellMetrics.cell($0, scale: scale) }
        return WallCapacity.count(width: size.width, height: size.height, minCard: minCard, spec: spec, arrangement: model.arrangement)
    }

    func updateCapacity(_ w: WallEntry) {
        let c = Self.capacity(of: w.root, model: w.model)
        if c != w.capacity {
            w.capacity = c
            resyncWalls()
        } else {
            updatePills()
        }
    }

    /// Every wall re-reads its agents (scope or capacity changed).
    func resyncWalls() {
        updateContinued()
        for w in walls { w.model.onAgentsChanged?() }
        updateHome()
        updatePills()
    }

    func setScope(_ s: WallScope, for w: WallEntry) {
        guard w.scope != s else { return }
        w.scope = s
        resyncWalls()
        restorer.scheduleSave()
    }

    func wall(for model: AppModel) -> WallEntry? { walls.first { $0.model === model } }

    private func updatePills() {
        let all = primary.unscopedWall
        let hasOverflow = walls.contains { $0.scope == .overflow }
        for w in walls {
            guard let pill = ScopePill.pill(for: w.model) else { continue }
            let n = agents(for: w, all: all).count
            switch w.scope {
            case .all where hasOverflow && walls.first === w: pill.set(name: nil, count: n, of: all.count)
            case .all: pill.set(name: nil, count: n)
            case .overflow: pill.set(name: "Overflow", count: n)
            case .filter where ScopeSegment(scope: w.scope) != nil: pill.set(name: nil, count: nil) // Needs you / Working: All counts everyone
            case .filter: pill.set(name: "Filter", count: n)
            case .group, .project: pill.set(name: scopeName(w.scope) ?? w.scope.title, count: n)
            }
            if let name = scopeName(w.scope), !w.isMain { w.window.title = "Hesper — \(name)" }
        }
    }

    // MARK: Walls

    /// ⌥⌘N: a new wall; Overflow when the first wall can't fit everyone at
    /// its minimum card width, else All.
    @discardableResult
    func newWall(scope: WallScope? = nil, frame: NSRect? = nil, id: String? = nil, show: Bool = true) -> WallEntry {
        let n = nextWallNumber
        nextWallNumber += 1
        let m = mirrorModel()
        if let first = walls.first {
            m.arrangement = first.model.arrangement
            m.minChars = first.model.minChars
            updateCapacityNow(first)
        }
        let s = scope ?? ScopeMath.defaultScope(firstWallCapacity: walls.first?.capacity ?? 1, agentCount: primary.registry.agents.count)
        let c = WallWindowController(model: m, userFontSize: userFontSize, title: "Hesper — \(scopeName(s) ?? "Wall \(n)")", manager: self)
        let w = WallEntry(id: id ?? "wall-\(UUID().uuidString.prefix(8).lowercased())", isMain: false, model: m, root: c.root, window: c.window!, scope: s)
        w.controller = c
        walls.append(w)
        attach(w)
        if let frame {
            c.window?.setFrame(frame, display: false)
        } else if let ref = NSApp.keyWindow ?? walls.first?.window {
            c.window?.setFrame(ref.frame.offsetBy(dx: 40, dy: -40), display: false)
        }
        if show {
            c.showWindow(nil)
            c.window?.makeKeyAndOrderFront(nil)
        }
        m.onAgentsChanged?()
        resyncWalls()
        restorer.scheduleSave()
        return w
    }

    private func updateCapacityNow(_ w: WallEntry) {
        w.capacity = Self.capacity(of: w.root, model: w.model)
    }

    /// Make Home Wall: the wall becomes the first of the desk (the
    /// needs-you strip, the head of the overflow chain, history cards).
    func makeHome(_ w: WallEntry) {
        guard let i = walls.firstIndex(where: { $0 === w }), i > 0 else { return }
        walls.remove(at: i)
        walls.insert(w, at: 0)
        resyncWalls()
        restorer.scheduleSave()
    }

    /// Walls in this order (desk restore); unknown ids keep their place after.
    func orderWalls(_ ids: [String]) {
        let rank = Dictionary(ids.enumerated().map { ($1, $0) }, uniquingKeysWith: { a, _ in a })
        let sorted = walls.enumerated().sorted { a, b in
            (rank[a.element.id] ?? Int.max, a.offset) < (rank[b.element.id] ?? Int.max, b.offset)
        }.map(\.element)
        guard sorted.map(\.id) != walls.map(\.id) else { return }
        walls = sorted
        resyncWalls()
    }

    var mainWall: WallEntry { walls.first { $0.isMain } ?? walls[0] }

    func wallClosed(_ c: WallWindowController) {
        guard let i = walls.firstIndex(where: { $0.controller === c }) else { return }
        let w = walls.remove(at: i)
        releaseDrafts(of: w)
        ScopePill.forget(w.model)
        if front.model === w.model { front.model = activeWall.model }
        w.model.onAgentsChanged = nil
        resyncWalls()
        restorer.scheduleSave()
    }

    // MARK: Agent windows

    /// Opens the agent in its own window, or brings its window forward.
    /// `asTab`: joins the frontmost agent window as a tab.
    @discardableResult
    func openAgentWindow(_ id: String, asTab: Bool = false, frame: NSRect? = nil, activate: Bool = true, ownsSize: Bool = true) -> AgentWindowController? {
        guard let a = primary.agent(id) else { return nil }
        let front = frontAgentWindow()
        let (c, created) = book.openOrFocus(id) {
            let m = mirrorModel()
            let c = AgentWindowController(agent: a, model: m, manager: self, fontSize: userFontSize, ownsSize: ownsSize)
            m.routeAttention = { [weak self, weak m] a in self?.routeAttention(a, from: m) ?? false }
            return c
        }
        if created, let w = c.window {
            if let frame {
                w.setFrame(frame, display: false)
            } else if let ref = front?.window ?? NSApp.keyWindow {
                w.setFrame(NSRect(x: ref.frame.minX + 30, y: ref.frame.maxY - 30 - 680, width: 980, height: 680), display: false)
            } else {
                w.center()
            }
            for name in [NSWindow.didMoveNotification, NSWindow.didResizeNotification, NSWindow.didEnterFullScreenNotification,
                         NSWindow.didExitFullScreenNotification, NSWindow.didBecomeKeyNotification] {
                NotificationCenter.default.addObserver(forName: name, object: w, queue: .main) { [weak self] _ in
                    MainActor.assumeIsolated { self?.restorer.scheduleSave() }
                }
            }
            if asTab, let host = front?.window, host !== w {
                host.addTabbedWindow(w, ordered: .above)
            }
            markersChanged()
            restorer.scheduleSave()
        }
        if activate {
            c.showWindow(nil)
            c.window?.makeKeyAndOrderFront(nil)
            NSApp.activate(ignoringOtherApps: true)
        }
        return c
    }

    func agentWindow(for id: String) -> AgentWindowController? { book.handle(for: id) }
    var agentWindows: [AgentWindowController] { Array(book.byAgent.values) }

    /// ⌥⌘← ⌥⌘→ in an untabbed agent window: the window shows the
    /// previous / next agent in wall order without a window of its own.
    @discardableResult
    func step(_ c: AgentWindowController, by d: Int) -> Bool {
        guard let id = book.stepTarget(from: c.agentID, by: d, order: primary.projectStepOrder(primary.unscopedWall.map(\.id))),
              book.retarget(c.agentID, to: id) else { return false }
        c.retarget(to: id)
        markersChanged()
        restorer.scheduleSave()
        ownership.schedule()
        return true
    }

    func agentWindowClosed(_ c: AgentWindowController) {
        book.remove(handle: c)
        c.model.onAgentsChanged = nil
        markersChanged()
        restorer.scheduleSave()
        ownership.schedule()
    }

    private func frontAgentWindow() -> AgentWindowController? {
        for w in NSApp.orderedWindows {
            if let c = (w as? AgentWindow)?.controller, book.handle(for: c.agentID) === c { return c }
        }
        return book.byAgent.values.first
    }

    private func markersChanged() {
        for w in walls { w.model.onAgentsChanged?() }
    }

    private func focusTerminals() -> [AgentTerminal] {
        // Agent windows, walls' focus views, and every tile (the active one
        // is the read-write candidate; the flag is inert on view tiles).
        book.byAgent.values.compactMap(\.terminal) + walls.compactMap { $0.root.focus.terminal }
            + walls.flatMap { $0.root.wall.tiles.values.map(\.terminal) }
    }

    func isOurs(_ w: NSWindow) -> Bool {
        walls.contains { $0.window === w } || book.byAgent.values.contains { $0.window === w }
    }

    // MARK: Routing

    /// The model of the key window (menus act on it), else the main one.
    var activeModel: AppModel {
        if let k = NSApp.keyWindow ?? NSApp.mainWindow {
            if let w = walls.first(where: { $0.window === k }) { return w.model }
            if let c = (k as? AgentWindow)?.controller { return c.model }
        }
        return primary
    }

    var activeWall: WallEntry {
        if let k = NSApp.keyWindow ?? NSApp.mainWindow, let w = walls.first(where: { $0.window === k }) { return w }
        // Settings or a panel in front: the wall that was (what Settings shows).
        if let w = walls.first(where: { $0.model === front.model }) { return w }
        for win in NSApp.orderedWindows { if let w = walls.first(where: { $0.window === win }) { return w } }
        return walls[0]
    }

    /// Where an agent is shown now (pure rule in HesperCore).
    func route(for id: String) -> WindowRoute {
        let order = NSApp.orderedWindows
        let front = walls.filter { $0.window.isVisible || $0.window.isMiniaturized }.sorted { a, b in
            (order.firstIndex(of: a.window) ?? Int.max) < (order.firstIndex(of: b.window) ?? Int.max)
        }
        return WindowRouting.route(agent: id, agentWindows: book.agents,
                                   walls: front.map { (id: $0.id, shows: Set($0.model.wall.map(\.id))) }, mainWall: walls[0].id)
    }

    /// Takes the user to an agent: its window, the frontmost wall showing
    /// it (focus view there when `focus`, else selected), or the main wall.
    func open(_ id: String, focus: Bool) {
        primary.bringBackIfNeeded(id) // a background agent comes back onto its wall first
        switch route(for: id) {
        case .agentWindow:
            openAgentWindow(id)
        case .wall(let wid):
            guard let w = walls.first(where: { $0.id == wid }) else { return }
            if focus || w.model.mode == .focus { w.model.focus(id) } else { w.model.activate(id) }
            bringForward(w.window)
        case .mainWall:
            let w = walls[0]
            w.model.focus(id)
            bringForward(w.window)
        }
    }

    private func routeAttention(_ a: Agent, from m: AppModel?) -> Bool {
        open(a.id, focus: false)
        return true
    }

    func bringForward(_ w: NSWindow) {
        if w.isMiniaturized { w.deminiaturize(nil) }
        w.makeKeyAndOrderFront(nil)
        NSApp.activate(ignoringOtherApps: true)
    }

    /// ⌘↩ / ⌘Esc in an agent window: the agent in the frontmost wall
    /// showing it (selected), else the main wall.
    func showInWall(_ id: String) {
        let order = NSApp.orderedWindows
        let shown = walls.filter { $0.model.wall.contains { $0.id == id } }
            .sorted { (order.firstIndex(of: $0.window) ?? Int.max) < (order.firstIndex(of: $1.window) ?? Int.max) }
        let w = shown.first ?? walls[0]
        if w.model.mode == .focus { w.model.exitFocus() }
        w.model.select(id)
        bringForward(w.window)
    }

    // MARK: New agents per window (contract "As built — new agents per window")

    /// A wall window that is open: every extra wall (closing one removes
    /// it); the main wall unless its window was closed (ordered out).
    func isOpen(_ w: WallEntry) -> Bool {
        !w.isMain || w.window.isVisible || w.window.isMiniaturized || NSApp.isHidden
    }

    var openWalls: [OpenWall] { walls.filter(isOpen).map { OpenWall(id: $0.id, scope: $0.scope) } }

    /// The wall standing in for the main one while its window is closed.
    var actingMainID: String { DraftHome.actingMain(openWalls: openWalls, main: mainWall.id) ?? mainWall.id }

    /// The drafts wall `w` shows: its own, and on the (acting) main wall
    /// those without an open window. Each draft: exactly one open wall.
    func drafts(_ ds: [Draft], shownOn w: WallEntry) -> [Draft] {
        let open = openWalls
        let main = DraftHome.actingMain(openWalls: open, main: mainWall.id)
        return DraftHome.visible(drafts: ds, openWalls: open.map(\.id), mainWall: main, this: w.id)
    }

    /// A wall window closed: its drafts (with the edits it hadn't saved
    /// yet) go to the main wall — its New area, or their project's band.
    private func releaseDrafts(of w: WallEntry) {
        guard !w.isMain else { return }
        w.model.leaveComposer()
        let book = w.model.drafts
        _ = w.model.drafts.flushAll() // its unsaved edits travel with the release, not after it
        for d in DraftHome.released(book.all, closing: w.id) { primary.editDraftContent(d) }
    }

    /// A draft of `w` started: when `w` doesn't show the agent (another
    /// project, its own window elsewhere), a toast says where it went,
    /// with a way there.
    private func started(_ a: Agent, from w: WallEntry) {
        guard walls.contains(where: { $0 === w }) else { return }
        var all = primary.unscopedWall
        if !all.contains(where: { $0.id == a.id }) { all.append(a) }
        let d = distribution(all, catalog: primary.catalog)
        let shows = walls.filter(isOpen).map { (id: $0.id, shows: Set((d.agents[$0.id] ?? []).map(\.id))) }
        guard let target = StartLanding.elsewhere(agent: a.id, this: w.id, walls: shows, mainWall: actingMainID), target != w.id else { return }
        let c = primary.catalog
        let project = c.projectID(for: a).map { c.name(project: $0) } ?? (a.project.map { ($0 as NSString).lastPathComponent } ?? "no project")
        w.model.showToast("Started \(a.name) in \(project)", action: .init(title: "Show ↗") { [weak self] in self?.showStarted(a.id, in: target) })
    }

    /// "Show ↗": the wall that shows the agent, forward, the agent
    /// selected; else wherever ⌘J would take it (the main wall).
    func showStarted(_ id: String, in target: String) {
        guard let w = walls.first(where: { $0.id == target }), w.model.wall.contains(where: { $0.id == id }) else {
            open(id, focus: false)
            return
        }
        if w.model.mode == .focus { w.model.exitFocus() }
        w.model.select(id)
        bringForward(w.window)
    }

    // MARK: Tiles

    private func tileClick(_ id: String, _ e: NSEvent, _ v: NSView, model: AppModel?) -> Bool {
        let f = e.modifierFlags
        if f.contains(.control) {
            if let model, let menu = tileMenu(id, model: model) { NSMenu.popUpContextMenu(menu, with: e, for: v) }
            return true
        }
        if f.contains(.option) && f.contains(.shift) {
            openAgentWindow(id, asTab: true)
            return true
        }
        // Only ⇧-click and ⌘-click open the agent window; a plain click
        // activates the tile and ⌘↩ / double-click open the focus view in
        // this window.
        if f.contains(.shift) || f.contains(.command) {
            openAgentWindow(id)
            return true
        }
        return false
    }

    /// ⌃-click / right-click: the agent's window commands plus the same
    /// actions as the palette's (move and rename open their popovers on
    /// the tile, stop / remove are undoable).
    private func tileMenu(_ id: String, model: AppModel) -> NSMenu? {
        guard let a = model.agent(id) else { return nil }
        let menu = NSMenu()
        func add(_ title: String, _ key: String = "", _ mods: NSEvent.ModifierFlags = [], _ action: @escaping @MainActor () -> Void) {
            let i = NSMenuItem(title: title, action: #selector(MenuAction.run), keyEquivalent: key)
            i.keyEquivalentModifierMask = mods
            let act = MenuAction(action)
            i.target = act
            i.representedObject = act
            menu.addItem(i)
        }
        add(book.handle(for: id) == nil ? "Open in New Window" : "Show Window", "\r", [.command, .option]) { [weak self] in self?.openAgentWindow(id) }
        add("Open in New Tab") { [weak self] in self?.openAgentWindow(id, asTab: true) }
        menu.addItem(.separator())
        // Moving work across Macs: one item per Mac it can go to (none
        // with an older hesperd there).
        for t in model.moveTargets(a) {
            add(MoveText.continueTitle(t.displayName)) { [weak model] in model?.requestMove(a, to: t.short) }
            add(MoveText.forkTitle(t.displayName)) { [weak model] in model?.requestMove(a, to: t.short, options: MoveOptions(fork: true)) }
        }
        add("Close", "w", [.command]) { [weak model] in model?.requestClose(a) }
        if a.isRunning {
            add("Send to Background", "w", [.command, .option]) { [weak model] in model?.sendToBackground(a) }
            add("Kill", "w", [.command, .control]) { [weak model] in model?.kill(a) }
        } else {
            add("Resume") { [weak model] in model?.resume(a) }
        }
        add("Rename…") { [weak model] in
            model?.select(id)
            model?.renameText[id] = a.name
            model?.openPopover(.rename(id))
        }
        // Its scratch: rename, keep, promote, archive, delete.
        if let pid = model.scratchID(of: a), !model.scratchActions(pid).isEmpty {
            menu.addItem(.separator())
            let item = NSMenuItem(title: "Scratch “\(model.scratchName(pid))”", action: nil, keyEquivalent: "")
            let sub = NSMenu(title: item.title)
            model.addScratchItems(pid, to: sub, window: wall(for: model)?.window)
            item.submenu = sub
            menu.addItem(item)
        }
        return menu
    }

    // MARK: Scope popover (the overlay system's popover, on the pill)

    func showScopePopover(for model: AppModel) {
        guard wall(for: model) != nil else { return }
        model.openPopover(.scope)
    }

    /// The scope popover's rows: All, Overflow, Filter, then the filter's
    /// values (⏎ toggles and closes, ⌘⏎ toggles and keeps it open).
    func scopeContent(for w: WallEntry) -> PopoverContent {
        let all = primary.unscopedWall
        let catalog = primary.catalog
        func count(_ s: WallScope) -> Int {
            distribution(all, catalog: catalog, override: (w, s)).agents[w.id]?.count ?? 0
        }
        var filter = WallFilter()
        if case .filter(let f) = w.scope { filter = f }
        var items: [OverlayItem] = []
        if let name = scopeName(w.scope) {
            items.append(OverlayItem(id: "current", title: "\(w.scope.title): \(name)", detail: "\(count(w.scope)) · ⌘0 sidebar picks another",
                                     checked: true, run: {}))
        }
        items += [
            OverlayItem(id: "all", title: "All", detail: "every agent · \(count(.all))", checked: w.scope == .all,
                        run: { [weak self, weak w] in if let w { self?.setScope(.all, for: w) } }),
            OverlayItem(id: "overflow", title: "Overflow", detail: "what earlier walls can't fit · \(count(.overflow))", checked: w.scope == .overflow,
                        run: { [weak self, weak w] in if let w { self?.setScope(.overflow, for: w) } }),
            OverlayItem(id: "filter", title: "Filter", detail: "\(filter.summary(names: { catalog.groups[$0]?.name ?? catalog.project($0)?.name ?? ($0 as NSString).lastPathComponent })) · \(count(.filter(filter)))", checked: w.scope.isFilter,
                        run: { [weak self, weak w] in if let w { self?.setScope(.filter(filter), for: w) } }),
        ]
        func toggle(_ section: String, _ value: String, _ title: String, _ on: Bool, _ change: @escaping (inout WallFilter) -> Void) {
            items.append(OverlayItem(id: "\(section):\(value)", section: section, title: "\(section): \(title)", checked: w.scope.isFilter && on,
                                     run: { [weak self, weak w] in
                                         guard let self, let w else { return }
                                         var f = WallFilter()
                                         if case .filter(let cur) = w.scope { f = cur }
                                         change(&f)
                                         self.setScope(.filter(f), for: w)
                                     }))
        }
        for g in catalog.orderedGroups {
            toggle("Group", g.id, g.name, filter.groups.contains(g.id)) { f in
                if f.groups.contains(g.id) { f.groups.remove(g.id) } else { f.groups.insert(g.id) }
            }
        }
        let projectIDs = Set(all.compactMap { catalog.root(catalog.projectID(for: $0)) })
        for p in projectIDs.sorted(by: { catalog.name(project: $0).lowercased() < catalog.name(project: $1).lowercased() }) {
            toggle("Project", p, catalog.name(project: p), filter.projects.contains(p)) { f in
                if f.projects.contains(p) { f.projects.remove(p) } else { f.projects.insert(p) }
            }
        }
        for m in Array(Set(all.map(\.machine) + primary.machines.map(\.short))).sorted() {
            toggle("Machine", m, m, filter.machines.contains(m)) { f in
                if f.machines.contains(m) { f.machines.remove(m) } else { f.machines.insert(m) }
            }
        }
        for k in Array(Set(all.map(\.kind) + ["claude", "codex"])).sorted() {
            toggle("Kind", k, Theme.kindLabel(k), filter.kinds.contains(k)) { f in
                if f.kinds.contains(k) { f.kinds.remove(k) } else { f.kinds.insert(k) }
            }
        }
        for g in StateGroup.allCases {
            toggle("State", g.rawValue, g.title, filter.states.contains(g)) { f in
                if f.states.contains(g) { f.states.remove(g) } else { f.states.insert(g) }
            }
        }
        if walls.first !== w {
            items.append(OverlayItem(id: "home", section: "Desk", title: "Make Home Wall", detail: "needs-you strip, overflow head",
                                     run: { [weak self, weak w] in if let w { self?.makeHome(w) } }))
        }
        return PopoverContent(title: "This wall shows", items: items, note: nil,
                              hints: [("⏎", "choose"), ("⌘⏎", "keep open"), ("esc", "close")])
    }

    var scopePopoverShown: Bool { walls.contains { $0.model.popover == .scope } }
    func closeScopePopover() { for w in walls where w.model.popover == .scope { w.model.closePopover() } }

    // MARK: Menus

    /// Adds the window commands to the app's menus (built by AppDelegate).
    func installMenus() {
        guard let main = NSApp.mainMenu else { return }
        func item(_ title: String, _ key: String, _ mods: NSEvent.ModifierFlags, _ action: @escaping @MainActor () -> Void) -> NSMenuItem {
            let i = NSMenuItem(title: title, action: #selector(MenuAction.run), keyEquivalent: key)
            i.keyEquivalentModifierMask = mods
            let act = MenuAction(action)
            i.target = act
            i.representedObject = act
            return i
        }
        if let agents = main.items.first(where: { $0.submenu?.title == "Agents" })?.submenu {
            let at = (agents.items.firstIndex { $0.title == "Open / Back to Wall" } ?? agents.items.count - 1) + 1
            agents.insertItem(item("Open in New Window", "\r", [.command, .option]) { [weak self] in
                guard let self, let id = self.activeWall.model.selectedID else { return }
                self.openAgentWindow(id)
            }, at: at)
            agents.insertItem(item("Open in New Tab", "", []) { [weak self] in
                guard let self, let id = self.activeWall.model.selectedID else { return }
                self.openAgentWindow(id, asTab: true)
            }, at: at + 1)
        }
        if let wm = NSApp.windowsMenu {
            wm.insertItem(item("New Wall Window", "n", [.command, .option]) { [weak self] in self?.newWall() }, at: 0)
            wm.insertItem(item("Project Sidebar", "0", [.command]) { [weak self] in
                guard let self else { return }
                self.activeWall.model.sidebarVisible.toggle()
            }, at: 0)
            wm.insertItem(item("Wall Scope…", "s", [.command, .control]) { [weak self] in
                guard let self else { return }
                let w = self.activeWall
                self.bringForward(w.window)
                self.showScopePopover(for: w.model)
            }, at: 1)
            wm.insertItem(item("Make Home Wall", "", []) { [weak self] in
                guard let self else { return }
                self.makeHome(self.activeWall)
            }, at: 2)
            wm.insertItem(.separator(), at: 3)
            desks.installMenus(in: wm, at: 4)
            wm.addItem(.separator())
            wm.addItem(withTitle: "Bring All to Front", action: #selector(NSApplication.arrangeInFront(_:)), keyEquivalent: "")
        }
    }
}

/// A closure as a menu item target.
@MainActor
final class MenuAction: NSObject {
    let action: @MainActor () -> Void
    init(_ a: @escaping @MainActor () -> Void) { action = a }
    @objc func run() { action() }
}
