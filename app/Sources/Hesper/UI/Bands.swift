import AppKit
import HesperCore
import SwiftUI

// Bands on the wall (docs/rebuild-contract.md "As built — projects
// (views)"): each group / project / branch gets a quiet heading row above
// its cards (6 pt project square, name, Mono path, count, collapse caret,
// the needs-you pill when collapsed, ＋ new agent here, ↗ open as wall)
// with a hairline below. No backdrop. A click on the heading collapses /
// expands it (⌥-click: every other band collapses); dragging it reorders
// the bands of this wall.

/// A band's drop target while a heading is dragged (hidden otherwise: bands
/// have no backdrop).
@MainActor
final class BandBackdropView: NSView {
    override var isFlipped: Bool { true }
    /// Kept for the wall's bookkeeping; the drop target is `working`.
    var colorHex = Theme.Token.dim.hex
    var highlighted = false { didSet { if oldValue != highlighted { restyle() } } }

    override init(frame: NSRect) {
        super.init(frame: frame)
        wantsLayer = true
        DS.Radius.apply(DS.Radius.panel, to: layer)
        isHidden = true
        restyle()
    }

    @available(*, unavailable)
    required init?(coder: NSCoder) { fatalError() }

    override func viewDidChangeEffectiveAppearance() {
        super.viewDidChangeEffectiveAppearance()
        restyle()
    }

    /// The drop target's tint and border.
    static let fillAlpha: CGFloat = 0.08
    static let borderAlpha: CGFloat = 0.6

    /// The container's quiet fill over the wall (the heading and its cards
    /// read as one group).
    static let restFillAlpha: CGFloat = 0.45

    private func restyle() {
        layer?.backgroundColor = highlighted ? Theme.ns(.working, alpha: Self.fillAlpha).cg(in: self)
                                             : Theme.ns(.surface, alpha: Self.restFillAlpha).cg(in: self)
        layer?.borderColor = highlighted ? Theme.ns(.working, alpha: Self.borderAlpha).cg(in: self) : Theme.hairline.cg(in: self)
        layer?.borderWidth = 1
    }

    override func hitTest(_ point: NSPoint) -> NSView? { nil }
}

/// What a band heading shows: square, name, path, count, caret.
struct BandHeaderContent: View {
    var title: String
    var subtitle: String
    var colorHex: String
    var total: Int
    var active: Int
    var needsYou: Int
    var collapsed: Bool
    var compact: Bool
    /// A pointer line (desks): "N agents · N working".
    var pointer = false

    /// The project square (6 pt, so Signal stays the only loud color).
    static let swatch: CGFloat = DS.Spacing.s

    var body: some View {
        HStack(spacing: DS.Spacing.m) {
            Rectangle().fill(Theme.color(colorHex)).frame(width: Self.swatch, height: Self.swatch)
            Text(title)
                .font(.ds(.body, .semibold))
                .foregroundStyle(Theme.fg)
                .lineLimit(1)
                .layoutPriority(2)
            if !compact && !subtitle.isEmpty {
                Text(subtitle)
                    .font(.ds(.meta))
                    .foregroundStyle(Theme.dim)
                    .lineLimit(1)
                    .truncationMode(.middle)
            }
            Text(countLabel)
                .font(.ds(.meta))
                .foregroundStyle(Theme.dim)
                .lineLimit(1)
                .fixedSize()
            Image(systemName: "chevron.down")
                .font(.ds(.meta, .semibold))
                .rotationEffect(.degrees(collapsed ? -90 : 0))
                .foregroundStyle(Theme.dim)
                .accessibilityHidden(true)
            if needsYou > 0 && collapsed {
                Pill("\(needsYou) needs you", mark: .needsYou)
                    .accessibilityIdentifier("band.badge")
            }
            Spacer(minLength: 0)
        }
        .padding(.leading, DS.Spacing.xs)
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .leading)
    }

    private var countLabel: String {
        if pointer { return "\(total) · \(active) working" }
        var parts = ["\(total)"]
        if active > 0 && active != total { parts.append("\(active) active") }
        if needsYou > 0 && !collapsed { parts.append("\(needsYou) need\(needsYou == 1 ? "s" : "") you") }
        return parts.joined(separator: " · ")
    }
}

