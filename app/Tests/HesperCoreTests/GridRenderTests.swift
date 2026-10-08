import CoreGraphics
import CoreText
import Foundation
import ImageIO
import Testing
import UniformTypeIdentifiers
@testable import HesperCore

/// Draws a wall layout's geometry (bands, headings, tiles) offscreen into a
/// PNG: a look at the Grid arrangement without launching the app. Runs
/// only with HESPER_GRID_PNG_DIR set (the directory to write into).
@Suite struct GridRender {
    static let dir = ProcessInfo.processInfo.environment["HESPER_GRID_PNG_DIR"]
    let table = FontFit.CellTable(scale: 2, ratio: FontFit.CellRatio(widthPerPoint: 0.6, heightPerPoint: 1.3))
    func cell(_ f: Double) -> CellSize { table.cell(f) }

    /// The user's wall: 10 agents in 6 bands.
    static let userBands: [(String, Int)] = [("news-api", 4), ("hesper", 1), ("acme-api", 1), ("recipes", 1),
                                             ("2026-10-06-please-understand…", 2), ("New", 1)]

    func render(_ bands: [(String, Int)], collapsed: Set<String> = [], w: Double, h: Double, density: DesignTokens.Density = .comfortable,
                name: String) throws {
        guard let dir = Self.dir else { return }
        var inputs: [WallTileInput] = []
        var slots: [BandSlot] = []
        for (key, k) in bands {
            let start = inputs.count
            for j in 0..<k { inputs.append(WallTileInput(grid: GridSize(cols: 120, rows: 40), state: j % 3 == 2 ? .idle : .working, band: WallSpec.footerLine)) }
            slots.append(BandSlot(key: key, items: Array(start..<inputs.count), collapsed: collapsed.contains(key)))
        }
        let l = WallLayout.make(inputs, bands: slots, arrangement: .grid, width: w, height: h, spec: .wall(density), cell: cell)
        let cw = Int(w), chh = Int(max(h, l.contentHeight))
        let cs = CGColorSpace(name: CGColorSpace.sRGB)!
        guard let ctx = CGContext(data: nil, width: cw, height: chh, bitsPerComponent: 8, bytesPerRow: 0, space: cs,
                                  bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue) else { return }
        ctx.translateBy(x: 0, y: Double(chh)); ctx.scaleBy(x: 1, y: -1) // top-left origin
        func rgb(_ r: Double, _ g: Double, _ b: Double, _ a: Double = 1) -> CGColor { CGColor(colorSpace: cs, components: [r, g, b, a])! }
        func cg(_ r: Rect) -> CGRect { CGRect(x: r.x, y: r.y, width: r.width, height: r.height) }
        func text(_ s: String, _ x: Double, _ y: Double, _ size: Double, _ c: CGColor) {
            let font = CTFontCreateWithName("Helvetica" as CFString, size, nil)
            let attr = NSAttributedString(string: s, attributes: [NSAttributedString.Key(kCTFontAttributeName as String): font,
                                                                   NSAttributedString.Key(kCTForegroundColorAttributeName as String): c])
            let line = CTLineCreateWithAttributedString(attr)
            ctx.saveGState()
            ctx.textMatrix = CGAffineTransform(scaleX: 1, y: -1)
            ctx.textPosition = CGPoint(x: x, y: y)
            CTLineDraw(line, ctx)
            ctx.restoreGState()
        }
        ctx.setFillColor(rgb(0.07, 0.07, 0.09)); ctx.fill(CGRect(x: 0, y: 0, width: cw, height: chh))
        // The viewport's bottom edge (what fits one screen).
        ctx.setStrokeColor(rgb(0.9, 0.3, 0.3)); ctx.setLineWidth(1); ctx.stroke(CGRect(x: 0.5, y: 0.5, width: w - 1, height: h - 1))
        for b in l.bands {
            let path = CGPath(roundedRect: cg(b.frame).insetBy(dx: 0.5, dy: 0.5), cornerWidth: 10, cornerHeight: 10, transform: nil)
            ctx.addPath(path); ctx.setStrokeColor(rgb(0.3, 0.3, 0.36)); ctx.strokePath()
            text((b.collapsed ? "▸ " : "") + b.key, b.header.x + 10, b.header.y + b.header.height * 0.62, 12, rgb(0.75, 0.75, 0.8))
        }
        let spec = WallSpec.wall(density)
        for (i, t) in l.tiles.enumerated() where !t.hidden {
            let path = CGPath(roundedRect: cg(t.card), cornerWidth: 8, cornerHeight: 8, transform: nil)
            ctx.addPath(path); ctx.setFillColor(rgb(0.14, 0.14, 0.17)); ctx.fillPath()
            if let body = t.body { ctx.setFillColor(rgb(0.04, 0.04, 0.05)); ctx.fill(cg(body)) }
            if let term = t.terminal { ctx.setStrokeColor(rgb(0.25, 0.45, 0.8, 0.6)); ctx.stroke(cg(term)) }
            text("agent \(i + 1) · \(t.cols)×\(t.rows) · \(Int(t.card.width))×\(Int(t.card.height))", t.card.x + 10, t.card.y + spec.header * 0.65, 11,
                 rgb(0.85, 0.85, 0.9))
        }
        text("\(name): \(Int(w))×\(Int(h)), C=\(l.gridColumns), content \(Int(l.contentHeight)) high", 8, Double(chh) - 8, 12, rgb(0.9, 0.6, 0.3))
        guard let img = ctx.makeImage() else { return }
        try FileManager.default.createDirectory(atPath: dir, withIntermediateDirectories: true)
        let url = URL(fileURLWithPath: dir).appendingPathComponent("\(name).png")
        guard let dest = CGImageDestinationCreateWithURL(url as CFURL, UTType.png.identifier as CFString, 1, nil) else { return }
        CGImageDestinationAddImage(dest, img, nil)
        CGImageDestinationFinalize(dest)
    }

    @Test func renderTheUsersWalls() throws {
        try render(Self.userBands, w: 2056, h: 1250, name: "user-10-2056x1250")
        try render(Self.userBands, w: 1512, h: 900, name: "user-10-1512x900")
        try render(Self.userBands, w: 1512, h: 900, density: .compact, name: "user-10-1512x900-compact")
        try render(Self.userBands, collapsed: ["recipes", "New"], w: 2056, h: 1250, name: "user-10-collapsed-2056x1250")
        try render([("a", 4), ("b", 4), ("c", 4), ("d", 4)], w: 2056, h: 1250, name: "16-in-4-2056x1250")
        try render([("a", 16)], w: 2056, h: 1250, name: "16-one-project-2056x1250")
        try render([("a", 7), ("b", 5), ("c", 2), ("d", 1), ("e", 1)], w: 2056, h: 1250, name: "16-mixed-2056x1250")
        try render([("a", 1)], w: 2056, h: 1250, name: "1-2056x1250")
        try render([("a", 4)], w: 2056, h: 1250, name: "4-one-project-2056x1250")
    }
}
