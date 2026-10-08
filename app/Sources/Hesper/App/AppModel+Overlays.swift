import AppKit
import HesperCore

/// What can be undone for 6 s (the toast, ⌘Z).
enum UndoAction: Sendable, Equatable {
    /// Closed agents (⌘W, tidy up): reopened from their sessions.
    case reopen([ClosedAgent])
    case moveBack(String, to: String)
    case restoreDraft(Draft)
    /// Shared history: a deleted session (hesperd keeps it 30 s).
    case restoreSession(String)
}

/// An agent answered from the ⌘J inbox: the state it answered (a new
/// question from it is a new item) and when (keys right after an answer
/// are ignored for `settle` seconds).
struct QueueAnswer: Equatable {
    var since: Date?
    var at: Date
    static let settle: TimeInterval = 0.35
}

/// One popover at a time; each attaches to the thing it acts on.
enum PopoverKind: Equatable {
    /// ⌘⇧M: from the tile.
    case move(String)
    case rename(String)
    /// The toolbar's pills and layout button; the background tray.
    case attention, machines, layout, background
    /// The wall's scope pill (window layer: content from AppModel.scopeContent).
    case scope
    /// ⌃⌘D: the desk picker (desks: content from AppModel.deskContent).
    case desks
    /// Save Desk As… ("") / Rename (the desk id): a name field.
    case deskName(String)
    /// A draft's chip (draft id, which chip).
    case chip(String, TokenKind)

    var key: String {
        switch self {
        case .move: return "move"
        case .rename(let id): return "rename:\(id)"
        case .attention: return "attention"
        case .machines: return "machines"
        case .layout: return "layout"
        case .background: return "background"
        case .scope: return "scope"
        case .desks: return "desks"
        case .deskName(let id): return "deskName:\(id)"
        case .chip(let id, let k): return "chip:\(id):\(k.rawValue)"
        }
    }

    var draftID: String? { if case .chip(let id, _) = self { return id } else { return nil } }
    var agentID: String? {
        switch self {
        case .move(let id), .rename(let id): return id
        default: return nil
        }
    }
    /// Typing filters the list.
    var filterable: Bool {
        switch self {
        case .chip(_, let k): return k == .project || k == .branch || k == .profile || k == .machine
        case .move, .attention, .machines, .layout, .background, .rename, .scope, .desks, .deskName: return false
        }
    }
}

/// A row in a popover or the palette.
struct OverlayItem: Identifiable {
    var id: String
    var section: String? = nil
    var title: String
    var detail: String = ""
    var mark: String? = nil
    var dot: String? = nil
    /// An agent row's state: drawn as its StateMark (else `dot` is a plain
    /// square of that color).
    var stateMark: StateMarkKind? = nil
    var enabled = true
    var checked = false
    var run: @MainActor () -> Void
    /// ⌘⏎ (palette: open full size; attention: allow).
    var alternate: (@MainActor () -> Void)? = nil
    /// ⇥ (palette): this row's own actions.
    var actions: [OverlayItem] = []
}

struct PopoverContent {
    var title: String?
    var items: [OverlayItem]
    var note: String?
    var hints: [(String, String)]
    var empty: String = "Nothing here"
    /// A text field (rename, branch): placeholder and the binding's key.
    var field: String?
}

extension AppModel {
    // MARK: Popovers

    func openPopover(_ kind: PopoverKind) {
        if popover == kind { closePopover(); return }
        guard let anchor = anchorProvider?(kind) else { return }
        showPalette = false
        popoverAnchor = anchor
        // A choice list opens with the highlight on the current choice (the
        // layout popover on this wall's arrangement), not on its first row.
        if !kind.filterable {
            popoverLists[kind.key] = OverlayList.opening(checked: popoverContent(kind).items.map(\.checked))
        } else if popoverLists[kind.key] == nil {
            popoverLists[kind.key] = OverlayList()
        }
        popover = kind
    }

    func closePopover() { popover = nil }

    var popoverList: OverlayList {
        get { popover.flatMap { popoverLists[$0.key] } ?? OverlayList() }
        set { if let p = popover { popoverLists[p.key] = newValue } }
    }

