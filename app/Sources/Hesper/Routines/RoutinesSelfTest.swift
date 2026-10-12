import AppKit
import HesperCore

/// Routines UI test (`make -C app test-ui-routines`, Tools/run-routines.sh):
/// the real app against the fake daemon with `--routines 5`. Drives ⌘⇧R,
/// the list's order, selection and runs, Run now (the fake ends the run
/// itself), a run ending as needs-you (`fake.routineRun`), Pause / Resume,
/// Remove with its confirmation, ⌘K's Routines section and app.open
/// (`hesperctl open routines`), checking which routines.* the fake saw
/// (`fake.routineCalls`); phase "noroutines" (an older daemon): every
/// entry point stays hidden. Screenshots with `--routines-shots DIR`.
@MainActor
final class RoutinesSelfTest {
    let manager: WindowManager
    let out: String
    let shots: String?
    let phase: String?
    var checks: [SelfTest.Check] = []
    private var shotFiles: [String] = []
    private var model: AppModel { manager.primary }
    private var main: WallEntry { manager.walls[0] }
    private var window: NSWindow { main.window }
    private var hub: RoutinesHub { model.routinesHub }
    private var panel: RoutinesPanel? { RoutinesPanelHost.panel(for: model) }

    struct Result: Encodable { var passed: Int; var failed: Int; var checks: [SelfTest.Check]; var shots: [String] }

    // The fake's seed (Tools/fake-hesperd/routines.go), oldest first.
    private let floor = "r-floor-prices", digest = "r-nightly-digest", audit = "r-dependency-audit", sweep = "r-review-sweep", notes = "r-release-notes"

    init(manager: WindowManager, out: String, env: AppEnvironment) {
        self.manager = manager
        self.out = out
        shots = env.values["routines-shots"]
        phase = env.values["routines-phase"]
        Task { await run() }
    }

    private func check(_ name: String, _ ok: Bool, _ detail: String = "") {
        checks.append(SelfTest.Check(name: name, ok: ok, detail: detail))
        print(ok ? "ok  " : "FAIL", name, detail)
    }

    /// The routines.* calls the fake saw (oldest first), cleared.
    private func calls(clear: Bool = true) async -> [(method: String, params: JSONValue)] {
        guard let v = try? await model.client.call("fake.routineCalls", .object(["clear": .bool(clear)])) else { return [] }
        return (v.arrayValue ?? []).map { ($0["method"]?.stringValue ?? "", $0["params"] ?? .null) }
    }

    private func key(_ chars: String, _ code: UInt16, _ flags: NSEvent.ModifierFlags = []) {
        Harness.press(chars, keyCode: code, flags: flags, window: window)
    }

    private func makeKey() async {
        for _ in 0..<3 {
            NSApp.activate(ignoringOtherApps: true)
            window.makeKeyAndOrderFront(nil)
            if await Harness.wait(1, { self.window.isKeyWindow }) { return }
        }
    }

    private var menuItem: NSMenuItem? { NSApp.mainMenu?.items.compactMap(\.submenu).flatMap(\.items).first { $0.title == "Routines…" } }
    private func routine(_ id: String) -> Routine? { hub.routine(id) }
    private func lastRun(_ id: String) -> RoutineRun? { routine(id)?.lastRun }

