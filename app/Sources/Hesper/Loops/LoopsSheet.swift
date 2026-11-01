import AppKit
import HesperCore
import Observation
import SwiftUI

/// The Loops sheet's state (⌘L): the board, one loop's detail, or the
/// map; the selection, the field being typed and the inline error. The
/// board and the map are rebuilt when the store's version or the filter
/// moved (`sync`), never per render.
@MainActor
@Observable
final class LoopsSheetModel {
    enum Mode: Equatable { case board, detail(String), map }
    /// What the inline field types: a new step, a step's trigger, or the
    /// merge picker's filter.
    enum Field: Equatable { case addStep, trigger(item: String), merge }

    @ObservationIgnored let hub: LoopHub
    @ObservationIgnored private weak var app: AppModel?

    private(set) var mode: Mode = .board
    /// Where esc goes from a detail (the board or the map).
    @ObservationIgnored private var back: Mode = .board
    private(set) var board = LoopBoard(loops: [])
    /// ⇥: every loop, or only the wall's project or group.
    private(set) var allScope = true
    private(set) var map = LoopMap.build(scope: .all, loops: [:], agents: [])
    /// The map's loop tints (rebuilt with the map).
    private(set) var mapColors: [String: Theme.Token] = [:]
    /// The detail's selected row: a step's or a member's id.
    var row: String?
    var field: Field?
    var fieldText = ""
    var mergeIndex = 0
    private(set) var error: String?
    private(set) var busy = false

    @ObservationIgnored private var cache = LoopMapCache()
    @ObservationIgnored private var seen = -1
    @ObservationIgnored private var seenFilter: LoopBoardFilter?

    init(hub: LoopHub, app: AppModel) {
        self.hub = hub
        self.app = app
    }

    /// The wall's project or group, as a filter (nil: an unscoped wall).
    var scopeFilter: LoopBoardFilter? {
        switch app?.scope {
        case .project(let id): return LoopBoardFilter(project: id)
        case .group(let id): return LoopBoardFilter(group: id)
        default: return nil
        }
    }

    var scopeTitle: String? {
        guard let app else { return nil }
        switch app.scope {
        case .project(let id): return app.catalog.project(id)?.name ?? id
        case .group(let id): return app.catalog.groups[id]?.name ?? id
        default: return nil
        }
    }

    var filter: LoopBoardFilter { allScope ? LoopBoardFilter() : scopeFilter ?? LoopBoardFilter() }

    // MARK: Open, sync

    func open(select: String?) {
        error = nil
        field = nil
        allScope = scopeFilter == nil
        seen = -1
        sync()
        if let select, hub.loop(select)?.isActive == true {
            if !board.order.contains(select) { allScope = true; sync() }
            board.select(select)
        }
        mode = .board
    }

    func closed() { field = nil }

    /// Rebuilds what the store's version or the filter changed.
    func sync() {
        let store = hub.store
        let f = filter
        guard store.version != seen || f != seenFilter else { return }
        seen = store.version
        seenFilter = f
        board = LoopBoard(loops: store.loops.values, filter: f, catalog: app?.catalog ?? ProjectCatalog(),
                          loopLimit: store.settings.loopLimit, selected: board.selected)
        if mode == .map { rebuildMap() }
        if case .detail(let id) = mode, hub.loop(id) == nil { mode = back }
    }

    private func rebuildMap() {
        let store = hub.store
        let agents = app.map { Array($0.lists.registry.agents.values) } ?? []
        let catalog = app?.catalog ?? ProjectCatalog()
        let local = app?.localMachine
        // Live state rides on the loop's links (hesperd joins it), so the
        // store's version is the map's.
        let next = cache.map(LoopMapCache.Key(scope: .all, version: store.version)) {
            LoopMap.build(scope: .all, loops: store.loops, agents: agents, catalog: catalog, local: local)
        }
        guard next != map || mapColors.isEmpty else { return }
        map = next
        mapColors = LoopMapView.colors(next)
    }

    var detailLoop: Loop? { if case .detail(let id) = mode { return hub.loop(id) }; return nil }
    /// The loop the verbs act on: the detail's, else the board's selection.
    var target: Loop? { detailLoop ?? (mode == .board ? board.selectedLoop : nil) }

