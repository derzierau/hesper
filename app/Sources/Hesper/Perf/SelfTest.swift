import AppKit
import HesperCore

/// UI smoke test (`make test-ui`): drives the real app against the fake
/// daemon through the same key paths a user takes, checks what the views
/// show, writes JSON and quits with a status.
@MainActor
final class SelfTest {
    let model: AppModel
    let root: RootView
    let out: String
    var checks: [Check] = []

    struct Check: Encodable { var name: String; var ok: Bool; var detail: String }
    struct Result: Encodable { var passed: Int; var failed: Int; var checks: [Check]; var reattachMs: Double }

    init(model: AppModel, root: RootView, out: String, phase: String? = nil) {
        self.model = model
        self.root = root
        self.out = out
        Task { if phase == "restore" { await runRestore() } else { await run() } }
    }

    private func check(_ name: String, _ ok: Bool, _ detail: String = "") {
        checks.append(Check(name: name, ok: ok, detail: detail))
        print(ok ? "ok  " : "FAIL", name, detail)
    }

    private var window: NSWindow { root.window! }

    private func screen(_ id: String) -> String { root.wall.tiles[id]?.terminal.surface?.readScreen() ?? "" }

    /// Against the fake daemon states are set directly (test-only
    /// `fake.setState`); against the real hesperd they come the way they do
    /// in production: a `hesperd hook claude <Event>` process with the
    /// agent's HESPER_AGENT_ID, as Claude's hooks would run it.
    private var realDaemon: Bool { (model.registry.hello?.daemon ?? "") != "fake-hesperd" }

    private func setState(_ id: String, _ state: String, attention: JSONValue = .null, summary: String? = nil) async {
        guard realDaemon else {
            var p: [String: JSONValue] = ["id": .string(id), "state": .string(state), "attention": attention]
            if let summary { p["summary"] = .string(summary) }
            _ = try? await model.client.call("fake.setState", .object(p))
            return
        }
        let sid = model.agent(id)?.sessionId ?? ""
        var payload: [String: JSONValue] = ["session_id": .string(sid), "cwd": .string(model.agent(id)?.project ?? "/")]
        let event: String
        switch state {
        case "approval":
            event = "PermissionRequest"
            payload["tool_name"] = attention["title"] ?? "Bash"
            payload["tool_input"] = ["command": attention["detail"] ?? "true"]
        case "question":
            event = "PreToolUse"
            payload["tool_name"] = "AskUserQuestion"
            payload["tool_input"] = ["questions": [["question": attention["detail"] ?? "?"]]]
        case "done":
            event = "Stop"
            payload["last_assistant_message"] = .string(summary ?? "done")
        default:
            event = "UserPromptSubmit"
            payload["prompt"] = "continue"
        }
        payload["hook_event_name"] = .string(event)
        let data = (try? JSONEncoder().encode(JSONValue.object(payload))) ?? Data()
        let env = model.env
        await Task.detached {
            let p = Process()
            p.executableURL = URL(fileURLWithPath: env.hesperdPath)
            p.arguments = ["hook", "claude", event]
            var e = ProcessInfo.processInfo.environment
            e["HESPER_AGENT_ID"] = id
            e["HESPER_SOCKET"] = env.socketPath
            p.environment = e
            let pipe = Pipe()
            p.standardInput = pipe
            try? p.run()
            pipe.fileHandleForWriting.write(data)
            try? pipe.fileHandleForWriting.close()
            p.waitUntilExit()
        }.value
    }

    /// A project the daemon can make worktrees in (real hesperd needs a repo).
    private var project: String { ProcessInfo.processInfo.environment["HESPER_SELFTEST_PROJECT"] ?? "/tmp/fake/project" }

