import AppKit
import libghostty

/// A libghostty surface in an NSView. libghostty owns the PTY (it execs the
/// command) and renders on its own thread into the layer it installs on this
/// view; we feed it size, scale, focus, occlusion, keys and mouse.
@MainActor
final class GhosttySurfaceView: NSView, TerminalSurface, @preconcurrency NSTextInputClient {
    weak var delegate: (any TerminalSurfaceDelegate)?
    var view: NSView { self }

    nonisolated(unsafe) private(set) var surface: ghostty_surface_t?
    private var pendingCommand: TerminalCommand?
    private(set) var fontSize: Double = 13
    private(set) var processExited = false
    var acceptsInput = false
    var prefersLowLatency = false
    var isRenderingVisible = true { didSet { updateOcclusion() } }
    private var windowVisible = true
    private var occlusionObserver: NSObjectProtocol?
    private var screenObserver: NSObjectProtocol?

    // Key handling state (mirrors Ghostty.app's SurfaceView).
    private var markedText = NSMutableAttributedString()
    private var keyTextAccumulator: [String]?
    private var trackingArea: NSTrackingArea?

    /// Called on every RENDER request and cell size change (perf probes).
    var onRender: (() -> Void)?

    override init(frame: NSRect) {
        super.init(frame: frame)
        // libghostty installs its own Metal-backed layer on this view.
        wantsLayer = true
    }

    @available(*, unavailable)
    required init?(coder: NSCoder) { fatalError() }

    isolated deinit {
        if let surface { ghostty_surface_free(surface) }
        if let occlusionObserver { NotificationCenter.default.removeObserver(occlusionObserver) }
        if let screenObserver { NotificationCenter.default.removeObserver(screenObserver) }
    }

    // MARK: TerminalSurface

    func start(_ command: TerminalCommand, fontSize: Double) {
        self.fontSize = fontSize
        pendingCommand = command
        createSurfaceIfReady()
    }

    func setFontSize(_ points: Double) {
        let p = (points * 100).rounded() / 100
        guard p > 0, abs(p - fontSize) > 0.001 else { return }
        fontSize = p
        guard let surface else { return }
        let action = "set_font_size:\(p)"
        _ = action.withCString { ghostty_surface_binding_action(surface, $0, UInt(action.utf8.count)) }
    }

    var metrics: TerminalMetrics? {
        guard let surface else { return nil }
        let s = ghostty_surface_size(surface)
        return TerminalMetrics(cols: Int(s.columns), rows: Int(s.rows), widthPx: Int(s.width_px), heightPx: Int(s.height_px),
                               cellWidthPx: Int(s.cell_width_px), cellHeightPx: Int(s.cell_height_px))
    }

    func setFocused(_ focused: Bool) {
        guard let surface else { return }
        ghostty_surface_set_focus(surface, focused)
    }

    func sendText(_ text: String) {
        guard let surface else { return }
        text.withCString { ghostty_surface_text(surface, $0, UInt(text.utf8.count)) }
    }

    func sendKey(character: Character) {
        guard let window, let code = Self.keyCodes[Character(character.lowercased())] else { sendText(String(character)); return }
        let chars = String(character)
        for type in [NSEvent.EventType.keyDown, .keyUp] {
            guard let ev = NSEvent.keyEvent(with: type, location: .zero, modifierFlags: [], timestamp: ProcessInfo.processInfo.systemUptime,
                                            windowNumber: window.windowNumber, context: nil, characters: chars,
                                            charactersIgnoringModifiers: chars, isARepeat: false, keyCode: code) else { continue }
            if type == .keyDown { keyDown(with: ev) } else { keyUp(with: ev) }
        }
    }

    func readScreen() -> String? {
        guard let surface else { return nil }
        var text = ghostty_text_s()
        let sel = ghostty_selection_s(
            top_left: ghostty_point_s(tag: GHOSTTY_POINT_VIEWPORT, coord: GHOSTTY_POINT_COORD_TOP_LEFT, x: 0, y: 0),
            bottom_right: ghostty_point_s(tag: GHOSTTY_POINT_VIEWPORT, coord: GHOSTTY_POINT_COORD_BOTTOM_RIGHT, x: 0, y: 0),
            rectangle: false)
        guard ghostty_surface_read_text(surface, sel, &text) else { return nil }
        defer { ghostty_surface_free_text(surface, &text) }
        guard let p = text.text else { return "" }
        return String(decoding: UnsafeRawBufferPointer(start: p, count: Int(text.text_len)), as: UTF8.self)
    }

