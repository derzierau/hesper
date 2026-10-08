import AppKit
import HesperCore
import Observation

/// Scratch projects (scratch contract), shared by every window's model:
/// what each Mac's hesperd can do with them and hesperd's scratch
/// settings.
@MainActor
@Observable
final class ScratchBook {
    /// Per Mac: makes scratches (`projects.scratch`, spawn `scratch: true`).
    var support = ScratchSupport()
    /// This hesperd keeps, archives, restores and promotes them.
    var lifecycle = false
    /// `scratch.archiveAfterDays` / `deleteAfterDays`; nil: not readable
    /// (Settings hides the controls).
    var settings: ScratchSettings?
}

extension AppModel {
    /// A draft without a folder on `machine` starts a new scratch.
    func scratchAvailable(on machine: String) -> Bool {
        lists.scratchBook.support.supported(machine, local: localMachine)
    }

    // MARK: What hesperd can do (after each connect, main model)

    /// One harmless call per connect: `projects.scratchKeep` for an id no
    /// project has answers "not found" from a hesperd with scratches and
    /// "unknown method" from an older one. Then the settings.
    func probeScratch() async {
        let book = scratchBook
        do {
            try await client.keepScratch("~probe", keep: false)
            book.lifecycle = true
        } catch let e as RPCError {
            book.lifecycle = !CloseRPC.missing(e)
        } catch {
            return // not connected: ask again on the next connect
        }
        book.support.byMachine[localMachine] = book.lifecycle
        await refreshScratchSettings()
        onAgentsChanged?()
    }

    func refreshScratchSettings() async {
        guard scratchBook.lifecycle, let v = try? await client.settings(ScratchSettings.keys) else {
            scratchBook.settings = nil
            return
        }
        scratchBook.settings = ScratchSettings(json: v)
    }

    func setScratchSetting(_ key: String, days: Int) {
        let d = ScratchSettings.clamp(days)
        var s = scratchBook.settings ?? ScratchSettings()
        if key == ScratchSettings.archiveKey { s.archiveAfterDays = d } else { s.deleteAfterDays = d }
        scratchBook.settings = s
        run("change the scratch setting") {
            try await self.client.setSettings(ScratchSettings.setParams(key, days: d))
        }
    }

    /// A spawn with `scratch: true` came back "no scratches here": that Mac
    /// gets today's "Choose folder" from now on.
    func scratchMissing(on machine: String) {
        scratchBook.support.byMachine[machine] = false
    }

    // MARK: Actions

    /// Agents running in the project now (any Mac).
    func liveAgents(in pid: String) -> Int {
        registry.agents.values.filter { catalog.projectID(for: $0) == pid && $0.isRunning }.count
    }

    func scratchActions(_ pid: String) -> [ScratchActionItem] {
        ScratchActions.available(catalog.projects[pid], live: liveAgents(in: pid), supported: lists.scratchBook.lifecycle)
    }

    /// The scratch an agent runs in (nil: none, or not one with a lifecycle).
    func scratchID(of a: Agent) -> String? {
        guard let pid = catalog.projectID(for: a), ScratchLifecycle.isLifecycle(catalog.projects[pid]) else { return nil }
        return pid
    }

    func scratchName(_ pid: String) -> String {
        catalog.project(pid).map(ScratchName.display) ?? pid
    }

    func perform(_ action: ScratchAction, scratch pid: String, window: NSWindow? = nil) {
        guard let p = catalog.projects[pid] else { return }
        let name = ScratchName.display(p)
        switch action {
        case .rename:
            guard let n = ProjectSidebar.ask("Rename scratch", value: name) else { return }
            renameProject(pid, to: n)
        case .keep, .unkeep:
            let keep = action == .keep
            run(keep ? "keep the scratch" : "change the scratch", unavailable: "Scratch") {
                try await self.client.keepScratch(pid, keep: keep)
                self.showToast(keep ? "“\(name)” is kept: never archived" : "“\(name)” archives after \(self.scratchBook.settings?.archiveAfterDays ?? 14) quiet days")
            }
        case .archive:
            run("archive the scratch") {
                try await self.client.archiveScratch(pid)
                self.showToast("Archived “\(name)”", action: ToastAction(title: "Restore") { [weak self] in self?.perform(.restore, scratch: pid) })
            }
        case .restore:
            run("restore the scratch") {
                try await self.client.restoreScratch(pid)
                self.showToast("Restored “\(name)”")
            }
        case .promote:
            let folder = p.path(on: p.scratch?.home, local: localMachine)
            PromoteScratchPresenter.present(on: window ?? NSApp.keyWindow, scratchName: name, folder: folder, suggested: ScratchName.slug(name, limit: 64)) {
                [weak self] newName, repo in self?.promoteScratch(pid, name: newName, createRepo: repo)
            }
        case .delete:
            let a = NSAlert()
            a.messageText = "Delete “\(name)”?"
            a.informativeText = ScratchActions.deleteMessage(p)
            a.alertStyle = .warning
            a.addButton(withTitle: "Delete")
            a.addButton(withTitle: "Cancel")
            a.buttons.first?.hasDestructiveAction = true
            guard a.runModal() == .alertFirstButtonReturn else { return }
            deleteScratch(pid, name: name)
        }
    }

