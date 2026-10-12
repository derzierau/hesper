import AppKit
import HesperCore
import Observation
import SwiftUI

/// Which wall windows have a Routines panel, and the window's key path
/// into it (MainWindow asks first while one is open), like ReviewPanelHost.
@MainActor
enum RoutinesPanelHost {
    private final class Entry {
        weak var root: RootView?
        var panel: RoutinesPanel?
        init(_ r: RootView) { root = r }
    }
    private static var entries: [ObjectIdentifier: Entry] = [:]

    /// RootView.init (marked integration point).
    static func attach(_ root: RootView) {
        entries = entries.filter { $0.value.root != nil }
        entries[ObjectIdentifier(root.model)] = Entry(root)
        _ = RoutinesHub.shared(for: root.model)
    }

    static func panel(for model: AppModel) -> RoutinesPanel? { entries[ObjectIdentifier(model)]?.panel }
    static func isOpen(_ model: AppModel) -> Bool { panel(for: model)?.isOpen == true }

    private static func entry(for window: NSWindow) -> Entry? {
        entries.values.first { $0.root?.window === window }
    }

    /// ⌘⇧R, the menu: open on `select`, or close when open and nothing new
    /// is asked.
    static func toggle(_ model: AppModel, select: String?) {
        if let p = panel(for: model), p.isOpen {
            if let select, select != p.session.selectedID { p.session.select(select) } else { p.close() }
            return
        }
        show(model, select: select)
    }

    /// Open (or stay open) on `select`: ⌘K, the sidebar, hesperctl.
    static func show(_ model: AppModel, select: String?) {
        if let p = panel(for: model), p.isOpen {
            if let select { p.session.select(select) }
            return
        }
        let hub = RoutinesHub.shared(for: model)
        guard hub.available else {
            if hub.support.local == false { model.showToast("Routines need a newer hesperd on this Mac") } else { NSSound.beep() }
            return
        }
        guard let e = entries[ObjectIdentifier(model)], let root = e.root else { return }
        let p = e.panel ?? RoutinesPanel(model: model, root: root)
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
}

/// The panel's measures (spacing, type, radii and colors come from DS /
/// Theme).
enum RoutineLook {
    /// The sheet: History's size (min(1100, 86%) × min(760, 80%)), a
    /// little above the middle.
    static let listWidth: CGFloat = 320
    static let headerHeight: CGFloat = 48
    static let rowHeight: CGFloat = 56
    /// The task shown in the detail: its first lines.
    static let taskLines = 4

    static func frame(in size: CGSize) -> CGRect {
        let w = min(SearchLook.historyMaxWidth, (size.width * SearchLook.historyWidthFraction).rounded())
        let h = min(SearchLook.historyMaxHeight, (size.height * SearchLook.historyHeightFraction).rounded())
        let y = ((size.height - h) * SearchLook.historyAboveFraction).rounded()
        return CGRect(x: ((size.width - w) / 2).rounded(), y: max(0, y), width: max(0, w), height: max(0, h))
    }

    /// The chip's tone as a token (never Signal but for "needs you").
    static func token(_ tone: RoutineTone) -> Theme.Token {
        switch tone {
        case .good: return .done
        case .attention: return .signal
        case .critical: return .error
        case .neutral: return .dim
        }
    }

    /// The state mark next to a run's outcome: a running run works, one
    /// that needs someone pulses, the rest by tone.
    static func mark(_ run: RoutineRun?) -> StateMarkKind {
        guard let run else { return .idle }
        if run.isRunning { return .working }
        switch RoutineText.tone(run.outcome) {
        case .good: return .done
        case .attention: return .needsYou
        case .critical: return .error
        case .neutral: return run.outcome == RunOutcome.exited ? .exited : .idle
        }
    }
}

/// The panel's state and what its keys do: the selected routine, the
/// remove confirmation, the inline note, and the calls (run now, pause,
/// remove, open a run). The panel's views read it.
@MainActor
@Observable
final class RoutinesSession {
    @ObservationIgnored let hub: RoutinesHub
    private(set) var selectedID: String?
    /// ⌫ once: this routine goes on the second ⌫.
    private(set) var confirmRemove: String?
    /// The detail's status line (an `exists`, an error, a hint).
    private(set) var note: (text: String, error: Bool)?
    private(set) var busy: String?

