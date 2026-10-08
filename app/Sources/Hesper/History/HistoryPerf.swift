import AppKit
import HesperCore
import QuartzCore

/// Main-thread work per run-loop pass (from waking up to going to sleep
/// again): what a frame budget has to fit into.
@MainActor
final class MainThreadWatch {
    private var observer: CFRunLoopObserver?
    private var began: CFTimeInterval = 0
    private(set) var samples: [Double] = []

    func start() {
        samples.removeAll(keepingCapacity: true)
        let o = CFRunLoopObserverCreateWithHandler(nil, CFRunLoopActivity.afterWaiting.rawValue | CFRunLoopActivity.beforeWaiting.rawValue, true, 0) { [weak self] _, act in
            MainActor.assumeIsolated {
                guard let self else { return }
                let now = CACurrentMediaTime()
                if act == .afterWaiting { self.began = now } else if self.began > 0 { self.samples.append((now - self.began) * 1000); self.began = 0 }
            }
        }
        observer = o
        CFRunLoopAddObserver(CFRunLoopGetMain(), o, .commonModes)
    }

    func stop() {
        if let o = observer { CFRunLoopRemoveObserver(CFRunLoopGetMain(), o, .commonModes) }
        observer = nil
    }

    func reset() { samples.removeAll(keepingCapacity: true) }

    var stats: [String: Double] {
        ["p50": Harness.percentile(samples, 0.5), "p99": Harness.percentile(samples, 0.99), "max": samples.max() ?? 0,
         "over4ms": Double(samples.filter { $0 > 4 }.count), "passes": Double(samples.count)]
    }
}

/// `make -C app perf-history`: History's budgets on the fake daemon with
/// 3000 sessions and 16 agents on the wall (JSON to --history-perf-out).
@MainActor
final class HistoryPerf {
    let manager: WindowManager
    let out: String
    private var model: AppModel { manager.primary }
    private var window: NSWindow { manager.walls[0].window }
    private var panel: HistoryPanel? { HistoryPanelHost.panel(for: model) }
    private let watch = MainThreadWatch()
    private let probe = FrameProbe()

    struct Result: Encodable {
        var sessions: Int
        var agents: Int
        var openColdMs: Double
        var openWarmMs: [String: Double]
        var openWarmFreshMs: [String: Double]
        var typeKeyToRowsMs: [String: Double]
        var daemonAnswerToRowsMs: [String: Double]
        var applyPageMs: [String: Double]
        var mainThreadTypingMs: [String: Double]
        /// One keystroke into the search field (the text system's own work).
        var keyEventMs: [String: Double]
        var scroll: FrameProbe.Report?
        var scrollRowsReached: Int
        var mainThreadScrollMs: [String: Double]
        var cardSelectMainThreadMs: [String: Double]
        var cardDetailMs: [String: Double]
        var floodEvents: Int
        var floodUIUpdatesPerSecondMax: Int
        var mainThreadFloodMs: [String: Double]
        var paletteHistoryMs: [String: Double]
        var mainThreadPaletteMs: [String: Double]
        /// The same measurements without History (what the wall itself costs).
        var mainThreadWallOnlyMs: [String: Double]
        var mainThreadPaletteWithoutHistoryMs: [String: Double]
        var notes: [String]
    }

    init(manager: WindowManager, out: String, env: AppEnvironment) {
        self.manager = manager
        self.out = out
        Task { await run() }
    }

    private func stats(_ xs: [Double]) -> [String: Double] {
        ["p50": Harness.percentile(xs, 0.5), "p90": Harness.percentile(xs, 0.9), "max": xs.max() ?? 0, "n": Double(xs.count)]
    }

    private func key(_ chars: String, _ code: UInt16, _ flags: NSEvent.ModifierFlags = []) {
        Harness.press(chars, keyCode: code, flags: flags, window: window)
    }

