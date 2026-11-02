import AppKit
import HesperCore

/// Loops UI test (`make -C app test-ui-loops`, Tools/run-loops.sh): the
/// real app against the fake daemon seeded with `--projects demo --loops
/// demo`. Opens each loops sheet through its key and checks its rows: the
/// board's four columns, a loop's detail with steps and triggers, the
/// map, the wrap-up's verdicts and a product page with its milestones,
/// unfiled loops and planning view; then the Settings › Steward pane.
/// Opens windows: run only with the owner's consent. Screenshots with
/// `--loops-shots DIR`.
@MainActor
final class LoopsSelfTest {
    let manager: WindowManager
    let out: String
    let shots: String?
    var checks: [SelfTest.Check] = []
    private var shotFiles: [String] = []
    private var model: AppModel { manager.primary }
    private var main: WallEntry { manager.walls[0] }
    private var panel: LoopsPanel? { LoopsSheetHost.panel(for: model) }

    struct Result: Encodable { var passed: Int; var failed: Int; var checks: [SelfTest.Check]; var shots: [String] }

    init(manager: WindowManager, out: String, env: AppEnvironment) {
        self.manager = manager
        self.out = out
        shots = env.values["loops-shots"]
        Task { await run() }
    }

    private func check(_ name: String, _ ok: Bool, _ detail: String = "") {
        checks.append(SelfTest.Check(name: name, ok: ok, detail: detail))
        print(ok ? "ok  " : "FAIL", name, detail)
    }

    /// The control with accessibility identifier `id` in the main window.
    private func control(_ id: String) -> NSAccessibilityProtocol? {
        func find(_ el: Any) -> NSAccessibilityProtocol? {
            guard let a = el as? NSAccessibilityProtocol else { return nil }
            if a.accessibilityIdentifier() == id { return a }
            for c in a.accessibilityChildren() ?? [] { if let f = find(c) { return f } }
            return nil
        }
        return find(main.window as Any)
    }

    private func key(_ chars: String, _ code: UInt16, _ flags: NSEvent.ModifierFlags = []) {
        Harness.press(chars, keyCode: code, flags: flags, window: main.window)
    }

    private func run() async {
        let hub = model.loopHub
        let loaded = await Harness.wait(20) { self.model.isConnected && hub.loaded && hub.store.loops.count >= 10 }
        check("connects; the loops, goals and the Steward are listed", loaded && hub.available && hub.steward.enabled,
              "\(hub.store.loops.count) loops, \(hub.store.milestones.count) milestones")
        guard loaded else { return finish() }
        NSApp.activate(ignoringOtherApps: true)
        main.window.makeKeyAndOrderFront(nil)
        _ = await Harness.wait(2) { self.main.window.isKeyWindow }
        check("chips for every active loop; the your-move count", hub.chips["lp-0000000push"] != nil && hub.chips["lp-0000000done"] == nil
              && hub.onYouCount > 0, "\(hub.chips.count) chips, \(hub.onYouCount) your move")

        await board(hub)
        await wrapUp(hub)
        await product(hub)
        await steward()
        finish()
    }

    // MARK: Loops (⌘L)

