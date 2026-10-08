import AppKit
import HesperCore

/// Shared history, app-wide (one per daemon connection; docs "As built —
/// shared history (app)"): whether hesperd has `sessions.*`, the counts,
/// the first index's progress, ghost cards, resume cards, and the
/// `sessions.changed` / `sessions.indexing` stream — decoded on the
/// connection's reader thread and handed to the main thread coalesced to
/// at most 4 updates a second, so History never costs the wall a frame.
@MainActor
final class HistoryHub {
    private static var hubs: [ObjectIdentifier: HistoryHub] = [:]

    /// The hub of a model's daemon connection (the window models share
    /// the primary's client, so they share its hub).
    static func shared(for model: AppModel) -> HistoryHub {
        let key = ObjectIdentifier(model.client)
        if let h = hubs[key] { return h }
        let h = HistoryHub(model: model)
        hubs[key] = h
        return h
    }

    let client: DaemonClient
    private weak var primary: AppModel?
    /// nil: not known yet (connecting); false: an older hesperd (History
    /// hidden: no menu item, ⌘Y beeps, no palette section, no ghosts).
    private(set) var supported: Bool?
    private(set) var stats = SessionStats()
    private(set) var indexing: IndexingProgress?
    /// The first index ran while the app watched (tests).
    private(set) var sawIndexing = false
    /// Sessions of removed agents (ghost cards), newest first.
    private(set) var removedSessions: [Session] = []
    /// Ghost cards forgotten (⌫), by ghost item id; remembered.
    private(set) var dismissed: Set<String>
    /// Ghost cards shown now (the wall's items read this).
    private(set) var ghosts: [Session] = []
    /// Agents resumed from History: their tile shows the left-off card
    /// until the CLI has drawn (agent id → session, when).
    private(set) var resumeCards: [String: (session: Session, changes: SessionChanges?, at: Date)] = [:]

    /// Last `sessions.*` latency per method (perf report).
    private(set) var lastLatencyMs: [String: Double] = [:]
    /// UI updates delivered from the event stream (perf: ≤ 4/s).
    private(set) var flushes: [TimeInterval] = []

    private let buffer = EventBuffer()
    private var observers: [UUID: @MainActor (Change) -> Void] = [:]
    private var expiryTimer: Timer?
    private var probing = false
    private static let dismissedKey = "historyDismissedGhosts"
    private let persist: Bool

    enum Change {
        case support, stats, indexing, ghosts
        /// Coalesced sessions.changed / sessions.removed.
        case sessions(changed: [Session], removed: [String])
        /// Back after an undone (or failed) delete.
        case restored(Session)
    }

    private init(model: AppModel) {
        client = model.client
        primary = model
        persist = !model.env.options.contains("ephemeral") && model.env.values["selftest-out"] == nil && model.env.values["perf-out"] == nil
        dismissed = persist ? Set(UserDefaults.standard.stringArray(forKey: Self.dismissedKey) ?? []) : []
        let buffer = self.buffer
        client.onOtherNotification = { method, params in
            // Reader thread: decode here, then one hop to main per slot.
            switch method {
            case "sessions.changed":
                if let s = params["session"].flatMap(Session.init(json:)) {
                    if params["session"]?["deleted"]?.boolValue == true { buffer.add(removed: s.id) } else { buffer.add(changed: s) }
                }
            case "sessions.removed":
                if let id = params["id"]?.stringValue { buffer.add(removed: id) }
            case "sessions.indexing":
                if let p = IndexingProgress(json: params) { buffer.add(indexing: p) }
            default:
                return
            }
            if let wait = buffer.schedule() {
                DispatchQueue.main.asyncAfter(deadline: .now() + wait) {
                    MainActor.assumeIsolated { HistoryHub.flushAll() }
                }
            }
        }
        observeChanges { [weak self] in
            guard let self, let m = self.primary else { return }
            if m.isConnected { self.connected() } else { self.supported = self.supported == false ? false : nil }
        }
    }

    private static func flushAll() { for h in hubs.values { h.flush() } }

    // MARK: Observers (panels, palettes, walls)

    @discardableResult
    func observe(_ f: @escaping @MainActor (Change) -> Void) -> UUID {
        let id = UUID()
        observers[id] = f
        return id
    }

