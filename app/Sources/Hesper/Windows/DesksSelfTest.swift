import AppKit
import HesperCore

/// Desks UI test (`make -C app test-ui-desks`, Tools/run-desks.sh): the
/// real app against the fake daemon (`--projects demo`) with injected
/// display sets carved out of the real main display (`--fake-screens`,
/// DeskController.inject). Phase 1: windows.json migrated into "Laptop
/// only"; pointers to a project's own wall (tiles gone, minimize brings
/// the band back, a click brings the wall forward, the per-wall setting);
/// plugging in "office" folds to defaults; an office desk is arranged
/// (group wall made home, agent window, collapsed band, sidebar); laptop
/// and office switch back and forth restoring walls, scopes, views and
/// agent windows; order-independence; no switch during a drag; Save Desk
/// As and the ⌃⌘D picker; automatic switching off. Phase 2 (relaunch on
/// the office displays): the office desk comes back. Screenshots with
/// `--desks-shots DIR`.
@MainActor
final class DesksSelfTest {
    let manager: WindowManager
    let out: String
    let phase: Int
    let shots: String?
    var checks: [SelfTest.Check] = []
    private var shotFiles: [String] = []
    private var model: AppModel { manager.primary }
    private var desks: DeskController { manager.desks }
    private var mainWall: WallEntry { manager.mainWall }

    struct Result: Encodable { var phase: Int; var passed: Int; var failed: Int; var checks: [SelfTest.Check]; var shots: [String] }

    init(manager: WindowManager, out: String, env: AppEnvironment) {
        self.manager = manager
        self.out = out
        phase = Int(env.values["desks-phase"] ?? "1") ?? 1
        shots = env.values["desks-shots"]
        Task { phase == 1 ? await runPhase1() : await runPhase2() }
    }

    private func check(_ name: String, _ ok: Bool, _ detail: String = "") {
        checks.append(SelfTest.Check(name: name, ok: ok, detail: detail))
        print(ok ? "ok  " : "FAIL", name, detail)
    }

    private func setState(_ id: String, _ state: String, attention: JSONValue = .null) async {
        _ = try? await model.client.call("fake.setState", .object(["id": .string(id), "state": .string(state), "attention": attention]))
    }

    private func makeKey(_ w: NSWindow?) async {
        guard let w else { return }
        for _ in 0..<3 {
            NSApp.activate(ignoringOtherApps: true)
            w.makeKeyAndOrderFront(nil)
            if await Harness.wait(1, { w.isKeyWindow }) { return }
        }
    }

    private func click(_ v: NSView) {
        func ev(_ t: NSEvent.EventType) -> NSEvent? {
            let p = v.convert(NSPoint(x: min(40, v.bounds.width / 2), y: v.bounds.midY), to: nil)
            return NSEvent.mouseEvent(with: t, location: p, modifierFlags: [], timestamp: ProcessInfo.processInfo.systemUptime,
                                      windowNumber: v.window?.windowNumber ?? 0, context: nil, eventNumber: 0, clickCount: 1, pressure: 1)
        }
        if let d = ev(.leftMouseDown) { v.mouseDown(with: d) }
        if let u = ev(.leftMouseUp) { v.mouseUp(with: u) }
    }

    private func project(of id: String) -> String? { model.catalog.root(model.catalog.projectID(for: model.agent(id)!)) }
    private func agents(in p: String) -> [String] { model.unscopedWall.filter { model.catalog.lineage(model.catalog.projectID(for: $0)).contains(p) }.map(\.id) }

    /// Switches desks by injecting displays and waiting for the switch.
    @discardableResult
    private func plug(_ name: String, expectSwitch: Bool = true) async -> Bool {
        let n = desks.switches
        desks.inject(DeskController.fakeDisplays(name))
        if !expectSwitch {
            await Harness.sleep(1.8)
            return desks.switches == n
        }
        let ok = await Harness.wait(6) { self.desks.switches == n + 1 }
        await Harness.sleep(1.2) // animations, agent windows, the save after
        return ok
    }

