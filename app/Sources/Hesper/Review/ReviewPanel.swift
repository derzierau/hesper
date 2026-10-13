import AppKit
import HesperCore
import SwiftUI

/// Which wall windows have a review sheet, and the window's key path into
/// it (MainWindow asks first while one is open), like HistoryPanelHost.
@MainActor
enum ReviewPanelHost {
    private final class Entry {
        weak var root: RootView?
        var panel: ReviewPanel?
        init(_ r: RootView) { root = r }
    }
    private static var entries: [ObjectIdentifier: Entry] = [:]

    /// RootView.init (marked integration point).
    static func attach(_ root: RootView) {
        entries = entries.filter { $0.value.root != nil }
        entries[ObjectIdentifier(root.model)] = Entry(root)
        _ = ReviewHub.shared(for: root.model)
    }

    static func panel(for model: AppModel) -> ReviewPanel? { entries[ObjectIdentifier(model)]?.panel }
    static func isOpen(_ model: AppModel) -> Bool { panel(for: model)?.isOpen == true }

    private static func entry(for window: NSWindow) -> Entry? {
        entries.values.first { $0.root?.window === window }
    }

    /// ⌘R, the pill, a tile's ⏎, the menu bar, a notification. Open on
    /// `select`, or close when open and nothing new is asked.
    static func toggle(_ model: AppModel, select: String?) {
        if let p = panel(for: model), p.isOpen {
            if let select, select != p.session.selectedID { p.session.select(select) } else { p.close() }
            return
        }
        show(model, select: select)
    }

    /// Open (or stay open) on `select`: a tile's Review, the menu bar, a
    /// notification.
    static func show(_ model: AppModel, select: String?) {
        if let p = panel(for: model), p.isOpen {
            if let select { p.session.select(select) }
            return
        }
        let hub = ReviewHub.shared(for: model)
        guard hub.available || !hub.items.isEmpty else {
            model.showToast(hub.support.local == false ? "Review needs a newer hesperd on this Mac" : "Nothing to review yet")
            return
        }
        guard let e = entries[ObjectIdentifier(model)], let root = e.root else { return }
        let p = e.panel ?? ReviewPanel(model: model, root: root)
        e.panel = p
        p.open(select: select)
    }

    static func handleKeyEquivalent(_ event: NSEvent, window: NSWindow) -> Bool {
        guard let p = entry(for: window)?.panel, p.isOpen else { return false }
        return p.handleKey(event, equivalent: true)
    }

    static func handleKey(_ event: NSEvent, window: NSWindow) -> Bool {
        guard event.type == .keyDown, let p = entry(for: window)?.panel, p.isOpen else { return false }
        return p.handleKey(event, equivalent: false)
    }

    /// The list changed: every wall's tiles re-read their footers.
    static func wallsChanged() {
        for e in entries.values { e.root?.model.onAgentsChanged?() }
    }
}

/// Review (⌘R): a modal sheet over the wall like History (a Night scrim,
/// one opaque Panel in the SheetChrome frame: the title row with the
/// count and the close × esc, the toolbar, the footer). Left the inbox
/// (every Mac, ranked), middle the selected item's header and its changes
/// as one stream, right the evidence, risk and attention; narrow, the
/// evidence goes under the inbox. Keys: ReviewKeys.
@MainActor
final class ReviewPanel: NSView, NSTableViewDataSource, NSTableViewDelegate {
    let session: ReviewSession
    private weak var model: AppModel?
    private weak var root: RootView?
    private let machineNames: () -> [String: String]
    private(set) var isOpen = false