    func close() {
        if let surface {
            ghostty_surface_free(surface)
            self.surface = nil
        }

        removeFromSuperview()
    }

    // MARK: Engine callbacks (from GhosttyRuntime)

    func engineCellSizeChanged(width: UInt32, height: UInt32) {
        delegate?.surfaceMetricsDidChange(self)
    }

    /// The terminal's title (OSC 0/2): tiles get their scroll position
    /// this way from `hesperd attach --view`.
    func engineTitle(_ title: String) {
        delegate?.surfaceTitleDidChange(self, title: title)
    }

    func engineChildExited() {
        guard !processExited else { return }
        processExited = true
        delegate?.surfaceDidExit(self)
    }

    func engineRenderRequested() { onRender?() }

    func engineMouseShape(_ shape: ghostty_action_mouse_shape_e) {
        guard acceptsInput else { return }
        switch shape {
        case GHOSTTY_MOUSE_SHAPE_TEXT: NSCursor.iBeam.set()
        case GHOSTTY_MOUSE_SHAPE_POINTER: NSCursor.pointingHand.set()
        default: NSCursor.arrow.set()
        }
    }

    func engineCompleteClipboard(text: String, state: UnsafeMutableRawPointer) {
        guard let surface else { return }
        let mime = strdup("text/plain")
        let data = strdup(text)
        defer { free(mime); free(data) }
        var content = ghostty_clipboard_content_s(mime: mime, data: data, len: text.utf8.count)
        withUnsafePointer(to: &content) { cp in
            var done = ghostty_clipboard_complete_s(contents: cp, contents_len: 1, available: nil, available_len: 0, confirmed: false, remember: false)
            ghostty_surface_complete_clipboard_request(surface, &done, state)
        }
    }

    func engineDenyClipboard(state: UnsafeMutableRawPointer) {
        guard let surface else { return }
        ghostty_surface_deny_clipboard_request(surface, state)
    }

    // MARK: Lifecycle

    override func viewDidMoveToWindow() {
        super.viewDidMoveToWindow()
        if let occlusionObserver { NotificationCenter.default.removeObserver(occlusionObserver) }
        if let screenObserver { NotificationCenter.default.removeObserver(screenObserver) }
        guard let window else { return }
        occlusionObserver = NotificationCenter.default.addObserver(forName: NSWindow.didChangeOcclusionStateNotification, object: window, queue: .main) { [weak self] _ in
            MainActor.assumeIsolated { self?.updateOcclusion() }
        }
        screenObserver = NotificationCenter.default.addObserver(forName: NSWindow.didChangeScreenNotification, object: window, queue: .main) { [weak self] _ in
            MainActor.assumeIsolated { self?.updateDisplayID() }
        }
        createSurfaceIfReady()
        updateOcclusion()
        updateDisplayID()
    }

    override func setFrameSize(_ newSize: NSSize) {
        super.setFrameSize(newSize)
        createSurfaceIfReady()
        syncSize()
    }

    override func viewDidChangeBackingProperties() {
        super.viewDidChangeBackingProperties()
        guard let surface, let window else { return }
        let scale = window.backingScaleFactor
        layer?.contentsScale = scale
        ghostty_surface_set_content_scale(surface, scale, scale)
        syncSize()
    }

