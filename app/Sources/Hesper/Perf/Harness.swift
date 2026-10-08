import AppKit
import Darwin
import HesperCore

/// Shared helpers for the automated modes (--perf-out, --selftest-out).
@MainActor
enum Harness {
    static func wait(_ timeout: Double, _ cond: @MainActor () -> Bool) async -> Bool {
        let end = Date().addingTimeInterval(timeout)
        while Date() < end {
            if cond() { return true }
            try? await Task.sleep(nanoseconds: 20_000_000)
        }
        return cond()
    }

    static func waitAsync(_ timeout: Double, _ cond: @MainActor () async -> Bool) async -> Bool {
        let end = Date().addingTimeInterval(timeout)
        while Date() < end {
            if await cond() { return true }
            try? await Task.sleep(nanoseconds: 50_000_000)
        }
        return await cond()
    }

    static func sleep(_ s: Double) async { try? await Task.sleep(nanoseconds: UInt64(s * 1e9)) }

    static func footprintMB() -> Double {
        var info = task_vm_info_data_t()
        var count = mach_msg_type_number_t(MemoryLayout<task_vm_info_data_t>.size / MemoryLayout<natural_t>.size)
        let kr = withUnsafeMutablePointer(to: &info) {
            $0.withMemoryRebound(to: integer_t.self, capacity: Int(count)) { task_info(mach_task_self_, task_flavor_t(TASK_VM_INFO), $0, &count) }
        }
        return kr == KERN_SUCCESS ? Double(info.phys_footprint) / 1_048_576 : -1
    }

    static func cpuSeconds() -> Double {
        var u = rusage()
        getrusage(RUSAGE_SELF, &u)
        return Double(u.ru_utime.tv_sec) + Double(u.ru_utime.tv_usec) / 1e6 + Double(u.ru_stime.tv_sec) + Double(u.ru_stime.tv_usec) / 1e6
    }

    static func keyEvent(_ chars: String, keyCode: UInt16, flags: NSEvent.ModifierFlags = [], window: NSWindow) -> NSEvent? {
        NSEvent.keyEvent(with: .keyDown, location: .zero, modifierFlags: flags, timestamp: ProcessInfo.processInfo.systemUptime,
                         windowNumber: window.windowNumber, context: nil, characters: chars, charactersIgnoringModifiers: chars,
                         isARepeat: false, keyCode: keyCode)
    }

    /// Sends a key the way AppKit would: ⌘ chords through performKeyEquivalent,
    /// plain keys to the first responder.
    @discardableResult
    static func press(_ chars: String, keyCode: UInt16, flags: NSEvent.ModifierFlags = [], window: NSWindow) -> Bool {
        guard let e = keyEvent(chars, keyCode: keyCode, flags: flags, window: window) else { return false }
        if flags.contains(.command) { return window.performKeyEquivalent(with: e) }
        window.sendEvent(e)
        return true
    }

    static func write(_ obj: some Encodable, to path: String) {
        let enc = JSONEncoder()
        enc.outputFormatting = [.prettyPrinted, .sortedKeys]
        if let d = try? enc.encode(obj) { try? d.write(to: URL(fileURLWithPath: path)) }
    }

    static func percentile(_ xs: [Double], _ p: Double) -> Double {
        guard !xs.isEmpty else { return 0 }
        let s = xs.sorted()
        return s[min(s.count - 1, Int(Double(s.count - 1) * p + 0.5))]
    }
}

/// `make perf`: frame pacing with every tile live, keystroke latency in
/// focus, state→ring latency, memory and CPU. Writes JSON and quits.
@MainActor
final class PerfRunner {
    let model: AppModel
    let root: RootView
    let out: String
    let seconds: Double
    let expected: Int
    let probe = FrameProbe()

    struct Result: Encodable {
        var agents: Int
        var tilesLive: Int
        var frames: FrameProbe.Report?
        var cpuPercent: Double
        var footprintMB: Double
        var footprintIdleMB: Double
        var keystrokeToGridMs: [String: Double]
        var keystrokeToFrameMs: [String: Double]
        var keystrokeSamples: Int
        /// The same in an active wall tile (typing on the wall).
        var activeTileKeystrokeToGridMs: [String: Double]
        var activeTileKeystrokeToFrameMs: [String: Double]
        /// The frame measurement ran with this many active (rw) tiles.
        var activeTilesDuringFrames: Int
        var stateToRingMs: [String: Double]
        var reattachAllMs: Double
        /// Frames the fake TUIs got onto each tile's grid per second (flood
        /// mode prints a frame counter): what the tiles had to show.
        var contentFPS: [String: Double]
        var notes: [String]
    }

