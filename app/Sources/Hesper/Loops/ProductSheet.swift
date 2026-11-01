import AppKit
import HesperCore
import Observation
import SwiftUI

/// A product's page (⇧⌘L, G4) and its planning view (G5): the scope, the
/// page built from the store (rebuilt when its version moves), the
/// selected row, the file picker and the planner agent.
@MainActor
@Observable
final class ProductSheetModel {
    enum Mode: Equatable { case page, planning }

    @ObservationIgnored let hub: LoopHub
    @ObservationIgnored private weak var app: AppModel?

    private(set) var scope: String?
    /// Every product (a scope with a goal or a milestone), ⇥ order.
    private(set) var scopes: [String] = []
    private(set) var page: ProductPage?
    private(set) var mode: Mode = .page
    /// A milestone's or a loop's id on the page; a proposal's in planning.
    var selected: String?
    /// Y: the milestones an unfiled loop can go under, ↑↓ ⏎.
    private(set) var filing = false
    var fileIndex = 0
    private(set) var error: String?
    private(set) var busy = false
    /// The planning session's agent (steward.plan's answer).
    private(set) var planner: String?
    /// The sheet is up: the planner's terminal is attached only then (a
    /// hidden sheet keeps no live view of it).
    private(set) var visible = false
    @ObservationIgnored private var seen = -1

    init(hub: LoopHub, app: AppModel) {
        self.hub = hub
        self.app = app
    }

    var catalog: ProjectCatalog { app?.catalog ?? ProjectCatalog() }

    func open(scope s: String?) {
        visible = true
        error = nil
        filing = false
        seen = -1
        sync()
        let known = s.map { catalog.groups[$0] != nil || catalog.project($0) != nil || scopes.contains($0) } ?? false
        let next = known ? s : scope ?? scopes.first
        if next != scope { mode = .page; planner = nil }
        scope = next
        seen = -1
        sync()
    }

    func closed() {
        filing = false
        visible = false
    }

    func sync() {
        let store = hub.store
        guard store.version != seen else { return }
        seen = store.version
        scopes = ProductPage.scopes(goals: Array(store.goals.values), milestones: Array(store.milestones.values), catalog: catalog)
        page = scope.map { ProductPage(scope: $0, store: store, catalog: catalog) }
        let r = rows
        if selected.map({ !r.contains($0) }) ?? true { selected = r.first }
        if mode == .planning, page?.proposed.isEmpty ?? true, planner == nil { mode = .page }
    }

    /// The rows ↑↓ walk: milestones with their loops, then the unfiled
    /// loops; in planning, the proposed milestones.
    var rows: [String] {
        guard let page else { return [] }
        if mode == .planning { return page.proposed.map(\.id) }
        return page.milestones.flatMap { [$0.id] + $0.loops.filter(\.isActive).map(\.id) } + page.unfiled.map(\.id)
    }

    /// Open milestones an unfiled loop can be filed under.
    var fileTargets: [ProductMilestone] { page?.milestones.filter { $0.standing != .done } ?? [] }

    var selectedUnfiled: Loop? { page?.unfiled.first { $0.id == selected } }

    /// The scope's projects that duplicate another (the same folder listed
    /// twice): the page offers to remove the entry.
    var duplicates: [Project] {
        guard let scope else { return [] }
        let ids = catalog.groups[scope].map { Set($0.projectIds) } ?? [scope]
        return ids.compactMap { catalog.project($0) }.filter { $0.duplicateOf != nil }.sorted { $0.name < $1.name }
    }

    func title(of scope: String) -> String { catalog.groups[scope]?.name ?? catalog.project(scope)?.name ?? scope }

    // MARK: Keys

