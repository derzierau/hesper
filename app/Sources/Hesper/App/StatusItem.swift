import AppKit
import HesperCore
import UserNotifications

/// Menu bar item: the "h." mark (its stop filled while something needs
/// you) and a menu of agents grouped Needs you · Working · Recent, each with
/// its state square and machine, then the app's main ways in. Menus keep
/// the system font.
@MainActor
final class StatusItemController: NSObject, NSMenuDelegate {
    private let item = NSStatusBar.system.statusItem(withLength: NSStatusItem.variableLength)
    private let model: AppModel
    private var imageNeedsYou = false
    var showWindow: () -> Void = {}
    /// Window layer: takes the user to an agent (its window, a wall).
    var openAgent: ((String) -> Void)?

    init(model: AppModel) {
        self.model = model
        super.init()
        let menu = NSMenu()
        menu.delegate = self
        item.menu = menu
        item.button?.setAccessibilityIdentifier("hesper.statusitem")
        observeChanges { [weak self] in self?.refresh() }
    }

    private func refresh() {
        let c = model.counts
        let title = c.menuBarTitle
        // "h.": the full stop filled while an approval or a question waits.
        let needsYou = c.approval + c.question > 0
        if item.button?.image == nil || needsYou != imageNeedsYou {
            imageNeedsYou = needsYou
            item.button?.image = BrandMark.statusImage(needsYou: needsYou)
            item.button?.imagePosition = .imageLeading
        }
        item.button?.title = title.isEmpty ? " \(c.total)" : " \(title)"
        item.button?.toolTip = "Hesper: \(c.total) agents, \(c.approval) approvals, \(c.question) questions, \(c.error) errors"
    }

