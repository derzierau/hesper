import Foundation
import Testing
@testable import HesperCore

private func laptop(at x: Double = 0, y: Double = 0, number: String = "1") -> DisplayInfo {
    DisplayInfo(vendor: 1552, model: 41_002, serial: 0, name: "Built-in Retina Display", builtin: true, sizeMM: (302, 196),
                frame: Rect(x: x, y: y, width: 1512, height: 982), visible: Rect(x: x, y: y, width: 1512, height: 950), number: number)
}

private func studio(at x: Double = 1512, y: Double = 0, number: String = "2", serial: UInt32 = 0x1234) -> DisplayInfo {
    DisplayInfo(vendor: 1552, model: 44_918, serial: serial, name: "Studio Display", builtin: false, sizeMM: (597, 336),
                frame: Rect(x: x, y: y, width: 2560, height: 1440), visible: Rect(x: x, y: y, width: 2560, height: 1415), number: number)
}

private func dell(at x: Double, serial: UInt32 = 0) -> DisplayInfo {
    DisplayInfo(vendor: 4268, model: 41_234, serial: serial, name: "DELL U2720Q", builtin: false, sizeMM: (597, 336),
                frame: Rect(x: x, y: 0, width: 1920, height: 1080))
}

private func wall(_ id: String, main: Bool = false, scope: WallScope = .all, screen: String? = nil) -> SavedWall {
    SavedWall(id: id, isMain: main, scope: scope, arrangement: .shelf, minChars: 80,
              frame: SavedFrame(x: 10, y: 10, width: 900, height: 600), screen: screen, fullScreen: false)
}

private func agentWin(_ id: String, group: Int? = nil, selected: Bool = false, screen: String? = nil) -> SavedAgentWindow {
    SavedAgentWindow(agent: id, frame: SavedFrame(x: 100, y: 100, width: 800, height: 500), screen: screen, tabGroup: group, selectedTab: selected, fullScreen: false)
}

@Suite struct DisplaySetupSignature {
    @Test func orderIndependent() {
        let a = DisplaySetup([laptop(), studio()])
        let b = DisplaySetup([studio(), laptop()])
        #expect(a.id == b.id)
        #expect(a.signature == b.signature)
        #expect(a.screens.map(\.key) == b.screens.map(\.key))
        #expect(a.id.hasPrefix("ds-") && a.id.count == 19)
    }

    @Test func survivesReconnect() {
        // New CGDirectDisplayIDs, the external made main (global origin
        // moves), a different resolution: still the same setup.
        let before = DisplaySetup([laptop(number: "1"), studio(number: "2")])
        var big = studio(at: 0, number: "7")
        big.frame = Rect(x: 0, y: 0, width: 3008, height: 1692)
        let after = DisplaySetup([laptop(at: -1512, y: 0, number: "9"), big])
        #expect(before.id == after.id)
    }

    @Test func changesOnPlugAndArrangement() {
        let office = DisplaySetup([laptop(), studio()])
        let alone = DisplaySetup([laptop()])
        let leftOf = DisplaySetup([laptop(), studio(at: -2560)])
        let above = DisplaySetup([laptop(), studio(at: -500, y: 982)])
        #expect(office.id != alone.id)
        #expect(office.id != leftOf.id)
        #expect(office.id != above.id)
        #expect(leftOf.id != above.id)
        // A different Studio Display (another serial) is another setup.
        #expect(office.id != DisplaySetup([laptop(), studio(serial: 0x9999)]).id)
    }

    @Test func identicalDisplaysWithoutSerialAreNumbered() {
        let s = DisplaySetup([dell(at: 1920), dell(at: 0)])
        #expect(Set(s.keys).count == 2)
        // The left one is the plain key, the right one "#2", whatever the order.
        #expect(s.keys[1].hasSuffix("1080mm") == false || !s.keys[1].contains("#"))
        #expect(s.keys[0].hasSuffix("#2"))
        #expect(DisplaySetup([dell(at: 0), dell(at: 1920)]).id == s.id)
    }

    @Test func identityFallsBackToNameAndSize() {
        #expect(laptop().identity == "v1552-m41002-built-in_retina_display-302x196mm")
        #expect(studio().identity == "v1552-m44918-s4660")
    }

