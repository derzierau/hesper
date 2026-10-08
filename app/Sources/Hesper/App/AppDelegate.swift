import AppKit
import HesperCore
import SwiftUI

@MainActor
final class AppDelegate: NSObject, NSApplicationDelegate {
    let env = AppEnvironment.resolve()
    var model: AppModel!
    var windowController: MainWindowController!
    var windows: WindowManager!
    var statusItem: StatusItemController?
    var notifier: Notifier?
    var harness: AnyObject?
    var quickLaunch: QuickLaunchController?
    var appControl: AppControl?
    let hotKey = GlobalHotKey()
    private var automated = false

    func applicationDidFinishLaunching(_ notification: Notification) {
        let user = GhosttyUserConfig.load()
        let runtime = GhosttyRuntime.start(configText: TerminalConfigText.make(user: user))
        if !runtime.diagnostics.isEmpty { NSLog("ghostty config: %@", runtime.diagnostics.joined(separator: "; ")) }

        model = AppModel(env: env, userFontSize: user.fontSize)
        WindowManager.prepare() // window layer: before the main toolbar exists
        windowController = MainWindowController(model: model, userFontSize: user.fontSize ?? 13)
        windows = WindowManager(primary: model, main: windowController, env: env, userFontSize: user.fontSize ?? 13)
        buildMenu()
        windows.installMenus()

        automated = env.values["perf-out"] != nil || env.values["selftest-out"] != nil || env.values["layout-out"] != nil || env.values["windows-test-out"] != nil || env.values["walls-perf-out"] != nil || env.values["projects-test-out"] != nil || env.values["desks-test-out"] != nil || env.values["history-test-out"] != nil || env.values["history-perf-out"] != nil
        let ql = QuickLaunchController(model: model)
        quickLaunch = ql
        model.onQuickLaunch = { [weak self] in self?.quickLaunch?.toggle() }
        model.onQuickLaunchPreset = { [weak self] path, machine in self?.quickLaunch?.show(project: path, machine: machine) }
        model.onShowSettings = { [weak self] in self?.showSettings() }
        if !env.options.contains("no-status-item") && !automated {
            let s = StatusItemController(model: model)
            s.showWindow = { [weak self] in self?.showWindow() }
            s.openAgent = { [weak self] id in self?.windows.open(id, focus: true) }
            statusItem = s
        }
        let n = Notifier(model: model, enabled: !automated && !env.options.contains("no-notifications"))
        n.showWindow = { [weak self] in self?.showWindow() }
        n.openAgent = { [weak self] id in self?.windows.open(id, focus: true) }
        n.lookingAt = { [weak self] in
            guard let m = self?.windows.activeModel, m.mode == .focus else { return nil }
            return m.focusedID
        }
        notifier = n
        model.onTransitions = { [weak n] t in n?.post(t) }
        // Closing agents: a background agent finished (and was closed).
        model.onFinishedInBackground = { [weak n, weak self] name in
            n?.postFinished(name)
            if NSApp.isActive, let text = CloseText.notification(.finishedInBackground, name: name) {
                self?.windows.activeModel.showToast(text)
            }
        }
        model.settings.onAutoTidyChanged = { [weak self] in self?.model.scheduleAutoTidy() }
        model.onStarted = { [weak n, weak self] a, quick in
            if quick { n?.postStarted(a) ?? (); if NSApp.isActive { self?.model.goTo(a.id) } }
        }
        registerHotKey()
        // App control: hesperctl open / wall / desk through hesperd. Never
        // in automated runs (a test instance must not take the user's
        // hesperctl calls).
        if !automated && !env.options.contains("no-app-control") { appControl = AppControl(manager: windows) }

        model.start()
        if !automated { FirstRun.watch(model: model) } // once, for a fresh setup
        if let size = env.values["window-size"], let w = windowController.window {
            let parts = size.split(separator: "x").compactMap { Double($0) }
            if parts.count == 2 {
                w.setContentSize(NSSize(width: parts[0], height: parts[1]))
                w.center()
            }
        }
        showWindow()
        windows.restorer.restoreWalls()

        if let out = env.values["history-test-out"] { // shared history
            harness = HistorySelfTest(manager: windows, out: out, env: env)
        } else if let out = env.values["history-perf-out"] {
            harness = HistoryPerf(manager: windows, out: out, env: env)
        } else if let out = env.values["projects-test-out"] {
            harness = ProjectsSelfTest(manager: windows, out: out, env: env)
        } else if let out = env.values["desks-test-out"] {
            harness = DesksSelfTest(manager: windows, out: out, env: env)
        } else if let out = env.values["windows-test-out"] {
            harness = WindowSelfTest(manager: windows, out: out, env: env)
        } else if let out = env.values["walls-perf-out"] {
            harness = WallsPerf(manager: windows, out: out, env: env)
        } else if let out = env.values["perf-out"] {
            harness = PerfRunner(model: model, root: windowController.root, out: out, env: env)
        } else if let out = env.values["layout-out"] {
            harness = LayoutProbe(model: model, root: windowController.root, out: out, env: env)
        } else if let out = env.values["selftest-out"] {
            harness = SelfTest(model: model, root: windowController.root, out: out, phase: env.values["selftest-phase"])
        }
    }

