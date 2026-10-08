import Foundation
import Testing
@testable import HesperCore

private func agent(_ id: String, _ state: AgentState, since: TimeInterval = 0, machine: String = "L", created: TimeInterval = 0) -> Agent {
    Agent(id: id, machine: machine, name: id, state: state, stateSince: Date(timeIntervalSince1970: since),
          created: Date(timeIntervalSince1970: created))
}

@Suite struct AttentionOrdering {
    @Test func approvalThenQuestionThenErrorOldestFirst() {
        let list = [
            agent("L/q1", .question, since: 10), agent("L/a2", .approval, since: 30), agent("L/e1", .error, since: 1),
            agent("L/a1", .approval, since: 20), agent("L/w", .working, since: 0), agent("L/q0", .question, since: 5),
            agent("L/d", .done, since: 0),
        ]
        #expect(AttentionQueue.ordered(list).map(\.id) == ["L/a1", "L/a2", "L/q0", "L/q1", "L/e1"])
    }

    @Test func nextMovesThroughTheQueueAndWraps() {
        let list = [agent("L/a", .approval, since: 1), agent("L/b", .question, since: 2), agent("L/c", .working)]
        #expect(AttentionQueue.next(list, after: nil)?.id == "L/a")
        #expect(AttentionQueue.next(list, after: "L/c")?.id == "L/a")
        #expect(AttentionQueue.next(list, after: "L/a")?.id == "L/b")
        #expect(AttentionQueue.next(list, after: "L/b")?.id == "L/a")
        #expect(AttentionQueue.next([agent("L/c", .working)], after: nil) == nil)
    }

    @Test func tiesBreakById() {
        let list = [agent("L/b", .approval, since: 5), agent("L/a", .approval, since: 5)]
        #expect(AttentionQueue.ordered(list).map(\.id) == ["L/a", "L/b"])
    }
}

@Suite struct KeyRouting {
    @Test func arrangementKeys() {
        for mode in [AppMode.wall, .focus] {
            #expect(KeyRouter.route(KeyChord(.char("1"), command: true, option: true), mode: mode, selectedState: nil) == .arrangement(0))
            #expect(KeyRouter.route(KeyChord(.char("4"), command: true, option: true), mode: mode, selectedState: nil) == .arrangement(3))
            #expect(KeyRouter.route(KeyChord(.char("l"), command: true, option: true), mode: mode, selectedState: nil) == .cycleArrangement)
        }
        #expect(KeyRouter.route(KeyChord(.char("1"), command: true), mode: .wall, selectedState: nil) == .none)
    }

    @Test func commandShortcutsAgreeInBothModes() {
        for mode in [AppMode.wall, .focus] {
            #expect(KeyRouter.route(KeyChord(.char("n"), command: true), mode: mode, selectedState: nil) == .newAgent)
            #expect(KeyRouter.route(KeyChord(.char("j"), command: true), mode: mode, selectedState: nil) == .nextAttention)
            #expect(KeyRouter.route(KeyChord(.char("k"), command: true), mode: mode, selectedState: nil) == .palette)
            #expect(KeyRouter.route(KeyChord(.enter, command: true), mode: mode, selectedState: .working) == .toggleFocus)
            #expect(KeyRouter.route(KeyChord(.char("["), command: true), mode: mode, selectedState: nil) == .stepPrevious)
            #expect(KeyRouter.route(KeyChord(.char("]"), command: true), mode: mode, selectedState: nil) == .stepNext)
            #expect(KeyRouter.route(KeyChord(.char("w"), command: true), mode: mode, selectedState: .working) == .closeAgent)
            #expect(KeyRouter.route(KeyChord(.char("M"), command: true, shift: true), mode: mode, selectedState: nil) == .moveAgent)
            #expect(KeyRouter.route(KeyChord(.char("m"), command: true, shift: true), mode: mode, selectedState: nil) == .moveAgent)
        }
    }

