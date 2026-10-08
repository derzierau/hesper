import AppKit
import HesperCore
import SwiftUI

/// The project sidebar (⌘0, per wall): a calm navigator over what runs
/// (HesperCore `ProjectNav` has the rules):
///
///   [ Filter projects            ]   the filter (type to search all)
///   All agents            ■2 ■5 ■3
///   ACTIVE      projects with agents, needs you first; groups expand
///   RECENT      quiet projects of the last week, with their age
///   All projects  45 ›               the full list (filterable)
///   ─────────────────────────────
///   ■ laptop 6   ■ mini 4            machines
///
/// Click: this wall shows it (a machine: a filter on it). ⌥-click: a new
/// wall scoped to it. Drag a row onto another wall window (it takes the
/// scope) or out onto a display (a new wall there). ⌘-click selects
/// several projects (New Group from Selection). The context menu edits the
/// data (projects.update, groups.save, projects.promote, projects.remove,
/// groups.remove). ↑ ↓ ⏎ in the filter walk the rows; esc clears it.
@MainActor
final class ProjectSidebar: NSView, NSTextFieldDelegate {
    static let width = CGFloat(ProjectNav.Metrics.width)

    /// Everything the sidebar shows, read at once.
    struct Snapshot {
        var catalog: ProjectCatalog
        var agents: [Agent]
        var machines: [Machine]
        var local: String?
        var scope: WallScope
        var historyAvailable: Bool
        var now = Date()
    }

    private weak var model: AppModel?
    private let source: () -> Snapshot?
    let scroll = NSScrollView()
    private let stack = FlippedView()
    private(set) var rows: [SidebarRowView] = []
    private var machineViews: [SidebarRowView] = []
    private let filter = SidebarFilterField()
    private let footer = FlippedView()
    private let footerLine = NSView()
    private let edge = NSView()
    /// ⌘-click selection (project ids).
    var selection: Set<String> = []
    /// Expanded groups, the filter, the full list (per wall).
    private(set) var nav = ProjectNavState()
    /// The keyboard's row (↑ ↓ in the filter).
    private var cursor: String?
    private var lastSections: [ProjectNavSection] = []
    private var lastMachines: [SidebarMachine] = []
    private var lastScope: WallScope?

    override var isFlipped: Bool { true }

    convenience init(model: AppModel) {
        self.init(source: { [weak model] in
            guard let model else { return nil }
            return Snapshot(catalog: model.catalog, agents: model.unscopedWall, machines: model.machines, local: model.localMachine,
                            scope: model.scope, historyAvailable: model.historyAvailable)
        })
        self.model = model
    }

    /// Without a model (offscreen renders): the snapshot is all there is.
    init(source: @escaping () -> Snapshot?) {
        self.source = source
        super.init(frame: .zero)
        wantsLayer = true
        for v in [edge, footerLine] { v.wantsLayer = true }
        scroll.documentView = stack
        scroll.drawsBackground = false
        scroll.hasVerticalScroller = true
        scroll.autohidesScrollers = true
        scroll.scrollerStyle = .overlay
        filter.field.delegate = self
        filter.onClear = { [weak self] in self?.leaveSearch() }
        addSubview(filter)
        addSubview(scroll)
        addSubview(footer)
        addSubview(footerLine)
        addSubview(edge)
        applyTheme()
        setAccessibilityIdentifier("sidebar")
        setAccessibilityRole(.list)
        setAccessibilityLabel("Projects")
        NotificationCenter.default.addObserver(forName: DS.densityDidChange, object: nil, queue: .main) { [weak self] _ in
            MainActor.assumeIsolated { self?.needsLayout = true }
        }
    }

    @available(*, unavailable)
    required init?(coder: NSCoder) { fatalError() }

    override func viewDidChangeEffectiveAppearance() {
        super.viewDidChangeEffectiveAppearance()
        applyTheme()
    }

    private func applyTheme() {
        layer?.backgroundColor = Theme.ns(.surface).cg(in: self)
        edge.layer?.backgroundColor = Theme.hairline.cg(in: self)
        footerLine.layer?.backgroundColor = Theme.hairline.cg(in: self)
    }

    private var rowHeight: CGFloat { CGFloat(ProjectNav.Metrics.rowHeight(DS.density)) }
    private static let headerHeight = CGFloat(ProjectNav.Metrics.sectionGap) + DS.Spacing.l
    private var footerHeight: CGFloat { machineViews.isEmpty ? 0 : rowHeight + DS.Spacing.s }

