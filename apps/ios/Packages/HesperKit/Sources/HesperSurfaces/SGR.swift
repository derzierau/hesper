/// A terminal color as the daemon's view writes it. Abstract on purpose:
/// HesperDesign maps palette entries and RGB to the screen's colors.
public enum AnsiColor: Equatable, Sendable {
    /// A palette entry, 0…255: 0–7 the normal colors, 8–15 the bright ones.
    case indexed(Int)
    case rgb(UInt8, UInt8, UInt8)
}

/// The attributes of a run of text; nil colors are the terminal's defaults.
public struct RunStyle: Equatable, Sendable {
    public var fg: AnsiColor?
    public var bg: AnsiColor?
    public var bold: Bool
    public var dim: Bool
    public var italic: Bool
    public var underline: Bool
    public var reverse: Bool
    public var strike: Bool

    public init(
        fg: AnsiColor? = nil, bg: AnsiColor? = nil, bold: Bool = false, dim: Bool = false, italic: Bool = false,
        underline: Bool = false, reverse: Bool = false, strike: Bool = false
    ) {
        self.fg = fg
        self.bg = bg
        self.bold = bold
        self.dim = dim
        self.italic = italic
        self.underline = underline
        self.reverse = reverse
        self.strike = strike
    }

    public static let plain = RunStyle()
}

/// Text of one style within a row.
public struct StyledRun: Equatable, Sendable {
    public var text: String
    public var style: RunStyle

    public init(text: String, style: RunStyle) {
        self.text = text
        self.style = style
    }
}

extension RunStyle {
    /// Applies one `ESC[<params>m`. Each parameter is its colon
    /// sub-parameters, nil for an empty one. Codes the phone does not show
    /// (blink, hidden, overline, underline color) are skipped, with their
    /// arguments.
    mutating func applySGR(_ params: [[Int?]]) {
        guard !params.isEmpty else {
            self = .plain
            return
        }
        var i = 0
        while i < params.count {
            let p = params[i]
            let code = p.first.flatMap { $0 } ?? 0
            i += 1
            switch code {
            case 0: self = .plain
            case 1: bold = true
            case 2: dim = true
            case 3: italic = true
            // `4:0` turns underline off; `4:n` picks a style, all shown alike.
            case 4: underline = (p.count > 1 ? p[1] ?? 1 : 1) != 0
            case 7: reverse = true
            case 9: strike = true
            case 22: (bold, dim) = (false, false)
            case 23: italic = false
            case 24: underline = false
            case 27: reverse = false
            case 29: strike = false
            case 30...37: fg = .indexed(code - 30)
            case 39: fg = nil
            case 40...47: bg = .indexed(code - 40)
            case 49: bg = nil
            case 90...97: fg = .indexed(code - 90 + 8)
            case 100...107: bg = .indexed(code - 100 + 8)
            case 38, 48, 58:
                var args: [Int?]
                if p.count > 1 {
                    // Colon form; `2:<space>:r:g:b` carries a color space
                    // id, and may be followed by tolerance sub-parameters.
                    args = Array(p.dropFirst())
                    if args.first == 2, args.count >= 5 { args = [2] + args[2...4] }
                } else {
                    // Semicolon form: the arguments are the next parameters.
                    let kind = i < params.count ? params[i].first ?? nil : nil
                    let n = kind == 5 ? 2 : kind == 2 ? 4 : 1
                    args = params[i..<min(i + n, params.count)].map { $0.first ?? nil }
                    i += args.count
                }
                guard let color = Self.color(args) else { break }
                if code == 38 { fg = color } else if code == 48 { bg = color }
            default: break
            }
        }
    }

    /// `5;n` or `2;r;g;b` as a color, nil when malformed.
    private static func color(_ args: [Int?]) -> AnsiColor? {
        let v = args.map { $0 ?? 0 }
        switch v.first {
        case 5 where v.count >= 2 && (0...255).contains(v[1]):
            return .indexed(v[1])
        case 2 where v.count >= 4 && v[1...3].allSatisfy { (0...255).contains($0) }:
            return .rgb(UInt8(v[1]), UInt8(v[2]), UInt8(v[3]))
        default:
            return nil
        }
    }
}