    /// A run's agent to show (running or needing someone).
    @ObservationIgnored var openAgent: ((String) -> Void)?
    /// An ended run's session in History.
    @ObservationIgnored var openSession: ((String) -> Void)?
    @ObservationIgnored var onToast: ((String, Bool) -> Void)?

    init(hub: RoutinesHub) { self.hub = hub }

    var routines: [Routine] { hub.routines }
    var selected: Routine? { hub.routine(selectedID) }
    var runs: [RoutineRun] { selectedID.flatMap { hub.runs[$0] } ?? [] }
    var loadingRuns: Bool { selectedID.map { hub.loadingRuns.contains($0) && hub.runs[$0] == nil } ?? false }

    // MARK: Selection

    /// The panel opened: the asked routine, else the one shown last, else the first.
    func opened(select id: String?) {
        let want = id.flatMap { i in routines.first { $0.id == i }?.id } ?? selectedID.flatMap { i in routines.first { $0.id == i }?.id } ?? routines.first?.id
        select(want, force: true)
    }

    func select(_ id: String?, force: Bool = false) {
        guard force || id != selectedID else { return }
        selectedID = id
        confirmRemove = nil
        note = nil
        hub.watch(id)
    }

    /// The list changed under the panel: keep the selection when it is
    /// still there, else the routine now in its place.
    func routinesChanged(previousIndex: Int?) {
        guard let id = selectedID else {
            if let first = routines.first { select(first.id) }
            return
        }
        guard hub.routine(id) == nil else { return }
        let list = routines
        select(list.isEmpty ? nil : list[min(previousIndex ?? 0, list.count - 1)].id)
    }

    func step(_ d: Int) {
        let list = routines
        guard !list.isEmpty else { return }
        let i = selectedID.flatMap { id in list.firstIndex { $0.id == id } } ?? (d > 0 ? -1 : list.count)
        select(list[max(0, min(list.count - 1, i + d))].id)
    }

    // MARK: Actions

    /// ⏎: routines.run. `exists` (a run is live) is a note, not an error.
    func runNow() {
        guard let r = selected, busy == nil else { return }
        confirmRemove = nil
        busy = "Starting \(r.name)…"
        Task {
            let result = await hub.runNow(r.id)
            busy = nil
            switch result {
            case .success(let run):
                note = (run.map { "Run \($0.seq) started" } ?? "Run started", false)
                hub.refreshRuns(r.id)
            case .failure(let e):
                if (e as? RPCError)?.dataCode == "exists" {
                    note = ("A run is already live: \(Self.describe(e))", false)
                } else {
                    note = ("Couldn't start a run: \(Self.describe(e))", true)
                }
            }
        }
    }

    /// P: routines.pause (paused ↔ resumed).
    func togglePause() {
        guard let r = selected, busy == nil else { return }
        confirmRemove = nil
        let paused = !r.paused
        busy = paused ? "Pausing \(r.name)…" : "Resuming \(r.name)…"
        Task {
            let result = await hub.pause(r.id, paused: paused)
            busy = nil
            switch result {
            case .success: note = (paused ? "Paused: the clock skips it; ⏎ still runs it" : "Resumed", false)
            case .failure(let e): note = ("Couldn't \(paused ? "pause" : "resume"): \(Self.describe(e))", true)
            }
        }
    }

    /// O (or a run row): a live run's agent, else its session in History.
    func open(_ run: RoutineRun? = nil) {
        guard let r = selected else { return }
        guard let run = run ?? r.lastRun else { note = ("No run to open yet: ⏎ starts one", false); return }
        if run.isLive, let a = run.agent { openAgent?(a); return }
        if let s = run.session { openSession?(s); return }
        if let a = run.agent, run.isRunning == false, run.ended == nil { openAgent?(a); return }
        note = (run.outcome == RunOutcome.skipped || run.outcome == RunOutcome.failed ? "Run \(run.seq) never started" : "Run \(run.seq) left no session to open", false)
    }

