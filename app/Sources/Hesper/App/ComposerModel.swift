import AppKit
import HesperCore
import Observation

/// One composer (a draft tile's, or the quick launch panel's): the draft's
/// text and choices, the tokens it resolves to, and the completion list for
/// the token at the caret.
@MainActor
@Observable
final class ComposerModel {
    let id: String
    @ObservationIgnored unowned let app: AppModel
    let quick: Bool
    var busy = false
    var error: String?
    var caret = 0
    var completion: CompletionState?
    /// The user chose the machine (chip, palette, @token, a draft opened
    /// for a machine): kept in the draft (`machineExplicit`), so a folder
    /// change keeps it. Otherwise the machine follows the folder.
    var machinePinned: Bool { draft.machineExplicit }
    /// fs.stat answers: `targetKey` → the folder is there.
    var targetFolders: [String: Bool] = [:]
    /// Bumped when a folder was created (the missing-folder note re-checks).
    var folderCheck = 0
    /// The text view: replace its text (a completion, a chip choice).
    @ObservationIgnored var onReplaceText: ((String, Int) -> Void)?
    /// The text view: where the caret is (root/panel coordinates).
    @ObservationIgnored var caretRect: (() -> CGRect?)?
    /// Completion closed with esc at this token: stays closed until the
    /// token changes.
    @ObservationIgnored private var dismissedToken: ComposerToken?

    struct CompletionState: Equatable {
        var token: ComposerToken
        var items: [CompletionItem]
        var list: OverlayList
        var selected: Int? { list.selection(enabled: items.map(\.enabled)) }
    }

    init(id: String, app: AppModel, quick: Bool) {
        self.id = id
        self.app = app
        self.quick = quick
    }

    var draft: Draft { quick ? app.quickDraft : (app.drafts[id] ?? Draft(id: id)) }
    var resolution: ComposerResolution { ComposerResolution.make(text: draft.text, draft: draft.choices, context: app.composerContext) }

    private func persist(_ d: Draft) { app.editDraftContent(d) }

    // MARK: Editing

    func textChanged(_ text: String, caret: Int) {
        self.caret = caret
        var d = draft
        if d.text != text {
            let previous = d.text
            d.text = text
            // The tokens decide (DraftSync): a #token is the project (the
            // last one; removing it clears it), an @token the machine; a
            // machine the user didn't choose follows the folder.
            d = app.syncDraft(d, previousText: previous)
            persist(d)
            error = nil
        }
        updateCompletion(text: text)
    }

    /// The folder chosen (chip list, folder picker, a typed path, recent):
    /// the draft's project, silently.
    func setFolder(_ path: String) {
        var d = app.followFolder(DraftSeed.folderChanged(draft, to: path))
        if let p = app.lists.profiles?.defaults?.projects?[path] { d.profile = p }
        persist(d)
        error = nil
        folderCheck += 1
    }

    /// Made in a project window: the project is the window's (a lock on
    /// the project pill) until unlocked — a click, or another project's
    /// folder (`DraftLock`, in `AppModel.editDraftContent`).
    var projectLocked: Bool { !quick && draft.projectLocked }

    func unlockProject() {
        var d = draft
        d.projectLocked = false
        persist(d)
    }

    /// A folder on this Mac that isn't there (the chip row's note).
    var missingFolder: String? {
        _ = folderCheck
        let r = resolution
        guard r.cloneURL == nil else { return nil }
        return DraftSeed.missingFolder(project: r.project, machine: r.machine, local: app.localMachine) { p in
            var dir: ObjCBool = false
            return FileManager.default.fileExists(atPath: p, isDirectory: &dir) && dir.boolValue
        }
    }

    // MARK: The folder on the target machine

    /// The draft's folder on another machine: what to ask fs.stat.
    var targetKey: String? {
        let r = resolution
        guard r.cloneURL == nil, let p = r.project, p.hasPrefix("/"), r.machine != app.localMachine else { return nil }
        return r.machine + "\n" + p
    }

    /// Asks the target machine whether the folder is there (the chip row
    /// asks whenever machine or folder change; start asks again). Returns
    /// false only when it is known to be missing.
    @discardableResult
    func checkTarget(force: Bool = false) async -> Bool {
        guard let key = targetKey else { return true }
        if !force, let known = targetFolders[key] { return known }
        let parts = key.split(separator: "\n", maxSplits: 1).map(String.init)
        guard let there = await app.folderExists(parts[1], on: parts[0]) else { return true }
        targetFolders[key] = there
        return there
    }

