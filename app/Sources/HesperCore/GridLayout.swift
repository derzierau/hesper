import Foundation

public struct Rect: Equatable, Sendable {
    public var x, y, width, height: Double
    public init(x: Double, y: Double, width: Double, height: Double) { self.x = x; self.y = y; self.width = width; self.height = height }
    public var maxX: Double { x + width }
    public var maxY: Double { y + height }
    public var midX: Double { x + width / 2 }
    public var midY: Double { y + height / 2 }
}

/// One terminal cell in points (engine pixels / backing scale).
public struct CellSize: Equatable, Sendable {
    public var width: Double
    public var height: Double
    public init(width: Double, height: Double) { self.width = width; self.height = height }
}

/// How the wall arranges its cards.
public enum WallArrangement: String, CaseIterable, Codable, Sendable {
    /// E (default): active agents in a fill grid, quiet ones (done, idle,
    /// exited) as compact cards on a shelf below.
    case shelf
    /// B: every agent a full-height column; scrolls sideways.
    case columns
    /// C: squarified treemap weighted by state.
    case treemap
    /// D: the agent ⌘J goes to (else the selected one) full height, the
    /// rest stacked on the right.
    case mainStack
    /// Grid: every agent the same size (no weights, no shelf), grouped by
    /// project bands on one lattice with one column count for the whole
    /// wall (GridArrangement.swift).
    case grid

    public var title: String {
        switch self {
        case .shelf: return "Grid + Shelf"
        case .columns: return "Columns"
        case .treemap: return "Treemap"
        case .mainStack: return "Main + Stack"
        case .grid: return "Grid"
        }
    }
}

/// The wall's spacing scale, fonts and card limits.
public struct WallSpec: Equatable, Sendable {
    /// Space around the wall.
    public var margin: Double = 20
    /// Space between cards.
    public var gap: Double = 14
    /// A card's header bar.
    public var header: Double = 32
    /// Space between the card's edge/header/band and its terminal. 0: the
    /// terminal spans the card's body edge to edge (the app's wall).
    public var inset: Double = 0
    /// Breathing room between the body's edge and the terminal's text,
    /// painted in the terminal's own background (the body keeps its color
    /// edge to edge; only the text moves in).
    public var padX: Double = 0
    public var padY: Double = 0
    /// The one font of every tile (the user's terminal font or the Settings
    /// value): each tile shows as many cols × rows as fit its terminal area
    /// at this font, and asks the PTY for that grid (attach --fit).
    public var tileFont: Double = 12
    /// A card is at least `minChars` wide and `minRows` tall at `tileFont`.
    public var minChars: Int = 80
    public var minRows: Int = 8
    /// Quiet cards on the shelf.
    public var shelfHeight: Double = 74
    public var shelfMinWidth: Double = 220
    /// Main + stack: the main card's share of the width.
    public var mainShare: Double = 0.55
    /// Bands (projects): a band's header line, its padding around the
    /// cards, the space between bands, a collapsed lane's width (B).
    public var bandHeader: Double = 34
    public var bandPad: Double = 8
    public var bandGap: Double = 16
    public var collapsedLaneWidth: Double = 220
    /// The band's heading row inside `bandHeader` (the rest, below its
    /// hairline, is space before the cards).
    public var bandHeading: Double = 34
    public init() {}
}

extension WallSpec {
    /// The app's wall at a density (docs/design-system.md): the tile header
    /// (26 / 22) and gutter (8 / 6) from `DesignTokens.Density`, the margin
    /// (16 / 12), quiet band headings (a 28 / 24 row, its hairline, a
    /// gutter before the cards; no backdrop, so no padding around them).
    /// The terminal spans the body edge to edge (inset 0).
    /// A live card's footer: one line, always reserved (the app's
    /// `Metrics.footer`), so the terminal never changes height with the state.
    public static let footerLine: Double = 40

    public static func wall(_ d: DesignTokens.Density) -> WallSpec {
        var s = WallSpec()
        let compact = d == .compact
        s.margin = compact ? DesignTokens.Spacing.l : DesignTokens.Spacing.xl
        s.gap = d.tileGutter
        s.header = d.tileHeader
        s.inset = 0
        s.padX = compact ? DesignTokens.Spacing.m : DesignTokens.Spacing.l
        s.padY = compact ? DesignTokens.Spacing.xs : DesignTokens.Spacing.m
        s.minRows = 10
        // A shelf card: its header, the footer's one line and a gutter.
        s.shelfHeight = d.tileHeader + footerLine + DesignTokens.Spacing.m
        s.bandHeading = compact ? DesignTokens.Chrome.maxHeight - DesignTokens.Spacing.xs : DesignTokens.Chrome.maxHeight
        s.bandHeader = s.bandHeading + d.tileGutter
        // A band is a bordered container around its heading and cards.
        s.bandPad = d.tileGutter
        s.bandGap = d.bandGap
        return s
    }
}