    private let panel = PanelView()
    let chrome = SheetChromeView(title: "Review", symbol: "checklist")
    private let box = FlippedView()
    private let meta: NSHostingView<ReviewSheetMeta>
    // The toolbar's verbs (KeyButtons; the caps come from the registry).
    private(set) var toolbarButtons: [CommandID: KeyButtonView] = [:]
    private let itemHeader: NSHostingView<ReviewItemHeader>
    private let evidence: NSHostingView<ReviewEvidencePanel>
    /// The footer: Fold ⏎, Next hunk J, Previous hunk K.
    private(set) var footerButtons: [KeyButtonView] = []
    let inbox = ReviewTable()
    private let inboxScroll = NSScrollView()
    let stream: ReviewStreamView
    private let emptyLabel = NSTextField(labelWithString: "")
    private let dividers = [NSView(), NSView(), NSView()]
    // The note editor under the stream: the anchor, Save ⌘⏎ and Cancel
    // esc, the field.
    private let noteBox = FlippedView()
    private let noteLabel = NSTextField(labelWithString: "")
    let noteField = NSTextField()
    private(set) var noteButtons: [KeyButtonView] = []
    private var shownItems: [ReviewItem] = []

    override var isFlipped: Bool { true }
    override var acceptsFirstResponder: Bool { true }

    convenience init(model: AppModel, root: RootView) {
        let hub = ReviewHub.shared(for: model)
        self.init(session: ReviewSession(hub: hub, client: model.client), machineNames: { [weak model] in
            model.map { MachineLabel.names($0.machines) } ?? [:]
        })
        self.model = model
        self.root = root
        session.onToast = { [weak model] text, error in model?.showToast(text, error: error) }
        session.summary = { [weak model] id in model?.agent(id)?.summary }
        session.openHistory = { [weak self] p, item in self?.openHistory(p, item) }
    }