    /// The detail's rows in keyboard order: steps, loose ends, members.
    func rows(_ l: Loop) -> [String] { LoopActions.steps(l).map(\.id) + l.agentLinks.map(\.id) }

    var mergeTargets: [Loop] {
        guard let l = target else { return [] }
        return LoopActions.mergeTargets(for: l, in: hub.store.loops.values, query: field == .merge ? fieldText : "")
    }

    // MARK: Keys

    /// One sheet key; false: not the sheet's (esc on the board closes it).
    func perform(_ a: LoopsKeyAction) -> Bool {
        if field != nil { return fieldKey(a) }
        switch a {
        case .close:
            switch mode {
            case .board: return false
            case .detail: mode = back
            case .map: mode = .board
            }
            return true
        case .up, .down:
            let d = a == .up ? -1 : 1
            if let l = detailLoop { moveRow(l, d) } else if mode == .board { board.move(d) }
        case .left, .right:
            if mode == .board { board.moveColumn(a == .left ? -1 : 1) }
        case .open: openSelected()
        case .map:
            if mode == .map { mode = .board } else { back = .board; mode = .map; rebuildMap() }
        case .switchScope:
            guard scopeFilter != nil else { NSSound.beep(); return true }
            allScope.toggle()
            sync()
        case .done: save { LoopActions.done($0.id) }
        case .drop: save { LoopActions.drop($0.id) }
        case .park: save { LoopActions.park($0) }
        case .merge:
            guard target != nil else { NSSound.beep(); return true }
            startField(.merge)
        case .nextAgent: nextAgent()
        case .startDraft: startDraft()
        case .check: check()
        case .addStep:
            guard target != nil else { NSSound.beep(); return true }
            if let l = target, mode == .board { show(l.id) }
            startField(.addStep)
        case .trigger:
            guard let l = detailLoop, let r = row, let item = l.items.first(where: { $0.id == r }) else { NSSound.beep(); return true }
            startField(.trigger(item: r), text: LoopActions.triggerField(item, steps: LoopActions.steps(l)))
        default:
            return false
        }
        return true
    }

    private func fieldKey(_ a: LoopsKeyAction) -> Bool {
        switch a {
        case .cancelField, .close: field = nil
        case .commitField: commitField()
        case .up, .down:
            guard field == .merge else { return false }
            mergeIndex = max(0, min(mergeTargets.count - 1, mergeIndex + (a == .up ? -1 : 1)))
        default: return false
        }
        return true
    }

    func startField(_ f: Field, text: String = "") {
        error = nil
        fieldText = text
        mergeIndex = 0
        field = f
    }

    func show(_ id: String) {
        guard let l = hub.loop(id) else { return }
        if mode != .detail(id) { back = mode == .map ? .map : .board }
        board.select(id)
        mode = .detail(id)
        error = nil
        if row == nil || !rows(l).contains(row ?? "") { row = rows(l).first }
    }

    private func moveRow(_ l: Loop, _ d: Int) {
        let r = rows(l)
        guard !r.isEmpty else { return }
        let i = r.firstIndex { $0 == row } ?? (d > 0 ? -1 : r.count)
        row = r[max(0, min(r.count - 1, i + d))]
    }

    private func openSelected() {
        switch mode {
        case .board: if let id = board.selected { show(id) }
        case .map: break
        case .detail:
            guard let l = detailLoop, let r = row else { return }
            if let link = l.agentLinks.first(where: { $0.id == r }) {
                guard let id = link.agent, link.isLive, let app else { error = "That agent is gone: its record stays in the loop."; return }
                LoopsSheetHost.close(app)
                app.goTo(id)
            } else if let draft = l.items.first(where: { $0.id == r })?.draft, let app {
                guard app.drafts[draft] != nil else { error = "That draft is gone."; return }
                hub.noteDraft(draft, loop: l.id)
                LoopsSheetHost.close(app)
                app.editDraft(draft)
            }
        }
    }

    // MARK: Verbs

