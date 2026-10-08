import AppKit
import HesperCore

/// Desks (docs/rebuild-contract.md "As built — desks"): the whole
/// arrangement of windows saved per display setup in desks.json, switched
/// automatically when displays are plugged in or out (debounced 1 s, never
/// during a drag or live resize, windows animated), named desks per setup
/// (Window ▸ Save Desk As…, Window ▸ Desks ▸ …, the ⌃⌘D picker), fold to
/// defaults for a setup without a desk. Pure rules: HesperCore/Desks.swift.
@MainActor
final class DeskController: NSObject, NSMenuDelegate {
    unowned let manager: WindowManager
    let url: URL?
    private(set) var book = DeskBook()
    /// The setup the current desk belongs to (what the windows show).
    private(set) var active: DisplaySetup
    /// Injected displays (tests: `--fake-screens`, `inject`); nil: NSScreen.
    private var fake: [DisplayInfo]?
    private var pending: DispatchWorkItem?
    /// Tests: pretend a drag / resize is in progress.
    var interactionBusy = false
    /// Desk switches done (tests) and the last one ("old → new").
    private(set) var switches = 0
    private(set) var lastSwitch = ""
    private var settings: AppSettings { manager.primary.settings }

    init(manager: WindowManager, env: AppEnvironment) {
        self.manager = manager
        if let p = env.values["window-state"] {
            url = URL(fileURLWithPath: p)
        } else if env.values["socket"] == nil && env.values["state-dir"] == nil && ProcessInfo.processInfo.environment["HESPER_SOCKET"] == nil
                    && env.values["selftest-out"] == nil && env.values["perf-out"] == nil && env.values["layout-out"] == nil {
            let dir = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0].appendingPathComponent("Hesper")
            url = dir.appendingPathComponent("desks.json")
        } else {
            url = nil
        }
        if let f = env.values["fake-screens"] { fake = Self.fakeDisplays(f) }
        active = DisplaySetup([])
        super.init()
        active = DisplaySetup(displays)
        load()
        NotificationCenter.default.addObserver(forName: NSApplication.didChangeScreenParametersNotification, object: nil, queue: .main) { [weak self] _ in
            MainActor.assumeIsolated { if self?.fake == nil { self?.displaysChanged() } }
        }
    }

    // MARK: Displays

    var displays: [DisplayInfo] { fake ?? Self.realDisplays() }
    var areas: [ScreenArea] { DisplaySetup.areas(displays) }
    var activeName: String { book.currentDesk(for: active.id)?.name ?? active.defaultName }

    static func realDisplays() -> [DisplayInfo] {
        NSScreen.screens.map { s in
            let n = (s.deviceDescription[NSDeviceDescriptionKey("NSScreenNumber")] as? NSNumber)?.uint32Value ?? 0
            let mm = CGDisplayScreenSize(n)
            let f = s.frame, v = s.visibleFrame
            return DisplayInfo(vendor: CGDisplayVendorNumber(n), model: CGDisplayModelNumber(n), serial: CGDisplaySerialNumber(n),
                               name: s.localizedName, builtin: CGDisplayIsBuiltin(n) != 0, sizeMM: (Double(mm.width), Double(mm.height)),
                               frame: Rect(x: f.minX, y: f.minY, width: f.width, height: f.height),
                               visible: Rect(x: v.minX, y: v.minY, width: v.width, height: v.height), number: String(n))
        }
    }

    /// Test displays carved out of the real main display (so windows stay
    /// visible): "laptop" (one built-in display), "office" (built-in left
    /// 40 % + "Studio Display" right 60 %), "office-reversed" (the same,
    /// listed the other way round), "office-left" (the Studio Display on
    /// the left).
    static func fakeDisplays(_ name: String) -> [DisplayInfo] {
        let v = NSScreen.main?.visibleFrame ?? NSRect(x: 0, y: 0, width: 1500, height: 900)
        func r(_ x: CGFloat, _ w: CGFloat) -> Rect { Rect(x: v.minX + x, y: v.minY, width: w, height: v.height) }
        let lw = (v.width * 0.4).rounded(), sw = v.width - lw
        let laptopAll = DisplayInfo(vendor: 1552, model: 41_002, serial: 0, name: "Built-in Retina Display", builtin: true, sizeMM: (302, 196),
                                    frame: r(0, v.width), number: "901")
        var laptop = laptopAll
        laptop.frame = r(0, lw); laptop.visible = laptop.frame
        let studio = DisplayInfo(vendor: 1552, model: 44_918, serial: 0x5354_5544, name: "Studio Display", builtin: false, sizeMM: (597, 336),
                                 frame: r(lw, sw), number: "902")
        switch name {
        case "office": return [laptop, studio]
        case "office-reversed": return [studio, laptop]
        case "office-left":
            var s = studio, l = laptop
            s.frame = r(0, sw); s.visible = s.frame
            l.frame = r(sw, lw); l.visible = l.frame
            return [s, l]
        default: return [laptopAll]
        }
    }

    /// The stable key of the display a frame overlaps most.
    func displayKey(for f: NSRect) -> String? {
        let ds = displays
        let keys = DisplaySetup.keys(ds)
        var best: (String, CGFloat)?
        for (k, d) in zip(keys, ds) {
            let r = NSRect(x: d.frame.x, y: d.frame.y, width: d.frame.width, height: d.frame.height).intersection(f)
            let a = r.isNull ? 0 : r.width * r.height
            if a > (best?.1 ?? 0) { best = (k, a) }
        }
        return best?.0
    }

    func isOnAttachedDisplay(_ f: NSRect) -> Bool { displayKey(for: f) != nil }

    // MARK: File

    private func load() {
        guard let url else { return }
        if let d = try? Data(contentsOf: url), let b = DeskBook.decode(d, setup: active, displays: displays) {
            book = b
            return
        }
        // The live file: windows.json (before desks) becomes this setup's desk.
        if url.lastPathComponent == "desks.json",
           let d = try? Data(contentsOf: url.deletingLastPathComponent().appendingPathComponent("windows.json")),
           let b = DeskBook.decode(d, setup: active, displays: displays) {
            book = b
            write()
        }
    }

    private func write() {
        guard let url, let data = try? book.encoded() else { return }
        try? FileManager.default.createDirectory(at: url.deletingLastPathComponent(), withIntermediateDirectories: true)
        try? data.write(to: url, options: .atomic)
    }

    // MARK: Current desk

    var currentWindows: SavedWindows? { book.currentDesk(for: active.id)?.windows }
    var currentDesk: Desk? { book.currentDesk(for: active.id) }
    var desksForSetup: [Desk] { book.desks(for: active.id) }

    /// What launch restores: this setup's desk, else the last used desk
    /// of another setup folded to defaults.
    func launchWindows() -> SavedWindows? {
        if let w = currentWindows { return w }
        guard let last = book.lastUsed(excluding: active.id), let main = areas.first else { return nil }
        return DeskFold.fold(last.windows, main: main, agentWindows: settings.foldAgentWindows)
    }

    /// The windows now go into the current desk — only while the displays
    /// are the ones that desk is for (after a plug / unplug the system
    /// moves windows; those frames never reach the old desk).
    func record(_ s: SavedWindows) {
        guard pending == nil, DisplaySetup(displays).id == active.id else { return }
        book.record(s, setup: active, now: Date().timeIntervalSince1970)
        write()
    }

    // MARK: Switching

    /// Tests: these displays are attached now (a display-setup change).
    func inject(_ d: [DisplayInfo]) {
        fake = d
        displaysChanged()
    }

    /// Plug / unplug / arrangement: decide 1 s after the last change.
    func displaysChanged() {
        pending?.cancel()
        let work = DispatchWorkItem { [weak self] in self?.settle() }
        pending = work
        DispatchQueue.main.asyncAfter(deadline: .now() + 1.0, execute: work)
    }

    private var busy: Bool {
        interactionBusy || NSEvent.pressedMouseButtons != 0 || NSApp.windows.contains { $0.inLiveResize }
    }

    private func settle() {
        if busy {
            let work = DispatchWorkItem { [weak self] in self?.settle() }
            pending = work
            DispatchQueue.main.asyncAfter(deadline: .now() + 0.5, execute: work)
            return
        }
        pending = nil
        let now = DisplaySetup(displays)
        guard now.id != active.id else {
            manager.scheduleVisibility()
            return
        }
        let old = active
        let snapshot = manager.restorer.snapshot()
        active = now
        if !settings.desksAutoSwitch {
            // Keep the windows; they become this setup's desk.
            if let s = snapshot { record(s) }
            manager.scheduleVisibility()
            return
        }
        let target: SavedWindows
        if let d = book.currentDesk(for: now.id) {
            target = d.windows
        } else {
            target = DeskFold.fold(snapshot ?? SavedWindows(), main: areas.first ?? ScreenArea(id: "", visible: Rect(x: 0, y: 0, width: 1200, height: 800)),
                                   agentWindows: settings.foldAgentWindows)
        }
        switches += 1
        lastSwitch = "\(book.currentDesk(for: old.id)?.name ?? old.defaultName) → \(book.currentDesk(for: now.id)?.name ?? now.defaultName)"
        manager.restorer.apply(target, animate: true)
        DispatchQueue.main.asyncAfter(deadline: .now() + 0.6) { [weak self] in
            self?.manager.restorer.saveNow()
            self?.manager.scheduleVisibility()
        }
    }

    /// The picker / Window ▸ Desks: another desk (of this setup, or one of
    /// another setup copied into this one).
    func select(_ id: String) {
        guard id != currentDesk?.id else { return }
        manager.restorer.saveNow()
        guard let d = book.select(id, setup: active, now: Date().timeIntervalSince1970) else { return }
        write()
        switches += 1
        lastSwitch = "→ \(d.name)"
        manager.restorer.apply(d.windows, animate: true)
        DispatchQueue.main.asyncAfter(deadline: .now() + 0.6) { [weak self] in self?.manager.restorer.saveNow() }
    }

    /// Save Desk As…: the windows as they are, under a name, for this setup.
    func saveAs(_ name: String) {
        guard let s = manager.restorer.snapshot() else { return }
        book.saveAs(name, windows: s, setup: active, now: Date().timeIntervalSince1970)
        write()
    }

    func rename(_ id: String, to name: String) {
        book.rename(id, to: name)
        write()
    }

    func remove(_ id: String) {
        let wasCurrent = id == currentDesk?.id
        book.remove(id)
        write()
        if wasCurrent, let d = currentDesk { manager.restorer.apply(d.windows, animate: true) }
    }

    // MARK: Picker (overlay system) and menus

    func showPicker() {
        let w = manager.activeWall
        w.window.makeKeyAndOrderFront(nil)
        NSApp.activate(ignoringOtherApps: true)
        w.model.openPopover(.desks)
    }

    func content(_ kind: PopoverKind, model: AppModel) -> PopoverContent {
        if case .deskName(let id) = kind {
            let title = id.isEmpty ? "Save desk as" : "Rename “\(book.desk(id)?.name ?? "")”"
            return PopoverContent(title: title, items: [], note: id.isEmpty ? "For \(active.defaultName); switch with ⌃⌘D" : nil,
                                  hints: [("⏎", id.isEmpty ? "save" : "rename"), ("esc", "close")], field: "Desk name")
        }
        let cur = currentDesk?.id
        func detail(_ d: Desk) -> String {
            let w = d.windows.walls.count, a = d.windows.agentWindows.count
            var parts = ["\(w) wall\(w == 1 ? "" : "s")"]
            if a > 0 { parts.append("\(a) agent window\(a == 1 ? "" : "s")") }
            if d.automatic { parts.append("automatic") }
            return parts.joined(separator: " · ")
        }
        var items = desksForSetup.map { d in
            OverlayItem(id: d.id, section: "This setup", title: d.name, detail: detail(d), checked: d.id == cur,
                        run: { [weak self] in self?.select(d.id) })
        }
        for s in book.setups.sorted(by: { $0.lastUsed > $1.lastUsed }) where s.id != active.id {
            for d in book.desks(for: s.id) {
                items.append(OverlayItem(id: d.id, section: "Other displays", title: d.name, detail: s.screens.map(\.name).joined(separator: " + "),
                                         run: { [weak self] in self?.select(d.id) }))
            }
        }
        items.append(OverlayItem(id: "save", section: "Actions", title: "Save Desk As…", detail: "this arrangement, by name", mark: "＋",
                                 run: { [weak model] in
                                     DispatchQueue.main.async { model?.deskNameText = ""; model?.openPopover(.deskName("")) }
                                 }))
        if let c = currentDesk {
            items.append(OverlayItem(id: "rename", section: "Actions", title: "Rename “\(c.name)”…", mark: "✎",
                                     run: { [weak model] in
                                         DispatchQueue.main.async { model?.deskNameText = c.name; model?.openPopover(.deskName(c.id)) }
                                     }))
        }
        return PopoverContent(title: "Desks · \(active.screens.map(\.name).joined(separator: " + "))", items: items,
                              note: settings.desksAutoSwitch ? "Switches automatically when displays change" : "Automatic switching is off (Settings ▸ Desks)",
                              hints: [("⏎", "switch"), ("esc", "close")])
    }

    func nameSubmitted(_ id: String, _ name: String) {
        if id.isEmpty { saveAs(name) } else { rename(id, to: name) }
    }

    /// Wires a model's picker hooks (every wall).
    func attach(_ m: AppModel) {
        m.deskContent = { [weak self, weak m] k in
            guard let self, let m else { return PopoverContent(title: nil, items: [], note: nil, hints: []) }
            return self.content(k, model: m)
        }
        m.deskNameSubmit = { [weak self] id, name in self?.nameSubmitted(id, name) }
    }

    private let desksMenu = NSMenu(title: "Desks")

    func installMenus(in wm: NSMenu, at index: Int) {
        func item(_ title: String, _ key: String, _ mods: NSEvent.ModifierFlags, _ action: @escaping @MainActor () -> Void) -> NSMenuItem {
            let i = NSMenuItem(title: title, action: #selector(MenuAction.run), keyEquivalent: key)
            i.keyEquivalentModifierMask = mods
            let act = MenuAction(action)
            i.target = act
            i.representedObject = act
            return i
        }
        desksMenu.delegate = self
        let sub = NSMenuItem(title: "Desks", action: nil, keyEquivalent: "")
        sub.submenu = desksMenu
        wm.insertItem(sub, at: index)
        wm.insertItem(item("Desk Picker…", "d", [.command, .control]) { [weak self] in self?.showPicker() }, at: index + 1)
        wm.insertItem(item("Save Desk As…", "", []) { [weak self] in
            guard let self else { return }
            let m = self.manager.activeWall.model
            self.manager.activeWall.window.makeKeyAndOrderFront(nil)
            m.deskNameText = ""
            m.openPopover(.deskName(""))
        }, at: index + 2)
        wm.insertItem(.separator(), at: index + 3)
    }

    func menuNeedsUpdate(_ menu: NSMenu) {
        menu.removeAllItems()
        let cur = currentDesk?.id
        for d in desksForSetup {
            let act = MenuAction { [weak self] in self?.select(d.id) }
            let i = NSMenuItem(title: d.name, action: #selector(MenuAction.run), keyEquivalent: "")
            i.target = act
            i.representedObject = act
            i.state = d.id == cur ? .on : .off
            menu.addItem(i)
        }
        if desksForSetup.isEmpty {
            let i = NSMenuItem(title: "\(active.defaultName) (saved when windows change)", action: nil, keyEquivalent: "")
            i.isEnabled = false
            menu.addItem(i)
        }
    }
}