    private func createSurfaceIfReady() {
        guard surface == nil, let command = pendingCommand, let window, bounds.width > 0, bounds.height > 0,
              let rt = GhosttyRuntime.shared, let app = prefersLowLatency ? (rt.focusApp ?? rt.app) : rt.app else { return }
        pendingCommand = nil
        var cfg = ghostty_surface_config_new()
        cfg.platform_tag = GHOSTTY_PLATFORM_MACOS
        cfg.platform = ghostty_platform_u(macos: ghostty_platform_macos_s(nsview: Unmanaged.passUnretained(self).toOpaque()))
        cfg.userdata = Unmanaged.passUnretained(self).toOpaque()
        cfg.backend = GHOSTTY_SURFACE_IO_BACKEND_EXEC
        cfg.scale_factor = window.backingScaleFactor
        cfg.font_size = Float(fontSize)
        cfg.wait_after_command = false
        cfg.context = GHOSTTY_SURFACE_CONTEXT_WINDOW
        // libghostty always runs the command through a login shell (login(1) + bash -c "exec -l ...").
        let commandLine = command.argv.map(Self.quote).joined(separator: " ")

        // Keep C strings alive for the duration of ghostty_surface_new.
        var cStrings: [UnsafeMutablePointer<CChar>] = []
        func c(_ s: String) -> UnsafeMutablePointer<CChar> { let p = strdup(s)!; cStrings.append(p); return p }
        defer { cStrings.forEach { free($0) } }
        cfg.command = UnsafePointer(c(commandLine))
        if let wd = command.workingDirectory { cfg.working_directory = UnsafePointer(c(wd)) }
        var envs = command.env.sorted(by: { $0.key < $1.key }).map { ghostty_env_var_s(key: c($0.key), value: c($0.value)) }
        surface = envs.withUnsafeMutableBufferPointer { buf in
            cfg.env_vars = buf.baseAddress
            cfg.env_var_count = buf.count
            return ghostty_surface_new(app, &cfg)
        }
        guard surface != nil else {
            processExited = true
            delegate?.surfaceDidExit(self)
            return
        }
        syncSize()
        updateOcclusion()
        updateDisplayID()
    }

    private func syncSize() {
        guard let surface else { return }
        let px = convertToBacking(bounds.size)
        guard px.width > 0, px.height > 0 else { return }
        ghostty_surface_set_size(surface, UInt32(px.width), UInt32(px.height))
    }

    private func updateOcclusion() {
        windowVisible = window?.occlusionState.contains(.visible) ?? false
        guard let surface else { return }
        ghostty_surface_set_occlusion(surface, isRenderingVisible && windowVisible && !isHiddenOrHasHiddenAncestor)
    }

    override func viewDidHide() { super.viewDidHide(); updateOcclusion() }
    override func viewDidUnhide() { super.viewDidUnhide(); updateOcclusion() }

    private func updateDisplayID() {
        guard let surface, let screen = window?.screen,
              let id = screen.deviceDescription[NSDeviceDescriptionKey("NSScreenNumber")] as? UInt32 else { return }
        ghostty_surface_set_display_id(surface, id)
    }

    static func quote(_ s: String) -> String {
        if !s.isEmpty, s.allSatisfy({ $0.isLetter || $0.isNumber || "/-_.=:@%+,".contains($0) }) { return s }
        return "'" + s.replacingOccurrences(of: "'", with: "'\\''") + "'"
    }

    // MARK: Input

    override var acceptsFirstResponder: Bool { acceptsInput }

    override func becomeFirstResponder() -> Bool {
        let ok = super.becomeFirstResponder()
        if ok { setFocused(true) }
        return ok
    }

    override func resignFirstResponder() -> Bool {
        let ok = super.resignFirstResponder()
        if ok { setFocused(false) }
        return ok
    }

    override func hitTest(_ point: NSPoint) -> NSView? {
        acceptsInput ? super.hitTest(point) : nil
    }

    override func keyDown(with event: NSEvent) {
        guard let surface, acceptsInput else { super.keyDown(with: event); return }
        let action = event.isARepeat ? GHOSTTY_ACTION_REPEAT : GHOSTTY_ACTION_PRESS

        // Option-as-alt etc.: ask libghostty which modifiers translate text.
        let translationMods = Self.flags(from: ghostty_surface_key_translation_mods(surface, Self.mods(event.modifierFlags)), keeping: event.modifierFlags)
        var translationEvent = event
        if translationMods != event.modifierFlags,
           let ev = NSEvent.keyEvent(with: event.type, location: event.locationInWindow, modifierFlags: translationMods,
                                     timestamp: event.timestamp, windowNumber: event.windowNumber, context: nil,
                                     characters: event.characters(byApplyingModifiers: translationMods) ?? "",
                                     charactersIgnoringModifiers: event.charactersIgnoringModifiers ?? "",
                                     isARepeat: event.isARepeat, keyCode: event.keyCode) {
            translationEvent = ev
        }

        let markedBefore = markedText.length > 0
        keyTextAccumulator = []
        defer { keyTextAccumulator = nil }
        interpretKeyEvents([translationEvent])

        if let list = keyTextAccumulator, !list.isEmpty {
            for text in list { sendKeyEvent(action, event, translationMods: translationMods, text: text, composing: false) }
        } else {
            sendKeyEvent(action, event, translationMods: translationMods, text: Self.ghosttyCharacters(translationEvent),
                         composing: markedText.length > 0 || markedBefore)
        }
    }