    private func run() async {
        var notes: [String] = []
        let agents = Int(model.env.values["perf-agents"] ?? "16") ?? 16
        _ = await Harness.wait(30) { self.model.isConnected && self.model.wall.count >= agents && self.model.history.supported == true }
        // The real hesperd: until its first index is done (synthetic fixtures).
        let indexStart = Date()
        _ = await Harness.wait(240) {
            self.model.history.refreshStatsThrottled()
            return self.model.history.stats.total > 0 && self.model.history.indexing == nil
        }
        notes.append("index ready after \(Int(Date().timeIntervalSince(indexStart))) s, \(model.history.stats.total) sessions")
        for _ in 0..<3 {
            NSApp.activate(ignoringOtherApps: true)
            window.makeKeyAndOrderFront(nil)
            if await Harness.wait(1, { self.window.isKeyWindow }) { break }
        }
        await Harness.sleep(3)
        let total = model.history.stats.total
        // Baselines: the wall alone, and ⌘K without its History section.
        watch.start()
        await Harness.sleep(4)
        let wallOnly = watch.stats
        HistoryPaletteSource.enabled = false
        watch.reset()
        await typePalette()
        let palBase = watch.stats
        HistoryPaletteSource.enabled = true

        // Open: cold (first), then warm 10×.
        key("y", 16, .command)
        _ = await Harness.wait(5) { (self.panel?.openToFirstRowsMs ?? nil) != nil }
        let cold = panel?.openToFirstRowsMs ?? -1
        _ = await Harness.wait(3) { (self.panel?.rows.count ?? 0) > 0 }
        await Harness.sleep(0.5)
        var warm: [Double] = [], fresh: [Double] = []
        for _ in 0..<10 {
            key("\u{1b}", 53)
            await Harness.sleep(0.3)
            guard let p = panel else { break }
            var shown = 0
            var t: CFTimeInterval = 0
            p.onRowsShown = { shown += 1; if shown == 2 { t = CACurrentMediaTime() } }
            key("y", 16, .command)
            let t0 = p.openStarted
            if let ms = p.openToFirstRowsMs { warm.append(ms) }
            _ = await Harness.wait(2) { shown >= 2 }
            if t > 0 { fresh.append((t - t0) * 1000) }
            p.onRowsShown = nil
            await Harness.sleep(0.2)
        }

        // Typing: 10 queries, a key every 110 ms.
        guard let p = panel else { return finish(notes: ["no panel"]) }
        HistoryPanel.layouts = 0
        var keyToRows: [Double] = [], answerToRows: [Double] = [], apply: [Double] = [], keyEvent: [Double] = []
        watch.start()
        for q in ["debounce", "flaky badge", "relay", "push provider", "ticker", "widgets", "auth", "scrollback", "edition picker", "fleet"] {
            window.makeFirstResponder(p.searchField)
            p.searchField.stringValue = ""
            p.controlTextDidChange(Notification(name: NSControl.textDidChangeNotification))
            await Harness.sleep(0.2)
            var rows = 0
            p.onRowsShown = { rows += 1 }
            for c in q {
                let t0 = CACurrentMediaTime()
                key(String(c), 0)
                keyEvent.append((CACurrentMediaTime() - t0) * 1000)
                await Harness.sleep(0.11)
            }
            _ = await Harness.wait(2) { rows > 0 && p.query.text == q && p.lastTypeToRowsMs != nil }
            await Harness.sleep(0.15)
            if let v = p.lastTypeToRowsMs { keyToRows.append(v) }
            if let v = p.lastAnswerToRowsMs { answerToRows.append(v) }
            if let v = p.lastApplyMs { apply.append(v) }
            p.onRowsShown = nil
        }
        let typingMain = watch.stats
        notes.append("panel layouts while typing: \(HistoryPanel.layouts)")
        p.searchField.stringValue = ""
        p.controlTextDidChange(Notification(name: NSControl.textDidChangeNotification))
        if let all = p.projectRow("all") { all.press() }
        await Harness.sleep(0.8)

        // Card: ↓ through 40 rows; sessions.show fills in.
        watch.reset()
        var detail: [Double] = []
        window.makeFirstResponder(p.table)
        for _ in 0..<40 {
            key("", 125)
            let t0 = CACurrentMediaTime()
            _ = await Harness.wait(1) { p.card.text?.changed != nil }
            detail.append((CACurrentMediaTime() - t0) * 1000)
        }
        let cardMain = watch.stats
        notes.append(String(format: "slowest selection change %.2f ms, card update + layout %.2f ms", HistoryPanel.maxSelectMs, SessionCardPanel.maxUpdateMs))

        // Scroll: 2 rows per frame for 8 s (display link), pages ahead.
        p.perform(.first)
        await Harness.sleep(0.3)
        probe.start(on: p)
        watch.reset()
        let clip = p.table.enclosingScrollView!.contentView
        var y: CGFloat = 0
        let link = p.displayLink(target: ScrollDriver { [weak p] in
            guard let p, let sv = p.table.enclosingScrollView else { return }
            y += HistoryPanel.rowHeight * 2
            let maxY = max(0, p.table.frame.height - sv.contentSize.height)
            clip.scroll(to: NSPoint(x: 0, y: min(y, maxY)))
            sv.reflectScrolledClipView(clip)
        }, selector: #selector(ScrollDriver.tick))
        let fps = Float(window.screen?.maximumFramesPerSecond ?? 60)
        link.preferredFrameRateRange = CAFrameRateRange(minimum: fps, maximum: fps, preferred: fps)
        probe.reset()
        link.add(to: .main, forMode: .common)
        await Harness.sleep(8)
        link.invalidate()
        let scroll = probe.report(tiles: 0)
        probe.stop()
        let scrollMain = watch.stats
        let reached = p.rows.count
        if reached < 2000 { notes.append("scroll reached \(reached) rows") }

        // Event flood: 400 sessions.changed + indexing per second for 5 s.
        watch.reset()
        let f0 = model.history.flushes.count
        if (try? await model.client.call("fake.sessionsFlood", .object(["seconds": 5, "rate": 400]))) == nil { notes.append("no event flood (not the fake daemon)") }
        await Harness.sleep(5.5)
        let fl = Array(model.history.flushes.suffix(from: f0))
        var perSecond = 0
        for (i, t) in fl.enumerated() { perSecond = max(perSecond, fl[i...].prefix { $0 < t + 1 }.count) }
        let floodMain = watch.stats

        // ⌘K: the History section while typing.
        key("\u{1b}", 53)
        await Harness.sleep(0.3)
        watch.reset()
        let pal = await typePalette()
        let palMain = watch.stats
        watch.stop()

        let r = Result(sessions: total, agents: model.wall.count, openColdMs: cold, openWarmMs: stats(warm), openWarmFreshMs: stats(fresh),
                       typeKeyToRowsMs: stats(keyToRows), daemonAnswerToRowsMs: stats(answerToRows), applyPageMs: stats(apply),
                       mainThreadTypingMs: typingMain, keyEventMs: stats(keyEvent), scroll: scroll, scrollRowsReached: reached, mainThreadScrollMs: scrollMain,
                       cardSelectMainThreadMs: cardMain, cardDetailMs: stats(detail), floodEvents: 2000,
                       floodUIUpdatesPerSecondMax: perSecond, mainThreadFloodMs: floodMain, paletteHistoryMs: stats(pal),
                       mainThreadPaletteMs: palMain, mainThreadWallOnlyMs: wallOnly, mainThreadPaletteWithoutHistoryMs: palBase, notes: notes)
        Harness.write(r, to: out)
        NSApp.terminate(nil)
    }

    /// Five queries typed into ⌘K, a key every 90 ms; History's latency.
    @discardableResult
    private func typePalette() async -> [Double] {
        var pal: [Double] = []
        let src = HistoryPaletteSource.shared(for: model)
        for q in ["debounce", "relay", "flaky", "widgets", "ticker"] {
            key("k", 40, .command)
            _ = await Harness.wait(1) { self.model.showPalette }
            await Harness.sleep(0.3)
            for i in 1...q.count {
                model.paletteList.query = String(q.prefix(i))
                await Harness.sleep(0.09)
            }
            if HistoryPaletteSource.enabled {
                _ = await Harness.wait(2) { src.query == q }
                if let v = src.lastLatencyMs { pal.append(v) }
            }
            await Harness.sleep(0.2)
            model.showPalette = false
            await Harness.sleep(0.2)
        }
        return pal
    }

    private func finish(notes: [String]) {
        Harness.write(["notes": notes], to: out)
        NSApp.terminate(nil)
    }
}

/// A display link's target (scrolls the list once per frame).
@MainActor
final class ScrollDriver: NSObject {
    let step: @MainActor () -> Void
    init(_ step: @escaping @MainActor () -> Void) { self.step = step }
    @objc func tick() { step() }
}
