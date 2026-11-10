import Foundation
import Testing
@testable import HesperCore

/// The garbled non-key panes: a read-write pane that did not own the size
/// got the PTY's raw stream at another grid. One owner per agent; every
/// other pane is a view.
@Suite struct SizeOwnerRule {
    typealias C = SizeOwner.Candidate
    // Windows: 1 = the main wall (key), 2 = wall 2, 3 = an agent window.
    func c(_ agent: String, _ window: Int?, focus: Bool = false, rw: Bool = false) -> C {
        C(agentID: agent, window: window, focusRole: focus, readWrite: focus || rw)
    }

    @Test func onlyTheKeyWindowsReadWriteTerminalOwns() {
        let cs = [
            c("a", 1),               // a plain tile of a in the key wall
            c("a", 2, rw: true),     // a's active tile in wall 2
            c("a", 3, focus: true),  // a's agent window
            c("b", 1, rw: true),     // b's active tile in the key wall
        ]
        #expect(SizeOwner.owners(cs, ownerWindow: 1) == [false, false, false, true])
        #expect(SizeOwner.owners(cs, ownerWindow: 3) == [false, false, true, false])
        #expect(SizeOwner.owners(cs, ownerWindow: 2) == [false, true, false, false])
    }

    @Test func atMostOneOwnerPerAgentEvenInOneWindow() {
        // The same agent twice in the key window (a wall's focus view and
        // its tile still marked active): the focus view wins, one owner.
        let cs = [c("a", 1, rw: true), c("a", 1, focus: true), c("a", 1, rw: true)]
        let o = SizeOwner.owners(cs, ownerWindow: 1)
        #expect(o == [false, true, false])
        // Without a focus view: the first listed.
        #expect(SizeOwner.owners([c("a", 1, rw: true), c("a", 1, rw: true)], ownerWindow: 1) == [true, false])
    }

    @Test func noOwnerWindowNoOwner() {
        // Before any of our windows is key: nobody owns, the fit decides.
        #expect(SizeOwner.owners([c("a", 1, focus: true), c("b", 2, rw: true)], ownerWindow: nil) == [false, false])
        #expect(SizeOwner.owners([c("a", nil, focus: true)], ownerWindow: 1) == [false])
    }

    @Test func everyAgentHasExactlyOneOwnerAcrossManyWindows() {
        // Three walls and two agent windows, each agent shown several times.
        var cs: [C] = []
        for w in 1...5 {
            for agent in ["a", "b", "c", "d"] {
                cs.append(c(agent, w))                          // tiles
                cs.append(c(agent, w, rw: agent == "a"))        // a active everywhere
            }
            cs.append(c("b", w, focus: true))                   // b focused everywhere
        }
        for key in 1...5 {
            let o = SizeOwner.owners(cs, ownerWindow: key)
            for agent in ["a", "b", "c", "d"] {
                let n = zip(cs, o).filter { $0.0.agentID == agent && $0.1 }.count
                #expect(n == (agent == "a" || agent == "b" ? 1 : 0), "agent \(agent) owners \(n) with window \(key) key")
            }
            #expect(zip(cs, o).allSatisfy { !$0.1 || $0.0.window == key })
        }
    }

    @Test func readWriteOnlyAsOwner() {
        // The bug: rw without owner (a raw stream at the PTY's grid in a
        // pane of another grid). Never again: non-owners are views.
        #expect(SizeOwner.attach(readWrite: true, owns: true) == .owner)
        #expect(SizeOwner.attach(readWrite: true, owns: false) == .view)
        #expect(SizeOwner.attach(readWrite: false, owns: true) == .view)
        #expect(SizeOwner.attach(readWrite: false, owns: false) == .view)
        let e = AppEnvironment.resolve(arguments: ["app", "--hesperd", "/g"], environment: [:], home: "/h", executableDir: nil, fileExists: { _ in false })
        #expect(e.attachArgv(id: "L/a", .owner) == ["/g", "attach", "L/a", "--owner"])
        #expect(e.attachArgv(id: "L/a", .view) == ["/g", "attach", "L/a", "--fit"])
    }
}