    /// `session` without a model: offscreen renders.
    init(session: ReviewSession, machineNames: @escaping () -> [String: String]) {
        self.session = session
        self.machineNames = machineNames
        meta = NSHostingView(rootView: ReviewSheetMeta(session: session, machineNames: [:]))
        itemHeader = NSHostingView(rootView: ReviewItemHeader(session: session, machineNames: [:]))
        evidence = NSHostingView(rootView: ReviewEvidencePanel(session: session))
        stream = ReviewStreamView(session: session)
        super.init(frame: .zero)
        wantsLayer = true
        panel.isSheet = true
        isHidden = true
        setAccessibilityElement(true)
        setAccessibilityIdentifier("review")
        setAccessibilityRole(.group)
        setAccessibilityLabel("Review")
        addSubview(panel)
        chrome.onClose = { [weak self] in self?.close() }
        chrome.closeButton.setAccessibilityIdentifier("review.close")
        chrome.frame = panel.contentView.bounds
        chrome.autoresizingMask = [.width, .height]
        panel.contentView.addSubview(chrome)
        box.frame = chrome.contentView.bounds
        box.autoresizingMask = [.width, .height]
        chrome.contentView.addSubview(box)
        // ‹ › file, Bulk accept, Provenance, the meta; then Send back and
        // the primary Accept and commit on the right.
        let s = session
        let verbs: [(CommandID, String, KeyButton.Variant, NSStackView.Gravity, () -> Void)] = [
            (.reviewPreviousFile, "Previous file", .icon("chevron.left"), .leading, { s.nextFile(back: true) }),
            (.reviewNextFile, "Next file", .icon("chevron.right"), .leading, { s.nextFile() }),
            (.reviewBulkAccept, "Bulk accept", .quiet, .leading, { s.bulkAccept() }),
            (.reviewProvenance, "Provenance", .quiet, .leading, { s.openProvenance() }),
            (.reviewSendBack, "Send back", .quiet, .trailing, { s.sendBack() }),
            (.reviewAccept, "Accept and commit", .primary(.working), .trailing, { s.accept() }),
        ]
        for (id, title, variant, gravity, run) in verbs {
            let b = KeyButtonView(title, kbd: Self.cap(id), variant: variant, onPress: run)
            b.setAccessibilityIdentifier("review.\(id.key)")
            chrome.toolbar.addView(b, in: gravity)
            toolbarButtons[id] = b
        }
        meta.setContentCompressionResistancePriority(.defaultLow, for: .horizontal)
        chrome.toolbar.addView(meta, in: .leading)

        for v in [itemHeader, evidence] as [NSView] { box.addSubview(v) }
        let rowVerbs: [(CommandID, String, () -> Void)] = [
            (.reviewToggleFold, "Fold", { s.toggleFold() }), (.reviewNextHunk, "Next hunk", { s.nextHunk() }),
            (.reviewPreviousHunk, "Previous hunk", { s.previousHunk() }),
        ]
        for (id, title, run) in rowVerbs {
            let b = KeyButtonView(title, kbd: Self.cap(id), onPress: run)
            b.setAccessibilityIdentifier("review.footer.\(id.key)")
            chrome.footer.addArrangedSubview(b)
            footerButtons.append(b)
        }
        for d in dividers { d.wantsLayer = true; box.addSubview(d) }

        let col = NSTableColumn(identifier: .init("i"))
        inbox.addTableColumn(col)
        inbox.headerView = nil
        inbox.rowHeight = ReviewLook.inboxRowHeight
        inbox.usesAutomaticRowHeights = false
        inbox.intercellSpacing = .zero
        inbox.backgroundColor = .clear
        inbox.selectionHighlightStyle = .none
        inbox.style = .plain
        inbox.dataSource = self
        inbox.delegate = self
        inbox.target = self
        inbox.action = #selector(inboxClicked)
        inbox.setAccessibilityIdentifier("review.inbox")
        inbox.setAccessibilityLabel("Ready to review")
        inboxScroll.documentView = inbox
        inboxScroll.drawsBackground = false
        inboxScroll.hasVerticalScroller = true
        inboxScroll.autohidesScrollers = true
        inboxScroll.scrollerStyle = .overlay
        box.addSubview(inboxScroll)
        box.addSubview(stream)

        emptyLabel.font = .ds(.body)
        emptyLabel.textColor = Theme.ns(.dim)
        emptyLabel.alignment = .center
        emptyLabel.maximumNumberOfLines = 2
        box.addSubview(emptyLabel)

        noteBox.wantsLayer = true
        noteBox.isHidden = true
        noteLabel.font = .ds(.meta, .medium)
        noteLabel.textColor = Theme.ns(.question)
        noteField.font = .ds(.body)
        noteField.textColor = Theme.ns(.text)
        noteField.placeholderString = "What should the agent change here?"
        noteField.focusRingType = .none
        noteField.isBordered = false
        noteField.drawsBackground = false
        noteField.usesSingleLineMode = false
        noteField.cell?.wraps = true
        noteField.cell?.isScrollable = false
        noteField.setAccessibilityIdentifier("review.note")
        noteBox.addSubview(noteLabel)
        noteBox.addSubview(noteField)
        let save = KeyButtonView("Save", kbd: KeyChord(.enter, command: true).label, variant: .primary(.question)) { [weak self] in
            guard let self else { return }
            self.session.saveNote(self.noteField.stringValue)
        }
        save.setAccessibilityIdentifier("review.note.save")
        let cancel = KeyButtonView("Cancel", kbd: SheetChromeView.closeKey) { [weak self] in self?.session.cancelNote() }
        cancel.setAccessibilityIdentifier("review.note.cancel")
        noteButtons = [save, cancel]
        for b in noteButtons { noteBox.addSubview(b) }
        box.addSubview(noteBox)

        session.onStream = { [weak self] in self?.streamChanged() }
        session.onFocus = { [weak self] old, new in self?.stream.focusChanged(old: old, new: new) }
        observeChanges { [weak self] in self?.sessionChanged() }
        applyTheme()
    }

    @available(*, unavailable)
    required init?(coder: NSCoder) { fatalError() }

    /// A verb's key cap in the sheet, as the registry spells it ("⇧⏎").
    static func cap(_ id: CommandID) -> String { CommandRegistry.labels(id, in: .review).first ?? "" }

    override func viewDidChangeEffectiveAppearance() {
        super.viewDidChangeEffectiveAppearance()
        applyTheme()
    }

    private func applyTheme() {
        for d in dividers { d.layer?.backgroundColor = Theme.ns(.line).cg(in: self) }
        noteBox.layer?.backgroundColor = Theme.ns(.question, alpha: ReviewLook.noteAlpha).cg(in: self)
        noteBox.layer?.borderColor = Theme.ns(.question, alpha: AttentionBar.primaryStroke).cg(in: self)
        noteBox.layer?.borderWidth = 1
        DS.Radius.apply(DS.Radius.control, to: noteBox.layer)
        layer?.backgroundColor = SearchLook.scrimColor(reduceTransparency: DS.reduceTransparency).cgColor
    }

