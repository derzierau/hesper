import AppKit
import HesperCore
import Observation

/// Routines, app-wide (one per daemon connection, like ReviewHub):
/// `routines.list` after a connect and on every `routines.changed` /
/// `routines.removed` (debounced), decoded off the main thread; the
/// selected routine's runs (`routines.runs`) fetched when it is selected
/// and again on its `routines.changed`. The panel, the menu item, the
/// palette section and the sidebar read it. A hesperd without
/// `routines.*` (-32601) hides every entry point, as History hides.
@MainActor
@Observable
final class RoutinesHub {
    private static var hubs: [ObjectIdentifier: RoutinesHub] = [:]

    static func shared(for model: AppModel) -> RoutinesHub {
        let key = ObjectIdentifier(model.client)
        if let h = hubs[key] { return h }
        let h = RoutinesHub(client: model.client)
        hubs[key] = h
        return h
    }

    @ObservationIgnored let client: DaemonClient
    private(set) var support = RoutinesSupport()
    /// Ranked (RoutineList): needs you, running, then by the next run.
    private(set) var routines: [Routine] = []
    /// Runs by routine id, newest first; only the routines shown so far.
    private(set) var runs: [String: [RoutineRun]] = [:]
    private(set) var loadingRuns: Set<String> = []
    /// `routines.list` calls answered (tests wait on it).
    private(set) var lists = 0
    @ObservationIgnored private var refreshTask: Task<Void, Never>?
    @ObservationIgnored private var again = false
    /// The routine whose runs follow its `routines.changed` (the selected one).
    @ObservationIgnored private var watched: String?
    @ObservationIgnored private var runsTasks: [String: Task<Void, Never>] = [:]
    @ObservationIgnored private var runsAgain: Set<String> = []

    private init(client: DaemonClient) {
        self.client = client
        let key = ObjectIdentifier(client)
        client.onRoutineNotification = { method, params in
            // Reader thread: the id only; the list comes debounced on main.
            let id = (method == RoutinesRPC.changed ? params["routine"]?["id"] : params["id"])?.stringValue
            DispatchQueue.main.async { MainActor.assumeIsolated { RoutinesHub.hubs[key]?.notified(id: id) } }
        }
    }

    var available: Bool { support.available }
    func routine(_ id: String?) -> Routine? { id.flatMap { id in routines.first { $0.id == id } } }

    // MARK: Refresh

    func connected() { scheduleRefresh(after: 0) }

    private func notified(id: String?) {
        scheduleRefresh()
        if let id, id == watched { refreshRuns(id) }
    }

    /// Debounced (a run beginning and ending, several routines changing at
    /// once): one routines.list.
    func scheduleRefresh(after delay: TimeInterval = 0.3) {
        if refreshTask != nil { again = true; return }
        let client = client
        refreshTask = Task { [weak self] in
            if delay > 0 { try? await Task.sleep(nanoseconds: UInt64(delay * 1_000_000_000)) }
            let r: Result<[Routine], any Error> = await Task.detached {
                do { return .success(try await client.routinesList()) } catch { return .failure(error) }
            }.value
            guard let self else { return }
            self.refreshTask = nil
            self.applied(r)
            if self.again {
                self.again = false
                self.scheduleRefresh()
            }
        }
    }

    private func applied(_ r: Result<[Routine], any Error>) {
        switch r {
        case .success(let list):
            support.local = true
            lists += 1
            let next = RoutineList.ranked(list)
            if next != routines { routines = next }
        case .failure(let e as RPCError) where RoutinesRPC.missing(e):
            support.local = false // an older hesperd: no routines
            routines = []
        case .failure:
            break // not connected, a timeout: keep what we have
        }
    }

    // MARK: Runs

    /// The selected routine: its runs now, and again on its routines.changed.
    func watch(_ id: String?) {
        watched = id
        if let id, runs[id] == nil { refreshRuns(id) }
    }