    private func save(_ patch: @escaping @Sendable (Loop) -> LoopPatch) {
        guard let l = target else { NSSound.beep(); return }
        let p = patch(l)
        let leaves = p.status == .done || p.status == .dropped
        run { try await $0.loopSave(p) }
        if leaves, case .detail = mode { mode = back }
    }

    private func check() {
        guard let l = detailLoop, let r = row, let item = l.items.first(where: { $0.id == r }) else { NSSound.beep(); return }
        let p = LoopActions.check(item)
        run { try await $0.loopItem(l.id, item: p); return nil }
    }

    private func commitField() {
        guard let f = field, let l = target else { field = nil; return }
        switch f {
        case .addStep:
            guard let p = LoopActions.step(fieldText, order: LoopActions.nextOrder(l)) else { NSSound.beep(); return }
            field = nil
            run { try await $0.loopItem(l.id, item: p); return nil }
        case .trigger(let id):
            guard let item = l.items.first(where: { $0.id == id }) else { field = nil; return }
            guard let p = LoopActions.trigger(fieldText, item: item, steps: LoopActions.steps(l)) else {
                error = "A trigger reads “after 2”, “pr <url>”, “url <url>”, “tag <name>” or “none”."
                return
            }
            field = nil
            run { try await $0.loopItem(l.id, item: p); return nil }
        case .merge:
            let list = mergeTargets
            guard list.indices.contains(mergeIndex) else { NSSound.beep(); return }
            let into = list[mergeIndex].id
            field = nil
            if case .detail = mode { mode = back }
            run { try await $0.loopMerge(into: into, from: l.id); return nil }
            board.select(into)
        }
    }

    /// N: a new draft in the loop's project; it starts in this loop.
    private func nextAgent() {
        guard let l = target, let app else { NSSound.beep(); return }
        LoopsSheetHost.close(app)
        app.newDraft(projectID: l.projects.first)
        if let d = app.editingDraftID { hub.noteDraft(d, loop: l.id) }
    }

    /// ⌘↩: the drafted next agent starts, in this loop.
    private func startDraft() {
        guard let l = target, let app else { NSSound.beep(); return }
        guard let item = LoopActions.draftedNext(l), let d = item.draft else {
            error = "No agent is drafted for this loop yet: N writes one."
            return
        }
        guard app.drafts[d] != nil else { error = "That draft is gone."; return }
        hub.noteDraft(d, loop: l.id)
        LoopsSheetHost.close(app)
        app.startDraft(d)
    }

    private func run(_ body: @escaping @Sendable (DaemonClient) async throws -> Loop?) {
        error = nil
        busy = true
        Task {
            let e = await hub.run(body)
            busy = false
            error = e
            sync()
        }
    }
}

// MARK: Views

/// ⌘L: the board (W1), a loop's detail (W2, steps as G2) or the map (G1),
/// in SheetChrome. The toolbar: scope, the "your move" count, Map; the
/// footer: the verbs of what is selected, each with its key cap.
struct LoopsSheetView: View {
    @Bindable var model: LoopsSheetModel
    var hub: LoopHub
    var onClose: () -> Void

    var body: some View {
        SheetChrome(title: title, symbol: "arrow.triangle.2.circlepath", count: hub.available ? board.order.count : nil,
                    closeIdentifier: "loops.close", toolbar: { toolbar }, footer: { footer }, onClose: onClose) {
            VStack(spacing: 0) {
                if let e = model.error { LoopsErrorLine(text: e) }
                content.frame(maxWidth: .infinity, maxHeight: .infinity)
                if hub.available, model.mode != .map { StewardLine(steward: hub.steward) }
            }
        }
        .onAppear { model.sync() }
        .onChange(of: hub.store.version) { model.sync() }
    }

    private var board: LoopBoard { model.board }

    private var title: String {
        if let l = model.detailLoop { return l.displayTitle }
        return model.mode == .map ? "Loops · Map" : "Loops"
    }