    func perform(_ a: LoopsKeyAction) -> Bool {
        if filing {
            switch a {
            case .up, .down: fileIndex = max(0, min(fileTargets.count - 1, fileIndex + (a == .up ? -1 : 1)))
            case .commitField, .open, .file: commitFile()
            case .cancelField, .close: filing = false
            default: break
            }
            return true
        }
        switch a {
        case .close:
            guard mode == .planning else { return false }
            mode = .page
            selected = rows.first
        case .up, .down:
            let r = rows
            guard !r.isEmpty else { return true }
            let i = r.firstIndex { $0 == selected } ?? (a == .down ? -1 : r.count)
            selected = r[max(0, min(r.count - 1, i + (a == .up ? -1 : 1)))]
        case .open: openSelected()
        case .file:
            guard selectedUnfiled != nil else {
                error = "Y files an unfiled loop: pick one under “Unfiled” first."
                return true
            }
            guard !fileTargets.isEmpty else {
                error = "No open milestone to file it under yet: P plans some with the Steward."
                return true
            }
            error = nil
            fileIndex = 0
            filing = true
        case .plan: plan()
        case .savePlan: savePlan()
        case .dropProposal:
            guard mode == .planning, let id = selected, let page, page.proposed.contains(where: { $0.id == id }) else { NSSound.beep(); return true }
            run { try await $0.goalRemove(id) }
        case .nextScope:
            guard !scopes.isEmpty else { NSSound.beep(); return true }
            let i = scope.flatMap { scopes.firstIndex(of: $0) } ?? -1
            open(scope: scopes[(i + 1) % scopes.count])
        default:
            return false
        }
        return true
    }

    private func openSelected() {
        guard let id = selected, let app else { return }
        if hub.loop(id) != nil { LoopsSheetHost.show(app, .loop(id), toggle: false); return }
        if let first = page?.milestones.first(where: { $0.id == id })?.loops.first(where: \.isActive) {
            LoopsSheetHost.show(app, .loop(first.id), toggle: false)
        }
    }

    private func commitFile() {
        guard let l = selectedUnfiled, fileTargets.indices.contains(fileIndex) else { filing = false; return }
        let ms = fileTargets[fileIndex].id
        filing = false
        run { try await $0.goalFile(milestone: ms, loop: l.id) }
    }

    /// P: a planning session with the Steward's planner agent. One that
    /// runs or starts for the scope is shown instead (hesperd starts a new
    /// planner on every steward.plan).
    private func plan() {
        guard let scope else { NSSound.beep(); return }
        error = nil
        mode = .planning
        if busy { return }
        let agents = app.map { Array($0.registry.agents.values) } ?? []
        if let running = ProductPage.runningPlanner(scope: scope, remembered: planner ?? hub.planners[scope], agents: agents) {
            planner = running.id
            return
        }
        busy = true
        Task {
            let (id, e) = await hub.value { try await $0.stewardPlan(scope: scope) }
            busy = false
            if let e { error = e; if page?.proposed.isEmpty ?? true { mode = .page } }
            if let id = id ?? nil {
                hub.planners[scope] = id
                if self.scope == scope { planner = id }
            }
        }
    }

    /// ⌘↩: every proposed milestone becomes the plan (goals.accept).
    private func savePlan() {
        guard let page, let intent = page.acceptAll, case .accept(let scope, let ids) = intent else {
            error = mode == .planning ? "No milestones proposed yet: the planner writes them as you talk." : "P plans milestones with the Steward first."
            return
        }
        run { try await $0.goalsAccept(scope: scope, ids: ids) }
        mode = .page
    }

    /// Remove a duplicate project entry (the person's call).
    func removeDuplicate(_ p: Project) {
        guard let app else { return }
        app.removeProject(p.id)
        if scope == p.id, let other = p.duplicateOf { open(scope: other) }
    }

    func openPlanner() {
        guard let id = planner, let app else { return }
        LoopsSheetHost.close(app)
        app.focus(id)
    }

    private func run(_ body: @escaping @Sendable (DaemonClient) async throws -> Void) {
        error = nil
        busy = true
        Task {
            let (_, e) = await hub.value { try await body($0) }
            busy = false
            error = e
        }
    }
}

