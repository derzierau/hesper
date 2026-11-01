import AppKit
import HesperCore
import SwiftUI

/// Which of the loops sheets is up: the board (⌘L), the wrap-up (⇧⌘E)
/// or a product's page (⇧⌘L, a project or group id).
enum LoopsSheetKind: Equatable {
    /// The board, `select` selected on it.
    case loops(select: String?)
    /// One loop's detail in the Loops sheet ("Open the loop").
    case loop(String)
    case wrapUp
    case product(scope: String?)

    var surface: Surface {
        switch self {
        case .loops, .loop: return .loops
        case .wrapUp: return .wrapUp
        case .product: return .product
        }
    }

    func same(_ o: LoopsSheetKind) -> Bool { surface == o.surface }
}

/// Which wall windows have a loops sheet, and the window's key path into
/// it (MainWindow asks first while one is open), like ShortcutsSheetHost.
/// One panel per window shows one sheet at a time; opening another
/// replaces it.
@MainActor
enum LoopsSheetHost {
    private final class Entry {
        weak var root: RootView?
        var panel: LoopsPanel?
        init(_ r: RootView) { root = r }
    }
    private static var entries: [ObjectIdentifier: Entry] = [:]

    /// RootView.init (marked integration point).
    static func attach(_ root: RootView) {
        entries = entries.filter { $0.value.root != nil }
        entries[ObjectIdentifier(root.model)] = Entry(root)
    }

    static func panel(for model: AppModel) -> LoopsPanel? { entries[ObjectIdentifier(model)]?.panel }
    static func isOpen(_ model: AppModel) -> Bool { panel(for: model)?.isOpen == true }
    /// The surface of the open sheet (nil: none).
    static func surface(_ model: AppModel) -> Surface? { panel(for: model).flatMap { $0.isOpen ? $0.kind.surface : nil } }

    private static func entry(for window: NSWindow) -> Entry? {
        entries.values.first { $0.root?.window === window }
    }

    /// Opens `kind` (another loops sheet is replaced); the same kind
    /// again closes it unless `toggle` is false.
    static func show(_ model: AppModel, _ kind: LoopsSheetKind, toggle: Bool = true) {
        if let p = panel(for: model), p.isOpen, p.kind.same(kind) {
            if toggle { p.close() } else { p.open(kind) }
            return
        }
        guard let e = entries[ObjectIdentifier(model)], let root = e.root else { return }
        let p = e.panel ?? LoopsPanel(model: model, root: root)
        e.panel = p
        p.open(kind)
    }

    static func close(_ model: AppModel) { panel(for: model)?.close() }

    static func handleKeyEquivalent(_ event: NSEvent, window: NSWindow) -> Bool {
        guard let p = entry(for: window)?.panel, p.isOpen else { return false }
        return p.handleKey(event, equivalent: true)
    }

    static func handleKey(_ event: NSEvent, window: NSWindow) -> Bool {
        guard event.type == .keyDown, let p = entry(for: window)?.panel, p.isOpen else { return false }
        return p.handleKey(event, equivalent: false)
    }
}

/// The sheets' size in the scrim: wider than History (the board has four
/// columns): min(1080, 86% of the window) by min(760, 84%).
enum LoopsLook {
    static let maxWidth: CGFloat = 1080
    static let maxHeight: CGFloat = 760
    static let widthFraction: CGFloat = 0.86
    static let heightFraction: CGFloat = 0.84
    /// A board column's narrowest width before the board scrolls sideways.
    static let columnMinWidth: CGFloat = 220
    /// The wrap-up's and product page's left list.
    static let listWidth: CGFloat = 300
    /// The verdict cards' height in the wrap-up.
    static let verdictHeight: CGFloat = 72
    /// A milestone's progress bar.
    static let barHeight: CGFloat = 4
    /// The planning view's terminal side (its share of the width).
    static let terminalFraction: CGFloat = 0.58
    /// The detail's label column (Goal, Done when), the steps' columns.
    static let labelWidth: CGFloat = 72
    static let numberWidth: CGFloat = 16
    static let ownerWidth: CGFloat = 56
    static let triggerWidth: CGFloat = 112
    static let whenWidth: CGFloat = 32
    /// Offers stay in the loop, dimmed (they never notify).
    static let offerOpacity = 0.7
    /// The merge and file pickers.
    static let pickerWidth: CGFloat = 420
    static let pickerRows = 8

    static func frame(in size: CGSize) -> CGRect {
        let w = min(maxWidth, (size.width * widthFraction).rounded())
        let h = min(maxHeight, (size.height * heightFraction).rounded())
        let y = ((size.height - h) * SearchLook.historyAboveFraction).rounded()
        return CGRect(x: ((size.width - w) / 2).rounded(), y: max(0, y), width: max(0, w), height: max(0, h))
    }
}

/// The modal frame of the loops sheets over the wall (a scrim that takes
/// every click, one opaque Panel holding the SwiftUI sheet). Keys come
/// from the window (`LoopsKeys` on the sheet's surface) and go to the
/// sheet's model; a field that has the keyboard keeps its letters.
@MainActor
final class LoopsPanel: NSView {
    private weak var model: AppModel?
    private weak var root: RootView?
    private(set) var isOpen = false
    private(set) var kind: LoopsSheetKind = .loops(select: nil)
    /// The open sheet's model (one of them).
    private(set) var board: LoopsSheetModel?
    private(set) var wrapUp: WrapUpSheetModel?
    private(set) var product: ProductSheetModel?

    private let panel = PanelView()
    private var hosting: NSHostingView<AnyView>
    private let hoverBlock = HoverSuspension()
    private var scrimTracking: NSTrackingArea?

    override var acceptsFirstResponder: Bool { true }

