import Foundation

/// One band as the layout sees it: its cards (indices into the inputs, in
/// wall order) and whether it is collapsed to its header line.
public struct BandSlot: Equatable, Sendable {
    public var key: String
    public var items: [Int]
    public var collapsed: Bool
    public init(key: String, items: [Int], collapsed: Bool = false) { self.key = key; self.items = items; self.collapsed = collapsed }
}

/// Grouped walls (docs/rebuild-contract.md "As built — projects (views)").
/// Each arrangement groups in its own way:
///
/// - **E grid + shelf**: full-width **bands** (header, the band's own fill
///   grid, its own shelf strip at the end); side-by-side **boxes** only when
///   every box gets at least two minimum cards of width. Vertical space goes
///   by grid rows, from each band's number of active agents with hysteresis
///   (`BandAllocator`), so only agents starting, stopping or moving change
///   it — never attention. Every grid row on the wall has the same height.
/// - **B columns**: one **lane** per band (header over its columns), the
///   wall scrolls sideways.
/// - **C treemap**: **nested** — bands by the weight of their agents first,
///   then the agents inside each band's rect.
/// - **Grid**: every card the same size, one column count for the wall;
///   bands are boxes on one lattice (GridArrangement.swift).
/// - **D main + stack**: no frames (the main slot follows ⌘J across
///   projects); the order is by band, tiles carry the project tint.
///
/// Collapsed bands are one header line (E, C: stacked first in boxes / C;
/// B: a narrow lane); their cards are `hidden`. Card terminals fit as on a
/// plain wall (one tile font, `fit`).
extension WallLayout {
    public static func make(_ inputs: [WallTileInput], bands: [BandSlot], arrangement: WallArrangement, width: Double, height: Double,
                            spec: WallSpec = WallSpec(), main: Int? = nil, top: Double = 0, previousWeights: [String: Int] = [:],
                            previousColumns: Int? = nil, cell: (Double) -> CellSize) -> WallLayout {
        guard !bands.isEmpty, arrangement != .mainStack else {
            return make(inputs, arrangement: arrangement, width: width, height: height, spec: spec, main: main, top: top,
                        previousColumns: previousColumns, cell: cell)
        }
        let n = inputs.count
        guard width > 2 * spec.margin, height > 2 * spec.margin + top else {
            return WallLayout(arrangement: arrangement, tiles: [], contentWidth: width, contentHeight: height)
        }
        let area = Rect(x: spec.margin, y: spec.margin + top, width: width - 2 * spec.margin, height: height - 2 * spec.margin - top)
        let minCard = minCard(spec, cell: cell)
        var rects = [Rect?](repeating: nil, count: n)
        var quiet = [Bool](repeating: false, count: n)
        var hidden = [Bool](repeating: false, count: n)
        var frames: [BandFrame] = []
        var weights: [String: Int] = [:]
        var gridColumns = 0
        for b in bands where b.collapsed { for i in b.items { hidden[i] = true } }

        switch arrangement {
        case .shelf:
            let r = shelfBands(inputs, bands, area, spec, minCard, previousWeights)
            for (i, rect) in r.rects { rects[i] = rect }
            for i in r.quiet { quiet[i] = true }
            frames = r.frames
            weights = r.weights
        case .columns:
            let r = lanes(bands, area, spec, minCard)
            for (i, rect) in r.rects { rects[i] = rect }
            frames = r.frames
        case .treemap:
            let r = nested(inputs, bands, area, spec)
            for (i, rect) in r.rects { rects[i] = rect }
            frames = r.frames
        case .grid:
            let r = gridBands(bands.map { GridGroup(key: $0.key, items: $0.items, collapsed: $0.collapsed) }, area, spec, minCard,
                              previous: previousColumns)
            for (i, rect) in r.rects { rects[i] = rect }
            frames = r.frames
            gridColumns = r.columns
        case .mainStack:
            break
        }

        var tiles: [WallTile] = []
        for i in 0..<n {
            let r = rects[i] ?? Rect(x: area.x, y: area.y, width: 0, height: 0)
            var t = fit(inputs[i], card: hidden[i] ? Rect(x: r.x, y: r.y, width: 0, height: 0) : r, quiet: quiet[i] || hidden[i], spec: spec, cell: cell)
            t.hidden = hidden[i]
            tiles.append(t)
        }
        let shown = tiles.filter { !$0.hidden }
        let maxX = max(shown.map(\.card.maxX).max() ?? 0, frames.map(\.frame.maxX).max() ?? 0)
        let maxY = max(shown.map(\.card.maxY).max() ?? 0, frames.map(\.frame.maxY).max() ?? 0)
        return WallLayout(arrangement: arrangement, tiles: tiles, contentWidth: max(width, maxX + spec.margin),
                          contentHeight: max(height, maxY + spec.margin), bands: frames, bandWeights: weights, gridColumns: gridColumns)
    }

