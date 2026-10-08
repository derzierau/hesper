import AppKit
import HesperCore
import Observation

/// A close waiting for one word from the user, inline on the agent's tile.
struct PendingClose: Equatable {
    var id: String
    var ask: CloseConfirm
    /// Confirms already given for this close.
    var acks: CloseAcks
}

/// Closing agents, shared by every window's model (one per app): what is
/// closing, what went to the background, the reopen stack, the queue for
/// offline Macs, and what this hesperd supports.
@MainActor
@Observable
final class CloseBook {
    /// Closed, not gone from hesperd's agents yet (or queued): on no wall.
    var closing: Set<String> = []
    /// The background flag until hesperd's agent agrees (optimistic).
    var backgroundOverride: [String: Bool] = [:]
    /// hesperd has no agents.background: the app keeps these off the walls.
    var localBackground: Set<String> = []
    /// Killed through agents.stop (hesperd has no agents.kill).
    var localKilled: Set<String> = []
    var reopen = ReopenStack()
    /// Closes waiting for their Mac to come back (agent id → record).
    var queued: [String: ClosedAgent] = [:]
    @ObservationIgnored var inFlight: Set<String> = []
    /// Per Mac (an updated laptop and an older mini differ).
    @ObservationIgnored var closeSupported: [String: Bool] = [:]
    @ObservationIgnored var killSupported: [String: Bool] = [:]
    @ObservationIgnored var backgroundSupported: [String: Bool] = [:]
    /// The "needs an update" toast was shown (once per launch).
    @ObservationIgnored var updateNoted = false
    @ObservationIgnored var autoTidyTimer: Timer?
    @ObservationIgnored var autoTidyAt: Date?
    /// Auto-tidy leaves these (uncommitted work in their worktree).
    @ObservationIgnored var autoTidySkip: Set<String> = []
    @ObservationIgnored var retryTimer: Timer?
    /// Every wall redraws (the window layer sets it).
    @ObservationIgnored var onChange: (() -> Void)?

    /// How often queued closes are tried again.
    static let retryInterval: TimeInterval = 30
}

extension AppModel {
    /// What a daemon event changed for closing: the agent before it.
    struct ClosingBefore { var agent: Agent? }

    func closingBefore(_ e: DaemonEvent) -> ClosingBefore? {
        switch e {
        case .changed(let a): return ClosingBefore(agent: registry.agents[a.id])
        case .removed(let id, _): return ClosingBefore(agent: registry.agents[id])
        default: return nil
        }
    }

    /// The main model only (once per event for the shared book).
    func closingAfter(_ e: DaemonEvent, before: ClosingBefore?) {
        switch e {
        case .changed(let a):
            if let o = closeBook.backgroundOverride[a.id], o == a.background { closeBook.backgroundOverride[a.id] = nil }
            // Background on this Mac only (older hesperd): the app closes
            // it when it finishes, as hesperd would.
            if closeBook.localBackground.contains(a.id), BackgroundTray.justFinished(a, from: before?.agent?.state) {
                closeBook.localBackground.remove(a.id)
                close(a, quiet: true)
                onFinishedInBackground?(a.name)
            }
            if closeBook.queued.values.contains(where: { $0.machine == a.machine }) { retryQueued(machine: a.machine) }
            scheduleAutoTidy()
        case .removed(let id, let reason):
            let was = closeBook.closing.contains(id)
            closeBook.closing.remove(id)
            closeBook.localBackground.remove(id)
            closeBook.backgroundOverride[id] = nil
            closeBook.localKilled.remove(id)
            closeBook.queued[id] = nil
            if RemovalReason(rawValue: reason ?? "") == .finishedInBackground, let a = before?.agent {
                closeBook.reopen.push(ClosedAgent(a))
                onFinishedInBackground?(a.name)
            }
            if was { notifyClosing() }
        case .connected:
            retryQueued(machine: nil)
            scheduleAutoTidy()
        default:
            break
        }
    }

    /// Every window's walls redraw.
    func notifyClosing() {
        onAgentsChanged?()
        closeBook.onChange?()
    }

    // MARK: Places

    /// On no wall: running in the background (hesperd's flag, this app's
    /// optimistic one, or this Mac's alone with an older hesperd).
    func isBackground(_ a: Agent) -> Bool {
        if let o = closeBook.backgroundOverride[a.id] { return o }
        return a.background || closeBook.localBackground.contains(a.id)
    }

    func isKilled(_ a: Agent) -> Bool { a.isKilled || (!a.isRunning && closeBook.localKilled.contains(a.id)) }