    private func area(_ i: Int) -> NSRect {
        let a = desks.areas[i].visible
        return NSRect(x: a.x, y: a.y, width: a.width, height: a.height)
    }

    private func close(_ a: NSRect, _ b: NSRect) -> Bool {
        abs(a.minX - b.minX) < 3 && abs(a.minY - b.minY) < 3 && abs(a.width - b.width) < 3 && abs(a.height - b.height) < 3
    }

    // MARK: Phase 1

    private func runPhase1() async {
        let connected = await Harness.wait(20) { self.model.isConnected && self.model.wall.count >= 10 && self.mainWall.root.wall.view.showsBands }
        check("connects; 10 agents in bands", connected, "\(model.wall.count)")
        guard connected else { return finish() }
        await makeKey(mainWall.window)
        await Harness.sleep(1.5)

        // 1. windows.json (v1) became this setup's desk.
        let laptopSetup = desks.active
        check("windows.json migrated into the current setup's automatic desk",
              desks.currentDesk?.automatic == true && desks.currentDesk?.name == "Laptop only" && mainWall.model.arrangement == .columns
                  && mainWall.model.sidebarVisible,
              "\(desks.currentDesk?.name ?? "nil") \(mainWall.model.arrangement) sidebar \(mainWall.model.sidebarVisible)")
        mainWall.model.arrangement = .shelf
        mainWall.model.sidebarVisible = false
        await Harness.sleep(1)
        manager.restorer.saveNow()
        if let url = desks.url, let d = try? Data(contentsOf: url), let s = String(data: d, encoding: .utf8) {
            check("desks.json written as version 2", s.contains("\"version\" : 2") && s.contains("\"setups\""))
        }

        // 2. Pointers: a project's own wall replaces its band on the main wall.
        let asApps = agents(in: "p-acme-apps")
        let pw = manager.newWall(scope: .project("p-acme-apps"), frame: area(0).insetBy(dx: 60, dy: 60))
        let pointerKey = ViewResolver.pointerKey(pw.id)
        let wall = mainWall.root.wall
        let pointed = await Harness.wait(4) { wall.view.bands.contains { $0.key == pointerKey } }
        check("a visible project wall: the main wall shows a pointer line", pointed, wall.view.bands.map(\.key).joined(separator: ","))
        check("…and no tiles (no attaches) of that project there",
              Set(mainWall.model.wall.map(\.id)).isDisjoint(with: asApps) && Set(wall.tiles.keys).isDisjoint(with: asApps),
              "\(asApps)")
        if let b = wall.view.bands.first(where: { $0.key == pointerKey }) {
            check("the pointer: title, “in … window”, counts", b.title == "acme-apps" && b.subtitle.contains(pw.window.title) && b.counted.count == asApps.count,
                  "\(b.title) · \(b.subtitle) · \(b.counted.count)")
        }
        await makeKey(mainWall.window)
        await Harness.sleep(1.5)
        shot(mainWall.window, "pointer")
        // A click brings the project wall forward.
        if let h = wall.bandHeaders[pointerKey] {
            click(h)
            let front = await Harness.wait(3) { pw.window.isKeyWindow || NSApp.orderedWindows.first === pw.window }
            check("a click on the pointer brings its wall forward", front)
        } else {
            check("a click on the pointer brings its wall forward", false, "no header")
        }
        // Attention stays on the pointer and the home strip.
        if let first = asApps.first {
            await setState(first, "approval", attention: ["kind": "approval", "title": "Bash", "detail": "git push", "options": ["allow", "deny"]])
            let ringed = await Harness.wait(3) { (wall.bandHeaders[pointerKey]?.needsYou ?? 0) == 1 && self.mainWall.model.homeNeedsYou.contains { $0.id == first } }
            check("an approval in it: the pointer carries the ring and the home strip the card", ringed)
            check("…and the main wall gets no tile for it", wall.tiles[first] == nil)
            await setState(first, "working")
        }
        // Minimized: the band comes back; restored: the pointer again.
        pw.window.miniaturize(nil)
        let back = await Harness.wait(5) { !wall.view.bands.contains { $0.key == pointerKey } && asApps.allSatisfy { wall.tiles[$0] != nil } }
        check("project wall minimized: the full band returns", back, wall.view.bands.map(\.key).joined(separator: ","))
        pw.window.deminiaturize(nil)
        let again = await Harness.wait(5) { wall.view.bands.contains { $0.key == pointerKey } && Set(wall.tiles.keys).isDisjoint(with: asApps) }
        check("restored: the pointer again", again)
        // The per-wall setting.
        mainWall.model.ownWallMode = .full
        let full = await Harness.wait(3) { asApps.allSatisfy { wall.tiles[$0] != nil } && !wall.view.bands.contains { $0.key == pointerKey } }
        mainWall.model.ownWallMode = .hidden
        let hidden = await Harness.wait(3) { Set(wall.tiles.keys).isDisjoint(with: asApps) && !wall.view.bands.contains { $0.key == pointerKey } }
        mainWall.model.ownWallMode = .collapsed
        let col = await Harness.wait(3) { wall.view.bands.contains { $0.key == pointerKey } }
        check("own walls setting: full / hidden / collapsed", full && hidden && col, "\(full) \(hidden) \(col)")
        check("the layout popover offers it", mainWall.model.popoverContent(.layout).items.contains { $0.id == "own:hidden" })
        // An agent window (⇧-click) is not a pointer.
        let solo = model.unscopedWall.first { !asApps.contains($0.id) }!.id
        manager.openAgentWindow(solo)
        await Harness.sleep(0.6)
        check("a single-agent window keeps its tile on the wall", wall.tiles[solo] != nil)
        manager.agentWindow(for: solo)?.window?.close()
        pw.window.close()
        let gone = await Harness.wait(4) { asApps.allSatisfy { wall.tiles[$0] != nil } }
        check("project wall closed: the band is back", gone)

        // 3. The laptop desk: main wall All, one agent window.
        let a = model.unscopedWall.first { project(of: $0.id) == "p-hesper" }!.id
        let b = model.unscopedWall.first { project(of: $0.id) == "p-edition" }!.id
        manager.openAgentWindow(a, frame: area(0).insetBy(dx: 200, dy: 120))
        _ = await Harness.wait(3) { self.manager.agentWindow(for: a) != nil }
        await Harness.sleep(0.8)
        manager.restorer.saveNow()
        let laptopDesk = desks.currentDesk
        check("laptop desk saved: 1 wall, agent window \(a)", laptopDesk?.windows.walls.count == 1 && laptopDesk?.windows.agentWindows.map(\.agent) == [a])

        // 4. Plug in the Studio Display: no desk yet → fold to defaults.
        let switched = await plug("office")
        let office = desks.active
        check("plugging in a display switches the desk (1 s debounce)", switched && office.id != laptopSetup.id, desks.lastSwitch)
        check("no desk for it: one All wall on the main display, agent windows kept",
              manager.walls.count == 1 && mainWall.scope == .all && close(mainWall.window.frame, area(0)) && manager.agentWindow(for: a) != nil,
              "\(manager.walls.count) \(mainWall.scope) \(mainWall.window.frame) vs \(area(0))")
        check("the new setup's desk is named from its displays", desks.currentDesk?.name == "Laptop + Studio Display", desks.currentDesk?.name ?? "nil")

        // 5. Arrange the office: a group wall on the Studio Display made home,
        // an agent window there, a collapsed band and the sidebar on the main wall.
        let studio = area(1)
        let gw = manager.newWall(scope: .group("g-acme"), frame: studio.insetBy(dx: 30, dy: 30))
        _ = await Harness.wait(4) { !gw.model.wall.isEmpty }
        manager.makeHome(gw)
        mainWall.model.collapsedBands = ["g:g-tools"]
        mainWall.model.sidebarVisible = true
        manager.agentWindow(for: a)?.window?.close()
        manager.openAgentWindow(b, frame: NSRect(x: studio.minX + 80, y: studio.minY + 40, width: 700, height: 420))
        await Harness.sleep(1.2)
        check("Make Home Wall: the group wall is first, with the strip row", manager.walls.first === gw && gw.model.homeStripReserved && !mainWall.model.homeStripReserved)
        check("the main wall points to the group's own wall", mainWall.root.wall.view.bands.contains { $0.pointer?.wall == gw.id })
        manager.restorer.saveNow()
        let gwFrame = gw.window.frame
        let bFrame = manager.agentWindow(for: b)?.window?.frame ?? .zero
        await deskShot("desk-office")

        // 6. Unplug: the laptop desk comes back.
        let toLaptop = await plug("laptop")
        check("unplugging switches back to the laptop desk", toLaptop && desks.active.id == laptopSetup.id && desks.currentDesk?.name == "Laptop only", desks.lastSwitch)
        check("…its walls: one main wall, All, nothing collapsed, no sidebar",
              manager.walls.count == 1 && mainWall.scope == .all && mainWall.model.collapsedBands.isEmpty && !mainWall.model.sidebarVisible,
              "\(manager.walls.map(\.id)) \(mainWall.scope)")
        check("…its agent window back, the office one closed", manager.agentWindow(for: a) != nil && manager.agentWindow(for: b) == nil)
        check("…the main wall is home again", manager.walls.first === mainWall && !mainWall.model.homeStripReserved)
        await deskShot("desk-laptop")

        // 7. Plug in again (listed the other way round): the office desk exactly.
        let toOffice = await plug("office-reversed")
        let gw2 = manager.walls.first { $0.id == gw.id }
        check("plugging in again restores the office desk (order-independent setup)", toOffice && desks.active.id == office.id, desks.lastSwitch)
        check("…the group wall: scope, frame on the Studio Display, home",
              gw2?.scope == .group("g-acme") && gw2.map { close($0.window.frame, gwFrame) } == true && manager.walls.first === gw2,
              "\(gw2?.window.frame ?? .zero) vs \(gwFrame)")
        check("…the main wall's view: collapsed band, sidebar",
              mainWall.model.collapsedBands == ["g:g-tools"] && mainWall.model.sidebarVisible)
        let bw = manager.agentWindow(for: b)?.window
        check("…the agent window on the Studio Display, the laptop's closed",
              bw.map { close($0.frame, bFrame) } == true && manager.agentWindow(for: a) == nil, "\(bw?.frame ?? .zero) vs \(bFrame)")
        check("same displays listed in another order: no switch", await plug("office", expectSwitch: false))

        // 8. Never during a drag / resize.
        desks.interactionBusy = true
        let n = desks.switches
        desks.inject(DeskController.fakeDisplays("laptop"))
        await Harness.sleep(2)
        check("no switch while a drag / resize is in progress", desks.switches == n)
        desks.interactionBusy = false
        let after = await Harness.wait(3) { self.desks.switches == n + 1 }
        check("…the switch follows when it ends", after)
        await Harness.sleep(1.2)
        await plug("office")

        // 9. Save Desk As… and the picker.
        await makeKey(mainWall.window)
        desks.showPicker()
        let picker = await Harness.wait(2) { self.manager.activeWall.model.popover == .desks }
        check("⌃⌘D opens the desk picker popover", picker)
        let pm = manager.activeWall.model
        pm.deskNameText = "Deep work"
        pm.closePopover()
        pm.deskNameSubmit?("", "Deep work")
        check("Save Desk As: a named desk for this setup, now current",
              desks.desksForSetup.map(\.name) == ["Laptop + Studio Display", "Deep work"] && desks.currentDesk?.name == "Deep work",
              desks.desksForSetup.map(\.name).joined(separator: ", "))
        mainWall.scope = .project("p-hesper")
        manager.resyncWalls()
        await Harness.sleep(0.8)
        manager.restorer.saveNow()
        await makeKey(manager.activeWall.window)
        desks.showPicker()
        _ = await Harness.wait(2) { self.manager.activeWall.model.popover == .desks }
        let content = manager.activeWall.model.popoverContent(.desks)
        check("the picker lists this setup's desks and the other display's", content.items.filter { $0.section == "This setup" }.count == 2
              && content.items.contains { $0.section == "Other displays" && $0.title == "Laptop only" }, content.items.map(\.title).joined(separator: ", "))
        await Harness.sleep(0.5)
        shot(manager.activeWall.window, "desk-picker")
        manager.activeWall.model.closePopover()
        let auto = desks.desksForSetup.first { $0.automatic }!
        desks.select(auto.id)
        await Harness.sleep(1)
        check("picking the automatic desk restores it", mainWall.scope == .all && desks.currentDesk?.id == auto.id)
        let menu = NSMenu()
        desks.menuNeedsUpdate(menu)
        check("Window ▸ Desks lists both, the current checked", menu.items.map(\.title) == ["Laptop + Studio Display", "Deep work"] && menu.items.first?.state == .on)
        if let deep = desks.desksForSetup.first(where: { $0.name == "Deep work" }) { desks.select(deep.id) }
        await Harness.sleep(1)
        check("…and Deep work again", mainWall.scope == .project("p-hesper") && desks.currentDesk?.name == "Deep work")

        // 10. Automatic switching off: the windows stay.
        model.settings.desksAutoSwitch = false
        let walls = manager.walls.map(\.id)
        let kept = await plug("laptop", expectSwitch: false)
        check("switching off: displays change, the windows stay", kept && manager.walls.map(\.id) == walls && desks.active.id == laptopSetup.id)
        model.settings.desksAutoSwitch = true
        await plug("office")
        check("…back on: the office desk (Deep work) returns", mainWall.scope == .project("p-hesper") && manager.walls.contains { $0.id == gw.id })
        manager.restorer.saveNow()
        if let url = desks.url, let d = try? Data(contentsOf: url), let book = try? JSONDecoder().decode(DeskBook.self, from: d) {
            check("desks.json: 2 setups, 3 desks", book.setups.count == 2 && book.desks.count == 3, "\(book.setups.count) \(book.desks.map(\.name))")
        }
        finish(terminate: true)
    }

