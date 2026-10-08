import AppKit
import HesperCore

/// Dev tool: `Hesper --render-bring <dir>` draws a draft whose folder is
/// only on this Mac offscreen (no window, no daemon) into PNGs, Dusk and
/// Daylight: the note's "Bring it to mini" (default) with its
/// alternatives, an older hesperd's note (today's), a bring that found
/// the folder there ("mini already has it · Use it"), one too large, the
/// tile's progress line while it brings, and narrow tiles; then exits.
@MainActor
enum BringRender {
    static func run(_ dir: String) {
        try? FileManager.default.createDirectory(atPath: dir, withIntermediateDirectories: true)
        let folder = localFolder()
        for (scheme, appearance) in [("dusk", NSAppearance.Name.darkAqua), ("daylight", .aqua)] {
            render(folder, appearance: appearance, width: 760, to: dir, name: "bring-note-\(scheme)")
            render(folder, appearance: appearance, width: 760, to: dir, name: "bring-fallback-\(scheme)") { m, _, _ in
                m.bringBook.support.byMachine["mini"] = false
            }
            render(folder, appearance: appearance, width: 760, to: dir, name: "bring-exists-\(scheme)") { _, c, key in
                c.bringFailures[key] = .exists
            }
            render(folder, appearance: appearance, width: 760, to: dir, name: "bring-too-large-\(scheme)") { _, c, key in
                c.bringFailures[key] = .tooLarge
            }
            render(folder, appearance: appearance, width: 760, to: dir, name: "bring-progress-\(scheme)") { m, c, _ in
                progress(m, c, step: "transfer", percent: 42)
            }
        }
        render(folder, appearance: .darkAqua, width: 480, to: dir, name: "bring-note-dusk-narrow")
        render(folder, appearance: .darkAqua, width: 480, to: dir, name: "bring-progress-dusk-narrow") { m, c, _ in
            progress(m, c, step: "unpack", percent: nil)
        }
        render(folder, appearance: .darkAqua, width: 760, to: dir, name: "bring-progress-dusk-checkpoint") { m, c, _ in
            progress(m, c, step: "checkpoint", percent: nil)
        }
    }

    static func progress(_ m: AppModel, _ c: ComposerModel, step: String, percent: Int?) {
        m.bringStarted(draft: c.id, to: "mini", changes: .with)
        var p: [String: JSONValue] = ["draft": .string(c.id), "to": "mini", "step": .string(step)]
        if let percent { p["percent"] = .number(Double(percent)) }
        if let bp = BringProgress(params: .object(p)) { m.bringProgressed(bp) }
        c.busy = true
    }

    /// A folder this Mac has (the chip shows it abbreviated).
    static func localFolder() -> String {
        let home = NSHomeDirectory()
        for p in [home + "/projects/hesper", home + "/projects/ghosty-config"] where AppModel.dirExists(p) { return p }
        let tmp = (NSTemporaryDirectory() as NSString).appendingPathComponent("hesper-render/hesper")
        try? FileManager.default.createDirectory(atPath: tmp, withIntermediateDirectories: true)
        return tmp
    }

    static func render(_ folder: String, appearance: NSAppearance.Name, width: CGFloat, to dir: String, name: String,
                       _ setup: (AppModel, ComposerModel, String) -> Void = { _, _, _ in }) {
        let env = AppEnvironment.resolve(arguments: ["Hesper", "--ephemeral", "--socket", "/dev/null/hesper-render.sock"], environment: [:])
        let model = AppModel(env: env)
        let hello: JSONValue = .object([
            "daemon": "hesperd", "version": "0", "machine": "laptop",
            "machines": .array([
                .object(["short": "laptop", "name": "ABC123456", "online": true, "route": "local"]),
                .object(["short": "mini", "name": "mini", "online": true, "rttMs": 41, "route": "relay"]),
            ]),
        ])
        if let h = try? hello.decode(HelloInfo.self) { model.ingest(.connected(h)) }
        let name0 = (folder as NSString).lastPathComponent
        model.ingest(.projectsListed([
            Project(id: "gh", name: name0, kind: .repo, paths: ["laptop": folder, "mini": "/Users/me/src/" + name0]),
        ], []))
        let d = Draft(id: "d-render-bring", text: "Add retries to the feed importer @mini", machine: "mini", project: folder, machineExplicit: true)
        model.drafts.edit(d)
        let c = model.composer(for: d.id)
        let key = c.targetKey ?? ""
        c.targetFolders[key] = false
        setup(model, c, key)
        let tile = DraftTileView(draft: d, model: model)
        tile.appearance = NSAppearance(named: appearance)
        tile.frame = NSRect(x: 0, y: 0, width: width, height: 250)
        tile.isSelected = true
        for _ in 0..<4 {
            tile.apply(d)
            tile.needsLayout = true
            tile.layoutSubtreeIfNeeded()
            RunLoop.current.run(until: Date().addingTimeInterval(0.05))
        }
        ScratchRender.write(tile, to: dir, name: name)
    }
}
