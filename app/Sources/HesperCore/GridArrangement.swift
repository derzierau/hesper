import Foundation

/// Grid (`WallArrangement.grid`): every card exactly the same size, no
/// weights, no main slot, no shelf (quiet agents are tiles like any other).
///
/// The wall is one lattice of `C` equal columns over its full width. Bands
/// (projects) are bordered boxes on that lattice, in band order: a band of
/// `k ≤ C` agents takes `k` adjacent cells of a band row and the next band
/// continues on the same row while it fits (so one-agent projects don't
/// each waste a row); a band of `k > C` agents is the full width, in rows
/// of `C`, its last row left-aligned. Between two boxes on a row the cells
/// keep the lattice's pitch (the gutter is two band paddings and a gap), so
/// every column lines up across the whole wall and the last column ends at
/// the right margin. Collapsed bands are their heading only, packed one
/// cell wide in a strip above the boxes. Without bands it is the plain
/// lattice.
///
/// `C` is chosen to fill the viewport: for every `C`, the cell width is
/// what `C` columns leave of the width, the cell height what the band rows
/// (their headings, padding and gaps) leave of the height divided by the
/// lattice rows. Among the `C` whose cell is at least the minimum card
/// (`minChars` × `minRows` at the tile font) and between `GridRule.minAspect`
/// and `GridRule.maxAspect` wide, the largest cell wins; else the fitting
/// `C` closest to that aspect range; else (nothing fits one screen) the most
/// columns that keep the minimum width, cells as tall as the minimum (or a
/// third of their width) and the wall scrolls down. A previous `C` stays
/// while it is still a valid choice nearly as good (`GridRule.keepShare`),
/// so a few points of resizing never flip the column count.
public enum GridRule {
    /// Terminal-friendly cells: wider than tall, not a strip.
    public static let minAspect = 1.2
    public static let maxAspect = 3.0
    /// Hysteresis: the previous column count stays while its cell keeps at
    /// least this share of the best cell's area.
    public static let keepShare = 0.9
}

/// One group on the grid: a band (its key) or the whole plain wall (nil).
struct GridGroup {
    var key: String?
    var items: [Int]
    var collapsed: Bool
}

/// The grid's cell for one column count.
public struct GridCell: Equatable, Sendable {
    public var columns: Int
    public var width: Double
    /// The height that fills the viewport (may be below the minimum).
    public var height: Double
    /// Lattice rows (all band rows' card rows).
    public var rows: Int
    public var aspect: Double { height > 0 ? width / height : .infinity }
    public var area: Double { width * max(0, height) }
}

extension WallLayout {
    /// A row of band boxes: each member a group (index into the open
    /// groups) and its first column; `rows`: lattice rows of the row.
    struct GridBandRow {
        var members: [(group: Int, column: Int)]
        var rows: Int
    }

    /// Bands onto a `c`-column lattice in order: a band that fits the rest
    /// of the current row goes there; a wider band gets rows of its own.
    static func gridPack(_ counts: [Int], columns c: Int) -> [GridBandRow] {
        var out: [GridBandRow] = []
        var cur: GridBandRow?
        var used = 0
        for (g, k) in counts.enumerated() {
            if k > c {
                if let r = cur { out.append(r); cur = nil; used = 0 }
                out.append(GridBandRow(members: [(g, 0)], rows: (k + c - 1) / c))
                continue
            }
            if var r = cur, used + k <= c {
                r.members.append((g, used))
                cur = r
                used += k
            } else {
                if let r = cur { out.append(r) }
                cur = GridBandRow(members: [(g, 0)], rows: 1)
                used = k
            }
        }
        if let r = cur { out.append(r) }
        return out
    }

    /// The lattice's measures: band chrome (zero on a plain wall) and the
    /// gutter between cells.
    struct GridChrome {
        var banded: Bool
        var heading: Double
        var pad: Double
        var bandGap: Double
        /// Between cells, both ways: on a banded wall two paddings and a
        /// gap, so cells of neighboring boxes keep the lattice's pitch.
        var gutter: Double
        var stripGap: Double

        init(banded: Bool, spec: WallSpec) {
            self.banded = banded
            heading = banded ? spec.bandHeader : 0
            pad = banded ? spec.bandPad : 0
            bandGap = banded ? spec.bandGap : 0
            gutter = banded ? 2 * spec.bandPad + spec.gap : spec.gap
            stripGap = spec.gap
        }
    }

    /// The cell `c` columns give in `area` (open band counts, `heads`
    /// heading-only bands).
    static func gridCell(_ counts: [Int], heads: Int, columns c: Int, _ area: Rect, _ ch: GridChrome) -> GridCell {
        let w = (area.width - 2 * ch.pad - Double(c - 1) * ch.gutter) / Double(c)
        let packed = gridPack(counts, columns: c)
        let rows = packed.reduce(0) { $0 + $1.rows }
        let strip = heads > 0 ? (heads + c - 1) / c : 0
        var fixed = Double(strip) * ch.heading + Double(max(0, strip - 1)) * ch.stripGap
        if strip > 0 && !packed.isEmpty { fixed += ch.bandGap }
        fixed += Double(packed.count) * (ch.heading + ch.pad) + Double(max(0, packed.count - 1)) * ch.bandGap
        fixed += packed.reduce(0.0) { $0 + Double($1.rows - 1) * ch.gutter }
        let h = rows > 0 ? (area.height - fixed) / Double(rows) : 0
        return GridCell(columns: c, width: w, height: h, rows: rows)
    }