/// A band's heading: the label (SwiftUI), ＋ / ↗ IconButtons and a hairline
/// below the row (`working`, 2 pt, while focused: ⌘N opens a draft here).
/// Hover raises the row (`surface`). Clicks collapse, drags reorder
/// (reported to the wall). The view spans the layout's header rect; the
/// row is its top `bandHeading`, the rest is space before the cards.
@MainActor
final class BandHeaderView: NSView {
    let key: String
    private let label = NSHostingView(rootView: BandHeaderContent(title: "", subtitle: "", colorHex: Theme.Token.dim.hex, total: 0, active: 0, needsYou: 0, collapsed: false, compact: false))
    private let row = NSView()
    private let hairline = NSView()
    let plus = IconButtonView(symbol: "plus", help: "New agent here", shortcut: "⌘N", target: nil, action: nil)
    let open = IconButtonView(symbol: "arrow.up.right.square", help: "Open as a wall", target: nil, action: nil)
    private(set) var band: Band?
    private(set) var collapsed = false
    private(set) var needsYou = 0
    var onToggle: ((Bool) -> Void)?
    var onNew: (() -> Void)?
    var onOpenWall: (() -> Void)?
    /// Drag: began / moved (wall point) / ended (wall point).
    var onDrag: ((DragPhase, NSPoint) -> Void)?
    enum DragPhase { case began, moved, ended }
    private var downAt: NSPoint?
    private var dragging = false
    private var hovering = false { didSet { if oldValue != hovering { restyle() } } }
    private var hoverArea: NSTrackingArea?

    override var isFlipped: Bool { true }

    init(key: String) {
        self.key = key
        super.init(frame: .zero)
        wantsLayer = true
        row.wantsLayer = true
        DS.Radius.apply(DS.Radius.control, to: row.layer)
        hairline.wantsLayer = true
        addSubview(row)
        addSubview(hairline)
        addSubview(label)
        for (b, id) in [(plus, "new"), (open, "open")] {
            b.target = self
            b.action = #selector(button(_:))
            b.setAccessibilityIdentifier("band.\(key).\(id)")
            addSubview(b)
        }
        setAccessibilityElement(true)
        setAccessibilityRole(.group)
        setAccessibilityIdentifier("band.\(key)")
    }

    @available(*, unavailable)
    required init?(coder: NSCoder) { fatalError() }

    private(set) var focused = false

    func apply(_ b: Band, counts: (total: Int, active: Int, needsYou: Int, quiet: Int), collapsed: Bool, canOpenWall: Bool, focused: Bool = false) {
        self.focused = focused
        band = b
        self.collapsed = collapsed
        needsYou = counts.needsYou
        label.rootView = BandHeaderContent(title: b.displayTitle, subtitle: b.subtitle, colorHex: b.colorHex, total: counts.total, active: counts.active,
                                           needsYou: counts.needsYou, collapsed: collapsed, compact: bounds.width < Self.compactWidth, pointer: b.pointer != nil)
        // A pointer line (its project has its own wall): no ＋ / ↗; a
        // click brings that wall forward.
        let pointer = b.pointer != nil
        plus.isHidden = pointer
        open.isHidden = pointer || !canOpenWall || b.wallScope == nil
        toolTip = pointer ? "Show its wall window" : b.isNew ? "Drafts without a project: choose a folder, start, and the agent joins its project's band" : nil
        plus.toolTip = b.isNew ? "New agent (⌘N)" : "New agent in \(b.title) (⌘N while this heading is focused)"
        setAccessibilityLabel("\(b.displayTitle), \(counts.total) agents\(counts.needsYou > 0 ? ", \(counts.needsYou) need you" : "")\(pointer ? ", pointer" : collapsed ? ", collapsed" : "")")
        restyle()
        setAccessibilityValue((pointer ? "pointer" : collapsed ? "collapsed" : "expanded") + (focused ? ", focused" : ""))
        needsLayout = true
    }

    /// Below this width the path is left out.
    static let compactWidth: CGFloat = 420

    override func viewDidChangeEffectiveAppearance() {
        super.viewDidChangeEffectiveAppearance()
        restyle()
    }

    private var focusRing: Bool { focused && band?.pointer == nil }

    /// Layer colors from the last `apply` (re-run when the appearance changes).
    private func restyle() {
        row.layer?.backgroundColor = hovering ? Theme.ns(.surface).cg(in: self) : NSColor.clear.cgColor
        hairline.layer?.backgroundColor = (focusRing ? Theme.ns(.working) : Theme.hairline).cg(in: self)
        needsLayout = true
    }

    override func updateTrackingAreas() {
        super.updateTrackingAreas()
        if let hoverArea { removeTrackingArea(hoverArea) }
        let t = NSTrackingArea(rect: .zero, options: [.mouseEnteredAndExited, .activeInActiveApp, .inVisibleRect], owner: self)
        addTrackingArea(t)
        hoverArea = t
    }

    override func mouseEntered(with event: NSEvent) { hovering = true }
    override func mouseExited(with event: NSEvent) { hovering = false }

    /// The heading row's height (the top of the header rect).
    private var rowHeight: CGFloat { min(bounds.height, CGFloat(Metrics.wall.bandHeading)) }