    @Test func defaultNames() {
        #expect(DisplaySetup([laptop()]).defaultName == "Laptop only")
        #expect(DisplaySetup([studio(), laptop()]).defaultName == "Laptop + Studio Display")
        #expect(DisplaySetup([laptop(), studio(at: -2560)]).defaultName == "Studio Display + Laptop")
        #expect(DisplaySetup([studio(at: 0)]).defaultName == "Studio Display")
    }
}

@Suite struct DeskModel {
    let laptopSetup = DisplaySetup([laptop()])
    let office = DisplaySetup([laptop(), studio()])

    @Test func recordMakesOneAutomaticDeskPerSetup() {
        var b = DeskBook()
        let w1 = SavedWindows(walls: [wall("main", main: true)])
        b.record(w1, setup: laptopSetup, now: 1)
        b.record(SavedWindows(walls: [wall("main", main: true, scope: .overflow)]), setup: laptopSetup, now: 2)
        #expect(b.desks.count == 1)
        #expect(b.desks[0].automatic && b.desks[0].name == "Laptop only")
        #expect(b.currentDesk(for: laptopSetup.id)?.windows.walls[0].scope == .overflow)
        b.record(SavedWindows(walls: [wall("main", main: true), wall("w2", scope: .group("g-apps"))]), setup: office, now: 3)
        #expect(b.desks.count == 2)
        #expect(b.currentDesk(for: office.id)?.name == "Laptop + Studio Display")
        #expect(b.currentDesk(for: laptopSetup.id)?.windows.walls.count == 1)
        #expect(b.setups.count == 2)
    }

    @Test func saveAsAndSwitchWithinASetup() {
        var b = DeskBook()
        b.record(SavedWindows(walls: [wall("main", main: true)]), setup: office, now: 1)
        let deep = b.saveAs("Deep work", windows: SavedWindows(walls: [wall("main", main: true, scope: .project("p-app"))]), setup: office, now: 2)
        #expect(!deep.automatic)
        #expect(b.desks(for: office.id).map(\.name) == ["Laptop + Studio Display", "Deep work"])
        #expect(b.currentDesk(for: office.id)?.id == deep.id)
        // Changes now go into "Deep work"; the automatic desk is untouched.
        b.record(SavedWindows(walls: [wall("main", main: true, scope: .group("g"))]), setup: office, now: 3)
        #expect(b.desk(deep.id)?.windows.walls[0].scope == .group("g"))
        #expect(b.automaticDesk(for: office.id)?.windows.walls[0].scope == .all)
        // Back to the automatic desk.
        let auto = b.automaticDesk(for: office.id)!
        b.select(auto.id, setup: office, now: 4)
        #expect(b.currentDesk(for: office.id)?.id == auto.id)
        // Same name again replaces, rename, remove.
        b.saveAs("deep work", windows: SavedWindows(walls: [wall("main", main: true)]), setup: office, now: 5)
        #expect(b.desks(for: office.id).count == 2)
        b.rename(auto.id, to: "Office")
        #expect(b.automaticDesk(for: office.id)?.name == "Office")
        b.remove(auto.id)
        #expect(b.automaticDesk(for: office.id) != nil) // automatic desks stay
        b.remove(deep.id)
        #expect(b.desks(for: office.id).map(\.name) == ["Office"])
        #expect(b.currentDesk(for: office.id)?.id == auto.id)
    }

    @Test func roundTripAndBadFiles() throws {
        var b = DeskBook()
        b.record(SavedWindows(walls: [wall("main", main: true, scope: .filter(WallFilter(machines: ["M"])))],
                              agentWindows: [agentWin("L/a", group: 0, selected: true), agentWin("L/b", group: 0)]), setup: office, now: 1)
        let back = DeskBook.decode(try b.encoded(), setup: laptopSetup)
        #expect(back == b)
        #expect(DeskBook.decode(Data("{\"version\":3}".utf8), setup: office) == nil)
        #expect(DeskBook.decode(Data("nope".utf8), setup: office) == nil)
    }

