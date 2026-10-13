import AppKit
import HesperCore
import SwiftUI

/// SheetChrome: the frame History, Review and Routines share inside their
/// Panel. A title row (StateMark or SF Symbol, title in `panelTitle`
/// semibold, a count Pill, then the `esc` key cap and a close ×
/// IconButton), a toolbar row of KeyButtons for the sheet's verbs, the
/// content, and a footer row of KeyButtons for the row-level keys. Rows
/// are separated by 1 px `line` hairlines; the toolbar and footer are
/// left out when empty. Closing is the host's (`onClose`; esc and the
/// scrim click stay with its key routing). SwiftUI `SheetChrome`; AppKit
/// `SheetChromeView` (add content to `contentView`, verbs to `toolbar` and
/// `footer`).
struct SheetChrome<Toolbar: View, Content: View, Footer: View>: View {
    var title: String
    var mark: StateMarkKind?
    var symbol: String?
    var count: Int?
    var toolbar: Toolbar
    var content: Content
    var footer: Footer
    var onClose: () -> Void

    init(title: String, mark: StateMarkKind? = nil, symbol: String? = nil, count: Int? = nil,
         @ViewBuilder toolbar: () -> Toolbar = { EmptyView() }, @ViewBuilder footer: () -> Footer = { EmptyView() },
         onClose: @escaping () -> Void, @ViewBuilder content: () -> Content) {
        self.title = title; self.mark = mark; self.symbol = symbol; self.count = count
        self.toolbar = toolbar(); self.footer = footer(); self.content = content(); self.onClose = onClose
    }

    var body: some View {
        VStack(spacing: 0) {
            HStack(spacing: DS.Spacing.m) {
                if let mark { StateMark(mark) } else if let symbol {
                    Image(systemName: symbol).font(.system(size: IconButton.symbolSize, weight: .medium)).foregroundStyle(Theme.fg2)
                }
                Text(title).font(DS.font(.panelTitle, .semibold)).foregroundStyle(Theme.fg).lineLimit(1)
                if let count { Pill("", variant: .count, count: count) }
                Spacer(minLength: DS.Spacing.m)
                Kbd(SheetChromeView.closeKey)
                IconButton("xmark", help: "Close", shortcut: SheetChromeView.closeKey, action: onClose)
            }
            .padding(.horizontal, DS.Spacing.l)
            .frame(height: SheetChromeView.titleHeight)
            if Toolbar.self != EmptyView.self {
                hairline
                HStack(spacing: DS.Spacing.s) { toolbar; Spacer(minLength: 0) }
                    .padding(.horizontal, DS.Spacing.l)
                    .frame(height: SheetChromeView.barHeight)
            }
            hairline
            content.frame(maxWidth: .infinity, maxHeight: .infinity)
            if Footer.self != EmptyView.self {
                hairline
                HStack(spacing: DS.Spacing.s) { footer; Spacer(minLength: 0) }
                    .padding(.horizontal, DS.Spacing.l)
                    .frame(height: SheetChromeView.barHeight)
            }
        }
        .accessibilityElement(children: .contain)
        .accessibilityLabel(title)
    }

    private var hairline: some View { Rectangle().fill(Theme.stroke).frame(height: 1) }
}

/// AppKit SheetChrome: title row, `toolbar` and `footer` stacks (hidden
/// while empty), `contentView` between the hairlines.
@MainActor
final class SheetChromeView: NSView {
    static let titleHeight: CGFloat = 44
    static let barHeight: CGFloat = 36
    /// The key cap next to the close ×.
    static let closeKey = "esc"

    var title: String { didSet { titleLabel.stringValue = title; setAccessibilityLabel(title) } }
    var mark: StateMarkKind? { didSet { markView.isHidden = mark == nil; if let mark { markView.kind = mark }; needsLayout = true } }
    var symbol: String? { didSet { applySymbol(); needsLayout = true } }
    var count: Int? { didSet { countPill.count = count; countPill.isHidden = count == nil; needsLayout = true } }
    var onClose: (() -> Void)?

    let contentView = NSView()
    /// KeyButtonViews for the sheet's verbs (left to right).
    let toolbar = NSStackView()
    /// KeyButtonViews for the row-level keys.
    let footer = NSStackView()

