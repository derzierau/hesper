import AppKit
import HesperCore
import SwiftUI

/// Which wall windows have a shortcuts sheet, and the window's key path
/// into it (MainWindow asks first while one is open), like
/// HistoryPanelHost and ReviewPanelHost.
@MainActor
enum ShortcutsSheetHost {
    private final class Entry {
        weak var root: RootView?
        var panel: ShortcutsPanel?
        init(_ r: RootView) { root = r }
    }
    private static var entries: [ObjectIdentifier: Entry] = [:]

    /// RootView.init (marked integration point).
    static func attach(_ root: RootView) {
        entries = entries.filter { $0.value.root != nil }
        entries[ObjectIdentifier(root.model)] = Entry(root)
    }

    static func panel(for model: AppModel) -> ShortcutsPanel? { entries[ObjectIdentifier(model)]?.panel }
    static func isOpen(_ model: AppModel) -> Bool { panel(for: model)?.isOpen == true }

    private static func entry(for window: NSWindow) -> Entry? {
        entries.values.first { $0.root?.window === window }
    }

    /// ⌘/ and Help ▸ Keyboard Shortcuts: open on the surface the user is
    /// in, or close when open.
    /// - Parameter openSettings: the footer's "Open Settings › Shortcuts".
    static func toggle(_ model: AppModel, keyWindow: NSWindow?, openSettings: @escaping () -> Void) {
        if let p = panel(for: model), p.isOpen { p.close(); return }
        guard let e = entries[ObjectIdentifier(model)], let root = e.root else { return }
        let current = surface(of: model, keyWindow: keyWindow)
        let p = e.panel ?? ShortcutsPanel(model: model, root: root)
        e.panel = p
        p.openSettings = openSettings
        p.open(current: current)
    }