    // MARK: Open / close

    func open(select: String?) {
        guard let root, let model else { return }
        model.closePopover()
        model.showPalette = false
        if superview == nil { root.addReviewPanel(self) }
        isOpen = true
        isHidden = false
        frame = root.reviewFrame
        applyTheme()
        hoverBlock.suspend(under: self)
        refreshNames()
        reloadInbox()
        session.opened(select: select)
        needsLayout = true
        if let a = DS.caAnimation(.quick, keyPath: "transform") {
            alphaValue = 0
            DS.animate(.quick) { self.animator().alphaValue = 1 }
            a.fromValue = CATransform3DMakeScale(SearchLook.openScale, SearchLook.openScale, 1)
            a.toValue = CATransform3DIdentity
            panel.layer?.add(a, forKey: "open")
        }
        window?.makeFirstResponder(self)
        session.hub.scheduleRefresh(after: 0)
    }

    func close() {
        guard isOpen else { return }
        isOpen = false
        session.cancelNote()
        hoverBlock.resume()
        DS.animate(.quick, { self.animator().alphaValue = 0 }, completion: { [weak self] in
            guard let self, !self.isOpen else { return }
            self.isHidden = true
            self.alphaValue = 1
        })
        root?.reviewClosed()
    }

    /// Offscreen renders: shown at once, no window.
    func showForRender() {
        isOpen = true
        isHidden = false
        refreshNames()
        reloadInbox()
        streamChanged()
        sessionChanged()
    }

    private func refreshNames() {
        let names = machineNames()
        meta.rootView = ReviewSheetMeta(session: session, machineNames: names)
        itemHeader.rootView = ReviewItemHeader(session: session, machineNames: names)
    }

