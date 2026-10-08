import AppKit
import HesperCore

/// `--layout-out PATH` (debug/test hook, `make layout`): once the wall has
/// settled, dumps every tile's card frame, its terminal area and the engine's
/// real grid in pixels, so the layout can be checked numerically (slack =
/// terminal area − what the engine's grid covers). With `--window-size WxH`
/// the window gets that content size first. Quits after `--layout-hold`
/// seconds (default 3) so a screenshot can be taken meanwhile.
@MainActor
final class LayoutProbe {
    struct Tile: Encodable {
        var id: String
        var state: String
        var quiet: Bool             // a shelf card: no terminal
        var agentCols: Int
        var agentRows: Int
        var card: [Double]          // x, y, w, h in points (wall coordinates)
        var terminalArea: [Double]  // x, y, w, h in points (wall coordinates)
        var terminalAreaPx: [Int]   // w, h in backing pixels
        var fontSize: Double
        var engineCols: Int
        var engineRows: Int
        var cellPx: [Int]           // w, h
        var gridPx: [Int]           // engineCols × cellW, engineRows × cellH
        var contentRows: Int        // rows the tile shows of the agent
        var slackPx: [Int]          // terminal area − grid (w, h)
        var contentSlackPx: Int     // terminal area height − rows of content shown
        var layoutRows: Int         // rows the layout gave the card (0: no layout)
        var cardSlackPx: Int        // card − header − insets − band − terminal area (height)
    }
    struct Dump: Encodable {
        var windowNumber: Int
        var sheetWindowNumber: Int
        var window: [Double]
        var wall: [Double]
        var scale: Double
        var arrangement: String
        var fades: [String]
        var contentSize: [Double]
        var tiles: [Tile]
        var maxVerticalSlackPx: Int
        var maxContentSlackPx: Int
    }

    let model: AppModel
    let root: RootView
    let out: String
    let hold: Double
    /// `--layout-show focus|palette|new|confirm`: what to open before the
    /// dump (and the screenshot).
    let show: String?

    init(model: AppModel, root: RootView, out: String, env: AppEnvironment) {
        self.model = model
        self.root = root
        self.out = out
        hold = Double(env.values["layout-hold"] ?? "3") ?? 3
        show = env.values["layout-show"]
        Task { await run() }
    }