    @Test func focusPassesEverythingElseToTheAgent() {
        let keys: [KeyChord] = [KeyChord(.escape), KeyChord(.enter), KeyChord(.char("a")), KeyChord(.char("c"), control: true),
                                KeyChord(.char("c"), command: true), KeyChord(.left), KeyChord(.char("n"))]
        for k in keys { #expect(KeyRouter.route(k, mode: .focus, selectedState: .approval) == .passThrough, "\(k)") }
        #expect(KeyRouter.route(KeyChord(.escape, command: true), mode: .focus, selectedState: nil) == .exitFocus)
    }

    @Test func wallApprovalKeys() {
        #expect(KeyRouter.route(KeyChord(.enter), mode: .wall, selectedState: .approval) == .answer(.allow))
        #expect(KeyRouter.route(KeyChord(.char("a")), mode: .wall, selectedState: .approval) == .answer(.always))
        #expect(KeyRouter.route(KeyChord(.char("n")), mode: .wall, selectedState: .approval) == .denyWithMessage)
        #expect(KeyRouter.route(KeyChord(.char("a")), mode: .wall, selectedState: .working) == .none)
        #expect(KeyRouter.route(KeyChord(.char("a")), mode: .wall, selectedState: .working, typed: "a") == .typeInto("a"))
        // Approval keys win over typing on a tile that needs you; other
        // letters type into it.
        #expect(KeyRouter.route(KeyChord(.char("A"), shift: true), mode: .wall, selectedState: .approval, typed: "A") == .answer(.always))
        #expect(KeyRouter.route(KeyChord(.char("n")), mode: .wall, selectedState: .approval, typed: "n") == .denyWithMessage)
        #expect(KeyRouter.route(KeyChord(.char("x")), mode: .wall, selectedState: .approval, typed: "x") == .typeInto("x"))
        #expect(KeyRouter.route(KeyChord(.enter), mode: .wall, selectedState: .approval, typed: "\r") == .answer(.allow))
        #expect(KeyRouter.route(KeyChord(.enter), mode: .wall, selectedState: .working) == .activateTile)
        #expect(KeyRouter.route(KeyChord(.enter), mode: .wall, selectedState: nil) == .none)
        // ⌘esc on the wall: the active tile back to selected.
        #expect(KeyRouter.route(KeyChord(.escape, command: true), mode: .wall, selectedState: nil) == .exitFocus)
    }

    @Test func wallSelection() {
        #expect(KeyRouter.route(KeyChord(.left), mode: .wall, selectedState: nil) == .select(.left))
        #expect(KeyRouter.route(KeyChord(.down), mode: .wall, selectedState: .working, typed: "\u{F701}") == .select(.down))
        #expect(KeyRouter.route(KeyChord(.home), mode: .wall, selectedState: nil) == .select(.first))
        #expect(KeyRouter.route(KeyChord(.end), mode: .wall, selectedState: nil) == .select(.last))
        #expect(KeyRouter.route(KeyChord(.tab, shift: true), mode: .wall, selectedState: nil) == .stepPrevious)
        #expect(KeyRouter.route(KeyChord(.char("a"), option: true), mode: .wall, selectedState: .approval) == .none)
        #expect(KeyRouter.route(KeyChord(.char("l"), option: true), mode: .wall, selectedState: .working, typed: "@") == .typeInto("@"))
        #expect(KeyRouter.route(KeyChord(.char("x")), mode: .wall, selectedState: nil, typed: "x") == .none)
        #expect(KeyRouter.route(KeyChord(.char("x")), mode: .wall, selectedState: nil, selectedIsDraft: true, typed: "x") == .typeInto("x"))
        #expect(KeyRouter.route(KeyChord(.char("c"), control: true), mode: .wall, selectedState: .working, typed: "\u{3}") == .none)
    }

    /// ⌥⌘← ⌥⌘→ step agents everywhere (⌘[ ⌘] too); plain ← → in focus
    /// stay the terminal's.
    @Test func stepKeys() {
        for mode in [AppMode.wall, .focus] {
            #expect(KeyRouter.route(KeyChord(.left, command: true, option: true), mode: mode, selectedState: .working) == .stepPrevious)
            #expect(KeyRouter.route(KeyChord(.right, command: true, option: true), mode: mode, selectedState: .working) == .stepNext)
        }
        #expect(KeyRouter.routeActiveTile(KeyChord(.right, command: true, option: true)) == .stepNext)
        #expect(KeyRouter.route(KeyChord(.right), mode: .focus, selectedState: .working) == .passThrough)
        #expect(KeyRouter.route(KeyChord(.left), mode: .focus, selectedState: .working) == .passThrough)
        #expect(KeyRouter.routeActiveTile(KeyChord(.right)) == .passThrough)
    }

    @Test func terminalKeys() {
        #expect(TerminalKeys.bytes(KeyChord(.char("x")), characters: "x") == "x")
        #expect(TerminalKeys.bytes(KeyChord(.enter), characters: "\r") == "\r")
        #expect(TerminalKeys.bytes(KeyChord(.right), characters: "\u{F703}") == "\u{1b}[C")
        #expect(TerminalKeys.bytes(KeyChord(.char("c"), control: true), characters: "\u{3}") == "\u{3}")
        #expect(TerminalKeys.bytes(KeyChord(.char("\u{7f}")), characters: "\u{7f}") == "\u{7f}")
        #expect(TerminalKeys.bytes(KeyChord(.char("v"), command: true), characters: "v") == nil)
        #expect(TerminalKeys.bytes(KeyChord(.other), characters: "\u{F704}") == nil)
        #expect(TerminalKeys.isPrintable("é@ ") && !TerminalKeys.isPrintable("") && !TerminalKeys.isPrintable("\u{1b}"))
    }
}

@Suite struct Registry {
    let hello = HelloInfo(daemon: "d", version: "1", machine: "L", machines: [Machine(short: "L", name: "laptop")])

