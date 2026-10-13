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
    var menus: MenuBuilder!
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
            s.openReview = { [weak self] id in self?.windows.frontModel.openReview(select: id) }
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
        // Review: an agent on any Mac became ready to review.
        n.openReview = { [weak self] id in self?.windows.frontModel.openReview(select: id) }
        model.reviewHub.onArrivals = { [weak n] items in n?.postReadyToReview(items) }
        // Closing agents: a background agent finished (and was closed).
        model.onFinishedInBackground = { [weak n, weak self] name in
            n?.postFinished(name)
            if NSApp.isActive, let text = CloseText.notification(.finishedInBackground, name: name) {
                self?.windows.frontModel.showToast(text)
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

    /// The Dock menu: New Agent, Needs You…, History, Review, from the
    /// registry. Each brings the wall in front first, as the status item does.
    func applicationDockMenu(_ sender: NSApplication) -> NSMenu? {
        let menu = NSMenu()
        for case .command(let id, let title, _) in MenuModel.dockItems(context: commandContext()) {
            let i = NSMenuItem(title: title, action: #selector(runDockItem(_:)), keyEquivalent: "")
            i.target = self
            i.representedObject = CommandRef(id)
            i.setAccessibilityIdentifier("dock.\(id.key)")
            menu.addItem(i)
        }
        return menu
    }

    @objc private func runDockItem(_ sender: NSMenuItem) {
        guard let ref = sender.representedObject as? CommandRef else { return }
        showWindow()
        switch ref.id {
        case .review: windows.frontModel.openReview(select: nil) // opens, never toggles closed
        default: if let cmd = KeyRouter.command(for: ref.id) { windows.frontModel.perform(cmd) } else { run(ref.id) }
        }
    }


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

    // MARK: Menu (the command registry; the window routes the keys first)

    private func buildMenu() {
        menus = MenuBuilder(run: { [weak self] id in self?.run(id) }, context: { [weak self] in self?.commandContext() ?? .none })
        menus.arrangement = { [weak self] in
            guard let self else { return 0 }
            return WallArrangement.allCases.firstIndex(of: self.windows.activeWall.model.arrangement) ?? 0
        }
        menus.dense = { [weak self] in (self?.windows.activeWall.model.minChars ?? 80) < 80 }
        menus.undoKind = { [weak self] in NSApp.keyWindow?.firstResponder is NSText ? nil : self?.windows.activeModel?.undoToast?.action.kind }

        menus.desksMenu = windows.desks.desksMenu
        menus.install()
        if let history = menus.items[.history] { HistoryMenu.track(history, model: model) } // hidden while hesperd has no sessions.*
        #if DEBUG
        // Debug builds only: the design system gallery (screenshot review).
        let debug = NSMenuItem()
        let dm = NSMenu(title: "Debug")
        dm.addItem(withTitle: "Design System Gallery", action: #selector(showDesignGallery), keyEquivalent: "")
        debug.submenu = dm
        NSApp.mainMenu?.addItem(debug)
        #endif
    }

    /// What the menus validate against: the key window's model.
    func commandContext() -> CommandContext {
        let kind: KeyWindowKind = NSApp.keyWindow is AgentWindow ? .agentWindow : windows.activeModel == nil ? .other : .wall
        let fullScreen = NSApp.keyWindow?.styleMask.contains(.fullScreen) ?? false
        guard let m = windows.activeModel else {
            return CommandContext(keyWindow: .other, reviewCount: model.reviewHub.items.count,
                                  canUndo: NSApp.keyWindow?.firstResponder is NSText, historySupported: HistoryHub.shared(for: model).supported == true,
                                  inFullScreen: fullScreen)
        }
        return m.commandContext(keyWindow: kind, textHasFocus: NSApp.keyWindow?.firstResponder is NSText,
                                isHomeWall: windows.walls.first?.model === m, inFullScreen: fullScreen)
    }


    /// Runs a menu (or palette) command. AppCommands go to the key
    /// window's model as the keys would; the rest are the app's and the
    /// window layer's.
    func run(_ id: CommandID) {
        switch id {
        case .settings: showSettings()
        case .quickLaunch: quickLaunch?.toggle() // like the status item and the hotkey
        case .showWall: showWindow()
        case .toggleDense: let m = windows.activeWall.model; m.minChars = m.minChars < 80 ? 80 : 60
        case .showShortcuts: showShortcuts()
        case .help: open("https://github.com/derzierau/hesper#readme")
        case .hesperctlReference: open("https://github.com/derzierau/hesper/tree/main/relay/docs")
        case .reportProblem: open("https://github.com/derzierau/hesper/issues")
        case .find: // Edit ▸ Find: the History search while the sheet is open
            if let m = windows.activeModel { HistoryPanelHost.panel(for: m)?.perform(HistoryKeyAction.focusSearch(nil)) }
        case .undo: // a text field's own undo first, else the newest undo toast
            if NSApp.keyWindow?.firstResponder is NSText { NSApp.sendAction(Selector(("undo:")), to: nil, from: nil) } else { windows.activeModel?.perform(.undo) }
        case .renameAgent:
            guard let m = windows.activeModel, let a = m.current else { return }
            m.renameText[a.id] = a.name
            m.openPopover(.rename(a.id))
        case .resumeAgent:
            guard let m = windows.activeModel, let a = m.current else { return }
            m.resume(a)
        case .routines: break // the routines branch fills this in
        default:
            if let cmd = KeyRouter.command(for: id) { menuCommand(cmd) } else { _ = windows.run(id) }
        }
    }

    private func open(_ url: String) { if let u = URL(string: url) { NSWorkspace.shared.open(u) } }

    /// Help ▸ Keyboard Shortcuts (⌘/): the sheet rendered from the registry comes in phase 4.
    func showShortcuts() {}

    #if DEBUG
    @objc private func showDesignGallery() { DesignGallery.show() }
    #endif

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

    private func menuCommand(_ command: AppCommand) {
        // Window layer: menus act on the key window's model; layouts on the
        // frontmost wall.
        switch command {
        case .arrangement, .cycleArrangement: windows.activeWall.model.perform(command)
        case .stepPrevious where NSApp.keyWindow is AgentWindow, .stepNext where NSApp.keyWindow is AgentWindow:
            (NSApp.keyWindow as? AgentWindow)?.step(command == .stepNext ? 1 : -1)
        case .closeAgent where commandContext().closeTarget == .window:
            if let w = NSApp.keyWindow as? AgentWindow { w.closeKeepingAgent() } // the window only; the agent stays on its wall
            else { NSApp.keyWindow?.performClose(nil) } // Settings, First Run, the gallery

        default: windows.activeModel?.perform(command)
        }
    }
}