    private func run() async {
        let start = Date()
        // 1. Connect and show every agent live (start three when the daemon
        // has none: the real hesperd starts empty).
        _ = await Harness.wait(15) { self.model.isConnected }
        await Harness.sleep(0.3)
        for i in model.wall.count..<3 where model.isConnected {
            _ = await model.spawn(SpawnRequest(project: project, task: "selftest agent \(i + 1)\nfake work"))
        }
        let connected = await Harness.wait(15) { self.model.isConnected && self.model.wall.count >= 3 }
        check("connects and lists agents", connected, "\(model.wall.count) agents")
        // Tiles show the agent's last rows (the fake TUI's spinner line and
        // prompt), not its first.
        let live = await Harness.wait(20) { self.model.wall.allSatisfy { self.screen($0.id).contains(Self.tileMark) } }
        let reattach = Date().timeIntervalSince(start) * 1000
        check("every tile renders its agent (ro attach)", live, String(format: "%.0f ms after launch", reattach)
              + (live ? "" : " " + model.wall.map { "[\(self.screen($0.id).prefix(160))]" }.joined(separator: " ")))
        guard model.wall.count >= 3 else { return finish(reattach) }
        let a = model.wall[0], b = model.wall[1], c = model.wall[2]

        // Cards are sized to their terminals: the engine's grid is exactly
        // the terminal area (no slack), the agent's columns fit (unless cut
        // at the minimum font). Refits are debounced: let the wall settle.
        let exact = await Harness.wait(5) { self.model.wall.allSatisfy { self.tileIsExact($0.id) == nil } }
        check("tile terminal area is exactly the engine grid", exact,
              model.wall.compactMap { self.tileIsExact($0.id) }.joined(separator: "; "))

        // 2. Approval: ring + inline allow via the Return key on the wall.
        await setState(a.id, "approval", attention: ["kind": "approval", "title": "Bash", "detail": "git push", "options": ["allow", "always", "deny"]])
        let ringed = await Harness.wait(2) {
            guard let t = self.root.wall.tiles[a.id], let col = t.layer?.borderColor else { return false }
            return t.agent.state == .approval && col == Theme.stateNS(.approval).cg(in: t)
        }
        check("approval shows a ring", ringed)
        model.select(a.id)
        window.makeFirstResponder(root.wall)
        Harness.press("\r", keyCode: 36, window: window)
        let allowed = await Harness.wait(3) { self.model.agent(a.id)?.state == .working }
        await setState(a.id, "working")
        // ⏎ on a selected tile that needs approval is Allow, not "start typing".
        check("⏎ on an approval tile allows it (not typing)", allowed && model.activeTileID == nil,
              (model.agent(a.id)?.state.rawValue ?? "?") + " active=\(model.activeTileID ?? "nil")")

        // 3. ⌘J: the inbox. Approval before question, oldest first; the
        // approval answers inline (1 Allow · 2 Always · 3 Deny), free text
        // opens; J moves, ⌘O opens the tile, ⏎ the first answer, esc closes.
        await setState(b.id, "question", attention: ["kind": "question", "title": "Question", "detail": "Home or settings?"])
        await Harness.sleep(0.05)
        await setState(c.id, "approval", attention: ["kind": "approval", "title": "Edit", "detail": "x.go", "options": ["allow", "always", "deny"]])
        _ = await Harness.wait(2) { self.model.agent(c.id)?.state == .approval && self.model.agent(b.id)?.state == .question }
        model.select(a.id)
        window.makeFirstResponder(root.wall)
        Harness.press("j", keyCode: 0x26, flags: .command, window: window)
        let inbox = model.attentionEntries()
        check("⌘J opens the inbox: the approval first, then the question", model.popover == .attention && inbox.map(\.id) == [c.id, b.id],
              "\(model.popover?.key ?? "nil") \(inbox.map(\.id))")
        check("the approval answers inline, the free-text question opens",
              inbox.first?.answers.map(\.title) == ["Allow", "Always", "Deny"] && inbox.last?.answers.map(\.title) == ["Open"],
              inbox.map { $0.answers.map(\.title).joined(separator: ",") }.joined(separator: " | "))
        Harness.press("o", keyCode: 0x1F, flags: .command, window: window)
        check("⌘O opens the selected item's tile", model.popover == nil && model.selectedID == c.id, model.selectedID ?? "nil")
        window.makeFirstResponder(root.wall)
        Harness.press("j", keyCode: 0x26, flags: .command, window: window)
        _ = Harness.press("j", keyCode: 0x26, window: window)
        Harness.press("o", keyCode: 0x1F, flags: .command, window: window)
        check("J moves down: ⌘O then opens the question", model.popover == nil && model.selectedID == b.id, model.selectedID ?? "nil")
        window.makeFirstResponder(root.wall)
        Harness.press("j", keyCode: 0x26, flags: .command, window: window)
        _ = Harness.press("\r", keyCode: 36, window: window)
        let allowedInline = await Harness.wait(3) { self.model.agent(c.id)?.state == .working }
        check("⏎ in the inbox allows the approval in place (the inbox stays)", allowedInline && model.popover == .attention,
              (model.agent(c.id)?.state.rawValue ?? "?") + " \(model.popover?.key ?? "nil")")
        _ = Harness.press("\u{1b}", keyCode: 53, window: window)
        check("esc closes the inbox", model.popover == nil)
        await setState(b.id, "done", summary: "Opened PR #482 (draft)")
        await setState(c.id, "working")

        // 4. Focus: rw --owner; typing reaches the agent; PTY takes the view size.
        model.select(a.id)
        Harness.press("\r", keyCode: 36, flags: .command, window: window)
        check("⌘↩ opens focus", model.mode == .focus && model.focusedID == a.id)
        let focusUp = await Harness.wait(10) { self.root.focus.terminal?.surface?.readScreen()?.contains("fake agent") ?? false }
        check("focus view attaches", focusUp)
        if let s = root.focus.terminal?.surface {
            for ch in "hello" { s.sendKey(character: ch) }
            let echoed = await Harness.wait(3) { s.readScreen()?.contains("hello") ?? false }
            check("typing in focus reaches the agent", echoed, String(s.readScreen()?.split(separator: "\n").last ?? "nil"))
            let sized = await Harness.wait(3) {
                guard let m = s.metrics, let g = self.model.agent(a.id)?.size else { return false }
                return m.cols == g.cols && m.rows == g.rows
            }
            check("focus view owns the PTY size", sized, "\(String(describing: s.metrics.map { "\($0.cols)x\($0.rows)" })) vs \(String(describing: model.agent(a.id)?.size))")
            let hidden = root.wall.tiles.values.allSatisfy { !$0.terminal.visible }
            check("wall tiles stop rendering in focus", hidden, root.wall.tiles.values.filter { $0.terminal.visible }.map { "\($0.agent.id) hidden=\($0.isHidden) wallRV=\(root.wall.renderingVisible) mode=\(model.mode)" }.joined(separator: "; "))
        }
        // Plain ← → belong to the agent in focus (the fake TUI shows → as
        // ›); ⌥⌘→ ⌥⌘← step through the agents in wall order.
        if let s = root.focus.terminal?.surface {
            window.makeFirstResponder(s.view)
            Harness.press("\u{F703}", keyCode: 124, window: window)
            let reached = await Harness.wait(3) { s.readScreen()?.contains("›") ?? false }
            check("plain → in focus reaches the agent (no step)", reached && model.mode == .focus && model.focusedID == a.id,
                  String(s.readScreen()?.split(separator: "\n").last ?? "nil"))
        }
        Harness.press("\u{F703}", keyCode: 124, flags: [.command, .option], window: window)
        check("⌥⌘→ in focus steps to the next agent", model.focusedID == b.id, model.focusedID ?? "nil")
        Harness.press("\u{F702}", keyCode: 123, flags: [.command, .option], window: window)
        check("⌥⌘← steps back", model.focusedID == a.id, model.focusedID ?? "nil")
        Harness.press("]", keyCode: 0x1E, flags: .command, window: window)
        check("⌘] steps to the next agent", model.focusedID == b.id, model.focusedID ?? "nil")
        Harness.press("\r", keyCode: 36, flags: .command, window: window)
        check("⌘↩ returns to the wall", model.mode == .wall)
        let back = root.wall.tiles.values.allSatisfy { $0.terminal.visible }
        check("tiles render again on the wall", back)
        let refit = await Harness.wait(5) {
            guard let g = self.model.agent(a.id)?.size, let t = self.root.wall.tiles[a.id], t.agent.size == g,
                  let p = t.placed, let m = t.terminal.surface?.metrics else { return false }
            _ = p
            return m.cols <= g.cols && self.tileIsExact(a.id) == nil
        }
        check("tile refits after the PTY size changed", refit, tileIsExact(a.id) ?? "")
        // Focus closed: the owner is gone, the PTY follows the tile's fit
        // again (hesperd: ≥ 80x24, ±1), and every tile has the one font.
        let fitBack = await Harness.wait(6) {
            guard let g = self.model.agent(a.id)?.size, let p = self.root.wall.tiles[a.id]?.placed else { return false }
            return abs(g.cols - max(p.cols, 80)) <= 1 && abs(g.rows - max(p.rows, 24)) <= 1
        }
        check("after focus the PTY returns to the tile's grid", fitBack,
              "\(String(describing: model.agent(a.id)?.size)) vs tile \(root.wall.tiles[a.id]?.placed.map { "\($0.cols)x\($0.rows)" } ?? "?")")
        let fonts = Set(root.wall.tiles.values.compactMap { $0.onShelf ? nil : $0.terminal.surface?.fontSize })
        check("every tile has the same font", fonts.count == 1, "\(fonts)")
        check("done tile shows its summary", root.wall.tiles[b.id]?.agent.summary == "Opened PR #482 (draft)")
        let shelved = await Harness.wait(3) {
            guard let t = self.root.wall.tiles[b.id] else { return false }
            return t.onShelf && t.terminal.surface == nil && t.frame.height == Metrics.wall.shelfHeight
        }
        check("a done agent moves to the shelf (no terminal)", shelved)

        // Layouts: ⌥⌘2 columns, ⌥⌘L the next one, ⌥⌘1 back; every live
        // tile's terminal area stays exactly its engine grid.
        Harness.press("2", keyCode: 0x13, flags: [.command, .option], window: window)
        let columns = model.arrangement == .columns
        let exactColumns = await Harness.wait(5) {
            self.model.wall.allSatisfy { self.root.wall.tiles[$0.id]?.onShelf == false && self.tileIsExact($0.id) == nil }
        }
        check("⌥⌘2 switches to columns, tiles exact", columns && exactColumns,
              model.wall.compactMap { self.tileIsExact($0.id) }.joined(separator: "; "))
        Harness.press("l", keyCode: 0x25, flags: [.command, .option], window: window)
        let treemap = model.arrangement == .treemap
        let exactTreemap = await Harness.wait(5) { self.model.wall.allSatisfy { self.tileIsExact($0.id) == nil } }
        check("⌥⌘L cycles to the treemap, tiles exact", treemap && exactTreemap)
        Harness.press("1", keyCode: 0x12, flags: [.command, .option], window: window)
        let shelfBack = await Harness.wait(5) { self.root.wall.tiles[b.id]?.onShelf == true }
        check("⌥⌘1 back to grid + shelf", model.arrangement == .shelf && shelfBack)

        // 5. ⌘⇧M: a popover from the tile; ⏎ moves (the tile says
        // "moving…" in place) and the undo toast moves it back. The real
        // hesperd without part R's remote answers unavailable: a toast.
        model.select(c.id)
        Harness.press("M", keyCode: 0x2E, flags: [.command, .shift], window: window)
        check("⌘⇧M opens the move popover from the tile", model.popover == .move(c.id), String(describing: model.popover))
        let canMove = model.machines.contains { $0.short != c.machine && $0.online }
        if !canMove {
            // One machine (the real hesperd without remotes): the popover
            // says so and esc closes it.
            Harness.press("\u{1b}", keyCode: 53, window: window)
            check("move popover closes with esc (no other machine)", model.popover == nil)
        }
        if canMove { Harness.press("\r", keyCode: 36, window: window) }
        if !canMove {
        } else if realDaemon {
            let toast = await Harness.wait(5) { (self.model.toast?.text.contains("not available") ?? false) || (self.model.toast?.text.contains("Could not") ?? false) }
            check("move reports unavailable cleanly", toast, model.toast?.text ?? "no toast")
        } else {
            let movingShown = await Harness.wait(1) { !self.model.moving.isEmpty && self.root.wall.tiles[c.id] != nil }
            check("the tile shows moving… in place", movingShown)
            let local = c.localID
            let moved = await Harness.wait(8) { self.model.wall.contains { $0.localID == local && $0.machine != c.machine } && self.model.moving.isEmpty }
            check("move hands the agent to the other machine", moved, model.wall.map(\.id).joined(separator: ","))
            check("an undo toast offers to move it back", model.undoToast?.label.hasPrefix("Moved") ?? false, model.undoToast?.label ?? "none")
            window.makeFirstResponder(root.wall)
            Harness.press("z", keyCode: 0x06, flags: .command, window: window)
            let back = await Harness.wait(8) { self.model.agent(c.id) != nil && self.model.moving.isEmpty }
            check("⌘Z moves it back", back, model.wall.map(\.id).joined(separator: ","))
        }
        let cNow = model.agent(c.id) ?? c

        // 6. ⌃⌘W kills at once (no dialog): the pane stays, Killed; ⏎ resumes.
        //    (⌘W closes: off the wall, ⌘Z resumes its session — HistorySelfTest.)
        model.select(cNow.id)
        window.makeFirstResponder(root.wall)
        Harness.press("w", keyCode: 0x0D, flags: [.command, .control], window: window)
        check("⌃⌘W kills at once (no confirm)", model.popover == nil && !model.showPalette)
        let exited = await Harness.wait(8) { self.model.agent(c.id)?.state == .exited && self.model.agent(c.id)?.exit != nil }
        check("kill leaves the agent exited, its pane on the wall", exited && model.wall.contains { $0.id == c.id },
              String(screen(c.id).split(separator: "\n").suffix(2).joined(separator: " / ")))
        check("the pane says Killed", model.agent(c.id).map(model.isKilled) ?? false, model.agent(c.id)?.ended ?? "no ended")
        model.select(c.id)
        window.makeFirstResponder(root.wall)
        Harness.press("\r", keyCode: 36, window: window)
        let resumed = await Harness.wait(8) { (self.model.agent(c.id)?.isRunning ?? false) && self.screen(c.id).contains(Self.tileMark) }
        check("⏎ resumes the killed agent (tile reattaches)", resumed, "[\(screen(c.id).prefix(200))] \(model.agent(c.id)?.state.rawValue ?? "?")")

        // 7. Spawn through the client: a tile appears.
        let before = model.wall.count
        let err = await model.spawn(SpawnRequest(project: project, task: "selftest spawned agent\nmore", worktree: .auto, branch: "feature/selftest"))
        let appeared = await Harness.wait(8) { self.model.wall.count == before + 1 && self.root.wall.tiles.count == before + 1 }
        check("spawn adds a tile", err == nil && appeared, err ?? "")

        // 8. Palette opens and closes with ⌘K; esc closes it too.
        Harness.press("k", keyCode: 0x28, flags: .command, window: window)
        let opened = model.showPalette
        Harness.press("k", keyCode: 0x28, flags: .command, window: window)
        check("⌘K toggles the palette", opened && !model.showPalette)
        Harness.press("k", keyCode: 0x28, flags: .command, window: window)
        Harness.press("\u{1b}", keyCode: 53, window: window)
        check("esc closes the palette", !model.showPalette)

        // 8a. The footer never moves the terminal: approval, working, idle
        // in quick succession leave the terminal area where it was.
        if let t = root.wall.tiles[a.id] {
            await setState(a.id, "working")
            await Harness.sleep(0.8)
            let before = t.terminal.host.frame
            var frames = Set<String>()
            for st in ["approval", "working", "idle", "working", "question", "working"] {
                await setState(a.id, st, attention: st == "approval" ? ["kind": "approval", "title": "Bash", "detail": "true", "options": ["allow", "always", "deny"]]
                                                   : st == "question" ? ["kind": "question", "title": "Question", "detail": "?"] : .null)
                await Harness.sleep(0.15)
                frames.insert("\(t.terminal.host.frame)")
            }
            await Harness.sleep(0.6)
            check("state changes never move the tile's terminal", frames == ["\(before)"] && t.terminal.host.frame == before, "\(frames)")
        }

        // 8b. A first-run trust question (fake only: the real daemon finds
        // it on the agent's screen): ⏎ on the tile answers trust.
        if !realDaemon {
            await setState(a.id, "question", attention: ["kind": "question", "title": "Trust folder", "detail": "Trust ~/projects/x?", "options": ["trust", "exit"]])
            _ = await Harness.wait(2) { self.model.agent(a.id)?.attention?.isTrust == true }
            model.select(a.id)
            window.makeFirstResponder(root.wall)
            Harness.press("\r", keyCode: 36, window: window)
            let trusted = await Harness.wait(3) { self.model.agent(a.id)?.state == .working }
            check("⏎ on a trust question trusts the folder", trusted, model.agent(a.id)?.state.rawValue ?? "?")
        }

        // 8c0. Two levels on the wall: a click selects (no typing, no
        // read-write attach), arrows / Home / End move the selection without
        // resizing anything, typing a character makes the tile active and
        // delivers it, ⌘esc goes back to selected, a click on the selected
        // tile makes it active.
        let liveIDs = model.wall.filter { $0.isRunning && !$0.state.needsAttention && !(self.root.wall.tiles[$0.id]?.onShelf ?? true) }.map(\.id)
        if liveIDs.count >= 2, let t = root.wall.tiles[liveIDs[0]] {
            let x = liveIDs[0]
            model.deactivateTile()
            model.select(liveIDs[1])
            window.makeFirstResponder(root.wall)
            click(t)
            await Harness.sleep(0.6)
            let noRW = root.wall.tiles.values.allSatisfy { !$0.terminal.surfaceIsInteractive && $0.terminal.prewarm == nil }
            check("a click selects the tile: no typing, no read-write attach", model.selectedID == x && model.activeTileID == nil
                  && window.firstResponder === root.wall && noRW, "selected=\(model.selectedID ?? "nil") active=\(model.activeTileID ?? "nil") rw=\(!noRW)")
            let sizes0 = model.wall.map { "\($0.id)=\(String(describing: $0.size))" }
            let frames0 = root.wall.tiles.mapValues(\.frame)
            Harness.press("\u{F703}", keyCode: 124, window: window)
            let right = root.wall.neighbor(x, .right)
            check("→ moves the selection to the neighbor on the right", right != nil && right != x && model.selectedID == right && model.activeTileID == nil,
                  "\(x) → \(model.selectedID ?? "nil") (want \(right ?? "nil"))")
            let back = root.wall.neighbor(model.selectedID, .left)
            Harness.press("\u{F702}", keyCode: 123, window: window)
            check("← moves it back", model.selectedID == back, model.selectedID ?? "nil")
            Harness.press("\u{F701}", keyCode: 125, window: window)
            let down = model.selectedID
            Harness.press("\u{F700}", keyCode: 126, window: window)
            check("↓ ↑ move by the layout", down != nil && model.selectedID == root.wall.neighbor(down, .up), "\(down ?? "nil") → \(model.selectedID ?? "nil")")
            Harness.press("\u{F72B}", keyCode: 119, window: window)
            let end = model.selectedID
            Harness.press("\u{F729}", keyCode: 115, window: window)
            check("Home / End: the first / last card", end == root.wall.neighbor(nil, .last) && model.selectedID == root.wall.neighbor(nil, .first),
                  "end \(end ?? "nil") home \(model.selectedID ?? "nil")")
            await Harness.sleep(0.8)
            let noRW2 = root.wall.tiles.values.allSatisfy { !$0.terminal.surfaceIsInteractive && $0.terminal.prewarm == nil }
            let sizes1 = model.wall.map { "\($0.id)=\(String(describing: $0.size))" }
            check("moving the selection attaches nothing read-write and resizes nothing", noRW2 && sizes0 == sizes1
                  && root.wall.tiles.mapValues(\.frame) == frames0, "rw=\(!noRW2) \(sizes0) → \(sizes1)")
            if shots != nil { model.select(x); await Harness.sleep(0.6); shot("tile-selected") }
            // Just start typing: the first key activates, every key arrives.
            model.select(x)
            window.makeFirstResponder(root.wall)
            for ch in "zeta" { Harness.press(String(ch), keyCode: 0, window: window) }
            let typed = await Harness.wait(5) {
                self.model.activeTileID == x && t.terminal.surfaceIsInteractive && (t.terminal.surface?.readScreen()?.contains("zeta") ?? false)
            }
            check("typing a character on a selected tile makes it active and delivers it", typed,
                  "active=\(model.activeTileID ?? "nil") rw=\(t.terminal.surfaceIsInteractive) [\(t.terminal.surface?.readScreen()?.split(separator: "\n").last ?? "")]")
            if shots != nil { await Harness.sleep(0.4); shot("tile-active") }
            Harness.press("\u{1b}", keyCode: 53, flags: .command, window: window)
            let selectedAgain = await Harness.wait(3) {
                self.model.activeTileID == nil && self.model.selectedID == x && !t.terminal.surfaceIsInteractive && self.window.firstResponder === self.root.wall
            }
            check("⌘esc goes back to selected (the wall has the keys)", selectedAgain)
            click(t)
            let activeAgain = await Harness.wait(5) { self.model.activeTileID == x && t.terminal.surfaceIsInteractive }
            check("a click on the selected tile makes it active", activeAgain)
            Harness.press("\u{1b}", keyCode: 53, flags: .command, window: window)
            _ = await Harness.wait(3) { self.model.activeTileID == nil && !t.terminal.surfaceIsInteractive }
        }

        // 8c. Typing on the wall: ⏎ on a selected tile makes it active
        // (read-write, seamless swap), keys reach the agent, ⌘esc leaves.
        if let x = model.wall.first(where: { $0.isRunning && !$0.state.needsAttention && !(self.root.wall.tiles[$0.id]?.onShelf ?? true) }),
           let t = root.wall.tiles[x.id] {
            model.select(x.id)
            window.makeFirstResponder(root.wall)
            let frame0 = t.terminal.host.frame
            Harness.press("\r", keyCode: 36, window: window)
            var gaps = 0
            let swapped = await Harness.wait(5) {
                if t.terminal.surface == nil { gaps += 1 }
                return t.terminal.surfaceIsInteractive && self.window.firstResponder === t.terminal.surface?.view
            }
            check("⏎ makes the tile active: read-write, keyboard focus, no gap", model.activeTileID == x.id && swapped && gaps == 0
                  && t.terminal.host.frame == frame0, "active=\(model.activeTileID ?? "nil") swapped=\(swapped) gaps=\(gaps)")
            if let sv = t.terminal.surface {
                for ch in "wall" { Harness.press(String(ch), keyCode: 0, window: window) }
                let typed = await Harness.wait(3) { sv.readScreen()?.contains("wall") ?? false }
                check("typing in the active tile reaches the agent", typed, String(sv.readScreen()?.split(separator: "\n").last ?? ""))
            }
            Harness.press("\u{1b}", keyCode: 53, flags: .command, window: window)
            let left = await Harness.wait(3) { self.model.activeTileID == nil && !t.terminal.surfaceIsInteractive && t.terminal.prewarm == nil }
            check("⌘esc leaves the tile (back to a read-only view)", left)
            // Switching rapidly leaves exactly one read-write surface.
            let ids = model.wall.filter { $0.isRunning && !(self.root.wall.tiles[$0.id]?.onShelf ?? true) }.map(\.id)
            for id in (ids + ids).prefix(7) { model.activate(id) }
            await Harness.sleep(1.5)
            let rw = root.wall.tiles.values.filter { $0.terminal.surfaceIsInteractive }.map(\.agent.id)
            let leaks = root.wall.tiles.values.filter { $0.terminal.prewarm != nil }.count
            check("rapid switching leaves one active read-write tile, no leaks", rw == [model.activeTileID ?? "-"] && leaks == 0, "rw=\(rw) prewarm=\(leaks)")
            // A remote agent's tile (fake: the mini).
            if !realDaemon, model.machine("M")?.online == true {
                let err = await model.spawn(SpawnRequest(machine: "M", project: project, task: "selftest remote typing"))
                let up = await Harness.wait(8) { self.model.wall.contains { $0.machine == "M" && self.root.wall.tiles[$0.id]?.terminal.surface != nil } }
                if err == nil, up, let m = model.wall.first(where: { $0.machine == "M" }) {
                    model.activate(m.id)
                    let ok = await Harness.wait(5) { self.root.wall.tiles[m.id]?.terminal.surfaceIsInteractive ?? false }
                    check("a remote agent's tile becomes active", ok)
                } else {
                    check("a remote agent's tile becomes active", false, err ?? "no tile")
                }
            }
            model.deactivateTile()
            await Harness.sleep(0.5)
        }

        // 8d. Scrolling a tile back (the real hesperd keeps scrollback; the
        // fake daemon has none): the window stays on the same lines while
        // the agent goes on, a pill counts the new lines, End/click: live.
        if realDaemon, let x = model.wall.first(where: { $0.isRunning && !(self.root.wall.tiles[$0.id]?.onShelf ?? true) }),
           let t = root.wall.tiles[x.id] {
            _ = await Harness.wait(15) {
                // Wait for some scrollback (the fake TUI logs a line every 450 ms).
                t.terminal.scroll(lines: 0.0)
                return true
            }
            await Harness.sleep(4)
            t.terminal.scroll(lines: 6)
            let told = await Harness.wait(4) { t.terminal.scrollInfo.max > 0 && t.terminal.scrollInfo.offset >= 6 }
            check("a tile scrolls back into the agent's scrollback", told, "\(t.terminal.scrollInfo)")
            await Harness.sleep(0.5)
            let top0 = t.terminal.surface?.readScreen() ?? ""
            let grew = await Harness.wait(4) { t.terminal.scrollInfo.new >= 2 }
            let top1 = t.terminal.surface?.readScreen() ?? ""
            let pillShown = !(root.wall.tiles[x.id].map { $0.subviews.isEmpty } ?? true)
            check("new output does not move a scrolled tile; the pill counts it", grew && top0 == top1 && top0.contains { !$0.isWhitespace } && pillShown,
                  "\(t.terminal.scrollInfo) [\(top0.suffix(120))] → [\(top1.suffix(120))]")
            model.select(x.id)
            window.makeFirstResponder(root.wall)
            Harness.press("\u{F72B}", keyCode: 119, window: window)
            let live = await Harness.wait(3) { t.terminal.scrollInfo.offset == 0 }
            check("End takes the tile back to live", live, "\(t.terminal.scrollInfo)")
        }

        await runDrops()

        // 9. Drafts: ⌘N puts a draft tile right of the selected tile.
        guard let anchor = model.wall.first(where: { $0.isRunning && !$0.state.showsSummary }) else { return finish(reattach) }
        model.select(anchor.id)
        window.makeFirstResponder(root.wall)
        Harness.press("n", keyCode: 0x2D, flags: .command, window: window)
        let draftID = model.editingDraftID
        let items = model.wallItems.map(\.id)
        let ai = items.firstIndex(of: anchor.id) ?? -9
        check("⌘N inserts a draft right of the selected tile", draftID != nil && items.firstIndex(of: draftID!) == ai + 1, "\(items)")
        guard let did = draftID else { return finish(reattach) }
        // New drafts have no project (not the selected agent's): "Choose
        // folder", Start disabled.
        let composer = model.composer(for: did)
        check("⌘N's draft has no project (Choose folder; Start disabled)", model.draft(did)?.project == nil && composer.needsFolder
              && model.isNewDraft(did), model.draft(did)?.project ?? "nil")
        let focused = await Harness.wait(3) { self.window.firstResponder === self.root.wall.draftTiles[did]?.editor.textView }
        check("the draft's editor has the keyboard", focused)
        let text = "selftest draft started in place\nsecond line"
        typeIntoDraft(did, text)
        let saved = await Harness.waitAsync(5) { await self.daemonDraft(did)?.text == text }
        check("the draft is saved to the daemon", saved)
        let noProject = await daemonDraft(did)
        check("…without a project", noProject != nil && noProject?.project == nil)
        // ⌘↩ without a folder: nothing starts; the folder chip's list opens.
        let agentsBefore = model.wall.count
        Harness.press("\r", keyCode: 36, flags: .command, window: window)
        await Harness.sleep(0.4)
        check("⌘↩ without a folder starts nothing and opens the folder list", model.wall.count == agentsBefore && model.popover == .chip(did, .project)
              && composer.error == nil, "\(String(describing: model.popover)) \(composer.error ?? "")")
        model.closePopover()
        // The anchor's folder (the same project: the wall stays plain). The
        // fake's folder isn't on disk: an inline note, created on start.
        let folder = anchor.project ?? (model.env.stateDir ?? NSTemporaryDirectory())
        var isDir: ObjCBool = false
        let existed = FileManager.default.fileExists(atPath: folder, isDirectory: &isDir)
        composer.choose(.project, value: folder)
        await Harness.sleep(0.3)
        check("choosing the folder: the project follows; a missing folder is a note in the chip row (no warning)",
              composer.missingFolder == (existed ? nil : folder) && composer.error == nil
              && model.toast == nil && model.draft(did)?.project == folder && !composer.needsFolder, "existed \(existed)")
        shot("draft-missing-folder")
        window.makeFirstResponder(root.wall.draftTiles[did]?.editor.textView)
        await Harness.sleep(0.5)
        let draftFrame = root.wall.draftTiles[did]?.frame ?? .zero
        let draftIndex = model.wallItems.firstIndex { $0.id == did }
        Harness.press("\r", keyCode: 36, flags: .command, window: window)
        let started = await Harness.wait(10) { self.model.wall.contains { $0.task == "selftest draft started in place\nsecond line" } }
        let agentID = model.wall.first { $0.task == text }?.id ?? ""
        await Harness.sleep(0.6)
        let agentFrame = root.wall.tiles[agentID]?.frame ?? .zero
        let sameSlot = model.wallItems.firstIndex { $0.id == agentID } == draftIndex
        let sameFrame = abs(agentFrame.minX - draftFrame.minX) < 1 && abs(agentFrame.minY - draftFrame.minY) < 1
            && abs(agentFrame.width - draftFrame.width) < 1 && abs(agentFrame.height - draftFrame.height) < 1
        check("⌘↩ turns the draft into the agent in place", started && sameSlot && sameFrame && model.draft(did) == nil,
              "slot \(String(describing: draftIndex)) frame \(draftFrame) → \(agentFrame) started=\(started)")
        let removed = await Harness.waitAsync(5) { await self.daemonDraft(did) == nil }
        check("a started draft leaves the daemon", removed)
        check("…in its folder, created on start when it was missing", FileManager.default.fileExists(atPath: folder, isDirectory: &isDir) && isDir.boolValue
              && model.agent(agentID)?.project == folder && composer.error == nil, model.agent(agentID)?.project ?? "")
        // Created by this run and still empty: gone again (the next run
        // sees it missing too).
        if !existed, (try? FileManager.default.contentsOfDirectory(atPath: folder))?.isEmpty == true { try? FileManager.default.removeItem(atPath: folder) }

        // Esc keeps a draft as a quiet tile (the shelf), saved; an empty
        // draft and esc discards it.
        model.select(agentID)
        window.makeFirstResponder(root.wall)
        Harness.press("n", keyCode: 0x2D, flags: .command, window: window)
        guard let kept = model.editingDraftID else { return finish(reattach) }
        _ = await Harness.wait(3) { self.window.firstResponder === self.root.wall.draftTiles[kept]?.editor.textView }
        typeIntoDraft(kept, Self.parkedText)
        Harness.press("\u{1b}", keyCode: 53, window: window)
        let parked = await Harness.wait(3) { self.model.draft(kept)?.parked == true && self.root.wall.draftTiles[kept]?.onShelf == true }
        check("esc keeps the draft as a quiet tile on the shelf", parked && model.editingDraftID == nil)
        let parkedSaved = await Harness.waitAsync(5) { await self.daemonDraft(kept)?.parked == true }
        check("the kept draft is saved (parked)", parkedSaved)
        Harness.press("n", keyCode: 0x2D, flags: .command, window: window)
        let empty = model.editingDraftID
        _ = await Harness.wait(2) { empty.map { self.window.firstResponder === self.root.wall.draftTiles[$0]?.editor.textView } ?? false }
        Harness.press("\u{1b}", keyCode: 53, window: window)
        let discarded = await Harness.wait(3) { empty.map { self.model.draft($0) == nil } ?? false }
        check("esc on an empty draft discards it", discarded)
        await Harness.sleep(0.5) // let the parked draft's last save land

        await runMachineFolder()

        finish(reattach)
    }