    /// "This folder is on laptop, not on mini — Run on laptop" (and "Use
    /// mini's copy"): the folder isn't on the machine the draft runs on.
    var folderNote: FolderNote? {
        _ = folderCheck
        guard let key = targetKey else { return nil }
        let r = resolution
        var dir: ObjCBool = false
        let here = r.project.map { FileManager.default.fileExists(atPath: $0, isDirectory: &dir) && dir.boolValue } ?? false
        return FolderNote.make(project: r.project, machine: r.machine, local: app.localMachine, onTarget: targetFolders[key], existsLocally: here,
                               catalog: app.catalog, targetName: app.machineName(r.machine), hereName: app.machineName(app.localMachine))
    }

    /// "Run on laptop": this Mac, not chosen (the machine follows the
    /// folder again); an @machine token leaves the text.
    func runHere() {
        var d = draft
        if let t = resolution.tokens.last(where: { $0.token.kind == .machine })?.token {
            let ns = d.text as NSString
            var range = NSRange(location: t.location, length: t.length)
            if range.location + range.length < ns.length, ns.substring(with: NSRange(location: range.location + range.length, length: 1)) == " " { range.length += 1 }
            d.text = ns.replacingCharacters(in: range, with: "")
            onReplaceText?(d.text, min(t.location, (d.text as NSString).length))
        }
        d.machine = nil
        d.machineExplicit = false
        persist(d)
        error = nil
        folderCheck += 1
    }

    /// "Use mini's copy": the same project's folder on the target machine.
    func useTargetCopy() {
        guard let note = folderNote, let copy = note.copy else { return }
        var d = draft
        let machine = d.machine
        // A #token naming another folder would take the project back.
        if let t = resolution.tokens.last(where: { $0.token.kind == .project })?.token,
           !ComposerResolution.token(t.query, names: copy, context: app.composerContext) {
            let r = ComposerCompletion.apply("#" + (copy as NSString).lastPathComponent, to: t, in: d.text)
            onReplaceText?(r.text, r.caret)
            d.text = r.text
        }
        d = DraftSeed.folderChanged(d, to: copy, machinePinned: true)
        d.machine = machine
        d.machineExplicit = true
        persist(d)
        error = nil
        folderCheck += 1
    }

    /// hesperd said "no directory <path>" (a spawn): the chip row's note
    /// instead of the raw text. true: handled.
    func spawnFailed(_ message: String) -> Bool {
        guard DraftSync.missingDirectory(in: message) != nil else { return false }
        if let key = targetKey { targetFolders[key] = false }
        error = nil
        folderCheck += 1
        return true
    }

    /// "Create" in the note (and on start): mkdir -p. nil: done.
    @discardableResult
    func createMissingFolder() -> String? {
        guard let p = missingFolder else { return nil }
        do {
            try FileManager.default.createDirectory(atPath: p, withIntermediateDirectories: true)
            folderCheck += 1
            return nil
        } catch {
            let e = "Could not create \(ComposerCompletion.abbreviate(p)): \(error.localizedDescription)"
            self.error = e
            return e
        }
    }

    /// No folder yet: the draft can't start (Start disabled, the chip says
    /// "Choose folder") — unless it starts a new scratch.
    var needsFolder: Bool {
        let r = resolution
        return r.project == nil && r.cloneURL == nil && !startsScratch
    }

    /// No folder chosen and the machine's hesperd makes scratches: the
    /// project chip says "New scratch" and the start makes one
    /// (`ScratchDraft`). A project window's locked draft never does.
    var startsScratch: Bool {
        let r = resolution
        return ScratchDraft.starts(project: r.project, cloneURL: r.cloneURL, locked: projectLocked, supported: app.scratchAvailable(on: r.machine))
    }

