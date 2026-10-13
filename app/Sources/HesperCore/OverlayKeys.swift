import Foundation

/// The same keys in every overlay (palette, popovers, completion):
/// ↑↓ select · ⏎ do · ⌘⏎ do the alternative (palette: open full size;
/// attention: allow) · ⇥ act on the selection · esc close without losing
/// input · typing filters (⌫ deletes).
public enum OverlayKeyAction: Equatable, Sendable {
    case up, down, activate, alternate, actOn, back, close
    case type(String)
    case deleteBackward
    /// Not the overlay's: let it through (⌘ shortcuts, …).
    case pass
}

public enum OverlayKeys {
    /// The action a registry command is in an overlay (nil: not the
    /// overlay's, it passes).
    public static func action(for id: CommandID) -> OverlayKeyAction? {
        switch id {
        case .moveUp: return .up
        case .moveDown: return .down
        case .overlayActivate, .inboxPrimary: return .activate
        case .overlayAlternate, .inboxOpen: return .alternate
        case .overlayActOn: return .actOn
        case .overlayBack: return .back
        case .closeOverlay: return .close
        case .overlayDeleteBackward: return .deleteBackward
        default: return nil
        }
    }

    /// - Parameter surface: `.palette`, `.popover` or `.inbox` (the
    ///   registry's surface the overlay is).
    public static func route(_ k: KeyChord, characters: String? = nil, surface: Surface = .palette) -> OverlayKeyAction {
        if let id = CommandRegistry.resolve(k, in: surface), let a = action(for: id) { return a }
        if k.command || k.control { return .pass }
        if let c = characters, !c.isEmpty, c.unicodeScalars.allSatisfy({ !CharacterSet.controlCharacters.contains($0) && $0.value < 0xF700 }) {
            return .type(c)
        }
        return .pass
    }
}

/// An overlay's list: a query that filters, a selection that skips
/// disabled rows and wraps. Kept by the overlay's owner, so a closed
/// overlay comes back as it was left.
public struct OverlayList: Equatable, Sendable {
    public var query: String = ""
    public var index: Int = 0

    public init(query: String = "", index: Int = 0) { self.query = query; self.index = index }

    /// The first enabled row at or after `index` (clamped).
    public func selection(enabled: [Bool]) -> Int? {
        guard !enabled.isEmpty else { return nil }
        let start = max(0, min(index, enabled.count - 1))
        for k in 0..<enabled.count {
            let i = (start + k) % enabled.count
            if enabled[i] { return i }
        }
        return nil
    }

    public mutating func move(_ d: Int, enabled: [Bool]) {
        guard let cur = selection(enabled: enabled) else { return }
        let n = enabled.count
        var i = cur
        for _ in 0..<n {
            i = (i + d + n) % n
            if enabled[i] { index = i; return }
        }
    }

    public mutating func type(_ s: String) { query += s; index = 0 }
    public mutating func deleteBackward() { if !query.isEmpty { query.removeLast(); index = 0 } }
}
