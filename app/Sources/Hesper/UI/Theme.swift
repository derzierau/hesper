import AppKit
import HesperCore
import SwiftUI

/// Hesper's palette, and the one spacing/type scale every view uses.
///
/// Colors are named tokens with two schemes that follow the system
/// appearance: Dusk (dark, the default) and Daylight (light). Every
/// `NSColor`/`Color` made here is dynamic: it resolves against the
/// appearance it is drawn in. Layer colors (`cgColor`) are snapshots, so
/// views that set them re-apply in `viewDidChangeEffectiveAppearance`
/// (see `NSView.themed`). The terminal content (libghostty) keeps its own
/// theme; only the chrome around it uses these tokens.
enum Theme {
    enum Token: CaseIterable, Sendable {
        case background, surface, tile, line, text, text2, dim, dim2, signal, working, question, done, error, horizon

        /// (Dusk, Daylight) as sRGB hex.
        var schemes: (dusk: UInt32, daylight: UInt32) {
            switch self {
            case .background: return (0x0F1020, 0xF6F5FA)
            case .surface: return (0x171A2E, 0xECEBF3)   // toolbar, sidebar, card headers
            case .tile: return (0x13152A, 0xFFFFFF)
            case .line: return (0x272B48, 0xDFDEEA)
            case .text: return (0xD7DBF5, 0x1B1D33)
            case .text2: return (0xB3B6CF, 0x424457)    // text at 82% over background
            case .dim: return (0x7A80A8, 0x61668C)
            case .dim2: return (0x505578, 0xA0A2BB)     // halfway between dim and line
            case .signal: return (0xFF6F91, 0xC9305C)    // the dot, approvals
            case .working: return (0x8F9CFF, 0x4B55D6)   // accent
            case .question: return (0xFFCF5C, 0x8C6300)
            case .done: return (0x7FE0A8, 0x17804F)
            case .error: return (0xFF5D5D, 0xD93636)
            case .horizon: return (0xFFB36B, 0xB4600F)   // warm highlights (search matches)
            }
        }

        /// The token as a string, for APIs that carry colors as hex (overlay
        /// rows, band colors): its Dusk value, which `Theme.ns`/`Theme.color`
        /// resolve back to this (dynamic) token.
        var hex: String { String(format: "#%06X", schemes.dusk) }
    }

    enum Scheme { case dusk, daylight }

    static func scheme(_ appearance: NSAppearance) -> Scheme {
        appearance.bestMatch(from: [.darkAqua, .aqua]) == .aqua ? .daylight : .dusk
    }

    private static func srgb(_ v: UInt32, _ alpha: CGFloat) -> NSColor {
        NSColor(srgbRed: CGFloat((v >> 16) & 0xff) / 255, green: CGFloat((v >> 8) & 0xff) / 255, blue: CGFloat(v & 0xff) / 255, alpha: alpha)
    }

    /// A token as a dynamic color (Dusk or Daylight by the drawing appearance).
    static func ns(_ t: Token, alpha: CGFloat = 1) -> NSColor {
        let dusk = srgb(t.schemes.dusk, alpha), daylight = srgb(t.schemes.daylight, alpha)
        return NSColor(name: nil) { scheme($0) == .daylight ? daylight : dusk }
    }
    static func color(_ t: Token) -> Color { Color(nsColor: ns(t)) }

    /// Hex colors: a token's `hex` (and the Tokyo Night chrome values that
    /// HesperCore still hands out, e.g. band defaults) resolve to the token;
    /// anything else (a project's own color) stays as it is.
    static func ns(_ hex: String, alpha: CGFloat = 1) -> NSColor {
        if let t = tokenByHex[hex.lowercased()] { return ns(t, alpha: alpha) }
        var v: UInt64 = 0
        Scanner(string: String(hex.dropFirst())).scanHexInt64(&v)
        return srgb(UInt32(v & 0xffffff), alpha)
    }
    static func color(_ hex: String) -> Color { Color(nsColor: ns(hex)) }

    private static let tokenByHex: [String: Token] = {
        var m: [String: Token] = [:]
        for t in Token.allCases { m[t.hex.lowercased()] = t }
        m["#565f89"] = .dim       // band / sidebar row default (HesperCore)
        m["#414868"] = .dim
        m["#7aa2f7"] = .working   // "All" rows: the old accent
        return m
    }()

