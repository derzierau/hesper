import AppKit
import HesperCore
import SwiftUI

/// Which wall windows have a History panel, and the window's key path into
/// it (MainWindow asks first while one is open).
@MainActor
enum HistoryPanelHost {
    private final class Entry {
        weak var root: RootView?
        var panel: HistoryPanel?
        init(_ r: RootView) { root = r }
    }
    private static var entries: [ObjectIdentifier: Entry] = [:]

    /// RootView.init (marked integration point).
    static func attach(_ root: RootView) {
        entries = entries.filter { $0.value.root != nil }
        entries[ObjectIdentifier(root.model)] = Entry(root)
        _ = HistoryHub.shared(for: root.model)
    }

    static func root(for model: AppModel) -> RootView? { entries[ObjectIdentifier(model)]?.root }
    static func panel(for model: AppModel) -> HistoryPanel? { entries[ObjectIdentifier(model)]?.panel }
    static func isOpen(_ model: AppModel) -> Bool { panel(for: model)?.isOpen == true }

    private static func entry(for window: NSWindow) -> Entry? {
        entries.values.first { $0.root?.window === window }
    }

    /// ⌘Y, the menu, the sidebar's "Show History".
    static func toggle(_ model: AppModel, scope: HistoryScope? = nil, select: Session? = nil) {
        if let p = panel(for: model), p.isOpen, scope == nil, select == nil { p.close(); return }
        show(model, scope: scope, select: select, text: nil)
    }

    /// The search surface switching to its History scope (⇥ in ⌘K, the
    /// History pill, ⌘Y while ⌘K is open): the query comes along; a
    /// session from ⌘K's History section is selected in the preview.
    static func open(_ model: AppModel, text: String?, select: Session?) {
        show(model, scope: select == nil ? nil : .all, select: select, text: text)
    }

    private static func show(_ model: AppModel, scope: HistoryScope?, select: Session?, text: String?) {
        let hub = HistoryHub.shared(for: model)
        guard hub.supported == true else { NSSound.beep(); return }
        guard let e = entries[ObjectIdentifier(model)], let root = e.root else { return }
        let p = e.panel ?? HistoryPanel(model: model, root: root)
        e.panel = p
        p.open(scope: scope, select: select, text: text)
    }

    static func handleKeyEquivalent(_ event: NSEvent, window: NSWindow) -> Bool {
        guard let p = entry(for: window)?.panel, p.isOpen else { return false }
        return p.handleKey(event, equivalent: true)
    }

    static func handleKey(_ event: NSEvent, window: NSWindow) -> Bool {
        guard event.type == .keyDown, let p = entry(for: window)?.panel, p.isOpen else { return false }
        return p.handleKey(event, equivalent: false)
    }

    /// A resumed agent whose tile exists already: its left-off card now.
    static func attachResumeCards(_ agentID: String) {
        for e in entries.values {
            guard let root = e.root, let t = root.wall.tiles[agentID] else { continue }
            SessionCardOverlay.attachIfResumed(t, model: root.model)
        }
    }

    /// Ghost cards changed: every wall re-reads its items.
    static func wallsChanged() {
        for e in entries.values { e.root?.model.onAgentsChanged?() }
    }
}

/// A filter Pill / project row for tests and perf (`press()` applies it).
struct HistoryFilterHandle {
    let press: @MainActor () -> Void
}

/// History (⌘Y): the search surface in its History scope, attached to the
/// wall window over the wall (which keeps running underneath: no mode
/// change, no attach, no relayout). A modal sheet: a Night scrim over the
/// wall (it takes every click, scroll and hover; a click closes) and one
/// opaque Panel, a little above the middle: the
/// search field with the scope (All · History, ⇥ switches), the filter
/// Pills (project, Mac, tool, age, more), the list (virtualized
/// NSTableView, fixed rows drawn directly, pages by cursor ahead of the
/// scroll) and on the right the preview of the selected session.
@MainActor
final class HistoryPanel: NSView, NSTableViewDataSource, NSTableViewDelegate, NSTextFieldDelegate {
    let model: AppModel
    let hub: HistoryHub
    private weak var root: RootView?

    // State (survives close).
    private(set) var query = HistoryQuery()
    private(set) var list = HistoryList()
    private(set) var rows: [SearchRowText] = []
    private var contextProject: String?
    private(set) var isOpen = false
    private var generation = 0
    private var loadingMore = false
    private var searchWork: DispatchWorkItem?
    private var showWork: DispatchWorkItem?
    private var shownDetail: (id: String, changes: SessionChanges?)?
    private var observer: UUID?
    private var pendingSelect: Session?
    private var projectSections: [SidebarSection] = []

    // Views.
    private let panel = PanelView()
    private let box = FlippedView()
    private let magnifier = NSImageView()
    private let allPill = PillView(SearchScope.all.title, variant: .segment(selected: false))
    private let historyPill = PillView(SearchScope.history.title, variant: .segment(selected: true))
    private let tabKbd = KbdView("⇥")
    let searchField = NSTextField()
    private let headerLine = NSView()
    private let progress = NSView()
    private let progressFill = NSView()
    private let countLabel = NSTextField(labelWithString: "")
    private let filters = FilterRow()
    let table = NSTableView()
    private let scroll = NSScrollView()
    private let emptyLabel = NSTextField(labelWithString: "")
    private let divider = NSView()
    let card = SessionCardModel()
    private let cardHost: SessionCardPanel
    private let hints = NSHostingView(rootView: Hints(items: []))

    // Perf (tests): ⌘Y → first rows drawn; keystroke → rows; daemon answer → rows.
    private(set) var openStarted: CFTimeInterval = 0
    private(set) var openToFirstRowsMs: Double?
    private(set) var typedAt: CFTimeInterval = 0
    private(set) var answeredAt: CFTimeInterval = 0
    private(set) var lastTypeToRowsMs: Double?
    private(set) var lastAnswerToRowsMs: Double?
    private(set) var lastApplyMs: Double?
    private(set) var searches = 0
    var onRowsShown: (() -> Void)?
    /// ⌘C's target (tests use a private one).
    var pasteboard: NSPasteboard = .general

    static var rowHeight: CGFloat { SearchLook.rowHeight }

    override var isFlipped: Bool { true }

