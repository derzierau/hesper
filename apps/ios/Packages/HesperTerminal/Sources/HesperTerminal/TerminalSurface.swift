// The surface is libghostty's UIKit view; a Mac never builds it.
#if os(iOS)
    import Foundation
    import GhosttyTerminal
    import UIKit

    /// A Mac's terminal drawn on the phone by libghostty; the phone never
    /// types into it.
    ///
    /// The bytes come from the Mac's PTY (`feed`), never from a local
    /// process; what the terminal itself would send back (keys, query
    /// replies) is dropped, since a watcher does not type. The Mac owns the
    /// grid: `resize(cols:rows:)` sizes the view so that libghostty lays out
    /// exactly that many cells, and the phone scales the view to fit
    /// (`Viewport`). The view does not take touches: its own pinch would
    /// change the font size and with it the grid. Gestures belong to the
    /// screen that holds it.
    public final class TerminalSurface {
        /// Rows kept above the screen, at most.
        public static let maxScrollback = 10_000
        /// The font size handed to libghostty, before `Viewport` scales it.
        /// libghostty sizes fonts at 96 dpi (a 13 pt font drew a 10.33 pt
        /// cell, 0.6 em of 17.3 pt), so 13.5 draws a true 18 pt
        /// JetBrains Mono: a cell about 10.8 x 24 pt.
        public static let fontSize: Float = 13.5
        /// The font size the phone shows, in true points.
        public static let pointSize = CGFloat(fontSize) * 96 / 72

        /// The view, top-left anchored (`layer.anchorPoint` is `.zero`):
        /// `resize` changes its bounds and leaves its position alone, so a
        /// holder places it with `layer.position` (the pan) and scales it
        /// with `transform` (the zoom) without the corner moving.
        public var view: UIView { terminal }
        /// The grid libghostty lays out now; `(0, 0)` until it has drawn.
        public private(set) var grid: (cols: Int, rows: Int) = (0, 0)
        /// One cell of the unscaled grid, in points, as libghostty measured
        /// it; `.zero` until it has drawn.
        public private(set) var cell: CGSize = .zero
        /// Rows kept above the screen.
        public var scrollback: Int { Self.maxScrollback }
        /// The newest line is on screen.
        public private(set) var isAtBottom = true
        /// Called after `grid`, `cell` or `isAtBottom` changed.
        public var onChange: (() -> Void)?

        private let terminal: UITerminalView
        // Sendable (libghostty: `receive` may be called from any thread),
        // so `feed` needs no hop to the main actor.
        private nonisolated let session: InMemoryTerminalSession
        /// The grid the Mac asked for.
        private var target: (cols: Int, rows: Int)?
        /// Re-layouts since the last `resize`, to stop chasing a grid
        /// libghostty cannot reach.
        private var corrections = 0

        /// Padding off, so the grid is the view's pixels divided by the cell:
        /// with libghostty's default padding the grid came out one short.
        /// `scrollback-limit` is a byte budget in Ghostty, not rows: 10 000
        /// rows of 160 cells of 8 bytes. An estimate (rows also hold styles
        /// and graphemes); the exact row count is libghostty's.
        /// The background goes in the theme as well: the controller applies
        /// its theme over the configuration, and the default theme's
        /// background would win.
        private static func controller(background: String) -> TerminalController {
            var theme = TerminalTheme.default
            theme.light = theme.light.background(background)
            theme.dark = theme.dark.background(background)
            return TerminalController(
                configuration: TerminalConfiguration(startingFrom: .default) {
                    $0.withBackground(background)
                    $0.withFontSize(fontSize)
                    $0.withWindowPaddingX(0)
                    $0.withWindowPaddingY(0)
                    $0.withCustom("window-padding-balance", "false")
                    $0.withCursorStyleBlink(false)
                    $0.withCustom("scrollback-limit", String(maxScrollback * 160 * 8))
                },
                theme: theme
            )
        }

        /// The last cell libghostty measured on any surface: the font and
        /// display do not change, so a fresh surface sizes itself by it and
        /// its first layout is already the Mac's grid.
        private static var measuredCell: CGSize?
        /// Used to size the view before libghostty has measured any cell:
        /// JetBrains Mono's advance (0.6 em) and line height (1.32 em).
        private static let estimatedCell = CGSize(
            width: pointSize * 0.6,
            height: pointSize * 1.32
        )

        /// The cell a surface draws: the last one measured, else the estimate.
        /// A grid that fits the phone is sized by it before a surface drew.
        public static var expectedCell: CGSize { measuredCell ?? estimatedCell }
        /// Reaching the grid takes one pass with a measured cell; more
        /// means the Mac's grid cannot be drawn here.
        private static let maxCorrections = 3

        /// `background` fills the cells the Mac's output leaves at the
        /// default colour, and the rest of the view.
        public init(background: UIColor) {
            session = InMemoryTerminalSession(write: { _ in }, resize: { _ in })
            terminal = UITerminalView(frame: .zero)
            terminal.layer.anchorPoint = .zero
            terminal.isUserInteractionEnabled = false
            terminal.controller = Self.controller(background: Self.hex(background))
            terminal.configuration = TerminalSurfaceOptions(
                backend: .inMemory(session),
                fontSize: Self.fontSize
            )
            terminal.delegate = self
        }

        /// Hands the Mac's output to the terminal. Any thread; bytes that
        /// arrive before the view is on screen are buffered (1 MiB).
        public nonisolated func feed(_ data: Data) {
            session.receive(data)
        }

        /// Sizes the view so that libghostty lays out exactly `cols` by
        /// `rows` cells: the cell it measured times the grid, plus half a
        /// cell so pixel rounding cannot drop a column. Until it has
        /// measured, the cell another surface measured places the view, or
        /// on the first surface an estimate, and the first report corrects
        /// it. `grid` reaches `(cols, rows)` only after a layout pass.
        public func resize(cols: Int, rows: Int) {
            guard cols > 0, rows > 0 else { return }
            target = (cols, rows)
            corrections = 0
            layoutTarget()
        }

        /// Shows the newest line.
        public func scrollToBottom() {
            terminal.surface?.performBindingAction("scroll_to_bottom")
            setAtBottom(true)
        }

        /// The last row of the view with text on it, counted from the top;
        /// nil when the view is blank or not drawn yet. Reads every row's
        /// text, so a caller throttles it.
        public func lastTextRow() -> Int? {
            guard let text = session.readViewportText() else { return nil }
            let rows = text.split(separator: "\n", omittingEmptySubsequences: false)
            return rows.lastIndex { $0.contains { !$0.isWhitespace } }
        }

        /// Scrolls the scrollback by whole rows; negative goes back in time.
        public func scroll(rows: Int) {
            guard rows != 0 else { return }
            terminal.surface?.performBindingAction("scroll_page_lines:\(rows)")
        }

        private func layoutTarget() {
            guard let target else { return }
            let unit = cell != .zero ? cell : Self.measuredCell ?? Self.estimatedCell
            let size = CGSize(
                width: (CGFloat(target.cols) + 0.5) * unit.width,
                height: (CGFloat(target.rows) + 0.5) * unit.height
            )
            guard terminal.bounds.size != size else { return }
            terminal.bounds = CGRect(origin: .zero, size: size)
            terminal.setNeedsLayout()
        }

        private func setAtBottom(_ value: Bool) {
            guard value != isAtBottom else { return }
            isAtBottom = value
            onChange?()
        }

        /// `#rrggbb`, as Ghostty's config takes a colour.
        private static func hex(_ color: UIColor) -> String {
            var r: CGFloat = 0
            var g: CGFloat = 0
            var b: CGFloat = 0
            var a: CGFloat = 0
            color.getRed(&r, green: &g, blue: &b, alpha: &a)
            let byte = { (v: CGFloat) in Int((min(1, max(0, v)) * 255).rounded()) }
            return String(format: "#%02x%02x%02x", byte(r), byte(g), byte(b))
        }

        private var displayScale: CGFloat {
            let scale = terminal.traitCollection.displayScale
            return scale > 0 ? scale : 1
        }
    }

    extension TerminalSurface: TerminalSurfaceGridResizeDelegate, TerminalSurfaceScrollbarDelegate {
        public func terminalDidResize(_ size: TerminalGridMetrics) {
            guard size.columns > 0, size.rows > 0 else { return }
            let scale = displayScale
            if size.cellWidthPixels > 0, size.cellHeightPixels > 0 {
                cell = CGSize(
                    width: CGFloat(size.cellWidthPixels) / scale,
                    height: CGFloat(size.cellHeightPixels) / scale
                )
            } else {
                // No cell in the report: with no padding, the view's pixels
                // over the grid, rounded down, is the cell.
                cell = CGSize(
                    width: (CGFloat(size.widthPixels) / CGFloat(size.columns)).rounded(.down) / scale,
                    height: (CGFloat(size.heightPixels) / CGFloat(size.rows)).rounded(.down) / scale
                )
            }
            Self.measuredCell = cell
            grid = (Int(size.columns), Int(size.rows))
            if let target, grid != target, corrections < Self.maxCorrections {
                corrections += 1
                layoutTarget()
            }
            onChange?()
        }

        public func terminalDidUpdateScrollbar(_ scrollbar: TerminalScrollbar) {
            setAtBottom(scrollbar.offset + scrollbar.len >= scrollbar.total)
        }
    }
#endif
