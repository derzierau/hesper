import Observation

/// The wall in front: the key wall window, or the last one that was (while
/// Settings or a panel has the keyboard). Settings › Wall edits this wall's
/// own layout, the same model the top bar, the menus and ⌥⌘1… act on, so
/// every way of changing a wall's arrangement writes the one value its
/// switcher shows. Observable: Settings follows when another wall comes
/// to the front.
@MainActor @Observable
final class FrontWall {
    var model: AppModel
    init(_ model: AppModel) { self.model = model }
}
