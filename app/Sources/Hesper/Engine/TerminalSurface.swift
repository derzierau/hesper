import AppKit

/// What a surface runs: libghostty owns a normal PTY and execs this.
struct TerminalCommand: Equatable, Sendable {
    var argv: [String]
    var env: [String: String] = [:]
    var workingDirectory: String?
}

/// Engine-neutral terminal view. The app only talks to this protocol, so the
/// engine (libghostty today) can be swapped without touching the UI.
@MainActor
protocol TerminalSurface: AnyObject {
    var view: NSView { get }
    var delegate: (any TerminalSurfaceDelegate)? { get set }

    /// Starts the command once the view is in a window with a size.
    func start(_ command: TerminalCommand, fontSize: Double)
    /// False stops rendering entirely (offscreen, hidden, occluded).
    var isRenderingVisible: Bool { get set }
    /// Whether keyboard and mouse input reach the terminal.
    var acceptsInput: Bool { get set }
    /// Render as soon as output arrives (focus view) instead of vsync-paced.
    var prefersLowLatency: Bool { get set }
    var fontSize: Double { get }
    func setFontSize(_ points: Double)
    /// The engine's current grid and cell size in pixels (backing store).
    var metrics: TerminalMetrics? { get }
    func setFocused(_ focused: Bool)
    /// Pastes text as if typed (used by tests and the latency probe).
    func sendText(_ text: String)
    /// Sends one key press+release the way a real keystroke would arrive.
    func sendKey(character: Character)
    /// The visible screen as plain text.
    func readScreen() -> String?
    var processExited: Bool { get }
    func close()
}

struct TerminalMetrics: Equatable, Sendable {
    var cols: Int
    var rows: Int
    var widthPx: Int
    var heightPx: Int
    var cellWidthPx: Int
    var cellHeightPx: Int
}

@MainActor
protocol TerminalSurfaceDelegate: AnyObject {
    func surfaceDidExit(_ surface: any TerminalSurface)
    func surfaceMetricsDidChange(_ surface: any TerminalSurface)
    func surfaceTitleDidChange(_ surface: any TerminalSurface, title: String)
}

extension TerminalSurfaceDelegate {
    func surfaceTitleDidChange(_ surface: any TerminalSurface, title: String) {}
}