    init(model: AppModel, root: RootView, out: String, env: AppEnvironment) {
        self.model = model
        self.root = root
        self.out = out
        seconds = Double(env.values["perf-seconds"] ?? "10") ?? 10
        expected = Int(env.values["perf-agents"] ?? "16") ?? 16
        Task { await run() }
    }

    private func liveTiles() -> [AgentTerminal] {
        root.wall.tiles.values.map(\.terminal).filter { ($0.surface?.readScreen()?.contains { !$0.isWhitespace }) ?? false }
    }

    private func run() async {
        var notes: [String] = []
        let t0 = Date()
        _ = await Harness.wait(30) { self.model.wall.count >= self.expected }
        _ = await Harness.wait(30) { self.liveTiles().count >= self.model.wall.count }
        let reattachMs = Date().timeIntervalSince(t0) * 1000
        let footIdle = Harness.footprintMB()
        await Harness.sleep(3)

        // Shared history (--perf-history): History open over the wall and
        // sessions.changed + sessions.indexing streaming the whole run.
        if model.env.values["perf-history"] != nil {
            _ = await Harness.wait(5) { self.model.history.supported == true }
            HistoryPanelHost.toggle(model)
            _ = await Harness.wait(3) { HistoryPanelHost.isOpen(self.model) }
            if model.env.values["perf-history"] != "open" { _ = try? await model.client.call("fake.sessionsFlood", .object(["seconds": .number(seconds + 40), "rate": 200])) }
            notes.append("History open: \(HistoryPanelHost.isOpen(model)); 200 sessions events/s streaming")
            await Harness.sleep(1)
        }
        // One active (typing) tile during the frame measurement.
        var activeDuring = 0
        if let b = model.wall.dropFirst().first {
            model.activate(b.id)
            if await Harness.wait(5, { self.root.wall.tiles[b.id]?.terminal.surfaceIsInteractive ?? false }) { activeDuring = 1 }
            await Harness.sleep(1)
        }
        probe.views = { [root] in root.wall.tileViews }
        probe.start(on: root.wall)
        await Harness.sleep(1)
        probe.reset()
        let cpu0 = Harness.cpuSeconds(), w0 = Date()
        let content0 = contentFrames()
        await Harness.sleep(seconds)
        let content1 = contentFrames()
        let elapsed = Date().timeIntervalSince(w0)
        let contentFPS = content1.compactMap { id, f in content0[id].map { Double(f - $0) / elapsed } }.sorted()
        let report = probe.report(tiles: root.wall.tiles.count)
        let cpu = (Harness.cpuSeconds() - cpu0) / elapsed * 100
        let foot = Harness.footprintMB()
        probe.stop()

        // State change → ring: a second, test-only RPC flips states.
        var ring: [Double] = []
        if let a = model.wall.first {
            for i in 0..<20 {
                let target: AgentState = i % 2 == 0 ? .approval : .working
                let att: JSONValue = target == .approval ? ["kind": "approval", "title": "Bash", "detail": "perf probe", "options": ["allow", "always", "deny"]] : .null
                let start = CACurrentMediaTime()
                _ = try? await model.client.call("fake.setState", ["id": .string(a.id), "state": .string(target.rawValue), "attention": att])
                var seen = false
                let end = start + 1
                while CACurrentMediaTime() < end {
                    if let tile = root.wall.tiles[a.id], tile.agent.state == target { seen = true; break }
                    try? await Task.sleep(nanoseconds: 500_000)
                }
                if seen { ring.append((CACurrentMediaTime() - start) * 1000) }
            }
            _ = try? await model.client.call("fake.setState", ["id": .string(a.id), "state": "working"])
        }

        // Keystroke latency in focus mode.
        var grid: [Double] = [], frame: [Double] = []
        if let a = model.wall.first {
            model.focus(a.id)
            _ = await Harness.wait(10) { (self.root.focus.terminal?.surface?.readScreen()?.contains("❯") ?? false) }
            await Harness.sleep(1)
            if let s = root.focus.terminal?.surface {
                (grid, frame) = await measureKeystrokes(s, samples: 120)
            } else {
                notes.append("focus surface did not come up")
            }
            model.exitFocus()
        }
        if frame.isEmpty { notes.append("frame presentation after echo not observable from main thread") }

        // Keystroke latency in an active wall tile.
        var tGrid: [Double] = [], tFrame: [Double] = []
        if let a = model.wall.first {
            await Harness.sleep(1)
            model.activate(a.id)
            if await Harness.wait(5, { self.root.wall.tiles[a.id]?.terminal.surfaceIsInteractive ?? false }),
               let s = root.wall.tiles[a.id]?.terminal.surface {
                _ = await Harness.wait(5) { s.readScreen()?.contains("❯") ?? false }
                await Harness.sleep(0.5)
                (tGrid, tFrame) = await measureKeystrokes(s, samples: 120)
            } else {
                notes.append("active tile did not become interactive")
            }
            model.deactivateTile()
        }

        func stats(_ xs: [Double]) -> [String: Double] {
            ["p50": Harness.percentile(xs, 0.5), "p90": Harness.percentile(xs, 0.9), "p99": Harness.percentile(xs, 0.99),
             "max": xs.max() ?? 0, "min": xs.min() ?? 0]
        }
        let r = Result(agents: model.wall.count, tilesLive: liveTiles().count, frames: report, cpuPercent: cpu,
                       footprintMB: foot, footprintIdleMB: footIdle, keystrokeToGridMs: stats(grid), keystrokeToFrameMs: stats(frame),
                       keystrokeSamples: grid.count,
                       activeTileKeystrokeToGridMs: stats(tGrid), activeTileKeystrokeToFrameMs: stats(tFrame), activeTilesDuringFrames: activeDuring,
                       stateToRingMs: stats(ring), reattachAllMs: reattachMs,
                       contentFPS: ["min": contentFPS.first ?? 0, "median": contentFPS.isEmpty ? 0 : contentFPS[contentFPS.count / 2], "max": contentFPS.last ?? 0],
                       notes: notes)
        Harness.write(r, to: out)
        NSApp.terminate(nil)
    }

