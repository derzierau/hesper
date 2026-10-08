import AppKit
import HesperCore

/// Projects (views) UI test (`make -C app test-ui-projects`,
/// Tools/run-projects.sh): the real app against the fake daemon seeded with
/// `--projects demo` (two groups, a repository with a package, a scratch
/// folder, 10 agents). Drives bands, collapse, ＋ in a band, the sidebar
/// (click, ⌥-click), the home wall's needs-you strip, band navigation,
/// layouts per arrangement, agent window titles, persistence; screenshots
/// with `--projects-shots DIR`.
@MainActor
final class ProjectsSelfTest {
    let manager: WindowManager
    let out: String
    let shots: String?
    var checks: [SelfTest.Check] = []
    private var shotFiles: [String] = []
    private var model: AppModel { manager.primary }
    private var main: WallEntry { manager.walls[0] }
    private var wall: WallView { main.root.wall }

    struct Result: Encodable { var passed: Int; var failed: Int; var checks: [SelfTest.Check]; var shots: [String] }

    init(manager: WindowManager, out: String, env: AppEnvironment) {
        self.manager = manager
        self.out = out
        shots = env.values["projects-shots"]
        Task { await run() }
    }

    private func check(_ name: String, _ ok: Bool, _ detail: String = "") {
        checks.append(SelfTest.Check(name: name, ok: ok, detail: detail))
        print(ok ? "ok  " : "FAIL", name, detail)
    }

    private func setState(_ id: String, _ state: String, attention: JSONValue = .null) async {
        _ = try? await model.client.call("fake.setState", .object(["id": .string(id), "state": .string(state), "attention": attention]))
    }

    private func approval(_ id: String) async {
        await setState(id, "approval", attention: ["kind": "approval", "title": "Bash", "detail": "git push", "options": ["allow", "always", "deny"]])
    }

    private func mouse(_ type: NSEvent.EventType, _ v: NSView, _ flags: NSEvent.ModifierFlags = []) -> NSEvent? {
        let p = v.convert(NSPoint(x: min(40, v.bounds.width / 2), y: v.bounds.midY), to: nil)
        return NSEvent.mouseEvent(with: type, location: p, modifierFlags: flags, timestamp: ProcessInfo.processInfo.systemUptime,
                                  windowNumber: v.window?.windowNumber ?? 0, context: nil, eventNumber: 0, clickCount: 1, pressure: 1)
    }

    /// A click as AppKit delivers it: down, then up, on the view.
    private func click(_ v: NSView, _ flags: NSEvent.ModifierFlags = []) {
        if let d = mouse(.leftMouseDown, v, flags) { v.mouseDown(with: d) }
        if let u = mouse(.leftMouseUp, v, flags) { v.mouseUp(with: u) }
    }

    private func makeKey(_ w: NSWindow?) async {
        guard let w else { return }
        for _ in 0..<3 {
            NSApp.activate(ignoringOtherApps: true)
            w.makeKeyAndOrderFront(nil)
            if await Harness.wait(1, { w.isKeyWindow }) { return }
        }
    }

    private func frames() -> [String: NSRect] {
        var out: [String: NSRect] = [:]
        for (id, t) in wall.tiles { out[id] = t.frame }
        return out
    }

    private func band(_ title: String) -> Band? { wall.view.bands.first { $0.title == title } }

    private func settle() async { await Harness.sleep(2.5) } // refits re-attach (debounced): let the terminals draw