    /// ⌫: the first asks, the second removes (its runs too).
    func remove() {
        guard let r = selected, busy == nil else { return }
        if confirmRemove != r.id {
            confirmRemove = r.id
            return
        }
        confirmRemove = nil
        let index = routines.firstIndex { $0.id == r.id }
        busy = "Removing \(r.name)…"
        Task {
            let result = await hub.remove(r.id)
            busy = nil
            switch result {
            case .success:
                onToast?("Removed \(r.name)", false)
                if selectedID == r.id {
                    selectedID = nil
                    routinesChanged(previousIndex: index)
                }
            case .failure(let e):
                note = ("Couldn't remove: \(Self.describe(e))", true)
            }
        }
    }

    /// Esc with a confirm pending cancels it; true when it did.
    func cancelConfirm() -> Bool {
        guard confirmRemove != nil else { return false }
        confirmRemove = nil
        return true
    }

    static func describe(_ e: any Error) -> String {
        if let r = e as? RPCError { return r.message }
        return "\(e)"
    }
}

/// Routines (⌘⇧R): a modal sheet over the wall like Review and History (a
/// Night scrim, one opaque Panel). Left the routines (ranked: needs you,
/// running, then by next run) with name, Mac, cadence, next run and the
/// last run's chip; right the selected routine: task, project, Mac,
/// policy, next run, its runs newest first, and Run now / Pause / Open /
/// Remove. Keys: RoutineKeys.
@MainActor
final class RoutinesPanel: NSView, NSTableViewDataSource, NSTableViewDelegate {
    let session: RoutinesSession
    private weak var model: AppModel?
    private weak var root: RootView?
    private let machineNames: () -> [String: String]
    private let agentName: (String) -> String?
    private(set) var isOpen = false

    private let panel = PanelView()
    private let box = FlippedView()
    private let header: NSHostingView<RoutinesSheetHeader>
    private let detail: NSHostingView<RoutineDetailView>
    private let hints = NSHostingView(rootView: Hints(items: []))
    let table = RoutineTable()
    private let scroll = NSScrollView()
    private let emptyLabel = NSTextField(labelWithString: "")
    private let divider = NSView()
    private(set) var shown: [Routine] = []
    /// The rows were reloaded (tests).
    var onRowsShown: (() -> Void)?

    override var isFlipped: Bool { true }
    override var acceptsFirstResponder: Bool { true }

    init(model: AppModel, root: RootView) {
        let hub = RoutinesHub.shared(for: model)
        session = RoutinesSession(hub: hub)
        self.model = model
        self.root = root
        machineNames = { [weak model] in model.map { MachineLabel.names($0.machines) } ?? [:] }
        agentName = { [weak model] id in model?.agent(id)?.name }
        header = NSHostingView(rootView: RoutinesSheetHeader(session: session))
        detail = NSHostingView(rootView: RoutineDetailView(session: session, machineNames: [:], agentName: { _ in nil }))
        super.init(frame: .zero)
        wantsLayer = true
        panel.isSheet = true
        isHidden = true
        setAccessibilityElement(true)
        setAccessibilityIdentifier("routines")
        setAccessibilityRole(.group)
        setAccessibilityLabel("Routines")
        addSubview(panel)
        box.frame = panel.contentView.bounds
        box.autoresizingMask = [.width, .height]
        panel.contentView.addSubview(box)
        for v in [header, detail, hints] as [NSView] { box.addSubview(v) }
        divider.wantsLayer = true
        box.addSubview(divider)

        let col = NSTableColumn(identifier: .init("r"))
        table.addTableColumn(col)
        table.headerView = nil
        table.rowHeight = RoutineLook.rowHeight
        table.usesAutomaticRowHeights = false
        table.intercellSpacing = .zero
        table.backgroundColor = .clear
        table.selectionHighlightStyle = .none
        table.style = .plain
        table.dataSource = self
        table.delegate = self
        table.target = self
        table.action = #selector(rowClicked)
        table.setAccessibilityIdentifier("routines.list")
        table.setAccessibilityLabel("Routines")
        scroll.documentView = table
        scroll.drawsBackground = false
        scroll.hasVerticalScroller = true
        scroll.autohidesScrollers = true
        scroll.scrollerStyle = .overlay
        box.addSubview(scroll)

        emptyLabel.font = .ds(.body)
        emptyLabel.textColor = Theme.ns(.dim)
        emptyLabel.alignment = .center
        emptyLabel.maximumNumberOfLines = 3
        emptyLabel.setAccessibilityIdentifier("routines.empty")
        box.addSubview(emptyLabel)

        session.onToast = { [weak model] text, error in model?.showToast(text, error: error) }
        session.openAgent = { [weak self] id in self?.openAgent(id) }
        session.openSession = { [weak self] id in self?.openSession(id) }
        observeChanges { [weak self] in self?.sessionChanged() }
        updateHints()
        applyTheme()
    }