    /// The column count and the cell (see the type's comment).
    static func gridChoice(_ counts: [Int], heads: Int, _ area: Rect, _ ch: GridChrome, minCard: (width: Double, height: Double),
                           previous: Int?) -> (columns: Int, width: Double, height: Double) {
        let n = counts.reduce(0, +)
        let maxC = max(1, n)
        let eps = 1e-9
        let cells = (1...maxC).map { gridCell(counts, heads: heads, columns: $0, area, ch) }
        func fits(_ c: GridCell) -> Bool { c.width >= minCard.width - eps && c.height >= minCard.height - eps }
        func inRange(_ c: GridCell) -> Bool { c.aspect >= GridRule.minAspect - eps && c.aspect <= GridRule.maxAspect + eps }
        /// How far the aspect is outside the range (log scale; 0 inside).
        func off(_ c: GridCell) -> Double {
            let a = c.aspect
            if a < GridRule.minAspect { return log(GridRule.minAspect / a) }
            if a > GridRule.maxAspect { return log(a / GridRule.maxAspect) }
            return 0
        }
        let prev = previous.flatMap { p in cells.first { $0.columns == p } }

        let good = cells.filter { fits($0) && inRange($0) }
        if let best = good.max(by: { $0.area < $1.area || ($0.area == $1.area && $0.columns > $1.columns) }) {
            if let p = prev, fits(p), inRange(p), p.area >= GridRule.keepShare * best.area { return (p.columns, p.width, p.height) }
            return (best.columns, best.width, best.height)
        }
        let fitting = cells.filter(fits)
        if let best = fitting.min(by: { off($0) < off($1) || (off($0) == off($1) && $0.area > $1.area) }) {
            if let p = prev, fits(p), off(p) <= off(best) + 0.1 { return (p.columns, p.width, p.height) }
            return (best.columns, best.width, best.height)
        }
        // Nothing fits one screen: the most columns of at least the minimum
        // width, minimum-height cells (or a third of their width), scrolling.
        var c = cells.last { $0.width >= minCard.width - eps }?.columns ?? 1
        if let p = prev, p.columns < c, p.width >= minCard.width - eps, cells[c - 1].width < minCard.width * 1.03 { c = p.columns }
        let w = cells[c - 1].width
        return (c, w, max(minCard.height, w / GridRule.maxAspect))
    }

    /// Lays the grid out: card rects, band frames, the column count.
    static func gridBands(_ groups: [GridGroup], _ area: Rect, _ spec: WallSpec, _ minCard: (width: Double, height: Double),
                          previous: Int?) -> (rects: [(Int, Rect)], frames: [BandFrame], columns: Int) {
        let banded = groups.contains { $0.key != nil }
        let ch = GridChrome(banded: banded, spec: spec)
        let open = groups.filter { !$0.collapsed && !$0.items.isEmpty }
        let heads = banded ? groups.filter { $0.collapsed || $0.items.isEmpty } : []
        let counts = open.map(\.items.count)
        let choice = gridChoice(counts, heads: heads.count, area, ch, minCard: minCard, previous: previous)
        let c = choice.columns, cw = choice.width, cellH = choice.height
        let g = ch.gutter
        func colX(_ i: Int) -> Double { area.x + ch.pad + Double(i) * (cw + g) }
        func span(_ k: Int) -> Double { Double(k) * cw + Double(max(0, k - 1)) * g + 2 * ch.pad }

        var rects: [(Int, Rect)] = []
        var frames: [BandFrame] = []
        var y = area.y
        // Collapsed (and empty) bands: their heading, one cell wide, packed.
        if !heads.isEmpty {
            let strip = (heads.count + c - 1) / c
            for (i, b) in heads.enumerated() {
                let r = Rect(x: colX(i % c) - ch.pad, y: y + Double(i / c) * (ch.heading + ch.stripGap), width: span(1), height: ch.heading)
                frames.append(BandFrame(key: b.key ?? "", frame: r, header: r, collapsed: b.collapsed, style: .box))
            }
            y += Double(strip) * ch.heading + Double(strip - 1) * ch.stripGap + ch.bandGap
        }
        for row in gridPack(counts, columns: c) {
            for m in row.members {
                let b = open[m.group]
                let k = b.items.count
                let top = y + ch.heading
                for (t, i) in b.items.enumerated() {
                    rects.append((i, Rect(x: colX(m.column + t % c), y: top + Double(t / c) * (cellH + g), width: cw, height: cellH)))
                }
                if let key = b.key {
                    let wide = min(k, c)
                    let rows = (k + c - 1) / c
                    let frame = Rect(x: colX(m.column) - ch.pad, y: y, width: span(wide),
                                     height: ch.heading + Double(rows) * cellH + Double(rows - 1) * g + ch.pad)
                    frames.append(BandFrame(key: key, frame: frame, header: Rect(x: frame.x, y: y, width: frame.width, height: ch.heading),
                                            collapsed: false, style: wide == c ? .band : .box))
                }
            }
            y += ch.heading + Double(row.rows) * cellH + Double(row.rows - 1) * g + ch.pad + ch.bandGap
        }
        // Frames in band order.
        let order = Dictionary(groups.enumerated().compactMap { i, b in b.key.map { ($0, i) } }, uniquingKeysWith: { a, _ in a })
        frames.sort { (order[$0.key] ?? 0) < (order[$1.key] ?? 0) }
        return (rects, frames, c)
    }
}
