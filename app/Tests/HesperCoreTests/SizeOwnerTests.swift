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
