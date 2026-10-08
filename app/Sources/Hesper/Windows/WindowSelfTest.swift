import AppKit
import HesperCore

/// Windows UI smoke test (`make -C app test-ui-windows`, Tools/run-windows.sh):
/// drives the real app against the fake daemon. Phase 1 opens agent windows
/// (⇧-click, ⌘-click, ⌥⇧-click as a tab), checks the plain-click / ⌘↩ /
/// double-click paths stay in the wall, size ownership without ping-pong,
/// ⌥⌘N with Overflow, attention staying on the first wall, ⌘J routing,
/// the scope popover, screenshots, and saves the window state. Phase 2
/// (a relaunch on the same daemon and state file) checks what came back.
@MainActor
final class WindowSelfTest {
    let manager: WindowManager
    let out: String
    let phase: Int
    let shots: String?
    var checks: [SelfTest.Check] = []
    private var model: AppModel { manager.primary }
    private var main: WallEntry { manager.walls[0] }

    struct Result: Encodable { var phase: Int; var passed: Int; var failed: Int; var checks: [SelfTest.Check]; var shots: [String] }
    private var shotFiles: [String] = []

    init(manager: WindowManager, out: String, env: AppEnvironment) {
        self.manager = manager
        self.out = out
        phase = Int(env.values["windows-phase"] ?? "1") ?? 1
        shots = env.values["windows-shots"]
        Task { phase == 1 ? await runPhase1() : await runPhase2() }
    }

    private func check(_ name: String, _ ok: Bool, _ detail: String = "") {
        checks.append(SelfTest.Check(name: name, ok: ok, detail: detail))
        print(ok ? "ok  " : "FAIL", name, detail)
    }

    private func setState(_ id: String, _ state: String, attention: JSONValue = .null) async {
        _ = try? await model.client.call("fake.setState", .object(["id": .string(id), "state": .string(state), "attention": attention]))
    }

    private func click(_ id: String, _ flags: NSEvent.ModifierFlags = [], count: Int = 1, in wall: WallEntry? = nil) {
        let w = wall ?? main
        guard let tile = w.root.wall.tiles[id],
              let e = NSEvent.mouseEvent(with: .leftMouseDown, location: NSPoint(x: tile.frame.midX, y: 10), modifierFlags: flags,
                                         timestamp: ProcessInfo.processInfo.systemUptime, windowNumber: w.window.windowNumber,
                                         context: nil, eventNumber: 0, clickCount: count, pressure: 1) else { return }
        tile.mouseDown(with: e)
    }

    /// Makes `w` key, re-activating the app first (on a shared desktop
    /// another app may have taken activation, and then nothing becomes key).
    private func makeKey(_ w: NSWindow?) async {
        guard let w else { return }
        for _ in 0..<3 {
            NSApp.activate(ignoringOtherApps: true)
            w.makeKeyAndOrderFront(nil)
            if await Harness.wait(1, { w.isKeyWindow }) { return }
        }
    }

    private var agentWindowCount: Int { manager.agentWindows.count }
    private var appWindowCount: Int { NSApp.windows.filter { $0.isVisible && ($0 is AgentWindow || $0 is MainWindow) }.count }

    private func screen(_ c: AgentWindowController?) -> String { c?.terminal?.surface?.readScreen() ?? "" }

    // MARK: Phase 1