    @Test func aDeskFromAnotherSetupIsCopiedWhenPicked() {
        var b = DeskBook()
        let d = b.saveAs("Deep work", windows: SavedWindows(walls: [wall("main", main: true, scope: .project("p"))]), setup: office, now: 1)
        let picked = b.select(d.id, setup: laptopSetup, now: 2)
        #expect(picked?.setup == laptopSetup.id && picked?.name == "Deep work")
        #expect(b.desk(d.id)?.setup == office.id)
    }
}

@Suite struct DeskMigration {
    @Test func windowsJSONBecomesTheCurrentSetupsDesk() throws {
        let old = SavedWindows(walls: [wall("main", main: true, scope: .overflow, screen: "2"), wall("wall-1", scope: .project("p-x"), screen: "99")],
                               agentWindows: [agentWin("L/a", screen: "1")])
        let displays = [laptop(number: "1"), studio(number: "2")]
        let setup = DisplaySetup(displays)
        let b = try #require(DeskBook.decode(try old.encoded(), setup: setup, displays: displays, now: 5))
        #expect(b.desks.count == 1)
        let d = b.desks[0]
        #expect(d.automatic && d.setup == setup.id && d.name == "Laptop + Studio Display")
        #expect(d.windows.walls.map(\.id) == ["main", "wall-1"])
        #expect(d.windows.walls[0].scope == .overflow)
        // Display numbers become stable keys; unknown ones stay.
        #expect(d.windows.walls[0].screen == "v1552-m44918-s4660")
        #expect(d.windows.walls[1].screen == "99")
        #expect(d.windows.agentWindows[0].screen == laptop().identity)
        #expect(b.currentDesk(for: setup.id)?.id == d.id)
    }
}

@Suite struct DeskFolding {
    let main = ScreenArea(id: "laptop", visible: Rect(x: 0, y: 0, width: 1512, height: 950))
    let office = SavedWindows(walls: [wall("wall-2", scope: .group("g")),
                                      SavedWall(id: "main", isMain: true, scope: .overflow, arrangement: .treemap, minChars: 60,
                                                frame: SavedFrame(x: 1600, y: 0, width: 2000, height: 1200), screen: "studio", fullScreen: true,
                                                grouping: .project, collapsed: ["g:x"], bandOrder: ["g:a"], sidebar: true)],
                              agentWindows: [agentWin("L/a", screen: "studio"), agentWin("L/b", group: 1, selected: true), agentWin("L/c", group: 1)])

    @Test func foldsIntoOneAllWallOnTheMainDisplay() {
        let f = DeskFold.fold(office, main: main, agentWindows: .tabs)
        #expect(f.walls.count == 1)
        let w = f.walls[0]
        #expect(w.id == "main" && w.isMain && w.scope == .all && !w.fullScreen)
        #expect(w.arrangement == .treemap && w.minChars == 60 && w.grouping == .project && w.sidebar == true)
        #expect(w.collapsed == nil)
        #expect(w.screen == "laptop" && w.frame.rect == main.visible)
        // Agent windows: one tab group on the main display, the selected tab kept.
        #expect(f.agentWindows.map(\.agent) == ["L/a", "L/b", "L/c"])
        #expect(Set(f.agentWindows.map(\.tabGroup)) == [0])
        #expect(f.agentWindows.filter(\.selectedTab).map(\.agent) == ["L/b"])
        #expect(f.agentWindows.allSatisfy { $0.screen == "laptop" && $0.frame.x >= 0 && $0.frame.x + $0.frame.width <= 1512 })
    }

    @Test func foldClosingAgentWindows() {
        let f = DeskFold.fold(office, main: main, agentWindows: .close)
        #expect(f.agentWindows.isEmpty && f.walls.count == 1)
    }

    @Test func oneAgentWindowIsNoTabGroup() {
        let f = DeskFold.fold(SavedWindows(walls: [], agentWindows: [agentWin("L/a")]), main: main, agentWindows: .tabs)
        #expect(f.walls.map(\.id) == ["main"])
        #expect(f.agentWindows.first?.tabGroup == nil && f.agentWindows.first?.selectedTab == true)
    }

