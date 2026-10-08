import AppKit
import HesperCore
import Observation

/// A move waiting for one word from the user, inline on the agent's tile
/// (hesperd refused it before touching anything).
struct PendingMove: Equatable {
    var id: String
    var to: String
    var options: MoveOptions
    var ask: MovePreflight
}

/// Moving work across Macs, shared by every window's model (one per app):
/// what is moving where, how far it got, the question asked on a tile, and
/// what each Mac's hesperd supports.
@MainActor
@Observable
final class MoveBook {
    /// Moves in flight: agent id → target Mac.
    var targets: [String: String] = [:]
    /// Of those, the forks (the agent stays here).
    var forks: Set<String> = []
    /// agents.moving: how far each one got.
    var progress: [String: MoveProgress] = [:]
    /// A move asking first, on the agent's tile (in whichever window).
    var confirm: PendingMove?
    /// Per Mac: its hesperd has agents.move / checkpoints.restore (nil:
    /// not known yet). An older one hides the actions for its agents.
    @ObservationIgnored var supported: [String: Bool] = [:]
    @ObservationIgnored var restoreSupported: [String: Bool] = [:]
    /// The "needs a newer hesperd" toast was shown for these Macs.
    @ObservationIgnored var updateNoted: Set<String> = []
}

extension AppModel {
    var moveConfirm: PendingMove? {
        get { moveBook.confirm }
        set { moveBook.confirm = newValue }
    }

    /// Every window's walls redraw (the tiles' bands).
    func notifyMoving() { notifyClosing() }

    // MARK: What shows

    /// The Macs this agent can continue on (none: no move action shows).
    func moveTargets(_ a: Agent) -> [Machine] {
        MoveRules.targets(a, machines: machines, supported: moveBook.supported[a.machine], moving: moving[a.id] != nil)
    }

    /// The palette's (and ⌘J's) items for an agent: "Continue migrations
    /// on mini", "Fork migrations on mini".
    func moveActions(_ a: Agent) -> [OverlayItem] {
        moveTargets(a).flatMap { t in [
            OverlayItem(id: "move:\(a.id):\(t.short)", title: MoveText.continueTitle(t.displayName, name: a.name), detail: "⇧⌘M",
                        run: { [weak self] in self?.showPalette = false; self?.requestMove(a, to: t.short) }),
            OverlayItem(id: "fork:\(a.id):\(t.short)", title: MoveText.forkTitle(t.displayName, name: a.name),
                        run: { [weak self] in self?.showPalette = false; self?.requestMove(a, to: t.short, options: MoveOptions(fork: true)) }),
        ] }
    }

    // MARK: Moving

    /// "Continue on mini": agents.move. The tile shows the progress line
    /// until it leaves; then the new agent appears on mini, selected, and
    /// a toast "Moved migrations to mini · ⌘Z undo" (undo moves it back).
    /// A refusal asks on the tile (`PendingMove`).
    func requestMove(_ a: Agent, to m: String, options: MoveOptions = MoveOptions(), undoable: Bool = true) {
        closePopover()
        guard moving[a.id] == nil else { return }
        if moveConfirm?.id == a.id { moveConfirm = nil }
        if closeConfirm?.id == a.id { closeConfirm = nil }
        if mode != .focus && selectedID != a.id && wall.contains(where: { $0.id == a.id }) { select(a.id) }
        moving[a.id] = m
        if options.fork { moveBook.forks.insert(a.id) }
        moveBook.progress[a.id] = MoveProgress(id: a.id, to: m)
        notifyMoving()
        let target = machine(m)?.displayName ?? m
        let wasSelected = selectedID == a.id, wasFocused = mode == .focus && focusedID == a.id
        Task { @MainActor in
            do {
                let newID = try await self.client.move(a.id, to: m, options: options)
                self.moveEnded(a.id)
                self.moveBook.supported[a.machine] = true
                if options.fork {
                    let show = newID.map { id in ToastAction(title: "Show", run: { [weak self] in self?.goTo(id) }) }
                    self.showToast(MoveText.moved(a.name, to: target, fork: true), action: show)
                    return
                }
                if let newID {
                    if let p = self.placements.removeValue(forKey: a.id) { self.placements[newID] = p }
                    if self.selectedID == a.id || self.focusedID == a.id {
                        self.moved(a.id, to: newID)
                    } else if self.agent(a.id) == nil, self.selectedID != newID, self.moveFollow[newID] == nil, wasSelected || wasFocused {
                        // Gone here before the reply (agents.removed won the
                        // race): the selection follows anyway.
                        self.moveFollow[newID] = wasFocused
                        if self.agent(newID) != nil { self.followMoved(newID) }
                    }
                    if undoable { self.pushUndo(.moveBack(newID, to: a.machine), label: MoveText.moved(a.name, to: target)) }
                } else if undoable {
                    self.showToast(MoveText.moved(a.name, to: target))
                }
                self.onModeChanged?()
            } catch let e as RPCError where MoveRPC.missing(e) {
                self.moveEnded(a.id)
                self.moveBook.supported[a.machine] = false
                if !self.moveBook.updateNoted.contains(a.machine) {
                    self.moveBook.updateNoted.insert(a.machine)
                    self.showToast(MoveText.needsUpdate(self.machine(a.machine)?.displayName ?? a.machine), error: true)
                }
            } catch let e as RPCError {
                self.moveEnded(a.id)
                guard self.agent(a.id) != nil, let ask = MovePreflight(e, tool: a.kind) else { return }
                self.moveConfirm = PendingMove(id: a.id, to: m, options: options, ask: ask)
                self.notifyMoving()
            } catch {
                self.moveEnded(a.id)
                self.showToast("Could not move \(a.name): \(self.describe(error))", error: true)
            }
        }
    }

