import Foundation

extension WallArrangement {
    /// The arrangement's place in every list (⌥⌘1…, menus, Settings).
    public var number: Int { (Self.allCases.firstIndex(of: self) ?? 0) + 1 }
    /// Its shortcut (⌥⌘1 …).
    public var shortcut: String { "⌥⌘\(number)" }
    /// All of them ("⌥⌘1–5").
    public static var shortcutRange: String { "⌥⌘1–\(allCases.count)" }
    /// By number (1-based, ⌥⌘1 …); nil outside.
    public static func numbered(_ n: Int) -> WallArrangement? { allCases.indices.contains(n - 1) ? allCases[n - 1] : nil }
    /// The next one (⌥⌘L), wrapping.
    public var next: WallArrangement { Self.allCases[number % Self.allCases.count] }

    /// Its SF Symbol (the top bar's layout button, the popover).
    public var symbol: String {
        switch self {
        case .shelf: return "square.grid.2x2"
        case .columns: return "rectangle.split.3x1"
        case .treemap: return "rectangle.3.group"
        case .mainStack: return "rectangle.leadinghalf.inset.filled"
        case .grid: return "square.grid.3x3"
        }
    }
}

/// What the layout switcher shows for a wall's arrangement: the top bar's
/// button (symbol, tooltip) and the popover's rows (the current one
/// checked, and highlighted when the popover opens). Derived from the
/// wall's one `arrangement`, never stored, so it can't go stale.
public struct LayoutSwitcher: Equatable, Sendable {
    public struct Row: Equatable, Sendable {
        public var arrangement: WallArrangement
        public var title: String
        public var shortcut: String
        public var checked: Bool
    }

    public var current: WallArrangement
    public init(_ current: WallArrangement) { self.current = current }

    public var symbol: String { current.symbol }
    /// The button's tooltip and VoiceOver label: "Layout: Grid".
    public var help: String { "Layout: \(current.title)" }
    public var shortcut: String { WallArrangement.shortcutRange }
    public var rows: [Row] {
        WallArrangement.allCases.map { Row(arrangement: $0, title: $0.title, shortcut: $0.shortcut, checked: $0 == current) }
    }
    /// The row the popover highlights when it opens: the current one.
    public var openIndex: Int { current.number - 1 }
}

extension OverlayList {
    /// A list as a popover opens it: the highlight on the first checked
    /// row (the current choice), else the first row.
    public static func opening(checked: [Bool]) -> OverlayList {
        OverlayList(index: checked.firstIndex(of: true) ?? 0)
    }
}