    func popoverContent(_ kind: PopoverKind) -> PopoverContent {
        let q = popoverLists[kind.key]?.query ?? ""
        func filter(_ items: [OverlayItem]) -> [OverlayItem] {
            guard kind.filterable, !q.isEmpty else { return items }
            return items.filter { ComposerCompletion.score(q, $0.title + " " + $0.detail) != nil }
        }
        switch kind {
        case .move(let id):
            guard let a = agent(id) else { return PopoverContent(title: nil, items: [], note: nil, hints: []) }
            let items = machines.filter { $0.short != a.machine }.map { m in
                OverlayItem(id: m.short, title: m.displayName, detail: ComposerCompletion.machineDetail(m, local: false), mark: m.short,
                            enabled: m.online, run: { [weak self] in self?.move(a, to: m.short) })
            }
            return PopoverContent(title: "Move \(a.name) to", items: items,
                                  note: a.kind == "shell" ? "Shells do not move" : "with conversation + uncommitted work",
                                  hints: [("⏎", "move"), ("esc", "close")], empty: "No other machines")
        case .rename(let id):
            return PopoverContent(title: "Rename \(agent(id)?.name ?? "")", items: [], note: nil, hints: [("⏎", "rename"), ("esc", "close")], field: "Name")
        case .attention:
            // Drawn by AttentionPanel (Overlays.swift) from attentionEntries();
            // these rows serve the generic list code (selection, tests).
            let items = attentionEntries().map { e -> OverlayItem in
                OverlayItem(id: e.id, title: e.agent.name, detail: ([e.agent.machine] + e.whereabouts).joined(separator: " · "),
                            stateMark: StateMarkKind(e.agent.state),
                            run: { [weak self] in self?.openFromQueue(e.agent) },
                            alternate: { [weak self] in if let p = InboxKeys.primary(e.answers) { self?.answerFromQueue(e, p) } })
            }
            return PopoverContent(title: "Needs you · \(items.count)", items: items, note: nil, hints: AttentionPanel.hints, empty: AttentionPanel.emptyText)
        case .machines:
            let items = machines.map { m -> OverlayItem in
                let n = wall.filter { $0.machine == m.short }.count
                let route = m.route == "local" ? "this Mac" : (m.online ? [m.route ?? "", m.rttMs.map { "\(Int($0)) ms" } ?? ""].filter { !$0.isEmpty }.joined(separator: " · ") : "offline")
                return OverlayItem(id: m.short, title: m.displayName, detail: "\(route) · \(n) agent\(n == 1 ? "" : "s")", mark: m.short,
                                   dot: m.online ? Theme.Token.done.hex : Theme.Token.dim.hex, enabled: m.online,
                                   run: { [weak self] in self?.closePopover(); self?.newDraft(machine: m.short) })
            }
            return PopoverContent(title: nil, items: items,
                                  note: "Pair another Mac: hesperctl pair-host --machine \(localMachine) there, then approve it here",
                                  hints: [("⏎", "new agent there"), ("esc", "close")])
        case .layout:
            var items = LayoutSwitcher(arrangement).rows.map { r in
                OverlayItem(id: r.arrangement.rawValue, title: r.title, detail: r.shortcut, mark: nil, checked: r.checked,
                            run: { [weak self] in self?.arrangement = r.arrangement })
            }
            for n in [60, 80, 100] {
                items.append(OverlayItem(id: "min\(n)", section: "Min width", title: "Min width: \(n) characters", detail: n == 60 ? "dense" : "",
                                         checked: minChars == n, run: { [weak self] in self?.minChars = n }))
            }
            // Projects (views): bands one level below the scope, or the user's choice.
            if wallScope != nil {
                let auto = ViewGrouping.level(.auto, scope: scope, hasGroups: catalog.hasGroups)
                for g in Grouping.allCases {
                    let detail = g == .auto ? "now \(auto == .branch ? "by branch / package" : "by \(auto.rawValue)")" : ""
                    items.append(OverlayItem(id: "grouping:\(g.rawValue)", section: "Grouping", title: "Grouping: \(g.title)", detail: detail,
                                             checked: grouping == g, run: { [weak self] in self?.grouping = g }))
                }
                // Desks: projects / groups that have their own wall window.
                for m in OwnWallMode.allCases {
                    let detail = m == .collapsed ? "one line, click shows it" : m == .full ? "tiles here too" : ""
                    items.append(OverlayItem(id: "own:\(m.rawValue)", section: "Own walls", title: "Own wall: \(m.title)", detail: detail,
                                             checked: ownWallMode == m, run: { [weak self] in self?.ownWallMode = m }))
                }
            }
            return PopoverContent(title: nil, items: items, note: nil, hints: [("⏎", "choose"), ("⌘⏎", "keep open"), ("esc", "close")])
        case .background:
            // Drawn by BackgroundPanel (Overlays.swift); these rows serve the
            // generic list code (↑↓ ⏎, tests).
            let names = MachineLabel.names(machines)
            let items = backgroundAgents.map { a in
                OverlayItem(id: a.id, title: a.name, detail: BackgroundTray.meta(a, machine: names[a.machine] ?? a.machine),
                            stateMark: StateMarkKind(a.state), run: { [weak self] in self?.bringBackFromTray(a) })
            }
            return PopoverContent(title: backgroundSummary.title, items: items, note: nil, hints: BackgroundPanel.hints, empty: "Nothing in the background")
        case .scope: // window layer
            return scopeContent?() ?? PopoverContent(title: nil, items: [], note: nil, hints: [])
        case .desks, .deskName: // desks
            return deskContent?(kind) ?? PopoverContent(title: nil, items: [], note: nil, hints: [])
        case .chip(let draftID, let tk):
            return chipContent(draftID, tk, query: q, filter: filter)
        }
    }