    private func runPhase1() async {
        _ = await Harness.wait(15) { self.model.isConnected && self.model.wall.count >= 6 }
        let live = await Harness.wait(20) { self.model.wall.allSatisfy { (self.main.root.wall.tiles[$0.id]?.terminal.surface?.readScreen() ?? "").contains(SelfTest.tileMark) } }
        check("main wall shows the agents live", live, "\(model.wall.count) agents")
        let ids = model.registry.wallOrder.map(\.id)
        guard ids.count >= 6 else { return finish() }
        let a = ids[0], b = ids[1], c = ids[2]
        await makeKey(main.window)
        let before = appWindowCount

        // Plain click, ⌘↩ and double-click stay in this window.
        click(a)
        check("plain click selects, opens no window", model.selectedID == a && agentWindowCount == 0 && appWindowCount == before)
        Harness.press("\r", keyCode: 36, flags: .command, window: main.window)
        check("⌘↩ opens the focus view in the same window", model.mode == .focus && model.focusedID == a && agentWindowCount == 0 && appWindowCount == before)
        Harness.press("\r", keyCode: 36, flags: .command, window: main.window)
        click(b, count: 2)
        check("double-click opens the focus view in the same window", model.mode == .focus && model.focusedID == b && agentWindowCount == 0)
        Harness.press("\r", keyCode: 36, flags: .command, window: main.window)
        check("back on the wall", model.mode == .wall)

        // ⇧-click: own window; again: the same window comes forward.
        click(a, .shift)
        let ca = manager.agentWindow(for: a)
        check("⇧-click opens the agent in its own window", ca != nil && agentWindowCount == 1)
        let attached = await Harness.wait(10) { self.screen(ca).contains("fake agent") }
        check("agent window attaches the terminal", attached, String(screen(ca).suffix(120)))
        if let s = ca?.terminal?.surface {
            for ch in "winhi" { s.sendKey(character: ch) }
            let echoed = await Harness.wait(3) { s.readScreen()?.contains("winhi") ?? false }
            check("typing in the agent window reaches the agent", echoed)
            let owns = await Harness.wait(3) {
                guard let m = s.metrics, let g = self.model.agent(a)?.size else { return false }
                return m.cols == g.cols && m.rows == g.rows
            }
            check("the key agent window owns the PTY size", owns, "\(String(describing: s.metrics.map { "\($0.cols)x\($0.rows)" })) vs \(String(describing: model.agent(a)?.size))")
            // A file dropped on the agent window arrives as a paste.
            if let c = ca, let w = c.window {
                let file = URL(fileURLWithPath: NSTemporaryDirectory()).appendingPathComponent("hesper-window-drop-\(UUID().uuidString.prefix(6)).txt")
                try? Data("x".utf8).write(to: file)
                let v = s.view
                let drag = FakeDrag(pasteboard: FakeDrag.pasteboard([file as NSURL]), window: w,
                                    location: v.convert(NSPoint(x: v.bounds.midX, y: v.bounds.midY), to: nil))
                let dest = drag.destination()
                _ = dest?.draggingEntered(drag)
                let done = dest?.performDragOperation(drag) ?? false
                let pasted = await Harness.wait(5) { s.readScreen()?.contains(file.lastPathComponent + "⟧") ?? false }
                check("a file dropped on the agent window arrives as a paste", done && pasted, drag.chain())
                try? FileManager.default.removeItem(at: file)
            }
        }
        check("its title is the agent's name, subtitle project · machine · kind",
              ca?.window?.title == model.agent(a)?.name
                && (ca?.window?.subtitle.hasPrefix("\(model.catalog.name(project: model.catalog.projectID(for: model.agent(a)!))) · ") ?? false)
                && (ca?.window?.subtitle.contains(model.agent(a)?.machine ?? "?") ?? false),
              "\(ca?.window?.title ?? "") / \(ca?.window?.subtitle ?? "")")
        check("its tile shows the ↗ marker", main.root.wall.tiles[a]?.windowMarker.isHidden == false)
        await makeKey(main.window)
        await Harness.sleep(0.1)
        click(a, .shift)
        await Harness.sleep(0.1)
        check("⇧-click again brings the same window forward", agentWindowCount == 1 && manager.agentWindow(for: a) === ca && (ca?.window?.isKeyWindow ?? false))

        // ⌘-click opens too; ⌥⇧-click joins the frontmost agent window as a tab.
        click(b, .command)
        let cb = manager.agentWindow(for: b)
        check("⌘-click opens the agent in its own window", cb != nil && agentWindowCount == 2)
        click(c, [.option, .shift])
        let cc = manager.agentWindow(for: c)
        let tabbed = cc?.window?.tabbedWindows?.contains { $0 === cb?.window } ?? false
        check("⌥⇧-click opens it as a tab of the frontmost agent window", cc != nil && tabbed,
              "tabs: \(cc?.window?.tabbedWindows?.count ?? 0)")
        _ = await Harness.wait(10) { self.screen(cc).contains("fake agent") }

        // Size ownership: flipping key between two views of one agent
        // settles once (debounced), no ping-pong.
        await makeKey(main.window)
        model.focus(a)
        _ = await Harness.wait(10) { self.main.root.focus.terminal?.surface?.readScreen()?.contains("fake agent") ?? false }
        await Harness.sleep(1.2)
        var sizes: [GridSize] = []
        manager.eventTap = { e in if case .changed(let ag) = e, ag.id == a, let s = ag.size, sizes.last != s { sizes.append(s) } }
        let sw0 = manager.ownership.switches
        for i in 0..<8 {
            NSApp.activate(ignoringOtherApps: true); (i % 2 == 0 ? ca?.window : main.window)?.makeKeyAndOrderFront(nil)
            await Harness.sleep(0.05)
        }
        await makeKey(ca?.window)
        await Harness.sleep(2.0)
        manager.eventTap = nil
        let sw = manager.ownership.switches - sw0
        let finalOwner: Bool = {
            guard let m = ca?.terminal?.surface?.metrics, let g = self.model.agent(a)?.size else { return false }
            return m.cols == g.cols && m.rows == g.rows
        }()
        check("switching windows doesn't ping-pong the PTY size", sizes.count <= 1 && sw <= 2 && finalOwner,
              "size changes \(sizes.map { "\($0.cols)x\($0.rows)" }), ownership re-attaches \(sw)")
        await makeKey(main.window)
        model.exitFocus()

        // Three candidates, one owner: the key agent window beats the
        // active tile of a wall that isn't key; the active tile owns again
        // once its wall is key.
        model.select(a)
        click(a) // a click on the selected tile makes it active
        _ = await Harness.wait(3) { self.main.root.wall.tiles[a]?.terminal.surfaceIsInteractive ?? false }
        await makeKey(ca?.window)
        await Harness.sleep(1.5)
        let tileT = main.root.wall.tiles[a]?.terminal
        let windowOwns: Bool = {
            guard let m = ca?.terminal?.surface?.metrics, let g = self.model.agent(a)?.size else { return false }
            return m.cols == g.cols && m.rows == g.rows
        }()
        check("key agent window owns over the active tile of another window", windowOwns && tileT?.ownsSize == false && ca?.terminal?.ownsSize == true,
              "tile owns \(String(describing: tileT?.ownsSize)), size \(String(describing: model.agent(a)?.size))")
        await makeKey(main.window)
        await Harness.sleep(1.5)
        check("the active tile owns again when its wall is key", tileT?.ownsSize == true && ca?.terminal?.ownsSize == false)
        model.deactivateTile()

        // ⌥⌘N: a new wall; the first can't fit all six at its minimum → Overflow.
        let key = Harness.keyEvent("n", keyCode: 0x2D, flags: [.command, .option], window: main.window)
        if let key { _ = NSApp.mainMenu?.performKeyEquivalent(with: key) }
        let w2ok = await Harness.wait(3) { self.manager.walls.count == 2 }
        let w2 = manager.walls.last!
        check("⌥⌘N opens a wall window with Overflow", w2ok && w2.scope == .overflow, "scope \(w2.scope.title), main capacity \(main.capacity)")
        let firstIDs = Set(main.model.wall.map(\.id)), secondIDs = Set(w2.model.wall.map(\.id))
        check("overflow continues where the first wall is full", firstIDs.isDisjoint(with: secondIDs) && firstIDs.union(secondIDs).count == ids.count
              && firstIDs.count == main.capacity, "main \(firstIDs.count) (capacity \(main.capacity)), wall 2 \(secondIDs.count)")
        let w2live = await Harness.wait(20) { w2.model.wall.allSatisfy { (w2.root.wall.tiles[$0.id]?.terminal.surface?.readScreen() ?? "").contains(SelfTest.tileMark) } }
        check("the second wall's tiles render", w2live && !w2.model.wall.isEmpty)
        check("one control connection for all windows", manager.walls.allSatisfy { $0.model.client === model.client }
              && manager.agentWindows.allSatisfy { $0.model.client === model.client })

        // Attention always stays on the first wall.
        if let moved = w2.model.wall.last?.id {
            await setState(moved, "approval", attention: ["kind": "approval", "title": "Bash", "detail": "git push", "options": ["allow", "always", "deny"]])
            let up = await Harness.wait(3) { self.main.model.wall.contains { $0.id == moved } && !w2.model.wall.contains { $0.id == moved } }
            check("an agent needing you moves to the first wall", up)
            let ring = main.root.wall.tiles[moved]?.ringColor == Theme.stateNS(.approval)
            check("its ring shows there", ring)
            if shots != nil { await Harness.sleep(1.5); shot(main.window, "walls-all"); shot(w2.window, "walls-overflow") }
            await setState(moved, "working")
        }

        // ⌘J opens the inbox; ⌘O: to the agent's own window when it has one.
        await setState(a, "approval", attention: ["kind": "approval", "title": "Edit", "detail": "x.go", "options": ["allow", "always", "deny"]])
        _ = await Harness.wait(2) { self.model.agent(a)?.state == .approval }
        await makeKey(main.window)
        await Harness.sleep(0.1)
        Harness.press("j", keyCode: 0x26, flags: .command, window: main.window)
        await Harness.sleep(0.2)
        Harness.press("o", keyCode: 0x1F, flags: .command, window: main.window)
        await Harness.sleep(0.2)
        check("⌘J, ⌘O goes to the agent's own window", ca?.window?.isKeyWindow ?? false)
        let stopped = ca?.stop.kind == .needsYou && ca?.window?.backgroundColor == Theme.windowBG
        let ringMain = main.root.wall.tiles[a]?.ringColor == Theme.stateNS(.approval)
        check("its title stop needs you (no tinted title bar) and its tile is ringed", stopped && ringMain)
        if shots != nil, let w = ca?.window { await Harness.sleep(0.8); shot(w, "agent-window") }
        await setState(a, "working")
        if shots != nil, let w = cc?.window { await makeKey(w); await Harness.sleep(0.8); shot(w, "agent-tabs") }

        // ⌃⌘S: the scope popover, attached to the wall's scope pill.
        await makeKey(w2.window)
        await Harness.sleep(0.2)
        if let k = Harness.keyEvent("s", keyCode: 0x01, flags: [.command, .control], window: w2.window) { _ = NSApp.mainMenu?.performKeyEquivalent(with: k) }
        let pop = await Harness.wait(2) { self.manager.scopePopoverShown }
        // The scope menu hangs from the bar's All segment.
        let pillFrame = ScopePill.pill(for: w2.model)?.anchorInWindow.map { w2.root.convert($0, from: nil) } ?? .zero
        let anchor = w2.model.popoverAnchor
        let attachedToPill = pop && abs(anchor.midX - pillFrame.midX) < 2 && abs(anchor.midY - pillFrame.midY) < 2
        check("⌃⌘S shows the scope popover attached to the All segment", attachedToPill, "pill \(pillFrame) anchor \(anchor)")
        if shots != nil, pop { await Harness.sleep(0.6); shot(w2.window, "scope-popover") }
        manager.closeScopePopover()

        // ⌥⌘← ⌥⌘→ within an agent window: tabbed → the previous / next
        // tab (wrapping); untabbed → the window shows the previous / next
        // agent in wall order that has no window of its own.
        func optCmd(_ right: Bool, _ w: NSWindow) {
            if let e = Harness.keyEvent(right ? "\u{F703}" : "\u{F702}", keyCode: right ? 124 : 123, flags: [.command, .option], window: w) {
                _ = w.performKeyEquivalent(with: e)
            }
        }
        if let wb = cb?.window, let wc = cc?.window {
            await makeKey(wc)
            optCmd(true, wc)
            let toB = await Harness.wait(2) { wc.tabGroup?.selectedWindow === wb }
            optCmd(false, wb)
            let toC = await Harness.wait(2) { wc.tabGroup?.selectedWindow === wc }
            check("⌥⌘→ ⌥⌘← in a tabbed agent window select the next / previous tab", toB && toC && cb?.agentID == b && cc?.agentID == c)
        }
        if let ca, let wa = ca.window {
            await makeKey(wa)
            let order = model.unscopedWall.map(\.id)
            let i = order.firstIndex(of: a) ?? 0
            let want = (1..<order.count).map { order[(i + $0) % order.count] }.first { manager.agentWindow(for: $0) == nil }
            optCmd(true, wa)
            let retargeted = want != nil && ca.agentID == want && manager.agentWindow(for: want!) === ca && manager.agentWindow(for: a) == nil
                && wa.title == model.agent(want!)?.name
            let shows = await Harness.wait(10) { self.screen(ca).contains("fake agent") && ca.terminal?.agent.id == want }
            check("⌥⌘→ in an untabbed agent window shows the next agent without a window", retargeted && shows,
                  "want \(want ?? "nil") got \(ca.agentID) title \(wa.title)")
            // Its tile may be on the Overflow wall: every wall showing it.
            func marked(_ id: String) -> [Bool] { manager.walls.compactMap { $0.root.wall.tiles[id]?.windowMarker.isHidden }.map { !$0 } }
            let marker = want.map { !marked($0).isEmpty && marked($0).allSatisfy { $0 } } ?? false
            check("…and that agent's tile carries the ↗ marker instead", marker && !marked(a).contains(true),
                  "\(want ?? "nil") \(want.map(marked) ?? []) / \(a) \(marked(a))")
            optCmd(false, wa)
            let back = await Harness.wait(10) { ca.agentID == a && self.manager.agentWindow(for: a) === ca && self.screen(ca).contains("fake agent") && ca.terminal?.agent.id == a }
            check("⌥⌘← brings the window back to the previous agent", back, ca.agentID)
        }

        // Closing an agent window never stops the agent.
        ca?.window?.performClose(nil)
        await Harness.sleep(1)
        check("closing the agent window leaves the agent running", manager.agentWindow(for: a) == nil && (model.agent(a)?.isRunning ?? false))
        manager.openAgentWindow(a)
        _ = await Harness.wait(5) { self.screen(self.manager.agentWindow(for: a)).contains("fake agent") }

        // Save, plus a window of an agent that no longer exists.
        manager.restorer.saveNow()
        if manager.restorer.url != nil, var s = manager.restorer.load() {
            check("window state saved", s.walls.count == 2 && s.agentWindows.count == 3,
                  "walls \(s.walls.count), agent windows \(s.agentWindows.map(\.agent))")
            s.agentWindows.append(SavedAgentWindow(agent: "L/gone00", frame: SavedFrame(x: 100, y: 100, width: 700, height: 500), screen: nil,
                                                   tabGroup: nil, selectedTab: false, fullScreen: false))
            manager.desks.record(s) // desks.json: the current desk
        } else {
            check("window state saved", false, "no state file")
        }
        finish(terminate: true)
    }

