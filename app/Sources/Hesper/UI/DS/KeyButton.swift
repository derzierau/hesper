import AppKit
import HesperCore
import SwiftUI

/// KeyButton: a Pill-sized button with a label and a trailing Kbd, the
/// control every footer hint, strip hint and header label becomes
/// (`KeyButton("Allow", kbd: "⏎") { … }`). Radius `control`, height
/// `Pill.height`.
/// - primary(tone): tinted like a `.status` Pill with the mark's tone at
///   14% (24% on hover), text in `fg`; the surface's default verb.
/// - quiet: a 1 px `line` hairline, `line` fill on hover; the other verbs.
/// - icon(symbol): an IconButton whose tooltip carries the key
///   ("Allow (⏎)"); the label is the tooltip.
/// Disabled buttons draw at 40%. SwiftUI `KeyButton`; AppKit `KeyButtonView`.
struct KeyButton: View {
    enum Variant: Equatable {
        case primary(StateMarkKind.Tone)
        case quiet
        case icon(String)
    }

    var label: String
    var kbd: String?
    var variant: Variant = .quiet
    var action: () -> Void

    init(_ label: String, kbd: String? = nil, variant: Variant = .quiet, action: @escaping () -> Void) {
        self.label = label; self.kbd = kbd; self.variant = variant; self.action = action
    }

    static let height = Pill.height
    static let disabledOpacity = 0.4

    @State private var hover = false
    @Environment(\.isEnabled) private var enabled
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    var body: some View {
        if case .icon(let symbol) = variant {
            IconButton(symbol, help: label, shortcut: kbd, action: action)
                .opacity(enabled ? 1 : Self.disabledOpacity)
        } else {
            Button(action: action) {
                HStack(spacing: DS.Spacing.s) {
                    Text(label).font(DS.font(.chrome, .medium)).foregroundStyle(Theme.fg)
                    if let kbd { Kbd(kbd) }
                }
                .lineLimit(1)
                .padding(.horizontal, DS.Spacing.m)
                .frame(height: Self.height)
                .background(DS.Radius.shape(DS.Radius.control).fill(fill))
                .overlay(DS.Radius.shape(DS.Radius.control).strokeBorder(variant == .quiet ? Theme.stroke : .clear, lineWidth: 1))
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .fixedSize()
            .opacity(enabled ? 1 : Self.disabledOpacity)
            .onHover { h in DS.withMotion(.quick, reduceMotion: reduceMotion) { hover = h } }
            .help(kbd.map { "\(label) (\($0))" } ?? label)
            .accessibilityLabel(label)
        }
    }

    private var fill: Color {
        switch variant {
        case .primary(let tone): return Color(nsColor: Theme.ns(Theme.token(tone), alpha: hover ? 0.24 : 0.14))
        case .quiet: return hover ? Theme.chipBG : .clear
        case .icon: return .clear
        }
    }
}

/// AppKit KeyButton: the same look (label, trailing KbdView, hover fill);
/// an `.icon` variant is an IconButtonView.
@MainActor
final class KeyButtonView: NSControl {
    var label: String { didSet { labelField.stringValue = label; toolTip = tooltip; setAccessibilityLabel(label); invalidateIntrinsicContentSize() } }
    var kbd: String? { didSet { kbdView.keys = kbd ?? ""; kbdView.isHidden = kbd == nil; toolTip = tooltip; invalidateIntrinsicContentSize() } }
    var variant: KeyButton.Variant { didSet { applyTheme() } }
    var onPress: (() -> Void)?
    private var hovering = false { didSet { applyTheme() } }
    private var tracking: NSTrackingArea?

    private let labelField = NSTextField(labelWithString: "")
    private let kbdView = KbdView("")
    private var icon: IconButtonView?

    init(_ label: String, kbd: String? = nil, variant: KeyButton.Variant = .quiet, onPress: (() -> Void)? = nil) {
        self.label = label; self.kbd = kbd; self.variant = variant; self.onPress = onPress
        super.init(frame: .zero)
        wantsLayer = true
        DS.Radius.apply(DS.Radius.control, to: layer)
        layer?.borderWidth = 1
        labelField.font = DS.nsFont(.chrome, .medium)
        labelField.stringValue = label
        labelField.lineBreakMode = .byClipping
        kbdView.keys = kbd ?? ""
        kbdView.isHidden = kbd == nil
        addSubview(labelField)
        addSubview(kbdView)
        if case .icon(let symbol) = variant {
            let b = IconButtonView(symbol: symbol, help: label, shortcut: kbd, target: self, action: #selector(pressed))
            addSubview(b)
            icon = b
            labelField.isHidden = true
            kbdView.isHidden = true
        }
        toolTip = tooltip
        setAccessibilityElement(true)
        setAccessibilityRole(.button)
        setAccessibilityLabel(label)
        applyTheme()
    }

    @available(*, unavailable)
    required init?(coder: NSCoder) { fatalError() }

    private var tooltip: String { kbd.map { "\(label) (\($0))" } ?? label }

    /// The text's width with the label's 2 pt insets (a clipping label
    /// cuts its last glyph without them).
    private var labelWidth: CGFloat { ceil(labelField.attributedStringValue.size().width) + 2 * DS.Spacing.xxs }

    override var intrinsicContentSize: NSSize {
        if icon != nil { return NSSize(width: IconButton.side, height: IconButton.side) }
        var w = labelWidth + 2 * DS.Spacing.m
        if kbd != nil { w += DS.Spacing.s + kbdView.intrinsicContentSize.width }
        return NSSize(width: w, height: KeyButton.height)
    }

    override func layout() {
        super.layout()
        if let icon { icon.frame = bounds; return }
        let l = labelField.intrinsicContentSize
        let lw = labelWidth
        labelField.frame = NSRect(x: DS.Spacing.m, y: ((bounds.height - l.height) / 2).rounded(), width: lw, height: l.height)
        let k = kbdView.intrinsicContentSize
        kbdView.frame = NSRect(x: DS.Spacing.m + lw + DS.Spacing.s, y: ((bounds.height - k.height) / 2).rounded(), width: k.width, height: k.height)
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
    override func mouseUp(with event: NSEvent) {
        guard isEnabled, bounds.contains(convert(event.locationInWindow, from: nil)) else { return }
        pressed()
    }

    @objc private func pressed() {
        onPress?()
        if let action { NSApp.sendAction(action, to: target, from: self) }
    }

    override func accessibilityPerformPress() -> Bool { pressed(); return true }

    override var isEnabled: Bool { didSet { applyTheme() } }

    override func viewDidChangeEffectiveAppearance() {
        super.viewDidChangeEffectiveAppearance()
        applyTheme()
    }

    private func applyTheme() {
        alphaValue = isEnabled ? 1 : KeyButton.disabledOpacity
        labelField.textColor = Theme.ns(.text)
        switch variant {
        case .primary(let tone):
            layer?.backgroundColor = Theme.ns(Theme.token(tone), alpha: hovering ? 0.24 : 0.14).cg(in: self)
            layer?.borderColor = NSColor.clear.cgColor
        case .quiet:
            layer?.backgroundColor = hovering ? Theme.ns(.line).cg(in: self) : NSColor.clear.cgColor
            layer?.borderColor = Theme.ns(.line).cg(in: self)
        case .icon:
            layer?.backgroundColor = NSColor.clear.cgColor
            layer?.borderColor = NSColor.clear.cgColor
        }
    }
}