    @ViewBuilder private var content: some View {
        if !hub.loaded {
            LoopsEmpty(symbol: "hourglass", title: "Loading loops…", detail: "Hesper asks hesperd for every open loop.")
        } else if !hub.available {
            LoopsEmpty(symbol: "arrow.triangle.2.circlepath", title: "This hesperd has no loops yet",
                       detail: "Update hesperd on this Mac: every agent you start then opens a loop that stays until it is done.")
        } else {
            switch model.mode {
            case .board: LoopBoardView(model: model)
            case .detail(let id):
                if let l = hub.loop(id) { LoopDetailView(model: model, loop: l) }
            case .map: LoopMapView(map: model.map, colors: model.mapColors, onOpen: { model.show($0) })
            }
        }
    }

    @ViewBuilder private var toolbar: some View {
        if model.mode == .board, let scope = model.scopeTitle {
            Pill("All", variant: .segment(selected: model.allScope))
            Pill(scope, variant: .segment(selected: !model.allScope), kbd: CommandRegistry.cap(.loopsSwitchScope, in: .loops))
        }
        if let pill = board.pill { Pill(pill, mark: .question) }
        if let hint = board.limitHint { Text(hint).font(DS.font(.chrome)).foregroundStyle(Theme.dim).lineLimit(1) }
        Spacer()
        if model.mode == .detail(model.detailLoop?.id ?? "") {
            KeyButton("Back", kbd: SheetChromeView.closeKey) { _ = model.perform(.close) }
        }
        KeyButton(model.mode == .map ? "Board" : "Map", kbd: CommandRegistry.cap(.loopsMap, in: .loops)) { _ = model.perform(.map) }
            .accessibilityIdentifier("loops.map")
        if model.target != nil {
            KeyButton("New agent in loop", kbd: CommandRegistry.cap(.loopsNextAgent, in: .loops)) { _ = model.perform(.nextAgent) }
        }
    }

    @ViewBuilder private var footer: some View {
        if model.field != nil {
            Text(fieldHint).font(DS.font(.chrome)).foregroundStyle(Theme.dim)
            Spacer()
            KeyButton("Cancel", kbd: "esc") { _ = model.perform(.cancelField) }
            KeyButton(model.field == .merge ? "Merge" : "Save", kbd: "⏎", variant: .primary(.working)) { _ = model.perform(.commitField) }
        } else if let l = model.target {
            if model.mode == .board {
                KeyButton("Open", kbd: CommandRegistry.cap(.loopsOpen, in: .loops)) { _ = model.perform(.open) }
            } else {
                KeyButton("Check off", kbd: CommandRegistry.cap(.loopsCheck, in: .loops)) { _ = model.perform(.check) }
                KeyButton("Add step", kbd: CommandRegistry.cap(.loopsAddStep, in: .loops)) { _ = model.perform(.addStep) }
                KeyButton("Trigger", kbd: CommandRegistry.cap(.loopsTrigger, in: .loops)) { _ = model.perform(.trigger) }
            }
            Spacer()
            KeyButton(l.looksDone ? "Done (looks done)" : "Done", kbd: CommandRegistry.cap(.loopsDone, in: .loops),
                      variant: l.looksDone ? .primary(.done) : .quiet) { _ = model.perform(.done) }
            KeyButton(l.status == .parked ? "Unpark" : "Park", kbd: CommandRegistry.cap(.loopsPark, in: .loops)) { _ = model.perform(.park) }
            KeyButton("Drop", kbd: CommandRegistry.cap(.loopsDrop, in: .loops)) { _ = model.perform(.drop) }
            KeyButton("Merge", kbd: CommandRegistry.cap(.loopsMerge, in: .loops)) { _ = model.perform(.merge) }
            if LoopActions.draftedNext(l) != nil {
                KeyButton("Start drafted agent", kbd: CommandRegistry.cap(.loopsStartDraft, in: .loops), variant: .primary(.working)) {
                    _ = model.perform(.startDraft)
                }
            }
        }
    }

    private var fieldHint: String {
        switch model.field {
        case .addStep: return "“@agent …” or “@others …” names who owns it"
        case .trigger: return "after 2 · pr <url> · url <url> · tag <name> · none"
        case .merge: return "Merge into the loop you pick: its record keeps both"
        case nil: return ""
        }
    }
}

// MARK: Board (W1)

struct LoopBoardView: View {
    var model: LoopsSheetModel