    @Test func transitionsOnlyWhenEnteringAttention() {
        var r = AgentRegistry()
        r.apply(.connected(hello))
        #expect(r.apply(.changed(agent("L/a", .working))).isEmpty)
        let t = r.apply(.changed(agent("L/a", .approval)))
        #expect(t.count == 1 && t[0].from == .working)
        #expect(r.apply(.changed(agent("L/a", .approval))).isEmpty, "same state again is not a new transition")
        #expect(r.counts.approval == 1)
    }

    @Test func reconcileDropsAgentsGoneWhileDisconnected() {
        var r = AgentRegistry()
        r.apply(.connected(hello))
        r.apply(.changed(agent("L/a", .working)))
        r.apply(.changed(agent("L/b", .working)))
        r.apply(.disconnected("x"))
        r.apply(.connected(hello))
        r.apply(.changed(agent("L/a", .working)))      // snapshot after resubscribe
        r.apply(.changed(agent("L/new", .starting)))   // spawned after list was computed
        r.apply(.reconciled(["L/a"]))
        #expect(Set(r.agents.keys) == ["L/a", "L/new"])
    }

    @Test func wallOrderLocalFirstThenCreation() {
        var r = AgentRegistry()
        r.apply(.connected(hello))
        r.apply(.changed(agent("M/x", .working, machine: "M", created: 1)))
        r.apply(.changed(agent("L/b", .working, created: 2)))
        r.apply(.changed(agent("L/a", .approval, created: 3)))
        #expect(r.wallOrder.map(\.id) == ["L/b", "L/a", "M/x"])
    }

    @Test func notificationPolicy() {
        let t = AgentRegistry.Transition(agent: agent("L/a", .question), from: .working)
        #expect(shouldNotify(t, focusedID: nil, appActive: true))
        #expect(!shouldNotify(t, focusedID: "L/a", appActive: true))
        #expect(shouldNotify(t, focusedID: "L/a", appActive: false))
        #expect(!shouldNotify(AgentRegistry.Transition(agent: agent("L/a", .done), from: .working), focusedID: nil, appActive: false))
    }