    private let markView = StateMarkView()
    private let symbolView = NSImageView()
    private let titleLabel = NSTextField(labelWithString: "")
    private let countPill = PillView("", variant: .count)
    private let escKey = KbdView(SheetChromeView.closeKey)
    /// The close × (the host names it for its tests).
    private(set) lazy var closeButton = IconButtonView(symbol: "xmark", help: "Close", shortcut: SheetChromeView.closeKey,
                                                       target: self, action: #selector(closePressed))
    private let lines = (top: NSView(), toolbar: NSView(), footer: NSView())

    init(title: String, mark: StateMarkKind? = nil, symbol: String? = nil, count: Int? = nil, onClose: (() -> Void)? = nil) {
        self.title = title; self.mark = mark; self.symbol = symbol; self.count = count; self.onClose = onClose
        super.init(frame: .zero)
        wantsLayer = true
        titleLabel.font = DS.nsFont(.panelTitle, .semibold)
        titleLabel.stringValue = title
        titleLabel.lineBreakMode = .byTruncatingTail
        markView.isHidden = mark == nil
        if let mark { markView.kind = mark }
        applySymbol()
        countPill.count = count
        countPill.isHidden = count == nil
        for s in [toolbar, footer] {
            s.orientation = .horizontal
            s.spacing = DS.Spacing.s
            s.alignment = .centerY
        }
        for l in [lines.top, lines.toolbar, lines.footer] { l.wantsLayer = true }
        [markView, symbolView, titleLabel, countPill, escKey, closeButton, lines.top, toolbar, lines.toolbar, contentView, lines.footer, footer]
            .forEach(addSubview)
        setAccessibilityElement(true)
        setAccessibilityRole(.group)
        setAccessibilityLabel(title)
        applyTheme()
    }

    @available(*, unavailable)
    required init?(coder: NSCoder) { fatalError() }

    @objc private func closePressed() { onClose?() }

    private func applySymbol() {
        symbolView.isHidden = symbol == nil || mark != nil
        guard let symbol else { return }
        let cfg = NSImage.SymbolConfiguration(pointSize: IconButton.symbolSize, weight: .medium)
        symbolView.image = NSImage(systemSymbolName: symbol, accessibilityDescription: nil)?.withSymbolConfiguration(cfg)
        symbolView.contentTintColor = Theme.ns(.text2)
    }

    private var hasToolbar: Bool { !toolbar.arrangedSubviews.isEmpty }
    private var hasFooter: Bool { !footer.arrangedSubviews.isEmpty }

    override func layout() {
        super.layout()
        let th = Self.titleHeight, bh = Self.barHeight
        let l = DS.Spacing.l, m = DS.Spacing.m
        // Title row (AppKit's y grows upward: the title sits at the top).
        var y = bounds.height - th
        var x = l
        func place(_ v: NSView, width w: CGFloat? = nil) {
            guard !v.isHidden else { return }
            let s = v.intrinsicContentSize
            let width = w ?? s.width
            v.frame = NSRect(x: x, y: y + ((th - s.height) / 2).rounded(), width: width, height: s.height)
            x += width + m
        }
        place(markView)
        place(symbolView, width: IconButton.symbolSize + 2)
        let right = bounds.width - l
        var rx = right
        for v in [closeButton, escKey] as [NSView] {
            let s = v.intrinsicContentSize
            rx -= s.width
            v.frame = NSRect(x: rx, y: y + ((th - s.height) / 2).rounded(), width: s.width, height: s.height)
            rx -= m
        }
        let countW = countPill.isHidden ? 0 : countPill.intrinsicContentSize.width + m
        let titleW = max(0, rx - x - countW)
        let ts = titleLabel.intrinsicContentSize
        // The text's width with the label's 2 pt insets (the intrinsic
        // width of a truncating label comes up short).
        let textW = ceil(titleLabel.attributedStringValue.size().width) + 2 * DS.Spacing.xxs
        titleLabel.frame = NSRect(x: x, y: y + ((th - ts.height) / 2).rounded(), width: min(titleW, textW), height: ts.height)
        x = titleLabel.frame.maxX + m
        place(countPill)
        lines.top.frame = NSRect(x: 0, y: y - 1, width: bounds.width, height: 1)
        y -= 1
        // Toolbar.
        toolbar.isHidden = !hasToolbar
        lines.toolbar.isHidden = !hasToolbar
        if hasToolbar {
            toolbar.frame = NSRect(x: l, y: y - bh, width: bounds.width - 2 * l, height: bh)
            lines.toolbar.frame = NSRect(x: 0, y: y - bh - 1, width: bounds.width, height: 1)
            y -= bh + 1
        }
        // Footer, from the bottom.
        footer.isHidden = !hasFooter
        lines.footer.isHidden = !hasFooter
        var bottom: CGFloat = 0
        if hasFooter {
            footer.frame = NSRect(x: l, y: 0, width: bounds.width - 2 * l, height: bh)
            lines.footer.frame = NSRect(x: 0, y: bh, width: bounds.width, height: 1)
            bottom = bh + 1
        }
        contentView.frame = NSRect(x: 0, y: bottom, width: bounds.width, height: max(0, y - bottom))
    }

    override func viewDidChangeEffectiveAppearance() {
        super.viewDidChangeEffectiveAppearance()
        applyTheme()
    }

    private func applyTheme() {
        titleLabel.textColor = Theme.ns(.text)
        symbolView.contentTintColor = Theme.ns(.text2)
        for l in [lines.top, lines.toolbar, lines.footer] { l.layer?.backgroundColor = Theme.ns(.line).cg(in: self) }
    }
}
