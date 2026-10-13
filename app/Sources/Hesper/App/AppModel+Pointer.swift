import AppKit
import HesperCore

/// The tile menu's items the pointer paths reach by identifier.
enum TileMenuItem {
    /// "Open in New Window" / "Show Window" (the ↗ badge).
    static let openWindow = NSUserInterfaceItemIdentifier("tile.openWindow")
}

// Pointer paths into the window layer (WindowManager builds the tile menu
// and owns the agent windows; the model only has its hooks).
extension AppModel {
    /// The tile's menu (right-click, ⌃-click, the header's ⋯) under the
    /// pointer.
    func showTileMenu(_ id: String) {
        if let m = tileMenu?(id) { popUpMenu(m) }
    }

    /// Opens the agent's own window (the ↗ badge, like ⇧-click): the tile
    /// menu's "Open in New Window" item, run without the menu.
    func openAgentWindow(_ id: String) {
        guard let item = tileMenu?(id)?.items.first(where: { $0.identifier == TileMenuItem.openWindow }),
              let act = item.representedObject as? MenuAction else { return }
        act.run()
    }
}
