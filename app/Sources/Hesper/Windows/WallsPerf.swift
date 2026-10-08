import AppKit
import HesperCore

/// `make -C app perf-walls`: the same agents on 1 wall, then on N walls
/// (scope All, side by side on the main display): main-thread frame pacing,
/// frames reaching tiles, CPU, footprint. Writes JSON and quits.
@MainActor
final class WallsPerf {
    let manager: WindowManager
    let out: String
    let seconds: Double
    let expected: Int
    let wallCount: Int

    struct Phase: Encodable {
        var walls: Int
        var tiles: Int
        var tilesRendering: Int
        var frames: FrameProbe.Report?
        var cpuPercent: Double
        var footprintMB: Double
    }

    struct Result: Encodable {
        var agents: Int
        var controlConnections: Int
        var oneWall: Phase
        var manyWalls: Phase
        var allTilesLiveMs: Double
        var notes: [String]
    }

    init(manager: WindowManager, out: String, env: AppEnvironment) {
        self.manager = manager
        self.out = out
        seconds = Double(env.values["perf-seconds"] ?? "10") ?? 10
        expected = Int(env.values["perf-agents"] ?? "16") ?? 16
        wallCount = Int(env.values["perf-walls"] ?? "3") ?? 3
        Task { await run() }
    }

    private var model: AppModel { manager.primary }

    private func surfaces() -> [NSView] {
        manager.walls.flatMap { $0.root.wall.tileViews }
    }

    private func liveCount() -> Int {
        manager.walls.reduce(0) { n, w in
            n + w.root.wall.tiles.values.filter { ($0.terminal.surface?.readScreen()?.contains { !$0.isWhitespace }) ?? false }.count
        }
    }

    private func measure() async -> Phase {
        let probe = FrameProbe()
        probe.views = { [weak self] in self?.surfaces() ?? [] }
        probe.start(on: manager.walls[0].root.wall)
        await Harness.sleep(1)
        probe.reset()
        let cpu0 = Harness.cpuSeconds(), t0 = Date()
        await Harness.sleep(seconds)
        let elapsed = Date().timeIntervalSince(t0)
        let tiles = manager.walls.reduce(0) { $0 + $1.root.wall.tiles.count }
        let report = probe.report(tiles: tiles)
        probe.stop()
        let rendering = manager.walls.reduce(0) { n, w in n + w.root.wall.tiles.values.filter { $0.terminal.visible && $0.terminal.surface != nil }.count }
        return Phase(walls: manager.walls.count, tiles: tiles, tilesRendering: rendering, frames: report,
                     cpuPercent: (Harness.cpuSeconds() - cpu0) / elapsed * 100, footprintMB: Harness.footprintMB())
    }

    private func run() async {
        var notes: [String] = []
        _ = await Harness.wait(30) { self.model.wall.count >= self.expected }
        let vis = NSScreen.main?.visibleFrame ?? NSRect(x: 0, y: 0, width: 1500, height: 900)
        let w = (vis.width / CGFloat(wallCount)).rounded(.down)
        func frame(_ i: Int) -> NSRect { NSRect(x: vis.minX + CGFloat(i) * w, y: vis.minY, width: w, height: vis.height) }
        manager.walls[0].window.setFrame(frame(0), display: true)
        _ = await Harness.wait(30) { self.liveCount() >= self.model.wall.count }
        await Harness.sleep(2)
        let one = await measure()

        let t0 = Date()
        for i in 1..<wallCount { manager.newWall(scope: .all, frame: frame(i)) }
        let all = await Harness.wait(60) { self.liveCount() >= self.model.wall.count * self.wallCount }
        let liveMs = Date().timeIntervalSince(t0) * 1000
        if !all { notes.append("only \(liveCount()) of \(model.wall.count * wallCount) tiles came live") }
        await Harness.sleep(2)
        let many = await measure()
        let conns = Set(manager.walls.map { ObjectIdentifier($0.model.client) }).count
        Harness.write(Result(agents: model.wall.count, controlConnections: conns, oneWall: one, manyWalls: many,
                             allTilesLiveMs: liveMs, notes: notes), to: out)
        exit(0)
    }
}