    private func chipContent(_ draftID: String, _ tk: TokenKind, query q: String, filter: ([OverlayItem]) -> [OverlayItem]) -> PopoverContent {
        let c = composer(for: draftID)
        let r = c.resolution
        let ctx = composerContext
        let hints = [("⏎", "choose"), ("type", "filter"), ("esc", "close")]
        switch tk {
        case .machine:
            let items = ctx.machines.map { m in
                OverlayItem(id: m.short, title: m.displayName, detail: ComposerCompletion.machineDetail(m, local: m.short == localMachine), mark: m.short,
                            enabled: m.online, checked: m.short == r.machine,
                            run: { [weak self] in c.choose(.machine, value: m.short); self?.closePopover() })
            }
            return PopoverContent(title: "Run on", items: filter(items), note: nil, hints: hints)
        case .project:
            if ComposerCompletion.isGitURL(q) {
                let item = OverlayItem(id: "clone", title: "Clone \(ComposerCompletion.repoName(q)) and start", detail: q, mark: "⤓",
                                       run: { [weak self] in
                                           self?.closePopover()
                                           c.complete(.project)
                                           if let t = ComposerParser.token(at: c.caret, in: c.draft.text) { Task { await c.cloneProject(q, token: t) } }
                                       })
                return PopoverContent(title: "Project", items: [item], note: nil, hints: hints)
            }
            let items = (groupFolderChoices(machine: r.machine) ?? ctx.projects).prefix(60).map { p in // a group window: its projects
                OverlayItem(id: p.path, title: p.name, detail: ComposerCompletion.abbreviate((p.path as NSString).deletingLastPathComponent) + (p.recent ? " · recent" : ""),
                            checked: p.path == r.project, run: { [weak self] in c.choose(.project, value: p.path); self?.closePopover() })
            }
            // A typed path ("~/projects/new-thing"): that folder, existing
            // or not (a missing one is created on start).
            var typed: [OverlayItem] = []
            if let path = DraftSeed.typedFolder(q) {
                var dir: ObjCBool = false
                let exists = FileManager.default.fileExists(atPath: path, isDirectory: &dir) && dir.boolValue
                typed.append(OverlayItem(id: "folder:\(path)", title: (path as NSString).lastPathComponent,
                                         detail: ComposerCompletion.abbreviate(path) + (exists ? "" : " · new folder"), mark: "/",
                                         checked: path == r.project, run: { [weak self] in c.choose(.project, value: path); self?.closePopover() }))
            }
            let pick = OverlayItem(id: "pick", title: "Choose folder…", detail: "Finder", mark: "…",
                                   run: { [weak self] in self?.closePopover(); self?.pickFolder(for: c) })
            return PopoverContent(title: "Folder", items: typed + filter(Array(items)) + [pick], note: "Type a path, or paste a git URL to clone it",
                                  hints: hints, empty: "No folder matches")
        case .profile:
            let items = ctx.profiles.keys.sorted().map { name in
                OverlayItem(id: name, title: name, detail: ComposerCompletion.profileDetail(name, kind: ctx.profiles[name] ?? ""),
                            checked: name == r.profile, run: { [weak self] in c.choose(.profile, value: name); self?.closePopover() })
            }
            return PopoverContent(title: "Profile", items: filter(items), note: nil, hints: hints)
        case .branch:
            var items: [OverlayItem] = []
            if !q.isEmpty {
                items.append(OverlayItem(id: "custom", title: q, detail: "new worktree on this branch", mark: "⑂",
                                         run: { [weak self] in c.choose(.branch, value: q); self?.closePopover() }))
            }
            let suggested = ComposerResolution.suggestBranch(r.name)
            if !suggested.isEmpty {
                items.append(OverlayItem(id: "suggested", title: suggested, detail: "suggested from the first line", mark: "⑂",
                                         checked: r.worktree && r.branch == suggested,
                                         run: { [weak self] in c.choose(.branch, value: suggested); self?.closePopover() }))
            }
            items.append(OverlayItem(id: "none", title: "No worktree", detail: "work in the project itself", mark: "–", checked: !r.worktree,
                                     run: { [weak self] in
                                         var d = c.draft
                                         if r.worktree { c.toggleWorktree() } else { d.worktree = false; self?.editDraftContent(d) }
                                         self?.closePopover()
                                     }))
            return PopoverContent(title: "Worktree", items: items, note: nil, hints: [("⏎", "choose"), ("type", "branch name"), ("⌘⇧W", "toggle")])
        }
    }