    private func run() async {
        let expectAgents = show != "empty"
        // The real hesperd starts empty: start some agents (fake TUIs).
        if let project = ProcessInfo.processInfo.environment["HESPER_SELFTEST_PROJECT"], expectAgents {
            _ = await Harness.wait(15) { self.model.isConnected }
            let n = Int(ProcessInfo.processInfo.environment["AGENTS"] ?? "4") ?? 4
            let names = ["push-provider-fcm", "relay latency", "edition picker", "ios widgets", "draft PR copy", "auth renewal"]
            for i in model.wall.count..<n {
                _ = await model.spawn(SpawnRequest(project: project, task: names[i % names.count] + "\nfake work"))
            }
        }
        _ = await Harness.wait(expectAgents ? 20 : 3) { self.model.isConnected && (!self.model.wall.isEmpty || !expectAgents) }
        _ = await Harness.wait(20) {
            self.root.wall.tiles.values.allSatisfy { $0.onShelf || (($0.terminal.surface?.readScreen()?.contains { !$0.isWhitespace }) ?? false) }
        }
        // Refits are debounced and re-attach; let the wall settle.
        await Harness.sleep(2.5)
        switch show {
        case "focus":
            if let a = model.wall.dropFirst().first ?? model.wall.first { model.focus(a.id) }
            await Harness.sleep(1.5)
        case "new":
            model.perform(.newAgent)
            await Harness.sleep(1)
        case "confirm", "undo":
            if let a = model.wall.first(where: { $0.state == .working }) { model.select(a.id) }
            model.perform(.closeAgent)
            await Harness.sleep(1.2)
        case "draft":
            selectWorking()
            model.perform(.newAgent)
            await Harness.sleep(1)
        case "draft-typing":
            selectWorking()
            model.perform(.newAgent)
            await Harness.sleep(0.8)
            if let id = model.editingDraftID, let tv = root.wall.draftTiles[id]?.editor.textView {
                root.window?.makeFirstResponder(tv)
                tv.insertText("The badge test on the rail is flaky under load. Find the race in #hesper and fix it, run the suite 20× to prove it /codex @",
                              replacementRange: tv.selectedRange())
            }
            await Harness.sleep(0.8)
        case "palette":
            model.paletteList.query = "push" // the query survives closing: set it first
            model.perform(.palette)
            await Harness.sleep(0.8)
        case "move":
            selectWorking()
            model.perform(.moveAgent)
            await Harness.sleep(0.8)
        case "attention", "machines", "layout":
            model.openPopover(show == "attention" ? .attention : show == "machines" ? .machines : .layout)
            await Harness.sleep(0.8)
        case "deny":
            if let a = model.wall.first(where: { $0.state == .approval }) {
                model.select(a.id)
                model.denyText[a.id] = "Push to a branch first, not main"
                model.perform(.denyWithMessage)
            }
            await Harness.sleep(0.8)
        case "quick":
            (NSApp.delegate as? AppDelegate)?.quickLaunch?.show()
            await Harness.sleep(0.4)
            if let q = (NSApp.delegate as? AppDelegate)?.quickLaunch, let tv = q.panel.firstResponder as? ComposerTextView {
                tv.insertText("Bump the relay's Go version and fix what breaks #acme-apps @", replacementRange: tv.selectedRange())
            }
            await Harness.sleep(0.8)
            extraWindow = (NSApp.delegate as? AppDelegate)?.quickLaunch?.panel.windowNumber ?? 0
        case "scroll":
            // Let the agents log for a while, then scroll one tile back.
            await Harness.sleep(8)
            if let a = model.wall.first, let t = root.wall.tiles[a.id] {
                t.terminal.scroll(lines: 10)
                await Harness.sleep(2.5)
            }
        case "selected":
            // Selected, not active: the highlight ring and the footer's key
            // hints; no cursor, read-only.
            selectWorking()
            root.window?.makeFirstResponder(root.wall)
            await Harness.sleep(0.8)
        case "selected-approval":
            if let a = model.wall.first(where: { $0.state == .approval }) { model.select(a.id) } else { selectWorking() }
            root.window?.makeFirstResponder(root.wall)
            await Harness.sleep(0.8)
        case "active":
            if let a = model.wall.first(where: { $0.isRunning }), let t = root.wall.tiles[a.id] {
                model.activate(a.id)
                _ = await Harness.wait(5) { t.terminal.surfaceIsInteractive }
                for ch in "typing on the wall" { t.terminal.surface?.sendKey(character: ch) }
                await Harness.sleep(1)
            }
        case "after-focus":
            // Focus one agent (its PTY takes the focus view's size), then
            // back: every tile must have the same font and fill its card.
            if let a = model.wall.first(where: { $0.state == .working }) {
                model.focus(a.id)
                await Harness.sleep(2.5)
                model.exitFocus()
                await Harness.sleep(3)
            }
        case "settings":
            (NSApp.delegate as? AppDelegate)?.showSettings()
            await Harness.sleep(1)
            extraWindow = (NSApp.delegate as? AppDelegate)?.settingsWindow?.windowNumber ?? 0
        default:
            break
        }
        Harness.write(dump(), to: out)
        if show == "flap" { await flap() }
        await Harness.sleep(hold)
        // A sheet would hold terminate up.
        exit(0)
    }

    private var extraWindow = 0

    /// `--layout-show flap`: rapid state/activity changes on one agent
    /// (every 100 ms for 3 s); records the tile's terminal frame and footer
    /// each step. The terminal must never move.
    private func flap() async {
        guard let a = model.wall.first(where: { $0.state == .working }), let t = root.wall.tiles[a.id] else { return }
        let steps: [(String, JSONValue)] = [
            ("working", ["activity": "Bash: ./gradlew :push:test"]), ("idle", ["summary": "Waiting for input"]),
            ("working", ["activity": "Read(src/push/PushMessaging.kt)"]), ("working", ["activity": "Edit(src/push/PushMessaging.kt)"]),
            ("idle", [:]), ("working", ["activity": "Bash: git status"]),
            ("approval", ["attention": ["kind": "approval", "title": "Bash", "detail": "git push origin main", "options": ["allow", "always", "deny"]]]),
            ("working", ["activity": "Bash: git push"]),
        ]
        var frames = Set<String>(), shown: [String] = []
        let start = Date()
        var i = 0
        while Date().timeIntervalSince(start) < 3 {
            let (state, extra) = steps[i % steps.count]
            var p: [String: JSONValue] = ["id": .string(a.id), "state": .string(state), "attention": .null]
            if case .object(let o) = extra { for (k, v) in o { p[k] = v } }
            if p["activity"] == nil { p["activity"] = "" }
            _ = try? await model.client.call("fake.setState", .object(p))
            await Harness.sleep(0.1)
            frames.insert("\(t.terminal.host.frame)")
            shown.append(t.shown.state.rawValue)
            i += 1
        }
        let report: [String: JSONValue] = ["agent": .string(a.id), "steps": .number(Double(i)),
                                           "distinctTerminalFrames": .number(Double(frames.count)), "terminalFrames": .array(frames.map { .string($0) }),
                                           "footerShown": .array(shown.map { .string($0) })]
        Harness.write(report, to: out + ".flap.json")
    }

