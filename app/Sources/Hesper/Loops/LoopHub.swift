import AppKit
import HesperCore
import Observation

/// Loops, goals, milestones and the Steward, app-wide (one per daemon
/// connection, like ReviewHub). On a connect it lists `loops.list`,
/// `goals.list`, `steward.status` and `loops.settings.get`; after that
/// the `loops.*`, `goals.*` and `steward.*` notifications keep `store`
/// current. Notifications are decoded on the reader thread, batched and
/// applied on the main actor once per batch; `chips` and `onYouCount`
/// are rebuilt then, never per render.
///
/// Readers: the Loops, Wrap-up and Product sheets, the tiles' loop chip
/// (`chip(for:)`), the sidebar counts, the inbox and the composer. Writes
/// go through `run`, which calls hesperd off the main actor and returns
/// the error to show inline.
@MainActor
@Observable
final class LoopHub {
    private static var hubs: [ObjectIdentifier: LoopHub] = [:]

    /// The hub of a model's daemon connection (window models share the
    /// primary's client, so they share its hub).
    static func shared(for model: AppModel) -> LoopHub {
        let key = ObjectIdentifier(model.client)
        if let h = hubs[key] { return h }
        let h = LoopHub(client: model.client)
        hubs[key] = h
        return h
    }

    @ObservationIgnored let client: DaemonClient
    /// Every loop, goal and milestone, the Steward and the settings.
    private(set) var store = LoopStore()
    /// A chip for every active loop, by loop id.
    private(set) var chips: [String: LoopChip] = [:]
    /// Active loops that need the person (looks done and parked left out).
    private(set) var onYouCount = 0
    /// The sidebar's loop counts per project (ProjectLoops).
    private(set) var projectLoops = ProjectLoops()
    /// The first list arrived (false: still connecting; empty states wait).
    private(set) var loaded = false

    @ObservationIgnored private let inbox = LoopsInbox()
    @ObservationIgnored private var flushTask: Task<Void, Never>?
    @ObservationIgnored private var listTask: Task<Void, Never>?
    /// Events applied while a list is out: replayed on top of it.
    @ObservationIgnored private var replay: [LoopsEvent]?

    private init(client: DaemonClient) {
        self.client = client
        let key = ObjectIdentifier(client)
        let inbox = inbox
        client.onLoopsNotification = { method, params in
            // Reader thread: decode here, queue, and hop to main once per batch.
            guard let e = LoopsEvent.decode(method: method, params: params), inbox.push(e) else { return }
            DispatchQueue.main.async { MainActor.assumeIsolated { LoopHub.hubs[key]?.scheduleFlush() } }
        }
    }

    /// hesperd answers loops.* (false: an older hesperd; the sheets say so).
    var available: Bool { store.supported }
    var settings: LoopSettings { store.settings }
    var steward: StewardStatus { store.steward }
    func loop(_ id: String?) -> Loop? { id.flatMap { store.loops[$0] } }
    /// The chip of an agent's loop (nil: no loop, or not an active one).
    func chip(for agent: Agent) -> LoopChip? { agent.loop.flatMap { chips[$0] } }

    // MARK: Refresh

    /// A connect: list everything again.
    func connected() { refresh() }

    /// One list of every record (debounced: a list already out is kept).
    func refresh() {
        guard listTask == nil else { return }
        replay = []
        let client = client
        listTask = Task { [weak self] in
            let r = await Task.detached { await Self.list(client) }.value
            guard let self else { return }
            self.listTask = nil
            self.listed(r)
        }
    }

    private struct Listing: Sendable {
        var loops: Result<[Loop], any Error>
        var goals: GoalsList?
        var steward: StewardStatus?
        var settings: LoopSettings?
    }

    private nonisolated static func list(_ client: DaemonClient) async -> Listing {
        async let l: Result<[Loop], any Error> = { do { return .success(try await client.loopsList()) } catch { return .failure(error) } }()
        async let g = try? client.goalsList()
        async let s = try? client.stewardStatus()
        async let t = try? client.loopSettings()
        return await Listing(loops: l, goals: g, steward: s, settings: t)
    }

    private func listed(_ r: Listing) {
        let events = replay ?? []
        replay = nil
        switch r.loops {
        case .success(let list): store.listed(loops: list)
        case .failure(let e as RPCError) where LoopsRPC.missing(e): store.listed(loops: nil)
        case .failure: return // not connected, a timeout: keep what we have
        }
        if let g = r.goals { store.listed(goals: g) }
        if let s = r.steward { store.apply(.stewardChanged(s)) }
        if let t = r.settings { store.setSettings(t) }
        for e in events { store.apply(e) } // newer than the list
        loaded = true
        derive()
    }

    private func scheduleFlush() {
        guard flushTask == nil else { return }
        flushTask = Task { [weak self] in
            // A short wait gathers a burst (a merge, a snapshot) into one apply.
            try? await Task.sleep(nanoseconds: 120_000_000)
            guard let self else { return }
            self.flushTask = nil
            self.apply(self.inbox.drain())
        }
    }

