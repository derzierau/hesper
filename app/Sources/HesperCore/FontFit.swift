import Foundation

/// Cell sizes per font size, for laying tiles out before (and after) the
/// engine has reported them.
public enum FontFit {
    /// Cell size per point of font size, in backing pixels, measured from the
    /// engine at some font size (cell px / (pt × scale) is ~constant).
    public struct CellRatio: Equatable, Sendable {
        public var widthPerPoint: Double
        public var heightPerPoint: Double
        public init(widthPerPoint: Double, heightPerPoint: Double) { self.widthPerPoint = widthPerPoint; self.heightPerPoint = heightPerPoint }

        public static func measure(cellWidthPx: Int, cellHeightPx: Int, fontSize: Double, scale: Double) -> CellRatio? {
            guard cellWidthPx > 0, cellHeightPx > 0, fontSize > 0, scale > 0 else { return nil }
            return CellRatio(widthPerPoint: Double(cellWidthPx) / (fontSize * scale),
                             heightPerPoint: Double(cellHeightPx) / (fontSize * scale))
        }

        /// Typical monospace (JetBrains Mono at Ghostty defaults) before measuring.
        public static let typical = CellRatio(widthPerPoint: 0.6, heightPerPoint: 1.34)
    }

    /// Cell sizes in engine pixels by font size: exact where the engine
    /// reported them, else estimated from the ratio and rounded to whole
    /// pixels the way the engine does (≈; corrected once measured).
    public struct CellTable: Equatable, Sendable {
        public var scale: Double
        public var ratio: CellRatio?
        public var measured: [Double: PixelCell] = [:]
        public init(scale: Double, ratio: CellRatio? = nil) { self.scale = scale; self.ratio = ratio }

        public struct PixelCell: Equatable, Sendable {
            public var width: Int
            public var height: Int
            public init(width: Int, height: Int) { self.width = width; self.height = height }
        }

        public func pixels(_ font: Double) -> PixelCell {
            if let m = measured[font] { return m }
            let r = ratio ?? .typical
            return PixelCell(width: max(1, Int((r.widthPerPoint * font * scale).rounded())),
                             height: max(1, Int((r.heightPerPoint * font * scale).rounded())))
        }

        /// The cell in points.
        public func cell(_ font: Double) -> CellSize {
            let p = pixels(font)
            return CellSize(width: Double(p.width) / scale, height: Double(p.height) / scale)
        }

        /// Records what the engine reported; true when that is news.
        public mutating func record(font: Double, widthPx: Int, heightPx: Int) -> Bool {
            guard widthPx > 0, heightPx > 0 else { return false }
            let p = PixelCell(width: widthPx, height: heightPx)
            if ratio == nil { ratio = CellRatio.measure(cellWidthPx: widthPx, cellHeightPx: heightPx, fontSize: font, scale: scale) }
            if measured[font] == p { return false }
            measured[font] = p
            return true
        }
    }
}