    private func selectWorking() {
        if let a = model.wall.first(where: { $0.state == .working }) { model.select(a.id) }
    }

    func dump() -> Dump {
        let wall = root.wall
        let scale = Double(root.window?.backingScaleFactor ?? 2)
        var tiles: [Tile] = []
        for a in model.wall {
            guard let t = wall.tiles[a.id] else { continue }
            if t.onShelf {
                tiles.append(Tile(id: a.id, state: a.state.rawValue, quiet: true, agentCols: a.size?.cols ?? 0, agentRows: a.size?.rows ?? 0,
                                  card: [t.frame.minX, t.frame.minY, t.frame.width, t.frame.height].map(Self.r),
                                  terminalArea: [], terminalAreaPx: [], fontSize: 0, engineCols: 0, engineRows: 0, cellPx: [], gridPx: [],
                                  contentRows: 0, slackPx: [0, 0], contentSlackPx: 0, layoutRows: 0, cardSlackPx: 0))
                continue
            }
            let host = t.terminal.host
            let area = host.convert(host.bounds, to: wall)
            let px = host.convertToBacking(host.bounds.size)
            let m = t.terminal.surface?.metrics
            let cols = m?.cols ?? 0, rows = m?.rows ?? 0
            let cw = m?.cellWidthPx ?? 0, ch = m?.cellHeightPx ?? 0
            let content = min(rows, a.size?.rows ?? rows)
            let spec = Metrics.wall
            let band = t.bandHeight(width: t.bounds.width)
            let cardSlack = (t.frame.height - spec.header - 2 * spec.inset - band - host.bounds.height) * scale
            tiles.append(Tile(id: a.id, state: a.state.rawValue, quiet: false, agentCols: a.size?.cols ?? 0, agentRows: a.size?.rows ?? 0,
                              card: [t.frame.minX, t.frame.minY, t.frame.width, t.frame.height].map(Self.r),
                              terminalArea: [area.minX, area.minY, area.width, area.height].map(Self.r),
                              terminalAreaPx: [Int(px.width.rounded()), Int(px.height.rounded())],
                              fontSize: t.terminal.surface?.fontSize ?? 0,
                              engineCols: cols, engineRows: rows, cellPx: [cw, ch], gridPx: [cols * cw, rows * ch],
                              contentRows: content,
                              slackPx: [Int(px.width.rounded()) - cols * cw, Int(px.height.rounded()) - rows * ch],
                              contentSlackPx: Int(px.height.rounded()) - content * ch,
                              layoutRows: t.placed?.rows ?? 0, cardSlackPx: Int(cardSlack.rounded())))
        }
        let w = root.window
        return Dump(windowNumber: w?.windowNumber ?? 0, sheetWindowNumber: extraWindow != 0 ? extraWindow : (w?.attachedSheet?.windowNumber ?? 0),
                    window: [w?.frame.width ?? 0, w?.frame.height ?? 0].map(Self.r),
                    wall: [wall.bounds.width, wall.bounds.height].map(Self.r), scale: scale,
                    arrangement: wall.layoutResult.arrangement.rawValue,
                    fades: [wall.leftFade, wall.rightFade].map { "\($0.isHidden ? "hidden" : "shown") \($0.frame) super=\($0.superview != nil)" } + ["clip \(wall.scrollView.contentView.bounds) doc \(wall.frame)"],
                    contentSize: [wall.layoutResult.contentWidth, wall.layoutResult.contentHeight].map(Self.r), tiles: tiles,
                    maxVerticalSlackPx: tiles.map { $0.slackPx[1] }.max() ?? 0,
                    maxContentSlackPx: tiles.map(\.contentSlackPx).max() ?? 0)
    }

    static func r(_ d: Double) -> Double { (d * 10).rounded() / 10 }
}
