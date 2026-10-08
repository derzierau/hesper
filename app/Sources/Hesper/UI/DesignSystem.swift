import AppKit
import HesperCore
import QuartzCore
import SwiftUI

/// Hesper's design system: the one place for spacing, radii, type, motion,
/// elevation and density. Views use these names, never literal numbers.
/// Colors stay in `Theme` (tokens, Dusk/Daylight). The pure values live in
/// HesperCore (`DesignTokens`, `StateMarkKind`) so they are unit tested;
/// this file adds the AppKit/SwiftUI side. Primitives built on it are in
/// UI/DS/. Rules and usage: docs/design-system.md.
enum DS {
    typealias TextStyle = DesignTokens.TextStyle
    typealias Weight = DesignTokens.Weight
    typealias Density = DesignTokens.Density
    typealias Motion = DesignTokens.Motion

    // MARK: Spacing

    /// The spacing scale (points): 2 · 4 · 6 · 8 · 12 · 16 · 24.
    enum Spacing {
        static let xxs = CGFloat(DesignTokens.Spacing.xxs)
        static let xs = CGFloat(DesignTokens.Spacing.xs)
        static let s = CGFloat(DesignTokens.Spacing.s)
        static let m = CGFloat(DesignTokens.Spacing.m)
        static let l = CGFloat(DesignTokens.Spacing.l)
        static let xl = CGFloat(DesignTokens.Spacing.xl)
        static let xxl = CGFloat(DesignTokens.Spacing.xxl)
    }

    // MARK: Radius

    /// Corner radii, always continuous corners: kbd 4 · control 7 · tile 10 · panel 14.
    enum Radius {
        static let kbd = CGFloat(DesignTokens.Radius.kbd)
        static let control = CGFloat(DesignTokens.Radius.control)
        static let tile = CGFloat(DesignTokens.Radius.tile)
        static let panel = CGFloat(DesignTokens.Radius.panel)

        /// SwiftUI: the continuous rounded rectangle of a radius.
        static func shape(_ r: CGFloat) -> RoundedRectangle { RoundedRectangle(cornerRadius: r, style: .continuous) }

        /// AppKit: rounds a layer with continuous corners.
        static func apply(_ r: CGFloat, to layer: CALayer?, masks: Bool = false) {
            guard let layer else { return }
            layer.cornerRadius = r
            layer.cornerCurve = .continuous
            if masks { layer.masksToBounds = true }
        }
    }

    // MARK: Type

    /// Geist at a step of the type scale (`meta` is Geist Mono).
    static func font(_ style: TextStyle, _ weight: Weight = .regular) -> Font {
        style.isMono ? .geistMono(CGFloat(style.size), weight.swiftUI) : .geist(CGFloat(style.size), weight.swiftUI)
    }
    static func nsFont(_ style: TextStyle, _ weight: Weight = .regular) -> NSFont {
        let size = CGFloat(style.size)
        return style.isMono ? .geistMono(ofSize: size, weight: weight.appKit) : .geist(ofSize: size, weight: weight.appKit)
    }
    /// Geist Mono at a step's size (meta values inside sans text, counts).
    static func monoFont(_ style: TextStyle, _ weight: Weight = .regular) -> Font { .geistMono(CGFloat(style.size), weight.swiftUI) }
    static func nsMonoFont(_ style: TextStyle, _ weight: Weight = .regular) -> NSFont { .geistMono(ofSize: CGFloat(style.size), weight: weight.appKit) }

    // MARK: Motion

    /// The one ease-out curve, cubic-bezier(0.2, 0, 0, 1).
    static var timingFunction: CAMediaTimingFunction {
        let c = Motion.curve
        return CAMediaTimingFunction(controlPoints: Float(c.c1x), Float(c.c1y), Float(c.c2x), Float(c.c2y))
    }

    /// Reduce Motion (System Settings → Accessibility → Display).
    static var reduceMotion: Bool { NSWorkspace.shared.accessibilityDisplayShouldReduceMotion }
    /// Reduce Transparency: floating layers fall back to solid `surface`.
    static var reduceTransparency: Bool { NSWorkspace.shared.accessibilityDisplayShouldReduceTransparency }

    /// A SwiftUI animation for a motion step, or nil (no animation) under
    /// Reduce Motion. `reduceMotion` is injectable (tests, SwiftUI's
    /// `@Environment(\.accessibilityReduceMotion)`).
    static func animation(_ m: Motion, reduceMotion: Bool = DS.reduceMotion) -> Animation? {
        guard Motion.animates(reduceMotion: reduceMotion) else { return nil }
        let c = Motion.curve
        return .timingCurve(c.c1x, c.c1y, c.c2x, c.c2y, duration: m.duration)
    }