    /// The tray's agents: needing you first.
    var backgroundAgents: [Agent] {
        BackgroundTray.agents(registry.agents.values.filter { !pendingRemoval.contains($0.id) }, isBackground: isBackground)
    }

    var backgroundSummary: BackgroundSummary {
        BackgroundTray.summary(registry.agents.values.filter { !pendingRemoval.contains($0.id) }, isBackground: isBackground)
    }

    /// The agent leaves this wall: the selection moves to its neighbor.
    private func leaveWall(_ id: String, _ change: () -> Void) {
        let items = wallItems
        let i = items.firstIndex { $0.id == id }
        change()
        if activeTileID == id { activeTileID = nil }
        if selectedID == id, let i {
            let rest = items.filter { $0.id != id }
            selectedID = rest.isEmpty ? nil : rest[min(i, rest.count - 1)].id
        }
        if closeConfirm?.id == id { closeConfirm = nil }
        notifyClosing()
        onModeChanged?()
    }

    /// In focus: what follows when the focused agent leaves (the next
    /// agent, the wall after the last one; an agent window closes).
    private func followUp(for id: String) -> (@MainActor () -> Void)? {
        guard mode == .focus, focusedID == id else { return nil }
        if let c = closeInFocus { return { c(id) } }
        let order = projectStepOrder(wall.map(\.id))
        let rest = order.filter { $0 != id }
        guard let i = order.firstIndex(of: id), !rest.isEmpty else { return { [weak self] in self?.exitFocus() } }
        let next = rest[i % rest.count]
        return { [weak self] in self?.focus(next) }
    }

    // MARK: ⌘W close

    /// ⌘W: asks first where closing would lose something (an answer it
    /// waits for, a running shell command, uncommitted work), else closes.
    func requestClose(_ a: Agent, acks: CloseAcks = [], changes: Int? = nil) {
        closePopover()
        let checkable = a.machine == localMachine && !(a.worktree ?? "").isEmpty
        switch CloseRules.decide(isDraft: false, agent: a, acks: acks, worktreeCheckable: checkable, worktreeChanges: changes) {
        case .confirm(let ask):
            if mode != .focus && selectedID != a.id { select(a.id) }
            closeConfirm = PendingClose(id: a.id, ask: ask, acks: acks)
        case .checkWorktree:
            let wt = a.worktree ?? ""
            Task { @MainActor in
                let n = await Self.uncommittedCount(wt)
                guard let now = self.agent(a.id), !self.pendingRemoval.contains(a.id) else { return }
                self.requestClose(now, acks: acks, changes: n)
            }
        case .close:
            if closeConfirm?.id == a.id { closeConfirm = nil }
            close(a)
        case .discardDraft, .nothing:
            break
        }
    }

    /// ⏎ on the strip: close anyway (the worktree: keep it); `discard`:
    /// the worktree's "Discard".
    func confirmClose(discardWorktree: Bool = false) {
        guard let p = closeConfirm, let a = agent(p.id) else { closeConfirm = nil; return }
        closeConfirm = nil
        if case .worktree = p.ask {
            close(a, discardWorktree: discardWorktree)
        } else {
            requestClose(a, acks: p.acks.union(p.ask.kind))
        }
    }

    /// Esc on the strip: nothing happens.
    func cancelClose() { closeConfirm = nil }

    /// Closes now: off every wall at once, agents.close (graceful) sent;
    /// a 6 s toast "Closed api · ⌘Z undo · ⌘⇧T reopen" (`quiet`: none, the
    /// caller makes one). Undo and ⌘⇧T resume its session.
    @discardableResult
    func close(_ a: Agent, discardWorktree: Bool = false, quiet: Bool = false) -> ClosedAgent {
        let rec = ClosedAgent(a, band: resolvedView.band(of: a.id)?.key)
        let follow = followUp(for: a.id)
        leaveWall(a.id) { closeBook.closing.insert(a.id) }
        closeBook.inFlight.insert(a.id)
        closeBook.reopen.push(rec)
        let undoID = quiet ? nil : pushUndo(.reopen([rec]), label: CloseText.closed(a.name))
        follow?()
        Task { @MainActor in
            defer { self.closeBook.inFlight.remove(a.id) }
            do {
                if let s = try await self.sendClose(a), s != rec.session { self.closedSession(a.id, s) }
                if discardWorktree, let wt = a.worktree, a.machine == self.localMachine { await Self.discardWorktree(wt) }
            } catch let e as RPCError where e.code == CloseRPC.machineOffline {
                self.closeBook.queued[a.id] = rec
                self.scheduleRetry()
                let label = CloseText.closed(a.name, queuedFor: self.machine(a.machine)?.displayName ?? a.machine)
                if let undoID {
                    self.undo.relabel(undoID, label)
                    self.undoToast = self.undo.top()
                } else if !quiet {
                    self.showToast(label)
                }
            } catch {
                self.closeBook.closing.remove(a.id)
                self.closeBook.reopen.remove(agentID: a.id)
                if let undoID { self.undo.drop(undoID); self.undoToast = self.undo.top() }
                self.notifyClosing()
                self.showToast("Could not close \(a.name): \(self.describe(error))", error: true)
            }
        }
        return rec
    }