    /// The folder list's "New scratch": no folder (a #token naming one
    /// leaves the text).
    func useScratch() {
        var d = draft
        if let t = resolution.tokens.last(where: { $0.token.kind == .project })?.token {
            let ns = d.text as NSString
            var range = NSRange(location: t.location, length: t.length)
            if range.location + range.length < ns.length, ns.substring(with: NSRange(location: range.location + range.length, length: 1)) == " " { range.length += 1 }
            d.text = ns.replacingCharacters(in: range, with: "")
            onReplaceText?(d.text, min(t.location, (d.text as NSString).length))
        }
        d = DraftSeed.folderChanged(d, to: nil)
        d.projectLocked = false
        persist(d)
        error = nil
        folderCheck += 1
    }

    func caretMoved(_ caret: Int) {
        guard caret != self.caret else { return }
        self.caret = caret
        updateCompletion(text: draft.text)
    }

    func updateCompletion(text: String? = nil) {
        let text = text ?? draft.text
        guard let t = ComposerParser.token(at: caret, in: text) else {
            completion = nil
            dismissedToken = nil
            return
        }
        if let dt = dismissedToken, dt.location == t.location, dt.kind == t.kind { completion = nil; return }
        let items = ComposerCompletion.items(for: t, context: app.composerContext, firstLine: ComposerResolution.firstLine(text))
        // A resolved token at its end with only itself to offer: no list.
        if items.isEmpty || (items.count == 1 && ComposerParser.resolve(text, context: app.composerContext).contains { $0.token == t }
                             && items[0].action == .insert(String(t.kind.sigil) + t.query)) {
            completion = nil
            return
        }
        let keep = completion?.token.location == t.location ? completion?.list ?? OverlayList() : OverlayList()
        completion = CompletionState(token: t, items: items, list: OverlayList(query: t.query, index: keep.query == t.query ? keep.index : 0))
    }

    /// Opens the completion for a kind at the caret (a chip with nothing
    /// typed yet inserts the sigil).
    func complete(_ kind: TokenKind) {
        let ns = draft.text as NSString
        let c = min(caret, ns.length)
        let before = c > 0 ? ns.substring(with: NSRange(location: c - 1, length: 1)) : "\n"
        let insert = (before.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty ? "" : " ") + String(kind.sigil)
        let text = ns.replacingCharacters(in: NSRange(location: c, length: 0), with: insert)
        let caret = c + (insert as NSString).length
        dismissedToken = nil
        onReplaceText?(text, caret)
        textChanged(text, caret: caret)
    }

    // MARK: Completion keys

    /// ↑↓ ⏎ ⇥ esc while the list is open; false: not the list's.
    func completionKey(_ action: OverlayKeyAction) -> Bool {
        guard var c = completion else { return false }
        switch action {
        case .up: c.list.move(-1, enabled: c.items.map(\.enabled)); completion = c
        case .down: c.list.move(1, enabled: c.items.map(\.enabled)); completion = c
        case .activate, .actOn, .alternate:
            guard let i = c.selected else { completion = nil; return action != .alternate }
            accept(c.items[i])
            return true
        case .close:
            dismissedToken = c.token
            completion = nil
        default:
            return false
        }
        return true
    }

    func accept(_ item: CompletionItem) {
        guard let t = completion?.token ?? ComposerParser.token(at: caret, in: draft.text) else { return }
        switch item.action {
        case .insert(let s):
            let r = ComposerCompletion.apply(s, to: t, in: draft.text)
            onReplaceText?(r.text, r.caret)
            completion = nil
            dismissedToken = nil
            textChanged(r.text, caret: r.caret)
            // The folder of the row chosen (not the first of that name).
            if item.kind == .project, item.id.hasPrefix("project:") {
                let path = String(item.id.dropFirst("project:".count))
                if draft.project != path { setFolder(path) }
            }
            if item.kind == .branch {
                var d = draft
                d.worktree = true
                persist(d)
            }
        case .clone(let url):
            completion = nil
            Task { await cloneProject(url, token: t) }
        }
    }

    /// "Clone and start": clone through hesperd, then the token names the
    /// clone (and the draft starts, as the item says).
    func cloneProject(_ url: String, token: ComposerToken) async {
        busy = true
        let machine = resolution.machine
        let result = await app.clone(url: url, machine: machine == app.localMachine ? nil : machine)
        busy = false
        switch result {
        case .success(let path):
            let name = (path as NSString).lastPathComponent
            let r = ComposerCompletion.apply("#\(name)", to: token, in: draft.text)
            onReplaceText?(r.text, r.caret)
            textChanged(r.text, caret: r.caret)
            var d = draft
            d.project = path
            persist(d)
            if quick {
                _ = await app.startQuick()
            } else {
                app.startDraft(id)
            }
        case .failure(let e):
            error = e
        }
    }