    /// "Choose folder…": the system folder picker; the choice is the
    /// draft's folder (silently, like a chip choice).
    func pickFolder(for c: ComposerModel) {
        let panel = NSOpenPanel()
        panel.canChooseDirectories = true
        panel.canChooseFiles = false
        panel.canCreateDirectories = true
        panel.allowsMultipleSelection = false
        panel.prompt = "Choose"
        panel.message = "The folder the agent works in"
        let root = ProcessInfo.processInfo.environment["HESPER_PROJECT_ROOT"] ?? NSHomeDirectory() + "/projects"
        panel.directoryURL = URL(fileURLWithPath: c.resolution.project ?? root)
        let done: (NSApplication.ModalResponse) -> Void = { r in
            guard r == .OK, let url = panel.url else { return }
            c.choose(.project, value: url.path)
        }
        if let w = NSApp.keyWindow, !(w is NSPanel) { panel.beginSheetModal(for: w, completionHandler: done) } else { done(panel.runModal()) }
    }

    /// ↑↓ ⏎ ⌘⏎ esc and typing while a popover is open. False: not its key.
    func popoverKey(_ action: OverlayKeyAction) -> Bool {
        guard let kind = popover else { return false }
        if kind == .attention { return attentionKey(action) }
        if kind.agentID != nil, case .rename = kind {
            if action == .close { closePopover(); return true }
            return false // the text field has the keys
        }
        if case .deskName = kind {
            if action == .close { closePopover(); return true }
            return false // the text field has the keys
        }
        let content = popoverContent(kind)
        var list = popoverList
        let enabled = content.items.map(\.enabled)
        switch action {
        case .up: list.move(-1, enabled: enabled)
        case .down: list.move(1, enabled: enabled)
        case .activate, .actOn:
            if let i = list.selection(enabled: enabled) {
                let item = content.items[i]
                if kind != .layout { closePopover() }
                item.run()
                if kind == .layout { closePopover() }
            } else if action == .activate, kind.filterable { return true }
            return true
        case .alternate:
            if let i = list.selection(enabled: enabled) {
                let item = content.items[i]
                if let alt = item.alternate { alt() } else { item.run() }
            }
            return true
        case .close:
            closePopover()
            return true
        case .back:
            return true
        case .type(let s):
            guard kind.filterable else { return false }
            list.type(s)
        case .deleteBackward:
            guard kind.filterable else { return false }
            list.deleteBackward()
        case .pass:
            return false
        }
        popoverList = list
        return true
    }

    // MARK: ⌘J: the attention inbox

    /// One item of the inbox: the agent, its exact question, the answers
    /// it offers inline, where it is.
    struct AttentionEntry: Identifiable {
        var agent: Agent
        var id: String { agent.id }
        var question: String
        var answers: [InboxAnswer]
        /// "other wall", "other Mac" (the machine itself is always shown).
        var whereabouts: [String]
        var since: String
        /// Answered from here; waiting for the agent to move on.
        var answered: Bool
    }

