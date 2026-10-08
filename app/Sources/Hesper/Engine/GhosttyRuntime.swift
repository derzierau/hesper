import AppKit
import libghostty
import os

/// The one libghostty app instance. Every C call into libghostty's app-level
/// API lives here or in GhosttySurfaceView; nothing else imports GhosttyKit.
///
/// Threading (Swift 6): the wakeup callback arrives on any thread and only
/// schedules a tick on the main queue; the action callback is answered on the
/// main thread, and actions that arrive elsewhere are bounced to it.
@MainActor
final class GhosttyRuntime {
    private(set) static var shared: GhosttyRuntime?

    nonisolated(unsafe) private(set) var app: ghostty_app_t?
    nonisolated(unsafe) private(set) var focusApp: ghostty_app_t?
    nonisolated(unsafe) private var config: ghostty_config_t?
    nonisolated(unsafe) private(set) var lowLatencyConfig: ghostty_config_t?
    let configPath: URL
    private(set) var diagnostics: [String] = []

    /// Coalesces wakeups: at most one tick is queued at a time.
    nonisolated let tickPending = OSAllocatedUnfairLock(initialState: false)

    static func start(configText: String) -> GhosttyRuntime {
        if let shared { return shared }
        let rt = GhosttyRuntime(configText: configText)
        shared = rt
        return rt
    }

    private init(configText: String) {
        _ = ghostty_init(UInt(CommandLine.argc), CommandLine.unsafeArgv)
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent("hesper-app-\(getpid())", isDirectory: true)
        try? FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        configPath = dir.appendingPathComponent("ghostty.config")
        // Read-only tiles (this app) draw no cursor: a cursor means "keys go
        // here", which only the active tile and the focus view (the second
        // app below, read-write) are.
        try? (configText + "cursor-opacity = 0\n").write(to: configPath, atomically: true, encoding: .utf8)

        let cfg = ghostty_config_new()
        ghostty_config_load_file(cfg, configPath.path)
        ghostty_config_finalize(cfg)
        let n = ghostty_config_diagnostics_count(cfg)
        for i in 0..<n {
            let d = ghostty_config_get_diagnostic(cfg, i)
            if let m = d.message { diagnostics.append(String(cString: m)) }
        }
        config = cfg

        // The focus view trades power for latency: libghostty's renderer
        // draws as soon as output arrives instead of waiting for vsync, while
        // tiles stay vsync-paced. libghostty fixes a surface's vsync when it
        // is created and crashes when a config or font size changes under
        // many live surfaces (see "As built"), so focus surfaces live in a
        // second libghostty app created with `window-vsync = false`.
        let lowPath = dir.appendingPathComponent("ghostty-focus.config")
        try? (configText + "window-vsync = false\n").write(to: lowPath, atomically: true, encoding: .utf8)
        let low = ghostty_config_new()
        ghostty_config_load_file(low, lowPath.path)
        ghostty_config_finalize(low)
        lowLatencyConfig = low

        var rt = ghostty_runtime_config_s()
        rt.userdata = Unmanaged.passUnretained(self).toOpaque()
        rt.supports_selection_clipboard = false
        rt.wakeup_cb = ghosttyWakeup
        rt.action_cb = ghosttyAction
        rt.read_clipboard_cb = ghosttyReadClipboard
        rt.confirm_read_clipboard_cb = ghosttyConfirmReadClipboard
        rt.write_clipboard_cb = ghosttyWriteClipboard
        rt.close_surface_cb = ghosttyCloseSurface
        app = ghostty_app_new(&rt, cfg)
        focusApp = ghostty_app_new(&rt, low)
        ghostty_app_set_focus(app, NSApp?.isActive ?? true)
        ghostty_app_set_color_scheme(app, GHOSTTY_COLOR_SCHEME_DARK)

        NotificationCenter.default.addObserver(forName: NSApplication.didBecomeActiveNotification, object: nil, queue: .main) { [weak self] _ in
            MainActor.assumeIsolated { for a in [self?.app, self?.focusApp] { if let a { ghostty_app_set_focus(a, true) } } }
        }
        NotificationCenter.default.addObserver(forName: NSApplication.didResignActiveNotification, object: nil, queue: .main) { [weak self] _ in
            MainActor.assumeIsolated { for a in [self?.app, self?.focusApp] { if let a { ghostty_app_set_focus(a, false) } } }
        }
    }

    func tick() {
        tickPending.withLock { $0 = false }
        if let app { ghostty_app_tick(app) }
        if let focusApp { ghostty_app_tick(focusApp) }
    }

    var versionString: String {
        let info = ghostty_info()
        guard let v = info.version else { return "?" }
        return String(decoding: UnsafeRawBufferPointer(start: v, count: Int(info.version_len)), as: UTF8.self)
    }

    nonisolated static func from(_ userdata: UnsafeMutableRawPointer?) -> GhosttyRuntime? {
        guard let userdata else { return nil }
        return Unmanaged<GhosttyRuntime>.fromOpaque(userdata).takeUnretainedValue()
    }
}

// MARK: - C callbacks (nonisolated; hop to main where needed)

private func ghosttyWakeup(_ userdata: UnsafeMutableRawPointer?) {
    guard let rt = GhosttyRuntime.from(userdata) else { return }
    let schedule = rt.tickPending.withLock { pending -> Bool in
        if pending { return false }
        pending = true
        return true
    }
    guard schedule else { return }
    DispatchQueue.main.async {
        MainActor.assumeIsolated { rt.tick() }
    }
}