    func showWindow() {
        windowController.showWindow(nil)
        windowController.window?.makeKeyAndOrderFront(nil)
        NSApp.activate(ignoringOtherApps: true)
    }

    func applicationShouldHandleReopen(_ sender: NSApplication, hasVisibleWindows flag: Bool) -> Bool {
        showWindow()
        return true
    }

    func applicationShouldTerminateAfterLastWindowClosed(_ sender: NSApplication) -> Bool { false }

    func applicationWillTerminate(_ notification: Notification) {
        windows?.restorer.saveNow() // window layer: state restoration
    }

    /// Quit: unsaved draft edits and removals waiting out their undo window
    /// go to hesperd first (at most 2 s).
    func applicationShouldTerminate(_ sender: NSApplication) -> NSApplication.TerminateReply {
        guard let model, model.isConnected, model.hasPendingWork else { return .terminateNow }
        // terminateLater would run the loop in the modal panel mode, where
        // main-actor tasks never run: spin the default mode instead.
        var done = false
        Task { @MainActor in
            await model.flushDrafts()
            await model.commitPendingUndo()
            done = true
        }
        let deadline = Date().addingTimeInterval(2)
        while !done && Date() < deadline { RunLoop.main.run(mode: .default, before: Date().addingTimeInterval(0.02)) }
        return .terminateNow
    }

    /// ⌃⌥Space (Settings): the quick launch panel from any app. Never in
    /// automated runs or with --no-hotkey (a test instance must not take
    /// the user's hotkey).
    func registerHotKey() {
        guard !automated, !env.options.contains("no-hotkey"), model.settings.quickLaunchEnabled else { hotKey.unregister(); return }
        hotKey.register(model.settings.hotKey) { [weak self] in self?.quickLaunch?.toggle() }
    }

    // MARK: Menu (mirrors KeyRouter; the window routes the keys first)

