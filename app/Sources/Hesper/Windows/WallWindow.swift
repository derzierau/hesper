import AppKit
import HesperCore
import SwiftUI

/// A wall in the window layer: the main wall (MainWindowController) or an
/// extra one (WallWindowController). Each has its own AppModel (layout,
/// card width, selection, overlays) and a scope.
@MainActor
final class WallEntry {
    let id: String
    let isMain: Bool
    let model: AppModel
    let root: RootView
    let window: NSWindow
    /// The wall's scope lives in its model (the view: scope + grouping +
    /// layout).
    var scope: WallScope {
        get { model.scope }
        set { model.scope = newValue }
    }
    /// Cards that fit without scrolling at the minimum card size.
    var capacity = 1
    var controller: WallWindowController?

    init(id: String, isMain: Bool, model: AppModel, root: RootView, window: NSWindow, scope: WallScope) {
        self.id = id; self.isMain = isMain; self.model = model; self.root = root; self.window = window
        model.scope = scope
    }
}

/// An extra wall window (⌥⌘N): the same RootView, toolbar and key routing
/// as the main wall, on its own AppModel. Closing it closes it for real.
@MainActor
final class WallWindowController: NSWindowController, NSWindowDelegate {
    let model: AppModel
    let root: RootView
    let toolbar: MainToolbar
    weak var manager: WindowManager?

    init(model: AppModel, userFontSize: Double, title: String, manager: WindowManager) {
        self.model = model
        self.manager = manager
        root = RootView(model: model)
        root.userFontSize = userFontSize
        toolbar = MainToolbar(model: model)
        let w = MainWindow(contentRect: NSRect(x: 0, y: 0, width: 1200, height: 800),
                           styleMask: [.titled, .closable, .miniaturizable, .resizable, .fullSizeContentView], backing: .buffered, defer: false)
        w.model = model
        w.title = title
        w.titlebarAppearsTransparent = true
        w.titleVisibility = .hidden
        w.backgroundColor = Theme.windowBG
        w.contentView = root
        w.isReleasedWhenClosed = false
        w.tabbingMode = .disallowed
        w.collectionBehavior.insert(.fullScreenPrimary)
        super.init(window: w)
        w.delegate = self
        toolbar.install(in: w)
        root.toolbarAnchor = { [weak self] k in self?.toolbar.anchor(for: k) }
    }

    @available(*, unavailable)
    required init?(coder: NSCoder) { fatalError() }

    func windowDidResize(_ notification: Notification) {
        model.closePopover()
    }

    func windowWillClose(_ notification: Notification) {
        model.closePopover()
        root.focus.close()
        for t in root.wall.tiles.values { t.terminal.close() }
        manager?.wallClosed(self)
    }
}