    /// The queue (approval, question, error; oldest first) as inbox items.
    func attentionEntries() -> [AttentionEntry] {
        let queue = AttentionQueue.ordered(registry.agents.values)
        guard !queue.isEmpty else { return [] }
        let here = Set(wall.map(\.id)).subtracting(homeNeedsYou.map(\.id))
        let local = localMachine
        return queue.map { a in
            AttentionEntry(agent: a,
                           question: AttentionInbox.question(state: a.state, attention: a.attention),
                           answers: AttentionInbox.answers(state: a.state, attention: a.attention),
                           whereabouts: (isBackground(a) ? ["background"] : [])
                               + AttentionInbox.whereabouts(machine: a.machine, local: local, onThisWall: here.contains(a.id)),
                           since: Theme.elapsed(since: a.stateSince) ?? "",
                           answered: queueAnswered[a.id].map { $0.since == a.stateSince } ?? false)
        }
    }

    /// ⌘J: opens the inbox under the toolbar's attention pill (again:
    /// closes it). Nothing in the queue: says so. No pill to hang it on
    /// (an agent window): goes to the next agent, as before.
    func toggleAttentionQueue() {
        if popover == .attention { closePopover(); return }
        pruneQueueAnswered()
        guard !AttentionQueue.ordered(registry.agents.values).isEmpty else {
            closePopover()
            showToast(AttentionPanel.emptyText)
            return
        }
        guard anchorProvider?(.attention) != nil else { goToNextAttention(); return }
        leaveComposer() // the inbox takes the keys (a composer would keep them)
        openPopover(.attention)
    }

    /// Where ⌘J went before the inbox (and still goes without a toolbar):
    /// the next agent needing you, in its own window if it has one.
    func goToNextAttention() {
        closePopover()
        guard let next = AttentionQueue.next(registry.agents.values, after: current?.id) else {
            showToast(AttentionPanel.emptyText)
            return
        }
        leaveComposer()
        if routeAttention?(next) == true { return }
        if mode == .focus { focus(next.id) } else { activate(next.id) }
    }

    /// The inbox's keys: J/K ↑↓ move, 1–9 answer, ⏎ primary, ⌘⏎ open, esc.
    /// Typing never falls through to the wall while it is open.
    func attentionKey(_ action: OverlayKeyAction) -> Bool {
        let entries = attentionEntries()
        var list = popoverList
        let enabled = entries.map { _ in true }
        let sel = list.selection(enabled: enabled)
        switch InboxKeys.route(action) {
        case .move(let d):
            list.move(d, enabled: enabled)
            popoverList = list
        case .answer(let n):
            if let sel, let a = InboxKeys.answer(n, in: entries[sel].answers) { answerFromQueue(entries[sel], a) }
        case .primary:
            if let sel, let a = InboxKeys.primary(entries[sel].answers) { answerFromQueue(entries[sel], a) }
        case .open:
            if let sel { openFromQueue(entries[sel].agent) }
        case .close:
            closePopover()
        case .none:
            if case .type = action { return true }
            return false
        }
        return true
    }

    /// ⌘O: the selected item's tile.
    func attentionOpenSelected() -> Bool { attentionKey(.alternate) }

    /// Selects an item (a click on it).
    func selectAttention(_ id: String) {
        guard let i = attentionEntries().firstIndex(where: { $0.id == id }) else { return }
        var l = popoverList
        l.index = i
        popoverList = l
    }

    /// An inline answer: a decision (agents.answer) or a numbered option's
    /// key (agents.input); "Open" opens the tile. An item answered from
    /// here takes no second answer, and keys right after an answer are
    /// ignored (the next item slides up under the same key).
    func answerFromQueue(_ e: AttentionEntry, _ answer: InboxAnswer) {
        if case .open = answer.action { openFromQueue(e.agent); return }
        pruneQueueAnswered()
        guard !e.answered, queueAnswered[e.id] == nil else { return }
        if let last = queueAnswered.values.map(\.at).max(), Date().timeIntervalSince(last) < QueueAnswer.settle { return }
        queueAnswered[e.id] = QueueAnswer(since: e.agent.stateSince, at: Date())
        let a = e.agent
        switch answer.action {
        case .decision(let d):
            if d == .deny { denyOpen = nil; denyText[a.id] = nil }
            Task { @MainActor in
                do { try await self.client.answer(a.id, decision: d) } catch {
                    self.queueAnswered[a.id] = nil
                    self.showToast("Could not answer \(a.name): \(self.describe(error))", error: true)
                }
            }
        case .keys(let k):
            sendInput(a.id, k)
        case .open:
            break
        }
    }