    override func layout() {
        super.layout()
        let hairline: CGFloat = 1
        let w = bounds.width - hairline
        let pad = DS.Spacing.s
        filter.frame = NSRect(x: pad, y: pad, width: max(0, w - 2 * pad), height: rowHeight)
        let top = filter.frame.maxY + DS.Spacing.xs
        let fh = footerHeight
        scroll.frame = NSRect(x: 0, y: top, width: w, height: max(0, bounds.height - top - fh))
        footer.frame = NSRect(x: 0, y: bounds.height - fh, width: w, height: fh)
        footerLine.frame = NSRect(x: 0, y: footer.frame.minY, width: w, height: fh > 0 ? hairline : 0)
        edge.frame = NSRect(x: w, y: 0, width: hairline, height: bounds.height)
        layoutRows()
        layoutFooter()
    }

    // MARK: Building

    /// Re-reads projects, groups, machines and agents.
    func reload() {
        guard let s = source() else { return }
        var state = nav
        // The quiet project this wall shows offers "New agent here".
        if case .project(let p) = s.scope { state.selectedProject = p } else { state.selectedProject = nil }
        let sections = ProjectNav.make(catalog: s.catalog, agents: s.agents, machines: s.machines, local: s.local, now: s.now,
                                       state: state, historyAvailable: s.historyAvailable)
        let machines = SidebarMachines.make(s.machines, agents: s.agents)
        guard sections != lastSections || machines != lastMachines || lastScope != s.scope else { return }
        lastSections = sections
        lastMachines = machines
        lastScope = s.scope
        // Reuse a row's view by id (agents change often; views stay).
        var old = Dictionary(rows.compactMap { r in r.reuseKey.map { ($0, r) } }, uniquingKeysWith: { a, _ in a })
        var next: [SidebarRowView] = []
        for sec in sections {
            if let t = sec.title { next.append(SidebarRowView(header: t)) }
            for r in sec.rows {
                if let v = old.removeValue(forKey: r.id) { v.item = .nav(r); next.append(v) } else { next.append(SidebarRowView(item: .nav(r), sidebar: self)) }
            }
        }
        for r in rows where !next.contains(where: { $0 === r }) { r.removeFromSuperview() }
        for r in next where r.superview !== stack { stack.addSubview(r) }
        rows = next
        for v in machineViews { v.removeFromSuperview() }
        machineViews = machines.map { SidebarRowView(item: .machine($0), sidebar: self) }
        for v in machineViews { footer.addSubview(v) }
        if let c = cursor, !ProjectNav.flat(sections).contains(where: { $0.id == c }) { cursor = nil }
        refreshSelection()
        filter.update(query: nav.query, fullList: nav.showAll, count: sections.last?.rows.first(where: { $0.kind == .allProjects })?.meta)
        needsLayout = true
        layoutRows()
        layoutFooter()
    }

    func refreshSelection() {
        let scope = source()?.scope
        for r in rows {
            let current = r.item.scope != nil && r.item.scope == scope
            r.refresh(selected: current, multi: r.row?.projectID.map(selection.contains) ?? false, cursor: r.item.id == cursor)
        }
        for m in machineViews { m.refresh(selected: m.item.scope == scope, multi: false, cursor: false) }
    }

    private func layoutRows() {
        var y: CGFloat = 0
        let w = scroll.contentSize.width
        for r in rows {
            let h = r.isHeader ? Self.headerHeight : rowHeight
            r.frame = NSRect(x: 0, y: y, width: w, height: h)
            y += h
        }
        stack.frame = NSRect(x: 0, y: 0, width: w, height: max(y + DS.Spacing.l, scroll.contentSize.height))
    }

    private func layoutFooter() {
        var x = DS.Spacing.s
        let y = footerLine.frame.height + (footer.bounds.height - footerLine.frame.height - rowHeight) / 2
        for v in machineViews {
            let w = min(v.fittingWidth, max(0, footer.bounds.width - x - DS.Spacing.s))
            v.frame = NSRect(x: x, y: y.rounded(), width: w, height: rowHeight)
            x += w + DS.Spacing.xxs
        }
    }

    // MARK: Actions

    func clicked(_ row: SidebarRow, _ flags: NSEvent.ModifierFlags) {
        let item = rows.first { $0.row?.id == row.id }?.item
        clicked(item ?? .nav(Self.navRow(row)), flags)
    }

    private static func navRow(_ row: SidebarRow) -> ProjectNavRow {
        var r = ProjectNavRow(id: row.id, kind: row.kind == .group ? .group : row.kind == .all ? .all : .project, title: row.title)
        r.projectID = row.projectID; r.groupID = row.groupID; r.base = row
        return r
    }