/// What the layout needs to know about one agent's card.
public struct WallTileInput: Equatable, Sendable {
    /// The agent's PTY size.
    public var grid: GridSize
    public var state: AgentState
    /// Height of the card's attention band (0: none).
    public var band: Double
    public init(grid: GridSize, state: AgentState = .working, band: Double = 0) { self.grid = grid; self.state = state; self.band = band }

    /// Done, idle or exited: the shelf's.
    public var isQuiet: Bool { state == .done || state == .idle || state == .exited }
}

/// One card on the wall: its frame, and for a live card the terminal area
/// inside it (whole cells, bottom-anchored: the agent's last rows sit right
/// above the band), its font and the rows it shows. Shelf cards have none.
public struct WallTile: Equatable, Sendable {
    public var card: Rect
    public var quiet: Bool
    /// The terminal area in wall coordinates: `rows × cell.height` high,
    /// `card.width − 2 inset` wide (nil on the shelf). `cols × rows` is the
    /// grid the tile asks the PTY for (attach --fit).
    public var terminal: Rect?
    /// The whole body the terminal sits in (under the header, above the
    /// band, inset by `spec.inset`): the terminal's own background fills
    /// it, so the sub-cell remainder above `terminal` never shows another
    /// color. nil on the shelf.
    public var body: Rect? = nil
    public var font: Double
    public var cell: CellSize
    /// The agent's last rows the card shows (≤ the agent's rows).
    public var rows: Int
    /// Columns that fit the terminal area (< the agent's: cut on the right).
    public var cols: Int
    public var clipped: Bool
    /// In a collapsed band: not shown (no frame, no rendering).
    public var hidden = false
}

/// A band's place on the wall (E band or box, B lane, C nested rect).
public struct BandFrame: Equatable, Sendable {
    public enum Style: String, Sendable { case band, box, lane, nested }
    public var key: String
    /// The whole frame (header, cards, shelf strip, padding).
    public var frame: Rect
    public var header: Rect
    public var collapsed: Bool
    public var style: Style
    public init(key: String, frame: Rect, header: Rect, collapsed: Bool, style: Style) {
        self.key = key; self.frame = frame; self.header = header; self.collapsed = collapsed; self.style = style
    }
}

/// Lays the wall out. Cards fill the wall to its margin; a card's size picks
/// its terminal grid at the one tile font: as many whole columns and rows
/// as fit (the PTY follows the largest tile's grid; a narrower viewer is
/// cut on the right and shows the last rows). Cards stay at least
/// `minChars` × `minRows` at that font; where that does not fit the window
/// the wall scrolls (sideways for columns, else down).
public struct WallLayout: Equatable, Sendable {
    public var arrangement: WallArrangement
    public var tiles: [WallTile]
    public var contentWidth: Double
    public var contentHeight: Double
    /// Bands (projects), in band order; empty on a plain wall.
    public var bands: [BandFrame] = []
    /// E: each band's weight after hysteresis (BandAllocator), to pass
    /// back as `previous` next time.
    public var bandWeights: [String: Int] = [:]
    /// Grid: the wall's column count (0: another arrangement), to pass back
    /// as `previousColumns` next time (hysteresis).
    public var gridColumns: Int = 0
    public init(arrangement: WallArrangement, tiles: [WallTile], contentWidth: Double, contentHeight: Double,
                bands: [BandFrame] = [], bandWeights: [String: Int] = [:], gridColumns: Int = 0) {
        self.arrangement = arrangement; self.tiles = tiles; self.contentWidth = contentWidth; self.contentHeight = contentHeight
        self.bands = bands; self.bandWeights = bandWeights; self.gridColumns = gridColumns
    }

    public static let empty = WallLayout(arrangement: .shelf, tiles: [], contentWidth: 0, contentHeight: 0)

    /// The smallest card a terminal gets: `minChars` × `minRows` at the
    /// tile font.
    public static func minCard(_ spec: WallSpec, cell: (Double) -> CellSize) -> (width: Double, height: Double) {
        let c = cell(spec.tileFont)
        return (Double(spec.minChars) * c.width + 2 * spec.inset, spec.header + 2 * spec.inset + Double(spec.minRows) * c.height)
    }

