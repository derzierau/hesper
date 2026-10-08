import Foundation

/// The design system's pure values (no AppKit/SwiftUI): the scales, the
/// density table, the state → mark mapping and the motion rule. The app's
/// `DS` namespace (app/Sources/Hesper/UI/DesignSystem.swift) re-exports
/// these and adds the drawing side. See docs/design-system.md.
public enum DesignTokens {
    /// The spacing scale (points): a 4 pt grid with 2 and 6 for tight chrome.
    public enum Spacing {
        public static let xxs: Double = 2
        public static let xs: Double = 4
        public static let s: Double = 6
        public static let m: Double = 8
        public static let l: Double = 12
        public static let xl: Double = 16
        public static let xxl: Double = 24
        public static let all: [Double] = [xxs, xs, s, m, l, xl, xxl]
    }

    /// Corner radii (points); always drawn with continuous corners.
    public enum Radius {
        /// Kbd hints and small chips.
        public static let kbd: Double = 4
        /// Pills, buttons, rows.
        public static let control: Double = 7
        /// Wall tiles and cards.
        public static let tile: Double = 10
        /// Floating panels (popovers, sheets, the palette).
        public static let panel: Double = 14
    }

    /// The type scale (Geist; `meta` is Geist Mono).
    public enum TextStyle: String, CaseIterable, Sendable {
        case meta, chrome, body, panelTitle, sheetTitle, display

        public var size: Double {
            switch self {
            case .meta: return 11
            case .chrome: return 12
            case .body: return 13
            case .panelTitle: return 15
            case .sheetTitle: return 20
            case .display: return 28
            }
        }

        public var isMono: Bool { self == .meta }
    }

    /// The only three weights the chrome uses.
    public enum Weight: Int, CaseIterable, Sendable {
        case regular = 400, medium = 500, semibold = 600
    }

    /// Hard caps of the chrome.
    public enum Chrome {
        /// No chrome element (toolbar, bars, headers) is taller.
        public static let maxHeight: Double = 28
        /// Between bands (projects) on the wall.
        public static let bandGap: Double = 16
    }

    /// Wall density: one token set, two modes.
    public enum Density: String, CaseIterable, Codable, Sendable {
        case comfortable, compact

        public static let `default` = Density.comfortable

        /// A tile's header bar.
        public var tileHeader: Double { self == .comfortable ? 26 : 22 }
        /// The gap between tiles.
        public var tileGutter: Double { self == .comfortable ? 8 : 6 }
        /// Between bands; the same in both modes.
        public var bandGap: Double { Chrome.bandGap }

        public var label: String { self == .comfortable ? "Comfortable" : "Compact" }
    }

    /// Motion: three durations and one ease-out curve.
    public enum Motion: String, CaseIterable, Sendable {
        /// Hover, press, small fades.
        case quick
        /// Things that move (a tile to its window, a panel in).
        case move
        /// Things that settle (layout changes, sheets).
        case settle

        public var duration: Double {
            switch self {
            case .quick: return 0.12
            case .move: return 0.18
            case .settle: return 0.22
            }
        }

        /// The one curve: cubic-bezier(0.2, 0, 0, 1).
        public static let curve: (c1x: Double, c1y: Double, c2x: Double, c2y: Double) = (0.2, 0, 0, 1)

        /// The duration to animate with: 0 (no animation) under Reduce Motion.
        public func duration(reduceMotion: Bool) -> Double { reduceMotion ? 0 : duration }
        /// Whether to animate at all.
        public static func animates(reduceMotion: Bool) -> Bool { !reduceMotion }
    }

    /// The slow pulse of a mark that needs you (one full cycle).
    public static let pulsePeriod: Double = 2
}

/// The state mark: one shape (a square) for every agent state.
public enum StateMarkKind: String, CaseIterable, Sendable {
    case starting, working, needsYou, question, done, idle, error, exited

    /// How the square is drawn.
    public enum Fill: Sendable { case filled, outlined, slashed }
    /// Its color role (the app maps these to Theme tokens).
    public enum Tone: Sendable { case working, signal, question, done, dim, error }

    public init(_ state: AgentState) {
        switch state {
        case .starting: self = .starting
        case .working: self = .working
        case .approval: self = .needsYou
        case .question: self = .question
        case .done: self = .done
        case .idle, .unknown: self = .idle
        case .error: self = .error
        case .exited: self = .exited
        }
    }

    public var fill: Fill {
        switch self {
        case .working, .needsYou, .question, .done, .error: return .filled
        case .idle, .starting: return .outlined
        case .exited: return .slashed
        }
    }

    public var tone: Tone {
        switch self {
        case .starting, .working: return .working
        case .needsYou: return .signal
        case .question: return .question
        case .done: return .done
        case .idle, .exited: return .dim
        case .error: return .error
        }
    }

    /// Only "needs you" pulses (and never under Reduce Motion).
    public var pulses: Bool { self == .needsYou }
    public func pulses(reduceMotion: Bool) -> Bool { pulses && !reduceMotion }

    public var label: String {
        switch self {
        case .starting: return "starting"
        case .working: return "working"
        case .needsYou: return "needs you"
        case .question: return "question"
        case .done: return "done"
        case .idle: return "idle"
        case .error: return "error"
        case .exited: return "exited"
        }
    }

    /// The mark's side (points) and an outline's stroke.
    public static let side: Double = 7
    public static let stroke: Double = 1.5
}
