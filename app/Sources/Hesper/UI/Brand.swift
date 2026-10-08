import AppKit
import CoreText
import SwiftUI

/// Hesper's bundled type: Geist and Geist Mono (SIL OFL 1.1, see
/// Resources/Fonts/OFL.txt), variable TTFs registered for this process at
/// launch. System menus and the menu bar keep SF.
enum BrandFonts {
    static let files = ["Geist-VF.ttf", "GeistMono-VF.ttf"]
    nonisolated(unsafe) private(set) static var registered = false

    /// Contents/Resources/Fonts in the bundle; app/Resources/Fonts when run
    /// unbundled from the source tree (swift run, tests).
    static func fontsDirectory() -> URL? {
        let fm = FileManager.default
        var candidates: [URL] = []
        if let r = Bundle.main.resourceURL { candidates.append(r.appendingPathComponent("Fonts")) }
        candidates.append(URL(fileURLWithPath: #filePath)        // app/Sources/Hesper/UI/Brand.swift
            .deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
            .appendingPathComponent("Resources/Fonts"))
        return candidates.first { fm.fileExists(atPath: $0.appendingPathComponent(files[0]).path) }
    }

    /// Registers the fonts (process scope). Safe to call more than once.
    @discardableResult
    static func register() -> Bool {
        if registered { return true }
        guard let dir = fontsDirectory() else {
            print("hesper: Geist fonts not found, using the system font")
            return false
        }
        var ok = true
        for f in files {
            var err: Unmanaged<CFError>?
            if !CTFontManagerRegisterFontsForURL(dir.appendingPathComponent(f) as CFURL, .process, &err) {
                let e = err?.takeRetainedValue()
                // Already registered (e.g. a second call) is fine.
                if let e, CFErrorGetCode(e) == CTFontManagerError.alreadyRegistered.rawValue { continue }
                print("hesper: registering \(f) failed: \(e.map { String(describing: $0) } ?? "?")")
                ok = false
            }
        }
        registered = ok
        return ok
    }

    /// The named instance of the variable font for a weight ("Geist-SemiBold").
    static func postScriptName(mono: Bool, weight: NSFont.Weight) -> String {
        let w: String
        switch weight.rawValue {
        case ..<(-0.7): w = "Thin"
        case ..<(-0.5): w = "ExtraLight"
        case ..<(-0.2): w = "Light"
        case ..<0.15: w = "Regular"
        case ..<0.27: w = "Medium"
        case ..<0.35: w = "SemiBold"
        case ..<0.5: w = "Bold"
        case ..<0.6: w = "ExtraBold"
        default: w = "Black"
        }
        return (mono ? "GeistMono-" : "Geist-") + w
    }
}

extension NSFont {
    /// Geist at a weight (falls back to the system font if unregistered).
    static func geist(ofSize size: CGFloat, weight: NSFont.Weight = .regular) -> NSFont {
        NSFont(name: BrandFonts.postScriptName(mono: false, weight: weight), size: size) ?? .systemFont(ofSize: size, weight: weight)
    }
    /// Geist Mono, for Hesper's own chrome only (terminals keep the user's font).
    static func geistMono(ofSize size: CGFloat, weight: NSFont.Weight = .regular) -> NSFont {
        NSFont(name: BrandFonts.postScriptName(mono: true, weight: weight), size: size) ?? .monospacedSystemFont(ofSize: size, weight: weight)
    }
}

extension Font.Weight {
    var ns: NSFont.Weight {
        switch self {
        case .ultraLight: return .ultraLight
        case .thin: return .thin
        case .light: return .light
        case .medium: return .medium
        case .semibold: return .semibold
        case .bold: return .bold
        case .heavy: return .heavy
        case .black: return .black
        default: return .regular
        }
    }
}

extension Font {
    static func geist(_ size: CGFloat, _ weight: Font.Weight = .regular) -> Font {
        Font(NSFont.geist(ofSize: size, weight: weight.ns) as CTFont)
    }
    static func geistMono(_ size: CGFloat, _ weight: Font.Weight = .regular) -> Font {
        Font(NSFont.geistMono(ofSize: size, weight: weight.ns) as CTFont)
    }
}

/// The wordmark: lowercase "hesper" in Geist SemiBold, tracking −5.5%, and
/// Geist's square full stop: Signal when something needs you, Dim when not.
struct Wordmark: View {
    var needsYou: Bool
    var size: CGFloat = 14