    @Test func clampedOntoTheNewSetup() {
        // A frame saved on a display that is gone lands on the main one.
        let r = FrameClamp.clamp(Rect(x: 3000, y: 200, width: 2400, height: 1300), screen: "studio", screens: [main])
        #expect(r.x >= 0 && r.maxX <= 1512 && r.maxY <= 950 && r.width == 1512)
    }
}

@Suite struct HomeWallRule {
    @Test func firstWallIsHomeAndMakeHomeMovesItFirst() {
        var b = DeskBook()
        let s = DisplaySetup([laptop(), studio()])
        let d = b.record(SavedWindows(walls: [wall("main", main: true), wall("w2", scope: .overflow), wall("w3")]), setup: s, now: 1)
        #expect(d.home == "main")
        b.setHome("w3", desk: d.id)
        #expect(b.desk(d.id)?.home == "w3")
        #expect(b.desk(d.id)?.windows.walls.map(\.id) == ["w3", "main", "w2"])
        b.setHome("nope", desk: d.id)
        #expect(b.desk(d.id)?.home == "w3")
        #expect(HomeRule.order(["main", "w2", "w3"], home: "w2") == ["w2", "main", "w3"])
        #expect(HomeRule.order(["main", "w2"], home: nil) == ["main", "w2"])
    }
}

/// Projects with their own wall: pointers on the other walls.
@Suite struct OwnWallPointers {
    let v = ProjectsView()
    var c: ProjectCatalog { ProjectsView.catalog }
    var agents: [Agent] {
        [v.agent("1", "/p/acme-apps"), v.agent("2", "/p/acme-apps/apps/ios-app"), v.agent("3", "/p/hesper"),
         v.agent("4", "/p/design-system"), v.agent("5", "/p/acme-apps", state: .approval), v.agent("6", "/s/trial")]
    }
    func dist(_ walls: [WallSlot]) -> Distribution {
        ScopeMath.distribute(agents, walls: walls, place: v.place, unitOrder: ViewResolver.unitOrder(catalog: c))
    }

    @Test func visibleProjectWallBecomesAPointer() {
        let d = dist([WallSlot(id: "main", scope: .all, capacity: 20), WallSlot(id: "w-as", scope: .project("as"), capacity: 20)])
        // No acme-apps tiles on main (package included, the approval too).
        #expect(d.agents["main"]?.map(\.id) == ["L/3", "L/4", "L/6"])
        #expect(d.pointers["main"] == [WallPointer(wall: "w-as", scope: .project("as"), agents: ["L/1", "L/2", "L/5"])])
        #expect(d.agents["w-as"]?.map(\.id) == ["L/1", "L/2", "L/5"])
        #expect(d.pointers["w-as"] == nil, "a project wall never points")
    }

    @Test func minimizedClosedOrUnpluggedBringsTheBandBack() {
        // Minimized / hidden / on a display that is gone: not visible.
        let hidden = dist([WallSlot(id: "main", scope: .all, capacity: 20), WallSlot(id: "w-as", scope: .project("as"), capacity: 20, visible: false)])
        #expect(hidden.agents["main"]?.count == 6 && hidden.pointers.isEmpty)
        // Closed: not among the walls at all.
        let closed = dist([WallSlot(id: "main", scope: .all, capacity: 20)])
        #expect(closed.agents["main"]?.count == 6 && closed.pointers.isEmpty)
    }

    @Test func settingValues() throws {
        let walls = { (m: OwnWallMode) in [WallSlot(id: "main", scope: .all, capacity: 20, ownWalls: m), WallSlot(id: "w-g", scope: .group("g2"), capacity: 20)] }
        let full = dist(walls(.full))
        #expect(full.agents["main"]?.count == 6 && full.pointers.isEmpty)
        let hid = dist(walls(.hidden))
        #expect(hid.agents["main"]?.map(\.id).contains("L/3") == false && hid.pointers.isEmpty)
        let col = dist(walls(.collapsed))
        #expect(col.pointers["main"]?.first?.agents == ["L/3"])
        #expect(OwnWallMode.allCases.map(\.rawValue) == ["collapsed", "full", "hidden"])
        // Saved per wall (nil = collapsed).
        let w = SavedWall(id: "w", isMain: true, scope: .all, arrangement: .shelf, minChars: 80, frame: SavedFrame(x: 0, y: 0, width: 1, height: 1),
                          screen: nil, fullScreen: false, ownWalls: .hidden)
        #expect(try JSONDecoder().decode(SavedWall.self, from: JSONEncoder().encode(w)).ownWalls == .hidden)
    }