    func refreshRuns(_ id: String) {
        if runsTasks[id] != nil { runsAgain.insert(id); return }
        let client = client
        loadingRuns.insert(id)
        runsTasks[id] = Task { [weak self] in
            // Up to keepRuns (≤ 1000); hesperd's default would be 50.
            let r: Result<[RoutineRun], any Error> = await Task.detached {
                do { return .success(try await client.routinesRuns(id, limit: 1000)) } catch { return .failure(error) }
            }.value
            guard let self else { return }
            self.runsTasks[id] = nil
            self.loadingRuns.remove(id)
            switch r {
            case .success(let list): if self.runs[id] != list { self.runs[id] = list }
            case .failure(let e as RPCError) where e.dataCode == "not_found": self.runs[id] = nil
            case .failure: break
            }
            if self.runsAgain.remove(id) != nil { self.refreshRuns(id) }
        }
    }

    // MARK: Actions (the panel's; each off the main thread)

    func runNow(_ id: String) async -> Result<RoutineRun?, any Error> {
        let client = client
        return await Task.detached {
            do { return .success(try await client.routinesRun(id)) } catch { return .failure(error) }
        }.value
    }

    func pause(_ id: String, paused: Bool) async -> Result<Routine?, any Error> {
        let client = client
        let r: Result<Routine?, any Error> = await Task.detached {
            do { return .success(try await client.routinesPause(id, paused: paused)) } catch { return .failure(error) }
        }.value
        if case .success(let r?) = r { replace(r) }
        return r
    }

    func remove(_ id: String) async -> Result<Void, any Error> {
        let client = client
        let r: Result<Void, any Error> = await Task.detached {
            do { try await client.routinesRemove(id); return .success(()) } catch { return .failure(error) }
        }.value
        if case .success = r {
            routines.removeAll { $0.id == id } // gone now; the next list agrees
            runs[id] = nil
        }
        return r
    }

    /// A call answered with the routine: shown at once (the notification
    /// confirms it).
    private func replace(_ r: Routine) {
        var list = routines
        if let i = list.firstIndex(where: { $0.id == r.id }) { list[i] = r } else { list.append(r) }
        let next = RoutineList.ranked(list)
        if next != routines { routines = next }
    }
}

// MARK: The wall model's hooks

extension AppModel {
    var routinesHub: RoutinesHub { RoutinesHub.shared(for: self) }
    /// hesperd has routines.* (the entry points show; else hidden).
    var routinesAvailable: Bool { routinesHub.available }

    /// ⌘⇧R, the menu: open the panel (on `select`), or close it.
    func toggleRoutines(select: String? = nil) {
        prepareForRoutines()
        RoutinesPanelHost.toggle(self, select: select)
    }

    /// The panel open on `id` (never closes it): ⌘K, the sidebar, hesperctl.
    func openRoutines(select id: String?) {
        prepareForRoutines()
        RoutinesPanelHost.show(self, select: id)
    }

    private func prepareForRoutines() {
        if mode == .compose { leaveComposer() }
        closePopover()
        showPalette = false
        if HistoryPanelHost.isOpen(self) { HistoryPanelHost.panel(for: self)?.close() }
        if ReviewPanelHost.isOpen(self) { ReviewPanelHost.panel(for: self)?.close() }
    }

    /// Every daemon event (marked integration point in `handle`): a connect
    /// lists the routines.
    func routinesSaw(_ e: DaemonEvent) {
        if case .connected = e { routinesHub.connected() }
    }

    /// The palette's "Routines" section: every routine (ranked), ⏎ opens
    /// the panel on it.
    func routinesPaletteItems() -> [OverlayItem] {
        guard routinesAvailable, paletteScope == nil else { return [] }
        let names = MachineLabel.names(machines)
        let now = Date()
        return routinesHub.routines.map { r in
            let mac = r.machine.isEmpty ? localMachine : r.machine
            return OverlayItem(id: "routine:\(r.id)", section: "Routines", title: r.name,
                               detail: [names[mac] ?? mac, RoutineText.cadence(r), RoutineText.nextRun(r, now: now), RoutineText.lastRun(r, now: now)].joined(separator: " · "),
                               stateMark: RoutineLook.mark(r.lastRun),
                               run: { [weak self] in self?.showPalette = false; self?.openRoutines(select: r.id) })
        }
    }
}

/// The menu's Routines item (⌘⇧R): hidden while hesperd has no routines.*.
@MainActor
enum RoutinesMenu {
    static func track(_ item: NSMenuItem, model: AppModel) {
        let hub = RoutinesHub.shared(for: model)
        observeChanges { [weak item] in item?.isHidden = !hub.available }
    }
}