    // MARK: Phase 2 (relaunch)

    private func runPhase2() async {
        _ = await Harness.wait(15) { self.model.isConnected && self.model.wall.count >= 6 }
        let ids = model.registry.wallOrder.map(\.id)
        guard ids.count >= 3 else { return finish() }
        let a = ids[0], b = ids[1], c = ids[2]
        let back = await Harness.wait(10) { self.manager.agentWindows.count >= 3 }
        check("relaunch restores the agent windows", back, manager.agentWindows.map(\.agentID).sorted().joined(separator: ","))
        check("removed agents' windows don't come back", manager.agentWindow(for: "L/gone00") == nil)
        check("each agent window is back for its agent", [a, b, c].allSatisfy { manager.agentWindow(for: $0) != nil })
        let tabbed = manager.agentWindow(for: c)?.window?.tabbedWindows?.contains { $0 === self.manager.agentWindow(for: b)?.window } ?? false
        check("the tab group is restored", tabbed)
        check("the second wall is restored with its scope", manager.walls.count == 2 && manager.walls[1].scope == .overflow,
              manager.walls.map { $0.scope.title }.joined(separator: ","))
        let onScreen = (manager.walls.map(\.window) + manager.agentWindows.compactMap(\.window)).allSatisfy { w in
            NSScreen.screens.contains { $0.visibleFrame.intersects(w.frame) }
        }
        check("restored frames are on a screen", onScreen)
        let attached = await Harness.wait(10) { self.screen(self.manager.agentWindow(for: a)).contains("fake agent") }
        check("restored agent windows attach", attached)
        finish(terminate: true)
    }

    // MARK: Screenshots

    private func shot(_ w: NSWindow, _ name: String) {
        guard let dir = shots else { return }
        let path = "\(dir)/\(name).png"
        run(["-x", "-o", "-l", "\(w.windowNumber)", path])
        shotFiles.append(path)
    }

    private func run(_ args: [String]) {
        let p = Process()
        p.executableURL = URL(fileURLWithPath: "/usr/sbin/screencapture")
        p.arguments = args
        try? p.run()
        p.waitUntilExit()
    }

    private func finish(terminate: Bool = false) /* exit without the quit-time save: phase 1 edited the file */ {
        let failed = checks.filter { !$0.ok }.count
        Harness.write(Result(phase: phase, passed: checks.count - failed, failed: failed, checks: checks, shots: shotFiles), to: out)
        exit(failed == 0 ? 0 : 1)
    }
}