/// A tile's terminal area at the padded layout: the grid it asks the PTY
/// for (attach --fit) is exactly the cells of the area it is given, inside
/// the body (padding included), so a view's own grid matches its fit.
@Suite struct TileGridMatchesAttach {
    @Test func paddedTerminalAreaIsTheFitGrid() {
        let spec = WallSpec.wall(.comfortable)
        let cell = CellSize(width: 7.2, height: 15)
        for w in stride(from: 300.0, through: 1600, by: 37) {
            for h in stride(from: 200.0, through: 900, by: 41) {
                let card = Rect(x: 10, y: 20, width: w, height: h)
                let t = WallLayout.fit(WallTileInput(grid: GridSize(cols: 200, rows: 60)), card: card, quiet: false, spec: spec, cell: { _ in cell })
                guard let term = t.terminal, let body = t.body else { Issue.record("no terminal"); return }
                #expect(t.cols == Int((term.width / cell.width + 1e-9).rounded(.down)))
                #expect(abs(term.height - Double(t.rows) * cell.height) < 1e-9)
                #expect(term.x >= body.x + spec.padX - 1e-9 && term.maxX <= body.maxX - spec.padX + 1e-9)
                #expect(term.y >= body.y + spec.padY - 1e-9 && term.maxY <= body.maxY - spec.padY + 1e-9)
            }
        }
    }
}

/// Fit to iPhone (docs/rebuild-contract.md "Size rule with several
/// windows"): while another device owns an agent's PTY size,
/// every local pane of that agent is a view; a keystroke or a click in one
/// takes the size back for that pane at once.
@Suite struct SizeOwnerRemote {
    typealias C = SizeOwner.Candidate
    func c(_ agent: String, _ window: Int?, focus: Bool = false, rw: Bool = false, remote: Bool = false, takingBack: Bool = false) -> C {
        C(agentID: agent, window: window, focusRole: focus, readWrite: focus || rw, remoteOwned: remote, takingBack: takingBack)
    }

    @Test func aKeyTakesBackOnlyTheTerminalItTargets() {
        let cs = [c("a", 1, focus: true, remote: true), c("b", 1, rw: true, remote: true)]
        #expect(SizeOwner.keyTakeBackTarget(cs, targeted: [false, false]) == nil) // another control has focus
        #expect(SizeOwner.keyTakeBackTarget(cs, targeted: [false, true]) == 1)
        #expect(SizeOwner.keyTakeBackTarget(cs, targeted: [true, false]) == 0)
    }

    @Test func aRemoteOwnerMakesKeyAndNonKeyPanesViews() {
        let cs = [
            c("a", 1, focus: true, remote: true),  // a's focus view in the key window
            c("a", 1, rw: true, remote: true),     // a's active tile in the key window
            c("a", 2, focus: true, remote: true),  // a's agent window (not key)
            c("b", 1, rw: true),                   // b: no remote owner
        ]
        #expect(SizeOwner.owners(cs, ownerWindow: 1) == [false, false, false, true])
        #expect(SizeOwner.owners(cs, ownerWindow: 2) == [false, false, false, false])
        for (cand, owns) in zip(cs, SizeOwner.owners(cs, ownerWindow: 1)) where cand.agentID == "a" {
            #expect(SizeOwner.attach(readWrite: cand.readWrite, owns: owns) == .view)
        }
    }

    @Test func aKeystrokeOrClickTakesTheSizeBackForThatPane() {
        // The tile was typed in (or clicked): it owns at once, the other
        // panes of the agent stay views (latest owner wins).
        let cs = [
            c("a", 1, focus: true, remote: true),
            c("a", 1, rw: true, remote: true, takingBack: true),
            c("a", 2, focus: true, remote: true),
        ]
        #expect(SizeOwner.owners(cs, ownerWindow: 1) == [false, true, false])
        // Even before the owner window caught up (the pane typed in owns).
        #expect(SizeOwner.owners(cs, ownerWindow: 2) == [false, true, false])
        #expect(SizeOwner.owners(cs, ownerWindow: nil) == [false, true, false])
        // A pane that would not type (a plain tile) never owns.
        #expect(SizeOwner.owners([c("a", 1, remote: true, takingBack: true)], ownerWindow: 1) == [false])
        // Two panes taken back: one owner, the focus view first.
        let two = [c("a", 1, rw: true, remote: true, takingBack: true), c("a", 1, focus: true, remote: true, takingBack: true)]
        #expect(SizeOwner.owners(two, ownerWindow: 1) == [false, true])
    }