    func clicked(_ item: SidebarItem, _ flags: NSEvent.ModifierFlags, caret: Bool = false) {
        if case .nav(let r) = item {
            switch r.kind {
            case .allProjects:
                nav.showAll = true
                cursor = nil
                reload()
                window?.makeFirstResponder(filter.field)
                return
            case .scratch:
                nav.scratchExpanded.toggle()
                reload()
                return
            case .showArchived:
                nav.showArchived.toggle()
                reload()
                return
            case .newAgent:
                if let p = r.projectID { model?.newDraft(projectID: p) }
                return
            case .history:
                if let p = r.projectID { model?.toggleHistory(scope: .project(p)) }
                return
            case .empty:
                return
            case .group where caret:
                if let g = r.groupID { toggle(group: g) }
                return
            default:
                break
            }
        }
        guard let scope = item.scope else { return }
        if flags.contains(.command), let p = item.row?.projectID {
            if selection.contains(p) { selection.remove(p) } else { selection.insert(p) }
            refreshSelection()
            return
        }
        selection = []
        cursor = nil
        guard let model else { return }
        if flags.contains(.option) { model.sidebarAction?(.newWall(scope)) } else { model.sidebarAction?(.scope(scope)) }
        lastScope = nil
        reload()
    }

    /// A group's caret: its projects show (or hide) under it.
    func toggle(group id: String) {
        setGroup(id, expanded: !nav.expandedGroups.contains(id))
    }

    func setGroup(_ id: String, expanded: Bool) {
        if expanded { nav.expandedGroups.insert(id) } else { nav.expandedGroups.remove(id) }
        reload()
    }

    func dropped(_ item: SidebarItem, at screenPoint: NSPoint) {
        guard let scope = item.scope else { return }
        model?.sidebarAction?(.wallAt(scope, screenPoint))
    }

    // MARK: Filter

    /// Typing filters all projects; the results replace the sections.
    func setQuery(_ q: String) {
        guard q != nav.query else { return }
        nav.query = q
        cursor = nil
        reload()
        if nav.searching { cursor = ProjectNav.flat(lastSections).first { $0.kind != .all }?.id; refreshSelection() }
    }

    /// Esc / the field's ×: the filter clears, then the full list closes.
    func leaveSearch() {
        if !nav.query.isEmpty {
            filter.field.stringValue = ""
            setQuery("")
            return
        }
        nav.showAll = false
        cursor = nil
        reload()
        if window?.firstResponder === filter.field.currentEditor() { window?.makeFirstResponder(nil) }
    }

    func controlTextDidChange(_ obj: Notification) { setQuery(filter.field.stringValue) }

