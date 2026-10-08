import Foundation
import Testing
@testable import HesperCore

@Suite struct WallLayoutMath {
    let table = FontFit.CellTable(scale: 2, ratio: FontFit.CellRatio(widthPerPoint: 0.6, heightPerPoint: 1.3))
    let spec = WallSpec()
    func cell(_ f: Double) -> CellSize { table.cell(f) }
    var minCard: (width: Double, height: Double) { WallLayout.minCard(spec, cell: cell) }

    func agents(_ n: Int, _ g: GridSize = GridSize(cols: 120, rows: 40), _ s: AgentState = .working) -> [WallTileInput] {
        Array(repeating: WallTileInput(grid: g, state: s), count: n)
    }
    func layout(_ inputs: [WallTileInput], _ a: WallArrangement, _ w: Double, _ h: Double, main: Int? = nil) -> WallLayout {
        WallLayout.make(inputs, arrangement: a, width: w, height: h, spec: spec, main: main, cell: cell)
    }
    func noOverlap(_ l: WallLayout) -> Bool {
        for (i, a) in l.tiles.enumerated() {
            for b in l.tiles[(i + 1)...] {
                let o = a.card.x < b.card.maxX - 1e-6 && b.card.x < a.card.maxX - 1e-6 && a.card.y < b.card.maxY - 1e-6 && b.card.y < a.card.maxY - 1e-6
                if o { return false }
            }
        }
        return true
    }
    func close(_ a: Double, _ b: Double) -> Bool { abs(a - b) < 1e-6 }

    @Test func empty() {
        #expect(layout([], .shelf, 1000, 800).tiles.isEmpty)
        #expect(layout(agents(3), .shelf, 0, 800).tiles.isEmpty)
    }