    override func layout() {
        super.layout()
        let h = rowHeight
        let s = IconButton.side
        let y = ((h - s) / 2).rounded()
        // Inside the band's bordered container: the row keeps the
        // container's padding on both sides, so it lines up with the cards.
        let pad = CGFloat(Metrics.wall.bandPad)
        let w = max(0, bounds.width - 2 * pad)
        row.frame = NSRect(x: pad, y: 0, width: w, height: max(0, h - 1))
        // The container links heading and cards; the line only marks focus.
        let line: CGFloat = 2
        hairline.isHidden = !focusRing
        hairline.frame = NSRect(x: pad, y: h - line, width: w, height: line)
        open.frame = NSRect(x: pad + w - s, y: y, width: s, height: s)
        plus.frame = NSRect(x: (open.isHidden ? pad + w : open.frame.minX - DS.Spacing.xxs) - s, y: y, width: s, height: s)
        label.frame = NSRect(x: pad, y: 0, width: max(0, plus.frame.minX - DS.Spacing.xs - pad), height: h)
    }

    @objc private func button(_ sender: NSButton) {
        if sender === plus { onNew?() } else { onOpenWall?() }
    }

    override func hitTest(_ point: NSPoint) -> NSView? {
        let p = convert(point, from: superview)
        for b in [plus, open] where !b.isHidden && b.frame.contains(p) { return b }
        return bounds.contains(p) ? self : nil
    }

    override func mouseDown(with event: NSEvent) {
        downAt = event.locationInWindow
        dragging = false
    }

    /// How far the pointer moves before a press becomes a drag.
    static let dragSlop: CGFloat = 5

    override func mouseDragged(with event: NSEvent) {
        guard let downAt, let wall = superview else { return }
        let p = event.locationInWindow
        if !dragging && hypot(p.x - downAt.x, p.y - downAt.y) > Self.dragSlop {
            dragging = true
            onDrag?(.began, wall.convert(p, from: nil))
        }
        if dragging { onDrag?(.moved, wall.convert(p, from: nil)) }
    }

    override func mouseUp(with event: NSEvent) {
        defer { downAt = nil; dragging = false }
        if dragging, let wall = superview {
            onDrag?(.ended, wall.convert(event.locationInWindow, from: nil))
            return
        }
        onToggle?(event.modifierFlags.contains(.option))
    }

    override func resetCursorRects() { addCursorRect(NSRect(x: 0, y: 0, width: max(0, plus.frame.minX), height: rowHeight), cursor: .openHand) }
}

// MARK: Home wall: needs you elsewhere

/// The home wall's compact strip: agents needing you that this wall
/// doesn't show (scoped out, collapsed). A click takes you there (⌘J's
/// routing); an approval can be allowed from here.
struct NeedsYouStrip: View {
    var agents: [Agent]
    var projectName: (Agent) -> String?
    var onOpen: (String) -> Void
    var onAllow: (Agent) -> Void

    var body: some View {
        HStack(spacing: DS.Spacing.m) {
            HStack(spacing: DS.Spacing.xs) {
                StateMark(.needsYou)
                Text("Needs you elsewhere")
                    .font(.ds(.chrome, .semibold))
            }
            .foregroundStyle(Theme.color(.signal))
            .fixedSize()
            ScrollView(.horizontal, showsIndicators: false) {
                HStack(spacing: DS.Spacing.m) {
                    ForEach(agents, id: \.id) { a in card(a) }
                }
            }
            Spacer(minLength: 0)
        }
        .padding(.horizontal, DS.Spacing.l)
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .leading)
        .background(DS.Radius.shape(DS.Radius.tile).fill(Theme.color(.surface)))
        .overlay(DS.Radius.shape(DS.Radius.tile).strokeBorder(Theme.color(.signal).opacity(AttentionBar.primaryStroke), lineWidth: 1))
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("home.needsYou")
    }

    private func card(_ a: Agent) -> some View {
        HStack(spacing: DS.Spacing.m) {
            StateMark(state: a.state)
            VStack(alignment: .leading, spacing: DS.Spacing.xxs) {
                HStack(spacing: DS.Spacing.xs) {
                    Text(a.name).font(.ds(.chrome, .semibold)).foregroundStyle(Theme.fg).lineLimit(1)
                    if let p = projectName(a) { Text(p).font(.ds(.chrome)).foregroundStyle(Theme.dim).lineLimit(1) }
                }
                Text([a.attention?.title, a.attention?.detail].compactMap { $0 }.joined(separator: ": "))
                    .font(.ds(.meta)).foregroundStyle(Theme.fg2).lineLimit(1)
            }
            .frame(maxWidth: Self.cardWidth, alignment: .leading)
            if a.state == .approval {
                Button(action: { onAllow(a) }) { Pill("Allow", mark: .needsYou) }
                    .buttonStyle(.plain)
            }
        }
        .padding(.horizontal, DS.Spacing.m).padding(.vertical, DS.Spacing.s)
        .background(DS.Radius.shape(DS.Radius.tile).fill(Theme.color(.surface)))
        .overlay(DS.Radius.shape(DS.Radius.tile).strokeBorder(Theme.state(a.state), lineWidth: 1))
        .contentShape(Rectangle())
        .onTapGesture { onOpen(a.id) }
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("home.card.\(a.id)")
    }

    static let cardWidth: CGFloat = 260
}