// MARK: Views

/// G4: the goal, the milestones (progress, done-when n of m, loops, due,
/// what is next), the unfiled loops; G5 when planning: the planner's
/// terminal on the left, its proposals updating on the right.
struct ProductSheetView: View {
    var model: ProductSheetModel
    var hub: LoopHub
    var onClose: () -> Void

    var body: some View {
        SheetChrome(title: model.page?.title ?? "Product", symbol: model.page?.isGroup == true ? "square.stack.3d.up" : "shippingbox",
                    count: model.page.map { $0.milestones.count }, closeIdentifier: "product.close",
                    toolbar: { toolbar }, footer: { footer }, onClose: onClose) {
            VStack(spacing: 0) {
                if let e = model.error { LoopsErrorLine(text: e) }
                ForEach(model.duplicates, id: \.id) { p in duplicateBanner(p) }
                content.frame(maxWidth: .infinity, maxHeight: .infinity)
            }
        }
        .onChange(of: hub.store.version) { model.sync() }
    }

    @ViewBuilder private var toolbar: some View {
        if model.scopes.count > 1 {
            ForEach(model.scopes.prefix(LoopsLook.pickerRows), id: \.self) { s in
                Pill(model.title(of: s), variant: .segment(selected: s == model.scope))
            }
            if let k = CommandRegistry.cap(.productNextScope, in: .product) { Kbd(k) }
        }
        Spacer()
        if model.mode == .page {
            KeyButton("Plan with the Steward", kbd: CommandRegistry.cap(.productPlan, in: .product)) { _ = model.perform(.plan) }
                .accessibilityIdentifier("product.plan")
        } else {
            KeyButton("Back to the page", kbd: SheetChromeView.closeKey) { _ = model.perform(.close) }
        }
    }

    @ViewBuilder private var footer: some View {
        if model.filing {
            Text("File “\(model.selectedUnfiled?.displayTitle ?? "")” under a milestone").font(DS.font(.chrome)).foregroundStyle(Theme.dim)
            Spacer()
            KeyButton("Cancel", kbd: "esc") { _ = model.perform(.cancelField) }
            KeyButton("File", kbd: "⏎", variant: .primary(.working)) { _ = model.perform(.commitField) }
        } else if model.mode == .planning {
            KeyButton("Drop proposal", kbd: CommandRegistry.cap(.productDropProposal, in: .product)) { _ = model.perform(.dropProposal) }
            Spacer()
            let n = model.page?.proposed.count ?? 0
            KeyButton(n == 0 ? "Save plan" : "Save plan (\(n) milestones)", kbd: CommandRegistry.cap(.productSavePlan, in: .product),
                      variant: .primary(.done)) { _ = model.perform(.savePlan) }
                .disabled(n == 0)
                .accessibilityIdentifier("product.savePlan")
        } else {
            KeyButton("Open loop", kbd: CommandRegistry.cap(.loopsOpen, in: .product)) { _ = model.perform(.open) }
            if model.selectedUnfiled != nil {
                KeyButton("File", kbd: CommandRegistry.cap(.productFile, in: .product)) { _ = model.perform(.file) }
            }
            Spacer()
        }
    }

    @ViewBuilder private var content: some View {
        if !hub.loaded {
            LoopsEmpty(symbol: "hourglass", title: "Loading…", detail: "Hesper asks hesperd for goals and milestones.")
        } else if !hub.available {
            LoopsEmpty(symbol: "shippingbox", title: "This hesperd has no goals yet", detail: "Update hesperd on this Mac to plan products here.")
        } else if let page = model.page {
            switch model.mode {
            case .page: ProductPageView(model: model, page: page)
            case .planning: PlanningView(model: model, page: page, hub: hub)
            }
        } else {
            LoopsEmpty(symbol: "shippingbox", title: "No product here yet",
                       detail: "A project or group becomes a product once it has a goal or a milestone. Open ⇧⌘L on a wall scoped to one, then P plans its milestones with the Steward.")
        }
    }

