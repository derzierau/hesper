import AppKit
import SwiftUI

/// Kbd: a shortcut hint ("⌘K"): 10.5 pt, a 1 px `line` border, radius
/// `kbd`, dim text. Letters and digits in Geist Mono; the key symbols
/// (⌘ ⌥ ⇧ ⌃ ⏎ ⇥ ⌫ ↑ ↓ …), which Geist Mono doesn't have, in the system
/// font at the same size (medium), on one baseline. A single key is at
/// least as wide as it is tall. SwiftUI `Kbd("⌘K")`; AppKit `KbdView("⌘K")`.
struct Kbd: View {
    var keys: String
    init(_ keys: String) { self.keys = keys }

    static let fontSize: CGFloat = 10.5
    static var font: Font { .geistMono(fontSize, .medium) }
    static var nsFont: NSFont { .geistMono(ofSize: fontSize, weight: .medium) }
    /// The key symbols' font (Geist Mono has none of them).
    static var symbolFont: Font { .system(size: fontSize, weight: .medium) }
    static var nsSymbolFont: NSFont { .systemFont(ofSize: fontSize, weight: .medium) }
    /// Horizontal / vertical padding inside the border.
    static let padH: CGFloat = 5
    static let padV: CGFloat = 1
    /// The text line's height (both fonts on one baseline) and the box's.
    static var lineHeight: CGFloat {
        let m = nsFont, s = nsSymbolFont
        return ceil(max(m.ascender, s.ascender) - min(m.descender, s.descender))
    }
    static var boxHeight: CGFloat { lineHeight + 2 * padV }

    /// The characters drawn with the system font.
    static let symbols: Set<Character> = ["⌘", "⌥", "⇧", "⌃", "⏎", "↩", "↵", "⇥", "⇤", "⌫", "⌦", "⎋", "⇪", "␣",
                                          "↑", "↓", "←", "→", "⇞", "⇟", "↖", "↘", "·", "⏏", "⌄", "⌅"]
    static func isSymbol(_ c: Character) -> Bool { symbols.contains(c) }

    /// Runs of the same font: (text, symbol?).
    static func runs(_ keys: String) -> [(String, Bool)] {
        var out: [(String, Bool)] = []
        for c in keys {
            let sym = isSymbol(c)
            if let last = out.last, last.1 == sym { out[out.count - 1].0.append(c) } else { out.append((String(c), sym)) }
        }
        return out
    }

    /// AppKit: the keys as one attributed string (both fonts, one color).
    static func attributed(_ keys: String, color: NSColor) -> NSAttributedString {
        let a = NSMutableAttributedString()
        // The symbols sit a touch high in SF next to Mono caps: center them on the caps.
        let shift = ((nsFont.capHeight - nsSymbolFont.capHeight) / 2).rounded()
        for (t, sym) in runs(keys) {
            var attrs: [NSAttributedString.Key: Any] = [.font: sym ? nsSymbolFont : nsFont, .foregroundColor: color]
            if sym && shift != 0 { attrs[.baselineOffset] = shift }
            a.append(NSAttributedString(string: t, attributes: attrs))
        }
        return a
    }

    /// SwiftUI: the same runs.
    static func attributedText(_ keys: String) -> AttributedString {
        var a = AttributedString()
        for (t, sym) in runs(keys) {
            var r = AttributedString(t)
            r.font = sym ? symbolFont : font
            a += r
        }
        return a
    }

    var body: some View {
        Text(Self.attributedText(keys))
            .foregroundStyle(Theme.dim)
            .lineLimit(1)
            .padding(.horizontal, Self.padH)
            .frame(minWidth: Self.boxHeight)
            .frame(height: Self.boxHeight)
            .overlay(DS.Radius.shape(DS.Radius.kbd).strokeBorder(Theme.stroke, lineWidth: 1))
            .fixedSize()
            .accessibilityLabel("shortcut \(keys)")
    }
}

/// AppKit Kbd.
@MainActor
final class KbdView: NSView {
    var keys: String {
        didSet {
            guard keys != oldValue else { return }
            applyText()
            invalidateIntrinsicContentSize()
            needsLayout = true
            setAccessibilityLabel("shortcut \(keys)")
        }
    }
    /// Text and border in this color instead of `dim` / `line` (a Kbd on a
    /// filled button).
    var tint: NSColor? { didSet { applyTheme() } }
    private let label = NSTextField(labelWithString: "")

    init(_ keys: String) {
        self.keys = keys
        super.init(frame: .zero)
        wantsLayer = true
        DS.Radius.apply(DS.Radius.kbd, to: layer)
        layer?.borderWidth = 1
        label.alignment = .center
        label.lineBreakMode = .byClipping
        addSubview(label)
        applyText()
        applyTheme()
        setAccessibilityElement(true)
        setAccessibilityLabel("shortcut \(keys)")
    }

    @available(*, unavailable)
    required init?(coder: NSCoder) { fatalError() }

    private func applyText() { label.attributedStringValue = Kbd.attributed(keys, color: tint ?? Theme.ns(.dim)) }

    private var textWidth: CGFloat { ceil(label.attributedStringValue.size().width) }

    override var intrinsicContentSize: NSSize {
        let h = Kbd.boxHeight
        return NSSize(width: max(h, textWidth + 2 * Kbd.padH), height: h)
    }

    override func layout() {
        super.layout()
        let s = label.intrinsicContentSize
        label.frame = NSRect(x: 0, y: ((bounds.height - s.height) / 2).rounded(), width: bounds.width, height: s.height)
    }

    override func viewDidChangeEffectiveAppearance() {
        super.viewDidChangeEffectiveAppearance()
        applyTheme()
    }

    private func applyTheme() {
        layer?.borderColor = (tint?.withAlphaComponent(0.45) ?? Theme.ns(.line)).cg(in: self)
        applyText()
    }
}