    private func run() async {
        let connected = await Harness.wait(20) { self.model.isConnected && self.model.wall.count >= 10 && self.wall.view.showsBands }
        check("connects; 10 agents in bands", connected, "\(model.wall.count) agents, \(wall.view.bands.map(\.title))")
        guard connected else { return finish() }
        await makeKey(main.window)
        _ = await Harness.wait(20) { self.wall.tiles.values.allSatisfy { $0.onShelf || ($0.terminal.surface?.readScreen()?.contains("fake agent") ?? false) } }
        await settle()

        // 1. Bands render: one per group, then Scratch (collapsed until
        // opened); headers and frames.
        let titles = wall.view.bands.map(\.title)
        check("All groups by group: acme apps, tools, Scratch", titles == ["acme apps", "tools", "Scratch"], titles.joined(separator: ", "))
        check("the Scratch band starts collapsed", model.isCollapsed(key: "g:~scratch") && model.collapsedBands.isEmpty)
        model.toggleCollapse("g:~scratch")
        await Harness.sleep(0.6)
        check("a click opens it", !model.isCollapsed(key: "g:~scratch") && model.collapsedBands.isEmpty)
        let headersOK = wall.layoutResult.bands.count == 3 && wall.layoutResult.bands.allSatisfy { f in
            guard let h = self.wall.bandHeaders[f.key] else { return false }
            return !h.isHidden && h.frame.height > 20 && f.style == .band
        }
        check("each band has a header and a full-width frame (laptop: bands)", headersOK,
              wall.layoutResult.bands.map { "\($0.key) \($0.style) \(Int($0.frame.width))" }.joined(separator: "; "))
        let inside = wall.view.bands.allSatisfy { b in
            guard let f = self.wall.layoutResult.bands.first(where: { $0.key == b.key })?.frame else { return false }
            return b.members.allSatisfy { id in
                guard let c = (self.wall.tiles[id] ?? nil)?.frame else { return true }
                return c.minY >= f.y - 1 && c.maxY <= f.maxY + 1
            }
        }
        check("cards sit inside their band", inside)
        shot(main.window, "laptop-bands")

        // 2. Attention never moves anything.
        let acme = band("acme apps")!, tools = band("tools")!
        let before = frames()
        await approval(acme.members[1])
        _ = await Harness.wait(2) { self.model.agent(acme.members[1])?.state == .approval }
        await Harness.sleep(0.8)
        check("an approval re-lays out nothing", frames() == before)
        await setState(acme.members[1], "working")
        await Harness.sleep(0.5)

        // 3. ⌥↓ / ⌥↑ jump between bands; Tab follows group order.
        model.select(acme.members[0])
        main.window.makeFirstResponder(wall)
        Harness.press("", keyCode: 125, flags: .option, window: main.window)
        check("⌥↓ goes to the next band's first card", model.selectedID == tools.members.first, model.selectedID ?? "nil")
        Harness.press("", keyCode: 126, flags: .option, window: main.window)
        check("⌥↑ back to the previous band", model.selectedID == acme.members.first, model.selectedID ?? "nil")
        model.select(acme.members.last!)
        Harness.press("\t", keyCode: 48, window: main.window)
        check("Tab after a band's last card goes to the next band", model.selectedID == tools.members.first, model.selectedID ?? "nil")

        // 4. Collapse: a click on the header; ⌥-click collapses the others.
        if let h = wall.bandHeaders[tools.key] {
            click(h)
            await Harness.sleep(0.6)
            let f = wall.layoutResult.bands.first { $0.key == tools.key }
            check("a click collapses a band to one header line",
                  model.collapsedBands == [tools.key] && f?.collapsed == true && f?.frame.height == Metrics.wall.bandHeader
                    && tools.members.allSatisfy { self.wall.tiles[$0]?.isHidden == true && self.wall.tiles[$0]?.terminal.visible == false },
                  "\(model.collapsedBands)")
            check("Tab skips a collapsed band", { () -> Bool in
                self.model.select(acme.members.last!)
                Harness.press("\t", keyCode: 48, window: self.main.window)
                return !tools.members.contains(self.model.selectedID ?? "")
            }())
            await approval(tools.members[0])
            let badge = await Harness.wait(3) { h.needsYou == 1 && self.model.homeNeedsYou.contains { $0.id == tools.members[0] } }
            check("a collapsed band shows the attention badge; the home strip lists the agent", badge && !wall.strip.isHidden)
            await Harness.sleep(0.6)
            shot(main.window, "collapsed-bands")
            click(h)
            await Harness.sleep(0.4)
            check("a second click expands it", model.collapsedBands.isEmpty)
            await setState(tools.members[0], "working")
        }
        if let h = wall.bandHeaders[acme.key] {
            click(h, .option)
            await Harness.sleep(0.4)
            check("⌥-click collapses every other band", model.collapsedBands == Set([tools.key, "g:~scratch"]), "\(model.collapsedBands)")
            await Harness.sleep(0.6)
            shot(main.window, "collapsed-others")
            model.collapsedBands = []
            await Harness.sleep(0.6)
        }

        // 5. ＋ in a band: a draft there with the band's project.
        if let h = wall.bandHeaders[tools.key] {
            h.plus.performClick(nil)
            let made = await Harness.wait(2) { self.model.editingDraftID != nil }
            let id = model.editingDraftID ?? ""
            let d = model.draft(id)
            check("＋ in a band opens a draft there, in its project", made && d?.project == "/tmp/fake/projects/hesper"
                  && wall.view.band(of: id)?.key == tools.key, "\(d?.project ?? "nil") in \(wall.view.band(of: id)?.key ?? "nil")")
            // #project changed while writing: it stays, and moves on start.
            if var d {
                d.project = "/tmp/fake/projects/design-system"
                d.text = "move me to edition"
                model.editDraftContent(d)
                await Harness.sleep(0.3)
                check("a changed #project keeps the draft in its band while written", wall.view.band(of: id)?.key == tools.key)
                model.composer(for: id).choose(.project, value: "/tmp/fake/projects/design-system")
                model.startDraft(id)
                let started = await Harness.wait(5) { self.model.wall.contains { $0.task == "move me to edition" } }
                let a = model.wall.first { $0.task == "move me to edition" }
                _ = await Harness.wait(2) { a.map { self.wall.view.band(of: $0.id)?.key == acme.key } ?? false }
                check("on start the agent moves to its project's band", started && a.map { wall.view.band(of: $0.id)?.key == acme.key } == true,
                      a.map { wall.view.band(of: $0.id)?.key ?? "nil" } ?? "no agent")
            }
        }

        await newDrafts(acme: acme, tools: tools)

        // 6. Sidebar: ⌘0, click scopes, ⌥-click a new wall.
        Harness.press("0", keyCode: 29, flags: .command, window: main.window)
        let side = await Harness.wait(2) { !self.main.root.sidebar.isHidden && !self.main.root.sidebar.rows.isEmpty }
        check("⌘0 shows the project sidebar", side && model.sidebarVisible)
        await settle()
        let sidebar = main.root.sidebar
        // Active lists only projects with agents; groups open on their caret.
        sidebar.setGroup("g-acme", expanded: true)
        sidebar.setGroup("g-tools", expanded: true)
        let rowTitles = sidebar.rows.compactMap { $0.row?.title }
        check("sidebar: All agents, active groups → projects, scratch", rowTitles.first == "All agents"
              && ["acme apps", "acme-apps", "tools", "hesper", "spike-vt"].allSatisfy(rowTitles.contains), rowTitles.joined(separator: ", "))
        shot(main.window, "sidebar")
        if let row = sidebar.rows.first(where: { $0.row?.id == "g:g-acme/p:p-acme-apps" }) {
            click(row)
            let scoped = await Harness.wait(2) { self.main.scope == .project("p-acme-apps") }
            _ = await Harness.wait(2) { self.wall.view.level == .branch }
            let keys = wall.view.bands.map(\.key)
            check("a click on a project scopes this wall; bands by branch/package", scoped && model.wall.count == 4
                  && keys.contains("b:main") && keys.contains("b:pkg:p-ios-app") && keys.contains("b:br:feature/push-provider-fcm"),
                  "\(main.scope) \(model.wall.count) \(keys)")
        }
        // 7. Home wall: a scoped-out agent needing you shows as a card.
        let ghost = model.unscopedWall.first { model.catalog.projectID(for: $0) == "p-hesper" }!.id
        await Harness.sleep(0.5)
        let stripBefore = frames()
        await approval(ghost)
        let card = await Harness.wait(3) { self.model.homeNeedsYou.map(\.id) == [ghost] && !self.wall.strip.isHidden }
        check("home wall: a scoped-out agent needing approval appears as a card", card)
        check("the card resizes nothing (the strip row was reserved)", frames() == stripBefore)
        await settle()
        shot(main.window, "home-needs-you")
        model.openElsewhere?(ghost)
        let jumped = await Harness.wait(2) { self.model.mode == .focus && self.model.focusedID == ghost }
        check("clicking the card takes you there (⌘J routing)", jumped)
        model.exitFocus()
        await setState(ghost, "working")
        if let row = sidebar.rows.first(where: { $0.row?.id == "g:g-tools" }) {
            click(row, .option)
            let opened = await Harness.wait(3) { self.manager.walls.count == 2 }
            let w2 = manager.walls.last
            _ = await Harness.wait(5) { (w2?.model.wall.count ?? 0) == 3 }
            check("⌥-click on a group opens a new wall scoped to it", opened && w2?.scope == .group("g-tools") && w2?.model.wall.count == 3,
                  "\(manager.walls.count) walls, \(w2?.model.wall.count ?? -1)")
            // New agents per window: ⌘N in the group window makes the draft
            // there (a project of the group); closing the window moves it
            // to the main wall.
            var gd = ""
            if let w2 {
                await makeKey(w2.window)
                w2.model.perform(.newAgent)
                gd = w2.model.editingDraftID ?? ""
                let here = await Harness.wait(2) { w2.model.wallItems.contains { $0.id == gd } }
                let band = w2.model.draft(gd)?.band
                check("⌘N in a group window: the draft shows there, in one of the group's projects, not on the main wall",
                      here && w2.model.draft(gd)?.wall == w2.id && band.map { model.catalog.groupIDs(of: $0).contains("g-tools") } == true
                        && !model.wallItems.contains { $0.id == gd }, "band \(band ?? "nil") wall \(w2.model.draft(gd)?.wall ?? "nil")")
                if var d = w2.model.draft(gd) {
                    d.text = "kept when its window closes"
                    w2.model.editDraftContent(d)
                }
            }
            w2?.window.close()
            await Harness.sleep(0.4)
            if !gd.isEmpty {
                let moved = await Harness.wait(3) { self.model.wallItems.contains { $0.id == gd } && self.model.draft(gd)?.wall == nil }
                check("its window closed: the draft moves to the main wall, keeping its project", moved && model.draft(gd)?.band != nil
                      && model.draft(gd)?.text == "kept when its window closes")
                model.discardDraft(gd)
            }
        }
        if let all = sidebar.rows.first(where: { $0.row?.id == "all" }) { click(all) }
        model.sidebarVisible = false
        _ = await Harness.wait(2) { self.main.scope == .all }
        await settle()

        // 8. Overflow by group: groups stay whole across walls.
        main.window.setContentSize(NSSize(width: 1300, height: 760))
        await Harness.sleep(0.5)
        let ow = manager.newWall(scope: .overflow)
        _ = await Harness.wait(3) { !(ow.model.wall.isEmpty) }
        await Harness.sleep(0.5)
        let unit = { (a: Agent) in ViewResolver.unit(projectID: self.model.catalog.projectID(for: a), catalog: self.model.catalog) }
        let mainUnits = Set(model.wall.filter { !$0.state.needsAttention }.map(unit))
        let owUnits = Set(ow.model.wall.map(unit))
        check("Overflow moves whole groups", mainUnits.isDisjoint(with: owUnits) || !ow.model.continuedBands.isEmpty,
              "main \(mainUnits.sorted()) overflow \(owUnits.sorted()) cap \(main.capacity)")
        ow.window.close()
        main.window.setContentSize(NSSize(width: 1500, height: 950))
        await settle()

        // 9. Each layout groups its own way.
        model.arrangement = .columns
        await settle()
        check("B: one lane per band", wall.layoutResult.bands.count == 3 && wall.layoutResult.bands.allSatisfy { $0.style == .lane })
        shot(main.window, "lanes")
        model.arrangement = .treemap
        await settle()
        check("C: nested treemap", wall.layoutResult.bands.count == 3 && wall.layoutResult.bands.allSatisfy { $0.style == .nested })
        shot(main.window, "nested")
        model.arrangement = .mainStack
        await settle()
        check("D: no frames, project tint", wall.layoutResult.bands.isEmpty && wall.bandHeaders.values.allSatisfy(\.isHidden))
        shot(main.window, "main-stack")
        model.arrangement = .shelf
        model.grouping = .none
        await Harness.sleep(0.6)
        check("grouping None: a plain wall", !wall.view.showsBands && wall.layoutResult.bands.isEmpty)
        model.grouping = .auto
        await settle()

        // 10. Wide wall: side-by-side boxes.
        if let big = NSScreen.screens.max(by: { $0.visibleFrame.width < $1.visibleFrame.width }) {
            let vf = big.visibleFrame
            main.window.setFrame(NSRect(x: vf.minX, y: vf.minY, width: vf.width, height: min(vf.height, 1300)), display: true)
            await Harness.sleep(0.4)
            if wall.layoutResult.bands.first?.style != .box { model.minChars = 60 }
            await settle()
            check("a wide wall draws boxes side by side", wall.layoutResult.bands.contains { $0.style == .box },
                  "width \(Int(vf.width)) minChars \(model.minChars)")
            shot(main.window, "wide-boxes")
            model.minChars = 80
            main.window.setContentSize(NSSize(width: 1500, height: 950))
            main.window.center()
            await settle()
        }

        // 11. Agent windows: the agent's name as title, the project in the
        // subtitle, tab group per project.
        let first = acme.members[0]
        if let c = manager.openAgentWindow(first) {
            await Harness.sleep(0.5)
            check("agent window titled agent, subtitled acme-apps · …, tabbed per project",
                  c.window?.title == model.agent(first)?.name && (c.window?.subtitle.hasPrefix("acme-apps · ") ?? false)
                    && c.window?.tabbingIdentifier == "hesper.agent.p-acme-apps"
                    && c.projectColor == "#7aa2f7",
                  "\(c.window?.title ?? "") \(c.window?.tabbingIdentifier ?? "")")
            c.window?.close()
        }

        // 12. Header drag reorders the bands; a sidebar row dropped on a
        // display opens a wall there.
        if let h = wall.bandHeaders["g:~scratch"], let target = wall.layoutResult.bands.first(where: { $0.key == acme.key }) {
            let start = h.convert(NSPoint(x: 40, y: h.bounds.midY), to: nil)
            let end = wall.convert(NSPoint(x: target.frame.x + 60, y: target.frame.y + 40), to: nil)
            func ev(_ t: NSEvent.EventType, _ p: NSPoint) -> NSEvent? {
                NSEvent.mouseEvent(with: t, location: p, modifierFlags: [], timestamp: ProcessInfo.processInfo.systemUptime,
                                   windowNumber: main.window.windowNumber, context: nil, eventNumber: 0, clickCount: 1, pressure: 1)
            }
            if let e = ev(.leftMouseDown, start) { h.mouseDown(with: e) }
            for k in 1...6 {
                let p = NSPoint(x: start.x + (end.x - start.x) * Double(k) / 6, y: start.y + (end.y - start.y) * Double(k) / 6)
                if let e = ev(.leftMouseDragged, p) { h.mouseDragged(with: e) }
            }
            if let e = ev(.leftMouseUp, end) { h.mouseUp(with: e) }
            await Harness.sleep(0.6)
            check("dragging a header onto another band moves it there", wall.view.bands.first?.key == "g:~scratch" && model.collapsedBands.isEmpty,
                  wall.view.bands.map(\.key).joined(separator: ","))
        }
        let walls = manager.walls.count
        let away = NSPoint(x: main.window.frame.maxX + 400, y: main.window.frame.midY)
        manager.sidebar(.wallAt(.project("p-edition"), away), from: main)
        let dropped = await Harness.wait(3) { self.manager.walls.count == walls + 1 }
        check("a sidebar row dropped outside the window opens a wall there", dropped && manager.walls.last?.scope == .project("p-edition"))
        // ⌘N in a project window: the draft there, the project locked.
        if let pw = manager.walls.last, pw !== main, pw.scope == .project("p-edition") {
            await makeKey(pw.window)
            pw.model.perform(.newAgent)
            let pd = pw.model.editingDraftID ?? ""
            let here = await Harness.wait(2) { pw.model.wallItems.contains { $0.id == pd } }
            let d = pw.model.draft(pd)
            check("⌘N in a project window: the draft shows there with the project locked, not on the main wall",
                  here && d?.band == "p-edition" && d?.projectLocked == true && d?.project != nil && pw.model.composer(for: pd).projectLocked
                    && !model.wallItems.contains { $0.id == pd }, "band \(d?.band ?? "nil") locked \(d?.projectLocked ?? false) folder \(d?.project ?? "nil")")
            await Harness.sleep(0.6)
            shot(pw.window, "project-window-draft")
            pw.model.discardDraft(pd)
        }
        manager.walls.last.map { if $0 !== main { $0.window.close() } }
        await Harness.sleep(0.4)

        // 13. The view is saved with the wall.
        model.collapsedBands = ["g:~scratch"]
        model.bandOrder = [tools.key]
        await Harness.sleep(1.0)
        manager.restorer.saveNow()
        let saved = manager.restorer.load()?.walls.first
        check("windows.json keeps collapsed bands and band order", saved?.collapsed == ["g:~scratch"] && saved?.bandOrder == [tools.key])
        check("band order: the wall's own order first", wall.view.bands.first?.key == tools.key)
        finish()
    }