    /// The terminal area is whole rows that fill the body but for less than
    /// a cell, in whole engine pixels, at the bottom of the body.
    @Test func terminalFitsTheCardWithLessThanACellOfSlack() {
        for a in WallArrangement.allCases {
            for (w, h) in [(2000.0, 1240.0), (1440, 860), (1000, 700), (2560, 1400)] {
                for n in [1, 2, 3, 5, 7, 12, 16] {
                    var inputs = agents(n)
                    if n > 2 { inputs[1].band = 46 }
                    let l = layout(inputs, a, w, h)
                    #expect(l.tiles.count == n)
                    for (i, t) in l.tiles.enumerated() {
                        guard let term = t.terminal else { Issue.record("no terminal"); continue }
                        #expect(close(term.height, Double(t.rows) * t.cell.height))
                        let px = term.height * table.scale
                        #expect(abs(px - px.rounded()) < 1e-6, "whole pixels")
                        let body = t.card.height - spec.header - 2 * spec.inset - inputs[i].band
                        let slack = body - term.height
                        if t.rows > 1 { #expect(slack >= -1e-6, "\(a) n=\(n) \(w)x\(h): terminal taller than the body") }
                        if t.rows < inputs[i].grid.rows && t.rows > 1 {
                            #expect(slack < t.cell.height, "\(a) n=\(n) \(w)x\(h): slack \(slack)")
                        }
                        #expect(close(term.maxY, t.card.maxY - inputs[i].band - spec.inset), "bottom-anchored")
                        #expect(close(term.x, t.card.x + spec.inset) && close(term.width, t.card.width - 2 * spec.inset))
                    }
                }
            }
        }
    }

    /// Edge to edge (inset 0, the app's wall): the terminal body is the
    /// whole card under the header and above the band; the grid is the
    /// whole cells that fit it (cols × rows asked of the PTY), the rows
    /// bottom-anchored in it, and the remainder (< one cell) is in the body
    /// (painted in the terminal's background), never outside it.
    @Test func terminalSpansTheBodyEdgeToEdge() {
        #expect(WallSpec().inset == 0)
        for a in WallArrangement.allCases {
            for (w, h) in [(1728.0, 1040.0), (1280, 760), (2560, 1400)] {
                var inputs = agents(5)
                inputs[2].band = 40
                let l = layout(inputs, a, w, h)
                for (i, t) in l.tiles.enumerated() where !t.quiet {
                    guard let term = t.terminal, let body = t.body else { Issue.record("no terminal/body"); continue }
                    #expect(close(body.x, t.card.x) && close(body.width, t.card.width), "full width")
                    #expect(close(body.y, t.card.y + spec.header), "right under the header")
                    #expect(close(body.maxY, t.card.maxY - inputs[i].band), "down to the band")
                    #expect(close(term.x, body.x) && close(term.width, body.width) && close(term.maxY, body.maxY))
                    #expect(term.y >= body.y - 1e-6, "inside the body")
                    let cols = Int((body.width / t.cell.width + 1e-9).rounded(.down))
                    let rows = max(1, Int((body.height / t.cell.height + 1e-9).rounded(.down)))
                    #expect(t.cols == cols && t.rows == rows, "\(a) \(w)x\(h): grid \(t.cols)x\(t.rows), fits \(cols)x\(rows)")
                    if rows > 1 { #expect(body.height - term.height < t.cell.height, "less than a row of remainder") }
                    #expect(body.width - Double(t.cols) * t.cell.width < t.cell.width, "less than a column of remainder")
                }
            }
        }
    }

    /// A non-zero inset still frames the terminal (WallSpec stays general).
    @Test func insetStillShrinksTheBody() {
        var s = spec
        s.inset = 9
        let l = WallLayout.make(agents(2), arrangement: .shelf, width: 1600, height: 900, spec: s, cell: cell)
        for t in l.tiles {
            let body = t.body!
            #expect(close(body.x, t.card.x + 9) && close(body.width, t.card.width - 18))
            #expect(close(body.y, t.card.y + s.header + 9) && close(t.terminal!.maxY, t.card.maxY - 9))
        }
    }

    /// The shelf has no terminal and no body.
    @Test func shelfCardsHaveNoBody() {
        let inputs = agents(2) + agents(3, GridSize(cols: 120, rows: 40), .done)
        let l = layout(inputs, .shelf, 1600, 1000)
        for t in l.tiles where t.quiet { #expect(t.body == nil && t.terminal == nil) }
    }

    @Test func everyTileUsesTheOneFontAndFillsItsCard() {
        // Whatever the agents' PTY sizes (a 231x60 left over from focus
        // included), every tile has the same font and a grid that fills its
        // terminal area: the PTY follows the tile, not the other way round.
        let inputs = [WallTileInput(grid: GridSize(cols: 120, rows: 40)), WallTileInput(grid: GridSize(cols: 231, rows: 60)),
                      WallTileInput(grid: GridSize(cols: 80, rows: 24))]
        for a in WallArrangement.allCases {
            let l = layout(inputs, a, 1600, 1000)
            for t in l.tiles where !t.quiet {
                #expect(t.font == spec.tileFont)
                let w = t.card.width - 2 * spec.inset
                #expect(Double(t.cols) * t.cell.width <= w + 1e-9 && Double(t.cols + 1) * t.cell.width > w)
                let term = t.terminal!
                #expect(term.height == Double(t.rows) * t.cell.height)
            }
        }
    }

    /// A (inside E): the fewest rows that keep every card ≥ minChars; cards
    /// fill the area to the margin; the last row stretches.
    @Test func fillGridFillsTheWindow() {
        for (w, h) in [(1728.0, 1040.0), (2560, 1370), (1280, 760)] {
            for n in 1...12 {
                let l = layout(agents(n), .shelf, w, h)
                #expect(noOverlap(l))
                let ys = Set(l.tiles.map(\.card.y)).sorted()
                let scrolls = l.contentHeight > h + 0.5
                #expect(close(l.tiles.map(\.card.x).min()!, spec.margin) && close(l.tiles.map(\.card.y).min()!, spec.margin))
                if !scrolls { #expect(close(l.tiles.map(\.card.maxY).max()!, h - spec.margin), "\(n) in \(w)x\(h) fills the height") }
                for y in ys {
                    let row = l.tiles.filter { $0.card.y == y }
                    #expect(close(row.map(\.card.maxX).max()!, w - spec.margin), "every row reaches the right margin")
                    if row.count > 1 { #expect(row.allSatisfy { $0.card.width >= minCard.width - 1e-6 }) }
                }
                // Fewest rows: one row less would make cards narrower than the minimum.
                if ys.count > 1 {
                    let cols = Int((Double(n) / Double(ys.count - 1)).rounded(.up))
                    #expect((w - 2 * spec.margin - Double(cols - 1) * spec.gap) / Double(cols) < minCard.width)
                }
                #expect(l.tiles.allSatisfy { $0.card.height >= minCard.height - 1e-6 })
            }
        }
    }

    @Test func shelfHoldsQuietAgentsBelowTheGrid() {
        var inputs = agents(7)
        inputs[1].state = .done; inputs[4].state = .idle; inputs[6].state = .exited
        inputs[2].state = .approval
        let l = layout(inputs, .shelf, 1728, 1040)
        let quiet = [1, 4, 6], active = [0, 2, 3, 5]
        #expect(quiet.allSatisfy { l.tiles[$0].quiet && l.tiles[$0].terminal == nil && l.tiles[$0].card.height == spec.shelfHeight })
        #expect(active.allSatisfy { !l.tiles[$0].quiet && l.tiles[$0].terminal != nil })
        let shelfTop = quiet.map { l.tiles[$0].card.y }.min()!
        let gridBottom = active.map { l.tiles[$0].card.maxY }.max()!
        #expect(close(shelfTop, gridBottom + spec.gap))
        #expect(close(quiet.map { l.tiles[$0].card.maxY }.max()!, 1040 - spec.margin))
        #expect(close(quiet.map { l.tiles[$0].card.maxX }.max()!, 1728 - spec.margin))
        #expect(noOverlap(l))
        // An approval does not move anything: same frames as working.
        var calm = inputs
        calm[2].state = .working
        let l2 = layout(calm, .shelf, 1728, 1040)
        #expect(l2.tiles.map(\.card) == l.tiles.map(\.card))
        // All quiet: the fill grid, no shelf.
        let allQuiet = layout(agents(4, GridSize(cols: 120, rows: 40), .done), .shelf, 1728, 1040)
        #expect(allQuiet.tiles.allSatisfy { !$0.quiet && $0.card.height > spec.shelfHeight })
        // Many quiet agents wrap to more shelf rows.
        var many = agents(14, GridSize(cols: 120, rows: 40), .done)
        many[0].state = .working
        let wrapped = layout(many, .shelf, 1280, 900)
        #expect(Set(wrapped.tiles.filter(\.quiet).map(\.card.y)).count >= 2)
        #expect(wrapped.tiles.filter(\.quiet).allSatisfy { $0.card.width >= spec.shelfMinWidth - 1e-6 })
    }

    @Test func columnsAreFullHeightAndScrollSideways() {
        let few = layout(agents(3), .columns, 2560, 1400)
        #expect(few.tiles.allSatisfy { close($0.card.y, spec.margin) && close($0.card.height, 1400 - 2 * spec.margin) })
        #expect(close(few.tiles.last!.card.maxX, 2560 - spec.margin) && few.contentWidth == 2560)
        let many = layout(agents(12), .columns, 1728, 1040)
        #expect(many.tiles.allSatisfy { close($0.card.width, minCard.width) })
        #expect(many.contentWidth > 1728 && close(many.contentWidth, many.tiles.last!.card.maxX + spec.margin))
        #expect(noOverlap(many))
    }

    @Test func treemapWeighsWhatNeedsYou() {
        var inputs = agents(7)
        inputs[3].state = .approval; inputs[5].state = .done
        let l = layout(inputs, .treemap, 1728, 1040)
        #expect(noOverlap(l))
        let area = { (t: WallTile) in (t.card.width + spec.gap) * (t.card.height + spec.gap) }
        #expect(area(l.tiles[3]) > area(l.tiles[0]) && area(l.tiles[0]) > area(l.tiles[5]))
        #expect(abs(area(l.tiles[3]) / area(l.tiles[5]) - 4) < 0.01)
        #expect(close(l.tiles.map(\.card.x).min()!, spec.margin) && close(l.tiles.map(\.card.maxX).max()!, 1728 - spec.margin))
        #expect(close(l.tiles.map(\.card.y).min()!, spec.margin) && close(l.tiles.map(\.card.maxY).max()!, 1040 - spec.margin))
        let total = l.tiles.map(area).reduce(0, +)
        #expect(abs(total - (1728 - 2 * spec.margin + spec.gap) * (1040 - 2 * spec.margin + spec.gap)) < 1)
    }

    @Test func mainStackPutsTheMainAgentFirst() {
        let l = layout(agents(7), .mainStack, 1728, 1040, main: 4)
        let m = l.tiles[4].card
        #expect(close(m.x, spec.margin) && close(m.y, spec.margin) && close(m.height, 1040 - 2 * spec.margin))
        #expect(m.width >= minCard.width && close(m.width, ((1728 - 2 * spec.margin) * spec.mainShare).rounded()))
        #expect(l.tiles.enumerated().filter { $0.offset != 4 }.allSatisfy { $0.element.card.x > m.maxX })
        #expect(noOverlap(l))
        // More agents than fit one column at the minimum height: a second column.
        let big = layout(agents(16), .mainStack, 1728, 1040, main: 0)
        let stackXs = Set(big.tiles.dropFirst().map(\.card.x))
        #expect(stackXs.count >= 2)
        #expect(big.tiles.dropFirst().allSatisfy { $0.card.height >= minCard.height - 1e-6 })
        #expect(close(big.tiles.map(\.card.maxX).max()!, 1728 - spec.margin))
        // One agent: just the fill grid.
        #expect(close(layout(agents(1), .mainStack, 1728, 1040).tiles[0].card.width, 1728 - 2 * spec.margin))
    }

    @Test func scrollsDownOnlyWhenCardsWouldGetTooShort() {
        let fits = layout(agents(6), .shelf, 2000, 1240)
        #expect(fits.contentHeight == 1240 && fits.contentWidth == 2000)
        let small = layout(agents(16), .shelf, 1000, 600)
        #expect(small.contentHeight > 600)
        #expect(small.tiles.allSatisfy { $0.card.height >= minCard.height - 1e-6 && $0.rows >= spec.minRows })
        #expect(close(small.contentHeight, small.tiles.map(\.card.maxY).max()! + spec.margin))
        // The threshold: a window as tall as that content does not scroll.
        let exact = layout(agents(16), .shelf, 1000, small.contentHeight)
        #expect(exact.contentHeight == small.contentHeight)
    }

    @Test func minCharsSetting() {
        var dense = spec
        dense.minChars = 60
        let l80 = layout(agents(8), .shelf, 1728, 1040)
        let l60 = WallLayout.make(agents(8), arrangement: .shelf, width: 1728, height: 1040, spec: dense, cell: cell)
        #expect(Set(l60.tiles.map(\.card.y)).count < Set(l80.tiles.map(\.card.y)).count)
        #expect(l60.tiles.map(\.card.width).min()! >= WallLayout.minCard(dense, cell: cell).width - 1e-6)
    }

    @Test func neighbors() {
        let l = layout(agents(6), .shelf, 1500, 1000)
        #expect(l.neighbor(of: 0, .right) == 1)
        #expect(l.neighbor(of: 0, .left) == 0)
        let below = l.neighbor(of: 0, .down)
        #expect(l.tiles[below].card.y > l.tiles[0].card.maxY)
        #expect(l.neighbor(of: below, .up) == 0)
        #expect(l.neighbor(of: 0, .up) == 0)
    }

    @Test func cellTable() {
        var t = FontFit.CellTable(scale: 2)
        let est = t.pixels(10)
        #expect(est.width == 12 && est.height == 27)  // typical ratio, rounded
        let news = t.record(font: 10, widthPx: 13, heightPx: 29)
        let again = t.record(font: 10, widthPx: 13, heightPx: 29)
        #expect(news && !again)
        #expect(t.pixels(10) == .init(width: 13, height: 29))
        #expect(t.cell(10) == CellSize(width: 6.5, height: 14.5))
        #expect(abs(t.ratio!.widthPerPoint - 0.65) < 1e-9)
        let r = FontFit.CellRatio.measure(cellWidthPx: 14, cellHeightPx: 32, fontSize: 12, scale: 2)!
        #expect(abs(r.widthPerPoint - 14.0 / 24) < 1e-9)
        #expect(FontFit.CellRatio.measure(cellWidthPx: 0, cellHeightPx: 1, fontSize: 1, scale: 1) == nil)
    }
}

/// The app's wall at each density (`WallSpec.wall`): header, gutter, margin
/// and band headings follow `DesignTokens.Density`; terminals stay edge to
/// edge and whole-cell (attach --fit) at both.
@Suite struct WallDensity {
    let table = FontFit.CellTable(scale: 2, ratio: FontFit.CellRatio(widthPerPoint: 0.6, heightPerPoint: 1.3))
    func cell(_ f: Double) -> CellSize { table.cell(f) }
    func agents(_ n: Int) -> [WallTileInput] {
        Array(repeating: WallTileInput(grid: GridSize(cols: 120, rows: 40), state: .working, band: WallSpec.footerLine), count: n)
    }
    func close(_ a: Double, _ b: Double) -> Bool { abs(a - b) < 1e-6 }

    @Test func specFollowsTheDensityTokens() {
        let c = WallSpec.wall(.comfortable), k = WallSpec.wall(.compact)
        #expect(c.header == 26 && c.gap == 8 && c.margin == 16)
        #expect(k.header == 22 && k.gap == 6 && k.margin == 12)
        for (d, s) in [(DesignTokens.Density.comfortable, c), (.compact, k)] {
            #expect(s.header == d.tileHeader && s.gap == d.tileGutter && s.bandGap == d.bandGap)
            #expect(s.inset == 0, "terminals edge to edge")
            #expect(s.header <= DesignTokens.Chrome.maxHeight && s.bandHeading <= DesignTokens.Chrome.maxHeight)
            #expect(s.bandHeader == s.bandHeading + s.gap, "heading row, hairline, a gutter before the cards")
            #expect(s.bandPad == s.gap, "a bordered container: one gutter around the heading and cards")
            #expect(s.shelfHeight == s.header + WallSpec.footerLine + DesignTokens.Spacing.m)
            #expect(s.minRows == 10)
        }
        #expect(k.bandHeading < c.bandHeading)
    }

    @Test func gutterMarginAndEdgeToEdgeTerminalsAtBothDensities() {
        for d in DesignTokens.Density.allCases {
            let spec = WallSpec.wall(d)
            for arrangement in WallArrangement.allCases {
                let l = WallLayout.make(agents(6), arrangement: arrangement, width: 2000, height: 1240, spec: spec, cell: cell)
                #expect(l.tiles.count == 6)
                #expect(close(l.tiles.map(\.card.x).min()!, spec.margin) && close(l.tiles.map(\.card.y).min()!, spec.margin), "\(d) \(arrangement): margin")
                for t in l.tiles {
                    guard let term = t.terminal, let body = t.body else { Issue.record("no terminal"); continue }
                    #expect(close(body.x, t.card.x) && close(body.width, t.card.width), "\(d) \(arrangement): body edge to edge")
                    #expect(close(term.x, body.x + spec.padX) && close(term.width, body.width - 2 * spec.padX), "\(d) \(arrangement): text padded inside the body")
                    #expect(spec.padX > 0 && spec.padY > 0, "room around the text")
                    #expect(close(body.y, t.card.y + spec.header), "body right under the \(spec.header) pt header")
                    #expect(close(term.maxY, t.card.maxY - WallSpec.footerLine - spec.padY), "bottom-anchored above the footer, padded")
                    #expect(term.height <= body.height - 2 * spec.padY + 1e-6 && body.height - 2 * spec.padY - term.height < t.cell.height)
                    #expect(close(term.height, Double(t.rows) * t.cell.height) && t.cols == Int((term.width / t.cell.width + 1e-9).rounded(.down)))
                }
            }
            // Fill grid: neighbors are one gutter apart, both ways.
            let g = WallLayout.make(agents(4), arrangement: .shelf, width: 2000, height: 1240, spec: spec, cell: cell)
            let byRow = Dictionary(grouping: g.tiles, by: { $0.card.y }).sorted { $0.key < $1.key }
            #expect(byRow.count == 2)
            for (_, row) in byRow {
                let r = row.sorted { $0.card.x < $1.card.x }
                for (a, b) in zip(r, r.dropFirst()) { #expect(close(b.card.x - a.card.maxX, spec.gap), "\(d): gutter") }
            }
            if byRow.count == 2 { #expect(close(byRow[1].value[0].card.y - byRow[0].value[0].card.maxY, spec.gap)) }
        }
    }

    /// Compact gives the terminals the rows the chrome gives up.
    @Test func compactShowsAtLeastAsManyRows() {
        for n in [4, 9, 16] {
            let c = WallLayout.make(agents(n), arrangement: .shelf, width: 1512, height: 900, spec: .wall(.comfortable), cell: cell)
            let k = WallLayout.make(agents(n), arrangement: .shelf, width: 1512, height: 900, spec: .wall(.compact), cell: cell)
            #expect(zip(c.tiles, k.tiles).allSatisfy { $1.rows >= $0.rows && $1.cols >= $0.cols }, "n=\(n)")
            #expect(k.contentHeight <= c.contentHeight + 1e-6)
        }
    }

    /// Bands are a heading row above their cards: no padding, cards start a
    /// gutter below the heading; a collapsed band is one header.
    @Test func bandHeadingsAtBothDensities() {
        for d in DesignTokens.Density.allCases {
            let spec = WallSpec.wall(d)
            let slots = [BandSlot(key: "a", items: [0, 1]), BandSlot(key: "b", items: [2, 3]), BandSlot(key: "c", items: [4], collapsed: true)]
            let l = WallLayout.make(agents(5), bands: slots, arrangement: .shelf, width: 1512, height: 1400, spec: spec, cell: cell)
            #expect(l.bands.count == 3)
            for f in l.bands {
                #expect(close(f.header.height, spec.bandHeader))
                if f.collapsed {
                    #expect(close(f.frame.height, spec.bandHeader))
                    continue
                }
                let members = slots.first { $0.key == f.key }!.items.map { l.tiles[$0] }
                #expect(close(members.map(\.card.y).min()!, f.header.maxY), "\(d) \(f.key): cards right under the heading's gutter")
                #expect(spec.bandPad > 0 && close(members.map(\.card.x).min()!, f.frame.x + spec.bandPad), "\(d) \(f.key): cards inside the band's bordered container")
                #expect(close(members.map(\.card.maxY).max()!, f.frame.maxY - spec.bandPad))
            }
            #expect(l.tiles[4].hidden)
            let open = l.bands.filter { !$0.collapsed }.sorted { $0.frame.y < $1.frame.y }
            if open.count == 2 && open[0].style == .band { #expect(close(open[1].frame.y - open[0].frame.maxY, spec.bandGap)) }
        }
    }
}

/// Grid: every card the same size, bands on one lattice, one column count.
@Suite struct GridArrangementMath {
    let table = FontFit.CellTable(scale: 2, ratio: FontFit.CellRatio(widthPerPoint: 0.6, heightPerPoint: 1.3))
    func cell(_ f: Double) -> CellSize { table.cell(f) }
    let spec = WallSpec.wall(.comfortable)
    var minCard: (width: Double, height: Double) { WallLayout.minCard(spec, cell: cell) }
    func close(_ a: Double, _ b: Double) -> Bool { abs(a - b) < 1e-6 }

    /// The user's wall: 10 agents in 6 projects.
    static let user = [4, 1, 1, 1, 2, 1]

    func grid(_ counts: [Int], _ w: Double, _ h: Double, collapsed: Set<Int> = [], previous: Int? = nil, spec s: WallSpec? = nil) -> WallLayout {
        var inputs: [WallTileInput] = []
        var slots: [BandSlot] = []
        for (b, k) in counts.enumerated() {
            let start = inputs.count
            for j in 0..<k { inputs.append(WallTileInput(grid: GridSize(cols: 120, rows: 40), state: j % 2 == 0 ? .working : .idle, band: WallSpec.footerLine)) }
            slots.append(BandSlot(key: "p\(b)", items: Array(start..<inputs.count), collapsed: collapsed.contains(b)))
        }
        return WallLayout.make(inputs, bands: slots, arrangement: .grid, width: w, height: h, spec: s ?? spec, previousColumns: previous, cell: cell)
    }

    static let scenarios: [[Int]] = [[1], [4], user, [16], [4, 4, 4, 4], [7, 5, 2, 1, 1]]
    static let sizes: [(Double, Double)] = [(2056, 1250), (1512, 900), (2560, 1400), (1280, 760)]

    @Test func everyTileTheSameSizeQuietOnesToo() {
        for c in Self.scenarios {
            for (w, h) in Self.sizes {
                let l = grid(c, w, h)
                let shown = l.tiles.filter { !$0.hidden }
                #expect(shown.count == c.reduce(0, +))
                #expect(shown.allSatisfy { !$0.quiet && $0.terminal != nil }, "no shelf")
                #expect(shown.allSatisfy { close($0.card.width, shown[0].card.width) && close($0.card.height, shown[0].card.height) }, "\(c) \(w)x\(h)")
                #expect(shown[0].card.width >= minCard.width - 1e-6 && shown[0].card.height >= minCard.height - 1e-6, "never below the minimum card")
            }
        }
    }

    /// One lattice: every card's x is one of C column positions; a band's
    /// cards are inside its frame, in order, and no frames overlap.
    @Test func groupedOnOneLattice() {
        for c in Self.scenarios {
            for (w, h) in Self.sizes {
                let l = grid(c, w, h)
                let xs = Set(l.tiles.map { ($0.card.x * 1000).rounded() })
                #expect(xs.count == min(l.gridColumns, c.reduce(0, +)), "\(c) \(w)x\(h): columns line up")
                var i = 0
                for (b, k) in c.enumerated() {
                    guard let f = l.bands.first(where: { $0.key == "p\(b)" }) else { Issue.record("no frame"); continue }
                    for t in l.tiles[i..<(i + k)] {
                        #expect(t.card.x >= f.frame.x && t.card.maxX <= f.frame.maxX + 1e-6 && t.card.y >= f.header.maxY - 1e-6
                                && t.card.maxY <= f.frame.maxY + 1e-6, "\(c): card inside its band")
                    }
                    let ys = l.tiles[i..<(i + k)].map(\.card.y)
                    #expect(ys == ys.sorted(), "rows in order")
                    i += k
                }
                for (a, f) in l.bands.enumerated() {
                    for g in l.bands[(a + 1)...] {
                        let o = f.frame.x < g.frame.maxX - 1e-6 && g.frame.x < f.frame.maxX - 1e-6 && f.frame.y < g.frame.maxY - 1e-6 && g.frame.y < f.frame.maxY - 1e-6
                        #expect(!o, "\(c): bands overlap")
                    }
                }
            }
        }
    }

    /// The full width: a full row's last card ends at the right margin
    /// (inside its band's padding), its band at the margin.
    @Test func fullWidth() {
        for c in [[4], Self.user, [16], [4, 4, 4, 4]] {
            for (w, h) in Self.sizes {
                let l = grid(c, w, h)
                #expect(close(l.tiles.map(\.card.maxX).max()!, w - spec.margin - spec.bandPad), "\(c) \(w)x\(h)")
                #expect(close(l.bands.map(\.frame.maxX).max()!, w - spec.margin))
                #expect(close(l.bands.map(\.frame.x).min()!, spec.margin))
            }
        }
        // A plain wall: no band padding.
        let inputs = Array(repeating: WallTileInput(grid: GridSize(cols: 120, rows: 40)), count: 6)
        let p = WallLayout.make(inputs, arrangement: .grid, width: 2056, height: 1250, spec: spec, cell: cell)
        #expect(close(p.tiles.map(\.card.maxX).max()!, 2056 - spec.margin) && close(p.tiles.map(\.card.maxY).max()!, 1250 - spec.margin))
        #expect(p.bands.isEmpty)
    }

    @Test func columnChoice() {
        // One agent: the whole wall.
        let one = grid([1], 2056, 1250)
        #expect(one.gridColumns == 1 && close(one.contentHeight, 1250))
        // Four in one project: 2 × 2.
        #expect(grid([4], 2056, 1250).gridColumns == 2)
        // The user's 10 in 6 projects: 3 columns, one screen, cells wider than tall.
        let u = grid(Self.user, 2056, 1250)
        #expect(u.gridColumns == 3 && close(u.contentHeight, 1250))
        let t = u.tiles[0].card
        #expect(t.width / t.height >= GridRule.minAspect && t.width / t.height <= GridRule.maxAspect)
        // Bands share band rows: the 1-agent projects sit side by side.
        #expect(Set(u.bands.filter { ["p1", "p2", "p3"].contains($0.key) }.map(\.frame.y)).count == 1)
        // 16 agents: 3 columns at the minimum width (80 characters), scrolling.
        let s = grid([16], 2056, 1250)
        #expect(s.gridColumns == 3)
        // A laptop: two columns of at least the minimum width.
        #expect(grid(Self.user, 1512, 900).gridColumns == 2)
    }

    @Test func collapsedBandsAreTheirHeadingOnly() {
        let l = grid(Self.user, 2056, 1250, collapsed: [3, 5])
        for k in ["p3", "p5"] {
            let f = l.bands.first { $0.key == k }!
            #expect(f.collapsed && close(f.frame.height, spec.bandHeader) && f.header == f.frame)
        }
        #expect(l.tiles[6].hidden && l.tiles[9].hidden)
        #expect(l.tiles.filter { !$0.hidden }.count == 8)
        let headings = l.bands.filter(\.collapsed)
        #expect(headings.allSatisfy { close($0.frame.y, spec.margin) }, "one strip at the top")
        let cards = l.tiles.filter { !$0.hidden }
        #expect(cards.map(\.card.y).min()! > headings[0].frame.maxY)
    }

    @Test func scrollsWhenNothingFitsOneScreen() {
        let l = grid([4, 4, 4, 4], 1512, 900)
        #expect(l.contentHeight > 900, "scrolls down")
        #expect(close(l.contentWidth, 1512), "never sideways")
        let t = l.tiles[0].card
        #expect(t.width >= minCard.width - 1e-6 && t.height >= minCard.height - 1e-6)
        // The most columns that keep the minimum width.
        let more = l.gridColumns + 1
        let inner = 1512 - 2 * spec.margin - 2 * spec.bandPad
        let g = 2 * spec.bandPad + spec.gap
        #expect((inner - Double(more - 1) * g) / Double(more) < minCard.width)
    }

    @Test func stableUnderSmallResizes() {
        for c in Self.scenarios {
            for (w, h) in Self.sizes {
                let base = grid(c, w, h)
                for (dw, dh) in [(-10.0, 0.0), (10, 0), (0, -10), (0, 10), (-10, -10), (10, 10)] {
                    let l = grid(c, w + dw, h + dh, previous: base.gridColumns)
                    #expect(l.gridColumns == base.gridColumns, "\(c) \(w)x\(h) → \(dw),\(dh)")
                }
            }
        }
        // Deterministic without history too, for the user's walls.
        for (w, h) in [(2056.0, 1250.0), (1512, 900)] {
            let base = grid(Self.user, w, h).gridColumns
            for d in [-10.0, -5, 5, 10] { #expect(grid(Self.user, w + d, h + d).gridColumns == base) }
        }
    }

    @Test func densityApplies() {
        let c = grid(Self.user, 2056, 1250)
        let k = grid(Self.user, 2056, 1250, spec: .wall(.compact))
        #expect(k.tiles[0].card.height > c.tiles[0].card.height, "compact chrome leaves taller cells")
        #expect(close(k.bands.map(\.frame.x).min()!, WallSpec.wall(.compact).margin))
    }
}

@Suite struct LayoutSwitcherState {
    @Test func reflectsTheArrangement() {
        for a in WallArrangement.allCases {
            let s = LayoutSwitcher(a)
            #expect(s.symbol == a.symbol && s.help == "Layout: \(a.title)")
            #expect(s.rows.filter(\.checked).map(\.arrangement) == [a], "exactly the current one checked")
            #expect(s.rows[s.openIndex].arrangement == a)
            #expect(OverlayList.opening(checked: s.rows.map(\.checked)).index == s.openIndex, "the popover opens on the current row")
        }
        #expect(Set(WallArrangement.allCases.map(\.symbol)).count == WallArrangement.allCases.count, "every arrangement its own icon")
        #expect(LayoutSwitcher(.shelf) != LayoutSwitcher(.grid), "a change is a different switcher state")
    }

    @Test func gridIsSelectableEverywhere() {
        #expect(WallArrangement.grid.title == "Grid" && WallArrangement.grid.symbol == "square.grid.3x3")
        #expect(WallArrangement.grid.shortcut == "⌥⌘5" && WallArrangement.shortcutRange == "⌥⌘1–5")
        #expect(WallArrangement.numbered(5) == .grid && WallArrangement.numbered(6) == nil && WallArrangement.numbered(0) == nil)
        #expect(WallArrangement.mainStack.next == .grid && WallArrangement.grid.next == .shelf)
        #expect(KeyRouter.route(KeyChord(.char("5"), command: true, option: true), mode: .wall, selectedState: nil) == .arrangement(4))
        #expect(KeyRouter.route(KeyChord(.char("6"), command: true, option: true), mode: .wall, selectedState: nil) == .none)
        #expect(WallArrangement(rawValue: "grid") == .grid, "per-wall persistence (windows.json, desks)")
    }

    @Test func openingListWithoutAChoice() {
        #expect(OverlayList.opening(checked: [false, false]).index == 0)
        #expect(OverlayList.opening(checked: []).index == 0)
    }
}
