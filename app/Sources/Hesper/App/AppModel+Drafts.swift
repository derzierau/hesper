import AppKit
import HesperCore

/// Drafts: ⌘N puts a draft tile right of the selected tile; typing edits
/// it (saved continuously to hesperd); ⌘↩ turns it into the agent in place;
/// esc leaves it as a quiet tile; ⌘W discards it (undo brings it back).
extension AppModel {
    // MARK: Context

    var composerContext: ComposerContext {
        let lists = self.lists
        let profiles = lists.profiles
        var projects: [ComposerContext.ProjectChoice] = lists.recentProjects.map { .init(path: $0.path, name: $0.name, recent: true) }
        let recent = Set(projects.map(\.path))
        projects += lists.projectFolders.filter { !recent.contains($0) }.map { .init(path: $0, recent: false) }
        let kinds = profiles?.profiles.mapValues(\.kind) ?? ["claude": "claude", "claude-auto-rc": "claude", "claude-unattended": "claude",
                                                                   "codex": "codex", "codex-unattended": "codex", "shell": "shell"]
        var kindDefaults = profiles?.defaults?.kinds ?? ["claude": "claude", "codex": "codex", "shell": "shell"]
        for (k, p) in settings.defaultProfiles where kinds[p] != nil { kindDefaults[k] = p }
        let ms = machines.isEmpty ? [Machine(short: localMachine, name: "this Mac", online: true, rttMs: 0, route: "local")] : machines
        return ComposerContext(machines: ms, localMachine: localMachine, projects: projects, profiles: kinds, kindDefaults: kindDefaults,
                               defaultKind: profiles?.defaults?.kind ?? "claude", projectProfiles: profiles?.defaults?.projects ?? [:])
    }

    // MARK: Machine and folder agree (DraftSync)

    nonisolated static func dirExists(_ p: String) -> Bool {
        var dir: ObjCBool = false
        return FileManager.default.fileExists(atPath: p, isDirectory: &dir) && dir.boolValue
    }

    /// A machine the user didn't choose follows the folder.
    func followFolder(_ d: Draft) -> Draft {
        DraftSync.followFolder(d, catalog: catalog, local: localMachine, existsLocally: Self.dirExists)
    }

    /// The text's tokens into the draft (the project follows the last
    /// #token; an @token is an explicit machine), then the machine follows
    /// the folder. `previousText` nil: a restored draft.
    func syncDraft(_ d: Draft, previousText: String?) -> Draft {
        var n = DraftSync.sync(d, previousText: previousText, context: composerContext)
        if n.project != d.project { n = DraftSeed.folderChanged(n, to: n.project) }
        return followFolder(n)
    }

    /// Drafts from hesperd (restored, saved by an older app, edited
    /// elsewhere) brought in line: the stored project never disagrees with
    /// a #token, a machine nobody chose follows the folder. Re-run when
    /// the lists the tokens resolve against change.
    func normalizeDrafts() {
        guard !isMirror, registry.hello != nil else { return }
        for d in drafts.all where startingDrafts[d.id] == nil {
            let n = syncDraft(d, previousText: nil)
            if n != d { editDraftContent(n) }
        }
    }

    func machineName(_ short: String) -> String {
        machine(short)?.displayName ?? (short == localMachine ? "this Mac" : short)
    }

    /// Is `path` a folder on `machine`? This Mac: the file system; another
    /// machine: fs.stat through hesperd (a daemon without it: the
    /// catalog's paths). nil: can't tell (offline, an error).
    func folderExists(_ path: String, on machine: String) async -> Bool? {
        if machine == localMachine { return Self.dirExists(path) }
        do {
            return try await client.folderExists(path, machine: machine)
        } catch let e as RPCError where e.code == -32601 || e.message.hasPrefix("no method") {
            return DraftSync.machines(holding: path, catalog: catalog).contains(machine) ? true : nil
        } catch {
            return nil
        }
    }

    func composer(for id: String) -> ComposerModel {
        if let c = composers[id] { return c }
        let c = ComposerModel(id: id, app: self, quick: id == quickDraft.id)
        composers[id] = c
        return c
    }