    func promoteScratch(_ pid: String, name: String, createRepo: Bool) {
        Task { @MainActor in
            do {
                let p = try await client.promoteScratch(pid, name: name, createRepo: createRepo)
                showToast("“\(p.name)” is a project now" + (createRepo ? " (GitHub repo created)" : ""))
            } catch let e as RPCError where e.message.localizedCaseInsensitiveContains("busy") {
                showToast("Could not promote “\(name)”: \(ScratchActions.busyReason.lowercased())", error: true)
            } catch {
                showToast("Could not promote the scratch: \(describe(error))", error: true)
            }
        }
    }

    /// `projects.scratchDelete {id}`: the folder and the catalog entry go
    /// (its sessions stay in History, "folder removed").
    func deleteScratch(_ pid: String, name: String) {
        Task { @MainActor in
            do {
                _ = try await client.call(ScratchRPC.delete, ["id": .string(pid)], timeout: 60)
                showToast("Deleted “\(name)”")
            } catch let e as RPCError where CloseRPC.missing(e) {
                showToast("This hesperd can't delete scratches yet", error: true)
            } catch {
                showToast("Could not delete the scratch: \(describe(error))", error: true)
            }
        }
    }

    /// A History session's scratch while it is archived (its preview offers
    /// "Restore scratch").
    func archivedScratch(for s: Session) -> String? {
        guard lists.scratchBook.lifecycle else { return nil }
        let pid = s.projectId ?? catalog.projectID(path: s.cwd, machine: s.machine)
        guard let pid, ScratchLifecycle.isArchived(catalog.projects[pid]) else { return nil }
        return pid
    }

    // MARK: Palette and menus

    /// ⌘K: the selected agent's scratch actions; while searching, every
    /// scratch's (restore for archived ones).
    func scratchPaletteItems(query: String) -> [OverlayItem] {
        var ids: [String] = []
        if let a = current, let pid = scratchID(of: a) { ids.append(pid) }
        if !query.isEmpty {
            let all = catalog.projects.values.filter { ScratchLifecycle.isLifecycle($0) }
                .sorted { ($0.lastUsed ?? .distantPast, $0.id) > ($1.lastUsed ?? .distantPast, $1.id) }
            for p in all where !ids.contains(p.id) { ids.append(p.id) }
        }
        var out: [OverlayItem] = []
        for pid in ids {
            let name = scratchName(pid)
            for item in scratchActions(pid) {
                let title = Self.paletteTitle(item.action, name: name)
                out.append(OverlayItem(id: "scratch:\(item.action.rawValue):\(pid)", section: "Scratch", title: title,
                                       detail: item.reason ?? "scratch", enabled: item.enabled,
                                       run: { [weak self] in self?.showPalette = false; self?.perform(item.action, scratch: pid) }))
            }
        }
        return out
    }

    static func paletteTitle(_ a: ScratchAction, name: String) -> String {
        switch a {
        case .rename: return "Rename scratch “\(name)”…"
        case .keep: return "Keep scratch “\(name)”"
        case .unkeep: return "Don't keep scratch “\(name)”"
        case .promote: return "Promote “\(name)” to a project…"
        case .archive: return "Archive scratch “\(name)”"
        case .restore: return "Restore scratch “\(name)”"
        case .delete: return "Delete scratch “\(name)”…"
        }
    }

    /// The scratch's actions as menu items (sidebar, tile menu).
    func addScratchItems(_ pid: String, to menu: NSMenu, window: NSWindow? = nil) {
        for item in scratchActions(pid) {
            let act = MenuAction { [weak self] in self?.perform(item.action, scratch: pid, window: window) }
            let i = NSMenuItem(title: item.action.title, action: item.enabled ? #selector(MenuAction.run) : nil, keyEquivalent: "")
            i.target = act
            i.representedObject = act
            i.isEnabled = item.enabled
            if let r = item.reason { i.toolTip = r }
            menu.addItem(i)
        }
    }
}
