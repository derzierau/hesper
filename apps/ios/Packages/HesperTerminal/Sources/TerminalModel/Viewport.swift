import CoreGraphics

/// How a Mac's terminal grid sits in the phone's view: the scale it is
/// drawn at, its scaled size, and how far it can be panned. All sizes are
/// points; `cell` is one cell of the unscaled surface.
///
/// A mirror's grid is the Mac's, so the phone scales it down instead:
/// `.fitWidth` shows every column, `.readable` draws text at about 18 pt
/// and pans sideways. A pinch zooms between the two limits: never smaller
/// than fit width, never magnified past 1.0, where the text would blur.
/// `.fitted` is the grid the phone asked for (it owns the size): drawn
/// unscaled, no pinch, no sideways pan.
public struct Viewport: Equatable, Sendable {
    public enum Mode: Equatable, Sendable {
        case readable
        case fitWidth
        /// The phone owns the grid, sized to the view: unscaled.
        case fitted
    }

    /// The cell width `.readable` aims for, in points: an 18 pt monospace
    /// font's advance (0.6 em). Width, not height: the height holds the
    /// font's line spacing (JetBrains Mono's is 1.32 em), so a cell as tall
    /// as a font size is a smaller font. Never magnified: on a surface
    /// whose cell is narrower than this, readable is its unscaled size.
    public static let readableCellWidth: CGFloat = 10.8
    /// Grids up to this many columns fit the width by default.
    public static let fitWidthColumns = 90

    public private(set) var cols: Int
    public private(set) var rows: Int
    /// A new size (a rotation, the dock opening) drops any pinch zoom.
    public var view: CGSize {
        didSet { if view != oldValue { pinch = nil } }
    }
    public var cell: CGSize {
        didSet { if cell != oldValue { pinch = nil } }
    }
    /// Setting a mode drops any pinch zoom.
    public var mode: Mode {
        didSet { pinch = nil }
    }
    /// A pinch's scale, before clamping; nil follows the mode.
    private var pinch: CGFloat?

    public init(grid: (cols: Int, rows: Int), view: CGSize, cell: CGSize, mode: Mode) {
        self.cols = max(0, grid.cols)
        self.rows = max(0, grid.rows)
        self.view = view
        self.cell = cell
        self.mode = mode
    }

    public static func defaultMode(cols: Int) -> Mode {
        cols <= fitWidthColumns ? .fitWidth : .readable
    }

    /// The Mac resized its terminal. The mode stays; a pinch is dropped,
    /// so the new grid is shown the way its mode shows it.
    public mutating func setGrid(cols: Int, rows: Int) {
        guard cols != self.cols || rows != self.rows else { return }
        self.cols = max(0, cols)
        self.rows = max(0, rows)
        pinch = nil
    }

    /// A pinch asks for this scale; `scale` clamps it.
    public mutating func zoom(to scale: CGFloat) {
        guard mode != .fitted else { return }
        pinch = scale
    }

    /// The unscaled size of the grid.
    public var gridSize: CGSize {
        CGSize(width: CGFloat(cols) * cell.width, height: CGFloat(rows) * cell.height)
    }

    /// The scale at which every column fits the view's width, at most 1.
    public var fitWidthScale: CGFloat {
        let width = gridSize.width
        guard width > 0, view.width > 0 else { return 1 }
        return min(1, view.width / width)
    }

    public var scale: CGFloat {
        if mode == .fitted { return 1 }
        let wanted: CGFloat
        if let pinch {
            wanted = pinch
        } else {
            switch mode {
            case .fitWidth:
                wanted = fitWidthScale
            case .readable:
                wanted = cell.width > 0 ? Self.readableCellWidth / cell.width : 1
            case .fitted:
                wanted = 1
            }
        }
        return min(1, max(fitWidthScale, wanted))
    }

    /// The grid's size on screen.
    public var contentSize: CGSize {
        let grid = gridSize
        return CGSize(width: grid.width * scale, height: grid.height * scale)
    }

    /// How far the content's top-left corner can be moved left and up, in
    /// points: zero on an axis where the content fits.
    public var panRange: (x: ClosedRange<CGFloat>, y: ClosedRange<CGFloat>) {
        let content = contentSize
        let x = mode == .fitted ? 0 : Self.overflow(content.width, view.width)
        return (x: 0...x, y: 0...Self.overflow(content.height, view.height))
    }

    /// The vertical pan that keeps `row` (counted from the grid's top)
    /// on screen with its bottom edge at the view's bottom, as near as the
    /// pan range allows. With no row (a blank screen) it is the grid's
    /// bottom, as for a full screen, whose last row is the grid's last.
    public func followOffset(lastTextRow row: Int?) -> CGFloat {
        let range = panRange.y
        guard let row else { return range.upperBound }
        let bottom = CGFloat(min(max(row, 0), max(rows - 1, 0)) + 1) * cell.height * scale
        return min(range.upperBound, max(range.lowerBound, bottom - view.height))
    }

    /// How much `content` is longer than `room`; a rounding error's worth
    /// (fit width multiplies and divides by the same width) counts as none.
    private static func overflow(_ content: CGFloat, _ room: CGFloat) -> CGFloat {
        let extra = content - room
        return extra > 1e-6 ? extra : 0
    }
}