    // MARK: Chips

    /// A chip's choice: rewrites the token of that kind when the text has
    /// one, else sets the draft's value.
    func choose(_ kind: TokenKind, value: String) {
        var d = draft
        let text = d.text
        if let t = resolution.tokens.last(where: { $0.token.kind == kind })?.token {
            let insert: String
            switch kind {
            case .machine: insert = "@\(value)"
            case .project:
                // The name the list shows (it resolves back to this folder,
                // see ComposerResolution); a name with spaces: the folder's.
                let name = app.composerContext.projects.first { $0.path == value }?.name ?? (value as NSString).lastPathComponent
                insert = "#" + (name.contains(where: \.isWhitespace) ? (value as NSString).lastPathComponent : name)
            case .profile: insert = "/\(value)"
            case .branch: insert = "~\(value)"
            }
            let r = ComposerCompletion.apply(insert, to: t, in: text)
            onReplaceText?(r.text, r.caret)
            d.text = r.text
        }
        switch kind {
        case .machine:
            d.machine = value == app.localMachine ? nil : value
            d.machineExplicit = true
        case .project:
            persist(d)
            setFolder(value)
            updateCompletion(text: draft.text)
            return
        case .profile: d.profile = value
        case .branch: d.branch = value; d.worktree = true
        }
        persist(d)
        updateCompletion(text: d.text)
    }

    /// ⌘⇧W: worktree on (with the suggested branch) or off.
    func toggleWorktree() {
        var d = draft
        let r = resolution
        if r.worktree {
            // A ~branch token forces it: remove the token.
            if let t = r.tokens.last(where: { $0.token.kind == .branch })?.token {
                let ns = d.text as NSString
                let out = ns.replacingCharacters(in: NSRange(location: t.location, length: t.length), with: "")
                d.text = out
                onReplaceText?(out, min(t.location, (out as NSString).length))
            }
            d.worktree = false
        } else {
            d.worktree = true
        }
        persist(d)
    }

    // MARK: Attachments

    /// Files dropped or pasted: their paths go into the task at the caret,
    /// shell-escaped like a drop on a terminal (images the agents can't
    /// read, e.g. HEIC, as a PNG copy).
    func attach(paths: [String]) -> String {
        let dir = app.attachmentsDir(for: id)
        let paths = paths.map { DropPlan.needsConversion($0) ? (AttachmentStore.convertToPNG($0, in: dir) ?? $0) : $0 }
        var d = draft
        for p in paths where !d.attachments.contains(p) { d.attachments.append(p) }
        persist(d)
        return ShellEscape.joined(paths)
    }

    /// Image bytes without a file (a screenshot pasted, a browser image):
    /// saved as `<timestamp>-<name>.png`.
    func saveImage(_ data: Data, name: String = "screenshot") -> String? {
        do { return try AttachmentStore.saveImage(data, name: name, in: app.attachmentsDir(for: id)) } catch {
            self.error = "Could not save the image: \(error.localizedDescription)"
            return nil
        }
    }

    // MARK: Start

    /// The spawn this draft asks for, or nil (and `error`) when it cannot
    /// start yet.
    func spawnRequest() -> SpawnRequest? {
        let r = resolution
        let profile = r.profile
        let kind = profile.flatMap { app.composerContext.profiles[$0] }
        // No folder: a new scratch, else not startable (the caller opens
        // the folder chip).
        let scratch = startsScratch
        guard r.project != nil || r.cloneURL != nil || scratch else { return nil }
        if r.task.isEmpty && kind != "shell" {
            error = "Write the task first."
            return nil
        }
        if let m = app.machine(r.machine), !m.online {
            error = "\(m.displayName) is offline."
            return nil
        }
        var req = SpawnRequest(machine: r.machine == app.localMachine ? nil : r.machine, profile: profile, kind: kind,
                               project: r.project ?? "", task: r.task, name: nil,
                               worktree: r.worktree && !scratch ? .auto : nil, branch: r.worktree && !scratch && !r.branch.isEmpty ? r.branch : nil)
        req.scratch = scratch
        return req
    }
}