    private func apply(_ events: [LoopsEvent]) {
        guard !events.isEmpty else { return }
        for e in events { store.apply(e) }
        replay?.append(contentsOf: events)
        derive()
    }

    private func derive() {
        // An acted-on loop that changed shows as it is now.
        for (id, at) in inboxActed where store.loops[id]?.lastChange != at { inboxActed[id] = nil }
        let c = LoopChip.all(store)
        if c != chips { chips = c }
        let n = LoopChip.onYouCount(store)
        if n != onYouCount { onYouCount = n }
        let p = ProjectLoops(loops: store.loops.values)
        if p != projectLoops { projectLoops = p }
    }

    // MARK: Inbox

    /// Loops acted on from the inbox (Yes, Later, Did it) and the change
    /// they showed then: hidden until the loop changes (the call's
    /// notification), shown again when the call fails.
    private(set) var inboxActed: [String: Date] = [:]
    @ObservationIgnored private var yourMoveMemo: (version: Int, excluding: Set<String>, acted: [String: Date], entries: [YourMoveEntry])?

    /// The inbox's "Your move" entries (YourMove.entries without the
    /// queue's agents and the loops just acted on), cached per store
    /// version: the panel asks on every render and key.
    func yourMove(excluding: Set<String>) -> [YourMoveEntry] {
        if let m = yourMoveMemo, m.version == store.version, m.excluding == excluding, m.acted == inboxActed { return m.entries }
        let acted = inboxActed
        let e = YourMove.entries(loops: store.loops.values, excluding: excluding).filter { acted[$0.loop.id] != $0.loop.lastChange }
        yourMoveMemo = (store.version, excluding, acted, e)
        return e
    }

    /// Opens a loop's detail (set by the Loops sheet; nil: the inbox's
    /// Open goes to the asking agent's tile instead).
    @ObservationIgnored var openLoop: ((String) -> Void)?

    /// Hides a loop's entry until the loop changes (nil: shows it again).
    func markActed(_ loop: Loop, _ on: Bool) {
        inboxActed[loop.id] = on ? loop.lastChange : nil
    }

    // MARK: Writes

    /// Runs a loops call off the main actor. A returned loop is applied at
    /// once (the notification agrees later); nil means it went through,
    /// else the text to show inline (`LoopsErrorText`).
    @discardableResult
    func run(_ body: @escaping @Sendable (DaemonClient) async throws -> Loop?) async -> String? {
        let client = client
        let r: Result<Loop?, any Error> = await Task.detached {
            do { return .success(try await body(client)) } catch { return .failure(error) }
        }.value
        switch r {
        case .success(let l):
            if let l { apply([.loopChanged(l)]) }
            return nil
        case .failure(let e):
            return LoopsErrorText.of(e)
        }
    }

    /// The Steward's record as steward.set returned it.
    func stewardSet(_ patch: StewardPatch) async -> String? {
        let client = client
        let r: Result<StewardStatus, any Error> = await Task.detached {
            do { return .success(try await client.stewardSet(patch)) } catch { return .failure(error) }
        }.value
        switch r {
        case .success(let s): apply([.stewardChanged(s)]); return nil
        case .failure(let e): return LoopsErrorText.of(e)
        }
    }

    // MARK: Fixtures

    /// A hub on no connection (offscreen renders).
    static func offscreen() -> LoopHub { LoopHub(client: DaemonClient(socketPath: "")) }

    /// Fixtures (offscreen renders, self-tests): the store as given.
    func showFixture(_ s: LoopStore) {
        store = s
        loaded = true
        derive()
    }
}

/// Decoded notifications between the reader thread and the main actor.
/// `push` is true when the queue was empty (the caller schedules one
/// flush); the queue is bounded, the oldest events go first (a list on
/// the next connect repairs the gap).
// @unchecked Sendable: every access to `queue` holds `lock`.
final class LoopsInbox: @unchecked Sendable {
    static let limit = 4096
    private let lock = NSLock()
    private var queue: [LoopsEvent] = []

    func push(_ e: LoopsEvent) -> Bool {
        lock.lock(); defer { lock.unlock() }
        queue.append(e)
        if queue.count > Self.limit { queue.removeFirst(queue.count - Self.limit) }
        return queue.count == 1
    }

    func drain() -> [LoopsEvent] {
        lock.lock(); defer { lock.unlock() }
        let q = queue
        queue = []
        return q
    }
}

// MARK: The wall model's hooks

extension AppModel {
    var loopHub: LoopHub { LoopHub.shared(for: self) }

    /// Every daemon event (marked integration point in `handle`): a
    /// connect lists the loops again.
    func loopSaw(_ e: DaemonEvent) {
        if case .connected = e { loopHub.connected() }
    }
}