    func unobserve(_ id: UUID?) { if let id { observers.removeValue(forKey: id) } }

    private func emit(_ c: Change) { for f in observers.values { f(c) } }

    // MARK: Support, stats, ghosts

    private func connected() {
        guard !probing else { return }
        probing = true
        Task { [weak self, client] in
            let r: Result<SessionStats, any Error> = await Task.detached { () -> Result<SessionStats, any Error> in
                do { return .success(try await client.sessionStats()) } catch { return .failure(error) }
            }.value
            guard let self else { return }
            self.probing = false
            switch r {
            case .success(let s):
                let was = self.supported
                self.supported = true
                self.applyStats(s)
                if was != true { self.emit(.support); await self.refreshGhosts() }
            case .failure(let e as RPCError) where e.code == -32601:
                self.supported = false
                self.emit(.support)
                self.updateGhosts()
            case .failure:
                break // not connected after all: the next connect probes again
            }
        }
    }

    private func applyStats(_ s: SessionStats) {
        stats = s
        if let p = s.indexing, !p.finished { indexing = p; sawIndexing = true } else { indexing = nil }
        emit(.stats)
    }

    func refreshStats() {
        guard supported == true else { return }
        Task { [weak self, client] in
            let s = await Task.detached { try? await client.sessionStats() }.value
            if let s { self?.applyStats(s) }
        }
    }

    private var lastStatsAsk: Date = .distantPast
    /// At most once a second (tests waiting for an index).
    func refreshStatsThrottled() {
        guard Date().timeIntervalSince(lastStatsAsk) > 1 else { return }
        lastStatsAsk = Date()
        refreshStats()
    }

    /// Removed agents of the last day (ghost cards).
    func refreshGhosts() async {
        guard supported == true else { return }
        let since = Session.format(Date().addingTimeInterval(-GhostCards.window))
        // The last day's sessions, 4 pages at most (hesperd: ≤ 50 a page);
        // new removals arrive as sessions.changed anyway.
        let items = await Task.detached { [client] () -> [Session]? in
            var out: [Session] = []
            var cursor: String?
            for _ in 0..<4 {
                var p: [String: JSONValue] = ["since": .string(since), "limit": 50]
                if let cursor { p["cursor"] = .string(cursor) }
                guard let page = try? SessionPage(json: await client.call("sessions.search", .object(p))) else { return out.isEmpty ? nil : out }
                out += page.items.filter { $0.removedAt != nil }
                guard let c = page.cursor else { break }
                cursor = c
            }
            return out
        }.value
        guard let items else { return }
        removedSessions = items
        updateGhosts()
    }

    var ghostCardsEnabled: Bool { primary?.settings.ghostCards ?? false }

    func updateGhosts() {
        let next = supported == true && ghostCardsEnabled ? GhostCards.visible(removedSessions, dismissed: dismissed) : []
        expiryTimer?.invalidate()
        if let at = GhostCards.nextExpiry(next) {
            expiryTimer = Timer.scheduledTimer(withTimeInterval: max(1, at.timeIntervalSinceNow + 1), repeats: false) { _ in
                MainActor.assumeIsolated { HistoryHub.flushAllGhosts() }
            }
        }
        guard next != ghosts else { return }
        ghosts = next
        emit(.ghosts)
        HistoryPanelHost.wallsChanged()
    }

    private static func flushAllGhosts() { for h in hubs.values { h.updateGhosts() } }

    func forgetGhost(_ itemID: String) {
        dismissed.insert(itemID)
        if persist { UserDefaults.standard.set(Array(dismissed.suffix(500)), forKey: Self.dismissedKey) }
        updateGhosts()
    }

    // MARK: The event stream (coalesced)

    private func flush() {
        let (changed, removed, idx) = buffer.drain()
        flushes.append(CACurrentMediaTime())
        if flushes.count > 400 { flushes.removeFirst(200) }
        if let idx {
            indexing = idx.finished ? nil : idx
            if !idx.finished { sawIndexing = true }
            emit(.indexing)
            if idx.finished { refreshStats() }
        }
        guard !changed.isEmpty || !removed.isEmpty else { return }
        var ghostsTouched = false
        for s in changed {
            if let i = removedSessions.firstIndex(where: { $0.id == s.id }) {
                if s.removedAt == nil || s.isLive { removedSessions.remove(at: i) } else { removedSessions[i] = s }
                ghostsTouched = true
            } else if s.removedAt != nil && !s.isLive {
                removedSessions.insert(s, at: 0)
                ghostsTouched = true
            }
        }
        if !removed.isEmpty {
            let n = removedSessions.count
            removedSessions.removeAll { removed.contains($0.id) }
            ghostsTouched = ghostsTouched || n != removedSessions.count
        }
        if ghostsTouched { updateGhosts() }
        emit(.sessions(changed: changed, removed: removed))
        refreshStatsSoon()
    }