    /// agents.close; an older hesperd (no method): agents.stop, then
    /// agents.remove once it ended (today's two steps, as one).
    func sendClose(_ a: Agent) async throws -> String? {
        if closeBook.closeSupported[a.machine] != false {
            do {
                let s = try await client.closeAgent(a.id)
                closeBook.closeSupported[a.machine] = true
                return s
            } catch let e as RPCError where CloseRPC.missing(e) {
                closeBook.closeSupported[a.machine] = false
            }
        }
        if agent(a.id)?.isRunning ?? a.isRunning {
            try await client.stopAgent(a.id)
            _ = await Self.waitUntil(8) { [weak self] in self?.agent(a.id)?.isRunning != true }
        }
        if agent(a.id) != nil { try await client.remove(a.id) }
        return nil
    }

    /// hesperd named the closed agent's History session.
    private func closedSession(_ agentID: String, _ session: String) {
        closeBook.reopen.update(agentID: agentID, session: session)
        undo.mapActions { action in
            guard case .reopen(var rs) = action else { return action }
            for i in rs.indices where rs[i].agentID == agentID { rs[i].session = session }
            return .reopen(rs)
        }
    }

    // MARK: ⌃⌘W kill

    /// Hard stop now; the pane stays ("Killed", Resume ⏎ · Close ⌘W).
    func kill(_ a: Agent) {
        closePopover()
        guard a.isRunning else { showToast("\(a.name) has already ended"); return }
        Task { @MainActor in
            do {
                if self.closeBook.killSupported[a.machine] != false {
                    do {
                        try await self.client.killAgent(a.id)
                        self.closeBook.killSupported[a.machine] = true
                        return
                    } catch let e as RPCError where CloseRPC.missing(e) {
                        self.closeBook.killSupported[a.machine] = false
                    }
                }
                self.closeBook.localKilled.insert(a.id)
                try await self.client.stopAgent(a.id)
            } catch {
                self.closeBook.localKilled.remove(a.id)
                self.showToast("Could not kill \(a.name): \(self.describe(error))", error: true)
            }
        }
    }

    // MARK: ⌥⌘W background

    /// Off the wall, still running; the toolbar's tray lists it, ⌘J and
    /// notifications still bring it when it needs you.
    func sendToBackground(_ a: Agent) {
        closePopover()
        guard a.isRunning else { showToast("\(a.name) has ended: close it instead (⌘W)"); return }
        let follow = followUp(for: a.id)
        leaveWall(a.id) { closeBook.backgroundOverride[a.id] = true }
        follow?()
        Task { @MainActor in await self.sendBackground(a, true) }
    }

    /// Back onto its wall (background off).
    func bringBack(_ a: Agent) {
        closeBook.backgroundOverride[a.id] = false
        closeBook.localBackground.remove(a.id)
        notifyClosing()
        Task { @MainActor in await self.sendBackground(a, false) }
    }

    /// Opening a background agent (⌘J, a notification, the menu bar)
    /// brings it back first.
    func bringBackIfNeeded(_ id: String) {
        if let a = agent(id), isBackground(a) { bringBack(a) }
    }

    private func sendBackground(_ a: Agent, _ on: Bool) async {
        do {
            if closeBook.backgroundSupported[a.machine] == false { throw RPCError(code: CloseRPC.noMethod, message: "no agents.background", kind: .remote) }
            try await client.setBackground(a.id, on)
            closeBook.backgroundSupported[a.machine] = true
            // The override stays until agents.changed agrees.
            if agent(a.id)?.background == on { closeBook.backgroundOverride[a.id] = nil }
        } catch let e as RPCError where CloseRPC.missing(e) {
            closeBook.backgroundSupported[a.machine] = false
            closeBook.backgroundOverride[a.id] = nil
            if on { closeBook.localBackground.insert(a.id) } else { closeBook.localBackground.remove(a.id) }
            if on && !closeBook.updateNoted {
                closeBook.updateNoted = true
                showToast("\(a.name) is in the background in this app only: \(CloseText.needsUpdate)")
            }
            notifyClosing()
        } catch {
            closeBook.backgroundOverride[a.id] = nil
            notifyClosing()
            showToast("Could not \(on ? "send \(a.name) to the background" : "bring \(a.name) back"): \(describe(error))", error: true)
        }
    }