    // MARK: E — bands, or boxes on wide walls

    /// Whether E draws side-by-side boxes: every box (of `count` per row)
    /// gets at least two minimum cards of width.
    public static func boxesPerRow(width: Double, spec: WallSpec, minCardWidth: Double) -> Int {
        let boxMin = 2 * minCardWidth + spec.gap + 2 * spec.bandPad
        return max(1, Int(((width + spec.bandGap) / (boxMin + spec.bandGap)).rounded(.down)))
    }

    struct Placed {
        var rects: [(Int, Rect)] = []
        var quiet: [Int] = []
        var frames: [BandFrame] = []
        var weights: [String: Int] = [:]
    }

    static func shelfBands(_ inputs: [WallTileInput], _ bands: [BandSlot], _ area: Rect, _ spec: WallSpec,
                           _ minCard: (width: Double, height: Double), _ previous: [String: Int]) -> Placed {
        var out = Placed()
        let open = bands.filter { !$0.collapsed }
        // Nothing active anywhere: every card is a live card (as a plain
        // all-quiet wall).
        let anyActive = open.contains { $0.items.contains { !inputs[$0].isQuiet } }
        func active(_ b: BandSlot) -> [Int] { anyActive ? b.items.filter { !inputs[$0].isQuiet } : b.items }
        func shelved(_ b: BandSlot) -> [Int] { anyActive ? b.items.filter { inputs[$0].isQuiet } : [] }
        out.weights = BandAllocator.weights(active: Dictionary(open.map { ($0.key, active($0).count) }, uniquingKeysWith: { a, _ in a }),
                                            previous: previous)
        let perRow = boxesPerRow(width: area.width, spec: spec, minCardWidth: minCard.width)
        var y = area.y
        let h = spec.bandHeader, pad = spec.bandPad, g = spec.gap

        func shelfHeight(_ q: Int, width: Double) -> (rows: Int, cols: Int, height: Double) {
            guard q > 0 else { return (0, 0, 0) }
            let cols = max(1, Int((width + g) / (spec.shelfMinWidth + g)))
            let rows = Int((Double(q) / Double(cols)).rounded(.up))
            return (rows, cols, Double(rows) * spec.shelfHeight + Double(rows - 1) * g)
        }
        func placeShelf(_ q: [Int], x: Double, y: Double, width: Double) {
            let s = shelfHeight(q.count, width: width)
            guard s.rows > 0 else { return }
            let per = Int((Double(q.count) / Double(s.rows)).rounded(.up))
            var i = 0
            for r in 0..<s.rows {
                let k = min(per, q.count - i)
                guard k > 0 else { break }
                let w = (width - Double(k - 1) * g) / Double(k)
                for c in 0..<k {
                    out.rects.append((q[i], Rect(x: x + Double(c) * (w + g), y: y + Double(r) * (spec.shelfHeight + g), width: w, height: spec.shelfHeight)))
                    out.quiet.append(q[i])
                    i += 1
                }
            }
        }
        func gridRows(_ b: BandSlot, cols: Int) -> Int {
            let w = out.weights[b.key] ?? 0
            return w == 0 || active(b).isEmpty ? 0 : Int((Double(w) / Double(max(1, cols))).rounded(.up))
        }

        if perRow >= 2 && open.count >= 2 {
            // Boxes: collapsed bands as header lines first, then rows of boxes.
            for b in bands where b.collapsed {
                let r = Rect(x: area.x, y: y, width: area.width, height: h)
                out.frames.append(BandFrame(key: b.key, frame: r, header: r, collapsed: true, style: .band))
                y += h + spec.bandGap / 2
            }
            let k = min(perRow, open.count)
            let rowsOfBoxes = Int((Double(open.count) / Double(k)).rounded(.up))
            var boxRows: [[BandSlot]] = []
            for r in 0..<rowsOfBoxes { boxRows.append(Array(open[(r * k)..<min(open.count, (r + 1) * k)])) }
            func boxWidth(_ m: Int) -> Double { (area.width - Double(m - 1) * spec.bandGap) / Double(m) }
            func cols(_ bw: Double) -> Int { max(1, Int(((bw - 2 * pad + g) / (minCard.width + g)).rounded(.down))) }
            // Per box row: the most grid rows of its boxes, the tallest shelf.
            var need: [(rows: Int, shelf: Double)] = []
            for row in boxRows {
                let bw = boxWidth(row.count)
                need.append((row.map { gridRows($0, cols: cols(bw)) }.max() ?? 0,
                             row.map { shelfHeight(shelved($0).count, width: bw - 2 * pad).height }.max() ?? 0))
            }
            let fixed = need.reduce(0.0) { acc, n in
                acc + h + pad + n.shelf + (n.rows > 0 && n.shelf > 0 ? g : 0) + Double(max(0, n.rows - 1)) * g
            } + Double(boxRows.count - 1) * spec.bandGap
            let totalRows = need.reduce(0) { $0 + $1.rows }
            let rowH = totalRows > 0 ? max(minCard.height, (area.y + area.height - y - fixed) / Double(totalRows)) : 0
            for (ri, row) in boxRows.enumerated() {
                let bw = boxWidth(row.count)
                let gridH = need[ri].rows > 0 ? Double(need[ri].rows) * rowH + Double(need[ri].rows - 1) * g : 0
                let boxH = h + gridH + (need[ri].rows > 0 && need[ri].shelf > 0 ? g : 0) + need[ri].shelf + pad
                for (bi, b) in row.enumerated() {
                    let x = area.x + Double(bi) * (bw + spec.bandGap)
                    let inner = bw - 2 * pad
                    let q = shelved(b), a = active(b)
                    let s = shelfHeight(q.count, width: inner).height
                    let myGrid = a.isEmpty ? 0 : boxH - h - pad - s - (s > 0 ? g : 0)
                    if !a.isEmpty {
                        out.rects += fillGrid(a, Rect(x: x + pad, y: y + h, width: inner, height: myGrid), spec, minCard,
                                              rows: gridRows(b, cols: cols(bw)))
                    }
                    placeShelf(q, x: x + pad, y: y + h + (a.isEmpty ? 0 : myGrid + g), width: inner)
                    let frame = Rect(x: x, y: y, width: bw, height: boxH)
                    out.frames.append(BandFrame(key: b.key, frame: frame, header: Rect(x: x, y: y, width: bw, height: h), collapsed: false, style: .box))
                }
                y += boxH + spec.bandGap
            }
            // Frames in band order (collapsed ones were added first).
            let order = Dictionary(bands.enumerated().map { ($1.key, $0) }, uniquingKeysWith: { a, _ in a })
            out.frames.sort { (order[$0.key] ?? 0) < (order[$1.key] ?? 0) }
            return out
        }

        // Bands: full width, stacked; every grid row the same height.
        let inner = area.width - 2 * pad
        let colsFit = max(1, Int(((inner + g) / (minCard.width + g)).rounded(.down)))
        var fixed = Double(bands.count - 1) * spec.bandGap
        var totalRows = 0
        for b in bands {
            if b.collapsed { fixed += h; continue }
            let rows = gridRows(b, cols: colsFit)
            let s = shelfHeight(shelved(b).count, width: inner).height
            fixed += h + pad + s + (rows > 0 && s > 0 ? g : 0) + Double(max(0, rows - 1)) * g
            totalRows += rows
        }
        let rowH = totalRows > 0 ? max(minCard.height, (area.height - fixed) / Double(totalRows)) : 0
        for b in bands {
            let header = Rect(x: area.x, y: y, width: area.width, height: h)
            if b.collapsed {
                out.frames.append(BandFrame(key: b.key, frame: header, header: header, collapsed: true, style: .band))
                y += h + spec.bandGap
                continue
            }
            var gy = y + h
            let rows = gridRows(b, cols: colsFit)
            if rows > 0 {
                let gridH = Double(rows) * rowH + Double(rows - 1) * g
                out.rects += fillGrid(active(b), Rect(x: area.x + pad, y: gy, width: inner, height: gridH), spec, minCard, rows: rows)
                gy += gridH
            }
            let q = shelved(b)
            if !q.isEmpty {
                if rows > 0 { gy += g }
                placeShelf(q, x: area.x + pad, y: gy, width: inner)
                gy += shelfHeight(q.count, width: inner).height
            }
            let bottom = gy + pad
            out.frames.append(BandFrame(key: b.key, frame: Rect(x: area.x, y: y, width: area.width, height: bottom - y), header: header,
                                        collapsed: false, style: .band))
            y = bottom + spec.bandGap
        }
        return out
    }