    private func duplicateBanner(_ p: Project) -> some View {
        HStack(spacing: DS.Spacing.s) {
            StateMark(.question)
            Text("“\(p.name)” is listed twice: the same folder as \(model.title(of: p.duplicateOf ?? "")), from before it had a remote.")
                .font(DS.font(.chrome)).foregroundStyle(Theme.fg).lineLimit(2)
            Spacer(minLength: DS.Spacing.m)
            KeyButton("Remove this entry") { model.removeDuplicate(p) }
                .accessibilityIdentifier("product.removeDuplicate")
        }
        .padding(.horizontal, DS.Spacing.l)
        .padding(.vertical, DS.Spacing.s)
        .background(Color(nsColor: Theme.ns(.question, alpha: 0.1)))
        .accessibilityIdentifier("product.duplicate")
    }
}

struct ProductPageView: View {
    var model: ProductSheetModel
    var page: ProductPage

    var body: some View {
        ScrollViewReader { proxy in
            ScrollView {
                VStack(alignment: .leading, spacing: DS.Spacing.l) {
                    goal
                    if page.milestones.isEmpty {
                        Text(page.proposed.isEmpty ? "No milestones yet. P plans them with the Steward: it proposes, you save with ⌘↩."
                             : "\(page.proposed.count) milestones are proposed: P opens the planning view to save them.")
                            .font(DS.font(.chrome)).foregroundStyle(Theme.dim)
                    }
                    ForEach(page.milestones) { m in milestone(m) }
                    unfiled
                }
                .padding(DS.Spacing.l)
                .frame(maxWidth: .infinity, alignment: .leading)
            }
            .onChange(of: model.selected) { if let s = model.selected { proxy.scrollTo(s) } }
        }
        .overlay(alignment: .bottom) { if model.filing { FilePicker(model: model) } }
        .accessibilityIdentifier("product.page")
    }

    @ViewBuilder private var goal: some View {
        if let g = page.goal {
            HStack(alignment: .firstTextBaseline, spacing: DS.Spacing.m) {
                Text("Goal").font(DS.font(.meta)).foregroundStyle(Theme.dim).frame(width: LoopsLook.labelWidth, alignment: .leading)
                Text(g.title).font(DS.font(.panelTitle, .medium)).foregroundStyle(Theme.fg)
            }
        } else {
            Text("No goal yet: the planner writes one with the milestones.").font(DS.font(.chrome)).foregroundStyle(Theme.dim)
        }
    }

    private func standingMark(_ s: MilestoneStanding) -> StateMarkKind {
        switch s {
        case .done: return .done
        case .onYou: return .question
        case .waiting: return .idle
        case .stalled: return .error
        case .onTrack: return .working
        }
    }

    private func milestone(_ pm: ProductMilestone) -> some View {
        let m = pm.milestone
        let selected = model.selected == m.id
        return VStack(alignment: .leading, spacing: DS.Spacing.s) {
            HStack(spacing: DS.Spacing.m) {
                Text(m.title).font(DS.font(.body, .semibold)).foregroundStyle(pm.standing == .done ? Theme.dim : Theme.fg).lineLimit(1)
                Pill(standingTitle(pm.standing), mark: standingMark(pm.standing))
                Spacer(minLength: DS.Spacing.m)
                if m.total > 0 { Text("done when \(m.done) of \(m.total)").font(DS.font(.meta)).foregroundStyle(Theme.dim) }
                if let due = m.due { Text("due \(Self.day(due))").font(DS.font(.meta)).foregroundStyle(Theme.dim) }
            }
            ProgressBar(value: pm.progress, tone: pm.standing == .done ? .done : .working)
            if let next = m.nextText { Text("Next: \(next)").font(DS.font(.chrome)).foregroundStyle(Theme.fg2).lineLimit(1) }
            let active = pm.loops.filter(\.isActive)
            ForEach(active) { l in loopRow(l) }
            let closed = pm.loops.count - active.count
            if closed > 0 { Text(closed == 1 ? "1 loop finished" : "\(closed) loops finished").font(DS.font(.meta)).foregroundStyle(Theme.dim) }
        }
        .padding(DS.Spacing.m)
        .background(DS.Radius.shape(DS.Radius.control).fill(Theme.color(.tile)))
        .overlay(DS.Radius.shape(DS.Radius.control).strokeBorder(selected ? Theme.accent : Theme.stroke, lineWidth: 1))
        .id(m.id)
        .onTapGesture { model.selected = m.id }
        .accessibilityIdentifier("product.milestone.\(m.id)")
    }

