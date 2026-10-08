import AppKit
import HesperCore
import UniformTypeIdentifiers

// Drag & drop onto terminals (docs: "As built — drop to attach"): wall
// tiles in every state (selected, active, read-only, shelf), the focus
// view and agent windows. The view that owns the agent (TileView,
// FocusView) owns a TerminalDrop; the libghostty surface inside it
// forwards its drag events there too, so the drop works whichever view
// AppKit picks.

/// A view that takes drops for one agent.
@MainActor
protocol TerminalDropTarget: AnyObject {
    var terminalDrop: TerminalDrop { get }
}

/// One view's drop handling: the highlight, reading the pasteboard, and
/// handing the payloads to the model.
@MainActor
final class TerminalDrop {
    private weak var view: NSView?
    private let agentID: () -> String?
    private let model: () -> AppModel?
    let overlay = DropOverlay()
    /// Set while an upload shows its progress (the overlay stays).
    private var busy = false

    init(view: NSView, agentID: @escaping () -> String?, model: @escaping () -> AppModel?) {
        self.view = view
        self.agentID = agentID
        self.model = model
        overlay.isHidden = true
    }

    /// Every type a terminal takes.
    static var types: [NSPasteboard.PasteboardType] {
        var t = DropClassifier.registeredTypes.map { NSPasteboard.PasteboardType($0) }
        t += NSFilePromiseReceiver.readableDraggedTypes.map { NSPasteboard.PasteboardType($0) }
        return Array(Set(t))
    }

    private func showOverlay(_ state: DropOverlay.State) {
        guard let view else { return }
        if overlay.superview !== view { view.addSubview(overlay, positioned: .above, relativeTo: nil) }
        overlay.frame = view.bounds
        overlay.autoresizingMask = [.width, .height]
        overlay.state = state
        overlay.isHidden = false
    }

    private func hideOverlay() {
        guard !busy else { return }
        overlay.isHidden = true
    }

    /// What dropping here would do (the overlay's label), or a refusal.
    private func verdict(_ pb: NSPasteboard) -> DropOverlay.State {
        guard let model = model(), let id = agentID(), let a = model.agent(id) else { return .reject("No agent here") }
        if !a.isRunning { return .reject("\(a.name) has exited") }
        if DropClassifier.classify(pb.types?.map(\.rawValue) ?? []) == .unsupported { return .reject("Only files, images and text can be dropped") }
        if a.machine != model.localMachine {
            return .hover("Drop to attach to \(a.name)", detail: "uploads to \(model.machine(a.machine)?.displayName ?? a.machine)")
        }
        return .hover("Drop to attach to \(a.name)", detail: nil)
    }

    func entered(_ info: any NSDraggingInfo) -> NSDragOperation {
        guard !busy else { return [] }
        let v = verdict(info.draggingPasteboard)
        showOverlay(v)
        if case .reject = v { return [] }
        return .copy
    }

    func updated(_ info: any NSDraggingInfo) -> NSDragOperation {
        guard !busy else { return [] }
        if case .reject = overlay.state { return [] }
        return .copy
    }

    func exited() { hideOverlay() }

    func perform(_ info: any NSDraggingInfo) -> Bool {
        guard !busy, let model = model(), let id = agentID(), let a = model.agent(id), a.isRunning else { hideOverlay(); return false }
        let pb = info.draggingPasteboard
        guard DropClassifier.classify(pb.types?.map(\.rawValue) ?? []) != .unsupported else { hideOverlay(); return false }
        hideOverlay()
        // The user goes on typing here: the window and app come forward.
        view?.window?.makeKeyAndOrderFront(nil)
        NSApp.activate()
        let dir = model.attachmentsDir(for: AttachmentNaming.folder(id))
        DropReader.read(pb, saveDir: dir) { [weak self] payloads, error in
            guard let self, let model = self.model() else { return }
            if let error { model.showToast(error, error: true) }
            guard !payloads.isEmpty else { return }
            Task { @MainActor in
                await model.deliverDrop(id, payloads: payloads) { [weak self] label in
                    guard let self else { return }
                    if let label {
                        self.busy = true
                        self.showOverlay(.progress(label))
                    } else {
                        self.busy = false
                        self.hideOverlay()
                    }
                }
            }
        }
        return true
    }
}

/// The drop highlight: an accent ring over the terminal and a label; a
/// red one for what can't be dropped; a small pill in the footer while an
/// upload runs.
final class DropOverlay: NSView {
    enum State: Equatable {
        case hover(String, detail: String?)
        case reject(String)
        case progress(String)
    }

    var state: State = .hover("", detail: nil) { didSet { update() } }
    private let pill = NSView()
    private let label = NSTextField(labelWithString: "")
    private let detail = NSTextField(labelWithString: "")

    override var isFlipped: Bool { true }