    // MARK: Lifecycle

    /// A new draft, focused, in this window (`Draft.wall`: it shows here,
    /// whatever the window's scope or order). Without a project (⌘N, the
    /// toolbar, the menu bar, palette "New agent") the window presets what
    /// it is about (`DraftPreset`, contract "As built — new agents per
    /// window"): a project window its project (locked), a group window its
    /// most recently used project, a machine-filtered wall the machine;
    /// All / Overflow nothing: no folder, in the wall's "New" area (right
    /// of the selected card on a plain wall); machine and profile from
    /// Settings — never from the selected card or its band.
    /// Explicit project actions preset the project and open the draft in
    /// that project's band: `band` (a band's ＋, ⌘N on a focused band
    /// header), `projectID` (the sidebar's "New Agent in …"), `project` (a
    /// folder: the palette's projects, ⌥↩'s next draft). `machine`: chosen
    /// explicitly (the palette's machines). In the focus view (and an
    /// agent window) there is no wall to hold a draft: quick launch opens,
    /// preset to the agent's project and machine.
    func newDraft(project: String? = nil, projectID: String? = nil, machine: String? = nil, after: String? = nil, band: String? = nil,
                  machineExplicit: Bool? = nil) {
        if mode == .focus, project == nil, projectID == nil, band == nil, let a = agent(focusedID), let preset = onQuickLaunchPreset {
            closePopover()
            showPalette = false
            let place = quickLaunchPlace(for: a)
            preset(place.path, place.machine)
            return
        }
        closePopover()
        showPalette = false
        if mode == .focus { exitFocus() }
        if let e = editingDraftID { parkDraft(e, refocus: false) }
        let anchor = after ?? (wallItems.contains { $0.id == selectedID } ? selectedID : nil)
        var d = Draft(after: anchor)
        d.wall = wallID
        let b = band.flatMap { resolvedView.band(key: $0) }
        var home = b?.projectID ?? projectID
        if home == nil, let project { home = folderProjectID(project, machine: machine ?? localMachine) }
        // The window's scope presets what nothing explicit chose.
        let preset = wallID == nil ? DraftPreset() : DraftPreset.make(scope: scope, catalog: catalog)
        if home == nil, project == nil { home = preset.projectID }
        var machine = machine
        var machineExplicit = machineExplicit
        if machine == nil, let m = preset.machine {
            machine = m
            machineExplicit = true
        }
        if home != nil || project != nil {
            let place = DraftSeed.place(project: home, catalog: catalog, local: localMachine, prefer: machine)
            d.project = project ?? place.path
            d.machine = project != nil ? (machine == localMachine ? nil : machine) : place.machine
            if let p = d.project {
                d.profile = lists.profiles?.defaults?.projects?[p] ?? catalog.project(home)?.defaults?.profile
            }
            draftBands[d.id] = home
            d.band = home
            d.projectLocked = DraftPreset.locks(projectID: home, scope: scope, catalog: catalog)
            if let b, let last = b.members.last { d.after = last }
            if let k = b?.key, collapsedBands.contains(k) { collapsedBands.remove(k) }
        } else {
            d.machine = machine == localMachine ? nil : machine
        }
        focusedBandKey = nil
        d.machineExplicit = machineExplicit ?? (machine != nil)
        drafts.edit(d)
        scheduleDraftSave()
        selectedID = d.id
        editingDraftID = d.id
        mode = .compose
        onAgentsChanged?()
        onModeChanged?()
    }

    /// The draft sits in the "New" area: opened without a project (or
    /// restored after a restart), until it starts.
    func isNewDraft(_ id: String) -> Bool { drafts[id] != nil && draftBand(id) == nil }

    /// The project a draft was opened in: saved on the draft (so every
    /// window and a restart agree), else this window's memory of it.
    func draftBand(_ id: String) -> String? { drafts[id]?.band ?? draftBands[id] }