    var body: some View {
        // The stop is the brand's red, always; when something needs you it
        // also glows.
        (Text("hesper").foregroundColor(Theme.fg) + Text(".").foregroundColor(Theme.color(.signal)))
            .font(.geist(size, .semibold))
            .tracking(-0.055 * size)
            .fixedSize()
            .shadow(color: needsYou ? Theme.color(.signal).opacity(0.7) : .clear, radius: needsYou ? size * 0.3 : 0)
            .accessibilityLabel(needsYou ? "Hesper, something needs you" : "Hesper")
    }

    /// The text's real size at `size` (Geist 600, the same tracking), for
    /// AppKit layout: a hosting view's fittingSize can come back short.
    static func measured(_ size: CGFloat) -> NSSize {
        let s = NSAttributedString(string: "hesper.", attributes: [.font: NSFont.geist(ofSize: size, weight: .semibold), .kern: -0.055 * size])
        let r = s.boundingRect(with: NSSize(width: 10_000, height: 10_000), options: [.usesLineFragmentOrigin, .usesFontLeading])
        return NSSize(width: ceil(r.width) + 2, height: ceil(r.height))
    }
}

/// The menu bar's "h.": a template image drawn from Geist (registered at
/// launch). The full stop is an outline when nothing needs you, filled
/// when something does.
enum BrandMark {
    static func statusImage(needsYou: Bool, pointSize: CGFloat = 18) -> NSImage {
        let font = NSFont.geist(ofSize: pointSize, weight: .bold)
        let ctFont = font as CTFont
        var chars: [UniChar] = Array("h.".utf16)
        var glyphs = [CGGlyph](repeating: 0, count: 2)
        CTFontGetGlyphsForCharacters(ctFont, &chars, &glyphs, 2)
        var advances = [CGSize](repeating: .zero, count: 2)
        CTFontGetAdvancesForGlyphs(ctFont, .horizontal, glyphs, &advances, 2)
        let hPath = CTFontCreatePathForGlyph(ctFont, glyphs[0], nil)
        let hBox = hPath?.boundingBoxOfPath ?? CGRect(x: 0, y: 0, width: pointSize * 0.5, height: pointSize * 0.7)
        let pBox = CTFontCreatePathForGlyph(ctFont, glyphs[1], nil)?.boundingBoxOfPath
            ?? CGRect(x: 0, y: 0, width: pointSize * 0.15, height: pointSize * 0.15)
        // Image coordinates (points), snapped so 1x and 2x stay crisp: the h's
        // ink starts at x 1 on a whole-point baseline (ascender box centered);
        // the period at the font's own spacing, a whole-point square.
        let hx = 1 - hBox.minX
        let baseline = ((pointSize - hBox.maxY) / 2).rounded()
        let side = max(3, pBox.width.rounded())
        let dot = CGRect(x: (hx + advances[0].width + pBox.minX).rounded(), y: baseline, width: side, height: side)
        let size = NSSize(width: dot.maxX + 1, height: pointSize)
        let img = NSImage(size: size, flipped: false) { _ in
            guard let cg = NSGraphicsContext.current?.cgContext else { return false }
            cg.setFillColor(NSColor.black.cgColor)
            cg.setStrokeColor(NSColor.black.cgColor)
            if let hPath {
                var t = CGAffineTransform(translationX: hx, y: baseline)
                if let p = hPath.copy(using: &t) { cg.addPath(p); cg.fillPath() }
            }
            if needsYou {
                cg.fill(dot)
            } else {
                // A 1 pt ring on the pixel grid.
                cg.setLineWidth(1)
                cg.stroke(dot.insetBy(dx: 0.5, dy: 0.5))
            }
            return true
        }
        img.isTemplate = true
        img.accessibilityDescription = needsYou ? "Hesper: something needs you" : "Hesper"
        return img
    }
}
