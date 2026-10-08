import Foundation
import Testing
@testable import HesperCore

/// Arrow keys on the wall: spatial neighbors in every arrangement.
@Suite struct WallNavigationTests {
    func r(_ x: Double, _ y: Double, _ w: Double, _ h: Double) -> Rect { Rect(x: x, y: y, width: w, height: h) }
    func go(_ cards: [Rect], _ i: Int, _ m: WallMove) -> Int { WallNavigation.target(cards, from: i, m) }

    /// Grid + shelf by hand: a row of 3, a stretched partial row of 2, a
    /// shelf of 3 below.
    var gridShelf: [Rect] {
        let w3 = (320.0 - 28) / 3
        return [r(0, 0, 100, 100), r(110, 0, 100, 100), r(220, 0, 100, 100),
                r(0, 110, 155, 100), r(165, 110, 155, 100),
                r(0, 220, w3, 30), r(w3 + 14, 220, w3, 30), r(2 * (w3 + 14), 220, w3, 30)]
    }

    @Test func gridRowsWrapLikeText() {
        let c = gridShelf
        #expect(go(c, 0, .right) == 1)
        #expect(go(c, 1, .right) == 2)
        #expect(go(c, 2, .right) == 3)   // end of a row → next row's first
        #expect(go(c, 3, .left) == 2)    // start of a row → previous row's last
        #expect(go(c, 4, .right) == 5)   // last grid card → the shelf
        #expect(go(c, 0, .left) == 0)    // no wrap around the wall
        #expect(go(c, 7, .right) == 7)
        #expect(go(c, 0, .up) == 0)      // nothing above: stays
    }

    @Test func partialRowGoesToTheCardMostOverIt() {
        let c = gridShelf
        #expect(go(c, 0, .down) == 3)
        #expect(go(c, 2, .down) == 4)
        #expect(go(c, 3, .up) == 0)
        #expect(go(c, 4, .up) == 2)
    }

    @Test func shelfAndGridConnect() {
        let c = gridShelf
        #expect(go(c, 3, .down) == 5)
        #expect(go(c, 4, .down) == 7)
        #expect(go(c, 6, .up) == 3)
        #expect(go(c, 7, .up) == 4)
        #expect(go(c, 5, .right) == 6)
        #expect(go(c, 7, .down) == 7)
        #expect(go(c, 4, .first) == 0)
        #expect(go(c, 0, .last) == 7)
    }

    @Test func mainAndStack() {
        // Main on the left, a stack column of three, a second column of one.
        let c = [r(0, 0, 500, 600), r(510, 0, 240, 190), r(510, 205, 240, 190), r(510, 410, 240, 190), r(760, 0, 240, 190)]
        #expect(go(c, 0, .right) == 2)   // the stack card level with the main one
        #expect(go(c, 2, .left) == 0)
        #expect(go(c, 4, .left) == 1)
        #expect(go(c, 1, .right) == 4)
        #expect(go(c, 1, .down) == 2)
        #expect(go(c, 3, .down) == 3)
        #expect(go(c, 2, .up) == 1)
        #expect(go(c, 0, .last) == 3)
        #expect(go(c, 3, .first) == 0)
    }

    @Test func unplacedCardsAreNeverTargets() {
        let c = [r(0, 0, 100, 100), r(0, 0, 0, 0), r(110, 0, 100, 100)]
        #expect(go(c, 0, .right) == 2)
        #expect(go(c, 2, .left) == 0)
        #expect(WallNavigation.readingOrder(c) == [0, 2])
    }

    // MARK: Real layouts

    let table = FontFit.CellTable(scale: 2, ratio: FontFit.CellRatio(widthPerPoint: 0.6, heightPerPoint: 1.3))
    func cell(_ f: Double) -> CellSize { table.cell(f) }
    func inputs(_ states: [AgentState]) -> [WallTileInput] { states.map { WallTileInput(grid: GridSize(cols: 120, rows: 40), state: $0) } }
    var mixed: [AgentState] { [.working, .done, .approval, .working, .idle, .working, .question, .exited, .working] }

    /// Every target lies on the move's side, unless a ← → wrapped to the
    /// reading-order neighbor because nothing was on that side.
    func checkDirections(_ l: WallLayout) {
        let cards = l.tiles.map(\.card)
        let order = WallNavigation.readingOrder(cards)
        for i in cards.indices {
            for m in [WallMove.left, .right, .up, .down] {
                let j = l.neighbor(of: i, m)
                let f = cards[i]
                func beyond(_ c: Rect) -> Bool {
                    switch m {
                    case .right: return c.x >= f.maxX - 1
                    case .left: return c.maxX <= f.x + 1
                    case .down: return c.y >= f.maxY - 1
                    case .up: return c.maxY <= f.y + 1
                    default: return false
                    }
                }
                let any = cards.indices.contains { $0 != i && beyond(cards[$0]) }
                if any {
                    #expect(j != i && beyond(cards[j]), "\(l.arrangement) \(i) \(m) → \(j)")
                } else if m == .left || m == .right {
                    let p = order.firstIndex(of: i)!
                    let want = m == .right ? (p + 1 < order.count ? order[p + 1] : i) : (p > 0 ? order[p - 1] : i)
                    #expect(j == want, "\(l.arrangement) \(i) \(m) wrap → \(j)")
                } else {
                    #expect(j == i, "\(l.arrangement) \(i) \(m) stays")
                }
            }
        }
    }