    func editDraft(_ id: String) {
        guard var d = drafts[id] else { return }
        if let e = editingDraftID, e != id { parkDraft(e, refocus: false) }
        closePopover()
        if mode == .focus { exitFocus() }
        if d.parked {
            d.parked = false
            editDraftContent(d)
        }
        selectedID = id
        editingDraftID = id
        mode = .compose
        onAgentsChanged?()
        onModeChanged?()
    }

    /// Esc: a quiet tile (the shelf in grid + shelf); an empty draft goes.
    func parkDraft(_ id: String, refocus: Bool = true) {
        if popover?.draftID == id { closePopover() }
        if editingDraftID == id { editingDraftID = nil }
        if mode == .compose && editingDraftID == nil { mode = .wall }
        guard var d = drafts[id] else { return }
        if d.isEmpty && startingDrafts[id] == nil {
            removeDraft(id)
        } else if !d.parked && startingDrafts[id] == nil {
            d.parked = true
            editDraftContent(d)
        }
        onAgentsChanged?()
        if refocus { onModeChanged?() }
    }

    /// Leaves the composer for something else (click elsewhere, ⌘J, focus).
    func leaveComposer() {
        if let id = editingDraftID { parkDraft(id, refocus: false) }
    }

    /// ⌘W on a draft: gone at once, undo brings it back.
    func discardDraft(_ id: String) {
        guard let d = drafts[id] else { return }
        if editingDraftID == id { editingDraftID = nil; mode = .wall }
        let items = wallItems
        let i = items.firstIndex { $0.id == id }
        removeDraft(id)
        if !d.isEmpty { pushUndo(.restoreDraft(d), label: "Discarded draft") }
        if selectedID == id, let i {
            let rest = items.filter { $0.id != id }
            selectedID = rest.isEmpty ? nil : rest[min(i, rest.count - 1)].id
        }
        onAgentsChanged?()
        onModeChanged?()
    }

    func restoreDraft(_ d: Draft) {
        drafts.restore(d)
        scheduleDraftSave()
        selectedID = d.id
        onAgentsChanged?()
        onModeChanged?()
    }

    func removeDraft(_ id: String) {
        _ = drafts.remove(id)
        draftBands[id] = nil
        composers[id] = nil
        startingDrafts[id] = nil
        if draftsSupported {
            Task { try? await client.removeDraft(id) }
        }
    }

    /// Another app (or a start elsewhere) removed it.
    func draftGone(_ id: String) {
        composers[id] = nil
        if editingDraftID == id { editingDraftID = nil; if mode == .compose { mode = .wall } }
        if popover?.draftID == id { closePopover() }
    }

    // MARK: Saving

    /// A local edit: the tile shows it now, hesperd gets it shortly.
    func editDraftContent(_ d: Draft) {
        if d.id == quickDraft.id { quickDraft = d; return }
        var d = d
        // Another project's folder unlocks a project window's draft.
        if d.projectLocked { d = DraftLock.check(d, folderProject: folderProjectID(d.project, machine: d.machine ?? localMachine), catalog: catalog) }
        drafts.edit(d)
        scheduleDraftSave()
    }

    func scheduleDraftSave() {
        guard draftSaveTask == nil else { return }
        draftSaveTask = Task { @MainActor [weak self] in
            while let self, let due = self.drafts.nextDue() {
                let wait = due.timeIntervalSinceNow
                if wait > 0 { try? await Task.sleep(nanoseconds: UInt64(wait * 1e9)) }
                for d in self.drafts.due() { self.send(d) }
            }
            self?.draftSaveTask = nil
        }
    }

    /// Unsaved drafts or closes still on their way to hesperd (quit waits).
    var hasPendingWork: Bool {
        !drafts.dirty.isEmpty || !drafts.inFlight.isEmpty || !closeBook.inFlight.isEmpty
    }

    /// Everything unsaved goes now (app quit, start).
    func flushDrafts() async {
        let list = drafts.flushAll()
        guard draftsSupported, isConnected else { return }
        for d in list {
            if let r = try? await client.saveDraft(d) { drafts.saved(r) } else { drafts.saveFailed(d.id) }
        }
    }