    /// - Parameter main: the agent main + stack puts first (the ⌘J target,
    ///   else the selected one); nil: the first.
    public static func make(_ inputs: [WallTileInput], arrangement: WallArrangement, width: Double, height: Double,
                            spec: WallSpec = WallSpec(), main: Int? = nil, top: Double = 0, previousColumns: Int? = nil,
                            cell: (Double) -> CellSize) -> WallLayout {
        let n = inputs.count
        guard n > 0, width > 2 * spec.margin, height > 2 * spec.margin + top else {
            return WallLayout(arrangement: arrangement, tiles: [], contentWidth: width, contentHeight: height)
        }
        let area = Rect(x: spec.margin, y: spec.margin + top, width: width - 2 * spec.margin, height: height - 2 * spec.margin - top)
        let minCard = minCard(spec, cell: cell)
        let all = Array(0..<n)
        var rects = [Rect?](repeating: nil, count: n)
        var quiet = [Bool](repeating: false, count: n)
        func put(_ placed: [(Int, Rect)]) { for (i, r) in placed { rects[i] = r } }
        var gridColumns = 0

        switch arrangement {
        case .shelf:
            let q = all.filter { inputs[$0].isQuiet }, active = all.filter { !inputs[$0].isQuiet }
            if q.isEmpty || active.isEmpty {
                put(fillGrid(all, area, spec, minCard))
            } else {
                let shelfCols = max(1, Int((area.width + spec.gap) / (spec.shelfMinWidth + spec.gap)))
                let shelfRows = Int((Double(q.count) / Double(shelfCols)).rounded(.up))
                let shelfH = Double(shelfRows) * spec.shelfHeight + Double(shelfRows - 1) * spec.gap
                let gridArea = Rect(x: area.x, y: area.y, width: area.width, height: max(minCard.height, area.height - shelfH - spec.gap))
                let grid = fillGrid(active, gridArea, spec, minCard)
                put(grid)
                let gridBottom = grid.map { $0.1.maxY }.max() ?? area.y
                let per = Int((Double(q.count) / Double(shelfRows)).rounded(.up))
                var i = 0
                for r in 0..<shelfRows {
                    let k = min(per, q.count - i)
                    guard k > 0 else { break }
                    let w = (area.width - Double(k - 1) * spec.gap) / Double(k)
                    for c in 0..<k {
                        rects[q[i]] = Rect(x: area.x + Double(c) * (w + spec.gap), y: gridBottom + spec.gap + Double(r) * (spec.shelfHeight + spec.gap),
                                           width: w, height: spec.shelfHeight)
                        quiet[q[i]] = true
                        i += 1
                    }
                }
            }
        case .columns:
            let tw = max(minCard.width, (area.width - Double(n - 1) * spec.gap) / Double(n))
            let h = max(area.height, minCard.height)
            for i in all { rects[i] = Rect(x: area.x + Double(i) * (tw + spec.gap), y: area.y, width: tw, height: h) }
        case .treemap:
            put(treemap(all, inputs, area, spec))
        case .mainStack:
            put(mainStack(all, area, spec, minCard, main: main.flatMap { all.contains($0) ? $0 : nil } ?? 0))
        case .grid:
            let r = gridBands([GridGroup(key: nil, items: all, collapsed: false)], area, spec, minCard, previous: previousColumns)
            put(r.rects)
            gridColumns = r.columns
        }

        var tiles: [WallTile] = []
        for i in all {
            let r = rects[i] ?? Rect(x: area.x, y: area.y, width: 0, height: 0)
            tiles.append(fit(inputs[i], card: r, quiet: quiet[i], spec: spec, cell: cell))
        }
        let maxX = tiles.map(\.card.maxX).max() ?? 0, maxY = tiles.map(\.card.maxY).max() ?? 0
        return WallLayout(arrangement: arrangement, tiles: tiles,
                          contentWidth: max(width, maxX + spec.margin), contentHeight: max(height, maxY + spec.margin), gridColumns: gridColumns)
    }

    /// The terminal inside a card: whole cells at the tile font, filling the
    /// card's width and height, anchored at the bottom of the card's body.
    public static func fit(_ input: WallTileInput, card: Rect, quiet: Bool, spec: WallSpec, cell: (Double) -> CellSize) -> WallTile {
        let bodyW = max(0, card.width - 2 * spec.inset)
        let termW = max(0, bodyW - 2 * spec.padX)
        let font = spec.tileFont
        let c = cell(font)
        let fitCols = c.width > 0 ? max(1, Int((termW / c.width + 1e-9).rounded(.down))) : 1
        let cols = fitCols
        if quiet {
            return WallTile(card: card, quiet: true, terminal: nil, font: font, cell: c, rows: 0, cols: cols, clipped: fitCols < input.grid.cols)
        }
        let bodyBottom = card.maxY - input.band - spec.inset
        let bodyH = max(0, bodyBottom - (card.y + spec.header + spec.inset))
        let termH = max(0, bodyH - 2 * spec.padY)
        let rows = max(1, Int((termH / c.height + 1e-9).rounded(.down)))
        let h = Double(rows) * c.height
        let term = Rect(x: card.x + spec.inset + spec.padX, y: bodyBottom - spec.padY - h, width: termW, height: h)
        let body = Rect(x: card.x + spec.inset, y: card.y + spec.header + spec.inset, width: bodyW, height: bodyH)
        return WallTile(card: card, quiet: false, terminal: term, body: body, font: font, cell: c, rows: rows, cols: cols, clipped: fitCols < input.grid.cols)
    }