    var body: some View {
        let board = model.board
        if board.isEmpty {
            LoopsEmpty(symbol: "checkmark.circle", title: model.allScope ? "No open loops" : "No open loops here",
                       detail: "Every agent you start opens a loop. It shows here until it is done, parked or dropped"
                           + (model.allScope ? "." : "; ⇥ shows every project."))
        } else {
            ScrollView(.horizontal) {
                HStack(alignment: .top, spacing: DS.Spacing.m) {
                    ForEach(LoopColumn.allCases) { c in column(c, board) }
                }
                .padding(DS.Spacing.l)
                .frame(minWidth: LoopsLook.columnMinWidth * CGFloat(LoopColumn.allCases.count))
            }
            .overlay(alignment: .bottom) { if model.field == .merge { MergePicker(model: model) } }
        }
    }

    private func column(_ c: LoopColumn, _ board: LoopBoard) -> some View {
        VStack(alignment: .leading, spacing: DS.Spacing.s) {
            HStack(spacing: DS.Spacing.s) {
                Text(LoopNextText.label(next(c))).font(DS.font(.chrome, .semibold)).foregroundStyle(Theme.fg2)
                Pill("", variant: .count, count: board.count(c))
                Spacer(minLength: 0)
            }
            .frame(height: DS.chromeMaxHeight)
            ScrollViewReader { proxy in
                ScrollView {
                    LazyVStack(spacing: DS.Spacing.s) {
                        ForEach(board.loops(c)) { l in
                            LoopCard(loop: l, selected: l.id == board.selected)
                                .id(l.id)
                                .onTapGesture { model.show(l.id) }
                        }
                        if board.count(c) == 0 { columnEmpty(c) }
                    }
                }
                .onChange(of: board.selected) { if let s = board.selected, board.loops(c).contains(where: { $0.id == s }) { proxy.scrollTo(s) } }
            }
        }
        .frame(minWidth: LoopsLook.columnMinWidth, maxWidth: .infinity, alignment: .top)
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("loops.column.\(c.rawValue)")
    }

    private func next(_ c: LoopColumn) -> LoopNext {
        switch c {
        case .you: return .you
        case .agents: return .agents
        case .others: return .others
        case .parked: return .parked
        }
    }

    private func columnEmpty(_ c: LoopColumn) -> some View {
        let text: String
        switch c {
        case .you: text = "Nothing waits on you"
        case .agents: text = "No agent is working on a loop"
        case .others: text = "Nothing waits on other people"
        case .parked: text = "P parks a loop until tomorrow"
        }
        return Text(text).font(DS.font(.chrome)).foregroundStyle(Theme.dim).frame(maxWidth: .infinity, alignment: .leading)
            .padding(DS.Spacing.m)
    }
}

/// A board card: title and age, the next step, the member chips (state
/// square and id) and the loose-end count. Selected: the `working` ring.
struct LoopCard: View {
    var loop: Loop
    var selected: Bool
    /// Member chips shown before "+N".
    static let maxMembers = 4

    var body: some View {
        VStack(alignment: .leading, spacing: DS.Spacing.xs) {
            HStack(alignment: .firstTextBaseline, spacing: DS.Spacing.s) {
                Text(loop.displayTitle).font(DS.font(.body, .medium)).foregroundStyle(Theme.fg).lineLimit(2)
                Spacer(minLength: DS.Spacing.xs)
                if let age = LoopWords.age(loop) { Text(age).font(DS.font(.meta)).foregroundStyle(Theme.dim) }
            }
            if loop.looksDone && loop.next == .you {
                Pill("Looks done", mark: .done)
            } else if let next = loop.nextText {
                Text(next).font(DS.font(.chrome)).foregroundStyle(loop.next == .you ? Theme.fg2 : Theme.dim).lineLimit(2)
            }
            HStack(spacing: DS.Spacing.s) {
                ForEach(loop.agentLinks.prefix(Self.maxMembers)) { k in MemberChip(link: k) }
                if loop.agentLinks.count > Self.maxMembers {
                    Text("+\(loop.agentLinks.count - Self.maxMembers)").font(DS.font(.meta)).foregroundStyle(Theme.dim)
                }
                Spacer(minLength: 0)
                let loose = loop.items.filter { $0.isOpen && $0.kind == .looseEnd }.count
                if loose > 0 { Text(loose == 1 ? "1 loose end" : "\(loose) loose ends").font(DS.font(.meta)).foregroundStyle(Theme.dim) }
            }
        }
        .padding(DS.Spacing.m)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(DS.Radius.shape(DS.Radius.control).fill(Theme.color(.tile)))
        .overlay(DS.Radius.shape(DS.Radius.control).strokeBorder(selected ? Theme.accent : Theme.stroke, lineWidth: selected ? DS.focusRing + 1 : 1))
        .contentShape(Rectangle())
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("loops.card.\(loop.id)")
        .accessibilityAddTraits(selected ? .isSelected : [])
    }
}