    /// The agent's tile (its own window when it has one), the inbox closed.
    func openFromQueue(_ a: Agent) {
        closePopover()
        leaveComposer()
        bringBackIfNeeded(a.id) // a background agent that needs you comes back
        if routeAttention?(a) == true { return }
        if mode == .focus { focus(a.id) } else { activate(a.id) }
    }

    /// Drops answered marks of agents that left the queue (or asked again).
    private func pruneQueueAnswered() {
        guard !queueAnswered.isEmpty else { return }
        for (id, m) in queueAnswered {
            if let a = agent(id), a.state.needsAttention, a.stateSince == m.since { continue }
            queueAnswered[id] = nil
        }
    }

    /// ⏎ in the attention list: the agent's tile, selected and in view.
    func goTo(_ id: String) {
        leaveComposer()
        if mode == .focus { focus(id) } else { activate(id) }
    }

    // MARK: Palette

    func togglePalette() {
        closePopover()
        if showPalette { showPalette = false; return }
        leaveComposer()
        paletteScope = nil
        showPalette = true
    }

    /// ⌘K: agents (each with its actions), projects, machines, actions,
    /// layouts, settings. Searching an agent also offers its actions.
    func paletteItems() -> [OverlayItem] {
        if let scope = paletteScope, let a = agent(scope) {
            return agentActions(a).map { var i = $0; i.section = "Actions for \(a.name)"; return i }
        }
        var out: [OverlayItem] = []
        for a in wall {
            out.append(OverlayItem(id: "agent:\(a.id)", section: "Agents", title: a.name,
                                   detail: "\(a.machine) · \(a.attention?.kind == "approval" ? "needs approval" : a.state.label)",
                                   dot: Theme.stateHex(a.state), stateMark: StateMarkKind(a.state),
                                   run: { [weak self] in self?.showPalette = false; self?.goTo(a.id) },
                                   alternate: { [weak self] in self?.showPalette = false; self?.focus(a.id) },
                                   actions: agentActions(a)))
        }
        for d in drafts.all {
            out.append(OverlayItem(id: "draft:\(d.id)", section: "Drafts", title: ComposerResolution.firstLine(d.text).isEmpty ? "Empty draft" : ComposerResolution.firstLine(d.text),
                                   detail: "draft", dot: Theme.Token.working.hex, stateMark: .starting,
                                   run: { [weak self] in self?.showPalette = false; self?.editDraft(d.id) }))
        }
        var actions: [OverlayItem] = [
            OverlayItem(id: "a:new", title: "New agent", detail: "⌘N", run: { [weak self] in self?.showPalette = false; self?.newDraft() }),
            OverlayItem(id: "a:next", title: "Next needing attention", detail: "⌘J", run: { [weak self] in self?.showPalette = false; self?.perform(.nextAttention) }),
            OverlayItem(id: "a:quick", title: "Quick launch", detail: settings.hotKey.label, run: { [weak self] in self?.showPalette = false; self?.onQuickLaunch?() }),
            OverlayItem(id: "a:tidy", title: "Tidy up: close finished agents", detail: "⇧⌘W", run: { [weak self] in self?.showPalette = false; self?.tidyUp() }),
        ]
        if let top = closeBook.reopen.top {
            actions.append(OverlayItem(id: "a:reopen", title: "Reopen \(top.name)", detail: "⇧⌘T", run: { [weak self] in self?.showPalette = false; self?.reopenLast() }))
        }
        // The selected agent's actions, and those of agents the query names.
        let q = paletteList.query.trimmingCharacters(in: .whitespaces)
        var named: [Agent] = []
        if let cur = current { named.append(cur) }
        if !q.isEmpty {
            for a in wall where a.id != current?.id && ComposerCompletion.score(q, a.name) != nil && ComposerCompletion.score(q, a.name)! < 2 { named.append(a) }
        }
        for a in named.prefix(3) { actions += agentActions(a) }
        for var a in actions { a.section = "Actions"; out.append(a) }
        for p in recentProjects {
            out.append(OverlayItem(id: "project:\(p.path)", section: "Projects", title: p.name, detail: ComposerCompletion.abbreviate(p.path),
                                   run: { [weak self] in self?.showPalette = false; self?.newDraft(project: p.path) }))
        }
        for m in machines {
            out.append(OverlayItem(id: "machine:\(m.short)", section: "Machines", title: m.displayName, detail: ComposerCompletion.machineDetail(m, local: m.short == localMachine),
                                   mark: m.short, dot: m.online ? Theme.Token.done.hex : Theme.Token.dim.hex, enabled: m.online,
                                   run: { [weak self] in self?.showPalette = false; self?.newDraft(machine: m.short) }))
        }
        for (i, a) in WallArrangement.allCases.enumerated() {
            out.append(OverlayItem(id: "layout:\(a.rawValue)", section: "Layouts", title: "Layout: \(a.title)", detail: "⌥⌘\(i + 1)", checked: a == arrangement,
                                   run: { [weak self] in self?.showPalette = false; self?.arrangement = a }))
        }
        out += historyPaletteItems() // shared history: "History" (async FTS results)
        out.append(OverlayItem(id: "s:settings", section: "Settings", title: "Settings…", detail: "⌘,", run: { [weak self] in self?.showPalette = false; self?.onShowSettings?() }))
        for n in [60, 80, 100] {
            out.append(OverlayItem(id: "s:min\(n)", section: "Settings", title: "Minimum card width: \(n) characters", checked: minChars == n,
                                   run: { [weak self] in self?.showPalette = false; self?.minChars = n }))
        }
        out.append(OverlayItem(id: "s:notify", section: "Settings", title: settings.notificationsEnabled ? "Turn notifications off" : "Turn notifications on",
                               run: { [weak self] in self?.showPalette = false; self?.settings.notificationsEnabled.toggle() }))
        return out
    }

