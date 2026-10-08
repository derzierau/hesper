import AppKit
import HesperCore

/// What History's keys do (the panel, ghost cards, ⌘K). Every call runs
/// off the main thread; a started agent joins its project's band like any
/// new agent and gets the left-off card as its tile's first screen.
@MainActor
struct HistoryActions {
    let model: AppModel
    let hub: HistoryHub

    /// ⏎ (where it ran) / ⌥⏎ (here: moves ownership to this Mac). A live
    /// session opens its agent instead, never a second process.
    func resume(_ s: Session, here: Bool, changes: SessionChanges?, done: @escaping @MainActor () -> Void = {}) {
        resume(s, on: here ? model.localMachine : nil, changes: changes, done: done)
    }

    /// Resume on `machine` (nil: where it ran). Another machine than the
    /// session's home moves it there (⌘⏎ "continue on the other Mac"):
    /// hesperd packs the branch, uncommitted work and the transcript.
    func resume(_ s: Session, on machine: String?, changes: SessionChanges?, failed: @escaping @MainActor () -> Void = {},
                done: @escaping @MainActor () -> Void = {}) {
        if let id = s.liveAgentID, model.agent(id) != nil {
            done()
            open(id)
            return
        }
        if s.live?.external == true && s.liveAgentID == nil {
            failed()
            model.showToast("“\(s.title)” is running outside Hesper (a terminal or an IDE): finish it there first", error: true)
            return
        }
        let client = hub.client
        Task { @MainActor in
            do {
                let r = try await hub.timed("sessions.resume") { try await client.resumeSession(s.id, machine: machine) }
                done()
                started(r, from: s, changes: changes)
            } catch {
                failed()
                model.showToast("Could not resume: \(model.describe(error))", error: true)
            }
        }
    }

    func fork(_ s: Session, done: @escaping @MainActor (Bool) -> Void) {
        let client = hub.client
        Task { @MainActor in
            do {
                let r = try await hub.timed("sessions.fork") { try await client.forkSession(s.id) }
                done(true)
                started(r, from: s, changes: nil)
            } catch {
                done(false)
                model.showToast("Could not fork: \(model.describe(error))", error: true)
            }
        }
    }

    /// Unedited: hesperd's `sessions.continueAs` (its brief, the session's
    /// folder mapped to the home). Edited: the edited brief starts `kind`
    /// in the session's folder on its Mac (agents.spawn), since
    /// continueAs takes no text.
    func continueAs(_ s: Session, kind: String, brief: String?, done: @escaping @MainActor (Bool) -> Void) {
        let client = hub.client
        Task { @MainActor in
            do {
                let r: SessionStart
                if let brief {
                    let req = SpawnRequest(machine: s.movedTo ?? s.machine, kind: kind, project: s.cwd, task: brief,
                                           name: SessionFormat.oneLine(s.title, max: 40))
                    r = .agent(try await hub.timed("agents.spawn") { try await client.spawn(req) }, note: nil)
                } else {
                    r = try await hub.timed("sessions.continueAs") { try await client.continueSession(s.id, kind: kind) }
                }
                done(true)
                started(r, from: nil, changes: nil)
            } catch {
                done(false)
                model.showToast("Could not continue in \(SessionFormat.kindLabel(kind)): \(model.describe(error))", error: true)
            }
        }
    }

    func archive(_ s: Session) {
        let client = hub.client
        Task { @MainActor in
            do {
                try await hub.timed("sessions.archive") { try await client.archiveSession(s.id, archived: !s.archived) }
                model.showToast(s.archived ? "Unarchived “\(s.title)”" : "Archived “\(s.title)” (the archived chip shows it)")
                hub.refreshStats()
            } catch {
                model.showToast("Could not archive: \(model.describe(error))", error: true)
            }
        }
    }

    /// ⌫: gone from the list at once; hesperd keeps it 30 s (⌘Z / the
    /// toast's Undo → sessions.delete {undo: true}). A running session
    /// answers "live": its agent opens instead.
    func delete(_ s: Session) {
        let client = hub.client
        Task { @MainActor in
            do {
                try await hub.timed("sessions.delete") { try await client.deleteSession(s.id) }
                model.pushUndo(.restoreSession(s.id), label: "Deleted “\(SessionFormat.oneLine(s.title, max: 40))”")
                hub.refreshStats()
            } catch where SessionStart.live(from: error) != nil {
                model.showToast("“\(s.title)” is running: stop it first", error: true)
                hub.emitChanged(s)
                if case .live(let id?) = SessionStart.live(from: error), model.agent(id) != nil { open(id) }
            } catch {
                model.showToast("Could not delete: \(model.describe(error))", error: true)
                hub.emitChanged(s)
            }
        }
    }

    func restore(_ id: String) {
        let client = hub.client
        Task { @MainActor in
            do {
                try await hub.timed("sessions.delete") { try await client.deleteSession(id, undo: true) }
                // Back in the list now (an undo may not be announced).
                if let d = try? await hub.timed("sessions.show", { try await client.showSession(id) }) { hub.emitChanged(d.session) }
                hub.refreshStats()
            } catch {
                model.showToast("Could not undo the delete: \(model.describe(error))", error: true)
            }
        }
    }

    private func started(_ r: SessionStart, from s: Session?, changes: SessionChanges?) {
        switch r {
        case .live(let id):
            if let id { open(id) } else { model.showToast("That session is running outside Hesper", error: true) }
        case .agent(let a, let note):
            if let s { hub.noteResumed(a.id, s, changes: changes) }
            if let note { model.showToast(note) }
            // The agent joins its project's band (draft/band rules) once
            // agents.changed brings it; then it is selected and in view.
            Task { @MainActor in
                _ = await Harness.wait(5) { model.agent(a.id) != nil }
                if model.agent(a.id) != nil { model.select(a.id) }
            }
        }
    }

    /// A live session: its agent's tile (or window), like ⌘J.
    private func open(_ id: String) {
        if let a = model.agent(id), model.routeAttention?(a) == true { return }
        model.goTo(id)
    }
}