    // MARK: The background tray (toolbar popover)

    /// ⏎ / a click on a tray row: back on its wall, selected (a needs-you
    /// one opens like ⌘J).
    func bringBackFromTray(_ a: Agent) {
        closePopover()
        bringBack(a)
        if a.state.needsAttention { openFromQueue(a) } else { activate(a.id) }
    }

    /// ⌘W in the tray: close the selected row's agent.
    func closeFromTray() -> Bool {
        let list = backgroundAgents
        guard !list.isEmpty else { closePopover(); return true }
        let i = popoverList.selection(enabled: list.map { _ in true }) ?? 0
        close(list[min(i, list.count - 1)])
        if backgroundAgents.isEmpty { closePopover() }
        return true
    }

    // MARK: ⇧⌘W tidy up

    /// Closes the finished agents (done, idle, exited) of the focused band,
    /// or of the whole wall; never working or needing you. One toast, one
    /// undo for all.
    func tidyUp() {
        closePopover()
        let v = resolvedView
        let band = v.showsBands ? focusedBandKey : nil
        let list = TidyUp.select(wall, band: band, bandOf: { v.band(of: $0.id)?.key })
        guard !list.isEmpty else {
            showToast(band == nil ? "Nothing finished to tidy up" : "Nothing finished in this band")
            return
        }
        let recs = list.map { close($0, quiet: true) }
        pushUndo(.reopen(recs), label: CloseText.tidied(list.map(\.name)))
    }

    // MARK: ⌘⇧T reopen, undo

    /// The most recently closed agent, resumed on its Mac, in its band.
    func reopenLast() {
        guard let top = closeBook.reopen.pop() else { showToast("Nothing closed to reopen"); return }
        // One way back: its own undo entry goes too.
        for e in undo.entries {
            if case .reopen(let rs) = e.action, rs.count == 1, rs[0].agentID == top.agentID { undo.drop(e.id) }
        }
        undoToast = undo.top()
        reopen([top])
    }

    /// Undo of a close: a queued one never left; else sessions.resume of
    /// its session (after hesperd finished closing it).
    func reopen(_ recs: [ClosedAgent]) {
        for r in recs {
            closeBook.reopen.remove(agentID: r.agentID)
            if closeBook.queued.removeValue(forKey: r.agentID) != nil {
                closeBook.closing.remove(r.agentID)
                notifyClosing()
                select(r.agentID)
                continue
            }
            Task { @MainActor in
                if self.closeBook.inFlight.contains(r.agentID) {
                    _ = await Self.waitUntil(12) { [weak self] in self?.closeBook.inFlight.contains(r.agentID) != true }
                }
                if self.agent(r.agentID) != nil {
                    // Still here (the close failed or is slow): just show it again.
                    self.closeBook.closing.remove(r.agentID)
                    self.notifyClosing()
                    self.select(r.agentID)
                    return
                }
                guard let sid = r.session else {
                    self.showToast("\(r.name) had no conversation to resume", error: true)
                    return
                }
                do {
                    switch try await self.client.resumeSession(sid, machine: nil) {
                    case .live(let id):
                        if let id { self.goTo(id) }
                    case .agent(let n, let note):
                        if let note { self.showToast(note) }
                        _ = await Self.waitUntil(5) { [weak self] in self?.agent(n.id) != nil }
                        if self.agent(n.id) != nil { self.select(n.id) }
                    }
                } catch {
                    if case .live(let id?) = SessionStart.live(from: error) { self.goTo(id); return }
                    self.showToast("Could not reopen \(r.name): \(self.describe(error))", error: true)
                }
            }
        }
    }

    // MARK: Offline Macs

    func scheduleRetry() {
        guard closeBook.retryTimer == nil else { return }
        closeBook.retryTimer = Timer.scheduledTimer(withTimeInterval: CloseBook.retryInterval, repeats: true) { [weak self] _ in
            MainActor.assumeIsolated { self?.retryQueued(machine: nil) }
        }
    }