    init(model: AppModel, root: RootView) {
        self.model = model
        self.root = root
        hub = HistoryHub.shared(for: model)
        cardHost = SessionCardPanel(model: card)
        super.init(frame: .zero)
        wantsLayer = true
        layer?.backgroundColor = SearchLook.scrimColor(reduceTransparency: DS.reduceTransparency).cgColor
        panel.isSheet = true
        isHidden = true
        setAccessibilityElement(true)
        setAccessibilityIdentifier("history")
        setAccessibilityRole(.group)
        setAccessibilityLabel("History search")

        addSubview(panel)
        box.frame = panel.contentView.bounds
        box.autoresizingMask = [.width, .height]
        panel.contentView.addSubview(box)

        // The field row: magnifier, scope (All · History ⇥), the field.
        magnifier.image = NSImage(systemSymbolName: "magnifyingglass", accessibilityDescription: nil)
        magnifier.symbolConfiguration = .init(pointSize: CGFloat(DS.TextStyle.panelTitle.size), weight: .regular)
        magnifier.contentTintColor = Theme.ns(.dim)
        box.addSubview(magnifier)
        for (p, scope) in [(allPill, SearchScope.all), (historyPill, SearchScope.history)] {
            p.setAccessibilityElement(true)
            p.setAccessibilityRole(.radioButton)
            p.setAccessibilityLabel("Scope: \(scope.title)")
            p.setAccessibilityValue(scope == .history ? "selected" : "")
            p.toolTip = "\(scope.title) (\(scope.shortcut))"
            box.addSubview(p)
        }
        allPill.setAccessibilityIdentifier("history.scope.all")
        allPill.addGestureRecognizer(NSClickGestureRecognizer(target: self, action: #selector(allScopeClicked)))
        tabKbd.toolTip = "⇥ switches scope"
        box.addSubview(tabKbd)

        // A plain field (no search-field cell: its placeholder and cancel
        // button sat at their own insets).
        searchField.placeholderAttributedString = NSAttributedString(
            string: "Search every session: titles, prompts, answers, branches…",
            attributes: [.font: NSFont.ds(.panelTitle), .foregroundColor: Theme.ns(.dim)])
        searchField.font = .ds(.panelTitle)
        searchField.textColor = Theme.ns(.text)
        searchField.focusRingType = .none
        searchField.isBordered = false
        searchField.isBezeled = false
        searchField.drawsBackground = false
        searchField.usesSingleLineMode = true
        searchField.lineBreakMode = .byTruncatingTail
        searchField.cell?.isScrollable = true
        searchField.cell?.wraps = false
        searchField.delegate = self
        searchField.setAccessibilityIdentifier("history.search")
        box.addSubview(searchField)

        for v in [headerLine, divider] { v.wantsLayer = true; box.addSubview(v) }
        // Indexing: a thin bar along the field's hairline.
        progress.wantsLayer = true
        progressFill.wantsLayer = true
        progress.addSubview(progressFill)
        progress.isHidden = true
        progress.setAccessibilityElement(true)
        progress.setAccessibilityRole(.progressIndicator)
        progress.setAccessibilityIdentifier("history.indexing")
        box.addSubview(progress)

        countLabel.font = .ds(.meta)
        countLabel.textColor = Theme.ns(.dim)
        countLabel.alignment = .right
        countLabel.lineBreakMode = .byTruncatingHead
        box.addSubview(countLabel)
        emptyLabel.font = .ds(.body)
        emptyLabel.textColor = Theme.ns(.dim)
        emptyLabel.alignment = .center
        emptyLabel.lineBreakMode = .byTruncatingTail
        box.addSubview(emptyLabel)

        box.addSubview(filters)

        let col = NSTableColumn(identifier: .init("s"))
        table.addTableColumn(col)
        table.headerView = nil
        table.rowHeight = Self.rowHeight
        table.usesAutomaticRowHeights = false
        table.intercellSpacing = .zero
        table.backgroundColor = .clear
        table.selectionHighlightStyle = .none
        table.style = .plain
        table.focusRingType = .none
        table.allowsEmptySelection = true
        table.dataSource = self
        table.delegate = self
        table.target = self
        table.doubleAction = #selector(doubleClicked)
        table.setAccessibilityIdentifier("history.list")
        table.setAccessibilityLabel("Sessions")
        scroll.documentView = table
        scroll.drawsBackground = false
        scroll.hasVerticalScroller = true
        scroll.autohidesScrollers = true
        scroll.scrollerStyle = .overlay
        scroll.contentView.postsBoundsChangedNotifications = true
        box.addSubview(scroll)
        NotificationCenter.default.addObserver(forName: NSView.boundsDidChangeNotification, object: scroll.contentView, queue: .main) { [weak self] _ in
            MainActor.assumeIsolated { self?.scrolled() }
        }

        box.addSubview(cardHost)
        observeChanges { [weak self] in self?.cardHost.update() }
        card.onAction = { [weak self] a in self?.perform(a) }
        card.onStartContinue = { [weak self] in self?.startContinue() }
        card.onCancelContinue = { [weak self] in self?.cancelContinue() }

        box.addSubview(hints)
        updateHints()
        applyTheme()

        observer = hub.observe { [weak self] c in self?.hubChanged(c) }
    }

    @available(*, unavailable)
    required init?(coder: NSCoder) { fatalError() }

    override func viewDidChangeEffectiveAppearance() {
        super.viewDidChangeEffectiveAppearance()
        applyTheme()
    }

    private func applyTheme() {
        for v in [headerLine, divider, progress] { v.layer?.backgroundColor = Theme.ns(.line).cg(in: self) }
        progressFill.layer?.backgroundColor = Theme.ns(.working).cg(in: self)
        layer?.backgroundColor = SearchLook.scrimColor(reduceTransparency: DS.reduceTransparency).cgColor
    }

    // MARK: Open / close

    func open(scope: HistoryScope?, select: Session?, text: String? = nil) {
        guard let root else { return }
        openStarted = CACurrentMediaTime()
        openToFirstRowsMs = nil
        model.closePopover()
        model.showPalette = false
        contextProject = Self.contextProject(model)
        if superview == nil { root.addHistoryPanel(self) }
        let firstOpen = !list.loaded
        if let scope {
            query.scope = scope
        } else if let text, !text.trimmingCharacters(in: .whitespaces).isEmpty {
            query.scope = .all // from ⌘K: it searched every project
        } else if firstOpen, let c = contextProject {
            query.scope = .project(c)
        }
        pendingSelect = select
        if let text {
            query.text = text
            searchField.stringValue = text
        } else if select != nil {
            query.text = ""
            searchField.stringValue = ""
        }
        if let select { pendingSelectID = select.id }
        reloadProjects()
        rebuildFilters()
        updateIndexing()
        isOpen = true
        isHidden = false
        frame = root.historyFrame
        applyTheme() // Reduce Transparency may have changed
        hoverBlock.suspend(under: self)
        needsLayout = true
        // Open like every floating layer: scale 0.98 → 1 and a fade (DS.Motion.quick).
        if let a = DS.caAnimation(.quick, keyPath: "transform") {
            alphaValue = 0
            DS.animate(.quick) { self.animator().alphaValue = 1 }
            a.fromValue = CATransform3DMakeScale(SearchLook.openScale, SearchLook.openScale, 1)
            a.toValue = CATransform3DIdentity
            panel.layer?.add(a, forKey: "open")
        }
        window?.makeFirstResponder(searchField)
        (window?.firstResponder as? NSTextView)?.moveToEndOfDocument(nil)
        updateHints()
        if !firstOpen && pendingSelect == nil && text == nil {
            // Warm: the last results draw at once; refresh underneath.
            noteRowsShown()
        }
        search(immediately: true)
        hub.refreshStats()
    }

    private var pendingSelectID: String?

    func close() {
        guard isOpen else { return }
        isOpen = false
        cancelContinue()
        hoverBlock.resume()
        DS.animate(.quick, { self.animator().alphaValue = 0 }, completion: { [weak self] in
            guard let self, !self.isOpen else { return }
            self.isHidden = true
            self.alphaValue = 1
        })
        root?.historyClosed()
    }

    static func contextProject(_ m: AppModel) -> String? {
        if let id = m.selectedID, let item = m.scopedWallItems.first(where: { $0.id == id }), let p = m.projectID(of: item) { return p }
        if case .project(let p) = m.scope { return p }
        return nil
    }

    // MARK: The scrim: it takes every mouse event over the wall

    /// Hover and tooltips of the views underneath (SwiftUI tracking areas
    /// fire through overlapping views) are off while the sheet is open.
    private let hoverBlock = HoverSuspension()
    private var scrimTracking: NSTrackingArea?

    override func updateTrackingAreas() {
        super.updateTrackingAreas()
        if let t = scrimTracking { removeTrackingArea(t) }
        let t = NSTrackingArea(rect: .zero, options: [.mouseMoved, .mouseEnteredAndExited, .cursorUpdate, .activeAlways, .inVisibleRect],
                               owner: self, userInfo: nil)
        addTrackingArea(t)
        scrimTracking = t
    }

    override func cursorUpdate(with event: NSEvent) { NSCursor.arrow.set() }
    override func mouseMoved(with event: NSEvent) {}
    override func mouseEntered(with event: NSEvent) {}
    override func mouseExited(with event: NSEvent) {}
    override func mouseDragged(with event: NSEvent) {}
    override func mouseUp(with event: NSEvent) {}
    override func rightMouseDown(with event: NSEvent) {}
    override func otherMouseDown(with event: NSEvent) {}
    override func scrollWheel(with event: NSEvent) {}
    override func magnify(with event: NSEvent) {}
    override func acceptsFirstMouse(for event: NSEvent?) -> Bool { true }

    override func mouseDown(with event: NSEvent) {
        // A click on the scrim (outside the panel) closes.
        if !panel.frame.contains(convert(event.locationInWindow, from: nil)) { close() }
    }

    @objc private func allScopeClicked() { switchScope() }

    /// ⇥ / the All pill: ⌘K's All scope with this query.
    func switchScope() {
        model.switchSearchScope(to: .all, text: query.text)
    }

    // MARK: Layout

    /// Layout passes (perf).
    nonisolated(unsafe) static var layouts = 0

    override func layout() {
        super.layout()
        Self.layouts += 1
        panel.frame = SearchLook.historyFrame(in: bounds.size)
        // The Panel's content (and the box in it) at their size now, not
        // in the Panel's own layout pass after this one.
        panel.layoutSubtreeIfNeeded()
        box.frame = panel.contentView.bounds
        layoutBox()
    }

    private func layoutBox() {
        let w = box.bounds.width, h = box.bounds.height
        let pad = DS.Spacing.xl, fh = SearchLook.fieldHeight
        // Field row.
        let ms = CGFloat(DS.TextStyle.panelTitle.size) + DS.Spacing.xs
        magnifier.frame = NSRect(x: pad, y: ((fh - ms) / 2).rounded(), width: ms, height: ms)
        var x = magnifier.frame.maxX + DS.Spacing.m
        for p in [allPill, historyPill] {
            let pw = p.intrinsicContentSize.width
            p.frame = NSRect(x: x, y: ((fh - Pill.height) / 2).rounded(), width: pw, height: Pill.height)
            x += pw + DS.Spacing.xs
        }
        let ks = tabKbd.intrinsicContentSize
        tabKbd.frame = NSRect(x: x, y: ((fh - ks.height) / 2).rounded(), width: ks.width, height: ks.height)
        x = tabKbd.frame.maxX + DS.Spacing.m
        let fieldH = ceil(searchField.intrinsicContentSize.height)
        searchField.frame = NSRect(x: x, y: ((fh - fieldH) / 2).rounded(), width: max(0, w - pad - x), height: fieldH)
        headerLine.frame = NSRect(x: 0, y: fh, width: w, height: 1)
        progress.frame = NSRect(x: 0, y: fh, width: w, height: DS.Spacing.xxs)
        updateProgressFill()

        // Footer.
        let footerTop = h - SearchLook.footerHeight
        hints.frame = NSRect(x: 0, y: footerTop, width: w, height: SearchLook.footerHeight)

        // Preview (right), the list with its filters (left).
        let previewW = min(SearchLook.previewMax, max(SearchLook.previewMin, (w * SearchLook.previewFraction).rounded()))
        let paneTop = fh + 1
        divider.frame = NSRect(x: w - previewW - 1, y: paneTop, width: 1, height: max(0, footerTop - paneTop))
        cardHost.frame = NSRect(x: w - previewW, y: paneTop, width: previewW, height: max(0, footerTop - paneTop))
        let listW = divider.frame.minX
        let fy = paneTop + DS.Spacing.m
        let countW = min(listW / 3, ceil(countLabel.intrinsicContentSize.width) + DS.Spacing.xs)
        countLabel.frame = NSRect(x: listW - DS.Spacing.l - countW, y: fy + ((Pill.height - countLabel.intrinsicContentSize.height) / 2).rounded(),
                                  width: countW, height: countLabel.intrinsicContentSize.height)
        filters.frame = NSRect(x: DS.Spacing.l, y: fy, width: max(0, countLabel.frame.minX - DS.Spacing.l - DS.Spacing.m), height: Pill.height)
        let listTop = fy + Pill.height + DS.Spacing.m
        scroll.frame = NSRect(x: DS.Spacing.s, y: listTop, width: max(0, listW - 2 * DS.Spacing.s), height: max(0, footerTop - listTop - DS.Spacing.xs))
        table.tableColumns.first?.width = scroll.contentSize.width
        emptyLabel.frame = NSRect(x: DS.Spacing.l, y: listTop + DS.Spacing.xxl, width: max(0, listW - 2 * DS.Spacing.l), height: emptyLabel.intrinsicContentSize.height)
    }

    // MARK: Search

    func controlTextDidChange(_ obj: Notification) {
        guard query.text != searchField.stringValue else { return }
        query.text = searchField.stringValue
        typedAt = CACurrentMediaTime()
        search(immediately: false)
    }

    func control(_ control: NSControl, textView: NSTextView, doCommandBy commandSelector: Selector) -> Bool {
        // ↑↓ ⏎ ⇥ esc reach the panel through the window first; nothing here.
        false
    }

    /// Debounced 60 ms while typing; the call, decoding and row text run
    /// off the main thread; stale answers are dropped.
    func search(immediately: Bool) {
        searchWork?.cancel()
        let w = DispatchWorkItem { [weak self] in MainActor.assumeIsolated { self?.runSearch() } }
        searchWork = w
        if immediately { w.perform() } else { DispatchQueue.main.asyncAfter(deadline: .now() + 0.06, execute: w) }
    }

    private func machineNames() -> [String: String] {
        MachineLabel.names(model.machines)
    }

    private func runSearch() {
        generation += 1
        let gen = generation
        let q = query
        let client = hub.client
        let names = machineNames()
        searches += 1
        Task { [weak self] in
            let r = await Task.detached { () -> Result<(SessionPage, [SearchRowText]), any Error> in
                do {
                    let page = try await client.searchSessions(q)
                    let now = Date()
                    return .success((page, page.items.map { SearchRowText($0, machines: names, now: now) }))
                } catch { return .failure(error) }
            }.value
            guard let self, gen == self.generation else { return }
            self.answeredAt = CACurrentMediaTime()
            switch r {
            case .success(let (page, rows)):
                self.applyFirstPage(page, rows)
            case .failure(let e):
                self.emptyLabel.stringValue = "Could not search: \(self.model.describe(e))"
                self.emptyLabel.isHidden = false
            }
        }
    }

    private func applyFirstPage(_ page: SessionPage, _ newRows: [SearchRowText]) {
        let t0 = CACurrentMediaTime()
        let previous = selectedSession?.id
        // A page asked for before an archive / delete still lists it.
        let hidden = hiddenIDs()
        list.replace(with: hidden.isEmpty ? page : SessionPage(items: page.items.filter { !hidden.contains($0.id) }, cursor: page.cursor))
        rows = hidden.isEmpty ? newRows : newRows.filter { !hidden.contains($0.id) }
        table.reloadData()
        var sel: Int?
        if let id = pendingSelectID, let i = list.row(of: id) { sel = i; pendingSelectID = nil } else if let id = previous, let i = list.row(of: id) { sel = i } else if !rows.isEmpty { sel = 0 }
        if let id = pendingSelectID, sel == nil || list.items[sel!].id != id, let s = pendingSelect {
            // The session to show isn't in this page (⇥ from ⌘K): show it on top.
            list.replace(with: SessionPage(items: [s] + page.items.filter { $0.id != s.id }, cursor: page.cursor))
            let names = machineNames()
            rows = list.items.map { SearchRowText($0, machines: names) }
            table.reloadData()
            sel = 0
            pendingSelectID = nil
        }
        pendingSelect = nil
        if let sel {
            table.selectRowIndexes(IndexSet(integer: sel), byExtendingSelection: false)
            table.scrollRowToVisible(sel)
        } else {
            table.deselectAll(nil)
        }
        selectionChanged()
        updateCount()
        lastApplyMs = (CACurrentMediaTime() - t0) * 1000
        noteRowsShown()
        loadMoreIfNeeded()
    }

    /// Offscreen renders and tests: show these sessions as if the daemon
    /// had answered (no search runs).
    func showFixture(_ sessions: [Session], hasMore: Bool = false, select row: Int = 0, changes: SessionChanges? = nil) {
        let names = machineNames()
        let page = SessionPage(items: sessions, cursor: hasMore ? "more" : nil)
        generation += 1
        applyFirstPage(page, sessions.map { SearchRowText($0, machines: names) })
        if row != 0 { self.select(row: row) }
        showWork?.cancel()
        if let s = selectedSession, let changes {
            shownDetail = (s.id, changes)
            refreshCard(keepChanges: true)
        }
        rebuildFilters()
        updateHints()
    }

    private func noteRowsShown() {
        let now = CACurrentMediaTime()
        if openToFirstRowsMs == nil { openToFirstRowsMs = (now - openStarted) * 1000 }
        if typedAt > 0 { lastTypeToRowsMs = (now - typedAt) * 1000; typedAt = 0 }
        if answeredAt > 0 { lastAnswerToRowsMs = (now - answeredAt) * 1000; answeredAt = 0 }
        onRowsShown?()
    }

    private func updateCount() {
        let n = rows.count
        let count = n == 0 ? "" : "\(n)\(list.hasMore ? "+" : "") session\(n == 1 ? "" : "s")"
        let label = hub.indexing.map { "indexing \($0.done.formatted()) of \($0.total.formatted())…" } ?? count
        if countLabel.stringValue != label {
            let relayout = countLabel.stringValue.count != label.count
            countLabel.stringValue = label
            if relayout { needsLayout = true } // its width (not on every page)
        }
        if !list.loaded {
            emptyLabel.stringValue = "Loading…"
        } else if rows.isEmpty {
            emptyLabel.stringValue = query.trimmedText.isEmpty ? "No sessions here" : "No session mentions “\(query.trimmedText)”"
        }
        emptyLabel.isHidden = !rows.isEmpty
    }

    // MARK: Paging (cursor, ahead of the scroll position)

    private func scrolled() { loadMoreIfNeeded() }

    private func loadMoreIfNeeded() {
        guard !loadingMore, list.hasMore else { return }
        let visible = table.rows(in: table.visibleRect)
        let last = visible.location + visible.length
        guard list.needsMore(lastVisibleRow: last, ahead: 60) else { return }
        loadingMore = true
        let gen = generation
        let q = query, cursor = list.cursor
        let client = hub.client
        let names = machineNames()
        Task { [weak self] in
            let r = await Task.detached { () -> (SessionPage, [SearchRowText])? in
                guard let page = try? await client.searchSessions(q, cursor: cursor) else { return nil }
                let now = Date()
                return (page, page.items.map { SearchRowText($0, machines: names, now: now) })
            }.value
            guard let self else { return }
            self.loadingMore = false
            guard gen == self.generation, let (page, newRows) = r else { return }
            let before = self.list.items.count
            let range = self.list.append(page)
            // Only rows not already shown (append drops duplicates).
            let added = newRows.filter { r in self.list.row(of: r.id).map { $0 >= before } ?? false }
            self.rows.append(contentsOf: added)
            if !range.isEmpty { self.table.insertRows(at: IndexSet(integersIn: range), withAnimation: []) }
            self.updateCount()
            self.loadMoreIfNeeded()
        }
    }

    // MARK: Hub events (coalesced ≤ 4/s)

    private func hubChanged(_ c: HistoryHub.Change) {
        switch c {
        case .stats: reloadProjects(); rebuildFilters(); updateIndexing()
        case .indexing: updateIndexing()
        case .support: if hub.supported == false { close() }
        case .ghosts: break
        case .restored(let s):
            recentlyHidden.removeValue(forKey: s.id)
            if let i = list.restore(s, query: query) {
                rows.insert(SearchRowText(s, machines: machineNames()), at: i)
                let sel = selectedSession?.id
                table.reloadData()
                if let sel, let j = list.row(of: sel) { table.selectRowIndexes(IndexSet(integer: j), byExtendingSelection: false) }
                updateCount()
            }
        case .sessions(let changed, let removed):
            guard isOpen || list.loaded else { return }
            let names = machineNames()
            let merge = list.merge(changed: changed, removed: removed, query: query)
            let structural = merge.structural
            var reload = IndexSet()
            if structural {
                // Re-align the row texts: unchanged rows keep theirs.
                var known: [String: SearchRowText] = [:]
                for r in rows { known[r.id] = r }
                let touched = Set(changed.map(\.id))
                rows = list.items.map { s in (touched.contains(s.id) ? nil : known[s.id]) ?? SearchRowText(s, machines: names) }
            } else {
                for i in merge.updated { rows[i] = SearchRowText(list.items[i], machines: names); reload.insert(i) }
            }
            if structural {
                let sel = selectedSession?.id
                table.reloadData()
                if let sel, let i = list.row(of: sel) { table.selectRowIndexes(IndexSet(integer: i), byExtendingSelection: false) }
                updateCount()
                selectionChanged()
            } else if !reload.isEmpty {
                table.reloadData(forRowIndexes: reload, columnIndexes: IndexSet(integer: 0))
                if let s = selectedSession, changed.contains(where: { $0.id == s.id }) { refreshCard(keepChanges: true) }
            }
        }
    }

    private func updateIndexing() {
        if let p = hub.indexing {
            progress.isHidden = false
            progressFraction = p.fraction
            progress.setAccessibilityValue("Indexing \(p.done.formatted()) of \(p.total.formatted()) sessions")
        } else {
            progress.isHidden = true
        }
        updateCount()
    }

    private var progressFraction = 0.0 { didSet { updateProgressFill() } }
    private func updateProgressFill() {
        progressFill.frame = NSRect(x: 0, y: 0, width: (progress.bounds.width * CGFloat(progressFraction)).rounded(), height: progress.bounds.height)
    }

    // MARK: Filters (Project, Mac, Tool, Age, More)

    private func reloadProjects() {
        projectSections = HistorySidebar.make(catalog: model.catalog, stats: hub.stats)
    }

    private func filterMenus() -> [HistoryFilterMenu] {
        let ctxName = contextProject.map { model.catalog.name(project: $0) }
        let macs = model.machines.map { (short: $0.short, name: $0.displayName) }
        return HistoryFilters.make(query, context: contextProject, contextName: ctxName, machines: macs, projects: projectSections)
    }

    private func rebuildFilters() {
        filters.show(filterMenus()) { [weak self] a in self?.applyFilter(a) }
    }

    private func applyFilter(_ a: HistoryFilterMenu.Action) {
        let q = HistoryFilters.apply(a, to: query, context: contextProject)
        guard q != query else { return }
        query = q
        rebuildFilters()
        search(immediately: true)
    }

    /// Tests / perf: a filter by its old chip id ("kind:codex", "mac:M",
    /// "time" (cycles), "live", "archived"…).
    func chip(_ id: String) -> HistoryFilterHandle? {
        let macs = model.machines.map { (short: $0.short, name: $0.displayName) }
        guard let c = HistoryChips.make(query, context: contextProject, contextName: nil, machines: macs).first(where: { $0.id == id }) else { return nil }
        return HistoryFilterHandle { [weak self] in self?.applyFilter(.chip(c.kind)) }
    }

    /// The project filter's rows (All, projects with session counts,
    /// Scratch, Elsewhere).
    var projectRows: [SidebarRow] { projectSections.flatMap(\.rows) }

    /// Tests / perf: choose a project row ("all", "p:<id>", HistorySidebar.scratchRow…).
    func projectRow(_ id: String) -> HistoryFilterHandle? {
        guard let r = projectRows.first(where: { $0.id == id }) else { return nil }
        let s = HistorySidebar.scope(of: r)
        return HistoryFilterHandle { [weak self] in
            guard let self, self.query.scope != s else { return }
            self.applyFilter(.scope(s))
        }
    }

    // MARK: Table

    func numberOfRows(in tableView: NSTableView) -> Int { rows.count }

    func tableView(_ tableView: NSTableView, viewFor tableColumn: NSTableColumn?, row: Int) -> NSView? {
        let cell = (tableView.makeView(withIdentifier: HistoryCell.id, owner: nil) as? HistoryCell) ?? HistoryCell()
        cell.show(rows[row])
        return cell
    }

    func tableView(_ tableView: NSTableView, rowViewForRow row: Int) -> NSTableRowView? {
        (tableView.makeView(withIdentifier: HistoryRowView.id, owner: nil) as? HistoryRowView) ?? HistoryRowView()
    }

    func tableViewSelectionDidChange(_ notification: Notification) { selectionChanged() }

    @objc private func doubleClicked() {
        if table.clickedRow >= 0 { perform(.resume) }
    }

    var selectedSession: Session? {
        let r = table.selectedRow
        return r >= 0 && r < list.items.count ? list.items[r] : nil
    }

    func select(row: Int) {
        guard !rows.isEmpty else { return }
        let r = max(0, min(rows.count - 1, row))
        table.selectRowIndexes(IndexSet(integer: r), byExtendingSelection: false)
        table.scrollRowToVisible(r)
    }

    /// The preview renders at once from the row's fields; git details
    /// (sessions.show) come 120 ms after the selection settles, into
    /// their reserved slot.
    /// The slowest selection change / preview update so far (perf).
    nonisolated(unsafe) static var maxSelectMs = 0.0

    private func selectionChanged() {
        let t0 = CACurrentMediaTime()
        defer { Self.maxSelectMs = max(Self.maxSelectMs, (CACurrentMediaTime() - t0) * 1000) }
        if card.mode == .continuing, selectedSession?.id != continuingID { cancelContinue() } // a reload re-selecting the same row keeps it
        refreshCard(keepChanges: false)
        showWork?.cancel()
        guard let s = selectedSession else { return }
        if shownDetail?.id == s.id { return }
        let w = DispatchWorkItem { [weak self] in MainActor.assumeIsolated { self?.loadDetail(s.id) } }
        showWork = w
        DispatchQueue.main.asyncAfter(deadline: .now() + 0.12, execute: w)
    }

    private var onlineMachines: [String] { model.machines.filter(\.online).map(\.short) }

    private func refreshCard(keepChanges: Bool) {
        guard let s = selectedSession else { card.text = nil; return }
        let changes = shownDetail?.id == s.id ? shownDetail?.changes : nil
        if !keepChanges && shownDetail?.id != s.id { shownDetail = nil }
        card.kind = s.kind
        let names = machineNames()
        var t = SearchLook.plain(SessionCardText(s, changes: changes, loadingChanges: changes == nil, local: model.localMachine, machines: names), s)
        t.subtitle = SearchLook.previewMeta(s, machines: names)
        // The search surface's keys: ⏎ resume, ⌥⏎ fork, ⌘⏎ continue on the
        // other Mac (a live agent: moves it with its work), R restore checkpoint.
        let moveTo = model.liveMoveTarget(s)?.to
        t.actions = SearchPreview.actions(s, card: t, local: model.localMachine, online: onlineMachines, machines: names,
                                          moveTo: moveTo, restore: model.restoreOffered(s))
        t.resumeHereNote = SearchPreview.continueNote(s, local: model.localMachine, online: onlineMachines, machines: names, moveTo: moveTo)
        card.text = t
    }

    private func loadDetail(_ id: String) {
        let client = hub.client
        Task { [weak self] in
            let d = await Task.detached { try? await client.showSession(id) }.value
            guard let self, self.selectedSession?.id == id else { return }
            self.shownDetail = (id, d?.changes ?? SessionChanges(files: [], worktreeExists: false))
            self.refreshCard(keepChanges: true)
        }
    }

    /// Sessions archived / deleted here in the last 5 s (stale pages).
    private var recentlyHidden: [String: Date] = [:]
    private func hide(_ id: String) { recentlyHidden[id] = Date() }
    private func hiddenIDs() -> Set<String> {
        recentlyHidden = recentlyHidden.filter { Date().timeIntervalSince($0.value) < 5 }
        return Set(recentlyHidden.keys)
    }

    // MARK: Keys

    private var searchHasFocus: Bool {
        guard let fe = window?.firstResponder as? NSTextView else { return false }
        return fe.delegate as? NSTextField === searchField
    }

    private var briefHasFocus: Bool {
        guard card.mode == .continuing, let tv = window?.firstResponder as? NSTextView else { return false }
        return !(tv.delegate as? NSTextField === searchField)
    }

    func handleKey(_ event: NSEvent, equivalent: Bool) -> Bool {
        let chord = KeyChord(event: event)
        if card.mode == .continuing {
            if chord.command && chord.key == .enter { if !equivalent { return false }; startContinue(); return true }
            if chord.key == .escape && !chord.command { if equivalent { return false }; cancelContinue(); return true }
            if briefHasFocus { return false }
        }
        let focus: HistoryFocus = searchHasFocus ? .search : .list
        let fieldSel = searchHasFocus && ((window?.firstResponder as? NSTextView)?.selectedRange().length ?? 0) > 0
        let action = SearchKeys.history(chord, keyCode: event.keyCode, characters: event.characters, focus: focus, fieldHasSelection: fieldSel)
        if equivalent {
            // ⌘ chords: ⌘F ⌘C ⌘Y ⌘↑ ⌘↓ ⌘⏎ are the panel's; ⌘K is the All
            // scope (the query comes along); the rest (⌘Z, ⌘W…) the window's.
            guard chord.command else { return false }
            if chord.key == .char("k") && !chord.option && !chord.control { switchScope(); return true }
            if action == .history(.pass) { return false }
        } else if chord.command {
            return false // performKeyEquivalent handles ⌘ chords
        }
        if action == .history(.pass) { return false }
        perform(action)
        return true
    }

    func perform(_ a: SearchKeyAction) {
        switch a {
        case .history(let h): perform(h)
        case .switchScope: switchScope()
        case .toggleFocus: perform(searchHasFocus ? .focusList : .focusSearch(nil))
        case .continueOnOtherMac:
            guard let s = selectedSession else { NSSound.beep(); return }
            continueOnOtherMac(s)
        case .restoreCheckpoint:
            // Nothing to restore: R types into the search, as before.
            guard let s = selectedSession, model.restoreOffered(s) else { perform(HistoryKeyAction.focusSearch("r")); return }
            model.restoreCheckpoint(s)
        }
    }

    func perform(_ a: HistoryKeyAction) {
        switch a {
        case .up: select(row: table.selectedRow - 1)
        case .down: select(row: table.selectedRow < 0 ? 0 : table.selectedRow + 1)
        case .pageUp: select(row: table.selectedRow - max(1, Int(scroll.contentSize.height / Self.rowHeight) - 1))
        case .pageDown: select(row: table.selectedRow + max(1, Int(scroll.contentSize.height / Self.rowHeight) - 1))
        case .first: select(row: 0)
        case .last: select(row: rows.count - 1)
        case .close: close()
        case .focusList:
            window?.makeFirstResponder(table)
            updateHints()
        case .focusSearch(let text):
            window?.makeFirstResponder(searchField)
            if let text {
                searchField.stringValue += text
                (window?.firstResponder as? NSTextView)?.moveToEndOfDocument(nil)
                controlTextDidChange(Notification(name: NSControl.textDidChangeNotification))
            }
            updateHints()
        case .resume, .resumeHere, .fork, .continueOther, .archive, .delete, .copyID:
            guard let s = selectedSession else { NSSound.beep(); return }
            act(a, on: s)
        case .pass: break
        }
    }

    private func updateHints() {
        let list = !searchHasFocus
        // One line: the keys the preview's buttons don't already show.
        hints.rootView = Hints(items: list
            ? [("↑↓", "select"), ("⇧⇥", "search"), ("⇥", "All"), ("esc", "close")]
            : [("↑↓", "select"), ("⇧⇥", "list"), ("⇥", "All"), ("esc", "close")])
    }

    // MARK: Actions

    private func act(_ a: HistoryKeyAction, on s: Session) {
        let actions = HistoryActions(model: model, hub: hub)
        let changes = shownDetail?.id == s.id ? shownDetail?.changes : nil
        switch a {
        case .resume:
            actions.resume(s, here: false, changes: changes) { [weak self] in self?.close() }
        case .resumeHere:
            continueOnOtherMac(s)
        case .fork:
            card.busy = "forking…"
            actions.fork(s) { [weak self] ok in self?.card.busy = nil; if ok { self?.close() } }
        case .continueOther:
            beginContinue(s)
        case .archive:
            var n = s
            n.archived.toggle()
            hide(s.id)
            hubChanged(.sessions(changed: [n], removed: []))
            actions.archive(s)
        case .delete:
            if s.isLive { model.showToast("\(s.title) is running: stop it first", error: true); return }
            hide(s.id)
            hubChanged(.sessions(changed: [], removed: [s.id]))
            actions.delete(s)
        case .copyID:
            pasteboard.clearContents()
            pasteboard.setString(s.sessionId.isEmpty ? s.id : s.sessionId, forType: .string)
            model.showToast("Copied the session id \(s.sessionId.isEmpty ? s.id : s.sessionId)")
        default: break
        }
    }

    /// ⌘⏎: resume on the other Mac (another Mac's session comes here; this
    /// Mac's goes to the other one). Moves ownership (sessions.resume {machine}).
    private func continueOnOtherMac(_ s: Session) {
        if let m = model.liveMoveTarget(s) {
            // A live agent moves with its worktree and conversation; its
            // tile shows how far it got.
            close()
            model.requestMove(m.agent, to: m.to)
            return
        }
        if s.isLive || s.movedTo != nil {
            // Nothing to move: ⏎'s own behavior (open the live agent / its new home).
            return act(.resume, on: s)
        }
        guard let target = SearchPreview.continueTarget(s, local: model.localMachine, online: onlineMachines) else {
            model.showToast("No other Mac online to continue “\(SessionFormat.oneLine(s.title, max: 40))” on", error: true)
            return
        }
        card.busy = "moving…"
        let changes = shownDetail?.id == s.id ? shownDetail?.changes : nil
        HistoryActions(model: model, hub: hub).resume(s, on: target, changes: changes, failed: { [weak self] in self?.card.busy = nil }) { [weak self] in
            self?.card.busy = nil
            self?.close()
        }
    }

    private func beginContinue(_ s: Session) {
        card.mode = .continuing
        continuingID = s.id
        card.continueKind = s.otherKind
        card.brief = ""
        card.briefLoading = true
        let client = hub.client
        Task { [weak self] in
            let text = await Task.detached { try? await client.sessionBrief(s.id) }.value
            guard let self, self.card.mode == .continuing, self.selectedSession?.id == s.id else { return }
            self.card.brief = text ?? "Continue the task “\(s.title)” in \(s.cwd).\nLast ask: \(s.lastUser)\nLast state: \(s.lastAssistant)"
            self.card.briefLoading = false
            self.originalBrief = self.card.brief
        }
    }

    private var originalBrief = ""
    private var continuingID: String?

    func startContinue() {
        guard card.mode == .continuing, let s = selectedSession else { return }
        let brief = card.brief == originalBrief ? nil : card.brief
        card.busy = "starting…"
        HistoryActions(model: model, hub: hub).continueAs(s, kind: card.continueKind, brief: brief) { [weak self] ok in
            self?.card.busy = nil
            if ok { self?.cancelContinue(); self?.close() }
        }
    }

    func cancelContinue() {
        guard card.mode == .continuing else { return }
        card.mode = .card
        card.briefLoading = false
        if isOpen { window?.makeFirstResponder(table) }
    }

    deinit {
        MainActor.assumeIsolated { hub.unobserve(observer) }
    }
}

// MARK: Rows

/// A row's background: the selection (`working` at 14%, radius
/// `control`); nothing otherwise.
final class HistoryRowView: NSTableRowView {
    static let id = NSUserInterfaceItemIdentifier("historyRow")

    override init(frame frameRect: NSRect) {
        super.init(frame: frameRect)
        identifier = Self.id
        wantsLayer = true
    }

    @available(*, unavailable)
    required init?(coder: NSCoder) { fatalError() }

    override var isSelected: Bool { didSet { needsDisplay = true } }

    override func drawBackground(in dirtyRect: NSRect) {
        guard isSelected else { return }
        let r = bounds.insetBy(dx: 0, dy: DS.Spacing.xxs / 2)
        Theme.ns(.working, alpha: SearchLook.selectionAlpha).setFill()
        NSBezierPath(roundedRect: r, xRadius: DS.Radius.control, yRadius: DS.Radius.control).fill()
    }

    override func drawSelection(in dirtyRect: NSRect) {}
}

/// One session, on the Row primitive's spec: its square (filled `working`
/// while live, outlined idle when past), the title at 600, "machine · age"
/// in Mono on the right, and the snippet with its matches on Horizon
/// amber. Drawn directly (no subviews, no SwiftUI): a recycled cell costs
/// a few string draws, so scrolling 2000 rows holds 120 fps.
final class HistoryCell: NSTableCellView {
    static let id = NSUserInterfaceItemIdentifier("historyCell")
    private(set) var shown: SearchRowText?
    private var strings: (title: NSAttributedString, meta: NSAttributedString, snippet: NSAttributedString)?
    private var mark: StateMarkKind = .idle

    private static let tail: NSParagraphStyle = { let p = NSMutableParagraphStyle(); p.lineBreakMode = .byTruncatingTail; return p }()
    private static let right: NSParagraphStyle = {
        let p = NSMutableParagraphStyle(); p.lineBreakMode = .byTruncatingTail; p.alignment = .right; return p
    }()
    private static let fTitle = NSFont.ds(.body, .semibold)
    private static let fMeta = NSFont.ds(.meta)
    private static let fSnippet = NSFont.ds(.chrome)
    private static let fMatch = NSFont.ds(.chrome, .medium)
    private static func lineHeight(_ f: NSFont) -> CGFloat { ceil(f.ascender - f.descender + f.leading) }
    private static let hTitle = lineHeight(fTitle), hSnippet = lineHeight(fSnippet), hMeta = lineHeight(fMeta)

    override var isFlipped: Bool { true }

    init() {
        super.init(frame: .zero)
        identifier = Self.id
        setAccessibilityElement(true)
        setAccessibilityRole(.row)
    }

    @available(*, unavailable)
    required init?(coder: NSCoder) { fatalError() }

    func show(_ r: SearchRowText) {
        guard shown != r else { return }
        shown = r
        mark = SearchLook.markKind(r.mark)
        strings = (
            NSAttributedString(string: r.title, attributes: [.font: Self.fTitle, .foregroundColor: Theme.ns(r.title == "(untitled)" ? .dim : .text),
                                                             .paragraphStyle: Self.tail]),
            NSAttributedString(string: r.meta, attributes: [.font: Self.fMeta, .foregroundColor: Theme.ns(.dim), .paragraphStyle: Self.right]),
            SearchLook.snippet(r, font: Self.fSnippet, matchFont: Self.fMatch, paragraph: Self.tail)
        )
        setAccessibilityIdentifier("history.row.\(r.id)")
        setAccessibilityLabel(r.accessibilityLabel)
        needsDisplay = true
    }

    override func draw(_ dirtyRect: NSRect) {
        guard let s = strings, let cg = NSGraphicsContext.current?.cgContext else { return }
        let w = bounds.width
        let pad = DS.Spacing.m, gap = DS.Spacing.m
        // Two lines (title, snippet) centered; one when there is no snippet.
        let twoLines = s.snippet.length > 0
        let block = twoLines ? Self.hTitle + DS.Spacing.xxs + Self.hSnippet : Self.hTitle
        let top = ((bounds.height - block) / 2).rounded()
        let side = SearchLook.markSide
        StateMark.draw(mark, in: CGRect(x: pad, y: (top + (Self.hTitle - side) / 2).rounded(), width: side, height: side),
                       color: Theme.ns(Theme.token(mark.tone)), context: cg)
        let tx = pad + side + gap
        let opts: NSString.DrawingOptions = [.usesLineFragmentOrigin, .truncatesLastVisibleLine]
        let metaW = min(ceil(s.meta.size().width), max(0, (w - tx) / 2))
        // The meta on the title's baseline.
        let metaY = top + (Self.fTitle.ascender - Self.fMeta.ascender).rounded()
        s.meta.draw(with: NSRect(x: w - pad - metaW, y: metaY, width: metaW, height: Self.hMeta), options: opts)
        s.title.draw(with: NSRect(x: tx, y: top, width: max(0, w - pad - metaW - gap - tx), height: Self.hTitle), options: opts)
        if twoLines {
            s.snippet.draw(with: NSRect(x: tx, y: top + Self.hTitle + DS.Spacing.xxs, width: max(0, w - pad - tx), height: Self.hSnippet), options: opts)
        }
    }

    /// The row's height: two lines and `m` above and below.
    static var height: CGFloat { (hTitle + DS.Spacing.xxs + hSnippet + 2 * DS.Spacing.m).rounded(.up) }
}

/// The filter Pills in one line (left to right at their natural width).
@MainActor
final class FilterRow: NSView {
    override var isFlipped: Bool { true }
    private(set) var pills: [FilterPill] = []

    func show(_ menus: [HistoryFilterMenu], onChoose: @escaping (HistoryFilterMenu.Action) -> Void) {
        if pills.map(\.filter.kind) == menus.map(\.kind) {
            for (p, m) in zip(pills, menus) { p.filter = m }
        } else {
            for p in pills { p.removeFromSuperview() }
            pills = menus.map { FilterPill(menu: $0, onChoose: onChoose) }
            for p in pills { addSubview(p) }
        }
        needsLayout = true
    }

    override func layout() {
        super.layout()
        var x: CGFloat = 0
        // Whole Pills only: one that doesn't fit (and those after it) hides.
        var full = false
        for p in pills {
            let w = p.intrinsicContentSize.width
            full = full || x + w > bounds.width
            p.isHidden = full
            p.frame = NSRect(x: x, y: 0, width: w, height: Pill.height)
            x += w + DS.Spacing.xs
        }
    }
}

/// A filter Pill (selected when its filter is set); a click opens its
/// options as a menu (✓ on; the project list indents packages).
@MainActor
final class FilterPill: NSView {
    var filter: HistoryFilterMenu { didSet { if oldValue != filter { apply() } } }
    private let pill = PillView("", variant: .segment(selected: false))
    private let chevron = NSImageView()
    private let onChoose: (HistoryFilterMenu.Action) -> Void

    override var isFlipped: Bool { true }

    init(menu: HistoryFilterMenu, onChoose: @escaping (HistoryFilterMenu.Action) -> Void) {
        self.filter = menu
        self.onChoose = onChoose
        super.init(frame: .zero)
        addSubview(pill)
        chevron.image = NSImage(systemSymbolName: "chevron.down", accessibilityDescription: nil)
        chevron.symbolConfiguration = .init(pointSize: CGFloat(DS.TextStyle.meta.size) - DS.Spacing.xxs, weight: .medium)
        addSubview(chevron)
        setAccessibilityElement(true)
        setAccessibilityRole(.popUpButton)
        setAccessibilityIdentifier("history.filter.\(filter.kind.rawValue)")
        apply()
    }

    @available(*, unavailable)
    required init?(coder: NSCoder) { fatalError() }

    private func apply() {
        // Sentence case ("Any time", "Last 7 days"); names stay as they are.
        pill.title = filter.kind == .age ? filter.title.prefix(1).uppercased() + filter.title.dropFirst() : filter.title
        pill.variant = .segment(selected: filter.active)
        applyBorder()
        chevron.contentTintColor = Theme.ns(filter.active ? .text : .dim)
        toolTip = filter.label
        setAccessibilityLabel(filter.label)
        invalidateIntrinsicContentSize()
        needsLayout = true
    }

    private var chevronW: CGFloat { DS.Spacing.m }

    /// Off: a 1 px `line` outline (the Pill's own fill shows when on).
    private func applyBorder() {
        wantsLayer = true
        DS.Radius.apply(DS.Radius.control, to: layer)
        layer?.borderWidth = filter.active ? 0 : 1
        layer?.borderColor = Theme.ns(.line).cg(in: self)
    }

    override var intrinsicContentSize: NSSize {
        NSSize(width: pill.intrinsicContentSize.width + chevronW + DS.Spacing.xxs, height: Pill.height)
    }

    override func layout() {
        super.layout()
        pill.frame = bounds
        // The chevron sits inside the pill's right padding.
        chevron.frame = NSRect(x: bounds.width - DS.Spacing.m - chevronW / 2, y: (bounds.height - chevronW) / 2, width: chevronW, height: chevronW)
    }

    override func viewDidChangeEffectiveAppearance() {
        super.viewDidChangeEffectiveAppearance()
        chevron.contentTintColor = Theme.ns(filter.active ? .text : .dim)
        applyBorder()
    }

    override func mouseDown(with event: NSEvent) { popUp() }
    override func accessibilityPerformPress() -> Bool { popUp(); return true }

    func popUp() {
        let m = NSMenu()
        m.autoenablesItems = false
        let indent = filter.options.contains { $0.action == .header }
        for o in filter.options {
            if o.action == .header {
                m.addItem(NSMenuItem.sectionHeader(title: o.title))
                continue
            }
            let item = NSMenuItem(title: o.title, action: #selector(chose(_:)), keyEquivalent: "")
            item.target = self
            item.state = o.on ? .on : .off
            item.indentationLevel = o.depth + (indent && o.id != "all" && o.id != "this" ? 1 : 0)
            item.representedObject = o.id
            if let n = o.count, n > 0 {
                let t = NSMutableAttributedString(string: o.title, attributes: [.font: NSFont.ds(.body)])
                t.append(NSAttributedString(string: "  \(n)", attributes: [.font: NSFont.ds(.meta), .foregroundColor: NSColor.secondaryLabelColor]))
                item.attributedTitle = t
            }
            m.addItem(item)
        }
        m.popUp(positioning: nil, at: NSPoint(x: 0, y: bounds.height + DS.Spacing.xs), in: self)
    }

    @objc private func chose(_ item: NSMenuItem) {
        guard let id = item.representedObject as? String, let o = filter.options.first(where: { $0.id == id }) else { return }
        onChoose(o.action)
    }
}

/// While a modal sheet is open: the hover tracking of the SwiftUI views
/// underneath it is taken away (tracking areas fire through overlapping
/// views, so a composer chip's tooltip would float over the sheet) and
/// given back on close. Terminals and other AppKit views keep theirs.
@MainActor
final class HoverSuspension {
    private var taken: [(view: NSView, areas: [NSTrackingArea])] = []

    func suspend(under sheet: NSView) {
        resume()
        guard let parent = sheet.superview, let i = parent.subviews.firstIndex(of: sheet) else { return }
        let sheetRect = sheet.frame
        for v in parent.subviews[..<i] where v.frame.intersects(sheetRect) && !v.isHidden { collect(v) }
    }

    private func collect(_ v: NSView) {
        if Self.isHosting(v), !v.trackingAreas.isEmpty {
            let areas = v.trackingAreas
            for a in areas { v.removeTrackingArea(a) }
            taken.append((v, areas))
        }
        for c in v.subviews where !c.isHidden { collect(c) }
    }

    static func isHosting(_ v: NSView) -> Bool { String(describing: type(of: v)).contains("HostingView") }

    func resume() {
        for (v, areas) in taken where v.trackingAreas.isEmpty { // it may have rebuilt them meanwhile
            for a in areas { v.addTrackingArea(a) }
        }
        taken.removeAll()
    }
}
