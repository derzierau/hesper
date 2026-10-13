import AppKit
import HesperCore

/// The menu bar, built from the command registry (`MenuModel.build`):
/// Hesper, File, Edit, Agents, View, Window, Help. Standard AppKit items
/// are selectors up the responder chain; every other item runs its
/// `CommandID` through `run`. `validateMenuItem` asks the registry's
/// availability with the context the delegate provides, so a command with
/// nothing to act on shows disabled (its reason as the tooltip) instead
/// of doing nothing. The dynamic parts stay with their owners: the layout
/// checkmarks and Dense Cards (`arrangement`, `dense`), Edit ▸ Undo's
/// title (`undoKind`), the Desks submenu (DeskController's `desksMenu`),
/// History hidden while unsupported (HistoryMenu.track on `items[.history]`)
/// and the window list (AppKit's, on the Window menu).
@MainActor
final class MenuBuilder: NSObject, NSMenuItemValidation, NSMenuDelegate {
    let run: @MainActor (CommandID) -> Void
    let context: @MainActor () -> CommandContext
    /// The View menu's checkmarks: the frontmost wall's layout and width.
    var arrangement: @MainActor () -> Int = { 0 }
    var dense: @MainActor () -> Bool = { false }
    /// What ⌘Z would undo on the app's stack; nil for a plain "Undo" (a
    /// text view's own undo).
    var undoKind: @MainActor () -> UndoKind? = { nil }
    /// Window ▸ Desks (DeskController fills it when it opens).
    var desksMenu: NSMenu?

    /// The built items by command, for the owners of the dynamic parts.
    private(set) var items: [CommandID: NSMenuItem] = [:]
    private(set) var menus: [MenuBar: NSMenu] = [:]

    init(run: @escaping @MainActor (CommandID) -> Void, context: @escaping @MainActor () -> CommandContext) {
        self.run = run
        self.context = context
    }

    /// Builds the menu bar and installs it (main, windows, services and
    /// help menus; AppKit adds the window list and Help's search field).
    func install() {
        let main = NSMenu()
        for menu in MenuModel.build().menus {
            let m = NSMenu(title: menu.title)
            m.delegate = self
            for item in menu.items {
                switch item {
                case .separator: m.addItem(.separator())
                case .command(let id, let title, let chord): m.addItem(makeItem(id, title: title, chord: chord))
                }
            }
            let top = NSMenuItem()
            top.submenu = m
            main.addItem(top)
            menus[menu.bar] = m
        }
        if let services = items[.services] {
            let sm = NSMenu(title: "Services")
            services.submenu = sm
            NSApp.servicesMenu = sm
        }
        items[.desks]?.submenu = desksMenu
        NSApp.mainMenu = main
        NSApp.windowsMenu = menus[.window]
        NSApp.helpMenu = menus[.help]
    }

    private func makeItem(_ id: CommandID, title: String, chord: KeyChord?) -> NSMenuItem {
        let i: NSMenuItem
        if let sel = Self.selector(id) {
            i = NSMenuItem(title: title, action: sel, keyEquivalent: "")
        } else {
            i = NSMenuItem(title: title, action: #selector(runItem(_:)), keyEquivalent: "")
            i.target = self
            i.representedObject = CommandRef(id)
        }
        // Only ⌘ chords become key equivalents: a plain key or ⌥-key on an
        // item would take it from the terminal in every window.
        if let chord, chord.command, let (key, mods) = Self.keyEquivalent(chord) {
            i.keyEquivalent = key
            i.keyEquivalentModifierMask = mods
        }
        i.setAccessibilityIdentifier("menu.\(id.key)")
        items[id] = i
        return i
    }

    /// Standard items: AppKit's selectors, validated by the responder chain.
    static func selector(_ id: CommandID) -> Selector? {
        switch id {
        case .about: return #selector(NSApplication.orderFrontStandardAboutPanel(_:))
        case .hide: return #selector(NSApplication.hide(_:))
        case .hideOthers: return #selector(NSApplication.hideOtherApplications(_:))
        case .showAll: return #selector(NSApplication.unhideAllApplications(_:))
        case .quit: return #selector(NSApplication.terminate(_:))
        case .redo: return Selector(("redo:"))
        case .cut: return #selector(NSText.cut(_:))
        case .copy: return #selector(NSText.copy(_:))
        case .paste: return #selector(NSText.paste(_:))
        case .selectAll: return #selector(NSText.selectAll(_:))
        case .delete: return #selector(NSText.delete(_:))
        case .minimize: return #selector(NSWindow.performMiniaturize(_:))
        case .zoom: return #selector(NSWindow.performZoom(_:))
        case .enterFullScreen: return #selector(NSWindow.toggleFullScreen(_:))
        case .bringAllToFront: return #selector(NSApplication.arrangeInFront(_:))
        case .services, .desks: return #selector(NSMenu.submenuAction(_:)) // a submenu; never fires
        default: return nil
        }
    }

    /// A chord as a menu key equivalent.
    static func keyEquivalent(_ c: KeyChord) -> (String, NSEvent.ModifierFlags)? {
        let key: String
        switch c.key {
        case .char(let ch): key = String(ch)
        case .enter: key = "\r"
        case .escape: key = "\u{1b}"
        case .tab: key = "\t"
        case .delete: key = "\u{8}"
        case .left: key = String(Character(UnicodeScalar(NSLeftArrowFunctionKey)!))
        case .right: key = String(Character(UnicodeScalar(NSRightArrowFunctionKey)!))
        case .up: key = String(Character(UnicodeScalar(NSUpArrowFunctionKey)!))
        case .down: key = String(Character(UnicodeScalar(NSDownArrowFunctionKey)!))
        case .home: key = String(Character(UnicodeScalar(NSHomeFunctionKey)!))
        case .end: key = String(Character(UnicodeScalar(NSEndFunctionKey)!))
        case .other: return nil
        }
        var mods: NSEvent.ModifierFlags = []
        if c.command { mods.insert(.command) }
        if c.shift { mods.insert(.shift) }
        if c.option { mods.insert(.option) }
        if c.control { mods.insert(.control) }
        return (key, mods)
    }

    @objc private func runItem(_ sender: NSMenuItem) {
        guard let ref = sender.representedObject as? CommandRef else { return }
        run(ref.id)
    }

    /// Availability from the registry: hidden items stay hidden, disabled
    /// ones carry their reason as the tooltip.
    func validateMenuItem(_ item: NSMenuItem) -> Bool {
        guard let ref = sender(item), let c = CommandRegistry.command(ref.id) else { return true }
        switch c.availability(context()) {
        case .hidden:
            item.isHidden = true
            return false
        case .disabled(let reason):
            item.isHidden = false
            item.toolTip = reason
            return false
        case .enabled:
            item.isHidden = false
            item.toolTip = nil
            return true
        }
    }

    private func sender(_ item: NSMenuItem) -> CommandRef? { item.representedObject as? CommandRef }

    func menuNeedsUpdate(_ menu: NSMenu) {
        if menu === menus[.view] {
            let current = arrangement()
            for (i, _) in WallArrangement.allCases.enumerated() { items[.arrangement(i)]?.state = i == current ? .on : .off }
            items[.toggleDense]?.state = dense() ? .on : .off
        } else if menu === menus[.edit] {
            items[.undo]?.title = MenuModel.undoTitle(undoKind())

        }
    }
}

/// A command id as a menu item's representedObject.
final class CommandRef: NSObject {
    let id: CommandID
    init(_ id: CommandID) { self.id = id }
}
