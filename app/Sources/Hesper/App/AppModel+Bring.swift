import AppKit
import HesperCore
import Observation

/// Bringing a draft's folder to the Mac it starts on, shared by every
/// window's model (one per app): which Macs' hesperd can, and how far each
/// draft's bring got (agents.bringing).
@MainActor
@Observable
final class BringBook {
    /// Per Mac: its hesperd brings folders (an older one: today's notes).
    var support = BringSupport()
    /// Brings in flight, by draft.
    var tracker = BringTracker()
}

extension AppModel {
    /// The note offers "Bring it to mini" for drafts on `machine`.
    func bringOffered(on machine: String) -> Bool {
        lists.bringBook.support.offered(machine)
    }

    /// A draft's bring in flight (its tile's progress line).
    func bringProgress(draft id: String) -> BringTracker.Pending? {
        lists.bringBook.tracker.progress(draft: id)
    }

    /// agents.bringing (the main model, once for the shared book).
    func bringProgressed(_ p: BringProgress) {
        guard bringBook.tracker.apply(p) != nil else { return }
        onAgentsChanged?()
    }

    func bringStarted(draft id: String, to machine: String, changes: BringChanges) {
        lists.bringBook.tracker.start(draft: id, to: machine, changes: changes)
    }

    func bringEnded(draft id: String) {
        guard lists.bringBook.tracker.progress(draft: id) != nil else { return }
        lists.bringBook.tracker.finish(draft: id)
    }

    /// A spawn with `bring` came back from a hesperd that can't bring
    /// (unknown method / param, or it ignored it: "no directory"): that
    /// Mac gets today's notes from now on.
    func bringMissing(on machine: String) {
        lists.bringBook.support.byMachine[machine] = false
    }

    /// A spawn with `bring` failed: today's notes (an older hesperd) or the
    /// reason inline under the chips. Returns the failure, nil: fallback.
    @discardableResult
    func bringFailed(_ e: RPCError, request req: SpawnRequest, composer c: ComposerModel) -> BringFailure? {
        let m = req.machine ?? localMachine
        guard let f = BringFailure(e, tool: req.kind ?? "") else {
            bringMissing(on: m)
            c.folderCheck += 1
            return nil
        }
        c.bringFailed(f)
        return f
    }
}