    func menuNeedsUpdate(_ menu: NSMenu) {
        menu.removeAllItems()
        // Background agents (⌥⌘W) have their own section; opening one brings it back.
        let groups = StatusMenuGroups.groups(model.registry.agents.values.filter { !model.pendingRemoval.contains($0.id) },
                                             isBackground: { [model] in model.isBackground($0) })
        let font = NSFont.menuFont(ofSize: 0)
        // One right-aligned tab stop for every row: the machine trails.
        let trail = Self.trailingTab(groups.flatMap(\.agents).map { ($0.name, $0.machine) }, font: font)
        for g in groups {
            menu.addItem(.sectionHeader(title: g.group.title))
            for a in g.agents { menu.addItem(row(a, group: g.group, tab: trail, font: font)) }
            menu.addItem(.separator())
        }
        if groups.isEmpty {
            let none = NSMenuItem(title: "Nothing running", action: nil, keyEquivalent: "")
            none.isEnabled = false
            menu.addItem(none)
            menu.addItem(.separator())
        }
        add(menu, "Open Hesper", "0", #selector(show))
        add(menu, "New agent…", "n", #selector(newAgent))
        add(menu, "History…", "y", #selector(history))
        menu.addItem(.separator())
        let quick = add(menu, "Quick Launch…  \(model.settings.hotKey.label)", "", #selector(quickLaunch))
        quick.setAccessibilityIdentifier("statusitem.quicklaunch")
        add(menu, "Settings…", ",", #selector(settings))
        menu.addItem(.separator())
        menu.addItem(withTitle: "Quit Hesper", action: #selector(NSApplication.terminate(_:)), keyEquivalent: "q")
    }

    @discardableResult
    private func add(_ menu: NSMenu, _ title: String, _ key: String, _ action: Selector) -> NSMenuItem {
        let i = NSMenuItem(title: title, action: action, keyEquivalent: key)
        i.target = self
        menu.addItem(i)
        return i
    }

    /// An agent row: its state square, its name, the machine trailing (and
    /// for one that needs you, what it asks, as the subtitle).
    private func row(_ a: Agent, group: StatusMenuGroups.Group, tab: CGFloat, font: NSFont) -> NSMenuItem {
        let i = NSMenuItem(title: a.name, action: #selector(openAgent(_:)), keyEquivalent: "")
        let p = NSMutableParagraphStyle()
        p.tabStops = [NSTextTab(textAlignment: .right, location: tab)]
        let s = NSMutableAttributedString(string: a.name, attributes: [.font: font, .paragraphStyle: p])
        s.append(NSAttributedString(string: "\t\(a.machine)", attributes: [.font: font, .paragraphStyle: p, .foregroundColor: NSColor.secondaryLabelColor]))
        i.attributedTitle = s
        if group == .needsYou, let ask = a.attention?.detail ?? a.attention?.title, !ask.isEmpty {
            if #available(macOS 14.4, *) { i.subtitle = ask } else { i.toolTip = ask }
        }
        i.image = StateMark.image(StateMarkKind(a.state))
        i.target = self
        i.representedObject = a.id
        i.setAccessibilityLabel("\(a.name), \(a.machine), \(a.state.label)")
        return i
    }

    /// Where the machine column ends: the widest name, a gap, the widest
    /// machine.
    static func trailingTab(_ rows: [(name: String, machine: String)], font: NSFont) -> CGFloat {
        let width = { (s: String) in (s as NSString).size(withAttributes: [.font: font]).width.rounded(.up) }
        let names = rows.map { width($0.name) }.max() ?? 0
        let machines = rows.map { width($0.machine) }.max() ?? 0
        return names + DS.Spacing.xxl + machines
    }

    @objc private func openAgent(_ sender: NSMenuItem) {
        guard let id = sender.representedObject as? String else { return }
        if let openAgent { openAgent(id); return } // window layer routing
        showWindow()
        model.focus(id)
    }

    @objc private func show() { showWindow() }
    @objc private func newAgent() { showWindow(); model.perform(.newAgent) }
    @objc private func history() { showWindow(); model.perform(.history) }
    @objc private func settings() { NSApp.activate(ignoringOtherApps: true); model.onShowSettings?() }
    @objc private func quickLaunch() { model.onQuickLaunch?() }
}

/// macOS notifications on entering approval / question / error: "<agent>
/// on <machine> needs you" with the exact question, grouped per machine;
/// approvals carry Allow / Deny, answered without opening the app.
@MainActor
final class Notifier: NSObject, UNUserNotificationCenterDelegate {
    private let model: AppModel
    private let enabled: Bool
    var showWindow: () -> Void = {}
    /// Window layer: routing of notification clicks, and the agent the
    /// user is looking at (no notification for it).
    var openAgent: ((String) -> Void)?
    var lookingAt: (() -> String?)?

    init(model: AppModel, enabled: Bool) {
        self.model = model
        // UNUserNotificationCenter needs a bundled app.
        self.enabled = enabled && Bundle.main.bundleIdentifier != nil
        super.init()
        guard self.enabled else { return }
        let center = UNUserNotificationCenter.current()
        center.delegate = self
        center.setNotificationCategories([Self.approvalCategory])
        center.requestAuthorization(options: [.alert, .sound, .badge]) { _, _ in }
    }

    /// Allow / Deny on an approval (Deny is destructive: shown in red).
    static var approvalCategory: UNNotificationCategory {
        UNNotificationCategory(identifier: AgentNotificationText.approvalCategory,
                               actions: [UNNotificationAction(identifier: AgentNotificationText.allowAction, title: "Allow", options: []),
                                         UNNotificationAction(identifier: AgentNotificationText.denyAction, title: "Deny", options: [.destructive])],
                               intentIdentifiers: [], options: [])
    }

    func post(_ transitions: [AgentRegistry.Transition]) {
        guard enabled, model.settings.notificationsEnabled else { return }
        let looking = lookingAt?() ?? (model.mode == .focus ? model.focusedID : nil)
        for t in transitions where shouldNotify(t, focusedID: looking, appActive: NSApp.isActive) {
            let a = t.agent
            let text = AgentNotificationText(a, machineName: model.machineName(a.machine))
            let content = UNMutableNotificationContent()
            content.title = text.title
            content.body = text.body
            content.subtitle = Theme.kindLabel(a.kind)
            content.userInfo = ["agent": a.id]
            content.threadIdentifier = text.thread
            if let c = text.category { content.categoryIdentifier = c }
            content.sound = a.state == .approval && model.settings.notificationSound ? .default : nil
            let req = UNNotificationRequest(identifier: "\(a.id)-\(a.state.rawValue)", content: content, trigger: nil)
            UNUserNotificationCenter.current().add(req)
        }
    }

    /// A background agent finished and was closed: "api finished in the
    /// background" (its session is in History, ⌘⇧T reopens it).
    func postFinished(_ name: String) {
        guard enabled, model.settings.notificationsEnabled else { return }
        let content = UNMutableNotificationContent()
        content.title = CloseText.notification(.finishedInBackground, name: name) ?? name
        content.body = "Closed; its conversation is in History"
        content.threadIdentifier = "background"
        UNUserNotificationCenter.current().add(UNNotificationRequest(identifier: "finished-\(name)-\(UUID().uuidString)", content: content, trigger: nil))
    }

    /// Quick launch started an agent: a notification that jumps to it.
    func postStarted(_ a: Agent) {
        guard enabled else { return }
        let content = UNMutableNotificationContent()
        content.title = "Started \(a.name)"
        content.body = [a.machine == model.localMachine ? nil : "on \(model.machine(a.machine)?.displayName ?? a.machine)",
                        a.project.map { ($0 as NSString).lastPathComponent }].compactMap { $0 }.joined(separator: " · ")
        content.subtitle = "\(Theme.kindLabel(a.kind)) · click to open it on the wall"
        content.userInfo = ["agent": a.id, "go": true]
        content.threadIdentifier = a.machine
        UNUserNotificationCenter.current().add(UNNotificationRequest(identifier: "\(a.id)-started", content: content, trigger: nil))
    }

    nonisolated func userNotificationCenter(_ center: UNUserNotificationCenter, didReceive response: UNNotificationResponse) async {
        let info = response.notification.request.content.userInfo
        let id = info["agent"] as? String
        let go = info["go"] as? Bool ?? false
        let action = response.actionIdentifier
        await MainActor.run {
            // Allow / Deny: the same answer as the tile's, only while the
            // agent still waits for it.
            if action == AgentNotificationText.allowAction || action == AgentNotificationText.denyAction {
                if let id, let a = model.agent(id), a.state == .approval {
                    model.answer(a, action == AgentNotificationText.allowAction ? .allow : .deny)
                }
                return
            }
            if let id, let openAgent { openAgent(id); return } // window layer routing
            showWindow()
            if let id { if go { model.goTo(id) } else { model.focus(id) } }
        }
    }

    nonisolated func userNotificationCenter(_ center: UNUserNotificationCenter, willPresent notification: UNNotification) async -> UNNotificationPresentationOptions {
        [.banner, .sound]
    }
}
