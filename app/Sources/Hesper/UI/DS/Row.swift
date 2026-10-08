import AppKit
import HesperCore
import SwiftUI

/// Row: the list row of the sidebar, popovers, the palette and History: a
/// leading StateMark, a title (Geist 13), an optional subtitle (dim, 12),
/// trailing meta in Geist Mono (11, dim) and an optional Kbd. Hover: a
/// `line` fill at 60%; selected: `working` at 20%. Radius `control`.
/// SwiftUI `Row(title: "api", subtitle: "migrations", meta: "mini · 2h",
/// mark: .needsYou, kbd: "⏎", selected: true)`; AppKit `RowView`.
struct Row: View {
    var title: String
    var subtitle: String? = nil
    var meta: String? = nil
    var mark: StateMarkKind? = nil
    var kbd: String? = nil
    var selected = false
    /// A plain colored square in the mark's place (a project, a group).
    var swatch: Color? = nil
    /// A state mark before the meta (the sidebar's "needs you").
    var trailingMark: StateMarkKind? = nil
    /// Extra leading room (nesting), inside the hover/selected fill.
    var indent: CGFloat = 0

    @State private var hover = false

    static func height(subtitle: Bool) -> CGFloat { subtitle ? 40 : DS.chromeMaxHeight }
    /// The side of a swatch: a little smaller than a state mark.
    static let swatchSide = DS.Spacing.s

    var body: some View {
        HStack(spacing: DS.Spacing.m) {
            if let mark { StateMark(mark) } else if let swatch { StateMark(color: swatch, side: Self.swatchSide) }
            VStack(alignment: .leading, spacing: DS.Spacing.xxs) {
                Text(title).font(DS.font(.body)).foregroundStyle(Theme.fg).lineLimit(1).truncationMode(.tail)
                if let subtitle {
                    Text(subtitle).font(DS.font(.chrome)).foregroundStyle(Theme.dim).lineLimit(1).truncationMode(.tail)
                }
            }
            Spacer(minLength: DS.Spacing.m)
            if let trailingMark { StateMark(trailingMark) }
            if let meta { Text(meta).font(DS.font(.meta)).foregroundStyle(Theme.dim).lineLimit(1) }
            if let kbd { Kbd(kbd) }
        }
        .padding(.leading, DS.Spacing.m + indent)
        .padding(.trailing, DS.Spacing.m)
        .frame(height: Self.height(subtitle: subtitle != nil))
        .background(DS.Radius.shape(DS.Radius.control).fill(background))
        .contentShape(Rectangle())
        .onHover { hover = $0 }
        .accessibilityElement(children: .combine)
    }

    private var background: Color {
        if selected { return Theme.selection }
        return hover ? Theme.chipBG.opacity(0.6) : .clear
    }
}

/// AppKit Row (wall-speed lists).
@MainActor
final class RowView: NSView {
    var title: String { didSet { titleLabel.stringValue = title } }
    var subtitle: String? { didSet { subtitleLabel.stringValue = subtitle ?? ""; subtitleLabel.isHidden = subtitle == nil; needsLayout = true } }
    var meta: String? { didSet { metaLabel.stringValue = meta ?? ""; metaLabel.isHidden = meta == nil; needsLayout = true } }
    var mark: StateMarkKind? { didSet { markView.isHidden = mark == nil; if let mark { markView.kind = mark }; needsLayout = true } }
    var kbd: String? { didSet { kbdView.keys = kbd ?? ""; kbdView.isHidden = kbd == nil; needsLayout = true } }
    var isSelected = false { didSet { applyTheme() } }
    private var hovering = false { didSet { applyTheme() } }

    private let markView = StateMarkView()
    private let titleLabel = NSTextField(labelWithString: "")
    private let subtitleLabel = NSTextField(labelWithString: "")
    private let metaLabel = NSTextField(labelWithString: "")
    private let kbdView = KbdView("")
    private var tracking: NSTrackingArea?

