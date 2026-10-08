import Foundation

/// Where a popover goes: next to its anchor (below, else above), its arrow
/// pointing at the anchor, inside the container, and — the overlay rule —
/// never over a card that needs the user when another spot is free.
public enum PopoverPlacement {
    public struct Result: Equatable, Sendable {
        public var frame: Rect
        /// true: the popover is below the anchor (arrow on its top edge).
        public var below: Bool
        /// The arrow tip's x, relative to the frame.
        public var arrowX: Double
    }

    public static func place(anchor: Rect, width: Double, height: Double, container: Rect, avoid: [Rect] = [],
                             arrow: Double = 7, margin: Double = 10) -> Result {
        let ax = anchor.midX
        // The anchor's own card is not in the way (a popover from it).
        let avoid = avoid.filter { !(intersects($0, anchor)) }
        func clampX(_ x: Double) -> Double { max(container.x + margin, min(x, container.maxX - margin - width)) }
        // Centered, aligned to either end, or just clear of a card in the way.
        var xs = [clampX(ax - width / 2), clampX(anchor.x), clampX(anchor.maxX - width)]
        for a in avoid { xs += [clampX(a.maxX + 8), clampX(a.x - width - 8)] }
        var candidates: [(Rect, Bool)] = []
        for x in xs {
            candidates.append((Rect(x: x, y: anchor.maxY + arrow, width: width, height: height), true))
        }
        for x in xs {
            candidates.append((Rect(x: x, y: anchor.y - arrow - height, width: width, height: height), false))
        }
        func fits(_ r: Rect) -> Bool { r.y >= container.y + 2 && r.maxY <= container.maxY - 2 }
        func overlap(_ r: Rect) -> Double {
            avoid.reduce(0) { acc, a in
                let w = min(r.maxX, a.maxX) - max(r.x, a.x), h = min(r.maxY, a.maxY) - max(r.y, a.y)
                return acc + (w > 0 && h > 0 ? w * h : 0)
            }
        }
        // The arrow must still reach the anchor (else the popover floats
        // away from what it acts on).
        let reachable = candidates.filter { ax >= $0.0.x + 16 && ax <= $0.0.maxX - 16 }
        let fitting = (reachable.isEmpty ? candidates : reachable).filter { fits($0.0) }
        let pick = fitting.first { overlap($0.0) == 0 }
            ?? fitting.min { overlap($0.0) < overlap($1.0) }
            ?? candidates[0]
        var frame = pick.0
        if !fits(frame) { frame.y = max(container.y + 2, min(frame.y, container.maxY - 2 - height)) }
        let tip = max(16, min(width - 16, ax - frame.x))
        return Result(frame: frame, below: pick.1, arrowX: tip)
    }

    static func intersects(_ a: Rect, _ b: Rect) -> Bool {
        a.x < b.maxX && b.x < a.maxX && a.y < b.maxY && b.y < a.maxY
    }
}