    /// Queued closes, again (the Mac is back: an agent of it changed, the
    /// connection came back, or the timer).
    func retryQueued(machine: String?) {
        if closeBook.queued.isEmpty {
            closeBook.retryTimer?.invalidate()
            closeBook.retryTimer = nil
            return
        }
        for r in closeBook.queued.values where (machine == nil || r.machine == machine) && !closeBook.inFlight.contains(r.agentID) {
            guard let a = agent(r.agentID) else { closeBook.queued[r.agentID] = nil; continue }
            closeBook.inFlight.insert(r.agentID)
            Task { @MainActor in
                defer { self.closeBook.inFlight.remove(r.agentID) }
                do {
                    if let s = try await self.sendClose(a) { self.closedSession(a.id, s) }
                    self.closeBook.queued[r.agentID] = nil
                } catch let e as RPCError where e.code == CloseRPC.machineOffline {
                    // Still away.
                } catch {
                    self.closeBook.queued[r.agentID] = nil
                    self.closeBook.closing.remove(r.agentID)
                    self.notifyClosing()
                    self.showToast("Could not close \(a.name): \(self.describe(error))", error: true)
                }
            }
        }
    }

    // MARK: Auto-tidy (Settings › Agents, off by default)

    func scheduleAutoTidy() {
        guard !isMirror else { return }
        guard settings.autoTidy else {
            closeBook.autoTidyTimer?.invalidate()
            closeBook.autoTidyTimer = nil
            closeBook.autoTidyAt = nil
            return
        }
        let at = AutoTidy.nextDue(unscopedWall, now: Date(), exclude: closeBook.autoTidySkip)
        guard at != closeBook.autoTidyAt else { return }
        closeBook.autoTidyTimer?.invalidate()
        closeBook.autoTidyAt = at
        guard let at else { closeBook.autoTidyTimer = nil; return }
        closeBook.autoTidyTimer = Timer.scheduledTimer(withTimeInterval: max(1, at.timeIntervalSinceNow + 0.5), repeats: false) { [weak self] _ in
            MainActor.assumeIsolated { self?.runAutoTidy() }
        }
    }

    private func runAutoTidy() {
        closeBook.autoTidyAt = nil
        guard settings.autoTidy else { return }
        for a in AutoTidy.due(unscopedWall, now: Date(), exclude: closeBook.autoTidySkip) {
            if a.machine == localMachine, let wt = a.worktree, !wt.isEmpty {
                // Never closes over uncommitted work without asking.
                closeBook.autoTidySkip.insert(a.id)
                Task { @MainActor in
                    if await Self.uncommittedCount(wt) == 0, let now = self.agent(a.id), CloseRules.isFinished(now), !self.isBackground(now) {
                        self.close(now, quiet: true)
                    }
                }
            } else {
                close(a, quiet: true)
            }
        }
        scheduleAutoTidy()
    }

    // MARK: Worktrees

    /// Files with uncommitted changes in a worktree on this Mac (0: clean
    /// or not there).
    nonisolated static func uncommittedCount(_ dir: String) async -> Int {
        await Task.detached {
            guard let out = git(["-C", dir, "status", "--porcelain", "--untracked-files=normal"]) else { return 0 }
            return out.split(separator: "\n").filter { !$0.isEmpty }.count
        }.value
    }

    /// "Discard": the agent's own (linked) worktree is removed with its
    /// changes; the branch and its commits stay. Never the main checkout.
    nonisolated static func discardWorktree(_ dir: String) async {
        await Task.detached {
            guard let gitDir = git(["-C", dir, "rev-parse", "--absolute-git-dir"])?.trimmingCharacters(in: .whitespacesAndNewlines),
                  let common = git(["-C", dir, "rev-parse", "--path-format=absolute", "--git-common-dir"])?.trimmingCharacters(in: .whitespacesAndNewlines),
                  !gitDir.isEmpty, !common.isEmpty, gitDir != common else { return }
            _ = git(["--git-dir=\(common)", "worktree", "remove", "--force", dir])
        }.value
    }

    nonisolated private static func git(_ args: [String]) -> String? {
        let p = Process()
        p.executableURL = URL(fileURLWithPath: "/usr/bin/git")
        p.arguments = args
        let out = Pipe()
        p.standardOutput = out
        p.standardError = FileHandle.nullDevice
        guard (try? p.run()) != nil else { return nil }
        let data = out.fileHandleForReading.readDataToEndOfFile()
        p.waitUntilExit()
        guard p.terminationStatus == 0 else { return nil }
        return String(decoding: data, as: UTF8.self)
    }

    static func waitUntil(_ seconds: Double, _ ok: @escaping @MainActor () -> Bool) async -> Bool {
        let end = Date().addingTimeInterval(seconds)
        while Date() < end {
            if ok() { return true }
            try? await Task.sleep(nanoseconds: 100_000_000)
        }
        return ok()
    }
}
