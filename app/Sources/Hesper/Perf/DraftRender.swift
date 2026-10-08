import AppKit
import HesperCore

/// Dev tool: `Hesper --render-draft <dir>` draws a draft tile offscreen (no
/// window, no daemon) into PNGs, Dusk and Daylight: a project window's
/// draft with the project locked, the same draft unlocked (its folder's
/// project, no lock), and a narrow tile; then exits.
@MainActor
enum DraftRender {
    static func run(_ dir: String) {
        try? FileManager.default.createDirectory(atPath: dir, withIntermediateDirectories: true)
        let env = AppEnvironment.resolve(arguments: ["Hesper", "--ephemeral", "--socket", "/dev/null/hesper-render.sock"], environment: [:])
        let model = AppModel(env: env)
        let home = NSHomeDirectory()
        model.ingest(.projectsListed([
            Project(id: "gh", name: "hesper", kind: .repo, paths: ["L": home + "/projects/hesper"], groups: ["g-tools"]),
            Project(id: "news", name: "news-api", kind: .repo, paths: ["L": home + "/projects/news-api"]),
        ], [ProjectGroup(id: "g-tools", name: "tools", projectIds: ["gh"])]))
        var locked = Draft(id: "d-render-locked", text: "Fix the toolbar overlap in project windows", project: home + "/projects/hesper")
        locked.band = "gh"
        locked.wall = "wall-gh"
        locked.projectLocked = true
        var free = locked
        free.id = "d-render-free"
        free.projectLocked = false
        for (scheme, appearance) in [("dusk", NSAppearance.Name.darkAqua), ("daylight", .aqua)] {
            render(locked, model: model, width: 760, appearance: appearance, to: dir, name: "draft-locked-\(scheme)")
            render(free, model: model, width: 760, appearance: appearance, to: dir, name: "draft-unlocked-\(scheme)")
        }
        render(locked, model: model, width: 480, appearance: .darkAqua, to: dir, name: "draft-locked-dusk-narrow")
    }

    static func render(_ d: Draft, model: AppModel, width: CGFloat, appearance: NSAppearance.Name, to dir: String, name: String) {
        model.drafts.edit(d)
        let tile = DraftTileView(draft: d, model: model)
        tile.appearance = NSAppearance(named: appearance)
        tile.frame = NSRect(x: 0, y: 0, width: width, height: 230)
        tile.isSelected = true
        // Two passes: the hosting views size themselves, then the tile.
        for _ in 0..<4 {
            tile.apply(d)
            tile.needsLayout = true
            tile.layoutSubtreeIfNeeded()
            RunLoop.current.run(until: Date().addingTimeInterval(0.05))
        }
        guard let rep = tile.bitmapImageRepForCachingDisplay(in: tile.bounds) else { return }
        tile.cacheDisplay(in: tile.bounds, to: rep)
        let path = (dir as NSString).appendingPathComponent(name + ".png")
        if let png = rep.representation(using: .png, properties: [:]) {
            try? png.write(to: URL(fileURLWithPath: path))
            print("wrote \(path)")
        }
    }
}