    init(title: String, subtitle: String? = nil, meta: String? = nil, mark: StateMarkKind? = nil, kbd: String? = nil) {
        self.title = title; self.subtitle = subtitle; self.meta = meta; self.mark = mark; self.kbd = kbd
        super.init(frame: NSRect(x: 0, y: 0, width: 280, height: Row.height(subtitle: subtitle != nil)))
        wantsLayer = true
        DS.Radius.apply(DS.Radius.control, to: layer)
        titleLabel.font = DS.nsFont(.body)
        subtitleLabel.font = DS.nsFont(.chrome)
        metaLabel.font = DS.nsFont(.meta)
        for l in [titleLabel, subtitleLabel, metaLabel] { l.lineBreakMode = .byTruncatingTail; l.cell?.truncatesLastVisibleLine = true }
        titleLabel.stringValue = title
        subtitleLabel.stringValue = subtitle ?? ""
        subtitleLabel.isHidden = subtitle == nil
        metaLabel.stringValue = meta ?? ""
        metaLabel.isHidden = meta == nil
        kbdView.keys = kbd ?? ""
        kbdView.isHidden = kbd == nil
        markView.isHidden = mark == nil
        if let mark { markView.kind = mark }
        [markView, titleLabel, subtitleLabel, metaLabel, kbdView].forEach(addSubview)
        setAccessibilityElement(true)
        setAccessibilityRole(.row)
        applyTheme()
    }

    @available(*, unavailable)
    required init?(coder: NSCoder) { fatalError() }

    override var isFlipped: Bool { true }
    override var intrinsicContentSize: NSSize { NSSize(width: NSView.noIntrinsicMetric, height: Row.height(subtitle: subtitle != nil)) }

    override func layout() {
        super.layout()
        let pad = DS.Spacing.m, gap = DS.Spacing.m
        var x = pad
        var right = bounds.width - pad
        if !markView.isHidden {
            let s = markView.side
            markView.frame = NSRect(x: x, y: ((bounds.height - s) / 2).rounded(), width: s, height: s)
            x += s + gap
        }
        if !kbdView.isHidden {
            let s = kbdView.intrinsicContentSize
            kbdView.frame = NSRect(x: right - s.width, y: ((bounds.height - s.height) / 2).rounded(), width: s.width, height: s.height)
            right -= s.width + gap
        }
        if !metaLabel.isHidden {
            let s = metaLabel.intrinsicContentSize
            let w = min(s.width.rounded(.up), max(0, (right - x) / 2))
            metaLabel.frame = NSRect(x: right - w, y: ((bounds.height - s.height) / 2).rounded(), width: w, height: s.height)
            right -= w + gap
        }
        let th = titleLabel.intrinsicContentSize.height
        if subtitleLabel.isHidden {
            titleLabel.frame = NSRect(x: x, y: ((bounds.height - th) / 2).rounded(), width: max(0, right - x), height: th)
        } else {
            let sh = subtitleLabel.intrinsicContentSize.height
            let top = ((bounds.height - th - sh - DS.Spacing.xxs) / 2).rounded()
            titleLabel.frame = NSRect(x: x, y: top, width: max(0, right - x), height: th)
            subtitleLabel.frame = NSRect(x: x, y: top + th + DS.Spacing.xxs, width: max(0, right - x), height: sh)
        }
        setAccessibilityLabel([mark?.label, title, subtitle, meta].compactMap { $0 }.joined(separator: ", "))
    }

    override func updateTrackingAreas() {
        super.updateTrackingAreas()
        if let tracking { removeTrackingArea(tracking) }
        let t = NSTrackingArea(rect: bounds, options: [.mouseEnteredAndExited, .activeInActiveApp, .inVisibleRect], owner: self)
        addTrackingArea(t)
        tracking = t
    }

    override func mouseEntered(with event: NSEvent) { hovering = true }
    override func mouseExited(with event: NSEvent) { hovering = false }

    override func viewDidChangeEffectiveAppearance() {
        super.viewDidChangeEffectiveAppearance()
        applyTheme()
    }

    private func applyTheme() {
        titleLabel.textColor = Theme.ns(.text)
        subtitleLabel.textColor = Theme.ns(.dim)
        metaLabel.textColor = Theme.ns(.dim)
        let bg: NSColor = isSelected ? Theme.ns(.working, alpha: 0.2) : hovering ? Theme.ns(.line, alpha: 0.6) : .clear
        layer?.backgroundColor = bg.cg(in: self)
    }
}