    private func buildMenu() {
        let main = NSMenu()
        let appItem = NSMenuItem()
        let appMenu = NSMenu()
        appMenu.addItem(withTitle: "About Hesper", action: #selector(NSApplication.orderFrontStandardAboutPanel(_:)), keyEquivalent: "")
        appMenu.addItem(.separator())
        let settings = NSMenuItem(title: "Settings…", action: #selector(showSettings), keyEquivalent: ",")
        settings.target = self
        appMenu.addItem(settings)
        appMenu.addItem(.separator())
        appMenu.addItem(withTitle: "Hide Hesper", action: #selector(NSApplication.hide(_:)), keyEquivalent: "h")
        appMenu.addItem(withTitle: "Quit Hesper", action: #selector(NSApplication.terminate(_:)), keyEquivalent: "q")
        appItem.submenu = appMenu
        main.addItem(appItem)

        let edit = NSMenuItem()
        let editMenu = NSMenu(title: "Edit")
        editMenu.addItem(withTitle: "Undo", action: Selector(("undo:")), keyEquivalent: "z")
        let redo = editMenu.addItem(withTitle: "Redo", action: Selector(("redo:")), keyEquivalent: "z")
        redo.keyEquivalentModifierMask = [.command, .shift]
        editMenu.addItem(.separator())
        editMenu.addItem(withTitle: "Cut", action: #selector(NSText.cut(_:)), keyEquivalent: "x")
        editMenu.addItem(withTitle: "Copy", action: #selector(NSText.copy(_:)), keyEquivalent: "c")
        editMenu.addItem(withTitle: "Paste", action: #selector(NSText.paste(_:)), keyEquivalent: "v")
        editMenu.addItem(withTitle: "Select All", action: #selector(NSText.selectAll(_:)), keyEquivalent: "a")
        edit.submenu = editMenu
        main.addItem(edit)

        let agents = NSMenuItem()
        let m = NSMenu(title: "Agents")
        func add(_ title: String, _ key: String, _ mods: NSEvent.ModifierFlags = .command, _ cmd: AppCommand) {
            let i = NSMenuItem(title: title, action: #selector(menuCommand(_:)), keyEquivalent: key)
            i.keyEquivalentModifierMask = mods
            i.target = self
            i.representedObject = MenuCommand(cmd)
            m.addItem(i)
        }
        add("New Agent", "n", .command, .newAgent)
        let ql = NSMenuItem(title: "Quick Launch…", action: #selector(showQuickLaunch), keyEquivalent: "")
        ql.target = self
        m.addItem(ql)
        add("Needs You…", "j", .command, .nextAttention)
        add("Command Palette", "k", .command, .palette)
        add("History", "y", .command, .history) // shared history (hidden while hesperd has no sessions.*)
        HistoryMenu.track(m.items.last!, model: model)
        m.addItem(.separator())
        add("Open / Back to Wall", "\r", .command, .toggleFocus)
        m.addItem(.separator())
        add("Close", "w", .command, .closeAgent)
        add("Send to Background", "w", [.command, .option], .backgroundAgent)
        add("Kill", "w", [.command, .control], .killAgent)
        add("Tidy Up Finished Agents", "w", [.command, .shift], .tidyUp)
        add("Reopen Closed Agent", "t", [.command, .shift], .reopenClosed)
        m.addItem(.separator())
        add("Move to Another Mac…", "m", [.command, .shift], .moveAgent)
        add("Undo Close or Move", "", .command, .undo)
        agents.submenu = m
        main.addItem(agents)

        let view = NSMenuItem()
        let vm = NSMenu(title: "View")
        vm.delegate = self
        for (i, a) in WallArrangement.allCases.enumerated() {
            let item = NSMenuItem(title: a.title, action: #selector(menuCommand(_:)), keyEquivalent: "\(i + 1)")
            item.keyEquivalentModifierMask = [.command, .option]
            item.target = self
            item.representedObject = MenuCommand(.arrangement(i))
            item.tag = 100 + i
            vm.addItem(item)
        }
        let cycle = NSMenuItem(title: "Next Layout", action: #selector(menuCommand(_:)), keyEquivalent: "l")
        cycle.keyEquivalentModifierMask = [.command, .option]
        cycle.target = self
        cycle.representedObject = MenuCommand(.cycleArrangement)
        vm.addItem(cycle)
        vm.addItem(.separator())
        // Previous / Next Agent: ⌥⌘← ⌥⌘→ (plain ← → stay the terminal's in
        // focus; on the wall they move the selection), ⌘[ ⌘] as hidden
        // aliases. The window routes the keys first; these show them.
        func step(_ title: String, _ key: Int, _ alias: String, _ cmd: AppCommand) {
            let i = NSMenuItem(title: title, action: #selector(menuCommand(_:)), keyEquivalent: String(Character(UnicodeScalar(key)!)))
            i.keyEquivalentModifierMask = [.command, .option]
            i.target = self
            i.representedObject = MenuCommand(cmd)
            vm.addItem(i)
            let a = NSMenuItem(title: title, action: #selector(menuCommand(_:)), keyEquivalent: alias)
            a.keyEquivalentModifierMask = .command
            a.target = self
            a.representedObject = MenuCommand(cmd)
            a.isHidden = true
            a.allowsKeyEquivalentWhenHidden = true
            vm.addItem(a)
        }
        step("Previous Agent", NSLeftArrowFunctionKey, "[", .stepPrevious)
        step("Next Agent", NSRightArrowFunctionKey, "]", .stepNext)
        vm.addItem(.separator())
        let dense = NSMenuItem(title: "Dense Cards (60 Characters)", action: #selector(toggleDense), keyEquivalent: "")
        dense.target = self
        dense.tag = 200
        vm.addItem(dense)
        view.submenu = vm
        main.addItem(view)

        let window = NSMenuItem()
        let wm = NSMenu(title: "Window")
        wm.addItem(withTitle: "Minimize", action: #selector(NSWindow.performMiniaturize(_:)), keyEquivalent: "m")
        // ⌘0 is the project sidebar (window layer); Show Wall moved to ⇧⌘0.
        let showWall = wm.addItem(withTitle: "Show Wall", action: #selector(showWallMenu), keyEquivalent: "0")
        showWall.keyEquivalentModifierMask = [.command, .shift]
        window.submenu = wm
        main.addItem(window)
        #if DEBUG
        // Debug builds only: the design system gallery (screenshot review).
        let debug = NSMenuItem()
        let dm = NSMenu(title: "Debug")
        dm.addItem(withTitle: "Design System Gallery", action: #selector(showDesignGallery), keyEquivalent: "")
        debug.submenu = dm
        main.addItem(debug)
        #endif
        NSApp.mainMenu = main
        NSApp.windowsMenu = wm
    }

    @objc private func showWallMenu() { showWindow() }

    #if DEBUG
    @objc private func showDesignGallery() { DesignGallery.show() }
    #endif

    @objc private func toggleDense() { let m = windows.activeWall.model; m.minChars = m.minChars < 80 ? 80 : 60 }

    @objc private func showQuickLaunch() { quickLaunch?.show() }

    private(set) var settingsWindow: NSWindow?
    @objc func showSettings() {
        if settingsWindow == nil {
            let w = NSWindow(contentViewController: NSHostingController(rootView: SettingsView(model: model, onHotKeyChanged: { [weak self] in self?.registerHotKey() }, desks: windows.desks, front: windows.front)))
            w.title = "Settings"
            w.styleMask = [.titled, .closable]
            w.toolbarStyle = .preference
            w.isReleasedWhenClosed = false
            w.center()
            settingsWindow = w
        }
        settingsWindow?.makeKeyAndOrderFront(nil)
    }

    @objc private func menuCommand(_ sender: NSMenuItem) {
        guard let c = sender.representedObject as? MenuCommand else { return }
        // Window layer: menus act on the key window's model; layouts on the
        // frontmost wall.
        switch c.command {
        case .arrangement, .cycleArrangement: windows.activeWall.model.perform(c.command)
        case .stepPrevious where NSApp.keyWindow is AgentWindow, .stepNext where NSApp.keyWindow is AgentWindow:
            (NSApp.keyWindow as? AgentWindow)?.step(c.command == .stepNext ? 1 : -1)
        case .backgroundAgent where NSApp.keyWindow is AgentWindow:
            (NSApp.keyWindow as? AgentWindow)?.closeKeepingAgent() // the window only; the agent stays on its wall
        default: windows.activeModel.perform(c.command)
        }
    }
}

extension AppDelegate: NSMenuDelegate {
    func menuNeedsUpdate(_ menu: NSMenu) {
        for item in menu.items {
            if item.tag >= 100 && item.tag < 100 + WallArrangement.allCases.count {
                item.state = WallArrangement.allCases[item.tag - 100] == windows.activeWall.model.arrangement ? .on : .off
            } else if item.tag == 200 {
                item.state = windows.activeWall.model.minChars < 80 ? .on : .off
            }
        }
    }
}

final class MenuCommand: NSObject {
    let command: AppCommand
    init(_ c: AppCommand) { command = c }
}
