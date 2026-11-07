import HesperCore
import HesperSurfaces
import SwiftUI
import UIKit

/// The Neon console's colours (`NeonPalette`, docs/iphone/neon.md). Dark
/// only for now, so each token is one colour. The only place the app names
/// a colour.
public enum Theme {
    nonisolated static func srgb(_ v: UInt32) -> UIColor {
        UIColor(red: CGFloat((v >> 16) & 0xff) / 255, green: CGFloat((v >> 8) & 0xff) / 255, blue: CGFloat(v & 0xff) / 255, alpha: 1)
    }

    /// A token as a UIKit colour, for the places SwiftUI does not reach.
    public nonisolated static func uiColor(_ token: NeonPalette.Token) -> UIColor {
        srgb(NeonPalette.hex(token))
    }

    private static func color(_ token: NeonPalette.Token) -> Color { Color(uiColor: uiColor(token)) }

    /// The screen, the console, command blocks.
    public static let background = color(.background)
    /// Cards, unselected segments, secondary buttons.
    public static let surface = color(.surface)
    /// Numbered option rows.
    public static let surface2 = color(.surface2)
    /// 1 pt borders and edges.
    public static let line = color(.line)
    public static let text = color(.text)
    /// Meta lines and details.
    public static let muted = color(.muted)
    /// Cyan, for action: primary buttons, selection, commands.
    public static let accent = color(.accent)
    /// Text and icons on an accent fill.
    public static let onAccent = color(.onAccent)
    /// The "You" panel and the open button.
    public static let accentTint = color(.accentTint)
    /// Magenta, needs you: marks, captions, destructive options.
    public static let signal = color(.signal)
    /// A destructive option row's fill.
    public static let signalTint = color(.signalTint)

    // The names the screens used before Neon, so they keep compiling until
    // each is restyled.
    public static let tile = surface
    public static let text2 = text
    public static let dim = muted
    public static let question = signal
    public static let done = muted
    public static let error = signal
    /// The glow behind onboarding.
    public static let horizon = accent
    public static let working = accent

    /// A state mark's tone as a colour: Neon has only cyan and magenta, so
    /// working is the accent, anything that needs you is signal, the rest
    /// is muted.
    public static func tone(_ tone: StateMarkKind.Tone) -> Color {
        switch tone {
        case .working: accent
        case .signal, .question, .error: signal
        case .done, .dim: muted
        }
    }
}

/// Spacing, radii and motion from the shared tokens (HesperCore's
/// `DesignTokens`), in points, plus the console's own shapes.
public enum DS {
    public enum Spacing {
        public static let xxs = CGFloat(DesignTokens.Spacing.xxs)
        public static let xs = CGFloat(DesignTokens.Spacing.xs)
        public static let s = CGFloat(DesignTokens.Spacing.s)
        public static let m = CGFloat(DesignTokens.Spacing.m)
        public static let l = CGFloat(DesignTokens.Spacing.l)
        public static let xl = CGFloat(DesignTokens.Spacing.xl)
        public static let xxl = CGFloat(DesignTokens.Spacing.xxl)
        /// Between the cards of a task grid and the console's two buttons.
        public static let grid: CGFloat = 10
    }

    public enum Radius {
        /// Every corner on the console: nearly square.
        public static let console: CGFloat = 2
    }

    public enum Edge {
        /// The priority card's accent top edge.
        public static let hero: CGFloat = 3
    }

    /// The phone's state mark is a little larger than the Mac's 7 pt.
    public static let markSide: CGFloat = 8

    /// The one ease-out curve, or none under Reduce Motion.
    public static func animation(_ motion: DesignTokens.Motion, reduceMotion: Bool) -> Animation? {
        guard DesignTokens.Motion.animates(reduceMotion: reduceMotion) else { return nil }
        let c = DesignTokens.Motion.curve
        return .timingCurve(c.c1x, c.c1y, c.c2x, c.c2y, duration: motion.duration)
    }
}