    private func run() async {
        let connected = await Harness.wait(20) { self.model.isConnected && self.model.wall.count >= 3 }
        check("connects", connected, "\(model.wall.count) agents")
        guard connected else { return finish() }
        await makeKey()
        if phase == "noroutines" { return await noRoutines() }

        let supported = await Harness.wait(5) { self.hub.support.local == true && self.hub.routines.count == 5 }
        check("the daemon has routines.*: Routines are available, 5 listed", supported, "\(hub.routines.count)")
        guard supported else { return finish() }
        let item = menuItem
        check("the Routines… menu item shows (⌘⇧R)", item != nil && item?.isHidden == false && item?.keyEquivalent == "r"
              && item?.keyEquivalentModifierMask == [.command, .shift])
        // The seeded needs-you run's agent waits at an approval on the wall.
        let approval = await Harness.wait(5) { self.lastRun(self.floor)?.agent.flatMap { self.model.agent($0) }?.state == .approval }
        check("the needs-you run's agent is on the wall at its approval", approval, lastRun(floor)?.agent ?? "no agent")

        // 1. ⌘⇧R opens the panel: the routines in rank order, the first selected, its runs.
        key("R", 15, [.command, .shift])
        let opened = await Harness.wait(3) { self.panel?.isOpen == true && (self.panel?.shown.count ?? 0) == 5 }
        guard opened, let p = panel else { check("⌘⇧R opens Routines with the 5 routines", false); return finish() }
        check("⌘⇧R opens Routines with the 5 routines", true)
        check("the panel has the keyboard", window.firstResponder === p)
        let ids = p.shown.map(\.id)
        check("ranked: needs you first, the 15-minute one next, the paused one last; the rest by next run",
              ids.first == floor && ids.dropFirst().first == sweep && ids.last == audit && p.shown == RoutineList.ranked(p.shown), ids.joined(separator: ", "))
        check("the first routine is selected", p.session.selectedID == floor)
        let runsLoaded = await Harness.wait(5) { (self.hub.runs[self.floor]?.count ?? 0) == 4 }
        let floorRuns = hub.runs[floor] ?? []
        check("its runs load newest first (routines.runs)", runsLoaded && floorRuns.first?.seq == 4 && floorRuns.first?.needsYou == true && floorRuns.last?.seq == 1,
              floorRuns.map { "\($0.seq):\($0.outcome)" }.joined(separator: " "))
        check("the wall underneath keeps its tiles (no mode change)", model.mode == .wall && main.root.wall.tiles.count == model.wall.count)
        let now = Date()
        check("texts: cadence, next run, last run", RoutineText.cadence(p.shown[0]) == "every 1h +5m" && RoutineText.lastRun(p.shown[0], now: now) == "needs you"
              && RoutineText.nextRun(p.shown[0], now: now).hasPrefix("in ") && RoutineText.nextRun(p.shown.last!, now: now) == "paused",
              "\(RoutineText.cadence(p.shown[0])) · \(RoutineText.nextRun(p.shown[0], now: now)) · \(RoutineText.lastRun(p.shown[0], now: now))")
        await Harness.sleep(0.6)
        shot("routines-panel")

        // 2. ↓ selects the next one; its runs show.
        key("\u{F701}", 125)
        let stepped = await Harness.wait(3) { p.session.selectedID == self.sweep && (self.hub.runs[self.sweep]?.count ?? 0) == 2 }
        check("↓ selects review-sweep and shows its 2 runs (timed out last)", stepped && hub.runs[sweep]?.first?.outcome == "timed-out",
              hub.runs[sweep]?.map(\.outcome).joined(separator: " ") ?? "")

        // 3. ⏎ Run now: routines.run; the run appears running, then done with the fake's message.
        _ = await calls()
        key("\r", 36)
        let started = await Harness.wait(5) { self.lastRun(self.sweep)?.isRunning == true && self.lastRun(self.sweep)?.seq == 3 }
        let runCalls = await calls()
        check("⏎ Run now: routines.run {id} seen; run 3 is running (routines.changed)",
              started && runCalls.contains { $0.method == "routines.run" && $0.params["id"]?.stringValue == sweep },
              runCalls.map(\.method).joined(separator: " "))
        check("the run's agent is on the wall, named after the routine", lastRun(sweep)?.agent.flatMap { model.agent($0) }?.name.hasPrefix("review-sweep · ") == true,
              lastRun(sweep)?.agent.flatMap { model.agent($0) }?.name ?? "none")
        check("the status line says the run started", p.session.note?.text.hasPrefix("Run 3 started") == true, p.session.note?.text ?? "")
        let ended = await Harness.wait(10) { self.lastRun(self.sweep)?.outcome == "done" && self.lastRun(self.sweep)?.message == "Swept 3 units; nothing refused." }
        check("the fake ends it: done with its message", ended, lastRun(sweep).map { "\($0.outcome) \($0.message ?? "")" } ?? "nil")
        let runsRefreshed = await Harness.wait(5) { self.hub.runs[self.sweep]?.first?.seq == 3 && self.hub.runs[self.sweep]?.first?.outcome == "done" }
        check("its runs list has run 3 done, manual, with the message", runsRefreshed && hub.runs[sweep]?.first?.manual == true
              && RoutineText.summaryLine(hub.runs[sweep]!.first!) == "Swept 3 units; nothing refused.")

        // 4. ⏎ twice: the second run finds the first live (exists) and says so in the panel, no alert.
        key("\u{F701}", 125)
        _ = await Harness.wait(3) { p.session.selectedID != self.sweep && p.session.selectedID != nil }
        let picked = p.session.selectedID ?? ""
        key("\r", 36)
        _ = await Harness.wait(5) { self.lastRun(picked)?.isRunning == true }
        _ = await Harness.wait(3) { p.session.busy == nil }
        key("\r", 36)
        let exists = await Harness.wait(5) { p.session.note?.text.hasPrefix("A run is already live") == true }
        check("⏎ while a run is live: `exists` as an inline note", exists && !(p.session.note?.error ?? true), p.session.note?.text ?? "")
        _ = await Harness.wait(10) { self.lastRun(picked)?.isRunning == false }

        // 5. A run ending as needs-you (fake.routineRun): the chip changes, the routine moves to the top.
        let before = p.shown.firstIndex { $0.id == notes } ?? -1
        _ = try? await model.client.call("fake.routineRun", ["id": .string(notes), "outcome": "needs-you", "delayMs": 300])
        let needs = await Harness.wait(10) { self.lastRun(self.notes)?.needsYou == true && p.shown.first?.id == self.notes }
        check("fake.routineRun needs-you: the chip says needs you and the routine moves to the top",
              needs && before > 0 && RoutineLook.mark(lastRun(notes)) == .needsYou && RoutineText.lastRun(routine(notes)!, now: Date()) == "needs you",
              "was at \(before); \(lastRun(notes)?.outcome ?? "nil")")
        let agentWaits = await Harness.wait(5) { self.lastRun(self.notes)?.agent.flatMap { self.model.agent($0) }?.state == .approval }
        check("its agent waits at an approval", agentWaits)
        p.session.select(notes)
        await Harness.sleep(0.5)
        shot("routines-needs-you")

        // 6. P pauses (routines.pause), the row says paused; P again resumes.
        p.session.select(floor)
        _ = await calls()
        key("p", 35)
        let paused = await Harness.wait(5) { self.routine(self.floor)?.paused == true && self.routine(self.floor)?.nextRun == nil }
        let pauseCalls = await calls()
        check("P: routines.pause {id, paused: true}; \"paused\" shown, no next run",
              paused && pauseCalls.contains { $0.method == "routines.pause" && $0.params["id"]?.stringValue == floor && $0.params["paused"]?.boolValue == true }
              && RoutineText.nextRun(routine(floor)!, now: Date()) == "paused", pauseCalls.map(\.method).joined(separator: " "))
        _ = await Harness.wait(3) { p.session.busy == nil }
        key("p", 35)
        let resumed = await Harness.wait(5) { self.routine(self.floor)?.paused == false && self.routine(self.floor)?.nextRun != nil }
        let resumeCalls = await calls()
        check("P again: routines.pause {paused: false}; the next run is back",
              resumed && resumeCalls.contains { $0.method == "routines.pause" && $0.params["paused"]?.boolValue == false })

        // 7. O on a needs-you run opens its agent (the panel closes); ⌘⇧R comes back.
        p.session.select(notes)
        key("o", 31)
        let agent = lastRun(notes)?.agent ?? ""
        let openedAgent = await Harness.wait(3) { self.panel?.isOpen != true && self.model.selectedID == agent }
        check("O: a needs-you run focuses its agent", openedAgent, "selected \(model.selectedID ?? "nil") vs \(agent)")
        key("R", 15, [.command, .shift])
        _ = await Harness.wait(3) { self.panel?.isOpen == true }

        // 8. ⌫ asks first (esc keeps it); ⌫ ⌫ removes (routines.remove), the row is gone.
        p.session.select(audit)
        _ = await calls()
        key("\u{7f}", 51)
        let asked = await Harness.wait(2) { p.session.confirmRemove == self.audit }
        key("\u{1b}", 53)
        let kept = await Harness.wait(2) { p.session.confirmRemove == nil && p.isOpen }
        // Selecting it fetches its runs (routines.runs may land after the
        // clear above): only a remove would be wrong here.
        let noRemove = await !calls(clear: false).contains { $0.method == "routines.remove" }
        check("⌫ asks in the panel; esc keeps the routine (and the panel open)", asked && kept && routine(audit) != nil && noRemove,
              "asked \(asked) kept \(kept) still listed \(routine(audit) != nil) no remove \(noRemove)")
        key("\u{7f}", 51)
        _ = await Harness.wait(2) { p.session.confirmRemove == self.audit }
        key("\u{7f}", 51)
        let removed = await Harness.wait(5) { self.routine(self.audit) == nil && p.shown.count == 4 && !p.shown.contains { $0.id == self.audit } }
        let removeCalls = await calls()
        check("⌫ ⌫: routines.remove {id} seen, the row is gone, another routine selected",
              removed && removeCalls.contains { $0.method == "routines.remove" && $0.params["id"]?.stringValue == audit } && p.session.selectedID != nil
              && p.session.selectedID != audit, "\(p.shown.count) rows, selected \(p.session.selectedID ?? "nil")")

        // 9. ⌘Y closes it and opens History; esc closes History. Esc closes the panel.
        key("y", 16, .command)
        let swapped = await Harness.wait(3) { self.panel?.isOpen != true && HistoryPanelHost.isOpen(self.model) }
        check("⌘Y closes Routines and opens History", swapped)
        key("\u{1b}", 53)
        _ = await Harness.wait(3) { !HistoryPanelHost.isOpen(self.model) }
        key("R", 15, [.command, .shift])
        _ = await Harness.wait(3) { self.panel?.isOpen == true }
        key("\u{1b}", 53)
        let closed = await Harness.wait(3) { self.panel?.isOpen != true }
        check("esc closes the panel", closed)

        // 10. ⌘K: a Routines section; ⏎ on a row opens the panel on it.
        key("k", 40, .command)
        _ = await Harness.wait(3) { self.model.showPalette }
        let rows = model.filteredPalette().filter { $0.section == "Routines" }
        check("⌘K lists the routines in a Routines section (ranked)", rows.count == 4 && rows.first?.id == "routine:\(notes)", rows.map(\.title).joined(separator: ", "))
        model.paletteList.query = "review-sweep"
        let narrowed = await Harness.wait(3) { self.model.filteredPalette().filter { $0.section == "Routines" }.map(\.id) == ["routine:\(self.sweep)"] }
        check("typing narrows it", narrowed)
        await Harness.sleep(0.4)
        shot("palette-routines")
        model.filteredPalette().first { $0.id == "routine:\(sweep)" }?.run()
        let fromPalette = await Harness.wait(3) { !self.model.showPalette && self.panel?.isOpen == true && self.panel?.session.selectedID == self.sweep }
        check("⏎ on it opens the panel on that routine", fromPalette)
        key("\u{1b}", 53)
        _ = await Harness.wait(3) { self.panel?.isOpen != true }

        // 11. hesperctl open routines [ID]: app.open {routines} through the app's control.
        let control = AppControl(manager: manager)
        let r1 = control.handle("app.open", ["routines": .string(digest)])
        let viaControl = await Harness.wait(3) { self.panel?.isOpen == true && self.panel?.session.selectedID == self.digest }
        check("app.open {routines: ID} opens the panel on it", viaControl && (try? r1.get())?["routine"]?.stringValue == digest, "\(r1)")
        let r2 = control.handle("app.open", ["routines": "r-nope"])
        check("app.open on an unknown routine: not_found", failure(r2)?.contains("no routine r-nope") == true, failure(r2) ?? "ok")
        let r3 = control.handle("app.open", ["routines": true])
        check("app.open {routines: true} keeps the panel open on the selection", (try? r3.get()) != nil && panel?.isOpen == true && panel?.session.selectedID == digest)
        key("\u{1b}", 53)
        _ = await Harness.wait(3) { self.panel?.isOpen != true }
        finish()
    }