    // MARK: B — lanes

    static func lanes(_ bands: [BandSlot], _ area: Rect, _ spec: WallSpec, _ minCard: (width: Double, height: Double)) -> Placed {
        var out = Placed()
        let h = spec.bandHeader, pad = spec.bandPad, g = spec.gap
        let open = bands.filter { !$0.collapsed }
        let n = open.reduce(0) { $0 + $1.items.count }
        let closed = Double(bands.count - open.count)
        let gaps = closed * (spec.collapsedLaneWidth) + Double(bands.count - 1) * spec.bandGap
            + Double(open.count) * 2 * pad + Double(max(0, n - open.count)) * g
        let tw = n > 0 ? max(minCard.width, (area.width - gaps) / Double(n)) : minCard.width
        let cardH = max(minCard.height, area.height - h - pad)
        var x = area.x
        for b in bands {
            if b.collapsed {
                let r = Rect(x: x, y: area.y, width: spec.collapsedLaneWidth, height: h)
                out.frames.append(BandFrame(key: b.key, frame: r, header: r, collapsed: true, style: .lane))
                x += spec.collapsedLaneWidth + spec.bandGap
                continue
            }
            let m = b.items.count
            let laneW = 2 * pad + Double(m) * tw + Double(max(0, m - 1)) * g
            for (k, i) in b.items.enumerated() {
                out.rects.append((i, Rect(x: x + pad + Double(k) * (tw + g), y: area.y + h, width: tw, height: cardH)))
            }
            out.frames.append(BandFrame(key: b.key, frame: Rect(x: x, y: area.y, width: laneW, height: h + cardH + pad),
                                        header: Rect(x: x, y: area.y, width: laneW, height: h), collapsed: false, style: .lane))
            x += laneW + spec.bandGap
        }
        return out
    }