    /// The surface whose section the sheet opens on: the front sheet or
    /// overlay, else the window's mode.
    static func surface(of model: AppModel, keyWindow: NSWindow?) -> Surface {
        if model.showPalette { return .palette }
        if let p = model.popover { return p == .attention ? .inbox : .popover }
        if ReviewPanelHost.isOpen(model) { return .review }
        if HistoryPanelHost.isOpen(model) { return .history }
        if keyWindow is AgentWindow { return .agentWindow }
        if keyWindow != nil, !(keyWindow is MainWindow) { return .settings }
        switch model.mode {
        case .focus: return .focus
        case .compose: return .composer
        case .wall: return model.activeTileID != nil ? .activeTile : .wall
        }
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

/// The sheet's size in the scrim: min(640, 70% of the window) wide and
/// min(680, 80%) tall, a little above the middle like History.
enum ShortcutsLook {
    static let maxWidth: CGFloat = 640
    static let maxHeight: CGFloat = 680
    static let widthFraction: CGFloat = 0.7
    static let heightFraction: CGFloat = 0.8
    /// The search field's width in the toolbar row.
    static let searchWidth: CGFloat = 260

    static func frame(in size: CGSize) -> CGRect {
        let w = min(maxWidth, (size.width * widthFraction).rounded())
        let h = min(maxHeight, (size.height * heightFraction).rounded())
        let y = ((size.height - h) * SearchLook.historyAboveFraction).rounded()
        return CGRect(x: ((size.width - w) / 2).rounded(), y: max(0, y), width: max(0, w), height: max(0, h))
    }
}

/// Keyboard Shortcuts (⌘/): a modal sheet over the wall like History and
/// Review (a Night scrim that takes every click, one opaque Panel in the
/// SheetChrome frame). The chrome's toolbar holds the search field, the
/// content the sections per surface (the user's surface first and open,
/// the others folded until clicked), the footer "Open Settings ›
/// Shortcuts ⌘,". esc or a scrim click closes; ⌘/ toggles through the
/// Help menu.
@MainActor
final class ShortcutsPanel: NSView {
    private weak var model: AppModel?
    private weak var root: RootView?
    private(set) var isOpen = false
    var openSettings: () -> Void = {}

    private let panel = PanelView()
    private let hosting: NSHostingView<ShortcutsSheetView>
    private let hoverBlock = HoverSuspension()
    private var scrimTracking: NSTrackingArea?
    private var opens = 0

    override var acceptsFirstResponder: Bool { true }

    init(model: AppModel, root: RootView) {
        self.model = model
        self.root = root
        hosting = NSHostingView(rootView: ShortcutsSheetView(current: .wall, opens: 0, onClose: {}, onSettings: {}))
        super.init(frame: .zero)
        wantsLayer = true
        panel.isSheet = true
        isHidden = true
        setAccessibilityElement(true)
        setAccessibilityIdentifier("shortcuts")
        setAccessibilityRole(.group)
        setAccessibilityLabel("Keyboard Shortcuts")
        addSubview(panel)
        hosting.frame = panel.contentView.bounds
        hosting.autoresizingMask = [.width, .height]
        panel.contentView.addSubview(hosting)
        applyTheme()
    }

    @available(*, unavailable)
    required init?(coder: NSCoder) { fatalError() }

    // MARK: Open / close

    func open(current: Surface) {
        guard let root, let model else { return }
        model.closePopover()
        model.showPalette = false
        if superview == nil { root.addShortcutsPanel(self) }
        isOpen = true
        isHidden = false
        frame = root.reviewFrame
        opens += 1
        hosting.rootView = ShortcutsSheetView(current: current, opens: opens,
                                              hotKey: model.settings.quickLaunchEnabled ? model.settings.hotKey.label : nil,
                                              overridden: !model.settings.shortcutOverrides.isEmpty,
                                              onClose: { [weak self] in self?.close() },
                                              onSettings: { [weak self] in self?.close(); self?.openSettings() })
        applyTheme()
        hoverBlock.suspend(under: self)
        needsLayout = true
        if let a = DS.caAnimation(.quick, keyPath: "transform") {
            alphaValue = 0
            DS.animate(.quick) { self.animator().alphaValue = 1 }
            a.fromValue = CATransform3DMakeScale(SearchLook.openScale, SearchLook.openScale, 1)
            a.toValue = CATransform3DIdentity
            panel.layer?.add(a, forKey: "open")
        }
        window?.makeFirstResponder(hosting)
    }

    func close() {
        guard isOpen else { return }
        isOpen = false
        hoverBlock.resume()
        DS.animate(.quick, { self.animator().alphaValue = 0 }, completion: { [weak self] in
            guard let self, !self.isOpen else { return }
            self.isHidden = true
            self.alphaValue = 1
        })
        root?.shortcutsClosed()
    }

    // MARK: The scrim takes every mouse event over the wall

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
    override func scrollWheel(with event: NSEvent) {
        if panel.frame.contains(convert(event.locationInWindow, from: nil)) { super.scrollWheel(with: event) }
    }

    override func mouseDown(with event: NSEvent) {
        if !panel.frame.contains(convert(event.locationInWindow, from: nil)) { close() }
    }

    // MARK: Layout

    override func layout() {
        super.layout()
        panel.frame = ShortcutsLook.frame(in: bounds.size)
        hosting.frame = panel.contentView.bounds
    }

    override func viewDidChangeEffectiveAppearance() {
        super.viewDidChangeEffectiveAppearance()
        applyTheme()
    }

    private func applyTheme() {
        layer?.backgroundColor = SearchLook.scrimColor(reduceTransparency: DS.reduceTransparency).cgColor
    }

    // MARK: Keys

    /// esc closes; the app-wide sheet chords (⌘K, ⌘Y, ⌘R, ⌘J) close it
    /// and go on to their owner; ⌘/ reaches the Help menu, which toggles.
    /// Everything else is the search field's.
    func handleKey(_ event: NSEvent, equivalent: Bool) -> Bool {
        let chord = KeyChord(event: event)
        if equivalent {
            guard chord.command else { return false }
            if CommandRegistry.resolve(chord, in: .everywhere).map(Self.passesThrough) == true { close() }
            return false
        }
        if chord.key == .escape, !chord.command { close(); return true }
        return false
    }

    /// The commands whose chord opens another sheet or overlay.
    static func passesThrough(_ id: CommandID) -> Bool {
        switch id {
        case .palette, .history, .review, .nextAttention: return true
        default: return false
        }
    }
}

/// The sheet's content: SheetChrome with the search field in the toolbar
/// row, the sections (a header Row with the count, then a Row per command
/// with its key caps) and the footer KeyButton to Settings › Shortcuts.
struct ShortcutsSheetView: View {
    var current: Surface
    /// Bumped by the host on every open: the field takes focus again and
    /// the folds reset.
    var opens: Int
    /// The quick launch hotkey's label (nil: quick launch is off).
    var hotKey: String?
    /// The user has overrides: only then can two commands share a chord.
    var overridden = false
    var onClose: () -> Void
    var onSettings: () -> Void

    @State private var query = ""
    /// The user's own folds (nil: the model's, the current surface open).
    @State private var folds: Set<Surface>?
    @FocusState private var searching: Bool
    /// The sections for the current query, computed when it changes (and
    /// on each open), not on every render.
    @State private var sections: [ShortcutsSheet.Section] = []

    private var count: Int { sections.reduce(0) { $0 + $1.rows.count } }

    private func computeSections() {
        sections = ShortcutsSheet.sections(current: current, query: query, hotKey: hotKey, scanConflicts: overridden)
    }

    var body: some View {
        SheetChrome(title: "Keyboard Shortcuts", symbol: "keyboard", count: count, closeIdentifier: "shortcuts.close", toolbar: { search }, footer: {
            KeyButton("Open Settings › Shortcuts", kbd: CommandRegistry.labels(.settings).first, action: onSettings)
                .accessibilityIdentifier("shortcuts.settings")
        }, onClose: onClose) {
            ScrollView {
                LazyVStack(alignment: .leading, spacing: DS.Spacing.s) {
                    ForEach(sections, id: \.surface) { s in section(s) }
                    if sections.isEmpty {
                        Text("No shortcut matches “\(query)”").font(DS.font(.chrome)).foregroundStyle(Theme.dim)
                            .padding(DS.Spacing.l)
                    }
                }
                .padding(DS.Spacing.l)
            }
            .accessibilityIdentifier("shortcuts.list")
        }
        .onAppear { computeSections(); searching = true }
        .onChange(of: opens) { query = ""; folds = nil; searching = true; computeSections() }
        .onChange(of: query) { folds = nil; computeSections() }
    }

    private var search: some View {
        HStack(spacing: DS.Spacing.s) {
            Image(systemName: "magnifyingglass").font(.system(size: IconButton.symbolSize, weight: .medium)).foregroundStyle(Theme.dim)
            TextField("Search shortcuts", text: $query)
                .textFieldStyle(.plain)
                .font(DS.font(.chrome))
                .focused($searching)
                .accessibilityIdentifier("shortcuts.search")
        }
        .padding(.horizontal, DS.Spacing.m)
        .frame(width: ShortcutsLook.searchWidth, height: IconButton.side)
        .background(DS.Radius.shape(DS.Radius.control).fill(Theme.color(.background)))
        .overlay(DS.Radius.shape(DS.Radius.control).strokeBorder(Theme.stroke, lineWidth: 1))
    }

    private func isOpen(_ s: ShortcutsSheet.Section) -> Bool { folds?.contains(s.surface) ?? s.expanded }

    @ViewBuilder private func section(_ s: ShortcutsSheet.Section) -> some View {
        let open = isOpen(s)
        Button {
            var f = folds ?? Set(sections.filter(\.expanded).map(\.surface))
            if open { f.remove(s.surface) } else { f.insert(s.surface) }
            folds = f
        } label: {
            HStack(spacing: DS.Spacing.m) {
                Image(systemName: open ? "chevron.down" : "chevron.right")
                    .font(.system(size: IconButton.symbolSize, weight: .medium)).foregroundStyle(Theme.dim)
                    .frame(width: IconButton.symbolSize)
                Text(s.title).font(DS.font(.chrome, .semibold)).foregroundStyle(Theme.fg2)
                Pill("", variant: .count, count: s.rows.count)
                Spacer(minLength: 0)
            }
            .padding(.horizontal, DS.Spacing.m)
            .frame(height: DS.chromeMaxHeight)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .accessibilityLabel(s.title)
        .accessibilityAddTraits(.isHeader)
        .accessibilityIdentifier("shortcuts.section.\(s.surface)")
        if open {
            VStack(spacing: 0) {
                ForEach(s.rows, id: \.id) { r in
                    Row(title: r.title, kbd: r.chord, indent: DS.Spacing.l)
                        .accessibilityIdentifier(ShortcutsSheet.rowIdentifier(r.id))
                }
            }
        }
    }
}