    private func send(_ d: Draft) {
        guard draftsSupported else { drafts.saved(d); return }
        Task { @MainActor in
            do {
                let r = try await client.saveDraft(d)
                drafts.saved(r)
            } catch {
                drafts.saveFailed(d.id)
                // Not connected: try again in a moment (and on reconnect).
                try? await Task.sleep(nanoseconds: 1_000_000_000)
                scheduleDraftSave()
            }
        }
    }

    /// "draft · saved" / "saving…" for the tile's header.
    func saveLabel(_ id: String) -> String {
        if startingDrafts[id] != nil { return "starting…" }
        if !draftsSupported { return "draft · this session" }
        if drafts.dirty[id] != nil || drafts.inFlight[id] != nil { return "draft · saving…" }
        return "draft · saved"
    }

    // MARK: Starting

    /// ⌘↩ (⌥↩: and a new draft right of it). The draft tile becomes the
    /// agent in place: same slot, the first line names it.
    func startDraft(_ id: String, openNext: Bool = false) {
        guard drafts[id] != nil, startingDrafts[id] == nil else { return }
        let c = composer(for: id)
        // No folder yet: ⌘↩ goes to the folder chip (Start is disabled).
        if c.needsFolder {
            if popover == nil { openPopover(.chip(id, .project)) }
            return
        }
        guard var req = c.spawnRequest() else { return }
        // A folder that isn't there yet (the note said so): created now.
        if c.missingFolder != nil {
            if c.createMissingFolder() != nil { onAgentsChanged?(); return }
            req.project = c.resolution.project ?? req.project
        }
        let after = drafts[id]?.after
        startingDrafts[id] = PendingStart(task: req.task, project: req.project, machine: req.machine ?? localMachine, after: after)
        c.busy = true
        c.error = nil
        let clone = c.resolution.cloneURL
        if openNext {
            newDraft(project: c.resolution.project, machine: c.resolution.machine, after: id, machineExplicit: c.machinePinned)
        } else {
            if editingDraftID == id { editingDraftID = nil }
            if mode == .compose { mode = .wall }
            onModeChanged?()
        }
        onAgentsChanged?()
        Task { @MainActor in
            var req = req
            if let clone {
                switch await self.clone(url: clone, machine: req.machine) {
                case .success(let path):
                    req.project = path
                    self.startingDrafts[id]?.project = path
                case .failure(let e):
                    self.startFailed(id, e)
                    return
                }
            }
            // The folder on the machine it starts on (fs.stat): missing →
            // the chip row's note ("This folder is on laptop, not on
            // mini — Run on laptop"), nothing starts.
            if clone == nil, let m = req.machine, m != self.localMachine, !(await c.checkTarget(force: true)) {
                self.startFailed(id, nil)
                return
            }
            if let m = req.machine, m != self.localMachine {
                guard let task = await self.uploadDraftAttachments(id, machine: m, task: req.task) else {
                    self.startFailed(id, "Could not send the attachments to \(self.machine(m)?.displayName ?? m)")
                    return
                }
                req.task = task
                self.startingDrafts[id]?.task = task
            }
            do {
                let a = try await client.spawn(req)
                adopt(draftID: id, agent: a)
                onDraftStarted?(a)
                Task { await refreshLists() }
            } catch {
                startFailed(id, describe(error))
            }
        }
    }

    /// nil message: the chip row already says why. hesperd's "no
    /// directory …" becomes the chip row's folder note, never raw text.
    private func startFailed(_ id: String, _ message: String?) {
        startingDrafts[id] = nil
        let c = composer(for: id)
        c.busy = false
        if let message, !c.spawnFailed(message) { c.error = message }
        onAgentsChanged?()
    }

    /// The agent of a draft being started showed up before spawn's reply.
    func adoptIfStarting(_ a: Agent) {
        guard adoptions[a.id] == nil else { return }
        if let (id, _) = startingDrafts.first(where: { $0.value.task == (a.task ?? "") && $0.value.project == (a.project ?? "") && $0.value.machine == a.machine }) {
            adopt(draftID: id, agent: a)
        }
    }

