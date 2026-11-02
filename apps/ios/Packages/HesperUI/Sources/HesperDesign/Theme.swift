import HesperCore
import SwiftUI
import UIKit

/// The Mac's colour tokens, Dusk in dark mode and Daylight in light mode
/// (app/Sources/Hesper/UI/Theme.swift, docs/design-system.md). The only
/// place the app names a colour.
public enum Theme {
    // nonisolated: UIKit and SwiftUI resolve dynamic colours on their render
    // thread too (glass tints while animating), so the provider closure must
    // not be main-actor.
    public nonisolated enum Token: CaseIterable, Sendable {
        case background, surface, tile, line, text, text2, dim, dim2, signal, working, question, done, error, horizon

        /// (Dusk, Daylight) as sRGB hex; the same values as the Mac.
        public var hex: (dusk: UInt32, daylight: UInt32) {
            switch self {
            case .background: (0x0F1020, 0xF6F5FA)
            case .surface: (0x171A2E, 0xECEBF3)
            case .tile: (0x13152A, 0xFFFFFF)
            case .line: (0x272B48, 0xDFDEEA)
            case .text: (0xD7DBF5, 0x1B1D33)
            case .text2: (0xB3B6CF, 0x424457)
            case .dim: (0x7A80A8, 0x61668C)
            case .dim2: (0x505578, 0xA0A2BB)
            case .signal: (0xFF6F91, 0xC9305C)
            case .working: (0x8F9CFF, 0x4B55D6)
            case .question: (0xFFCF5C, 0x8C6300)
            case .done: (0x7FE0A8, 0x17804F)
            case .error: (0xFF5D5D, 0xD93636)
            case .horizon: (0xFFB36B, 0xB4600F)
            }
        }

        public var color: Color { Color(uiColor: uiColor) }

        public var uiColor: UIColor {
            let (dusk, daylight) = hex
            return UIColor { $0.userInterfaceStyle == .dark ? Theme.srgb(dusk) : Theme.srgb(daylight) }
        }
    }

    nonisolated static func srgb(_ v: UInt32) -> UIColor {
        UIColor(red: CGFloat((v >> 16) & 0xff) / 255, green: CGFloat((v >> 8) & 0xff) / 255, blue: CGFloat(v & 0xff) / 255, alpha: 1)
    }

    public static let background = Token.background.color
    public static let surface = Token.surface.color
    public static let tile = Token.tile.color
    public static let line = Token.line.color
    public static let text = Token.text.color
    public static let text2 = Token.text2.color
    public static let dim = Token.dim.color
    /// The accent: primary actions (Allow, Start, Accept), selection.
    public static let accent = Token.working.color
    /// Needs you. Only on marks and the Inbox badge, never on buttons.
    public static let signal = Token.signal.color
    public static let question = Token.question.color
    public static let done = Token.done.color
    public static let error = Token.error.color

    /// A state mark's tone as a colour.
    public static func tone(_ tone: StateMarkKind.Tone) -> Color {
        switch tone {
        case .working: Token.working.color
        case .signal: Token.signal.color
        case .question: Token.question.color
        case .done: Token.done.color
        case .dim: Token.dim.color
        case .error: Token.error.color
        }
    }
}

/// Spacing, radii and motion from the shared tokens (HesperCore's
/// `DesignTokens`), in points.
public enum DS {
    public enum Spacing {
        public static let xxs = CGFloat(DesignTokens.Spacing.xxs)
        public static let xs = CGFloat(DesignTokens.Spacing.xs)
        public static let s = CGFloat(DesignTokens.Spacing.s)
        public static let m = CGFloat(DesignTokens.Spacing.m)
        public static let l = CGFloat(DesignTokens.Spacing.l)
        public static let xl = CGFloat(DesignTokens.Spacing.xl)
        public static let xxl = CGFloat(DesignTokens.Spacing.xxl)
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
