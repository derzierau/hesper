import Foundation

/// The one terminal font size (Settings › Font size): wall tiles, the
/// focused agent and agent windows all use it. Stored under `key`; a value
/// saved under `legacyKey` (when it drove only the tiles) is kept until a
/// new one is stored.
public enum TerminalFont {
    public static let range: ClosedRange<Double> = 9...16
    public static let step: Double = 0.5
    /// When neither a stored value nor the user's Ghostty font is usable.
    public static let defaultSize: Double = 12
    public static let key = "terminalFontSize"
    public static let legacyKey = "tileFontSize"

    public static func clamp(_ size: Double) -> Double {
        min(max(size, range.lowerBound), range.upperBound)
    }

    /// The first finite value of the stored, legacy and fallback (the
    /// user's Ghostty font) sizes, clamped to `range`.
    public static func resolve(stored: Double?, legacy: Double?, fallback: Double) -> Double {
        clamp([stored, legacy, fallback].lazy.compactMap { $0 }.first(where: \.isFinite) ?? defaultSize)
    }
}