    // Roles, each one a token.
    static let windowBG = ns(.background)   // the wall
    /// A card's body around the terminal.
    static let tileBG = ns(.tile)
    static let headerBG = ns(.surface)      // card header
    static let tileBorder = ns(.line)
    static let hairline = ns(.line)

    static let fg = color(.text)
    static let fg2 = color(.text2)
    static let dim = color(.dim)
    static let muted = color(.dim)
    static let surface = color(.surface)
    static let chipBG = color(.line)
    static let accent = color(.working)
    static let selection = Color(nsColor: ns(.working, alpha: 0.2))
    static let stroke = color(.line)

    static func stateToken(_ s: AgentState) -> Token {
        switch s {
        case .approval: return .signal
        case .question: return .question
        case .error: return .error
        case .working, .starting: return .working
        case .done: return .done
        case .idle, .exited, .unknown: return .dim
        }
    }
    /// A state mark's tone as a token.
    static func token(_ t: StateMarkKind.Tone) -> Token {
        switch t {
        case .working: return .working
        case .signal: return .signal
        case .question: return .question
        case .done: return .done
        case .dim: return .dim
        case .error: return .error
        }
    }
    static func stateHex(_ s: AgentState) -> String { stateToken(s).hex }
    static func state(_ s: AgentState) -> Color { color(stateToken(s)) }
    static func stateNS(_ s: AgentState) -> NSColor { ns(stateToken(s)) }

    static func kindLabel(_ kind: String) -> String {
        switch kind {
        case "claude": return "Claude"
        case "codex": return "Codex"
        case "shell": return "Shell"
        default: return kind.capitalized
        }
    }

    // Type scale: Geist for the chrome (400 body, 500 labels, 600 titles),
    // Geist Mono for Hesper's own mono bits. Terminals keep the user's font.
    static let title = DS.font(.body, .semibold)
    static let body = DS.font(.chrome)
    static let caption = Font.geist(11)
    static let chip = Font.geist(10.5, .medium)
    static let mono = Font.ds(.meta)
    static let monoSmall = Font.geistMono(10.5)

    /// "42s", "4m", "1h 12m".
    static func elapsed(since d: Date?, now: Date = Date()) -> String? {
        guard let d else { return nil }
        let s = max(0, Int(now.timeIntervalSince(d)))
        if s < 60 { return "\(s)s" }
        if s < 3600 { return "\(s / 60)m" }
        return "\(s / 3600)h \((s % 3600) / 60)m"
    }
}

extension NSView {
    /// Runs `apply` (which sets layer colors) in this view's appearance now,
    /// so `cgColor` snapshots match Dusk/Daylight. Call it again from
    /// `viewDidChangeEffectiveAppearance`.
    func themed(_ apply: () -> Void) {
        effectiveAppearance.performAsCurrentDrawingAppearance(apply)
    }
}

extension NSColor {
    /// This (dynamic) color resolved in `view`'s appearance, for layers.
    @MainActor func cg(in view: NSView) -> CGColor {
        var c = cgColor
        view.effectiveAppearance.performAsCurrentDrawingAppearance { c = self.cgColor }
        return c
    }
}

/// Older layout constants (new code uses `DS`: Spacing, Radius, density).
enum Metrics {
    static let space1 = DS.Spacing.xs
    static let space2 = DS.Spacing.m
    static let space3 = DS.Spacing.l
    static let space4 = DS.Spacing.xl
    static let space5: CGFloat = 20
    static let space6 = DS.Spacing.xxl
    /// Every card's radius (tiles, drafts, ghost and session cards).
    static let radius = DS.Radius.tile
    static let controlRadius = DS.Radius.control
    /// Every live card's footer: one line, always there, so the terminal
    /// area never changes height when the state does.
    static let footer: CGFloat = 40

    /// The wall at the current density (`WallSpec.wall`): header 26 / 22,
    /// gutter 8 / 6, margin 16 / 12, quiet band headings. No terminal
    /// inset: the terminal spans the card's body edge to edge (its sub-cell
    /// remainder is painted in the terminal's own background,
    /// `DS.terminalBackground`). Changes post `DS.densityDidChange`.
    @MainActor static var wall: WallSpec { .wall(DS.density) }
}

extension AgentState {
    var label: String {
        switch self {
        case .approval: return "needs approval"
        case .question: return "asks a question"
        case .error: return "error"
        case .working: return "working"
        case .starting: return "starting"
        case .done: return "done"
        case .idle: return "idle"
        case .exited: return "exited"
        case .unknown: return "unknown"
        }
    }
}