    /// The flood TUI's frame counter at the start of each tile's screen.
    private func contentFrames() -> [String: Int] {
        var out: [String: Int] = [:]
        for (id, t) in root.wall.tiles {
            if let txt = t.terminal.surface?.readScreen(), let n = Int(txt.prefix(5)) { out[id] = n }
        }
        return out
    }

    /// Types letters into the focused surface as real key events and spins
    /// until the agent's echo is in libghostty's grid (IO thread parsed it),
    /// then until the surface's layer shows a new frame.
    private func measureKeystrokes(_ s: any TerminalSurface, samples: Int) async -> ([Double], [Double]) {
        var grid: [Double] = [], frame: [Double] = []
        var typed = ""
        let letters = Array("asdfghjklqwertyuiopzxcvbnm")
        for i in 0..<samples {
            if typed.count >= 40 {
                s.sendKey(character: "\r")
                typed = ""
                await Harness.sleep(0.05)
            }
            let c = letters[i % letters.count]
            typed.append(c)
            let expect = "❯ " + typed
            let layer = s.view.layer
            let before = layer?.contents.map { ObjectIdentifier($0 as AnyObject) }
            let t0 = CACurrentMediaTime()
            s.sendKey(character: c)
            var tGrid: Double?
            while CACurrentMediaTime() - t0 < 0.25 {
                if let txt = s.readScreen(), txt.contains(expect) { tGrid = CACurrentMediaTime(); break }
            }
            if let tGrid {
                grid.append((tGrid - t0) * 1000)
                // libghostty's renderer presents by dispatching the new
                // IOSurface to the main queue, so yield and watch the layer.
                while CACurrentMediaTime() - t0 < 0.1 {
                    let now = layer?.contents.map { ObjectIdentifier($0 as AnyObject) }
                    if now != before { frame.append((CACurrentMediaTime() - t0) * 1000); break }
                    await Task.yield()
                    try? await Task.sleep(nanoseconds: 200_000)
                }
            }
            await Harness.sleep(0.03 + Double.random(in: 0...0.01))
        }
        return (grid, frame)
    }
}