    private func board(_ hub: LoopHub) async {
        key("l", 0x25, .command)
        let open = await Harness.wait(3) { LoopsSheetHost.surface(self.model) == .loops && self.control("loops.column.you") != nil }
        check("⌘L opens the board with its four columns", open
              && ["you", "agents", "others", "parked"].allSatisfy { self.control("loops.column.\($0)") != nil })
        guard open, let m = panel?.board else { return }
        let b = m.board
        check("columns: the ask and the step on you, agents working, the blocked one waiting, the spike parked",
              b.loops(.you).contains { $0.id == "lp-00000badges" } && b.loops(.agents).contains { $0.id == "lp-00000widget" }
                && b.loops(.others).contains { $0.id == "lp-0000000auth" } && b.loops(.parked).map(\.id) == ["lp-0000000vtcp"]
                && !b.order.contains("lp-0000000done"),
              LoopColumn.allCases.map { "\($0): \(b.loops($0).map(\.id))" }.joined(separator: "; "))
        check("a card per loop with its id", control("loops.card.lp-0000000push") != nil)
        check("the Steward line under the board", control("loops.steward") != nil)
        shot("loops-board")

        // ↓ → move the selection; ⏎ opens the detail.
        let first = b.selected
        key("", 125)
        key("", 124)
        check("↓ → move the selection", m.board.selected != nil && m.board.selected != first, m.board.selected ?? "nil")
        m.show("lp-0000000push")
        let detail = await Harness.wait(2) { self.control("loops.detail") != nil }
        let steps = hub.loop("lp-0000000push").map(LoopActions.steps) ?? []
        check("the detail lists the loop's steps with owner and trigger", detail && !steps.isEmpty
              && steps.allSatisfy { self.control("loops.step.\($0.id)") != nil }, "\(steps.count) steps")
        shot("loops-detail")

        // A: a step typed and saved (the fake answers loops.item).
        let before = steps.count
        key("a", 0x00)
        let field = await Harness.wait(2) { m.field == .addStep && self.control("loops.field") != nil }
        check("A opens the step field", field)
        m.fieldText = "@agent selftest step"
        key("\r", 36)
        let added = await Harness.wait(3) { (hub.loop("lp-0000000push").map(LoopActions.steps)?.count ?? 0) == before + 1 }
        check("⏎ saves the step; the loop updates from the notification", added && m.field == nil && m.error == nil, m.error ?? "")

        // Space checks the selected step off.
        if let s = hub.loop("lp-0000000push").flatMap({ LoopActions.steps($0).first { $0.isOpen } }) {
            m.row = s.id
            key(" ", 49)
            let ticked = await Harness.wait(3) { hub.loop("lp-0000000push")?.items.first { $0.id == s.id }?.state == .done }
            check("Space checks the step off", ticked)
        }

        // esc back to the board, G the map, G back.
        key("\u{1b}", 53)
        check("esc goes back to the board", m.mode == .board && LoopsSheetHost.isOpen(model))
        key("g", 0x05)
        let map = await Harness.wait(2) { m.mode == .map && self.control("loops.mapCanvas") != nil && self.control("loops.legend") != nil }
        check("G shows the map with its legend; lanes per machine", map && m.map.lanes.count >= 2 && !m.map.edges.isEmpty,
              "\(m.map.lanes.map(\.title)) \(m.map.edges.count) edges")
        check("the map's dangling parent is drawn as broken", m.map.edges.contains { $0.kind == .brokenParent })
        shot("loops-map")
        key("g", 0x05)
        check("G again: the board", m.mode == .board)

        // P parks the selected loop (it moves to Parked).
        m.show("lp-0000000copy")
        key("\u{1b}", 53)
        key("p", 0x23)
        let parked = await Harness.wait(3) { hub.loop("lp-0000000copy")?.status == .parked }
        check("P parks the selected loop", parked && m.board.loops(.parked).contains { $0.id == "lp-0000000copy" })

        key("l", 0x25, .command)
        let closed = await Harness.wait(2) { !LoopsSheetHost.isOpen(self.model) }
        check("⌘L again closes the board", closed)
    }

    // MARK: Wrap-up (⇧⌘E)

    private func wrapUp(_ hub: LoopHub) async {
        key("E", 0x0E, [.command, .shift])
        let open = await Harness.wait(3) { LoopsSheetHost.surface(self.model) == .wrapUp && self.control("wrapup.card") != nil }
        check("⇧⌘E opens the wrap-up on one loop", open)
        guard open, let m = panel?.wrapUp else { return }
        check("today's loops listed with a suggestion each; debris suggested drop",
              !m.wrap.entries.isEmpty && m.wrap.entries.contains { $0.id == "lp-0000debris" && $0.suggestion == .drop },
              m.wrap.entries.map { "\($0.id)=\($0.suggestion.title)" }.joined(separator: ", "))
        check("four verdict cards", ["done", "nextstep", "park", "drop"].allSatisfy { self.control("wrapup.verdict.\($0)") != nil })
        shot("wrapup")
        let first = m.selected
        key("\r", 36)
        check("⏎ takes the suggestion and moves on", m.wrap.decidedCount == 1 && m.selected != first)
        key("u", 0x20)
        check("U undoes it", m.wrap.decidedCount == 0 && m.selected == first)
        if let debris = m.wrap.entries.first(where: { $0.id == "lp-0000debris" }) {
            m.select(debris.id)
            key("x", 0x07)
            key("\r", 36, .command)
            let dropped = await Harness.wait(3) { hub.loop("lp-0000debris")?.status == .dropped }
            check("X then ⌘↩ drops the debris loop", dropped && m.error == nil, m.error ?? "")
        }
        key("\u{1b}", 53)
        let closed = await Harness.wait(2) { !LoopsSheetHost.isOpen(self.model) }
        check("esc closes the wrap-up", closed)
    }