    /// 9b. Machine and folder agree (the live bug's three drafts, saved as
    /// an older app left them): an old machine:mini draft whose folder is
    /// only on this Mac comes here; a #token naming another folder than
    /// the stored project fixes the project; an explicit mini choice stays
    /// and the chip row says "This folder is on laptop, not on mini — Run
    /// on laptop" (fs.stat through hesperd), and ⌘↩ starts nothing; the
    /// chip row wraps on a narrow tile with the machine chip first.
    private func runMachineFolder() async {
        let base = (model.env.stateDir ?? NSTemporaryDirectory()) + "/mf"
        let apiDir = base + "/laptop-only-api", other = base + "/other-folder"
        for p in [apiDir, other] { try? FileManager.default.createDirectory(atPath: p, withIntermediateDirectories: true) }
        model.projectFolders.append(apiDir) // the # list (as if under the projects root)
        func save(_ fields: [String: JSONValue]) async {
            _ = try? await model.client.call("drafts.save", ["draft": .object(fields)])
        }
        // Saved by an older app: no machineExplicit.
        await save(["id": "d-mfold", "text": "old draft on the mini", "machine": "M", "project": .string(apiDir)])
        await save(["id": "d-mftoken", "text": "#laptop-only-api\nplease analyse the application", "project": .string(other)])
        let old = await Harness.wait(5) { self.model.draft("d-mfold")?.machine == nil && self.model.draft("d-mfold")?.project == apiDir }
        let oldSaved = await Harness.waitAsync(5) { await self.daemonDraft("d-mfold").map { $0.machine == nil && !$0.machineExplicit } ?? false }
        check("an old mini draft (no machineExplicit) with a folder only on this Mac comes to this Mac", old && oldSaved,
              "\(String(describing: model.draft("d-mfold")))")
        let token = await Harness.wait(5) { self.model.draft("d-mftoken")?.project == apiDir }
        let tokenSaved = await Harness.waitAsync(5) { await self.daemonDraft("d-mftoken")?.project == apiDir }
        check("a #token naming another folder than the stored project: the project follows the token", token && tokenSaved,
              model.draft("d-mftoken")?.project ?? "nil")
        // Removing the token on an edit: back to "Choose folder".
        if let tv = root.wall.draftTiles["d-mftoken"]?.editor.textView {
            model.editDraft("d-mftoken")
            window.makeFirstResponder(tv)
            tv.setSelectedRange(NSRange(location: 0, length: ("#laptop-only-api" as NSString).length))
            tv.insertText("", replacementRange: tv.selectedRange())
            let cleared = await Harness.wait(3) { self.model.draft("d-mftoken")?.project == nil && self.model.composer(for: "d-mftoken").needsFolder }
            check("removing the #token clears the project (Choose folder)", cleared, model.draft("d-mftoken")?.project ?? "nil")
            model.parkDraft("d-mftoken")
        } else {
            check("removing the #token clears the project (Choose folder)", false, "no tile")
        }
        // The explicit mini choice (fake mini: fs.stat says folders named
        // laptop-only aren't there).
        if !realDaemon, model.machine("M")?.online == true {
            await save(["id": "d-mfpick", "text": "picked the mini", "machine": "M", "machineExplicit": true, "project": .string(apiDir)])
            _ = await Harness.wait(5) { self.model.draft("d-mfpick") != nil }
            let c = model.composer(for: "d-mfpick")
            let note = await Harness.waitAsync(5) {
                _ = await c.checkTarget()
                return c.folderNote?.kind == .elsewhere
            }
            check("an explicit mini choice stays; the chip row says the folder is on laptop, not on mini", note && model.draft("d-mfpick")?.machine == "M"
                  && c.folderNote?.text == "This folder is on laptop, not on mini —", c.folderNote?.text ?? "no note")
            model.editDraft("d-mfpick")
            _ = await Harness.wait(3) { self.root.wall.draftTiles["d-mfpick"] != nil }
            shot("draft-folder-elsewhere")
            // ⌘↩: nothing starts, no raw "no directory" anywhere.
            let before = model.wall.count
            model.startDraft("d-mfpick")
            await Harness.sleep(1.0)
            check("⌘↩ with the folder not on the mini starts nothing and shows no raw error", model.wall.count == before && model.draft("d-mfpick") != nil
                  && c.error == nil && c.folderNote != nil && !c.busy, c.error ?? "")
            // The fake mini ignores spawn's `bring` (an older hesperd): it
            // gets today's note from now on.
            check("a hesperd without bring falls back to today's note (Run on laptop first)", !c.bringOffered && c.folderActions.first == .runHere,
                  "\(c.folderActions)")
            check("hesperd's \"no directory\" maps to the note", c.spawnFailed("no directory \(apiDir)") && c.error == nil)
            // A narrow tile: the chip row wraps, the machine chip first and
            // visible with the machine's name.
            if let tile = root.wall.draftTiles["d-mfpick"] {
                let keep = tile.frame
                tile.frame = NSRect(x: keep.minX, y: keep.minY, width: 260, height: max(keep.height, 260))
                tile.layoutSubtreeIfNeeded()
                await Harness.sleep(0.4)
                tile.layoutSubtreeIfNeeded()
                let f = tile.chipFrames[.machine] ?? .zero
                let row = tile.chipRow.frame
                let label = ComposerCompletion.machineChip(model.machine("M"), short: "M", local: false)
                check("narrow tile: the chip row wraps and the machine chip stays visible (\"mini · 44 ms · relay\")",
                      f.width > 0 && f.minX >= 0 && f.maxX <= row.width + 0.5 && row.height > 30 && label == "mini · 44 ms · relay",
                      "machine \(f) row \(row) label \(label)")
                shot("draft-chips-narrow")
                tile.frame = keep
                tile.needsLayout = true
            }
            // Run on laptop: one click, this Mac, not chosen any more.
            c.runHere()
            await Harness.sleep(0.3)
            check("Run on laptop switches the machine (not explicit any more), the note goes", model.draft("d-mfpick")?.machine == nil
                  && model.draft("d-mfpick")?.machineExplicit == false && c.folderNote == nil)
            model.parkDraft("d-mfpick")
            removeTestDraft("d-mfpick")
        }
        removeTestDraft("d-mfold")
        removeTestDraft("d-mftoken")
        // For the restart: a draft whose #token names a folder the app
        // doesn't list yet (the next process does: the projects root).
        let restoreDir = (ProcessInfo.processInfo.environment["HESPER_PROJECT_ROOT"] ?? base) + "/laptop-only-restore"
        try? FileManager.default.createDirectory(atPath: restoreDir, withIntermediateDirectories: true)
        await save(["id": "d-mfrestore", "text": "#laptop-only-restore\nplease analyse the application", "machine": "M", "project": .string(other)])
        await Harness.sleep(0.5)
    }