    // MARK: The scrim takes every mouse event over the wall

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
        if !panel.frame.contains(convert(event.locationInWindow, from: nil)) { close() }
    }

    // MARK: Layout

    var isWide: Bool { panel.frame.width >= ReviewLook.wideMin }

    override func layout() {
        super.layout()
        panel.frame = ReviewLook.frame(in: bounds.size)
        panel.layoutSubtreeIfNeeded()
        chrome.frame = panel.contentView.bounds
        chrome.layoutSubtreeIfNeeded()
        box.frame = chrome.contentView.bounds
        layoutBox()
    }

    private func layoutBox() {
        let w = box.bounds.width, h = box.bounds.height
        let top: CGFloat = 0, bottom = h, paneH = max(0, bottom - top)
        let wide = w >= ReviewLook.wideMin
        let leftW = wide ? ReviewLook.inboxWidth : ReviewLook.narrowInboxWidth
        let rightW = wide ? ReviewLook.evidenceWidth : 0
        dividers[0].frame = NSRect(x: leftW, y: top, width: 1, height: paneH)
        dividers[1].frame = NSRect(x: w - rightW - 1, y: top, width: wide ? 1 : 0, height: paneH)
        if wide {
            inboxScroll.frame = NSRect(x: 0, y: top + DS.Spacing.s, width: leftW, height: max(0, paneH - DS.Spacing.s))
            evidence.frame = NSRect(x: w - rightW, y: top, width: rightW, height: paneH)
            dividers[2].frame = .zero
        } else {
            let inboxH = (paneH * ReviewLook.narrowInboxFraction).rounded()
            inboxScroll.frame = NSRect(x: 0, y: top + DS.Spacing.s, width: leftW, height: max(0, inboxH - DS.Spacing.s))
            dividers[2].frame = NSRect(x: 0, y: top + inboxH, width: leftW, height: 1)
            evidence.frame = NSRect(x: 0, y: top + inboxH + 1, width: leftW, height: max(0, paneH - inboxH - 1))
        }
        inbox.tableColumns.first?.width = inboxScroll.contentSize.width
        let midX = leftW + 1, midW = max(0, w - rightW - (wide ? 1 : 0) - midX)
        let ih = max(ReviewLook.itemHeaderHeight, itemHeader.fittingSize.height)
        itemHeader.frame = NSRect(x: midX, y: top, width: midW, height: ih)
        let editing = session.editingNote != nil
        let noteH = editing ? ReviewLook.noteEditorHeight : 0
        stream.frame = NSRect(x: midX, y: top + ih, width: midW, height: max(0, bottom - top - ih - noteH))
        noteBox.isHidden = !editing
        noteBox.frame = NSRect(x: midX + DS.Spacing.m, y: bottom - noteH + DS.Spacing.s, width: max(0, midW - 2 * DS.Spacing.m), height: max(0, noteH - 2 * DS.Spacing.s))
        // The first row: the anchor on the left, Save and Cancel on the right.
        let lh = KeyButton.height
        var bx = noteBox.bounds.width - DS.Spacing.m
        for b in noteButtons.reversed() {
            let s = b.intrinsicContentSize
            bx -= s.width
            b.frame = NSRect(x: bx, y: DS.Spacing.s, width: s.width, height: lh)
            bx -= DS.Spacing.xs
        }
        let labelH = ceil(noteLabel.intrinsicContentSize.height)
        noteLabel.frame = NSRect(x: DS.Spacing.m, y: DS.Spacing.s + ((lh - labelH) / 2).rounded(), width: max(0, bx - DS.Spacing.s - DS.Spacing.m), height: labelH)
        noteField.frame = NSRect(x: DS.Spacing.m, y: DS.Spacing.s + lh + DS.Spacing.xs, width: max(0, noteBox.bounds.width - 2 * DS.Spacing.m),
                                 height: max(0, noteBox.bounds.height - lh - 2 * DS.Spacing.s - DS.Spacing.xs))
        let eh = emptyLabel.intrinsicContentSize.height * 2
        emptyLabel.frame = NSRect(x: midX + DS.Spacing.xl, y: stream.frame.minY + DS.Spacing.xxl, width: max(0, midW - 2 * DS.Spacing.xl), height: eh)
    }

    // MARK: Updates

    private func sessionChanged() {
        // Read what the AppKit parts show (observation re-runs this).
        let items = session.items
        let selected = session.selectedID
        let editing = session.editingNote
        _ = session.loading
        guard isOpen else { return } // closed: nothing to show (and no diffs to fetch)
        let count = items.isEmpty ? nil : items.count
        if chrome.count != count { chrome.count = count }
        updateToolbar()
        if items != shownItems { reloadInbox() } else { syncInboxSelection(selected) }
        if editing != shownEditing { editingChanged(editing) }
        updateEmpty()
        needsLayout = true
    }

    private var shownEditing: ReviewNoteAnchor?

    /// The verbs follow the selection: nothing selected disables them all
    /// but Bulk accept (which needs items); Send back counts the notes.
    private func updateToolbar() {
        let hasItem = session.selected != nil
        for (id, b) in toolbarButtons {
            let on = id == .reviewBulkAccept ? !session.items.isEmpty : hasItem
            if b.isEnabled != on { b.isEnabled = on }
        }
        let hasHunks = !(session.stream?.hunks.isEmpty ?? true)
        for b in footerButtons where b.isEnabled != hasHunks { b.isEnabled = hasHunks }
        let n = session.currentNotes.count
        let sendBack = n == 0 ? "Send back" : "Send back · \(n)"
        if let b = toolbarButtons[.reviewSendBack], b.label != sendBack { b.label = sendBack }
    }

    private func editingChanged(_ a: ReviewNoteAnchor?) {
        shownEditing = a
        if let a {
            noteLabel.stringValue = "Note on \(ReviewNotes.where_(a))"
            noteField.stringValue = session.note(a) ?? ""
            layoutBox()
            window?.makeFirstResponder(noteField)
        } else if window?.firstResponder is NSText {
            window?.makeFirstResponder(self)
        }
    }

    private func streamChanged() {
        stream.reload()
        if let f = session.focused { stream.focusChanged(old: nil, new: f) }
        updateEmpty()
        updateToolbar()
    }

    private func updateEmpty() {
        var text = ""
        if session.items.isEmpty {
            text = "Nothing to review. Finished agents with changes show up here, from every Mac."
        } else if let s = session.stream, s.isEmpty {
            text = "No changes left in this agent's folder."
        }
        emptyLabel.stringValue = text
        emptyLabel.isHidden = text.isEmpty
    }

    // MARK: Inbox

    private func reloadInbox() {
        let before = shownItems.firstIndex { $0.id == session.selectedID }
        shownItems = session.items
        inbox.reloadData()
        session.itemsChanged(previousIndex: before)
        syncInboxSelection(session.selectedID)
    }

    private func syncInboxSelection(_ id: String?) {
        inbox.enumerateAvailableRowViews { rv, row in
            (rv.view(atColumn: 0) as? ReviewInboxCell)?.selected = shownItems.indices.contains(row) && shownItems[row].id == id
        }
        if let i = shownItems.firstIndex(where: { $0.id == id }) { inbox.scrollRowToVisible(i) }
    }

    func numberOfRows(in tableView: NSTableView) -> Int { shownItems.count }

    func tableView(_ tableView: NSTableView, viewFor tableColumn: NSTableColumn?, row: Int) -> NSView? {
        let v = tableView.makeView(withIdentifier: ReviewInboxCell.id, owner: nil) as? ReviewInboxCell ?? ReviewInboxCell()
        v.identifier = ReviewInboxCell.id
        let item = shownItems[row]
        v.show(item, machine: machineNames()[item.machine] ?? item.machine, selected: item.id == session.selectedID)
        return v
    }

    func tableView(_ tableView: NSTableView, rowViewForRow row: Int) -> NSTableRowView? { ReviewTableRowView() }

    @objc private func inboxClicked() {
        let r = inbox.clickedRow
        guard shownItems.indices.contains(r) else { return }
        session.select(shownItems[r].id)
    }

    // MARK: Keys

    func handleKey(_ event: NSEvent, equivalent: Bool) -> Bool {
        let chord = KeyChord(event: event)
        let action = ReviewKeys.route(chord, editingNote: session.editingNote != nil)
        if equivalent {
            guard chord.command else { return false }
        } else if chord.command {
            return false // performKeyEquivalent has ⌘ chords
        }
        if action == .pass {
            // Another sheet's or overlay's ⌘ key (⌘K, ⌘Y, ⌘J): this one steps aside.
            if equivalent, session.editingNote == nil, ReviewKeys.passesThrough(chord) { close() }
            return false
        }
        perform(action)
        return true
    }

    func perform(_ a: ReviewKeyAction) {
        switch a {
        case .previousItem: session.step(-1)
        case .nextItem: session.step(1)
        case .nextHunk: session.nextHunk()
        case .previousHunk: session.previousHunk()
        case .nextFile: session.nextFile()
        case .previousFile: session.nextFile(back: true)
        case .seen: session.toggleSeen()
        case .reject: session.reject()
        case .note: session.startNote()
        case .toggleFold: session.toggleFold()
        case .sendBack: session.sendBack()
        case .accept: session.accept()
        case .provenance: session.openProvenance()
        case .bulkAccept: session.bulkAccept()
        case .saveNote: session.saveNote(noteField.stringValue)
        case .cancelNote: session.cancelNote()
        case .close: if !session.cancelConfirm() { close() }
        case .pass: break
        }
    }

    // MARK: Provenance → History

    private func openHistory(_ p: ReviewProvenance, _ item: ReviewItem) {
        guard let model else { return }
        close()
        guard let sid = p.sessionId else {
            // No recorded edit: History searching for the agent.
            HistoryPanelHost.open(model, text: item.name, select: nil)
            return
        }
        let client = model.client
        Task { [weak model] in
            let detail = await Task.detached { try? await client.showSession(sid) }.value
            guard let model else { return }
            if let s = detail?.session {
                HistoryPanelHost.open(model, text: nil, select: s)
            } else {
                HistoryPanelHost.open(model, text: p.prompt.map { SessionFormat.oneLine($0, max: 60) } ?? item.name, select: nil)
            }
        }
    }
}