private func ghosttyAction(_ app: ghostty_app_t?, _ target: ghostty_target_s, _ action: ghostty_action_s) -> Bool {
    guard target.tag == GHOSTTY_TARGET_SURFACE, let s = target.target.surface,
          let ud = ghostty_surface_userdata(s) else {
        // App-level actions (quit, new window, …) belong to Ghostty.app's UI,
        // not ours. Report them handled so nothing falls through.
        return true
    }
    let view = Unmanaged<GhosttySurfaceView>.fromOpaque(ud).takeUnretainedValue()
    let tag = action.tag
    // Copy what we need out of the union before leaving this call.
    var cell: (UInt32, UInt32)?
    if tag == GHOSTTY_ACTION_CELL_SIZE { cell = (action.action.cell_size.width, action.action.cell_size.height) }
    var shape: ghostty_action_mouse_shape_e?
    if tag == GHOSTTY_ACTION_MOUSE_SHAPE { shape = action.action.mouse_shape }
    var title: String?
    if tag == GHOSTTY_ACTION_SET_TITLE, let p = action.action.set_title.title { title = String(cString: p) }
    var url: String?
    if tag == GHOSTTY_ACTION_OPEN_URL, let p = action.action.open_url.url {
        url = String(decoding: UnsafeRawBufferPointer(start: p, count: Int(action.action.open_url.len)), as: UTF8.self)
    }
    let work: @MainActor () -> Void = {
        switch tag {
        case GHOSTTY_ACTION_CELL_SIZE:
            if let cell { view.engineCellSizeChanged(width: cell.0, height: cell.1) }
        case GHOSTTY_ACTION_SHOW_CHILD_EXITED:
            view.engineChildExited()
        case GHOSTTY_ACTION_MOUSE_SHAPE:
            if let shape { view.engineMouseShape(shape) }
        case GHOSTTY_ACTION_OPEN_URL:
            if let url, let u = URL(string: url) { NSWorkspace.shared.open(u) }
        case GHOSTTY_ACTION_RENDER:
            view.engineRenderRequested()
        case GHOSTTY_ACTION_SET_TITLE:
            if let title { view.engineTitle(title) }
        default:
            break
        }
    }
    if Thread.isMainThread {
        MainActor.assumeIsolated { work() }
    } else {
        DispatchQueue.main.async { MainActor.assumeIsolated { work() } }
    }
    // Handled: nothing ghostty-app-specific (tabs, splits, windows, quit)
    // may fall through to the shell or the core.
    return true
}

private func ghosttyCloseSurface(_ userdata: UnsafeMutableRawPointer?, _ processAlive: Bool) {
    guard let userdata else { return }
    let view = Unmanaged<GhosttySurfaceView>.fromOpaque(userdata).takeUnretainedValue()
    DispatchQueue.main.async { MainActor.assumeIsolated { view.engineChildExited() } }
}

private func ghosttyWriteClipboard(_ userdata: UnsafeMutableRawPointer?, _ clipboard: ghostty_clipboard_e,
                                   _ contents: UnsafePointer<ghostty_clipboard_content_s>?, _ count: Int, _ confirm: Bool) {
    guard clipboard == GHOSTTY_CLIPBOARD_STANDARD, let contents, count > 0 else { return }
    var text: String?
    for i in 0..<count {
        let c = contents[i]
        let mime = c.mime.map { String(cString: $0) } ?? ""
        if mime.hasPrefix("text/plain") || text == nil, let d = c.data {
            text = String(decoding: UnsafeRawBufferPointer(start: d, count: c.len), as: UTF8.self)
        }
    }
    guard let text else { return }
    DispatchQueue.main.async {
        NSPasteboard.general.clearContents()
        NSPasteboard.general.setString(text, forType: .string)
    }
}

private func ghosttyReadClipboard(_ userdata: UnsafeMutableRawPointer?, _ clipboard: ghostty_clipboard_e,
                                  _ state: UnsafeMutableRawPointer?, _ mimes: UnsafePointer<UnsafePointer<CChar>?>?,
                                  _ mimeCount: Int, _ list: Bool) -> ghostty_clipboard_read_result_e {
    guard let userdata, let state, clipboard == GHOSTTY_CLIPBOARD_STANDARD else { return GHOSTTY_CLIPBOARD_READ_UNAVAILABLE }
    let view = Unmanaged<GhosttySurfaceView>.fromOpaque(userdata).takeUnretainedValue()
    nonisolated(unsafe) let st = state
    let complete: @MainActor () -> Void = {
        let text = NSPasteboard.general.string(forType: .string) ?? ""
        view.engineCompleteClipboard(text: text, state: st)
    }
    if Thread.isMainThread { MainActor.assumeIsolated { complete() } } else { DispatchQueue.main.async { MainActor.assumeIsolated { complete() } } }
    return GHOSTTY_CLIPBOARD_READ_STARTED
}

private func ghosttyConfirmReadClipboard(_ userdata: UnsafeMutableRawPointer?, _ payload: UnsafePointer<ghostty_clipboard_confirm_s>?,
                                         _ state: UnsafeMutableRawPointer?, _ request: ghostty_clipboard_request_e) {
    // Programs asking to read the clipboard (OSC 52) are denied; pastes the
    // user starts never come through here.
    guard let userdata, let state else { return }
    let view = Unmanaged<GhosttySurfaceView>.fromOpaque(userdata).takeUnretainedValue()
    nonisolated(unsafe) let st = state
    DispatchQueue.main.async { MainActor.assumeIsolated { view.engineDenyClipboard(state: st) } }
}