    @Test func clearedOwnerFallsBackToTheNormalRule() {
        // The daemon cleared sizeOwner: the key window's pane owns again.
        let cs = [c("a", 1, focus: true), c("a", 2, focus: true)]
        #expect(SizeOwner.owners(cs, ownerWindow: 1) == [true, false])
        var a = Agent(id: "L/a")
        #expect(!SizeOwner.remoteOwned(a, local: true))
        a.sizeOwner = ""
        #expect(!SizeOwner.remoteOwned(a, local: true))
        a.sizeOwner = "dev-1"
        #expect(SizeOwner.remoteOwned(a, local: true))
        #expect(SizeOwner.remoteOwned(a, local: false))
    }

    @Test func theAgentsOwnMacOwningIsRemoteOnlyOnAnotherMac() {
        // Mac B's tile owned Mac A's agent; Mac A took the size back. Mac
        // B's hesperd strips its own device id, so only sizeOwnerHost
        // tells Mac B it lost the size: its panes must become views.
        var a = Agent(id: "A/x")
        a.sizeOwnerHost = true
        #expect(SizeOwner.remoteOwned(a, local: false))
        #expect(SizeOwner.owner(a, local: false) == SizeOwner.hostOwner)
        // On Mac A it is its own owner attach: the normal rule.
        #expect(!SizeOwner.remoteOwned(a, local: true))
        // Mac B types: its owner attach clears the host owner, and Mac B's
        // take-back gives way to the normal rule (it owns in its key window).
        #expect(!SizeOwner.keepsTakeBack(old: SizeOwner.owner(a, local: false), new: nil))
    }

    @Test func aTakeBackLastsWhileTheSameRemoteOwnerIsShown() {
        // Kept until the daemon reports a change (it clears sizeOwner once
        // the Mac's owner attach lands); a new remote owner starts over.
        #expect(SizeOwner.keepsTakeBack(old: "dev-1", new: "dev-1"))
        #expect(!SizeOwner.keepsTakeBack(old: "dev-1", new: nil))
        #expect(!SizeOwner.keepsTakeBack(old: "dev-1", new: ""))
        #expect(!SizeOwner.keepsTakeBack(old: "dev-1", new: "dev-2"))
        #expect(!SizeOwner.keepsTakeBack(old: nil, new: "dev-1"))
    }

    @Test func badgeNamesTheDevice() {
        var a = Agent(id: "L/a")
        #expect(SizeOwner.badge(a, local: true, machine: "mini") == nil)
        a.sizeOwner = "dev-1"
        // No name (enforcement off): no guess at the kind of device.
        #expect(SizeOwner.badge(a, local: true, machine: "mini") == "On another device")
        a.sizeOwnerName = "Work iPhone"
        #expect(SizeOwner.badge(a, local: true, machine: "mini") == "On Work iPhone")
        a.sizeOwner = nil
        #expect(SizeOwner.badge(a, local: true, machine: "mini") == nil)
        // The agent's own Mac owns it: named on another Mac, none at home.
        a.sizeOwnerHost = true
        #expect(SizeOwner.badge(a, local: false, machine: "mini") == "On mini")
        #expect(SizeOwner.badge(a, local: true, machine: "mini") == nil)
    }

    @Test func decodesTheOwnerFieldsAndToleratesTheirAbsence() throws {
        let old = try JSONDecoder().decode(Agent.self, from: Data(#"{"id":"L/1","state":"working"}"#.utf8))
        #expect(old.sizeOwner == nil && old.sizeOwnerName == nil)
        let json = #"{"id":"L/1","state":"working","sizeOwner":"c-7f","sizeOwnerName":"iPhone"}"#
        let a = try JSONDecoder().decode(Agent.self, from: Data(json.utf8))
        #expect(a.sizeOwner == "c-7f")
        #expect(a.sizeOwnerName == "iPhone")
        #expect(SizeOwner.remoteOwned(a, local: true))
        #expect(!a.sizeOwnerHost)
        let host = try JSONDecoder().decode(Agent.self, from: Data(#"{"id":"L/1","sizeOwnerHost":true}"#.utf8))
        #expect(host.sizeOwnerHost && host.sizeOwner == nil)
        #expect(try JSONDecoder().decode(Agent.self, from: try JSONEncoder().encode(host)) == host)
        let empty = try JSONDecoder().decode(Agent.self, from: Data(#"{"id":"L/1","state":"working","sizeOwner":"","sizeOwnerName":""}"#.utf8))
        #expect(empty.sizeOwner == nil && empty.sizeOwnerName == nil)
        let round = try JSONDecoder().decode(Agent.self, from: try JSONEncoder().encode(a))
        #expect(round == a)
    }
}