    @available(*, unavailable)
    required init?(coder: NSCoder) { fatalError() }

    override func viewDidChangeEffectiveAppearance() {
        super.viewDidChangeEffectiveAppearance()
        applyTheme()
    }

    private func applyTheme() {
        divider.layer?.backgroundColor = Theme.ns(.line).cg(in: self)
        layer?.backgroundColor = SearchLook.scrimColor(reduceTransparency: DS.reduceTransparency).cgColor
    }

    // MARK: Open / close

    func open(select: String?) {
        guard let root, let model else { return }
        model.closePopover()
        model.showPalette = false
        if superview == nil { root.addRoutinesPanel(self) }
        isOpen = true
        isHidden = false
        frame = root.routinesFrame
        applyTheme()
        hoverBlock.suspend(under: self)
        refreshNames()
        reload()
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
        _ = session.cancelConfirm()
        hoverBlock.resume()
        DS.animate(.quick, { self.animator().alphaValue = 0 }, completion: { [weak self] in
            guard let self, !self.isOpen else { return }
            self.isHidden = true
            self.alphaValue = 1
        })
        root?.routinesClosed()
    }

    private func refreshNames() {
        detail.rootView = RoutineDetailView(session: session, machineNames: machineNames(), agentName: agentName)
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

    override func layout() {
        super.layout()
        panel.frame = RoutineLook.frame(in: bounds.size)
        panel.layoutSubtreeIfNeeded()
        box.frame = panel.contentView.bounds
        let w = box.bounds.width, h = box.bounds.height
        let hh = RoutineLook.headerHeight, fh = SearchLook.footerHeight
        header.frame = NSRect(x: 0, y: 0, width: w, height: hh)
        hints.frame = NSRect(x: 0, y: h - fh, width: w, height: fh)
        let top = hh, paneH = max(0, h - fh - top)
        let leftW = min(RoutineLook.listWidth, (w * 0.4).rounded())
        scroll.frame = NSRect(x: 0, y: top + DS.Spacing.s, width: leftW, height: max(0, paneH - DS.Spacing.s))
        table.tableColumns.first?.width = scroll.contentSize.width
        divider.frame = NSRect(x: leftW, y: top, width: 1, height: paneH)
        detail.frame = NSRect(x: leftW + 1, y: top, width: max(0, w - leftW - 1), height: paneH)
        let eh = emptyLabel.intrinsicContentSize.height * 3
        emptyLabel.frame = NSRect(x: DS.Spacing.xl, y: top + DS.Spacing.xxl, width: max(0, w - 2 * DS.Spacing.xl), height: eh)
    }

    // MARK: Updates

    private func sessionChanged() {
        let list = session.routines
        let selected = session.selectedID
        _ = (session.confirmRemove, session.note, session.busy)
        guard isOpen else { return }
        if list != shown { reload() } else { syncSelection(selected) }
        updateEmpty()
        updateHints()
        needsLayout = true
    }

    private func reload() {
        let before = shown.firstIndex { $0.id == session.selectedID }
        shown = session.routines
        table.reloadData()
        session.routinesChanged(previousIndex: before)
        syncSelection(session.selectedID)
        updateEmpty()
        onRowsShown?()
    }

    private func syncSelection(_ id: String?) {
        table.enumerateAvailableRowViews { rv, row in
            (rv.view(atColumn: 0) as? RoutineRowCell)?.selected = shown.indices.contains(row) && shown[row].id == id
        }
        if let i = shown.firstIndex(where: { $0.id == id }) { table.scrollRowToVisible(i) }
    }

    private func updateEmpty() {
        let empty = shown.isEmpty
        emptyLabel.stringValue = empty ? RoutineText.empty : ""
        emptyLabel.isHidden = !empty
        scroll.isHidden = empty
        detail.isHidden = empty
        divider.isHidden = empty
    }

    private func updateHints() {
        let paused = session.selected?.paused ?? false
        hints.rootView = Hints(items: [("↑↓", "select"), ("⏎", "run now"), ("P", paused ? "resume" : "pause"), ("O", "open run"),
                                       ("⌫", session.confirmRemove != nil ? "confirm" : "remove"), ("esc", session.confirmRemove != nil ? "cancel" : "close")],
                               leading: "\(shown.count) routine\(shown.count == 1 ? "" : "s")")
    }

    // MARK: The list

    func numberOfRows(in tableView: NSTableView) -> Int { shown.count }

    func tableView(_ tableView: NSTableView, viewFor tableColumn: NSTableColumn?, row: Int) -> NSView? {
        let v = tableView.makeView(withIdentifier: RoutineRowCell.id, owner: nil) as? RoutineRowCell ?? RoutineRowCell()
        v.identifier = RoutineRowCell.id
        let r = shown[row]
        let mac = r.machine.isEmpty ? (model?.localMachine ?? "") : r.machine
        v.show(r, machine: machineNames()[mac] ?? mac, selected: r.id == session.selectedID)
        return v
    }

    func tableView(_ tableView: NSTableView, rowViewForRow row: Int) -> NSTableRowView? { RoutineTableRowView() }

    @objc private func rowClicked() {
        let r = table.clickedRow
        guard shown.indices.contains(r) else { return }
        session.select(shown[r].id)
    }

    // MARK: Keys

    func handleKey(_ event: NSEvent, equivalent: Bool) -> Bool {
        let chord = KeyChord(event: event)
        let action = RoutineKeys.route(chord, keyCode: event.keyCode)
        if equivalent {
            guard chord.command else { return false }
        } else if chord.command {
            return false // performKeyEquivalent has ⌘ chords
        }
        if action == .pass {
            // Another sheet's or overlay's ⌘ key (⌘K, ⌘Y, ⌘R, ⌘J): this one steps aside.
            if equivalent, chord.command, !chord.shift, case .char(let c) = chord.key, "kyjr".contains(c) { close() }
            return false
        }
        perform(action)
        return true
    }

    func perform(_ a: RoutineKeyAction) {
        switch a {
        case .previous: session.step(-1)
        case .next: session.step(1)
        case .runNow: session.runNow()
        case .togglePause: session.togglePause()
        case .open: session.open()
        case .remove: session.remove()
        case .close: if !session.cancelConfirm() { close() }
        case .pass: break
        }
    }

    // MARK: Open a run

    /// A live run: its agent's tile (a background one comes back first).
    private func openAgent(_ id: String) {
        guard let model else { return }
        guard model.agent(id) != nil else {
            model.showToast("The run's agent is gone")
            return
        }
        close()
        model.goTo(id)
    }

    /// An ended run: its session in History, when the index has it.
    private func openSession(_ sid: String) {
        guard let model else { return }
        guard model.historyAvailable else {
            model.showToast("History needs a newer hesperd on this Mac")
            return
        }
        let client = model.client
        close()
        Task { [weak model] in
            let detail = await Task.detached { try? await client.showSession(sid) }.value
            guard let model else { return }
            if let s = detail?.session {
                HistoryPanelHost.open(model, text: nil, select: s)
            } else {
                HistoryPanelHost.open(model, text: sid, select: nil)
            }
        }
    }
}

/// The list's table: no first responder of its own (the panel routes
/// keys), no type-select.
final class RoutineTable: NSTableView {
    override var acceptsFirstResponder: Bool { false }
}

/// Rows draw their own backgrounds.
final class RoutineTableRowView: NSTableRowView {
    override func drawBackground(in dirtyRect: NSRect) {}
    override func drawSelection(in dirtyRect: NSRect) {}
    override var isOpaque: Bool { false }
}