    @Test func pointerRules() {
        #expect(OwnWalls.points(.all, to: .project("as")) && OwnWalls.points(.overflow, to: .group("g1")))
        #expect(OwnWalls.points(.group("g1"), to: .project("as")))
        #expect(!OwnWalls.points(.group("g1"), to: .group("g2")) && !OwnWalls.points(.project("as"), to: .group("g1")))
        #expect(!OwnWalls.points(.project("as"), to: .project("as")) && !OwnWalls.points(.all, to: .overflow))
        // A group wall points to a project wall inside it.
        let d = dist([WallSlot(id: "g", scope: .group("g1"), capacity: 20), WallSlot(id: "p", scope: .project("as"), capacity: 20)])
        #expect(d.agents["g"]?.map(\.id) == ["L/4"] && d.pointers["g"]?.first?.wall == "p")
    }

    @Test func pointersTakeNoCapacityInTheOverflowChain() {
        // acme apps (acme-apps 3 + edition 1) has its own group wall.
        let walls = [WallSlot(id: "main", scope: .all, capacity: 2), WallSlot(id: "o1", scope: .overflow, capacity: 2),
                     WallSlot(id: "w-g", scope: .group("g1"), capacity: 20)]
        let d = dist(walls)
        let chain = (d.agents["main"] ?? []) + (d.agents["o1"] ?? [])
        #expect(Set(chain.map(\.id)) == ["L/3", "L/6"], "only tools and scratch fill the chain")
        #expect(d.pointers["main"]?.first?.agents == ["L/1", "L/2", "L/4", "L/5"], "the pointer sits on the chain's head")
        #expect(d.pointers["o1"] == nil)
        // The approval in acme-apps is not forced home (it is on its own
        // wall; the home strip covers it).
        #expect(d.agents["main"]?.contains { $0.id == "L/5" } == false)
    }

    @Test func pointerBandsSitWhereTheirBandWas() {
        let d = dist([WallSlot(id: "main", scope: .all, capacity: 20), WallSlot(id: "w-as", scope: .project("as"), capacity: 20)])
        let shown = d.agents["main"]!, p = d.pointers["main"]!
        let items = { (a: [Agent]) in a.map { ViewItem(id: $0.id, projectID: c.projectID(for: $0)) } }
        let all = agents
        let pitems = p.map { ptr in (pointer: ptr, items: items(all.filter { ptr.agents.contains($0.id) })) }
        // By project: the pointer where acme-apps' band was (acme apps first).
        let byProject = ViewResolver.resolve(items(shown), level: .project, catalog: c, pointers: pitems)
        #expect(byProject.bands.map(\.key) == ["ptr:w-as", "p:ed", "p:gh", "p:~scratch"], "the scratch tr in the one Scratch band")
        let ptr = byProject.bands[0]
        #expect(ptr.title == "acme-apps" && ptr.members.isEmpty && ptr.counted == ["L/1", "L/2", "L/5"] && ptr.colorHex == "#7aa2f7")
        #expect(byProject.showsBands && !byProject.order.contains("L/1"))
        // By group: right after the acme apps band (edition stays in it).
        let byGroup = ViewResolver.resolve(items(shown), level: .group, catalog: c, pointers: pitems)
        #expect(byGroup.bands.map(\.key) == ["g:g1", "ptr:w-as", "g:g2", "g:~scratch"])
        // One band left plus a pointer: bands are drawn.
        let one = ViewResolver.resolve(items([all[2]]), level: .project, catalog: c, pointers: pitems)
        #expect(one.showsBands && one.bands.count == 2)
        #expect(HomeWall.reservesStrip(scope: .all, collapsedBands: 0, pointers: 1))
    }
}
