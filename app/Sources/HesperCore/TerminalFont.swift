import Foundation

/// The wall's terminal font size (Settings › Font size): every wall tile
/// uses it. Stored under `key`; a value saved under `legacyKey` is kept
/// until a new one is stored. The focused agent and agent windows keep
/// the user's Ghostty font (`focusSize`), uncapped.
public enum TerminalFont {
    public static let range: ClosedRange<Double> = 9...16
    public static let step: Double = 0.5
    /// When neither a stored value nor the user's Ghostty font is usable.
    public static let defaultSize: Double = 12
    public static let key = "terminalFontSize"
    public static let legacyKey = "tileFontSize"
    /// The focus font when the user's Ghostty font is unset or unusable.
    public static let focusDefaultSize: Double = 13

    public static func clamp(_ size: Double) -> Double {
        min(max(size, range.lowerBound), range.upperBound)
    }

    /// The first finite value of the stored, legacy and fallback (the
    /// user's Ghostty font) sizes, clamped to `range`.
    public static func resolve(stored: Double?, legacy: Double?, fallback: Double) -> Double {
        clamp([stored, legacy, fallback].lazy.compactMap { $0 }.first(where: \.isFinite) ?? defaultSize)
    }

    /// The focused agent's and agent windows' font: the user's Ghostty
    /// font size as is, else `focusDefaultSize`.
    public static func focusSize(ghostty: Double?) -> Double {
        guard let ghostty, ghostty.isFinite, ghostty > 0 else { return focusDefaultSize }
        return ghostty
    }
}