    private func removeTestDraft(_ id: String) {
        if model.editingDraftID == id { model.parkDraft(id) }
        if model.draft(id) != nil { model.removeDraft(id) }
    }

    static let parkedText = "selftest draft kept for the restart"

    /// 8e. Drop to attach: a file dropped on a read-only tile makes it
    /// active and arrives as one bracketed paste (the fake TUI shows a
    /// paste as ⟦…⟧); PNG bytes dropped on the focus view are saved and
    /// their path pasted; a file dropped on a remote agent is uploaded
    /// first (fake mini); what can't be dropped is refused.
    private func runDrops() async {
        model.deactivateTile()
        if model.mode == .focus { model.exitFocus() }
        await Harness.sleep(0.4)
        let live = model.wall.filter { $0.isRunning && $0.machine == model.localMachine && !(self.root.wall.tiles[$0.id]?.onShelf ?? true) }
        guard let x = live.first, let t = root.wall.tiles[x.id] else { return check("drop: a live tile", false) }
        if let other = model.wall.first(where: { $0.id != x.id }) { model.select(other.id) }
        let base = URL(fileURLWithPath: model.env.stateDir ?? NSTemporaryDirectory()).appendingPathComponent("drop test")
        try? FileManager.default.createDirectory(at: base, withIntermediateDirectories: true)
        let file = base.appendingPathComponent("drop me (1).txt")
        try? Data("dropped".utf8).write(to: file)
        let center = { (v: NSView) in v.convert(NSPoint(x: v.bounds.midX, y: v.bounds.midY), to: nil) }

        // Unsupported: refused with a message, nothing pasted.
        let odd = NSPasteboard(name: NSPasteboard.Name("hesper.selftest.odd.\(UUID().uuidString)"))
        odd.clearContents()
        odd.setData(Data([1, 2, 3]), forType: NSPasteboard.PasteboardType("com.example.unknown"))
        let oddDrag = FakeDrag(pasteboard: odd, window: window, location: center(t.terminal.host))
        _ = t.draggingEntered(oddDrag)
        let refused = t.terminalDrop.overlay.isHidden == false && t.terminalDrop.overlay.text.contains("Only files")
        t.draggingExited(oddDrag)
        check("drop: an unsupported payload is refused with a message", refused && t.terminalDrop.overlay.isHidden, t.terminalDrop.overlay.text)

        // A file on a read-only (not active) tile.
        let drag = FakeDrag(pasteboard: FakeDrag.pasteboard([file as NSURL]), window: window, location: center(t.terminal.host))
        let dest = drag.destination()
        let op = dest?.draggingEntered(drag) ?? []
        let label = t.terminalDrop.overlay.isHidden ? "" : t.terminalDrop.overlay.text
        shot("drop-hover")
        check("drop: the tile highlights \"Drop to attach to <name>\"", (dest === t || dest?.isDescendant(of: t) == true) && op == .copy && label.contains("Drop to attach to \(x.name)"),
              "dest=\(String(describing: dest.map { type(of: $0) })) op=\(op.rawValue) label=\(label)")
        let performed = dest?.performDragOperation(drag) ?? false
        let escaped = ShellEscape.quote(file.path)
        let arrived = await Harness.wait(6) { self.screen(x.id).contains("drop\\ me\\ \\(1\\).txt⟧") }
        check("drop: a file on a read-only tile makes it active and arrives as one bracketed paste", performed && model.activeTileID == x.id && arrived
              && model.lastDrop?.pastes == [escaped],
              "active=\(model.activeTileID ?? "nil") pastes=\(model.lastDrop?.pastes ?? []) [\(screen(x.id).split(separator: "\n").last ?? "")]")
        check("drop: the highlight goes away", t.terminalDrop.overlay.isHidden)

        // PNG bytes on the focus view (another agent).
        let y = live.dropFirst().first ?? x
        model.deactivateTile()
        model.focus(y.id)
        _ = await Harness.wait(10) { self.root.focus.terminal?.surface?.readScreen()?.contains("fake agent") ?? false }
        let rep = NSBitmapImageRep(bitmapDataPlanes: nil, pixelsWide: 4, pixelsHigh: 4, bitsPerSample: 8, samplesPerPixel: 4, hasAlpha: true,
                                   isPlanar: false, colorSpaceName: .deviceRGB, bytesPerRow: 0, bitsPerPixel: 0)
        let png = rep?.representation(using: .png, properties: [:]) ?? Data()
        let item = NSPasteboardItem()
        item.setData(png, forType: .png)
        await Harness.sleep(0.6) // the zoom into focus settles
        let pdrag = FakeDrag(pasteboard: FakeDrag.pasteboard([item]), window: window, location: center(root.focus.terminal?.host ?? root.focus))
        let pdest = pdrag.destination()
        _ = pdest?.draggingEntered(pdrag)
        let pdone = pdest?.performDragOperation(pdrag) ?? false
        let pasted = await Harness.wait(6) { self.root.focus.terminal?.surface?.readScreen()?.contains("image.png⟧") ?? false }
        let saved = model.lastDrop?.pastes.first.map { $0.hasSuffix("-image.png") && FileManager.default.fileExists(atPath: $0) && $0.contains(AttachmentNaming.folder(y.id)) } ?? false
        check("drop: PNG data on the focus view is saved and its path pasted", pdone && pasted && saved && model.focusedID == y.id,
              "chain=\(pdrag.chain()) dest=\(String(describing: pdest.map { type(of: $0) })) done=\(pdone) types=\(pdrag.pasteboard.types ?? []) focus=\(model.focusedID ?? "nil") pastes=\(model.lastDrop?.pastes ?? []) [\(root.focus.terminal?.surface?.readScreen()?.split(separator: "\n").last ?? "")]")
        model.exitFocus()
        await Harness.sleep(0.4)

        // A remote agent (fake mini): uploaded first, the path there pasted.
        if !realDaemon, let m = model.wall.first(where: { $0.machine == "M" && $0.isRunning }), let mt = root.wall.tiles[m.id], !mt.onShelf {
            let rdrag = FakeDrag(pasteboard: FakeDrag.pasteboard([file as NSURL]), window: window, location: center(mt.terminal.host))
            let rdest = rdrag.destination()
            _ = rdest?.draggingEntered(rdrag)
            let rlabel = mt.terminalDrop.overlay.text
            _ = rdest?.performDragOperation(rdrag)
            let up = await Harness.wait(8) { self.model.lastDrop?.id == m.id && self.screen(m.id).contains(".txt⟧") }
            let remotePath = model.lastDrop?.pastes.first ?? ""
            check("drop: a remote agent gets the file uploaded first, then that path", up && remotePath.contains("remote-M") && rlabel.contains("uploads to"),
                  "label=\(rlabel) pastes=\(model.lastDrop?.pastes ?? [])")
        }
        model.deactivateTile()
        await Harness.sleep(0.3)
    }

