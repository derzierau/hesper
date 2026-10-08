import Foundation
import Testing
@testable import HesperCore

private func ag(_ id: String, _ state: AgentState = .working, machine: String = "L", kind: String = "claude", project: String? = "/p/app") -> Agent {
    Agent(id: id, machine: machine, kind: kind, name: id, state: state, project: project)
}

@Suite struct ScopeDistribution {
    let agents = (1...10).map { ag("L/a\($0)") }

    @Test func singleWallShowsEveryone() {
        let d = ScopeMath.distribute(agents, walls: [WallSlot(id: "main", scope: .all, capacity: 4)])
        #expect(d["main"]?.count == 10)
    }

    @Test func overflowContinuesWhereTheFirstWallIsFull() {
        let d = ScopeMath.distribute(agents, walls: [WallSlot(id: "main", scope: .all, capacity: 4),
                                                     WallSlot(id: "w2", scope: .overflow, capacity: 3),
                                                     WallSlot(id: "w3", scope: .overflow, capacity: 2)])
        #expect(d["main"]?.map(\.id) == ["L/a1", "L/a2", "L/a3", "L/a4"])
        #expect(d["w2"]?.map(\.id) == ["L/a5", "L/a6", "L/a7"])
        // The last wall of the chain takes the rest (scrolls).
        #expect(d["w3"]?.map(\.id) == ["L/a8", "L/a9", "L/a10"])
    }

    @Test func attentionAlwaysStaysOnTheFirstWall() {
        var list = agents
        list[7].state = .approval
        list[8].state = .question
        list[9].state = .error
        let d = ScopeMath.distribute(list, walls: [WallSlot(id: "main", scope: .all, capacity: 4),
                                                   WallSlot(id: "w2", scope: .overflow, capacity: 4)])
        // Three urgent agents plus the first quiet one, still in wall order.
        #expect(d["main"]?.map(\.id) == ["L/a1", "L/a8", "L/a9", "L/a10"])
        #expect(d["w2"]?.map(\.id) == ["L/a2", "L/a3", "L/a4", "L/a5", "L/a6", "L/a7"])
    }

    @Test func attentionBeyondCapacityStillStaysFirst() {
        var list = agents
        for i in 4..<10 { list[i].state = .approval }
        let d = ScopeMath.distribute(list, walls: [WallSlot(id: "main", scope: .all, capacity: 3),
                                                   WallSlot(id: "w2", scope: .overflow, capacity: 3)])
        #expect(d["main"]?.count == 6)
        #expect(d["main"]?.allSatisfy { $0.state == .approval } == true)
        #expect(d["w2"]?.map(\.id) == ["L/a1", "L/a2", "L/a3", "L/a4"])
    }

    @Test func mirrorsAndFiltersSitOutsideTheChain() {
        var list = agents
        list[0].machine = "M"
        list[1].kind = "codex"
        list[2].state = .done
        let f = WallFilter(machines: ["M"])
        let d = ScopeMath.distribute(list, walls: [WallSlot(id: "main", scope: .all, capacity: 4),
                                                   WallSlot(id: "mirror", scope: .all, capacity: 1),
                                                   WallSlot(id: "f", scope: .filter(f), capacity: 1),
                                                   WallSlot(id: "o", scope: .overflow, capacity: 1)])
        #expect(d["mirror"]?.count == 10)
        #expect(d["f"]?.map(\.id) == ["L/a1"])
        #expect(d["main"]?.count == 4)
        #expect(d["o"]?.count == 6)
    }

    @Test func overflowWithoutAnAllHeadIsItsOwnChain() {
        let d = ScopeMath.distribute(agents, walls: [WallSlot(id: "main", scope: .filter(WallFilter(kinds: ["codex"])), capacity: 4),
                                                     WallSlot(id: "o", scope: .overflow, capacity: 2)])
        #expect(d["main"]?.isEmpty == true)
        #expect(d["o"]?.count == 10)
    }

    @Test func filtersCombine() {
        let list = [ag("L/1", .approval, kind: "codex", project: "/x"), ag("L/2", .working, kind: "codex", project: "/x"),
                    ag("M/3", .approval, machine: "M", kind: "codex", project: "/x"), ag("L/4", .approval, kind: "claude", project: "/x"),
                    ag("L/5", .question, kind: "codex", project: "/y")]
        let f = WallFilter(projects: ["/x"], machines: ["L"], kinds: ["codex"], states: [.needsYou])
        #expect(list.filter(f.matches).map(\.id) == ["L/1"])
        let wide = WallFilter(kinds: ["codex"], states: [.needsYou, .working])
        #expect(list.filter(wide.matches).map(\.id) == ["L/1", "L/2", "M/3", "L/5"])
        #expect(WallFilter().isEmpty && list.filter(WallFilter().matches).count == 5)
        #expect(StateGroup(.idle) == .quiet && StateGroup(.starting) == .working && StateGroup(.exited) == .ended)
    }

    @Test func defaultScopeForANewWall() {
        #expect(ScopeMath.defaultScope(firstWallCapacity: 6, agentCount: 6) == .all)
        #expect(ScopeMath.defaultScope(firstWallCapacity: 6, agentCount: 7) == .overflow)
    }

