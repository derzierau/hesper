// Draws Hesper's app icon, "h." variant A, into an .iconset (all sizes,
// 16…1024 incl. @2x), plus an optional 1024 px PNG preview.
//
//   swift Tools/make-icon.swift <out.iconset> [preview.png] [Geist-VF.ttf]
//
// The letter and the full stop are Geist Bold's own glyphs (wght 700 of the
// bundled variable font), at the font's natural size and spacing: the
// period sits at the h's advance width plus its own left side bearing.
// Geometry, on the 1024 canvas (macOS icon grid: an 824×824 body, corner
// radius 185, centered, transparent margin around it), relative to the body:
//   - the h's ascender height (710 font units) = 58% of the body;
//   - the h+period group is centered horizontally as one shape (h ink x0..x1
//     through the period's right edge);
//   - the ascender box is centered vertically, then lifted 1% (baseline at
//     50% + 29% − 1% of the body, from its top).
// Colors: body #1B1E3A (flat: no gradient, no border), h #F1F2FF,
// period #FF6F91 (Signal).
import CoreGraphics
import CoreText
import Foundation
import ImageIO
import UniformTypeIdentifiers

let args = CommandLine.arguments
guard args.count >= 2 else {
    FileHandle.standardError.write("usage: make-icon.swift <out.iconset> [preview.png] [Geist-VF.ttf]\n".data(using: .utf8)!)
    exit(2)
}
let outDir = args[1]
let preview = args.count > 2 ? args[2] : nil
let fontPath = args.count > 3 ? args[3] : "Resources/Fonts/Geist-VF.ttf"

// MARK: Geist Bold outlines in font units

let descs = CTFontManagerCreateFontDescriptorsFromURL(URL(fileURLWithPath: fontPath) as CFURL) as? [CTFontDescriptor] ?? []
guard let boldDesc = descs.first(where: { (CTFontDescriptorCopyAttribute($0, kCTFontNameAttribute) as? String) == "Geist-Bold" }) else {
    FileHandle.standardError.write("make-icon: no Geist-Bold instance in \(fontPath)\n".data(using: .utf8)!)
    exit(1)
}
let upm: CGFloat = 1000
let font = CTFontCreateWithFontDescriptor(boldDesc, upm, nil) // 1 pt = 1 font unit
var chars: [UniChar] = Array("h.".utf16)
var glyphs = [CGGlyph](repeating: 0, count: 2)
guard CTFontGetGlyphsForCharacters(font, &chars, &glyphs, 2),
      let hPath = CTFontCreatePathForGlyph(font, glyphs[0], nil),
      let dotPath = CTFontCreatePathForGlyph(font, glyphs[1], nil) else {
    FileHandle.standardError.write("make-icon: no outlines for h / .\n".data(using: .utf8)!)
    exit(1)
}
var advances = [CGSize](repeating: .zero, count: 2)
CTFontGetAdvancesForGlyphs(font, .horizontal, glyphs, &advances, 2)
let hBox = hPath.boundingBoxOfPath      // x0..x1, 0..710
let dotBox = dotPath.boundingBoxOfPath  // the square full stop
let hAdvance = advances[0].width
let ascender: CGFloat = 710
print(String(format: "make-icon: Geist-Bold h ink %.1f…%.1f (top %.1f), advance %.0f; period %.1f…%.1f × %.1f",
             hBox.minX, hBox.maxX, hBox.maxY, hAdvance, dotBox.minX, dotBox.maxX, dotBox.maxY))

// MARK: Geometry on the 1024 canvas (y down from the top, as specified)

let canvas: CGFloat = 1024
let bodySize: CGFloat = 824
let bodyOrigin = (canvas - bodySize) / 2           // 100
let radius: CGFloat = 185
let scale = 0.58 * bodySize / ascender             // px per font unit
let hInk = (hBox.maxX - hBox.minX) * scale
let gap = (hAdvance - hBox.maxX + dotBox.minX) * scale
let dotW = dotBox.width * scale
let groupW = hInk + gap + dotW
let left = bodyOrigin + bodySize / 2 - groupW / 2  // the group's ink starts here
let baselineFromTop = bodyOrigin + bodySize * (0.5 + 0.29 - 0.01)

func rgb(_ v: UInt32) -> CGColor {
    CGColor(srgbRed: CGFloat((v >> 16) & 0xff) / 255, green: CGFloat((v >> 8) & 0xff) / 255, blue: CGFloat(v & 0xff) / 255, alpha: 1)
}

func render(_ px: Int) -> CGImage {
    let ctx = CGContext(data: nil, width: px, height: px, bitsPerComponent: 8, bytesPerRow: 0,
                        space: CGColorSpace(name: CGColorSpace.sRGB)!, bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue)!
    ctx.setShouldAntialias(true)
    ctx.interpolationQuality = .high
    let k = CGFloat(px) / canvas
    ctx.scaleBy(x: k, y: k)
    // Body: the flat rounded square.
    ctx.addPath(CGPath(roundedRect: CGRect(x: bodyOrigin, y: bodyOrigin, width: bodySize, height: bodySize),
                       cornerWidth: radius, cornerHeight: radius, transform: nil))
    ctx.setFillColor(rgb(0x1B1E3A))
    ctx.fillPath()
    // Glyphs: CG is y-up, so the baseline sits at canvas − baselineFromTop.
    let baseline = canvas - baselineFromTop
    let origin = left - hBox.minX * scale          // the h's glyph origin
    var t = CGAffineTransform(translationX: origin, y: baseline).scaledBy(x: scale, y: scale)
    ctx.addPath(hPath.copy(using: &t)!)
    ctx.setFillColor(rgb(0xF1F2FF))
    ctx.fillPath()
    var td = CGAffineTransform(translationX: origin + hAdvance * scale, y: baseline).scaledBy(x: scale, y: scale)
    ctx.addPath(dotPath.copy(using: &td)!)
    ctx.setFillColor(rgb(0xFF6F91))
    ctx.fillPath()
    return ctx.makeImage()!
}

func writePNG(_ img: CGImage, _ path: String) {
    let url = URL(fileURLWithPath: path) as CFURL
    let dest = CGImageDestinationCreateWithURL(url, UTType.png.identifier as CFString, 1, nil)!
    CGImageDestinationAddImage(dest, img, nil)
    guard CGImageDestinationFinalize(dest) else { fatalError("could not write \(path)") }
}

try? FileManager.default.createDirectory(atPath: outDir, withIntermediateDirectories: true)
for size in [16, 32, 128, 256, 512] {
    writePNG(render(size), "\(outDir)/icon_\(size)x\(size).png")
    writePNG(render(size * 2), "\(outDir)/icon_\(size)x\(size)@2x.png")
}
if let preview { writePNG(render(1024), preview) }
print(String(format: "make-icon: h %.1f px tall, group %.1f px wide from x %.1f, baseline %.1f px from the top",
             ascender * scale, groupW, left, baselineFromTop))