    func agentActions(_ a: Agent) -> [OverlayItem] {
        var out: [OverlayItem] = [
            OverlayItem(id: "open:\(a.id)", title: "Open \(a.name)", detail: "⌘↩", run: { [weak self] in self?.showPalette = false; self?.focus(a.id) }),
        ]
        if a.state == .approval {
            out.append(OverlayItem(id: "allow:\(a.id)", title: "Allow \(a.attention?.title ?? "") for \(a.name)", detail: "⏎ on the tile",
                                   run: { [weak self] in self?.showPalette = false; self?.answer(a, .allow) }))
        }
        out.append(OverlayItem(id: "close:\(a.id)", title: "Close \(a.name)", detail: "⌘W", run: { [weak self] in self?.showPalette = false; self?.requestClose(a) }))
        if a.isRunning {
            if isBackground(a) {
                out.append(OverlayItem(id: "back:\(a.id)", title: "Bring \(a.name) back", run: { [weak self] in self?.showPalette = false; self?.bringBackFromTray(a) }))
            } else {
                out.append(OverlayItem(id: "background:\(a.id)", title: "Send \(a.name) to the background", detail: "⌥⌘W",
                                       run: { [weak self] in self?.showPalette = false; self?.sendToBackground(a) }))
            }
            out.append(OverlayItem(id: "kill:\(a.id)", title: "Kill \(a.name)", detail: "⌃⌘W", run: { [weak self] in self?.showPalette = false; self?.kill(a) }))
        } else {
            out.append(OverlayItem(id: "resume:\(a.id)", title: "Resume \(a.name)", detail: "⏎", run: { [weak self] in self?.showPalette = false; self?.resume(a) }))
        }
        out.append(OverlayItem(id: "move:\(a.id)", title: "Move \(a.name) to another Mac", detail: "⌘⇧M",
                               run: { [weak self] in self?.showPalette = false; self?.selectedID = a.id; self?.onModeChanged?(); self?.openPopover(.move(a.id)) }))
        out.append(OverlayItem(id: "rename:\(a.id)", title: "Rename \(a.name)", run: { [weak self] in
            self?.showPalette = false
            self?.selectedID = a.id
            self?.onModeChanged?()
            self?.renameText[a.id] = a.name
            self?.openPopover(.rename(a.id))
        }))
        return out
    }

    func filteredPalette() -> [OverlayItem] {
        let q = paletteList.query.trimmingCharacters(in: .whitespaces).lowercased()
        let all = paletteItems()
        guard !q.isEmpty else { return all }
        // History rows matched the full text already (hesperd's FTS).
        return all.filter { $0.section == "History" || ComposerCompletion.score(q, "\($0.title) \($0.detail) \($0.section ?? "")") != nil }
            .enumerated()
            .sorted { l, r in
                let ls = ComposerCompletion.score(q, l.element.title) ?? 3, rs = ComposerCompletion.score(q, r.element.title) ?? 3
                let lsec = Self.sectionOrder(l.element.section), rsec = Self.sectionOrder(r.element.section)
                if lsec != rsec { return lsec < rsec }
                if ls != rs { return ls < rs }
                return l.offset < r.offset
            }
            .map(\.element)
    }