    private func standingTitle(_ s: MilestoneStanding) -> String {
        if case .waiting(let on) = s, let t = page.milestones.first(where: { $0.id == on })?.milestone.title { return "Waiting on \(t)" }
        return s.title
    }

    private func loopRow(_ l: Loop) -> some View {
        Row(title: l.displayTitle, subtitle: l.nextText, meta: LoopWords.age(l), mark: LoopWords.mark(l),
            kbd: model.selected == l.id ? CommandRegistry.cap(.loopsOpen, in: .product) : nil, selected: model.selected == l.id)
            .id(l.id)
            .onTapGesture { model.selected = l.id }
            .accessibilityIdentifier("product.loop.\(l.id)")
    }

    @ViewBuilder private var unfiled: some View {
        VStack(alignment: .leading, spacing: DS.Spacing.xs) {
            HStack(spacing: DS.Spacing.s) {
                Text("Unfiled").font(DS.font(.chrome, .semibold)).foregroundStyle(Theme.fg2)
                Pill("", variant: .count, count: page.unfiled.count)
                Spacer(minLength: 0)
                if let k = CommandRegistry.cap(.productFile, in: .product) {
                    Text("File").font(DS.font(.meta)).foregroundStyle(Theme.dim)
                    Kbd(k)
                }
            }
            if page.unfiled.isEmpty {
                Text("Every open loop of this product is filed under a milestone.").font(DS.font(.chrome)).foregroundStyle(Theme.dim)
            }
            ForEach(page.unfiled) { l in loopRow(l) }
        }
        .accessibilityIdentifier("product.unfiled")
    }

    static func day(_ d: Date) -> String {
        let f = DateFormatter()
        f.setLocalizedDateFormatFromTemplate("MMM d")
        return f.string(from: d)
    }
}

/// Y: the open milestones to file the selected loop under.
struct FilePicker: View {
    var model: ProductSheetModel

    var body: some View {
        VStack(alignment: .leading, spacing: DS.Spacing.xs) {
            Text("File under").font(DS.font(.chrome, .semibold)).foregroundStyle(Theme.fg2)
            ForEach(Array(model.fileTargets.enumerated()), id: \.element.id) { i, m in
                Row(title: m.milestone.title, meta: m.milestone.total > 0 ? "\(m.milestone.done)/\(m.milestone.total)" : nil,
                    kbd: i == model.fileIndex ? "⏎" : nil, selected: i == model.fileIndex)
                    .onTapGesture { model.fileIndex = i }
            }
        }
        .padding(DS.Spacing.l)
        .frame(maxWidth: LoopsLook.pickerWidth)
        .dsPanel()
        .padding(DS.Spacing.l)
        .accessibilityIdentifier("product.file")
    }
}

/// G5: the planner agent's live terminal (a read-only view like a tile;
/// "Talk to the planner" opens it full size) and the milestones it
/// proposes, updating as goals.changed arrives; ⌘↩ saves them.
struct PlanningView: View {
    var model: ProductSheetModel
    var page: ProductPage
    var hub: LoopHub