    /// 5b. New drafts have no project: ⌘N / the toolbar / the menu bar
    /// open one in the "New" area (never the selected card's band); a
    /// folder chosen there changes the project silently; on start the
    /// agent moves into its project's band. Explicit project actions (⌘N
    /// on a focused header, the sidebar's "New Agent in …") open in that
    /// band with its project.
    private func newDrafts(acme: Band, tools: Band) async {
        let edition = "/tmp/fake/projects/design-system"
        try? FileManager.default.createDirectory(atPath: edition, withIntermediateDirectories: true)
        await Harness.sleep(0.4)
        // ⌘N with a card of "tools" selected.
        model.select(tools.members[0])
        main.window.makeFirstResponder(wall)
        Harness.press("n", keyCode: 0x2D, flags: .command, window: main.window)
        let id = model.editingDraftID ?? ""
        _ = await Harness.wait(2) { self.wall.view.band(of: id) != nil }
        let first = wall.view.bands.first
        check("⌘N: a draft without a project, in the New area (not the selected card's band)",
              !id.isEmpty && model.draft(id)?.project == nil && model.draft(id)?.machine == nil && model.composer(for: id).needsFolder
                && first?.key == ViewResolver.newKey && first?.title == "New" && first?.members == [id] && first?.colorHex == ViewResolver.newColor,
              "project \(model.draft(id)?.project ?? "nil") band \(wall.view.band(of: id)?.key ?? "nil") first \(first?.key ?? "nil")")
        await Harness.sleep(0.6)
        let newFrame = wall.layoutResult.bands.first { $0.key == ViewResolver.newKey }
        let headerShown = wall.bandHeaders[ViewResolver.newKey].map { !$0.isHidden } ?? false
        check("the New area is a band of its own at the top", headerShown && newFrame.map { f in
            self.wall.layoutResult.bands.allSatisfy { $0.key == f.key || $0.frame.y > f.frame.y }
        } == true)
        shot(main.window, "new-draft")
        // ⌘↩ without a folder: nothing starts, the folder list opens; no warning.
        let agents = model.wall.count
        if let tv = wall.draftTiles[id]?.editor.textView {
            main.window.makeFirstResponder(tv)
            tv.insertText("new draft lands in edition", replacementRange: tv.selectedRange())
        }
        model.startDraft(id)
        await Harness.sleep(0.3)
        check("no folder: Start is disabled, ⌘↩ opens the folder chip", model.wall.count == agents && model.popover == .chip(id, .project)
              && model.composer(for: id).error == nil && model.toast == nil, "\(String(describing: model.popover))")
        model.closePopover()
        // Choosing a folder: the project follows, silently; the draft stays in New.
        model.composer(for: id).choose(.project, value: edition)
        await Harness.sleep(0.4)
        let c = model.composer(for: id)
        check("choosing the folder sets the project; no warning, no note", model.draft(id)?.project == edition && c.error == nil
              && c.missingFolder == nil && model.toast == nil && wall.view.band(of: id)?.key == ViewResolver.newKey)
        shot(main.window, "folder-change")
        model.startDraft(id)
        let started = await Harness.wait(5) { self.model.wall.contains { $0.task == "new draft lands in edition" } }
        let a = model.wall.first { $0.task == "new draft lands in edition" }
        _ = await Harness.wait(3) { a.map { self.wall.view.band(of: $0.id)?.key == acme.key } ?? false }
        check("on start it moves once into its project's band; New is gone", started && a.map { wall.view.band(of: $0.id)?.key == acme.key } == true
              && wall.view.band(key: ViewResolver.newKey) == nil && c.error == nil, a.map { wall.view.band(of: $0.id)?.key ?? "nil" } ?? "no agent")
        await Harness.sleep(0.6)

        // The toolbar's New Agent and the menu bar's: the same (no project).
        model.select(tools.members[0])
        model.perform(.newAgent)
        let tb = model.editingDraftID ?? ""
        check("the toolbar / menu bar New Agent: no project either", model.draft(tb)?.project == nil && model.isNewDraft(tb))
        model.discardDraft(tb)

        // ⌘N on a focused band header: in that band, with its project.
        if let h = wall.bandHeaders[tools.key] {
            click(h) // collapses and focuses the header
            await Harness.sleep(0.3)
            let focused = model.focusedBandKey == tools.key && h.focused
            main.window.makeFirstResponder(wall)
            Harness.press("n", keyCode: 0x2D, flags: .command, window: main.window)
            let hd = model.editingDraftID ?? ""
            _ = await Harness.wait(2) { self.wall.view.band(of: hd) != nil }
            check("⌘N on a focused band header: a draft in that band, in its project", focused && model.draft(hd)?.project == "/tmp/fake/projects/hesper"
                  && wall.view.band(of: hd)?.key == tools.key && !model.collapsedBands.contains(tools.key) && model.focusedBandKey == nil,
                  "focused \(focused) \(model.draft(hd)?.project ?? "nil") in \(wall.view.band(of: hd)?.key ?? "nil")")
            await Harness.sleep(1.0)
            shot(main.window, "band-plus-draft")
            model.discardDraft(hd)
        }
        // The sidebar's "New Agent in <project>".
        model.sidebarVisible = true
        _ = await Harness.wait(2) { !self.main.root.sidebar.rows.isEmpty }
        main.root.sidebar.setGroup("g-tools", expanded: true)
        if let row = main.root.sidebar.rows.compactMap(\.row).first(where: { $0.projectID == "p-hesper" }),
           let item = main.root.sidebar.menu(for: row)?.items.first(where: { $0.title == "New Agent in hesper" }), let act = item.action {
            NSApp.sendAction(act, to: item.target, from: item)
            let sd = model.editingDraftID ?? ""
            _ = await Harness.wait(2) { self.wall.view.band(of: sd) != nil }
            check("the sidebar's “New Agent in hesper”: in its band, with its project",
                  model.draft(sd)?.project == "/tmp/fake/projects/hesper" && wall.view.band(of: sd)?.key == tools.key)
            model.discardDraft(sd)
        } else {
            check("the sidebar has “New Agent in hesper”", false)
        }
        model.sidebarVisible = false
        model.select(tools.members[0])
        await settle()
    }

    private func shot(_ w: NSWindow, _ name: String) {
        guard let dir = shots else { return }
        let path = "\(dir)/\(name).png"
        let p = Process()
        p.executableURL = URL(fileURLWithPath: "/usr/sbin/screencapture")
        p.arguments = ["-x", "-o", "-l", "\(w.windowNumber)", path]
        try? p.run()
        p.waitUntilExit()
        shotFiles.append(path)
    }

    private func finish() {
        let failed = checks.filter { !$0.ok }.count
        Harness.write(Result(passed: checks.count - failed, failed: failed, checks: checks, shots: shotFiles), to: out)
        exit(failed == 0 ? 0 : 1)
    }
}