    /// `withAnimation` with a motion step (a plain change under Reduce Motion).
    @MainActor static func withMotion<R>(_ m: Motion, reduceMotion: Bool = DS.reduceMotion, _ body: () throws -> R) rethrows -> R {
        try withAnimation(animation(m, reduceMotion: reduceMotion), body)
    }

    /// AppKit: runs `changes` in an NSAnimationContext with the motion step
    /// (duration 0, i.e. immediate, under Reduce Motion). Use `animator()`
    /// inside, as usual.
    @MainActor static func animate(_ m: Motion, reduceMotion: Bool = DS.reduceMotion, _ changes: () -> Void, completion: (@MainActor () -> Void)? = nil) {
        let d = m.duration(reduceMotion: reduceMotion)
        NSAnimationContext.runAnimationGroup { ctx in
            ctx.duration = d
            ctx.timingFunction = timingFunction
            ctx.allowsImplicitAnimation = d > 0
            changes()
        } completionHandler: {
            if let completion { MainActor.assumeIsolated { completion() } }
        }
    }

    /// Core Animation: a basic animation with the step's duration and the
    /// curve, or nil under Reduce Motion (set the model value directly).
    static func caAnimation(_ m: Motion, keyPath: String, reduceMotion: Bool = DS.reduceMotion) -> CABasicAnimation? {
        guard Motion.animates(reduceMotion: reduceMotion) else { return nil }
        let a = CABasicAnimation(keyPath: keyPath)
        a.duration = m.duration
        a.timingFunction = timingFunction
        return a
    }

    // MARK: Elevation

    /// Flat: tiles. Raised: bands and sidebar rows on hover (a `surface`
    /// fill, no shadow). Floating: glass (material + blur) and one soft
    /// shadow; solid `surface` under Reduce Transparency.
    enum Elevation: CaseIterable {
        case flat, raised, floating

        struct Shadow { var opacity: Float; var radius: CGFloat; var y: CGFloat }
        /// The one soft shadow (floating only).
        var shadow: Shadow? {
            self == .floating ? Shadow(opacity: 0.28, radius: 18, y: 6) : nil
        }
        var usesMaterial: Bool { self == .floating }

        /// AppKit: the shadow on a layer (the layer must not mask; put the
        /// rounded, masked content in a sublayer/subview).
        func applyShadow(to layer: CALayer?, radius corner: CGFloat) {
            guard let layer else { return }
            if let s = shadow {
                layer.shadowColor = NSColor.black.cgColor
                layer.shadowOpacity = s.opacity
                layer.shadowRadius = s.radius
                layer.shadowOffset = CGSize(width: 0, height: -s.y) // AppKit layers: y up
                layer.shadowPath = CGPath(roundedRect: layer.bounds, cornerWidth: corner, cornerHeight: corner, transform: nil)
            } else {
                layer.shadowOpacity = 0
            }
        }
    }

    // MARK: Chrome and density

    /// No chrome element is taller than this.
    static let chromeMaxHeight = CGFloat(DesignTokens.Chrome.maxHeight)
    static let bandGap = CGFloat(DesignTokens.Chrome.bandGap)

    /// The wall's density (Settings; AppSettings keeps it in sync).
    @MainActor static var density: Density = .default {
        didSet { if oldValue != density { NotificationCenter.default.post(name: densityDidChange, object: nil) } }
    }
    static let densityDidChange = Notification.Name("HesperDensityDidChange")

    /// A tile's header at the current density: 26 Comfortable, 22 Compact.
    @MainActor static var tileHeader: CGFloat { CGFloat(density.tileHeader) }
    /// The gap between tiles: 8 Comfortable, 6 Compact.
    @MainActor static var tileGutter: CGFloat { CGFloat(density.tileGutter) }

    // MARK: Terminal

    /// The terminal's own background (libghostty's theme, the same in Dusk
    /// and Daylight): anything around or behind a terminal is painted in it,
    /// never in a chrome token.
    static var terminalBackground: NSColor { Theme.ns(TokyoNight.background) }
}

extension DesignTokens.Weight {
    var swiftUI: Font.Weight {
        switch self {
        case .regular: return .regular
        case .medium: return .medium
        case .semibold: return .semibold
        }
    }
    var appKit: NSFont.Weight {
        switch self {
        case .regular: return .regular
        case .medium: return .medium
        case .semibold: return .semibold
        }
    }
}

extension Font {
    /// A step of the DS type scale: `.font(.ds(.chrome, .medium))`.
    static func ds(_ style: DS.TextStyle, _ weight: DS.Weight = .regular) -> Font { DS.font(style, weight) }
}

extension NSFont {
    /// A step of the DS type scale.
    static func ds(_ style: DS.TextStyle, _ weight: DS.Weight = .regular) -> NSFont { DS.nsFont(style, weight) }
}