    override init(frame: NSRect) {
        super.init(frame: frame)
        wantsLayer = true
        layer?.cornerRadius = Metrics.radius
        pill.wantsLayer = true
        DS.Radius.apply(DS.Radius.tile, to: pill.layer)
        pill.layer?.borderWidth = 1
        label.font = .ds(.body, .semibold)
        label.textColor = Theme.ns(.text)
        label.alignment = .center
        label.lineBreakMode = .byTruncatingTail
        detail.font = .geist(ofSize: 11)
        detail.textColor = Theme.ns(.text)
        detail.alignment = .center
        detail.lineBreakMode = .byTruncatingTail
        addSubview(pill)
        pill.addSubview(label)
        pill.addSubview(detail)
        setAccessibilityElement(true)
        setAccessibilityIdentifier("drop.overlay")
        update()
    }

    @available(*, unavailable)
    required init?(coder: NSCoder) { fatalError() }

    override func hitTest(_ point: NSPoint) -> NSView? { nil }

    /// The label shown (tests).
    var text: String {
        switch state {
        case .hover(let t, let d): return d.map { "\(t) · \($0)" } ?? t
        case .reject(let t), .progress(let t): return t
        }
    }

    override func viewDidChangeEffectiveAppearance() {
        super.viewDidChangeEffectiveAppearance()
        update()
    }

    private func update() { themed { updateInAppearance() } }

    private func updateInAppearance() {
        pill.layer?.backgroundColor = Theme.ns(.surface, alpha: 0.96).cgColor
        let accent: NSColor
        switch state {
        case .hover(let t, let d):
            accent = Theme.ns(.working)
            label.stringValue = t
            detail.stringValue = d ?? ""
            layer?.borderWidth = 3
            layer?.backgroundColor = accent.withAlphaComponent(0.10).cgColor
        case .reject(let t):
            accent = Theme.ns(.error)
            label.stringValue = t
            detail.stringValue = ""
            layer?.borderWidth = 3
            layer?.backgroundColor = accent.withAlphaComponent(0.08).cgColor
        case .progress(let t):
            accent = Theme.ns(.working)
            label.stringValue = t
            detail.stringValue = ""
            layer?.borderWidth = 0
            layer?.backgroundColor = NSColor.clear.cgColor
        }
        layer?.borderColor = accent.cgColor
        pill.layer?.borderColor = accent.withAlphaComponent(0.7).cgColor
        setAccessibilityLabel(text)
        needsLayout = true
    }

    override func layout() {
        super.layout()
        let hasDetail = !detail.stringValue.isEmpty
        let maxW = max(60, bounds.width - 24)
        let w = min(maxW, max(label.intrinsicContentSize.width, detail.intrinsicContentSize.width) + 28)
        let h: CGFloat = hasDetail ? 46 : 30
        let y: CGFloat
        if case .progress = state { y = bounds.height - h - 6 } else { y = (bounds.height - h) / 2 }
        pill.frame = NSRect(x: (bounds.width - w) / 2, y: max(0, y), width: w, height: h)
        label.frame = NSRect(x: 10, y: hasDetail ? 6 : 6, width: w - 20, height: 18)
        detail.frame = NSRect(x: 10, y: 25, width: w - 20, height: 15)
        detail.isHidden = !hasDetail
    }
}

/// Reads a drop's pasteboard into payloads: files (several, folders),
/// file promises (received into the attachments folder), image bytes,
/// web URLs and text.
@MainActor
enum DropReader {
    static func read(_ pb: NSPasteboard, saveDir: URL, completion: @escaping @MainActor ([DropPayload], String?) -> Void) {
        let kind = DropClassifier.classify(pb.types?.map(\.rawValue) ?? [])
        switch kind {
        case .files:
            let urls = pb.readObjects(forClasses: [NSURL.self], options: [.urlReadingFileURLsOnly: true]) as? [URL] ?? []
            completion(urls.map { u in
                var dir: ObjCBool = false
                FileManager.default.fileExists(atPath: u.path, isDirectory: &dir)
                return .file(path: u.path, isDirectory: dir.boolValue)
            }, urls.isEmpty ? "The dropped files could not be read" : nil)
        case .filePromise:
            let receivers = pb.readObjects(forClasses: [NSFilePromiseReceiver.self], options: nil) as? [NSFilePromiseReceiver] ?? []
            let fallback = imagePayload(pb)
            guard !receivers.isEmpty else { completion(fallback.map { [$0] } ?? [], fallback == nil ? "The dropped file could not be read" : nil); return }
            receive(receivers, into: saveDir) { paths, error in
                if paths.isEmpty, let fallback { completion([fallback], nil); return }
                completion(paths.map { .file(path: $0, isDirectory: false) }, error)
            }
        case .imageData:
            if let p = imagePayload(pb) { completion([p], nil) } else { completion([], "The dropped image could not be read") }
        case .url:
            let url = (pb.readObjects(forClasses: [NSURL.self], options: nil) as? [URL])?.first?.absoluteString ?? pb.string(forType: .URL) ?? pb.string(forType: .string)
            completion(url.map { [.text($0)] } ?? [], url == nil ? "The dropped link could not be read" : nil)
        case .text:
            let t = pb.string(forType: .string) ?? ""
            completion(t.isEmpty ? [] : [.text(t)], nil)
        case .unsupported:
            completion([], "Only files, images and text can be dropped")
        }
    }