    override func keyUp(with event: NSEvent) {
        guard acceptsInput else { super.keyUp(with: event); return }
        sendKeyEvent(GHOSTTY_ACTION_RELEASE, event, translationMods: nil, text: nil, composing: false)
    }

    override func flagsChanged(with event: NSEvent) {
        guard acceptsInput, let surface else { return }
        let mod: NSEvent.ModifierFlags
        switch event.keyCode {
        case 0x39: mod = .capsLock
        case 0x38, 0x3C: mod = .shift
        case 0x3B, 0x3E: mod = .control
        case 0x3A, 0x3D: mod = .option
        case 0x37, 0x36: mod = .command
        default: return
        }
        if markedText.length > 0 { return }
        let pressed = event.modifierFlags.contains(mod)
        var ev = ghostty_input_key_s()
        ev.action = pressed ? GHOSTTY_ACTION_PRESS : GHOSTTY_ACTION_RELEASE
        ev.keycode = UInt32(event.keyCode)
        ev.mods = Self.mods(event.modifierFlags)
        ev.consumed_mods = GHOSTTY_MODS_NONE
        _ = ghostty_surface_key(surface, ev)
    }

    override func doCommand(by selector: Selector) {
        // Handled by libghostty via keyDown; never beep.
    }

    private func sendKeyEvent(_ action: ghostty_input_action_e, _ event: NSEvent, translationMods: NSEvent.ModifierFlags?, text: String?, composing: Bool) {
        guard let surface else { return }
        var ev = ghostty_input_key_s()
        ev.action = action
        ev.keycode = UInt32(event.keyCode)
        ev.mods = Self.mods(event.modifierFlags)
        ev.consumed_mods = Self.mods((translationMods ?? event.modifierFlags).subtracting([.control, .command]))
        ev.composing = composing
        if event.type == .keyDown || event.type == .keyUp,
           let chars = event.characters(byApplyingModifiers: []), let cp = chars.unicodeScalars.first {
            ev.unshifted_codepoint = cp.value
        }
        if let text, !text.isEmpty, let first = text.utf8.first, first >= 0x20 {
            text.withCString { p in
                ev.text = p
                _ = ghostty_surface_key(surface, ev)
            }
        } else {
            _ = ghostty_surface_key(surface, ev)
        }
    }

    static func ghosttyCharacters(_ event: NSEvent) -> String? {
        guard let characters = event.characters else { return nil }
        if characters.count == 1, let scalar = characters.unicodeScalars.first {
            if scalar.value < 0x20 { return event.characters(byApplyingModifiers: event.modifierFlags.subtracting(.control)) }
            if scalar.value >= 0xF700 && scalar.value <= 0xF8FF { return nil }
        }
        return characters
    }

    static func mods(_ f: NSEvent.ModifierFlags) -> ghostty_input_mods_e {
        var m: UInt32 = 0
        if f.contains(.shift) { m |= GHOSTTY_MODS_SHIFT.rawValue }
        if f.contains(.control) { m |= GHOSTTY_MODS_CTRL.rawValue }
        if f.contains(.option) { m |= GHOSTTY_MODS_ALT.rawValue }
        if f.contains(.command) { m |= GHOSTTY_MODS_SUPER.rawValue }
        if f.contains(.capsLock) { m |= GHOSTTY_MODS_CAPS.rawValue }
        let raw = f.rawValue
        if raw & UInt(NX_DEVICERSHIFTKEYMASK) != 0 { m |= GHOSTTY_MODS_SHIFT_RIGHT.rawValue }
        if raw & UInt(NX_DEVICERCTLKEYMASK) != 0 { m |= GHOSTTY_MODS_CTRL_RIGHT.rawValue }
        if raw & UInt(NX_DEVICERALTKEYMASK) != 0 { m |= GHOSTTY_MODS_ALT_RIGHT.rawValue }
        if raw & UInt(NX_DEVICERCMDKEYMASK) != 0 { m |= GHOSTTY_MODS_SUPER_RIGHT.rawValue }
        return ghostty_input_mods_e(m)
    }

