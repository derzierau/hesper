import AppKit
import SwiftUI

/// IconButton: an SF Symbol in a 24 pt hit area, a `line` fill on hover
/// (radius `control`), a tooltip that names the action and its shortcut
/// ("Search (⌘K)"). SwiftUI `IconButton("magnifyingglass", help: "Search",
/// shortcut: "⌘K") { … }`; AppKit `IconButtonView`.
struct IconButton: View {
    var symbol: String
    var help: String
    var shortcut: String? = nil
    var action: () -> Void

    init(_ symbol: String, help: String, shortcut: String? = nil, action: @escaping () -> Void) {
        self.symbol = symbol; self.help = help; self.shortcut = shortcut; self.action = action
    }

    static let side: CGFloat = 24
    static let symbolSize: CGFloat = 12
    static func tooltip(_ help: String, _ shortcut: String?) -> String { shortcut.map { "\(help) (\($0))" } ?? help }

    @State private var hover = false
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    var body: some View {
        Button(action: action) {
            Image(systemName: symbol)
                .font(.system(size: Self.symbolSize, weight: .medium))
                .foregroundStyle(hover ? Theme.fg : Theme.fg2)
                .frame(width: Self.side, height: Self.side)
                .background(DS.Radius.shape(DS.Radius.control).fill(hover ? Theme.chipBG : .clear))
                .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .onHover { h in DS.withMotion(.quick, reduceMotion: reduceMotion) { hover = h } }
        .help(Self.tooltip(help, shortcut))
        .accessibilityLabel(help)
    }
}

/// AppKit IconButton: borderless, 24 × 24, hover fill, tooltip.
@MainActor
final class IconButtonView: NSButton {
    private var hovering = false { didSet { applyTheme() } }
    private var tracking: NSTrackingArea?

    init(symbol: String, help: String, shortcut: String? = nil, target: AnyObject?, action: Selector?) {
        super.init(frame: NSRect(x: 0, y: 0, width: IconButton.side, height: IconButton.side))
        let cfg = NSImage.SymbolConfiguration(pointSize: IconButton.symbolSize, weight: .medium)
        image = NSImage(systemSymbolName: symbol, accessibilityDescription: help)?.withSymbolConfiguration(cfg)
        imagePosition = .imageOnly
        isBordered = false
        bezelStyle = .regularSquare
        self.target = target
        self.action = action
        toolTip = IconButton.tooltip(help, shortcut)
        setAccessibilityLabel(help)
        wantsLayer = true
        DS.Radius.apply(DS.Radius.control, to: layer)
        applyTheme()
    }

    @available(*, unavailable)
    required init?(coder: NSCoder) { fatalError() }

    override var intrinsicContentSize: NSSize { NSSize(width: IconButton.side, height: IconButton.side) }

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
        contentTintColor = Theme.ns(hovering ? .text : .text2)
        layer?.backgroundColor = hovering ? Theme.ns(.line).cg(in: self) : NSColor.clear.cgColor
    }
}