    private var statsWork: DispatchWorkItem?
    private func refreshStatsSoon() {
        guard statsWork == nil else { return }
        let w = DispatchWorkItem { [weak self] in MainActor.assumeIsolated { self?.statsWork = nil; self?.refreshStats() } }
        statsWork = w
        DispatchQueue.main.asyncAfter(deadline: .now() + 1.5, execute: w)
    }

    /// Puts a session back into the lists (a failed delete).
    func emitChanged(_ s: Session) { emit(.restored(s)) }

    // MARK: Resume cards (the tile's first screen)

    func noteResumed(_ agentID: String, _ s: Session, changes: SessionChanges?) {
        resumeCards[agentID] = (s, changes, Date())
        // agents.changed may have made the tile already (before the reply).
        HistoryPanelHost.attachResumeCards(agentID)
        let old = resumeCards.filter { Date().timeIntervalSince($0.value.at) > 60 }.map(\.key)
        for k in old { resumeCards.removeValue(forKey: k) }
    }

    func takeResumeCard(_ agentID: String) -> (session: Session, changes: SessionChanges?)? {
        guard let c = resumeCards[agentID], Date().timeIntervalSince(c.at) < 30 else { return nil }
        return (c.session, c.changes)
    }

    func timed<T: Sendable>(_ method: String, _ body: @escaping @Sendable () async throws -> T) async throws -> T {
        let t0 = CACurrentMediaTime()
        let r = try await Task.detached { try await body() }.value
        lastLatencyMs[method] = (CACurrentMediaTime() - t0) * 1000
        return r
    }
}

/// The menu's History item (⌘Y): hidden while hesperd has no sessions.*;
/// the ghost card setting refreshes the walls.
@MainActor
enum HistoryMenu {
    static func track(_ item: NSMenuItem, model: AppModel) {
        let hub = HistoryHub.shared(for: model)
        item.isHidden = hub.supported != true
        hub.observe { [weak item] c in
            if case .support = c { item?.isHidden = hub.supported != true }
        }
        model.settings.onGhostCardsChanged = { hub.updateGhosts(); HistoryPanelHost.wallsChanged() }
    }
}

/// The reader thread's side: sessions.changed / removed / indexing piled
/// up between two main-thread flushes (≤ 4 a second).
final class EventBuffer: @unchecked Sendable {
    private let lock = NSLock()
    private var changed: [String: Session] = [:]
    private var order: [String] = []
    private var removed: Set<String> = []
    private var indexing: IndexingProgress?
    private var coalescer = UpdateCoalescer(maxPerSecond: 4)

    func add(changed s: Session) {
        lock.withLock {
            if changed[s.id] == nil { order.append(s.id) }
            changed[s.id] = s
            removed.remove(s.id)
        }
    }

    func add(removed id: String) {
        lock.withLock {
            removed.insert(id)
            if changed.removeValue(forKey: id) != nil { order.removeAll { $0 == id } }
        }
    }

    func add(indexing p: IndexingProgress) { lock.withLock { indexing = p } }

    /// nil: a flush is already on its way.
    func schedule() -> TimeInterval? {
        lock.withLock {
            switch coalescer.event(at: CACurrentMediaTime()) {
            case .flushNow: return 0
            case .scheduled(let after): return after
            case .alreadyScheduled: return nil
            }
        }
    }

    func drain() -> ([Session], [String], IndexingProgress?) {
        lock.withLock {
            coalescer.flushed(at: CACurrentMediaTime())
            let c = order.compactMap { changed[$0] }
            let r = Array(removed)
            let i = indexing
            changed = [:]; order = []; removed = []; indexing = nil
            return (c, r, i)
        }
    }
}