    var body: some View {
        GeometryReader { g in
            HStack(spacing: 0) {
                planner.frame(width: g.size.width * LoopsLook.terminalFraction)
                Rectangle().fill(Theme.stroke).frame(width: 1)
                proposals
            }
        }
        .accessibilityIdentifier("product.planning")
    }

    @ViewBuilder private var planner: some View {
        VStack(spacing: 0) {
            HStack(spacing: DS.Spacing.s) {
                Text("Planner").font(DS.font(.chrome, .semibold)).foregroundStyle(Theme.fg2)
                Spacer(minLength: 0)
                if model.planner != nil {
                    KeyButton("Talk to the planner", variant: .icon("arrow.up.left.and.arrow.down.right")) { model.openPlanner() }
                }
            }
            .padding(.horizontal, DS.Spacing.m)
            .frame(height: DS.chromeMaxHeight)
            if let id = model.planner, let app = model.appModel, let agent = app.agent(id) {
                if model.visible {
                    PlannerTerminal(agent: agent, model: app)
                        .background(Color(nsColor: DS.terminalBackground))
                } else {
                    Color(nsColor: DS.terminalBackground) // hidden: detached until the sheet shows again
                }
            } else {
                LoopsEmpty(symbol: "text.bubble", title: model.busy ? "Starting the planner…" : "No planner running",
                           detail: model.busy ? "The Steward starts a planning agent for this product."
                               : "P starts one: it asks about the product and proposes milestones on the right.")
            }
        }
    }

    private var proposals: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: DS.Spacing.s) {
                Text("Proposed milestones").font(DS.font(.chrome, .semibold)).foregroundStyle(Theme.fg2)
                if page.proposed.isEmpty {
                    Text("They appear here as the planner writes them.").font(DS.font(.chrome)).foregroundStyle(Theme.dim)
                }
                ForEach(Array(page.proposed.enumerated()), id: \.element.id) { i, m in
                    VStack(alignment: .leading, spacing: DS.Spacing.xxs) {
                        HStack(spacing: DS.Spacing.s) {
                            Text("\(i + 1)").font(DS.font(.meta)).foregroundStyle(Theme.dim)
                            Text(m.title).font(DS.font(.body, .medium)).foregroundStyle(Theme.fg).lineLimit(2)
                        }
                        ForEach(m.items) { c in
                            Text("· \(c.text)").font(DS.font(.chrome)).foregroundStyle(Theme.dim).lineLimit(2)
                        }
                    }
                    .padding(DS.Spacing.m)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .background(DS.Radius.shape(DS.Radius.control).fill(model.selected == m.id ? Theme.selection : Theme.color(.tile)))
                    .onTapGesture { model.selected = m.id }
                    .accessibilityIdentifier("product.proposed.\(m.id)")
                }
            }
            .padding(DS.Spacing.l)
        }
    }
}

/// The planner's terminal: an AgentTerminal in the tile role (a `--fit`
/// view, never read-write), closed when the view goes.
struct PlannerTerminal: NSViewRepresentable {
    var agent: Agent
    var model: AppModel

    final class Coordinator {
        var terminal: AgentTerminal?
    }

    func makeCoordinator() -> Coordinator { Coordinator() }

    func makeNSView(context: Context) -> NSView {
        let t = AgentTerminal(agent: agent, role: .tile, env: model.env)
        t.tileFont = TerminalFont.focusDefaultSize
        context.coordinator.terminal = t
        return t.host
    }

    func updateNSView(_ view: NSView, context: Context) {
        context.coordinator.terminal?.update(agent, connected: model.isConnected)
        context.coordinator.terminal?.layout()
    }

    static func dismantleNSView(_ view: NSView, coordinator: Coordinator) {
        coordinator.terminal?.close()
        coordinator.terminal = nil
    }
}

extension ProductSheetModel {
    /// The app model, for the planner's terminal.
    var appModel: AppModel? { app }
}