    // MARK: C — nested treemap

    static func nested(_ inputs: [WallTileInput], _ bands: [BandSlot], _ area: Rect, _ spec: WallSpec) -> Placed {
        var out = Placed()
        let h = spec.bandHeader, pad = spec.bandPad
        var y = area.y
        for b in bands where b.collapsed {
            let r = Rect(x: area.x, y: y, width: area.width, height: h)
            out.frames.append(BandFrame(key: b.key, frame: r, header: r, collapsed: true, style: .nested))
            y += h + spec.bandGap / 2
        }
        let open = bands.filter { !$0.collapsed }
        let rest = Rect(x: area.x, y: y, width: area.width, height: max(1, area.y + area.height - y))
        let ws = open.enumerated().map { (offset: $0.offset, weight: $0.element.items.reduce(0.0) { $0 + weight(inputs[$1].state) }) }
        let outer = squarify(ws.map { ($0.offset, $0.weight) }, rest, gap: spec.bandGap)
        for (bi, r) in outer {
            let b = open[bi]
            let header = Rect(x: r.x, y: r.y, width: r.width, height: h)
            let inner = Rect(x: r.x + pad, y: r.y + h, width: max(1, r.width - 2 * pad), height: max(1, r.height - h - pad))
            out.rects += treemap(b.items, inputs, inner, spec)
            out.frames.append(BandFrame(key: b.key, frame: r, header: header, collapsed: false, style: .nested))
        }
        let order = Dictionary(bands.enumerated().map { ($1.key, $0) }, uniquingKeysWith: { a, _ in a })
        out.frames.sort { (order[$0.key] ?? 0) < (order[$1.key] ?? 0) }
        return out
    }