    static func flags(from mods: ghostty_input_mods_e, keeping original: NSEvent.ModifierFlags) -> NSEvent.ModifierFlags {
        var f = original
        for (bit, flag) in [(GHOSTTY_MODS_SHIFT, NSEvent.ModifierFlags.shift), (GHOSTTY_MODS_CTRL, .control),
                            (GHOSTTY_MODS_ALT, .option), (GHOSTTY_MODS_SUPER, .command)] {
            if mods.rawValue & bit.rawValue != 0 { f.insert(flag) } else { f.remove(flag) }
        }
        return f
    }

    // MARK: Mouse

    override func updateTrackingAreas() {
        if let trackingArea { removeTrackingArea(trackingArea) }
        let t = NSTrackingArea(rect: bounds, options: [.mouseMoved, .mouseEnteredAndExited, .activeInKeyWindow, .inVisibleRect], owner: self)
        addTrackingArea(t)
        trackingArea = t
        super.updateTrackingAreas()
    }

    private func mousePos(_ event: NSEvent) {
        guard let surface else { return }
        let p = convert(event.locationInWindow, from: nil)
        ghostty_surface_mouse_pos(surface, p.x, bounds.height - p.y, Self.mods(event.modifierFlags))
    }

    private func mouseButton(_ event: NSEvent, _ state: ghostty_input_mouse_state_e, _ button: ghostty_input_mouse_button_e) {
        guard let surface, acceptsInput else { return }
        mousePos(event)
        _ = ghostty_surface_mouse_button(surface, state, button, Self.mods(event.modifierFlags))
    }

    override func mouseDown(with event: NSEvent) {
        if acceptsInput { window?.makeFirstResponder(self) }
        mouseButton(event, GHOSTTY_MOUSE_PRESS, GHOSTTY_MOUSE_LEFT)
    }
    override func mouseUp(with event: NSEvent) { mouseButton(event, GHOSTTY_MOUSE_RELEASE, GHOSTTY_MOUSE_LEFT) }
    override func rightMouseDown(with event: NSEvent) { mouseButton(event, GHOSTTY_MOUSE_PRESS, GHOSTTY_MOUSE_RIGHT) }
    override func rightMouseUp(with event: NSEvent) { mouseButton(event, GHOSTTY_MOUSE_RELEASE, GHOSTTY_MOUSE_RIGHT) }
    override func otherMouseDown(with event: NSEvent) { mouseButton(event, GHOSTTY_MOUSE_PRESS, GHOSTTY_MOUSE_MIDDLE) }
    override func otherMouseUp(with event: NSEvent) { mouseButton(event, GHOSTTY_MOUSE_RELEASE, GHOSTTY_MOUSE_MIDDLE) }
    override func mouseMoved(with event: NSEvent) { if acceptsInput { mousePos(event) } }
    override func mouseDragged(with event: NSEvent) { if acceptsInput { mousePos(event) } }

    override func scrollWheel(with event: NSEvent) {
        guard let surface, acceptsInput else { return }
        var x = event.scrollingDeltaX, y = event.scrollingDeltaY
        var mods: Int32 = 0
        if event.hasPreciseScrollingDeltas {
            x *= 2; y *= 2
            mods |= 1
        }
        let momentum: Int32
        switch event.momentumPhase {
        case .began: momentum = 1
        case .stationary: momentum = 2
        case .changed: momentum = 3
        case .ended: momentum = 4
        case .cancelled: momentum = 5
        case .mayBegin: momentum = 6
        default: momentum = 0
        }
        mods |= momentum << 1
        ghostty_surface_mouse_scroll(surface, x, y, ghostty_input_scroll_mods_t(mods))
    }

    // MARK: Drops
    //
    // The surface itself does nothing with drops: whoever registered it for
    // dragged types (AgentTerminal) gets them through the first ancestor
    // that takes drops (the tile or the focus view), so a drop works
    // whichever view AppKit picks as the destination.