/// A member: its state square and its short id.
struct MemberChip: View {
    var link: LoopLink

    var body: some View {
        HStack(spacing: DS.Spacing.xs) {
            StateMark(link.isLive ? StateMarkKind(link.state ?? .idle) : .exited)
            Text(LoopWords.shortID(link)).font(DS.font(.meta)).foregroundStyle(Theme.fg2).lineLimit(1)
        }
    }
}

// MARK: Detail (W2, steps G2)

struct LoopDetailView: View {
    var model: LoopsSheetModel
    var loop: Loop

    var body: some View {
        let steps = LoopActions.steps(loop)
        HStack(alignment: .top, spacing: 0) {
            ScrollView {
                VStack(alignment: .leading, spacing: DS.Spacing.l) {
                    header
                    if let drafted = LoopActions.draftedNext(loop) { draftCard(drafted) }
                    stepsSection("Steps", steps.filter { $0.kind == .step }, all: steps)
                    stepsSection("Loose ends", steps.filter { $0.kind != .step }, all: steps)
                    if model.field == .addStep || isTriggerField { fieldRow }
                    if let r = LoopActions.rollUp(loop) { Text(r).font(DS.font(.chrome)).foregroundStyle(Theme.dim) }
                    if !loop.offers.isEmpty { offers }
                }
                .padding(DS.Spacing.l)
                .frame(maxWidth: .infinity, alignment: .leading)
            }
            Rectangle().fill(Theme.stroke).frame(width: 1)
            ScrollView {
                VStack(alignment: .leading, spacing: DS.Spacing.l) {
                    members
                    timeline
                    links
                }
                .padding(DS.Spacing.l)
                .frame(maxWidth: .infinity, alignment: .leading)
            }
            .frame(width: LoopsLook.listWidth)
        }
        .overlay(alignment: .bottom) { if model.field == .merge { MergePicker(model: model) } }
        .accessibilityIdentifier("loops.detail")
    }

    private var isTriggerField: Bool { if case .trigger = model.field { return true }; return false }

    private var header: some View {
        VStack(alignment: .leading, spacing: DS.Spacing.s) {
            HStack(spacing: DS.Spacing.s) {
                Pill(LoopWords.nextLabel(loop), mark: LoopWords.mark(loop))
                if let age = LoopWords.age(loop) { Text(age).font(DS.font(.meta)).foregroundStyle(Theme.dim) }
                Spacer(minLength: 0)
            }
            labeled("Goal", loop.goal ?? "No goal yet: the Steward or you can write one (hesperctl loops rename).")
            labeled("Done when", loop.doneWhen ?? "Not said yet")
            if let n = loop.nextText { labeled("Next", n) }
        }
    }

    private func labeled(_ label: String, _ text: String) -> some View {
        HStack(alignment: .firstTextBaseline, spacing: DS.Spacing.m) {
            Text(label).font(DS.font(.meta)).foregroundStyle(Theme.dim).frame(width: LoopsLook.labelWidth, alignment: .leading)
            Text(text).font(DS.font(.body)).foregroundStyle(Theme.fg).fixedSize(horizontal: false, vertical: true)
        }
    }