    static func sectionOrder(_ s: String?) -> Int {
        switch s {
        case "Agents": return 0
        case "Drafts": return 1
        case "Actions": return 2
        case "Projects": return 3
        case "History": return 4 // shared history
        case "Machines": return 5
        case "Layouts": return 6
        default: return 7
        }
    }

    func paletteKey(_ action: OverlayKeyAction) -> Bool {
        let items = filteredPalette()
        var list = paletteList
        let enabled = items.map(\.enabled)
        switch action {
        case .up: list.move(-1, enabled: enabled)
        case .down: list.move(1, enabled: enabled)
        case .activate:
            if let i = list.selection(enabled: enabled) { items[i].run() }
            return true
        case .alternate:
            if let i = list.selection(enabled: enabled) { (items[i].alternate ?? items[i].run)() }
            return true
        case .actOn:
            if paletteTab(selectedID: list.selection(enabled: enabled).map { items[$0].id }) { return true } // search surface: ⇥ → History scope (not on an agent)
            if let i = list.selection(enabled: enabled), case let id = items[i].id, id.hasPrefix("agent:") {
                paletteScope = String(id.dropFirst(6))
                list = OverlayList()
            }
        case .back:
            if paletteScope != nil { paletteScope = nil; list = OverlayList(query: list.query) }
        case .close:
            if paletteScope != nil { paletteScope = nil } else { showPalette = false }
            return true
        default:
            return false
        }
        paletteList = list
        return true
    }

    // MARK: Move: at once, with undo (closing: AppModel+Closing)

    func move(_ a: Agent, to m: String, undoable: Bool = true) {
        closePopover()
        moving[a.id] = m
        onAgentsChanged?()
        let target = machine(m)?.displayName ?? m
        Task { @MainActor in
            do {
                let n = try await self.client.move(a.id, to: m)
                self.moving[a.id] = nil
                if let p = self.placements.removeValue(forKey: a.id) { self.placements[n.id] = p }
                if self.selectedID == a.id { self.selectedID = n.id }
                if undoable { self.pushUndo(.moveBack(n.id, to: a.machine), label: "Moved \(a.name) to \(target)") }
                self.onAgentsChanged?()
                self.onModeChanged?()
            } catch {
                self.moving[a.id] = nil
                self.onAgentsChanged?()
                if let e = error as? RPCError, e.kind == .unavailable {
                    self.showToast("Moving is not available: \(e.message)", error: true)
                } else {
                    self.showToast("Could not move \(a.name): \(self.describe(error))", error: true)
                }
            }
        }
    }

    // MARK: Undo

    @discardableResult
    func pushUndo(_ action: UndoAction, label: String) -> UUID {
        let e = undo.push(action, label: label)
        undoToast = undo.top()
        scheduleUndoExpiry()
        return e.id
    }

    func undoLast() {
        guard let e = undo.pop() else {
            showToast("Nothing to undo")
            return
        }
        perform(undo: e)
    }

    func undo(_ id: UUID) {
        guard let e = undo.take(id) else { return }
        perform(undo: e)
    }

    private func perform(undo e: UndoStack<UndoAction>.Entry) {
        undoToast = undo.top()
        switch e.action {
        case .reopen(let recs):
            reopen(recs)
        case .moveBack(let id, let to):
            if let a = agent(id) { move(a, to: to, undoable: false) }
        case .restoreDraft(let d):
            restoreDraft(d)
        case .restoreSession(let id): // shared history
            HistoryActions(model: self, hub: history).restore(id)
        }
    }

    private func scheduleUndoExpiry() {
        undoTask?.cancel()
        undoTask = Task { @MainActor [weak self] in
            while let self, let next = self.undo.nextExpiry {
                let wait = next.timeIntervalSinceNow
                if wait > 0 { try? await Task.sleep(nanoseconds: UInt64(wait * 1e9) + 1_000_000) }
                if Task.isCancelled { return }
                _ = self.undo.expire() // closes were sent at once: nothing to commit
                self.undoToast = self.undo.top()
            }
        }
    }

    /// App quit: what is still undoable just stays done (closes were
    /// sent at once).
    func commitPendingUndo() async {
        _ = undo.drain()
    }
}