    init(model: AppModel, root: RootView) {
        self.model = model
        self.root = root
        hosting = NSHostingView(rootView: AnyView(EmptyView()))
        super.init(frame: .zero)
        wantsLayer = true
        panel.isSheet = true
        isHidden = true
        setAccessibilityElement(true)
        setAccessibilityIdentifier("loops")
        setAccessibilityRole(.group)
        setAccessibilityLabel("Loops")
        addSubview(panel)
        hosting.frame = panel.contentView.bounds
        hosting.autoresizingMask = [.width, .height]
        panel.contentView.addSubview(hosting)
        applyTheme()
    }

    @available(*, unavailable)
    required init?(coder: NSCoder) { fatalError() }

    // MARK: Open / close

    func open(_ kind: LoopsSheetKind) {
        guard let root, let model else { return }
        model.closePopover()
        model.showPalette = false
        if superview == nil { root.addLoopsPanel(self) }
        let wasOpen = isOpen
        let replaces = !wasOpen || !self.kind.same(kind)
        self.kind = kind
        isOpen = true
        isHidden = false
        frame = root.reviewFrame
        let hub = model.loopHub
        let close: () -> Void = { [weak self] in self?.close() }
        switch kind {
        case .loops, .loop:
            let m = (replaces ? nil : board) ?? LoopsSheetModel(hub: hub, app: model)
            switch kind {
            case .loop(let id): m.open(select: id, detail: true)
            case .loops(let select): m.open(select: select, detail: false)
            default: break
            }
            board = m; wrapUp = nil; product = nil
            if replaces { hosting.rootView = AnyView(LoopsSheetView(model: m, hub: hub, onClose: close)) }
        case .wrapUp:
            let m = (replaces ? nil : wrapUp) ?? WrapUpSheetModel(hub: hub, app: model)
            m.open()
            wrapUp = m; board = nil; product = nil
            if replaces { hosting.rootView = AnyView(WrapUpSheetView(model: m, hub: hub, onClose: close)) }
        case .product(let scope):
            // Kept while hidden (its planner reattaches on show); another sheet drops it.
            let m = product ?? ProductSheetModel(hub: hub, app: model)
            m.open(scope: scope)
            product = m; board = nil; wrapUp = nil
            if replaces { hosting.rootView = AnyView(ProductSheetView(model: m, hub: hub, onClose: close)) }
        }
        setAccessibilityIdentifier("loops.\(kind.surface)")
        applyTheme()
        needsLayout = true
        guard !wasOpen else { window?.makeFirstResponder(hosting); return }
        hoverBlock.suspend(under: self)
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
        board?.closed(); wrapUp?.closed(); product?.closed()
        DS.animate(.quick, { self.animator().alphaValue = 0 }, completion: { [weak self] in
            guard let self, !self.isOpen else { return }
            self.isHidden = true
            self.alphaValue = 1
        })
        root?.loopsClosed()
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
        panel.frame = LoopsLook.frame(in: bounds.size)
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

    /// ⌘ chords: the sheet's own (⌘↩, its toggle) run here; another
    /// sheet's closes this one and goes on to its owner; the rest go to
    /// the menu. Plain keys: the sheet's verbs, unless a field has them.
    func handleKey(_ event: NSEvent, equivalent: Bool) -> Bool {
        let chord = KeyChord(event: event)
        let surface = kind.surface
        let editing = window?.firstResponder is NSText
        if equivalent {
            guard chord.command else { return false }
            if LoopsKeys.passesThrough(chord, in: surface) { close(); return false }
            let action = LoopsKeys.route(chord, in: surface)
            guard action != .pass else { return false }
            return perform(action)
        }
        if chord.command { return false }
        return perform(LoopsKeys.route(chord, in: surface, editing: editing))
    }

    /// The sheet's model runs it; true when the key was the sheet's.
    private func perform(_ action: LoopsKeyAction) -> Bool {
        if action == .pass { return false }
        let handled: Bool
        if let board { handled = board.perform(action) } else if let wrapUp { handled = wrapUp.perform(action) } else if let product { handled = product.perform(action) } else { handled = false }
        if !handled, action == .close { close(); return true }
        return handled
    }
}

// MARK: The wall model's hooks

extension AppModel {
    /// ⌘L: the board (on `select`, else the selected agent's loop), or
    /// close it.
    func toggleLoops(select: String? = nil) {
        prepareForLoops()
        LoopsSheetHost.show(self, .loops(select: select ?? current.flatMap { loopHub.chip(for: $0)?.loop }))
    }

    /// ⇧⌘L: the product page of `scope` (nil: the wall's project or group,
    /// else the first product).
    func toggleProduct(scope: String? = nil) {
        prepareForLoops()
        LoopsSheetHost.show(self, .product(scope: scope ?? productScope))
    }

    /// ⇧⌘E: the evening wrap-up.
    func toggleWrapUp() {
        prepareForLoops()
        LoopsSheetHost.show(self, .wrapUp)
    }

    /// The sheet open on `kind` (never closes it): hesperctl open.
    func openLoopsSheet(_ kind: LoopsSheetKind) {
        prepareForLoops()
        LoopsSheetHost.show(self, kind, toggle: false)
    }

    /// The wall's scope as a product scope: its project or group.
    var productScope: String? {
        switch scope {
        case .project(let id), .group(let id): return id
        default: return current?.projectId
        }
    }

    private func prepareForLoops() {
        if mode == .compose { leaveComposer() }
        closePopover()
        showPalette = false
        if HistoryPanelHost.isOpen(self) { HistoryPanelHost.panel(for: self)?.close() }
        if ReviewPanelHost.isOpen(self) { ReviewPanelHost.panel(for: self)?.close() }
        if ShortcutsSheetHost.isOpen(self) { ShortcutsSheetHost.panel(for: self)?.close() }
    }
}