    @Test func capacityCountsMinimumCards() {
        let spec = WallSpec()
        let min = (width: 400.0, height: 200.0)
        // (1000 - 40 + 14) / 414 = 2 cols; (700 - 40 + 14) / 214 = 3 rows.
        #expect(WallCapacity.count(width: 1000, height: 700, minCard: min, spec: spec, arrangement: .shelf) == 6)
        #expect(WallCapacity.count(width: 1000, height: 700, minCard: min, spec: spec, arrangement: .columns) == 2)
        #expect(WallCapacity.count(width: 100, height: 100, minCard: min, spec: spec, arrangement: .shelf) == 1)
    }
}

@Suite struct Routing {
    @Test func ownWindowThenFrontmostWallThenMain() {
        let walls: [(id: String, shows: Set<String>)] = [("w3", ["L/b"]), ("w2", ["L/a", "L/b"]), ("main", ["L/a"])]
        #expect(WindowRouting.route(agent: "L/a", agentWindows: ["L/a"], walls: walls, mainWall: "main") == .agentWindow("L/a"))
        #expect(WindowRouting.route(agent: "L/a", agentWindows: ["L/x"], walls: walls, mainWall: "main") == .wall("w2"))
        #expect(WindowRouting.route(agent: "L/b", agentWindows: [], walls: walls, mainWall: "main") == .wall("w3"))
        #expect(WindowRouting.route(agent: "L/z", agentWindows: [], walls: walls, mainWall: "main") == .mainWall("main"))
    }
}

@Suite struct AgentWindowUniqueness {
    final class W {}

    @Test func secondOpenReturnsTheSameWindow() {
        var book = AgentWindowBook<W>()
        var made = 0
        let first = book.openOrFocus("L/a") { made += 1; return W() }
        let second = book.openOrFocus("L/a") { made += 1; return W() }
        #expect(first.created && !second.created && first.handle === second.handle && made == 1)
        let other = book.openOrFocus("L/b") { made += 1; return W() }
        #expect(other.created && made == 2 && book.agents == ["L/a", "L/b"])
        book.remove(handle: first.handle)
        #expect(book.handle(for: "L/a") == nil)
        let again = book.openOrFocus("L/a") { made += 1; return W() }
        #expect(again.created && made == 3)
    }
}

@Suite struct Restoration {
    let sample = SavedWindows(
        walls: [SavedWall(id: "main", isMain: true, scope: .all, arrangement: .shelf, minChars: 80,
                          frame: SavedFrame(x: 0, y: 0, width: 1400, height: 900), screen: "1", fullScreen: false),
                SavedWall(id: "w2", isMain: false, scope: .filter(WallFilter(machines: ["M"], states: [.needsYou])), arrangement: .columns,
                          minChars: 60, frame: SavedFrame(x: 2000, y: 100, width: 1200, height: 800), screen: "2", fullScreen: true)],
        agentWindows: [SavedAgentWindow(agent: "L/a", frame: SavedFrame(x: 10, y: 10, width: 800, height: 600), screen: "1", tabGroup: 0, selectedTab: true, fullScreen: false),
                       SavedAgentWindow(agent: "L/gone", frame: SavedFrame(x: 10, y: 10, width: 800, height: 600), screen: "1", tabGroup: 0, selectedTab: false, fullScreen: false)])

    @Test func roundTrips() throws {
        let d = try sample.encoded()
        #expect(SavedWindows.decode(d) == sample)
        #expect(SavedWindows.decode(Data("{\"version\":9}".utf8)) == nil)
        #expect(SavedWindows.decode(Data("garbage".utf8)) == nil)
    }

    @Test func removedAgentsDontComeBack() {
        let kept = sample.keeping(agents: ["L/a", "L/other"])
        #expect(kept.agentWindows.map(\.agent) == ["L/a"])
        #expect(kept.walls.count == 2)
    }

    @Test func framesClampToTheScreensThereAre() {
        let laptop = ScreenArea(id: "1", visible: Rect(x: 0, y: 0, width: 1512, height: 944))
        let external = ScreenArea(id: "2", visible: Rect(x: 1512, y: 0, width: 2560, height: 1415))
        let f = Rect(x: 2000, y: 100, width: 1200, height: 800)
        // Display still there: unchanged.
        #expect(FrameClamp.clamp(f, screen: "2", screens: [laptop, external]) == f)
        // External display gone: moved onto the laptop and shrunk to fit.
        let onLaptop = FrameClamp.clamp(Rect(x: 2000, y: 100, width: 1800, height: 1200), screen: "2", screens: [laptop])
        #expect(onLaptop == Rect(x: 0, y: 0, width: 1512, height: 944))
        // Unknown display id: the one it overlaps most.
        let half = FrameClamp.clamp(Rect(x: 1400, y: 50, width: 800, height: 600), screen: "9", screens: [laptop, external])
        #expect(half.x >= 1512 && half.maxX <= external.visible.maxX)
        // Partly off the bottom left: moved fully on screen.
        let off = FrameClamp.clamp(Rect(x: -300, y: -200, width: 800, height: 600), screen: "1", screens: [laptop])
        #expect(off == Rect(x: 0, y: 0, width: 800, height: 600))
        // Tiny frames grow to a usable minimum.
        #expect(FrameClamp.clamp(Rect(x: 10, y: 10, width: 50, height: 50), screen: "1", screens: [laptop]).width == 360)
    }
}