    /// Image bytes (PNG kept, anything else converted later), named after
    /// the image's web URL when there is one.
    static func imagePayload(_ pb: NSPasteboard) -> DropPayload? {
        var name = "image"
        if let u = (pb.readObjects(forClasses: [NSURL.self], options: nil) as? [URL])?.first, !u.isFileURL {
            let last = (u.lastPathComponent as NSString).deletingPathExtension
            if !last.isEmpty && last != "/" { name = last }
        }
        for t in [NSPasteboard.PasteboardType.png, NSPasteboard.PasteboardType("public.jpeg"), NSPasteboard.PasteboardType("public.heic"), .tiff] {
            if let d = pb.data(forType: t), !d.isEmpty { return .image(data: d, name: name) }
        }
        if let img = NSImage(pasteboard: pb), let d = img.tiffRepresentation { return .image(data: d, name: name) }
        return nil
    }

    /// File promises (screenshot thumbnails, Photos, Mail): received into
    /// a scratch folder, then moved to `<timestamp>-<name>.<ext>`.
    private static func receive(_ receivers: [NSFilePromiseReceiver], into dir: URL, completion: @escaping @MainActor ([String], String?) -> Void) {
        let incoming = dir.appendingPathComponent(".incoming-\(UUID().uuidString)")
        do { try FileManager.default.createDirectory(at: incoming, withIntermediateDirectories: true) } catch {
            completion([], "Could not save the dropped file: \(error.localizedDescription)")
            return
        }
        let queue = OperationQueue()
        let group = DispatchGroup()
        let lock = NSLock()
        nonisolated(unsafe) var received: [URL] = []
        nonisolated(unsafe) var failure: String?
        for r in receivers {
            let n = max(1, r.fileNames.count)
            for _ in 0..<n { group.enter() }
            r.receivePromisedFiles(atDestination: incoming, options: [:], operationQueue: queue) { url, error in
                lock.lock()
                if let error { failure = error.localizedDescription } else { received.append(url) }
                lock.unlock()
                group.leave()
            }
        }
        group.notify(queue: .main) {
            MainActor.assumeIsolated {
                var paths: [String] = []
                for u in received {
                    let ext = u.pathExtension.isEmpty ? "dat" : u.pathExtension
                    let name = AttachmentStore.uniqueName(AttachmentNaming.fileName(u.lastPathComponent, ext: ext, at: Date()), in: dir)
                    let dest = dir.appendingPathComponent(name)
                    if (try? FileManager.default.moveItem(at: u, to: dest)) != nil { paths.append(dest.path) }
                }
                try? FileManager.default.removeItem(at: incoming)
                completion(paths, failure.map { "Could not receive the dropped file: \($0)" })
            }
        }
    }
}

/// Saving payloads that have no file (or a format the agents don't read).
enum AttachmentStore {
    static func uniqueName(_ name: String, in dir: URL) -> String {
        AttachmentNaming.unique(name) { FileManager.default.fileExists(atPath: dir.appendingPathComponent($0).path) }
    }

    /// PNG bytes for any image data (PNG kept as it is).
    static func png(_ data: Data) -> Data? {
        if data.starts(with: [0x89, 0x50, 0x4E, 0x47]) { return data }
        guard let img = NSImage(data: data), let tiff = img.tiffRepresentation, let rep = NSBitmapImageRep(data: tiff) else { return nil }
        return rep.representation(using: .png, properties: [:])
    }

    /// Writes image bytes as `<timestamp>-<name>.png` in `dir`.
    static func saveImage(_ data: Data, name: String, in dir: URL) throws -> String {
        guard let png = png(data) else { throw CocoaError(.fileReadCorruptFile) }
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        let file = dir.appendingPathComponent(uniqueName(AttachmentNaming.fileName(name, ext: "png", at: Date()), in: dir))
        try png.write(to: file, options: .atomic)
        return file.path
    }

    /// An image file the agents can't read (HEIC, TIFF, …) as a PNG copy.
    static func convertToPNG(_ path: String, in dir: URL) -> String? {
        guard let d = FileManager.default.contents(atPath: path) else { return nil }
        return try? saveImage(d, name: (path as NSString).lastPathComponent, in: dir)
    }
}