    func control(_ control: NSControl, textView: NSTextView, doCommandBy sel: Selector) -> Bool {
        let flat = ProjectNav.flat(lastSections)
        switch sel {
        case #selector(NSResponder.moveDown(_:)), #selector(NSResponder.moveUp(_:)):
            cursor = ProjectNav.step(flat, from: cursor, by: sel == #selector(NSResponder.moveDown(_:)) ? 1 : -1)
            refreshSelection()
            if let v = rows.first(where: { $0.item.id == cursor }) { v.scrollToVisible(v.bounds) }
            return true
        case #selector(NSResponder.insertNewline(_:)):
            guard let c = cursor ?? flat.first(where: { $0.kind != .all })?.id, let v = rows.first(where: { $0.item.id == c }) else { return true }
            clicked(v.item, NSApp.currentEvent?.modifierFlags.intersection([.option, .command]) ?? [])
            return true
        case #selector(NSResponder.cancelOperation(_:)):
            leaveSearch()
            return true
        default:
            return false
        }
    }

    /// The sidebar itself has the keyboard (a click on its empty part):
    /// typing goes into the filter.
    override var acceptsFirstResponder: Bool { true }

    override func mouseDown(with event: NSEvent) { window?.makeFirstResponder(self) }

    override func keyDown(with event: NSEvent) {
        if let t = event.characters, !t.isEmpty, event.modifierFlags.intersection([.command, .control, .option]).isEmpty,
           t.unicodeScalars.allSatisfy({ !CharacterSet.controlCharacters.contains($0) && $0.value < 0xF700 }) {
            window?.makeFirstResponder(filter.field)
            filter.field.stringValue = nav.query + t
            filter.field.currentEditor()?.selectedRange = NSRange(location: filter.field.stringValue.utf16.count, length: 0)
            setQuery(filter.field.stringValue)
            return
        }
        if event.keyCode == 53 { leaveSearch(); return } // esc
        super.keyDown(with: event)
    }

    // MARK: Menus

    func menu(for item: SidebarItem) -> NSMenu? {
        switch item {
        case .header: return nil
        case .nav(let r):
            return r.base.flatMap { menu(for: $0) }
        case .machine(let m):
            let menu = NSMenu()
            for (title, flags) in [("Show on This Wall", NSEvent.ModifierFlags()), ("Open as New Wall", .option)] {
                let act = MenuAction { [weak self] in self?.clicked(.machine(m), flags) }
                let i = NSMenuItem(title: title, action: #selector(MenuAction.run), keyEquivalent: "")
                i.target = act
                i.representedObject = act
                menu.addItem(i)
            }
            return menu
        }
    }

    func menu(for row: SidebarRow) -> NSMenu? {
        guard let model else { return nil }
        let menu = NSMenu()
        let catalog = model.catalog
        func add(_ title: String, to m: NSMenu = menu, enabled: Bool = true, _ action: @escaping @MainActor () -> Void) {
            let i = NSMenuItem(title: title, action: enabled ? #selector(MenuAction.run) : nil, keyEquivalent: "")
            let act = MenuAction(action)
            i.target = act
            i.representedObject = act
            i.isEnabled = enabled
            m.addItem(i)
        }
        func sub(_ title: String) -> NSMenu {
            let item = NSMenuItem(title: title, action: nil, keyEquivalent: "")
            let m = NSMenu(title: title)
            item.submenu = m
            menu.addItem(item)
            return m
        }
        if row.kind == .project, let pid = row.projectID {
            // An explicit project action: the draft opens in this project's band.
            add("New Agent in \(row.title)") { [weak model] in model?.newDraft(projectID: pid) }
            menu.addItem(.separator())
        }
        add("Show on This Wall") { [weak self] in self?.clicked(row, []) }
        add("Open as New Wall") { [weak model] in model?.sidebarAction?(.newWall(row.scope)) }
        if model.historyAvailable, row.kind != .group { // shared history
            add("Show History (⌘Y)") { [weak model] in model?.toggleHistory(scope: row.projectID.map(HistoryScope.project) ?? .all) }
        }
        guard row.kind != .all else { return menu }
        menu.addItem(.separator())
        let editable = catalog.supported
        switch row.kind {
        case .group:
            guard let gid = row.groupID, let g = catalog.groups[gid] else { return menu }
            add(nav.expandedGroups.contains(gid) ? "Collapse" : "Expand") { [weak self] in self?.toggle(group: gid) }
            add("Rename Group…") { [weak model] in
                guard let name = Self.ask("Rename group", value: g.name) else { return }
                var n = g; n.name = name; model?.saveGroup(n)
            }
            let colors = sub("Color")
            for c in ProjectColor.palette {
                add(c.name.capitalized, to: colors) { [weak model] in var n = g; n.color = c.hex; model?.saveGroup(n) }
            }
            menu.addItem(.separator())
            add("Remove Group") { [weak model] in model?.removeGroup(gid) }
        case .project:
            guard let pid = row.projectID, let p = catalog.project(pid) else { return menu }
            // hesperd's scratch: its lifecycle only (rename, keep, promote,
            // archive, restore, delete).
            if !model.scratchActions(pid).isEmpty {
                model.addScratchItems(pid, to: menu, window: window)
                return menu
            }
            let real = editable && !p.synthesized
            add("Rename…", enabled: real) { [weak model] in
                guard let name = Self.ask("Rename project", value: p.name) else { return }
                model?.renameProject(pid, to: name)
            }
            let colors = sub("Color")
            for c in ProjectColor.palette {
                add(c.name.capitalized, to: colors, enabled: real) { [weak model] in model?.setProjectColor(pid, c.hex) }
            }
            let profiles = sub("Default Profile")
            for name in (model.lists.profiles?.profiles.keys.sorted() ?? []) {
                add(name, to: profiles, enabled: real) { [weak model] in model?.setProjectDefaults(pid, profile: name, machine: nil) }
                if p.defaults?.profile == name { profiles.items.last?.state = .on }
            }
            let machines = sub("Default Machine")
            for m in model.machines {
                add(m.displayName, to: machines, enabled: real) { [weak model] in model?.setProjectDefaults(pid, profile: nil, machine: m.short) }
                if p.defaults?.machine == m.short { machines.items.last?.state = .on }
            }
            if p.isScratch || p.synthesized || ProjectNav.isScratch(p) {
                add("Promote to Project…", enabled: editable) { [weak model] in
                    let name = Self.ask("Promote \(p.name) to a project", value: ProjectNav.label(p.name))
                    guard let name else { return }
                    model?.promoteProject(pid, name: name)
                }
            }
            menu.addItem(.separator())
            let groups = sub("Add to Group")
            for g in catalog.orderedGroups {
                add(g.name, to: groups, enabled: real && !g.projectIds.contains(pid)) { [weak model] in
                    var n = g; n.projectIds.append(pid); model?.saveGroup(n)
                }
            }
            let picked = selection.isEmpty ? [pid] : Array(selection.union([pid])).sorted()
            add(picked.count > 1 ? "New Group from Selection (\(picked.count))…" : "New Group…", enabled: editable) { [weak self, weak model] in
                guard let model, let name = Self.ask("New group", value: "") else { return }
                let order = (model.catalog.orderedGroups.map(\.order).max() ?? -1) + 1
                model.saveGroup(ProjectGroup(id: "", name: name, projectIds: picked, order: order))
                self?.selection = []
            }
            if let gid = row.groupID, let g = catalog.groups[gid] {
                add("Remove from \(g.name)", enabled: editable) { [weak model] in
                    var n = g; n.projectIds.removeAll { $0 == pid }; model?.saveGroup(n)
                }
            }
            menu.addItem(.separator())
            add("Remove Project", enabled: real) { [weak model] in model?.removeProject(pid) }
        case .all:
            break
        }
        return menu
    }

    /// A one-line question (rename, group name); nil: cancelled.
    static func ask(_ title: String, value: String) -> String? {
        let a = NSAlert()
        a.messageText = title
        let f = NSTextField(string: value)
        f.frame = NSRect(x: 0, y: 0, width: 260, height: 24)
        a.accessoryView = f
        a.addButton(withTitle: "OK")
        a.addButton(withTitle: "Cancel")
        a.window.initialFirstResponder = f
        guard a.runModal() == .alertFirstButtonReturn else { return nil }
        let s = f.stringValue.trimmingCharacters(in: .whitespacesAndNewlines)
        return s.isEmpty ? nil : s
    }
}

/// What a sidebar line shows.
enum SidebarItem: Equatable {
    case header(String)
    case nav(ProjectNavRow)
    case machine(SidebarMachine)

    /// What a click gives the wall (nil: a heading, an action).
    var scope: WallScope? {
        switch self {
        case .header: return nil
        case .nav(let r): return r.scope
        case .machine(let m): return m.scope
        }
    }

    /// The project / group / All row (nil: a heading, an action, a machine).
    var row: SidebarRow? { if case .nav(let r) = self { return r.base } else { return nil } }

    var id: String? {
        switch self {
        case .header: return nil
        case .nav(let r): return r.id
        case .machine(let m): return m.id
        }
    }
}

// MARK: - Lines

/// The squares of a row: needs you (Signal), working, done, each with its
/// count; nothing for zero.
struct StateTallyView: View {
    var tally: StateTally

    var body: some View {
        HStack(spacing: DS.Spacing.s) {
            ForEach(Array(tally.parts.enumerated()), id: \.offset) { _, p in
                HStack(spacing: DS.Spacing.xs) {
                    // One accent at rest: done is a quiet outline.
                    StateMark(p.kind == .done ? .idle : p.kind)
                    Text("\(p.count)").font(DS.font(.meta)).monospacedDigit()
                        .foregroundStyle(p.kind == .needsYou ? Theme.fg : Theme.dim)
                }
            }
        }
        .fixedSize()
    }
}

/// One line: a section heading, a navigator row, or a machine (footer).
struct SidebarRowContent: View {
    var item: SidebarItem
    var selected: Bool
    var multi: Bool
    var cursor: Bool

    @State private var hover = false

    /// Room for a caret, so every title starts at the same x.
    static let caretSlot = DS.Spacing.m + DS.Spacing.xs
    static let indent = DS.Spacing.m + DS.Spacing.xs
    static let metaMaxWidth: CGFloat = 84
    static let shortMeta = 6
    private static var iconFont: Font { .system(size: DS.nsFont(.meta).pointSize, weight: .medium) }

    var body: some View {
        switch item {
        case .header(let title):
            Text(title.uppercased())
                .font(DS.font(.meta, .medium))
                .tracking(0.6)
                .foregroundStyle(Theme.dim)
                .padding(.leading, DS.Spacing.s + DS.Spacing.xs + Self.caretSlot + DS.Spacing.xs + DS.Spacing.xxs)
                .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .bottomLeading)
                .padding(.bottom, DS.Spacing.xs)
        case .nav(let r):
            nav(r)
        case .machine(let m):
            HStack(spacing: DS.Spacing.xs + DS.Spacing.xxs) {
                StateMark(m.online ? .done : .idle)
                Text(m.title).font(DS.font(.chrome)).foregroundStyle(m.online ? Theme.fg2 : Theme.dim).lineLimit(1)
                if m.count > 0 { Text("\(m.count)").font(DS.font(.meta)).foregroundStyle(Theme.dim) }
            }
            .padding(.horizontal, DS.Spacing.s)
            .frame(maxHeight: .infinity)
            .background(DS.Radius.shape(DS.Radius.control).fill(fill))
            .contentShape(Rectangle())
            .onHover { hover = $0 }
        }
    }

    private var fill: Color {
        if selected { return Theme.color(.working).opacity(0.14) }
        if multi { return Theme.color(.working).opacity(0.08) }
        return hover || cursor ? Theme.chipBG.opacity(0.6) : .clear
    }

    @ViewBuilder private func nav(_ r: ProjectNavRow) -> some View {
        let bar = (selected || hover || cursor) && (r.kind == .project || r.kind == .group)
        HStack(spacing: DS.Spacing.xs + DS.Spacing.xxs) {
            leading(r).frame(width: Self.caretSlot, alignment: .center)
            HStack(spacing: DS.Spacing.xs) {
                Text(r.title)
                    .font(DS.font(.body, r.kind == .all ? .medium : .regular))
                    .foregroundStyle(titleColor(r))
                    .lineLimit(1).truncationMode(.middle)
                    .layoutPriority(1)
                if let s = r.suffix {
                    Text(s).font(DS.font(.body)).foregroundStyle(Theme.dim)
                        .lineLimit(1).truncationMode(.tail)
                }
            }
            .layoutPriority(2)
            Spacer(minLength: DS.Spacing.s)
            if let m = r.meta {
                if m.count <= Self.shortMeta {
                    Text(m).font(DS.font(.meta)).foregroundStyle(Theme.dim).fixedSize() // an age, a count: never cut
                } else {
                    Text(m).font(DS.font(.meta)).foregroundStyle(Theme.dim)
                        .lineLimit(1).truncationMode(.middle)
                        .frame(maxWidth: Self.metaMaxWidth, alignment: .trailing)
                        .layoutPriority(1)
                }
            }
            if !r.tally.isEmpty { StateTallyView(tally: r.tally).layoutPriority(3) }
            if r.kind == .allProjects {
                Image(systemName: "chevron.right").font(.system(size: DS.Spacing.s + DS.Spacing.xxs, weight: .semibold)).foregroundStyle(Theme.dim)
            }
        }
        .padding(.leading, DS.Spacing.xs + CGFloat(r.depth) * Self.indent)
        .padding(.trailing, DS.Spacing.m)
        .frame(maxHeight: .infinity)
        .background(DS.Radius.shape(DS.Radius.control).fill(fill))
        .overlay(alignment: .leading) {
            if bar {
                DS.Radius.shape(DS.Radius.control / 2).fill(Theme.color(r.colorHex))
                    .frame(width: CGFloat(ProjectNav.Metrics.accentBar))
                    .padding(.vertical, DS.Spacing.s - DS.Spacing.xxs)
                    .padding(.leading, DS.Spacing.xxs)
            }
        }
        .padding(.horizontal, DS.Spacing.s)
        .contentShape(Rectangle())
        .onHover { hover = $0 }
    }

    @ViewBuilder private func leading(_ r: ProjectNavRow) -> some View {
        switch r.kind {
        case .group, .scratch:
            Image(systemName: r.expanded ? "chevron.down" : "chevron.right")
                .font(.system(size: DS.Spacing.s + DS.Spacing.xxs, weight: .semibold))
                .foregroundStyle(Theme.dim)
        case .showArchived:
            Image(systemName: "archivebox").font(Self.iconFont).foregroundStyle(Theme.dim)
        case .project where r.kept:
            Image(systemName: "pin.fill").font(Self.iconFont).foregroundStyle(Theme.dim)
                .accessibilityLabel("kept")
        case .newAgent:
            Image(systemName: "plus").font(Self.iconFont).foregroundStyle(Theme.dim)
        case .history:
            Image(systemName: "clock").font(Self.iconFont).foregroundStyle(Theme.dim)
        default:
            Color.clear
        }
    }

    private func titleColor(_ r: ProjectNavRow) -> Color {
        switch r.kind {
        case .empty, .newAgent, .history, .scratch, .showArchived, .allProjects: return Theme.dim
        default: return r.archived && !selected ? Theme.dim : r.quiet && !selected ? Theme.fg2 : Theme.fg
        }
    }
}

@MainActor
final class SidebarRowView: NSView, NSDraggingSource {
    var item: SidebarItem {
        didSet { if item != oldValue { host.rootView = SidebarRowContent(item: item, selected: selected, multi: multi, cursor: cursor); describe() } }
    }
    /// The project / group / All row (nil: a heading, an action, a machine).
    var row: SidebarRow? { item.row }
    private let host: NSHostingView<SidebarRowContent>
    private weak var sidebar: ProjectSidebar?
    private var downAt: NSPoint?
    private var dragged = false
    private var selected = false, multi = false, cursor = false
    /// How far the mouse moves before a press becomes a drag.
    private static let dragSlop: CGFloat = 5

    override var isFlipped: Bool { true }

    init(item: SidebarItem, sidebar: ProjectSidebar) {
        self.item = item
        self.sidebar = sidebar
        host = NSHostingView(rootView: SidebarRowContent(item: item, selected: false, multi: false, cursor: false))
        super.init(frame: .zero)
        addSubview(host)
        setAccessibilityElement(true)
        setAccessibilityRole(.button)
        describe()
    }

    init(header: String) {
        item = .header(header)
        host = NSHostingView(rootView: SidebarRowContent(item: item, selected: false, multi: false, cursor: false))
        super.init(frame: .zero)
        addSubview(host)
        setAccessibilityElement(true)
        setAccessibilityRole(.staticText)
        setAccessibilityLabel(header)
    }

    @available(*, unavailable)
    required init?(coder: NSCoder) { fatalError() }

    var isHeader: Bool { if case .header = item { return true } else { return false } }
    /// Reused across reloads by this id (headers are rebuilt).
    var reuseKey: String? { isHeader ? nil : item.id }
    var fittingWidth: CGFloat { ceil(host.fittingSize.width) }

    private func describe() {
        switch item {
        case .nav(let r):
            setAccessibilityIdentifier("sidebar.\(r.id)")
            let parts = [r.title, r.suffix, r.meta, r.kept ? "kept" : nil, r.tally.isEmpty ? nil : r.tally.spoken,
                         r.expandable ? (r.expanded ? "expanded" : "collapsed") : nil].compactMap { $0 }
            setAccessibilityLabel(parts.joined(separator: ", "))
        case .machine(let m):
            setAccessibilityIdentifier("sidebar.\(m.id)")
            setAccessibilityLabel("\(m.title), \(m.online ? "online" : "offline"), \(m.count) agents\(m.needsYou ? ", needs you" : "")")
        case .header:
            break
        }
    }

    func refresh(selected: Bool, multi: Bool, cursor: Bool) {
        guard !isHeader, selected != self.selected || multi != self.multi || cursor != self.cursor else { return }
        self.selected = selected; self.multi = multi; self.cursor = cursor
        host.rootView = SidebarRowContent(item: item, selected: selected, multi: multi, cursor: cursor)
        setAccessibilityValue(selected ? "selected" : "")
    }

    override func layout() {
        super.layout()
        host.frame = bounds
    }

    override func hitTest(_ point: NSPoint) -> NSView? {
        guard !isHeader, frame.contains(point) else { return nil }
        if case .nav(let r) = item, r.kind == .empty { return nil }
        return self
    }

    override func mouseDown(with event: NSEvent) {
        downAt = event.locationInWindow
        dragged = false
    }

    override func mouseDragged(with event: NSEvent) {
        guard !isHeader, item.scope != nil, let downAt, !dragged else { return }
        let p = event.locationInWindow
        guard hypot(p.x - downAt.x, p.y - downAt.y) > Self.dragSlop else { return }
        dragged = true
        let pb = NSPasteboardItem()
        pb.setString(item.id ?? "", forType: .string)
        let di = NSDraggingItem(pasteboardWriter: pb)
        let image = snapshot()
        di.setDraggingFrame(bounds, contents: image)
        beginDraggingSession(with: [di], event: event, source: self)
    }

    override func mouseUp(with event: NSEvent) {
        defer { downAt = nil }
        guard !isHeader, !dragged else { return }
        // The caret (a group's first slot) expands without scoping.
        var caret = false
        if case .nav(let r) = item, r.expandable {
            let x = convert(event.locationInWindow, from: nil).x
            caret = x < DS.Spacing.s + DS.Spacing.xs + CGFloat(r.depth) * SidebarRowContent.indent + SidebarRowContent.caretSlot + DS.Spacing.xs
        }
        sidebar?.clicked(item, event.modifierFlags, caret: caret)
    }

    override func menu(for event: NSEvent) -> NSMenu? {
        sidebar?.menu(for: item)
    }

    nonisolated func draggingSession(_ session: NSDraggingSession, sourceOperationMaskFor context: NSDraggingContext) -> NSDragOperation {
        .generic
    }

    /// Dropped where nothing took it (another window of ours, the
    /// desktop, another display): a wall there.
    nonisolated func draggingSession(_ session: NSDraggingSession, endedAt screenPoint: NSPoint, operation: NSDragOperation) {
        MainActor.assumeIsolated {
            dragged = false
            guard !isHeader else { return }
            sidebar?.dropped(item, at: screenPoint)
        }
    }

    private func snapshot() -> NSImage {
        guard let rep = bitmapImageRepForCachingDisplay(in: bounds) else { return NSImage(size: bounds.size) }
        cacheDisplay(in: bounds, to: rep)
        let img = NSImage(size: bounds.size)
        img.addRepresentation(rep)
        return img
    }
}

// MARK: - Filter field

/// The sidebar's filter: a quiet field ("Filter projects"); in the full
/// list or while filtering, a × leaves (esc does too).
@MainActor
final class SidebarFilterField: NSView {
    let field = NSTextField()
    private let glass = NSImageView()
    private let clear: IconButtonView
    var onClear: (() -> Void)?

    override var isFlipped: Bool { true }

    override init(frame: NSRect) {
        clear = IconButtonView(symbol: "xmark", help: "Close", shortcut: "esc", target: nil, action: nil)
        super.init(frame: frame)
        wantsLayer = true
        DS.Radius.apply(DS.Radius.control, to: layer)
        layer?.borderWidth = 1
        glass.image = NSImage(systemSymbolName: "magnifyingglass", accessibilityDescription: nil)?
            .withSymbolConfiguration(.init(pointSize: DS.Spacing.m - DS.Spacing.xxs, weight: .medium))
        field.isBordered = false
        field.drawsBackground = false
        field.focusRingType = .none
        field.font = DS.nsFont(.chrome)
        field.lineBreakMode = .byTruncatingTail
        field.cell?.isScrollable = true
        field.cell?.wraps = false
        field.setAccessibilityIdentifier("sidebar.filter")
        field.setAccessibilityLabel("Filter projects")
        clear.target = self
        clear.action = #selector(clearTapped)
        clear.isHidden = true
        clear.setAccessibilityIdentifier("sidebar.filter.close")
        [glass, field, clear].forEach(addSubview)
        update(query: "", fullList: false, count: nil)
        applyTheme()
    }

    @available(*, unavailable)
    required init?(coder: NSCoder) { fatalError() }

    @objc private func clearTapped() { onClear?() }

    func update(query: String, fullList: Bool, count: String?) {
        if field.stringValue != query, field.currentEditor() == nil { field.stringValue = query }
        clear.isHidden = query.isEmpty && !fullList
        let text = count.map { "Filter \($0) projects" } ?? "Filter projects"
        field.placeholderAttributedString = NSAttributedString(string: fullList ? "Filter all projects" : text,
                                                               attributes: [.font: DS.nsFont(.chrome), .foregroundColor: Theme.ns(.dim)])
        needsLayout = true
    }

    override func layout() {
        super.layout()
        let s = DS.Spacing.m + DS.Spacing.xs
        glass.frame = NSRect(x: DS.Spacing.s, y: (bounds.height - s) / 2, width: s, height: s)
        let fh = field.intrinsicContentSize.height
        let right = clear.isHidden ? bounds.width - DS.Spacing.s : bounds.width - IconButton.side - DS.Spacing.xxs
        let x = glass.frame.maxX + DS.Spacing.xs + DS.Spacing.xxs
        field.frame = NSRect(x: x, y: ((bounds.height - fh) / 2).rounded(), width: max(0, right - x), height: fh)
        let b = IconButton.side
        clear.frame = NSRect(x: bounds.width - b - DS.Spacing.xxs, y: (bounds.height - b) / 2, width: b, height: b)
    }

    override func viewDidChangeEffectiveAppearance() {
        super.viewDidChangeEffectiveAppearance()
        applyTheme()
    }

    private func applyTheme() {
        layer?.backgroundColor = Theme.ns(.background, alpha: 0.6).cg(in: self)
        layer?.borderColor = Theme.ns(.line).cg(in: self)
        glass.contentTintColor = Theme.ns(.dim)
        field.textColor = Theme.ns(.text)
    }

    override func mouseDown(with event: NSEvent) { window?.makeFirstResponder(field) }
}