    // MARK: Phase 2 (relaunch on the office displays)

    private func runPhase2() async {
        _ = await Harness.wait(15) { self.model.isConnected && self.model.wall.count >= 10 }
        let ok = await Harness.wait(6) { self.manager.walls.count == 2 && self.manager.agentWindows.count == 1 }
        await Harness.sleep(1)
        check("relaunch on the office displays: the office desk (Deep work)", ok && desks.currentDesk?.name == "Deep work" && mainWall.scope == .project("p-hesper"),
              "\(manager.walls.map(\.id)) \(manager.agentWindows.count) \(desks.currentDesk?.name ?? "nil")")
        check("…the group wall is home", manager.walls.first?.scope == .group("g-acme"))
        let onScreen = (manager.walls.map(\.window) + manager.agentWindows.compactMap(\.window)).allSatisfy { desks.isOnAttachedDisplay($0.frame) }
        check("…every window on an attached display", onScreen)
        finish(terminate: true)
    }

    // MARK: Screenshots

    private func shot(_ w: NSWindow, _ name: String) {
        guard let dir = shots else { return }
        let path = "\(dir)/\(name).png"
        capture(w, to: path)
        shotFiles.append(path)
    }

    private func capture(_ w: NSWindow, to path: String) {
        let p = Process()
        p.executableURL = URL(fileURLWithPath: "/usr/sbin/screencapture")
        p.arguments = ["-x", "-o", "-l", "\(w.windowNumber)", path]
        try? p.run()
        p.waitUntilExit()
    }