    private var dropHandler: NSView? {
        var v = superview
        while let s = v, s.registeredDraggedTypes.isEmpty { v = s.superview }
        return v
    }

    override func draggingEntered(_ sender: any NSDraggingInfo) -> NSDragOperation { dropHandler?.draggingEntered(sender) ?? [] }
    override func draggingUpdated(_ sender: any NSDraggingInfo) -> NSDragOperation { dropHandler?.draggingUpdated(sender) ?? [] }
    override func draggingExited(_ sender: (any NSDraggingInfo)?) { dropHandler?.draggingExited(sender) }
    override func prepareForDragOperation(_ sender: any NSDraggingInfo) -> Bool { dropHandler?.prepareForDragOperation(sender) ?? false }
    override func performDragOperation(_ sender: any NSDraggingInfo) -> Bool { dropHandler?.performDragOperation(sender) ?? false }

    // MARK: Edit menu

    @objc func copy(_ sender: Any?) { binding("copy_to_clipboard") }
    @objc func paste(_ sender: Any?) { binding("paste_from_clipboard") }
    override func selectAll(_ sender: Any?) { binding("select_all") }

    private func binding(_ action: String) {
        guard let surface else { return }
        _ = action.withCString { ghostty_surface_binding_action(surface, $0, UInt(action.utf8.count)) }
    }

    // MARK: NSTextInputClient

    func insertText(_ string: Any, replacementRange: NSRange) {
        let text: String
        switch string {
        case let s as NSAttributedString: text = s.string
        case let s as String: text = s
        default: return
        }
        unmarkText()
        if keyTextAccumulator != nil {
            keyTextAccumulator?.append(text)
            return
        }
        sendText(text)
    }

    func setMarkedText(_ string: Any, selectedRange: NSRange, replacementRange: NSRange) {
        switch string {
        case let s as NSAttributedString: markedText = NSMutableAttributedString(attributedString: s)
        case let s as String: markedText = NSMutableAttributedString(string: s)
        default: return
        }
        syncPreedit()
    }

    func unmarkText() {
        guard markedText.length > 0 else { return }
        markedText.mutableString.setString("")
        syncPreedit()
    }

    private func syncPreedit() {
        guard let surface else { return }
        let s = markedText.string
        if s.isEmpty {
            ghostty_surface_preedit(surface, nil, 0)
        } else {
            s.withCString { ghostty_surface_preedit(surface, $0, UInt(s.utf8.count)) }
        }
    }

    func selectedRange() -> NSRange { NSRange(location: NSNotFound, length: 0) }
    func markedRange() -> NSRange { markedText.length > 0 ? NSRange(location: 0, length: markedText.length) : NSRange(location: NSNotFound, length: 0) }
    func hasMarkedText() -> Bool { markedText.length > 0 }
    func attributedSubstring(forProposedRange range: NSRange, actualRange: NSRangePointer?) -> NSAttributedString? { nil }
    func validAttributesForMarkedText() -> [NSAttributedString.Key] { [] }
    func characterIndex(for point: NSPoint) -> Int { 0 }

    func firstRect(forCharacterRange range: NSRange, actualRange: NSRangePointer?) -> NSRect {
        guard let surface, let window else { return .zero }
        var x = 0.0, y = 0.0, w = 0.0, h = 0.0
        ghostty_surface_ime_point(surface, &x, &y, &w, &h)
        let local = NSRect(x: x, y: bounds.height - y, width: w, height: h)
        return window.convertToScreen(convert(local, to: nil))
    }

    static let keyCodes: [Character: UInt16] = [
        "a": 0x00, "s": 0x01, "d": 0x02, "f": 0x03, "h": 0x04, "g": 0x05, "z": 0x06, "x": 0x07, "c": 0x08, "v": 0x09,
        "b": 0x0B, "q": 0x0C, "w": 0x0D, "e": 0x0E, "r": 0x0F, "y": 0x10, "t": 0x11, "o": 0x1F, "u": 0x20, "i": 0x22,
        "p": 0x23, "l": 0x25, "j": 0x26, "k": 0x28, "n": 0x2D, "m": 0x2E, "\r": 0x24,
    ]
}