    func adopt(draftID: String, agent a: Agent) {
        guard adoptions[a.id] == nil else { return }
        let items = wallItems
        var after = startingDrafts[draftID]?.after ?? drafts[draftID]?.after
        if after == nil, let i = items.firstIndex(where: { $0.id == draftID }) {
            after = i == 0 ? "^" : items[i - 1].id
        }
        adoptions[a.id] = draftID
        if let after { placements[a.id] = after }
        // Whatever sat right of the draft now sits right of the agent.
        for d in drafts.all where d.after == draftID {
            var n = d
            n.after = a.id
            editDraftContent(n)
        }
        for (k, v) in placements where v == draftID { placements[k] = a.id }
        startingDrafts[draftID] = nil
        removeDraft(draftID)
        if selectedID == draftID { selectedID = a.id }
        onAgentsChanged?()
        onModeChanged?()
    }

    // MARK: Per window (contract "As built — new agents per window")

    /// The agent's project folder and machine, for quick launch from its
    /// window or focus view: the project's folder on the agent's machine
    /// (`DraftSeed.place`), else the agent's own folder.
    func quickLaunchPlace(for a: Agent) -> (path: String?, machine: String?) {
        let pid = catalog.projectID(for: a)
        let place = DraftSeed.place(project: pid, catalog: catalog, local: localMachine, prefer: a.machine)
        if let path = place.path { return (path, place.machine) }
        return (a.project, a.machine == localMachine ? nil : a.machine)
    }

    /// A group window's folder chip offers the group's projects (most
    /// recently used first), on the draft's machine; nil: every folder.
    func groupFolderChoices(machine: String) -> [ComposerContext.ProjectChoice]? {
        guard wallID != nil, case .group = scope, let ids = DraftPreset.make(scope: scope, catalog: catalog).choices, !ids.isEmpty else { return nil }
        var seen: Set<String> = []
        return ids.compactMap { id in
            guard let path = catalog.path(project: id, machine: machine, local: localMachine), seen.insert(path).inserted else { return nil }
            return ComposerContext.ProjectChoice(path: path, name: catalog.name(project: id), recent: false)
        }
    }

    // MARK: Quick launch

    /// ⌘↩ in the quick launch panel: start it (the agent joins the wall),
    /// clear the text, keep the choices.
    func startQuick() async -> String? {
        let c = composer(for: quickDraft.id)
        if c.needsFolder {
            c.complete(.project) // the # list: choose a folder first
            return nil
        }
        if c.missingFolder != nil, let e = c.createMissingFolder() { return e }
        guard let req = c.spawnRequest() else { return c.error }
        c.busy = true
        defer { c.busy = false }
        var r = req
        if let url = c.resolution.cloneURL {
            switch await clone(url: url, machine: r.machine) {
            case .success(let p): r.project = p
            case .failure(let e): c.error = e; return e
            }
        } else if let m = r.machine, m != localMachine, !(await c.checkTarget(force: true)) {
            return c.folderNote?.text ?? "The folder isn't on \(machineName(m))"
        }
        do {
            let a = try await client.spawn(r)
            var d = quickDraft
            d.text = ""
            d.attachments = []
            quickDraft = d
            c.error = nil
            onStarted?(a, true)
            Task { await refreshLists() }
            return nil
        } catch {
            let message = describe(error)
            if c.spawnFailed(message) { return c.folderNote?.text ?? c.missingFolder.map { "Folder doesn't exist: \(ComposerCompletion.abbreviate($0))" } ?? "The folder isn't there" }
            c.error = message
            return c.error
        }
    }

    // MARK: Attachments

    /// Where dropped images are saved: Application Support (not the
    /// project, so nothing lands in a repository; automated runs: the
    /// state dir).
    func attachmentsDir(for draftID: String) -> URL {
        let base: URL
        if !persistSettings, let s = env.stateDir {
            base = URL(fileURLWithPath: s).appendingPathComponent("attachments")
        } else if !persistSettings {
            base = FileManager.default.temporaryDirectory.appendingPathComponent("hesper-attachments")
        } else {
            base = (FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask).first ?? FileManager.default.temporaryDirectory)
                .appendingPathComponent("Hesper/Attachments")
        }
        return base.appendingPathComponent(draftID)
    }
}