    /// A plain click on a tile (as AppKit delivers it).
    private func click(_ t: TileView, count: Int = 1) {
        guard let e = NSEvent.mouseEvent(with: .leftMouseDown, location: NSPoint(x: 10, y: 10), modifierFlags: [],
                                         timestamp: ProcessInfo.processInfo.systemUptime, windowNumber: window.windowNumber,
                                         context: nil, eventNumber: 0, clickCount: count, pressure: 1) else { return }
        t.mouseDown(with: e)
    }

    /// `--selftest-shots DIR`: the window as a PNG.
    private var shots: String? { model.env.values["selftest-shots"] }
    private func shot(_ name: String) {
        guard let dir = shots else { return }
        let p = Process()
        p.executableURL = URL(fileURLWithPath: "/usr/sbin/screencapture")
        p.arguments = ["-x", "-o", "-l", "\(window.windowNumber)", "\(dir)/\(name).png"]
        try? p.run()
        p.waitUntilExit()
    }

    private func typeIntoDraft(_ id: String, _ text: String) {
        guard let tv = root.wall.draftTiles[id]?.editor.textView else { return }
        window.makeFirstResponder(tv)
        tv.insertText(text, replacementRange: tv.selectedRange())
    }

    private func daemonDraft(_ id: String) async -> Draft? {
        guard let v = try? await model.client.call("drafts.list"), let list = try? v.decode([Draft].self) else { return nil }
        return list.first { $0.id == id }
    }

