import Foundation

/// A keyboard move of the wall's selection: ← → ↑ ↓, Home, End.
public enum WallMove: Equatable, Sendable {
    case left, right, up, down, first, last
}

/// Spatial keyboard navigation over the wall's cards (every arrangement:
/// grid + shelf, columns, treemap, main + stack; drafts are cards too).
///
/// Rules:
/// - A direction picks among the cards lying wholly on that side of the
///   current one (1 pt tolerance). The nearest wins by
///   `gap along the direction + 2 × gap across it` (0 across when the cards
///   overlap on the other axis, i.e. are in the same row/column band), then
///   by the distance of the centres across it (a partial last row, a shelf
///   card under two grid cards: the one most under/over it).
/// - Wrap: ← / → with nothing on that side go to the previous / next card
///   in reading order (the end of a row continues on the next row, like
///   text); stops at the first / last card (no wrap around the wall).
///   ↑ / ↓ with nothing above / below stay.
/// - Home / End: the first / last card in reading order.
/// - Reading order: by top edge (rows), then left edge.
/// - Cards with no size (not placed) are never targets.
public enum WallNavigation {
    static let tolerance = 1.0

    /// Card indices top to bottom, left to right.
    public static func readingOrder(_ cards: [Rect]) -> [Int] {
        cards.indices.filter { cards[$0].width > 0 && cards[$0].height > 0 }.sorted { a, b in
            let ya = cards[a].y.rounded(), yb = cards[b].y.rounded()
            if ya != yb { return ya < yb }
            if cards[a].x != cards[b].x { return cards[a].x < cards[b].x }
            return a < b
        }
    }

    /// The card a move from `index` goes to (`index` itself when it stays).
    public static func target(_ cards: [Rect], from index: Int, _ move: WallMove) -> Int {
        let order = readingOrder(cards)
        guard let first = order.first, let last = order.last else { return index }
        switch move {
        case .first: return first
        case .last: return last
        default: break
        }
        guard cards.indices.contains(index), let pos = order.firstIndex(of: index) else { return first }
        if let j = nearest(cards, from: index, move) { return j }
        switch move {
        case .left: return pos > 0 ? order[pos - 1] : index
        case .right: return pos + 1 < order.count ? order[pos + 1] : index
        default: return index
        }
    }

    /// The nearest card wholly on the `move` side of `index`, if any.
    static func nearest(_ cards: [Rect], from index: Int, _ move: WallMove) -> Int? {
        let f = cards[index], t = tolerance
        var best: (index: Int, score: Double, across: Double)?
        for (j, c) in cards.enumerated() where j != index && c.width > 0 && c.height > 0 {
            let along: Double, gapAcross: Double, across: Double
            switch move {
            case .right:
                guard c.x >= f.maxX - t else { continue }
                along = c.x - f.maxX
                gapAcross = max(0, max(c.y, f.y) - min(c.maxY, f.maxY))
                across = abs(c.midY - f.midY)
            case .left:
                guard c.maxX <= f.x + t else { continue }
                along = f.x - c.maxX
                gapAcross = max(0, max(c.y, f.y) - min(c.maxY, f.maxY))
                across = abs(c.midY - f.midY)
            case .down:
                guard c.y >= f.maxY - t else { continue }
                along = c.y - f.maxY
                gapAcross = max(0, max(c.x, f.x) - min(c.maxX, f.maxX))
                across = abs(c.midX - f.midX)
            case .up:
                guard c.maxY <= f.y + t else { continue }
                along = f.y - c.maxY
                gapAcross = max(0, max(c.x, f.x) - min(c.maxX, f.maxX))
                across = abs(c.midX - f.midX)
            case .first, .last:
                return nil
            }
            let score = max(0, along) + 2 * gapAcross
            if let b = best {
                if score < b.score - 1e-6 || (abs(score - b.score) <= 1e-6 && across < b.across - 1e-6) { best = (j, score, across) }
            } else {
                best = (j, score, across)
            }
        }
        return best?.index
    }
}

extension WallLayout {
    /// The card a keyboard move from `index` selects (WallNavigation).
    public func neighbor(of index: Int, _ move: WallMove) -> Int {
        WallNavigation.target(tiles.map(\.card), from: index, move)
    }
}

/// What a key sends to an agent's terminal when the app forwards it itself
/// (the moment between "start typing" on a selected tile and its read-write
/// terminal taking the keyboard). Plain text, ⏎, ⌫, esc, ⇥, arrows and
/// ⌃-letters; nil for anything else (⌘ chords stay the app's).
public enum TerminalKeys {
    /// Text a key produced that is worth typing: no control characters, no
    /// function-key placeholders (U+F700…U+F8FF).
    public static func isPrintable(_ s: String) -> Bool {
        !s.isEmpty && s.unicodeScalars.allSatisfy { $0.value >= 0x20 && $0.value != 0x7f && !(0xF700...0xF8FF).contains($0.value) }
    }

    public static func bytes(_ k: KeyChord, characters: String?) -> String? {
        if k.command { return nil }
        if k.control {
            if case .char(let c) = k.key, let a = c.lowercased().unicodeScalars.first, ("a"..."z").contains(Character(a)) {
                return String(UnicodeScalar(UInt8(a.value - 0x60)))
            }
            return nil
        }
        switch k.key {
        case .enter: return "\r"
        case .escape: return "\u{1b}"
        case .tab: return k.shift ? "\u{1b}[Z" : "\t"
        case .delete: return "\u{7f}"
        case .up: return "\u{1b}[A"
        case .down: return "\u{1b}[B"
        case .right: return "\u{1b}[C"
        case .left: return "\u{1b}[D"
        case .char("\u{7f}"), .char("\u{8}"): return "\u{7f}"
        default:
            guard let s = characters, isPrintable(s) else { return nil }
            return s
        }
    }
}