    /// Every card can be reached from the first with the arrows.
    func reachable(_ l: WallLayout) -> Bool {
        var seen: Set<Int> = [l.neighbor(of: 0, .first)]
        var todo = Array(seen)
        while let i = todo.popLast() {
            for m in [WallMove.left, .right, .up, .down] {
                let j = l.neighbor(of: i, m)
                if seen.insert(j).inserted { todo.append(j) }
            }
        }
        return seen.count == l.tiles.count
    }

    @Test func everyArrangement() {
        for a in WallArrangement.allCases {
            for (w, h) in [(1500.0, 1000.0), (1000, 700), (2400, 1300)] {
                for n in [1, 2, 4, 5, 9] {
                    let l = WallLayout.make(inputs(Array(mixed.prefix(n))), arrangement: a, width: w, height: h, main: n > 2 ? 2 : nil, cell: cell)
                    checkDirections(l)
                    #expect(reachable(l), "\(a) \(n) at \(w)x\(h)")
                }
            }
        }
    }

    @Test func shelfLayoutRightWalksReadingOrder() {
        let l = WallLayout.make(inputs(mixed), arrangement: .shelf, width: 1500, height: 1000, cell: cell)
        let order = WallNavigation.readingOrder(l.tiles.map(\.card))
        var i = order[0], visited = [i]
        for _ in 1..<order.count { i = l.neighbor(of: i, .right); visited.append(i) }
        #expect(visited == order)
        // The shelf holds the quiet ones and is reached from the grid.
        let quiet = l.tiles.indices.filter { l.tiles[$0].quiet }
        #expect(!quiet.isEmpty)
        let grid = l.tiles.indices.filter { !l.tiles[$0].quiet }
        let bottom = grid.max { l.tiles[$0].card.y < l.tiles[$1].card.y }!
        #expect(quiet.contains(l.neighbor(of: bottom, .down)))
        #expect(grid.contains(l.neighbor(of: quiet[0], .up)))
    }

    @Test func scrolledColumns() {
        // Nine columns at the minimum width: the wall scrolls sideways; the
        // keys go card by card, also into the part scrolled off.
        let l = WallLayout.make(inputs(Array(repeating: .working, count: 9)), arrangement: .columns, width: 1000, height: 700, cell: cell)
        #expect(l.contentWidth > 1000)
        for i in 0..<8 { #expect(l.neighbor(of: i, .right) == i + 1) }
        for i in 1..<9 { #expect(l.neighbor(of: i, .left) == i - 1) }
        #expect(l.neighbor(of: 8, .right) == 8)
        #expect(l.neighbor(of: 0, .left) == 0)
        #expect(l.neighbor(of: 4, .up) == 4 && l.neighbor(of: 4, .down) == 4)
        #expect(l.neighbor(of: 3, .last) == 8 && l.neighbor(of: 3, .first) == 0)
    }

    @Test func treemapNeighborsAreAdjacent() {
        let l = WallLayout.make(inputs(mixed), arrangement: .treemap, width: 1600, height: 1000, cell: cell)
        let cards = l.tiles.map(\.card)
        // The heaviest (approval) card: its right/down neighbor touches it
        // (only a gap between them) when one exists.
        for i in cards.indices {
            for m in [WallMove.right, .down] {
                let j = l.neighbor(of: i, m)
                guard j != i else { continue }
                let gap = m == .right ? cards[j].x - cards[i].maxX : cards[j].y - cards[i].maxY
                let overlaps = m == .right ? cards[j].y < cards[i].maxY && cards[i].y < cards[j].maxY
                                           : cards[j].x < cards[i].maxX && cards[i].x < cards[j].maxX
                if overlaps { #expect(gap < 15, "\(i) \(m) → \(j) gap \(gap)") }
            }
        }
    }

    @Test func mainStackRealLayout() {
        let l = WallLayout.make(inputs(Array(repeating: .working, count: 6)), arrangement: .mainStack, width: 1800, height: 1000, main: 3, cell: cell)
        let mainCard = l.tiles[3].card
        #expect(mainCard.x < 30)
        // From main: right goes into the stack; left from the stack's
        // first column comes back to main.
        let s = l.neighbor(of: 3, .right)
        #expect(s != 3 && l.tiles[s].card.x > mainCard.maxX)
        #expect(l.neighbor(of: s, .left) == 3)
        #expect(l.neighbor(of: 3, .first) == 3)
    }
}

@Suite struct AgentWindowStepTests {
    final class H {}

    @Test func stepSkipsAgentsWithAWindowAndWraps() {
        var book = AgentWindowBook<H>()
        let a = H(), b = H()
        _ = book.openOrFocus("a") { a }
        _ = book.openOrFocus("c") { b }
        let order = ["a", "b", "c", "d"]
        #expect(book.stepTarget(from: "a", by: 1, order: order) == "b")
        #expect(book.stepTarget(from: "a", by: -1, order: order) == "d")      // wraps
        #expect(book.stepTarget(from: "c", by: -1, order: order) == "b")
        #expect(book.stepTarget(from: "a", by: 1, order: ["a", "c"]) == nil) // everyone else has a window
        #expect(book.stepTarget(from: "x", by: 1, order: order) == nil)
        let moved = book.retarget("a", to: "b")
        #expect(moved && book.handle(for: "b") === a && book.handle(for: "a") == nil)
        let taken = book.retarget("b", to: "c")                                 // one window per agent
        let unknown = book.retarget("zz", to: "d")
        #expect(!taken && !unknown)
    }
}
