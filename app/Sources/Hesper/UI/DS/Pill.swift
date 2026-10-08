import AppKit
import HesperCore
import SwiftUI

/// Pill: a capsule-ish chip with radius `control`, height ≤ 22.
/// - segment: one option of a segmented scope (selected: `line` fill, text;
///   else dim text, no fill).
/// - status: a state or fact ("2 need you"), optional leading StateMark;
///   tinted with the mark's tone at 14% when it has one.
/// - count: a number in Geist Mono (dim on `line`).
/// SwiftUI `Pill("All", variant: .segment(selected: true), count: 7)`;
/// AppKit `PillView`.
struct Pill: View {
    enum Variant: Equatable {
        case segment(selected: Bool)
        case status
        case count
    }

    var title: String
    var variant: Variant = .status
    var mark: StateMarkKind? = nil
    var count: Int? = nil
    var kbd: String? = nil

    init(_ title: String, variant: Variant = .status, mark: StateMarkKind? = nil, count: Int? = nil, kbd: String? = nil) {
        self.title = title; self.variant = variant; self.mark = mark; self.count = count; self.kbd = kbd
    }

    static let height: CGFloat = 22

    var body: some View {
        HStack(spacing: DS.Spacing.s) {
            if let mark { StateMark(mark) }
            if !title.isEmpty {
                Text(title)
                    .font(variant == .count ? DS.font(.meta, .medium) : DS.font(.chrome, .medium))
                    .foregroundStyle(foreground)
            }
            if let count { Text("\(count)").font(DS.font(.meta)).foregroundStyle(Theme.dim) }
            if let kbd { Kbd(kbd) }
        }
        .lineLimit(1)
        .padding(.horizontal, DS.Spacing.m)
        .frame(height: Self.height)
        .background(DS.Radius.shape(DS.Radius.control).fill(fill))
        .fixedSize()
    }

    private var foreground: Color {
        switch variant {
        case .segment(let selected): return selected ? Theme.fg : Theme.dim
        case .status: return Theme.fg
        case .count: return Theme.dim
        }
    }

    private var fill: Color {
        switch variant {
        case .segment(let selected): return selected ? Theme.chipBG : .clear
        case .count: return Theme.chipBG
        case .status:
            if let mark { return Color(nsColor: Theme.ns(Theme.token(mark.tone), alpha: 0.14)) }
            return Theme.chipBG
        }
    }
}

/// AppKit Pill: the same look as a view (leading StateMarkView, label,
/// optional count and KbdView).
@MainActor
final class PillView: NSView {
    var title: String { didSet { label.stringValue = title; needsLayout = true; invalidateIntrinsicContentSize() } }
    var variant: Pill.Variant { didSet { applyTheme() } }
    var mark: StateMarkKind? { didSet { markView.isHidden = mark == nil; if let mark { markView.kind = mark }; applyTheme(); invalidateIntrinsicContentSize() } }
    var count: Int? { didSet { countLabel.stringValue = count.map(String.init) ?? ""; countLabel.isHidden = count == nil; invalidateIntrinsicContentSize() } }

    private let markView = StateMarkView()
    private let label = NSTextField(labelWithString: "")
    private let countLabel = NSTextField(labelWithString: "")

    init(_ title: String, variant: Pill.Variant = .status, mark: StateMarkKind? = nil, count: Int? = nil) {
        self.title = title; self.variant = variant; self.mark = mark; self.count = count
        super.init(frame: .zero)
        wantsLayer = true
        DS.Radius.apply(DS.Radius.control, to: layer)
        label.stringValue = title
        countLabel.font = DS.nsFont(.meta)
        countLabel.stringValue = count.map(String.init) ?? ""
        countLabel.isHidden = count == nil
        markView.isHidden = mark == nil
        if let mark { markView.kind = mark }
        [markView, label, countLabel].forEach(addSubview)
        applyTheme()
    }

    @available(*, unavailable)
    required init?(coder: NSCoder) { fatalError() }

    private var parts: [NSView] { [markView, label, countLabel].filter { !$0.isHidden } }

    /// A part's width: labels by their text (an NSTextField's cached
    /// intrinsic size could be from before its font was set).
    private func width(_ v: NSView) -> CGFloat {
        if let l = v as? NSTextField { return ceil(l.attributedStringValue.size().width) + 2 * DS.Spacing.xxs }
        return v.intrinsicContentSize.width.rounded(.up)
    }

    override var intrinsicContentSize: NSSize {
        let widths = parts.map { width($0) }
        let w = widths.reduce(0, +) + CGFloat(max(0, widths.count - 1)) * DS.Spacing.s + 2 * DS.Spacing.m
        return NSSize(width: w, height: Pill.height)
    }

    override func layout() {
        super.layout()
        var x = DS.Spacing.m
        for v in parts {
            let s = v.intrinsicContentSize, w = width(v)
            v.frame = NSRect(x: x, y: ((bounds.height - s.height) / 2).rounded(), width: w, height: s.height)
            x += w + DS.Spacing.s
        }
    }

    override func viewDidChangeEffectiveAppearance() {
        super.viewDidChangeEffectiveAppearance()
        applyTheme()
    }

    private func applyTheme() {
        let fg: NSColor, bg: NSColor
        switch variant {
        case .segment(let selected):
            fg = Theme.ns(selected ? .text : .dim); bg = selected ? Theme.ns(.line) : .clear
        case .count:
            fg = Theme.ns(.dim); bg = Theme.ns(.line)
        case .status:
            fg = Theme.ns(.text); bg = mark.map { Theme.ns(Theme.token($0.tone), alpha: 0.14) } ?? Theme.ns(.line)
        }
        label.font = variant == .count ? DS.nsFont(.meta, .medium) : DS.nsFont(.chrome, .medium)
        label.textColor = fg
        invalidateIntrinsicContentSize()
        countLabel.textColor = Theme.ns(.dim)
        layer?.backgroundColor = bg.cg(in: self)
    }
}