    @Test func menuBarTitle() {
        let c = StateCounts([agent("L/a", .approval), agent("L/b", .approval), agent("L/c", .question), agent("L/d", .working)])
        #expect(c.menuBarTitle == "2! 1?")
        #expect(StateCounts([agent("L/d", .working)]).menuBarTitle == "")
    }
}

@Suite struct ModelDecoding {
    @Test func decodesTheContractAgent() throws {
        let json = """
        {"id":"L/a7f3k2","machine":"L","kind":"claude","profile":"claude-auto-rc","name":"push-provider-fcm",
         "task":"t","project":"/p","worktree":"/w","branch":"feature/x","state":"approval",
         "stateSince":"2026-10-06T19:02:11Z",
         "attention":{"kind":"approval","title":"Bash","detail":"git push","options":["allow","always","deny"]},
         "summary":"s","sessionId":"abc","size":{"cols":120,"rows":40},"created":"2026-10-06T19:00:00.123Z","pid":12345,"exit":null}
        """
        let a = try JSONDecoder().decode(Agent.self, from: Data(json.utf8))
        #expect(a.state == .approval && a.attention?.options == ["allow", "always", "deny"])
        #expect(a.size == GridSize(cols: 120, rows: 40))
        #expect(a.stateSince == Date(timeIntervalSince1970: 1_791_313_331))
        #expect(a.created != nil && a.isRunning && a.exit == nil)
    }