    /// Phase 2 (`--selftest-phase restore`, a new app process; with the real
    /// hesperd also a restarted daemon): the kept draft is back, and starts
    /// in place.
    private func runRestore() async {
        _ = await Harness.wait(15) { self.model.isConnected }
        let back = await Harness.wait(10) { self.model.drafts.all.contains { $0.text == Self.parkedText } }
        check("restart restores the kept draft", back, model.drafts.all.map(\.text).joined(separator: " | "))
        guard let d = model.drafts.all.first(where: { $0.text == Self.parkedText }) else { return finish(0) }
        let shown = await Harness.wait(10) { self.root.wall.draftTiles[d.id]?.onShelf == true }
        check("…as a quiet tile on the shelf", shown)
        check("…still without a project, in the New area (ungrouped)", d.project == nil && model.isNewDraft(d.id))
        // The draft whose #token names a folder the last process didn't
        // list: restored with the token's folder as its project, on this Mac.
        let restoreDir = (ProcessInfo.processInfo.environment["HESPER_PROJECT_ROOT"] ?? "-") + "/laptop-only-restore"
        let fixed = await Harness.wait(10) { self.model.draft("d-mfrestore")?.project == restoreDir && self.model.draft("d-mfrestore")?.machine == nil }
        let fixedSaved = await Harness.waitAsync(5) { await self.daemonDraft("d-mfrestore")?.project == restoreDir }
        check("restore: a #token naming another folder than the stored project fixes the project", fixed && fixedSaved,
              "\(String(describing: model.draft("d-mfrestore")))")
        if model.draft("d-mfrestore") != nil { model.removeDraft("d-mfrestore") }
        // Its folder now: an existing one (the project follows silently).
        let folder = model.wall.first?.project ?? model.env.stateDir ?? NSTemporaryDirectory()
        let existed = FileManager.default.fileExists(atPath: folder)
        model.composer(for: d.id).choose(.project, value: folder)
        check("choosing a folder: the project follows, no warning", model.composer(for: d.id).error == nil && model.draft(d.id)?.project == folder)
        model.select(d.id)
        window.makeFirstResponder(root.wall)
        Harness.press("\r", keyCode: 36, window: window)
        let editing = await Harness.wait(3) { self.model.editingDraftID == d.id && self.window.firstResponder === self.root.wall.draftTiles[d.id]?.editor.textView }
        check("⏎ on a kept draft edits it", editing)
        await Harness.sleep(0.8)
        let frame = root.wall.draftTiles[d.id]?.frame ?? .zero
        let slot = model.wallItems.firstIndex { $0.id == d.id }
        Harness.press("\r", keyCode: 36, flags: .command, window: window)
        let started = await Harness.wait(10) { self.model.wall.contains { $0.task == Self.parkedText } }
        let id = model.wall.first { $0.task == Self.parkedText }?.id ?? ""
        await Harness.sleep(0.6)
        let f = root.wall.tiles[id]?.frame ?? .zero
        let same = abs(f.minX - frame.minX) < 1 && abs(f.minY - frame.minY) < 1 && abs(f.width - frame.width) < 1 && abs(f.height - frame.height) < 1
        check("the restored draft starts in place", started && same && model.wallItems.firstIndex { $0.id == id } == slot, "\(frame) → \(f)")
        if !existed, (try? FileManager.default.contentsOfDirectory(atPath: folder))?.isEmpty == true { try? FileManager.default.removeItem(atPath: folder) }
        finish(0)
    }

    /// The fake TUI's spinner row, a few rows above its prompt.
    static let tileMark = "esc to interrupt"

    /// nil when the tile's terminal area is exactly its engine grid (±1 px)
    /// with the rows the layout gave it; else what is off.
    private func tileIsExact(_ id: String) -> String? {
        guard let t = root.wall.tiles[id], let p = t.placed else { return "\(id): not placed" }
        guard let s = t.terminal.surface, let m = s.metrics else { return "\(id): no surface" }
        let px = t.terminal.host.convertToBacking(t.terminal.host.bounds.size)
        let gridH = m.rows * m.cellHeightPx
        if m.rows != p.rows || abs(Int(px.height.rounded()) - gridH) > 1 || abs(s.fontSize - p.font) > 0.001 {
            return "\(id): area \(Int(px.height))px grid \(m.rows)x\(m.cellHeightPx)=\(gridH)px layout rows \(p.rows) font \(s.fontSize)/\(p.font)"
        }
        return nil
    }

    private func finish(_ reattach: Double) {
        let failed = checks.filter { !$0.ok }.count
        Harness.write(Result(passed: checks.count - failed, failed: failed, checks: checks, reattachMs: reattach), to: out)
        exit(failed == 0 ? 0 : 1)
    }
}