    // MARK: Arrangements (ported from the layout options page)

    /// A: the fewest rows such that every card keeps the minimum width; a
    /// short last row stretches to the full width. Rows never get shorter
    /// than the minimum card (the wall scrolls instead).
    static func fillGrid(_ items: [Int], _ r: Rect, _ spec: WallSpec, _ minCard: (width: Double, height: Double), rows atLeast: Int = 1) -> [(Int, Rect)] {
        let n = items.count
        guard n > 0 else { return [] }
        var rows = 1
        while rows <= n {
            let cols = Int((Double(n) / Double(rows)).rounded(.up))
            if (r.width - Double(cols - 1) * spec.gap) / Double(cols) >= minCard.width - 1e-9 || cols == 1 { break }
            rows += 1
        }
        rows = min(max(rows, atLeast), n)
        let cols = Int((Double(n) / Double(rows)).rounded(.up))
        let th = max(minCard.height, (r.height - Double(rows - 1) * spec.gap) / Double(rows))
        var out: [(Int, Rect)] = []
        var i = 0
        for row in 0..<rows {
            let inRow = min(cols, n - i)
            guard inRow > 0 else { break }
            let tw = (r.width - Double(inRow - 1) * spec.gap) / Double(inRow)
            for c in 0..<inRow {
                out.append((items[i], Rect(x: r.x + Double(c) * (tw + spec.gap), y: r.y + Double(row) * (th + spec.gap), width: tw, height: th)))
                i += 1
            }
        }
        return out
    }

    public static func weight(_ s: AgentState) -> Double {
        switch s {
        case .approval, .question, .error: return 4
        case .working, .starting, .unknown: return 2
        case .done, .idle, .exited: return 1
        }
    }

    /// C: squarified treemap, approvals/questions/errors weigh 4, working 2,
    /// quiet 1. The gaps are taken inside each rect (the area is grown by
    /// half a gap so the outer margin stays exact).
    static func treemap(_ items: [Int], _ inputs: [WallTileInput], _ area: Rect, _ spec: WallSpec) -> [(Int, Rect)] {
        let g = spec.gap
        var rect = Rect(x: area.x - g / 2, y: area.y - g / 2, width: area.width + g, height: area.height + g)
        let total = items.reduce(0.0) { $0 + weight(inputs[$1].state) }
        guard total > 0 else { return [] }
        let scale = rect.width * rect.height / total
        // Heaviest first; equal weights keep the wall's order.
        let list = items.enumerated()
            .map { (offset: $0.offset, i: $0.element, a: weight(inputs[$0.element].state) * scale) }
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

    /// D: `main` full height on the left (mainShare of the width, at least
    /// the minimum card), the rest in equal rows on the right, another stack
    /// column whenever rows would drop below the minimum card height.
    static func mainStack(_ items: [Int], _ area: Rect, _ spec: WallSpec, _ minCard: (width: Double, height: Double), main: Int) -> [(Int, Rect)] {
        guard items.count > 1 else { return fillGrid(items, area, spec, minCard) }
        let rest = items.filter { $0 != main }
        let mw = max(1, min(max(minCard.width, (area.width * spec.mainShare).rounded()), area.width - spec.gap - 1))
        let stack = Rect(x: area.x + mw + spec.gap, y: area.y, width: area.width - mw - spec.gap, height: area.height)
        let perCol = max(1, Int((stack.height + spec.gap) / (minCard.height + spec.gap)))
        let cols = Int((Double(rest.count) / Double(perCol)).rounded(.up))
        let cw = (stack.width - Double(cols - 1) * spec.gap) / Double(cols)
        var out: [(Int, Rect)] = [(main, Rect(x: area.x, y: area.y, width: mw, height: area.height))]
        var i = 0
        for c in 0..<cols {
            let k = min(perCol, rest.count - i)
            guard k > 0 else { break }
            let rh = (stack.height - Double(k - 1) * spec.gap) / Double(k)
            for r in 0..<k {
                out.append((rest[i], Rect(x: stack.x + Double(c) * (cw + spec.gap), y: stack.y + Double(r) * (rh + spec.gap), width: cw, height: rh)))
                i += 1
            }
        }
        return out
    }
}