    /// Back-compat: ⇧⌘M's popover, undo.
    func move(_ a: Agent, to m: String, undoable: Bool = true) { requestMove(a, to: m, undoable: undoable) }

    /// ⏎ on the strip: go on with what it asked ("Interrupt and move",
    /// "Move anyway"); a message only: dismiss it.
    func confirmMove() {
        guard let p = moveConfirm else { return }
        moveConfirm = nil
        notifyMoving()
        guard let add = p.ask.primary()?.options, let a = agent(p.id) else { return }
        var o = p.options
        o.interrupt = o.interrupt || add.interrupt
        o.leaveProcesses = o.leaveProcesses || add.leaveProcesses
        requestMove(a, to: p.to, options: o)
    }

    /// Esc on the strip: nothing moves.
    func cancelMove() {
        guard moveConfirm != nil else { return }
        moveConfirm = nil
        notifyMoving()
    }

    private func moveEnded(_ id: String) {
        moving[id] = nil
        moveBook.forks.remove(id)
        moveBook.progress[id] = nil
        notifyMoving()
    }

    // MARK: Daemon events (AppModel.handle)

    /// agents.moving (the main model, once for the shared book).
    func moveProgressed(_ p: MoveProgress) {
        if moving[p.id] == nil, !p.to.isEmpty { moving[p.id] = p.to } // a move from hesperctl / another app
        var n = p
        if n.to.isEmpty { n.to = moving[p.id] ?? "" }
        moveBook.progress[p.id] = n
        notifyMoving()
    }

    /// The agent is gone (moved, closed): nothing left to show for it.
    func moveGone(_ id: String) {
        if moving[id] != nil || moveBook.progress[id] != nil {
            moving[id] = nil
            moveBook.progress[id] = nil
            moveBook.forks.remove(id)
        }
        if moveConfirm?.id == id { moveConfirm = nil }
    }

    /// `from` continues as `to` (agents.removed "moved", or agents.move's
    /// reply): this window's selection and focus follow it, now or when
    /// it arrives.
    func moved(_ from: String, to: String) {
        guard selectedID == from || focusedID == from || moveFollow[to] != nil else { return }
        let focused = focusedID == from || moveFollow[to] == true
        if agent(to) != nil {
            moveFollow[to] = nil
            if focused { focus(to) } else { select(to) }
        } else {
            moveFollow[to] = focused
        }
    }

    /// An agent arrived: the one a move continues as takes the selection.
    func followMoved(_ id: String) {
        guard let focused = moveFollow.removeValue(forKey: id) else { return }
        if focused { focus(id) } else { select(id) }
    }

    // MARK: Checkpoints (History)

    /// History's "Restore checkpoint": a gone agent's checkpoint as a
    /// worktree on its Mac again (checkpoints.restore). An older hesperd
    /// hides the action for that Mac.
    func restoreCheckpoint(_ s: Session) {
        guard let cp = s.checkpoint else { return }
        let mac = s.machine.isEmpty ? localMachine : s.machine
        let title = SessionFormat.oneLine(s.title, max: 40)
        Task { @MainActor in
            do {
                let path = try await self.client.restoreCheckpoint(session: s.id, checkpoint: cp, machine: s.machine.isEmpty ? nil : s.machine)
                self.moveBook.restoreSupported[mac] = true
                let whereText = path.map { " · \(SessionFormat.abbreviate($0))" } ?? ""
                var action: ToastAction?
                if let path, mac == self.localMachine {
                    action = ToastAction(title: "New agent there", run: { [weak self] in self?.newDraft(project: path) })
                }
                self.showToast("Restored the checkpoint of “\(title)”\(whereText)", action: action)
            } catch let e as RPCError where MoveRPC.missing(e) {
                self.moveBook.restoreSupported[mac] = false
                self.showToast("Restoring checkpoints needs a newer hesperd on \(self.machine(mac)?.displayName ?? mac)", error: true)
            } catch {
                self.showToast("Could not restore the checkpoint: \(self.describe(error))", error: true)
            }
        }
    }

    /// History's ⌘⏎ on a live session: its agent and the Mac it moves to.
    func liveMoveTarget(_ s: Session) -> (agent: Agent, to: String)? {
        guard let id = s.liveAgentID, let a = agent(id) else { return nil }
        guard let t = SearchPreview.moveTarget(s, agent: a, machines: machines, supported: moveBook.supported[a.machine]) else { return nil }
        return (a, t)
    }

    func restoreOffered(_ s: Session) -> Bool {
        SearchPreview.restoreOffered(s, supported: moveBook.restoreSupported[s.machine.isEmpty ? localMachine : s.machine])
    }
}
