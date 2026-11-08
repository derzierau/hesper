// The surface is libghostty's UIKit view; a Mac never builds it.
#if os(iOS)
    import Foundation
    import GhosttyTerminal
    import Synchronization
    import UIKit

    /// A Mac's terminal drawn on the phone by libghostty.
    ///
    /// The bytes come from the Mac's PTY (`feed`), never from a local
    /// process; what the terminal itself sends back (mouse reports for a
    /// `wheel`, arrow keys, query replies) goes to `onInput`, which the
    /// holder forwards to the Mac only when it may type. The Mac owns the
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
        /// Called, on libghostty's thread, with every byte the terminal
        /// sends to the process; nil drops them.
        public var onInput: (@Sendable (Data) -> Void)? {
            get { input.handler.withLock { $0 } }
            set { input.handler.withLock { $0 = newValue } }
        }

        private let terminal: UITerminalView
        // Sendable (libghostty: `receive` may be called from any thread),
        // so `feed` needs no hop to the main actor.
        private nonisolated let session: InMemoryTerminalSession
        /// `onInput`, behind a lock: the session's `write` runs on
        /// libghostty's queue while the holder sets it on the main actor.
        private let input = InputSink()
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

        /// The key bar's colours: the holder's theme (this package names
        /// no colour).
        public struct KeyColors {
            public var key: UIColor
            public var label: UIColor
            /// An armed or locked Ctrl.
            public var active: UIColor
            public var onActive: UIColor

            public init(key: UIColor, label: UIColor, active: UIColor, onActive: UIColor) {
                self.key = key
                self.label = label
                self.active = active
                self.onActive = onActive
            }
        }

        /// `background` fills the cells the Mac's output leaves at the
        /// default colour, and the rest of the view; `keys` colours the key
        /// bar over the keyboard.
        public init(background: UIColor, keys: KeyColors) {
            session = InMemoryTerminalSession(write: { [input] data in input.send(data) }, resize: { _ in })
            terminal = UITerminalView(frame: .zero)
            terminal.layer.anchorPoint = .zero
            terminal.isUserInteractionEnabled = false
            terminal.controller = Self.controller(background: Self.hex(background))
            terminal.configuration = TerminalSurfaceOptions(
                backend: .inMemory(session),
                fontSize: Self.fontSize
            )
            terminal.delegate = self
            // libghostty's own key bar over the keyboard: the keys a prompt
            // needs that the iOS keyboard lacks. Ctrl is sticky, so Ctrl
            // then `c` interrupts.
            terminal.inputAccessoryItems = Self.keyRow
            terminal.inputAccessoryStyle = TerminalInputAccessoryStyle(
                regularBackground: keys.key,
                regularForeground: keys.label,
                activeBackground: keys.active,
                activeForeground: keys.onActive
            )
        }

        /// The key bar's keys, in order.
        public static let keyRow: [TerminalInputAccessoryItem] = [
            .esc, .tab, .ctrl, .divider, .arrowLeft, .arrowUp, .arrowDown, .arrowRight,
        ]

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

        /// The keyboard is up: the terminal is the first responder.
        public var keyboardUp: Bool { terminal.isFirstResponder }

        /// Brings up the keyboard; every key it types goes to `onInput`.
        /// False while the view is not in a window. The view still takes
        /// no touches: first responder does not need them.
        @discardableResult
        public func focusKeyboard() -> Bool {
            terminal.acquireProgrammaticFocus()
        }

        /// Puts the keyboard away.
        public func blurKeyboard() {
            guard terminal.isFirstResponder else { return }
            terminal.resignFirstResponder()
        }

        /// A key the phone sends without the keyboard (the dock's Send).
        public enum Key: Sendable {
            case enter, escape, tab, up, down, left, right, ctrlC
        }

        /// Presses and releases `key`, as a hardware keyboard would. Needs
        /// a surface (the view is drawn), not focus.
        public func press(_ key: Key) {
            switch key {
            case .enter: terminal.sendKey(.enter)
            case .escape: terminal.sendKey(.escape)
            case .tab: terminal.sendKey(.tab)
            case .up: terminal.sendKey(.arrowUp)
            case .down: terminal.sendKey(.arrowDown)
            case .left: terminal.sendKey(.arrowLeft)
            case .right: terminal.sendKey(.arrowRight)
            case .ctrlC: terminal.sendKey(.c, modifiers: .ctrl)
            }
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

        /// A wheel scroll of `points` (the unscaled view's), positive when
        /// the finger moved down: towards older text. libghostty decides
        /// by the agent's modes: its own scrollback on the primary screen,
        /// mouse reports or arrow keys (to `onInput`) on the alternate one.
        /// A precision scroll in pixels, so one cell height of finger
        /// travel is one line, as on a trackpad. libghostty reports a
        /// wheel only where it knows the pointer to be, and a phone has
        /// none, so a position inside the grid comes first.
        public func wheel(points: CGFloat) {
            guard points != 0 else { return }
            terminal.sendMousePos(x: Self.pointer, y: Self.pointer)
            terminal.sendMouseScroll(
                x: 0, y: Double(points) * Double(displayScale), mods: TerminalScrollModifiers(precision: true))
        }

        /// Where the phone's "pointer" sits for wheel reports, in points.
        private static let pointer: Double = 30

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

    /// The holder's input handler, shared with the session's `write`.
    private nonisolated final class InputSink: Sendable {
        let handler = Mutex<(@Sendable (Data) -> Void)?>(nil)
        func send(_ data: Data) { handler.withLock { $0 }?(data) }
    }
#endif
