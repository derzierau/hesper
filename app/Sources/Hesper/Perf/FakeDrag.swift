import AppKit

/// A drag for the UI tests: a pasteboard and a location, delivered to the
/// view AppKit would pick (hit test at the point, then up to the first
/// view registered for one of the pasteboard's types).
@MainActor
final class FakeDrag: NSObject, @preconcurrency NSDraggingInfo {
    let pasteboard: NSPasteboard
    let window: NSWindow
    let location: NSPoint

    init(pasteboard: NSPasteboard, window: NSWindow, location: NSPoint) {
        self.pasteboard = pasteboard
        self.window = window
        self.location = location
    }

    /// A private pasteboard holding `objects`.
    static func pasteboard(_ objects: [any NSPasteboardWriting]) -> NSPasteboard {
        let pb = NSPasteboard(name: NSPasteboard.Name("hesper.selftest.drop.\(UUID().uuidString)"))
        pb.clearContents()
        pb.writeObjects(objects)
        return pb
    }

    /// The view under the point and its ancestors (debugging a test).
    func chain() -> String {
        guard let content = window.contentView, let frame = content.superview else { return "no content" }
        var v = content.hitTest(frame.convert(location, from: nil))
        var out: [String] = []
        while let s = v { out.append("\(type(of: s))\(s.registeredDraggedTypes.isEmpty ? "" : "*")"); v = s.superview }
        return out.joined(separator: " < ") + " @\(location)"
    }

    /// The destination AppKit would choose.
    func destination() -> NSView? {
        guard let content = window.contentView, let frame = content.superview else { return nil }
        var v = content.hitTest(frame.convert(location, from: nil))
        let types = Set(pasteboard.types ?? [])
        while let s = v, Set(s.registeredDraggedTypes).isDisjoint(with: types) { v = s.superview }
        return v
    }

    var draggingDestinationWindow: NSWindow? { window }
    var draggingSourceOperationMask: NSDragOperation { .copy }
    var draggingLocation: NSPoint { location }
    var draggedImageLocation: NSPoint { location }
    var draggedImage: NSImage? { nil }
    var draggingPasteboard: NSPasteboard { pasteboard }
    var draggingSource: Any? { nil }
    var draggingSequenceNumber: Int { 1 }
    func slideDraggedImage(to screenPoint: NSPoint) {}
    var draggingFormation: NSDraggingFormation = .default
    var animatesToDestination = false
    var numberOfValidItemsForDrop = 1
    func enumerateDraggingItems(options enumOpts: NSDraggingItemEnumerationOptions = [], for view: NSView?, classes classArray: [AnyClass],
                                searchOptions: [NSPasteboard.ReadingOptionKey: Any] = [:],
                                using block: (NSDraggingItem, Int, UnsafeMutablePointer<ObjCBool>) -> Void) {}
    var springLoadingHighlight: NSSpringLoadingHighlight { .none }
    func resetSpringLoading() {}
}