    private func draftCard(_ item: LoopItem) -> some View {
        HStack(spacing: DS.Spacing.m) {
            StateMark(.starting)
            VStack(alignment: .leading, spacing: DS.Spacing.xxs) {
                Text("Next agent, drafted").font(DS.font(.meta)).foregroundStyle(Theme.dim)
                Text(item.text).font(DS.font(.body)).foregroundStyle(Theme.fg).lineLimit(2)
            }
            Spacer(minLength: DS.Spacing.m)
            KeyButton("Start", kbd: CommandRegistry.cap(.loopsStartDraft, in: .loops), variant: .primary(.working)) { _ = model.perform(.startDraft) }
        }
        .padding(DS.Spacing.m)
        .background(DS.Radius.shape(DS.Radius.control).fill(Theme.color(.tile)))
        .overlay(DS.Radius.shape(DS.Radius.control).strokeBorder(Theme.stroke, lineWidth: 1))
    }

    @ViewBuilder private func stepsSection(_ title: String, _ items: [LoopItem], all: [LoopItem]) -> some View {
        if !items.isEmpty || title == "Steps" {
            VStack(alignment: .leading, spacing: DS.Spacing.xxs) {
                HStack {
                    Text(title).font(DS.font(.chrome, .semibold)).foregroundStyle(Theme.fg2)
                    Spacer()
                    Text("Owner").font(DS.font(.meta)).foregroundStyle(Theme.dim).frame(width: LoopsLook.ownerWidth, alignment: .leading)
                    Text("Trigger").font(DS.font(.meta)).foregroundStyle(Theme.dim).frame(width: LoopsLook.triggerWidth, alignment: .leading)
                }
                .padding(.horizontal, DS.Spacing.m)
                if items.isEmpty {
                    Text("No steps yet. A adds one; agents add theirs as they work.").font(DS.font(.chrome)).foregroundStyle(Theme.dim)
                        .padding(.horizontal, DS.Spacing.m)
                }
                ForEach(items) { it in stepRow(it, number: (all.firstIndex { $0.id == it.id } ?? 0) + 1, all: all) }
            }
        }
    }

    private func stepRow(_ it: LoopItem, number: Int, all: [LoopItem]) -> some View {
        let selected = model.row == it.id
        return HStack(spacing: DS.Spacing.m) {
            Text("\(number)").font(DS.font(.meta)).foregroundStyle(Theme.dim).frame(width: LoopsLook.numberWidth, alignment: .trailing)
            StateMark(it.state == .done ? .done : it.isYourMove ? .question : .idle)
            Text(it.text).font(DS.font(.body)).foregroundStyle(it.state == .done ? Theme.dim : Theme.fg)
                .strikethrough(it.state == .done, color: Theme.dim).lineLimit(2)
            if it.draft != nil { Pill("drafted", variant: .count) }
            Spacer(minLength: DS.Spacing.m)
            Text(LoopWords.owner(it.owner)).font(DS.font(.chrome)).foregroundStyle(it.owner == .you && it.isOpen ? Theme.fg2 : Theme.dim)
                .frame(width: LoopsLook.ownerWidth, alignment: .leading)
            Text(LoopActions.triggerText(it, steps: all) ?? (it.isOpen ? "ready" : "—")).font(DS.font(.meta))
                .foregroundStyle(it.isOpen && !it.ready ? Theme.fg2 : Theme.dim).lineLimit(1)
                .frame(width: LoopsLook.triggerWidth, alignment: .leading)
        }
        .padding(.horizontal, DS.Spacing.m)
        .frame(minHeight: DS.chromeMaxHeight)
        .background(DS.Radius.shape(DS.Radius.control).fill(selected ? Theme.selection : .clear))
        .contentShape(Rectangle())
        .onTapGesture { model.row = it.id }
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("loops.step.\(it.id)")
    }

    private var fieldRow: some View {
        LoopsField(prompt: model.field == .addStep ? "New step" : "Trigger", text: Binding(get: { model.fieldText }, set: { model.fieldText = $0 }))
    }

    private var offers: some View {
        VStack(alignment: .leading, spacing: DS.Spacing.xs) {
            Text("Offered by agents").font(DS.font(.chrome, .semibold)).foregroundStyle(Theme.dim)
            ForEach(loop.offers) { o in
                Text(o.text).font(DS.font(.chrome)).foregroundStyle(Theme.dim).lineLimit(2).padding(.horizontal, DS.Spacing.m)
            }
        }
        .opacity(LoopsLook.offerOpacity)
    }