    // MARK: Product (⇧⌘L)

    private func product(_ hub: LoopHub) async {
        model.openLoopsSheet(.product(scope: "g-acme"))
        let open = await Harness.wait(3) { LoopsSheetHost.surface(self.model) == .product && self.control("product.page") != nil }
        check("the product page of acme apps opens", open)
        guard open, let m = panel?.product, let page = m.page else { return }
        check("goal, milestones in order (done last), the stalled-free beta first",
              page.goal?.title == "acme apps in both app stores" && page.milestones.map(\.id) == ["ms-00000000beta", "ms-00000launch", "ms-0000000alpha"],
              page.milestones.map(\.id).joined(separator: ", "))
        check("each milestone row shows", page.milestones.allSatisfy { self.control("product.milestone.\($0.id)") != nil })
        check("the unfiled section lists the scope's unfiled loops", control("product.unfiled") != nil && !page.unfiled.isEmpty,
              page.unfiled.map(\.id).joined(separator: ", "))
        shot("product")

        // Y files an unfiled loop under the first open milestone.
        if let l = page.unfiled.first {
            m.selected = l.id
            key("y", 0x10)
            check("Y opens the milestone picker", m.filing && control("product.file") != nil)
            key("\r", 36)
            let filed = await Harness.wait(3) { hub.loop(l.id)?.milestone != nil }
            check("⏎ files the loop", filed && m.error == nil, m.error ?? "")
        }

        // P plans: the planning view with the proposals; ⌘↩ accepts them.
        key("p", 0x23)
        let planning = await Harness.wait(5) { m.mode == .planning && self.control("product.planning") != nil }
        check("P opens the planning view with the proposed milestones", planning && !(m.page?.proposed.isEmpty ?? true)
              && (m.page?.proposed ?? []).allSatisfy { self.control("product.proposed.\($0.id)") != nil })
        shot("planning")
        key("\r", 36, .command)
        let accepted = await Harness.wait(3) { hub.store.milestones.values.filter { $0.scope == "g-acme" && $0.status == .proposed }.isEmpty }
        check("⌘↩ saves the plan (goals.accept)", accepted && m.mode == .page)
        key("\u{1b}", 53)
        let closed = await Harness.wait(2) { !LoopsSheetHost.isOpen(self.model) }
        check("esc closes the product page", closed)
    }

    // MARK: Settings › Steward

    private func steward() async {
        model.onShowSettings?()
        _ = await Harness.wait(3) { NSApp.windows.contains { $0.title == "Settings" && $0.isVisible } }
        NotificationCenter.default.post(name: SettingsView.showPane, object: nil, userInfo: ["pane": SettingsView.Pane.steward])
        guard let settings = NSApp.windows.first(where: { $0.title == "Settings" }) else { check("Settings opens", false); return }
        func find(_ id: String) -> NSAccessibilityProtocol? {
            func walk(_ el: Any) -> NSAccessibilityProtocol? {
                guard let a = el as? NSAccessibilityProtocol else { return nil }
                if a.accessibilityIdentifier() == id { return a }
                for c in a.accessibilityChildren() ?? [] { if let f = walk(c) { return f } }
                return nil
            }
            return walk(settings as Any)
        }
        let pane = await Harness.wait(3) { find("settings.steward.enabled") != nil }
        check("Settings › Steward: the switch, level, machine and the never list", pane && find("settings.steward.level") != nil
              && find("settings.steward.machine") != nil && find("settings.steward.never") != nil)
        if let dir = shots { capture(settings, "\(dir)/settings-steward.png") }
        settings.close()
    }

    // MARK: Out

    private func shot(_ name: String) {
        guard let dir = shots else { return }
        capture(main.window, "\(dir)/\(name).png")
    }

    private func capture(_ w: NSWindow, _ path: String) {
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