    /// The desk as a map: the (fake) displays and every Hesper window at
    /// its place, each captured on its own (nothing else of the screen).
    private func deskShot(_ name: String) async {
        guard let dir = shots else { return }
        await Harness.sleep(0.6)
        let ds = desks.displays
        let minX = ds.map(\.frame.x).min() ?? 0, minY = ds.map(\.frame.y).min() ?? 0
        let maxX = ds.map { $0.frame.maxX }.max() ?? 1, maxY = ds.map { $0.frame.maxY }.max() ?? 1
        let scale: CGFloat = 0.6, pad: CGFloat = 24
        let size = NSSize(width: (maxX - minX) * scale + 2 * pad, height: (maxY - minY) * scale + 2 * pad + 22)
        let windows = (NSApp.orderedWindows.reversed()).filter { w in w.isVisible && (manager.isOurs(w)) }
        var images: [(NSRect, NSImage)] = []
        for (i, w) in windows.enumerated() {
            let tmp = "\(dir)/\(name)-window\(i).png"
            capture(w, to: tmp)
            if let img = NSImage(contentsOfFile: tmp) { images.append((w.frame, img)) }
            try? FileManager.default.removeItem(atPath: tmp)
        }
        let image = NSImage(size: size)
        image.lockFocus()
        NSColor(calibratedRed: 0.08, green: 0.085, blue: 0.12, alpha: 1).setFill()
        NSRect(origin: .zero, size: size).fill()
        func map(_ r: NSRect) -> NSRect {
            NSRect(x: pad + (r.minX - minX) * scale, y: pad + 22 + (r.minY - minY) * scale, width: r.width * scale, height: r.height * scale)
        }
        for d in ds {
            let r = map(NSRect(x: d.frame.x, y: d.frame.y, width: d.frame.width, height: d.frame.height)).insetBy(dx: 3, dy: 3)
            NSColor(calibratedRed: 0.04, green: 0.045, blue: 0.07, alpha: 1).setFill()
            NSBezierPath(roundedRect: r, xRadius: 6, yRadius: 6).fill()
            NSColor(calibratedRed: 0.23, green: 0.25, blue: 0.35, alpha: 1).setStroke()
            let p = NSBezierPath(roundedRect: r, xRadius: 6, yRadius: 6); p.lineWidth = 3; p.stroke()
            (d.name as NSString).draw(at: NSPoint(x: r.minX + 6, y: r.minY - 20),
                                      withAttributes: [.font: NSFont.geistMono(ofSize: 12, weight: .regular), .foregroundColor: NSColor.lightGray])
        }
        for (f, img) in images { img.draw(in: map(f)) }
        let title = "desk “\(desks.currentDesk?.name ?? "")” · \(desks.active.defaultName)"
        (title as NSString).draw(at: NSPoint(x: pad, y: size.height - 20),
                                 withAttributes: [.font: NSFont.geist(ofSize: 13, weight: .semibold), .foregroundColor: NSColor.white])
        image.unlockFocus()
        if let tiff = image.tiffRepresentation, let rep = NSBitmapImageRep(data: tiff), let png = rep.representation(using: .png, properties: [:]) {
            let path = "\(dir)/\(name).png"
            try? png.write(to: URL(fileURLWithPath: path))
            shotFiles.append(path)
        }
    }

    private func finish(terminate: Bool = false) {
        let failed = checks.filter { !$0.ok }.count
        Harness.write(Result(phase: phase, passed: checks.count - failed, failed: failed, checks: checks, shots: shotFiles), to: out)
        exit(failed == 0 ? 0 : 1)
    }
}