    private var members: some View {
        VStack(alignment: .leading, spacing: DS.Spacing.xxs) {
            Text("Agents").font(DS.font(.chrome, .semibold)).foregroundStyle(Theme.fg2)
            if loop.agentLinks.isEmpty {
                Text("None yet: N starts one in this loop.").font(DS.font(.chrome)).foregroundStyle(Theme.dim)
            }
            ForEach(loop.agentLinks) { k in
                Row(title: k.title, subtitle: LoopWords.memberDetail(k), meta: k.machine,
                    mark: k.isLive ? StateMarkKind(k.state ?? .idle) : .exited, kbd: model.row == k.id && k.isLive ? "⏎" : nil,
                    selected: model.row == k.id)
                    .onTapGesture { model.row = k.id }
                    .accessibilityIdentifier("loops.member.\(k.ref)")
            }
        }
    }

    private var timeline: some View {
        let events = LoopWords.timeline(loop)
        return VStack(alignment: .leading, spacing: DS.Spacing.xs) {
            Text("Timeline").font(DS.font(.chrome, .semibold)).foregroundStyle(Theme.fg2)
            ForEach(Array(events.enumerated()), id: \.offset) { _, e in
                HStack(alignment: .firstTextBaseline, spacing: DS.Spacing.s) {
                    Text(e.when).font(DS.font(.meta)).foregroundStyle(Theme.dim).frame(width: LoopsLook.whenWidth, alignment: .leading)
                    Text(e.text).font(DS.font(.chrome)).foregroundStyle(Theme.fg2).lineLimit(2)
                }
            }
        }
    }

    @ViewBuilder private var links: some View {
        let other = loop.links.filter { $0.kind != .agent && $0.kind != .session }
        if !other.isEmpty {
            VStack(alignment: .leading, spacing: DS.Spacing.xs) {
                Text("Links").font(DS.font(.chrome, .semibold)).foregroundStyle(Theme.fg2)
                ForEach(other) { k in
                    HStack(spacing: DS.Spacing.s) {
                        Image(systemName: LoopWords.symbol(k.kind)).font(.system(size: IconButton.symbolSize)).foregroundStyle(Theme.dim)
                        Text(k.title).font(DS.font(.chrome)).foregroundStyle(Theme.fg2).lineLimit(1).truncationMode(.middle)
                        Spacer(minLength: 0)
                        Text(LoopWords.rel(k.rel)).font(DS.font(.meta)).foregroundStyle(Theme.dim)
                    }
                }
            }
        }
    }
}

/// M: the loops to merge into, filtered as typed (↑↓ pick, ⏎ merges).
struct MergePicker: View {
    var model: LoopsSheetModel

    var body: some View {
        let list = model.mergeTargets
        VStack(alignment: .leading, spacing: DS.Spacing.xs) {
            Text("Merge “\(model.target?.displayTitle ?? "")” into").font(DS.font(.chrome, .semibold)).foregroundStyle(Theme.fg2)
            LoopsField(prompt: "Filter loops", text: Binding(get: { model.fieldText }, set: { model.fieldText = $0; model.mergeIndex = 0 }))
            if list.isEmpty {
                Text("No other open loop matches").font(DS.font(.chrome)).foregroundStyle(Theme.dim).padding(DS.Spacing.m)
            }
            ForEach(Array(list.prefix(LoopsLook.pickerRows).enumerated()), id: \.element.id) { i, l in
                Row(title: l.displayTitle, meta: LoopWords.age(l), mark: LoopWords.mark(l), kbd: i == model.mergeIndex ? "⏎" : nil,
                    selected: i == model.mergeIndex)
                    .onTapGesture { model.mergeIndex = i }
            }
        }
        .padding(DS.Spacing.l)
        .frame(maxWidth: LoopsLook.pickerWidth)
        .dsPanel()
        .padding(DS.Spacing.l)
        .accessibilityIdentifier("loops.merge")
    }
}
