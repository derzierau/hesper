import AppKit
import SwiftUI

/// Panel: the floating glass container (popovers, sheets, the palette).
/// Material + blur, radius `panel` (continuous), a 1 px `line` hairline and
/// the one soft shadow (`DS.Elevation.floating`). Under Reduce
/// Transparency it is solid `surface`. Glass only for floating layers:
/// tiles and terminals stay solid.
/// SwiftUI `Panel { … }` (or `.dsPanel()` on any view); AppKit `PanelView`
/// (add content to `contentView`).
struct Panel<Content: View>: View {
    var padding: CGFloat = DS.Spacing.l
    @ViewBuilder var content: Content

    init(padding: CGFloat = DS.Spacing.l, @ViewBuilder content: () -> Content) {
        self.padding = padding
        self.content = content()
    }

    var body: some View {
        content.padding(padding).dsPanel()
    }
}

struct PanelBackground: ViewModifier {
    /// A modal sheet (⌘K, ⌘Y) over a scrim: an opaque `sheet` surface, so
    /// nothing behind it is ever legible.
    var opaque = false
    @Environment(\.accessibilityReduceTransparency) private var reduceTransparency

    func body(content: Content) -> some View {
        let shape = DS.Radius.shape(DS.Radius.panel)
        let s = DS.Elevation.floating.shadow!
        return content
            .background {
                if opaque {
                    shape.fill(Color(nsColor: PanelView.sheetColor))
                } else if reduceTransparency {
                    shape.fill(Theme.surface)
                } else {
                    shape.fill(.regularMaterial)
                }
            }
            .clipShape(shape)
            .overlay(shape.strokeBorder(Theme.stroke, lineWidth: 1))
            .shadow(color: .black.opacity(Double(s.opacity)), radius: s.radius / 2, y: s.y)
    }
}

extension View {
    /// The floating Panel look on any view.
    func dsPanel() -> some View { modifier(PanelBackground()) }
    /// The modal sheet look (opaque; goes over a scrim).
    func dsSheet() -> some View { modifier(PanelBackground(opaque: true)) }
}

/// AppKit Panel: a container view whose layer carries the shadow, holding
/// an NSVisualEffectView (masked to the continuous rounded rect), or a
/// solid `surface` layer under Reduce Transparency. Put content in
/// `contentView`.
@MainActor
final class PanelView: NSView {
    let contentView = NSView()
    private let effect = NSVisualEffectView()
    private let solid = NSView()
    private let border = CAShapeLayer()
    /// Injectable for tests and the gallery; defaults to the system setting.
    var reduceTransparency: () -> Bool = { DS.reduceTransparency } { didSet { applyTheme() } }
    /// A modal sheet over a scrim: opaque (`sheetColor`), no material.
    var isSheet = false { didSet { applyTheme() } }

    /// The modal sheet's surface: `surface` in Dusk, white (`tile`) in Daylight.
    static let sheetColor: NSColor = {
        let dusk = Theme.ns(.surface), day = Theme.ns(.tile)
        return NSColor(name: nil) { Theme.scheme($0) == .daylight ? day : dusk }
    }()

    override init(frame: NSRect) {
        super.init(frame: frame)
        wantsLayer = true
        layer?.masksToBounds = false
        effect.material = .popover
        effect.blendingMode = .withinWindow
        effect.state = .active
        effect.wantsLayer = true
        DS.Radius.apply(DS.Radius.panel, to: effect.layer, masks: true)
        solid.wantsLayer = true
        DS.Radius.apply(DS.Radius.panel, to: solid.layer, masks: true)
        contentView.wantsLayer = true
        DS.Radius.apply(DS.Radius.panel, to: contentView.layer, masks: true)
        addSubview(solid)
        addSubview(effect)
        addSubview(contentView)
        border.fillColor = nil
        border.lineWidth = 1
        layer?.addSublayer(border)
        NSWorkspace.shared.notificationCenter.addObserver(self, selector: #selector(accessibilityChanged),
                                                          name: NSWorkspace.accessibilityDisplayOptionsDidChangeNotification, object: nil)
        applyTheme()
    }

    @available(*, unavailable)
    required init?(coder: NSCoder) { fatalError() }

    @objc private func accessibilityChanged() { applyTheme() }

    override func layout() {
        super.layout()
        for v in [solid, effect, contentView] { v.frame = bounds }
        let r = DS.Radius.panel
        border.frame = bounds
        border.path = CGPath(roundedRect: bounds.insetBy(dx: 0.5, dy: 0.5), cornerWidth: r, cornerHeight: r, transform: nil)
        border.zPosition = 10
        DS.Elevation.floating.applyShadow(to: layer, radius: r)
    }

    override func viewDidChangeEffectiveAppearance() {
        super.viewDidChangeEffectiveAppearance()
        applyTheme()
    }

    private func applyTheme() {
        let solidOnly = isSheet || reduceTransparency()
        effect.isHidden = solidOnly
        solid.layer?.backgroundColor = (isSheet ? Self.sheetColor : Theme.ns(.surface)).cg(in: self)
        border.strokeColor = Theme.ns(.line).cg(in: self)
    }
}