    @Test func exitObjectAndUnknownStates() throws {
        let a = try JSONDecoder().decode(Agent.self, from: Data(#"{"id":"L/x","state":"exited","exit":{"code":null,"signal":"SIGHUP"}}"#.utf8))
        #expect(a.exit == ExitInfo(code: nil, signal: "SIGHUP") && !a.isRunning && a.machine == "L")
        let e = try JSONDecoder().decode(Agent.self, from: Data(#"{"id":"L/x","state":"error","exit":{"code":2}}"#.utf8))
        #expect(!e.isRunning && e.exit?.label == "exit 2")
        let u = try JSONDecoder().decode(Agent.self, from: Data(#"{"id":"L/x","state":"compacting"}"#.utf8))
        #expect(u.state == .unknown && u.isRunning)
    }

    @Test func profilesDefaults() throws {
        let json = #"{"profiles":{"claude-auto-rc":{"kind":"claude","argv":["claude"]},"codex-full":{"kind":"codex","argv":["codex"]}},"defaults":{"kind":"claude","kinds":{"claude":"claude-auto-rc","codex":"codex-full"},"projects":{"/p":"codex-full"}}}"#
        let p = try JSONDecoder().decode(ProfilesInfo.self, from: Data(json.utf8))
        #expect(p.defaultProfile(project: nil) == "claude-auto-rc")
        #expect(p.defaultProfile(project: "/p") == "codex-full")
        #expect(p.defaultProfile(project: "/p", kind: "claude") == "claude-auto-rc")
        #expect(p.defaultProfile(project: "/other", kind: "codex") == "codex-full")
    }

    @Test func spawnParams() {
        let r = SpawnRequest(machine: "M", profile: "codex-full", project: "/p", task: "do it", name: "", worktree: .auto, branch: "feature/x")
        #expect(r.params == ["project": "/p", "task": "do it", "machine": "M", "profile": "codex-full", "worktree": true, "branch": "feature/x"])
        #expect(SpawnRequest(project: "/p", task: "t", worktree: .path("/w")).params["worktree"] == "/w")
    }
}

@Suite struct UserConfig {
    @Test func readsFontsAndFollowsIncludes() {
        let files = [
            "/h/.config/ghostty/config": "# comment\nconfig-file = ~/.config/ghostty/config.ghostty\nfont-size = 11\n",
            "/h/.config/ghostty/config.ghostty": "font-family = \"JetBrains Mono\"\nfont-family = Symbols Nerd Font\nfont-size = 13\nconfig-file = ?extra\nfont-thicken = true\n",
            "/h/.config/ghostty/extra": "font-feature = -calt\n",
        ]
        let cfg = GhosttyUserConfig.load(paths: ["/h/.config/ghostty/config"], read: { p in
            files[p.replacingOccurrences(of: NSHomeDirectory(), with: "/h")] ?? files[p]
        })
        #expect(cfg.fontFamilies == ["JetBrains Mono", "Symbols Nerd Font"])
        #expect(cfg.fontSize == 13)  // the include is loaded after the file that names it
        #expect(cfg.fontThicken == true)
        #expect(cfg.fontFeatures == ["-calt"])
    }

    @Test func emptyFamilyResetsAndCyclesStop() {
        let files = ["/a": "font-family = A\nfont-family =\nfont-family = B\nconfig-file = /a\n"]
        let cfg = GhosttyUserConfig.load(paths: ["/a"], read: { files[$0] })
        #expect(cfg.fontFamilies == ["B"])
    }

    @Test func configTextCarriesFontAndTheme() {
        var u = GhosttyUserConfig()
        u.fontFamilies = ["Iosevka"]
        u.fontSize = 12.5
        let t = TerminalConfigText.make(user: u)
        #expect(t.contains("font-family = Iosevka\n"))
        #expect(t.contains("font-size = 12.5\n"))
        #expect(t.contains("background = #1a1b26\n"))
        #expect(t.contains("palette = 15=#c0caf5\n"))
        #expect(t.contains("keybind = clear\n"))
        #expect(TerminalConfigText.make(user: GhosttyUserConfig()).contains("font-size = 13\n"))
    }
}

@Suite struct Environment {
    @Test func overridesAndDefaults() {
        let e = AppEnvironment.resolve(arguments: ["app", "--socket", "/tmp/s.sock", "--hesperd=/bin/g", "--no-notifications"],
                                       environment: [:], home: "/h", executableDir: nil, fileExists: { _ in false })
        #expect(e.socketPath == "/tmp/s.sock" && e.hesperdPath == "/bin/g" && e.options.contains("no-notifications"))
        let d = AppEnvironment.resolve(arguments: ["app"], environment: ["HESPER_STATE_DIR": "/st", "PATH": "/x:/y"], home: "/h",
                                       executableDir: "/App/MacOS", fileExists: { $0 == "/y/hesperd" })
        #expect(d.socketPath == "/st/hesperd.sock" && d.hesperdPath == "/y/hesperd")
        #expect(d.attachEnvironment == ["HESPER_SOCKET": "/st/hesperd.sock", "HESPER_STATE_DIR": "/st"])
        let live = AppEnvironment.resolve(arguments: ["app"], environment: [:], home: "/h", executableDir: "/App/MacOS", fileExists: { $0 == "/App/MacOS/hesperd" })
        #expect(live.socketPath == "/h/.local/state/hesper/hesperd.sock" && live.hesperdPath == "/App/MacOS/hesperd")
        let env2 = AppEnvironment.resolve(arguments: ["app"], environment: ["HESPER_SOCKET": "/s2"], home: "/h", executableDir: nil, fileExists: { _ in false })
        #expect(env2.socketPath == "/s2")
    }

    @Test func attachArgv() {
        let e = AppEnvironment.resolve(arguments: ["app", "--hesperd", "/g"], environment: [:], home: "/h", executableDir: nil, fileExists: { _ in false })
        #expect(e.attachArgv(id: "L/a", readOnly: true, owner: false) == ["/g", "attach", "L/a", "--ro"])
        #expect(e.attachArgv(id: "L/a", readOnly: false, owner: true) == ["/g", "attach", "L/a", "--owner"])
        #expect(e.attachArgv(id: "L/a", readOnly: true, owner: true) == ["/g", "attach", "L/a", "--ro"])
        #expect(e.attachArgv(id: "L/a", readOnly: true, owner: false, view: true) == ["/g", "attach", "L/a", "--view"])
        #expect(e.attachArgv(id: "L/a", readOnly: true, owner: false, view: true, fit: true) == ["/g", "attach", "L/a", "--fit"])
    }
}
