import HesperSurfaces
import SwiftUI

// The terminal's colours on the phone. The one file besides NeonPalette
// that names colours as hex: an agent's screen asks for the 16 ANSI colours
// and the xterm 256-colour palette, and most of them have no Neon token.
extension Theme {
    /// The 16 ANSI colours as sRGB hex: 0–7 normal, 8–15 bright. Neon
    /// tokens where one fits; the rest are bright enough to read on the
    /// near-black background.
    nonisolated static let ansi16: [UInt32] = [
        NeonPalette.hex(.background),  // 0 black: the screen itself
        0xFF5C57,  // 1 red: errors, deletions
        0x5AF78E,  // 2 green: success, additions
        0xF3F99D,  // 3 yellow: warnings
        0x57C7FF,  // 4 blue: paths, links; kept clear of the accent's cyan
        NeonPalette.hex(.signal),  // 5 magenta: Neon's signal
        NeonPalette.hex(.accent),  // 6 cyan: Neon's accent
        NeonPalette.hex(.text),  // 7 white: the text colour
        NeonPalette.hex(.muted),  // 8 bright black: dim and meta text
        0xFF8A85,  // 9 bright red
        0x8AFFAD,  // 10 bright green
        0xFFFDC2,  // 11 bright yellow
        0x8AD8FF,  // 12 bright blue
        0xFF8ADD,  // 13 bright magenta: signal, lighter
        0xA6F9FF,  // 14 bright cyan: accent, lighter
        0xFFFFFF,  // 15 bright white: full white, above the text colour
    ]

    /// A palette entry as sRGB hex, or nil outside 0…255: the 16 above,
    /// 16–231 the xterm 6×6×6 cube, 232–255 its 24 greys.
    nonisolated static func ansiHex(_ index: Int) -> UInt32? {
        switch index {
        case 0..<16:
            return ansi16[index]
        case 16..<232:
            // xterm's cube levels: 0, then 95 rising by 40 to 255.
            func level(_ n: Int) -> UInt32 { n == 0 ? 0 : UInt32(55 + n * 40) }
            let i = index - 16
            return level(i / 36) << 16 | level(i / 6 % 6) << 8 | level(i % 6)
        case 232..<256:
            let v = UInt32(8 + (index - 232) * 10)
            return v << 16 | v << 8 | v
        default:
            return nil
        }
    }

    /// The opacity of faint text (SGR 2) on the phone's terminal views.
    public static let faint: Double = 0.6

    /// A palette entry, 0…255; an index outside it is clamped.
    public static func ansi(_ index: Int) -> Color {
        Color(uiColor: srgb(ansiHex(min(max(index, 0), 255)) ?? 0))
    }

    /// A run's colour: a palette entry, or RGB as it is; nil (and an index
    /// outside the palette) is the terminal's default, `fallback`.
    public static func color(_ c: AnsiColor?, default fallback: Color) -> Color {
        switch c {
        case nil: fallback
        case .indexed(let i): ansiHex(i).map { Color(uiColor: srgb($0)) } ?? fallback
        case .rgb(let r, let g, let b): Color(uiColor: srgb(UInt32(r) << 16 | UInt32(g) << 8 | UInt32(b)))
        }
    }
}

struct AnsiPalette_Previews: PreviewProvider {
    static var previews: some View {
        VStack(alignment: .leading, spacing: DS.Spacing.xs) {
            ForEach([0, 8], id: \.self) { row in
                HStack(spacing: DS.Spacing.xs) {
                    ForEach(row..<row + 8, id: \.self) { i in
                        Theme.ansi(i)
                            .frame(width: 32, height: 32)
                            .overlay(Text("\(i)").font(.caption2).foregroundStyle(Theme.muted))
                            .border(Theme.line, width: DS.Edge.line)
                    }
                }
            }
        }
        .padding()
        .background(Theme.background)
        .previewLayout(.sizeThatFits)
    }
}