    /// An older hesperd (no routines.*): nothing of Routines shows.
    private func noRoutines() async {
        let known = await Harness.wait(5) { self.hub.support.local == false }
        check("an older daemon: routines.* unsupported", known)
        check("the Routines… menu item is hidden", menuItem?.isHidden == true)
        key("R", 15, [.command, .shift])
        await Harness.sleep(0.4)
        check("⌘⇧R opens nothing", panel?.isOpen != true)
        check("the toast says hesperd needs an update", model.toast?.text.contains("newer hesperd") == true, model.toast?.text ?? "")
        key("k", 40, .command)
        _ = await Harness.wait(3) { self.model.showPalette }
        check("⌘K has no Routines section", !model.filteredPalette().contains { $0.section == "Routines" })
        model.showPalette = false
        check("the sidebar offers no Show Routines", !model.routinesAvailable)
        let control = AppControl(manager: manager)
        let r = control.handle("app.open", ["routines": true])
        check("app.open {routines} answers unavailable", failure(r)?.contains("routines.*") == true, failure(r) ?? "ok")
        finish()
    }

    private func failure(_ r: Swift.Result<JSONValue, RPCError>) -> String? {
        if case .failure(let e) = r { return e.message }
        return nil
    }

    private func shot(_ name: String) {
        guard let dir = shots else { return }
        let path = "\(dir)/\(name).png"
        let p = Process()
        p.executableURL = URL(fileURLWithPath: "/usr/sbin/screencapture")
        p.arguments = ["-x", "-o", "-l", "\(window.windowNumber)", path]
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