    /// Squarified treemap of weighted items in `area`, `gap` between rects
    /// (outer edges exact).
    static func squarify(_ items: [(Int, Double)], _ area: Rect, gap g: Double) -> [(Int, Rect)] {
        var rect = Rect(x: area.x - g / 2, y: area.y - g / 2, width: area.width + g, height: area.height + g)
        let total = items.reduce(0.0) { $0 + $1.1 }
        guard total > 0 else { return [] }
        let scale = rect.width * rect.height / total
        let list = items.enumerated().map { (offset: $0.offset, i: $0.element.0, a: $0.element.1 * scale) }
            .sorted { l, r in l.a != r.a ? l.a > r.a : l.offset < r.offset }
            .map { (i: $0.i, a: $0.a) }
        var out: [(Int, Rect)] = []
        func worst(_ row: [(i: Int, a: Double)], _ side: Double) -> Double {
            let s = row.reduce(0) { $0 + $1.a }
            return row.map { max(side * side * $0.a / (s * s), (s * s) / (side * side * $0.a)) }.max() ?? .infinity
        }
        var row: [(i: Int, a: Double)] = []
        func layoutRow() {
            let s = row.reduce(0) { $0 + $1.a }
            if rect.width >= rect.height {
                let w = s / rect.height
                var y = rect.y
                for r in row { let h = r.a / w; out.append((r.i, Rect(x: rect.x, y: y, width: w, height: h))); y += h }
                rect = Rect(x: rect.x + w, y: rect.y, width: rect.width - w, height: rect.height)
            } else {
                let h = s / rect.width
                var x = rect.x
                for r in row { let w = r.a / h; out.append((r.i, Rect(x: x, y: rect.y, width: w, height: h))); x += w }
                rect = Rect(x: rect.x, y: rect.y + h, width: rect.width, height: rect.height - h)
            }
            row = []
        }
        for it in list {
            let side = min(rect.width, rect.height)
            if row.isEmpty || worst(row + [it], side) <= worst(row, side) { row.append(it) } else { layoutRow(); row.append(it) }
        }
        if !row.isEmpty { layoutRow() }
        return out.map { ($0.0, Rect(x: $0.1.x + g / 2, y: $0.1.y + g / 2, width: max(0, $0.1.width - g), height: max(0, $0.1.height - g))) }
    }

    /// The band frame a point is in (header drag, clicks).
    public func band(at x: Double, _ y: Double) -> BandFrame? {
        bands.first { x >= $0.frame.x && x <= $0.frame.maxX && y >= $0.frame.y && y <= $0.frame.maxY }
    }
}
