import CoreGraphics
import Foundation
import HesperCore
import HesperFixtures
import HesperFleet
import Testing

/// Fit to iPhone: when the phone owns the Mac's terminal size, at which
/// grid, and when it falls back to the mirror.
@Suite struct TerminalSizingTests {
    let agent = Agent(id: "studio/s1", size: GridSize(cols: 97, rows: 56))
    let host = HostSizing(publishesOwner: true, rights: ["observe", "answer", "type"], device: "phone-1")
    // An iPhone 17 in portrait: 402 pt wide, an 18 pt cell (10.8 x 24 pt).
    let area = CGSize(width: 402, height: 600)
    let cell = CGSize(width: 10.8, height: 24)

    @Test func aCapableHostIsFittedToTheArea() {
        let plan = AttachPlan.make(agent, host: host, area: area, cell: cell)
        #expect(plan == AttachPlan(mode: .fitted, grid: GridSize(cols: 37, rows: 25)))
        #expect(plan.owner)
    }

    @Test func otherwiseTheMirrorAtTheMacsGrid() {
        let mirror = AttachPlan(mode: .mirror, grid: GridSize(cols: 97, rows: 56))
        // An older host: no capability.
        var old = host
        old.publishesOwner = false
        #expect(AttachPlan.make(agent, host: old, area: area, cell: cell) == mirror)
        // No `type` right.
        var watcher = host
        watcher.rights = ["observe"]
        #expect(AttachPlan.make(agent, host: watcher, area: area, cell: cell) == mirror)
        // An unknown Mac, or an area not measured yet.
        #expect(AttachPlan.make(agent, host: nil, area: area, cell: cell) == mirror)
        #expect(AttachPlan.make(agent, host: host, area: .zero, cell: cell) == mirror)
        #expect(!mirror.owner)
        // The Mac never reported a size: 80 x 24.
        #expect(AttachPlan.make(Agent(id: "mini/m3"), host: nil, area: area, cell: cell).grid == GridSize(cols: 80, rows: 24))
    }

    @Test func unknownRightsTryTheFit() {
        var unknown = host
        unknown.rights = []
        #expect(AttachPlan.make(agent, host: unknown, area: area, cell: cell).owner)
        #expect(AttachPlan.retriesAsMirror("forbidden") && AttachPlan.retriesAsMirror("unsupported"))
        #expect(!AttachPlan.retriesAsMirror("not_found") && !AttachPlan.retriesAsMirror("timeout"))
    }

    @Test func theGridIsWholeCellsAndNeverBelow20By5() {
        #expect(AttachPlan.fit(area: CGSize(width: 874, height: 300), cell: cell) == GridSize(cols: 80, rows: 12))
        #expect(AttachPlan.fit(area: CGSize(width: 100, height: 50), cell: cell) == GridSize(cols: 20, rows: 5))
        #expect(AttachPlan.fit(area: area, cell: .zero) == nil)
    }

    func fitted(_ device: String? = "phone-1") -> FitState {
        FitState(plan: AttachPlan(mode: .fitted, grid: GridSize(cols: 37, rows: 25)), device: device)
    }

    @Test func theAskedGridKeepsTheFit() {
        var s = fitted()
        do {
            let ok = !s.size(GridSize(cols: 37, rows: 25))
            #expect(ok)
        }
        #expect(s.isFitted && s.grid == GridSize(cols: 37, rows: 25))
    }

    @Test func aSizeNotAskedForIsTheMacTakingItBack() {
        var s = fitted()
        do {
            let ok = s.size(GridSize(cols: 143, rows: 40))
            #expect(ok)
        }
        #expect(!s.isFitted && s.grid == nil)
        // A mirror stays one.
        do {
            let ok = !s.size(GridSize(cols: 37, rows: 25))
            #expect(ok)
        }
        do {
            let ok = s.resize(to: GridSize(cols: 60, rows: 20)) == nil
            #expect(ok)
        }
    }

    @Test func aResizeIsAskedOnceAndItsOldGridMayStillArrive() {
        var s = fitted()
        // Turned to landscape: a new grid, sent once.
        do {
            let ok = s.resize(to: GridSize(cols: 80, rows: 12)) == GridSize(cols: 80, rows: 12)
            #expect(ok)
        }
        do {
            let ok = s.resize(to: GridSize(cols: 80, rows: 12)) == nil
            #expect(ok)
        }
        // The attach's own grid, still in flight, then the new one.
        do {
            let ok = !s.size(GridSize(cols: 37, rows: 25))
            #expect(ok)
        }
        do {
            let ok = !s.size(GridSize(cols: 80, rows: 12))
            #expect(ok)
        }
        // Once the new grid came back, the old one is the Mac's.
        do {
            let ok = s.size(GridSize(cols: 37, rows: 25))
            #expect(ok)
        }
        #expect(!s.isFitted)
    }

    @Test func theOwnerFieldTakesItBackOnlyAfterItNamedThisPhone() {
        var s = fitted()
        // Before hesperd names the phone: nothing to conclude.
        do {
            let ok = !s.owner(nil)
            #expect(ok)
        }
        do {
            let ok = !s.owner("phone-1")
            #expect(ok)
        }
        #expect(s.isFitted)
        // The Mac typed: cleared, or another device.
        do {
            let ok = s.owner(nil)
            #expect(ok)
        }
        #expect(!s.isFitted)
        var other = fitted()
        _ = other.owner("phone-1")
        do {
            let ok = other.owner("ipad-2")
            #expect(ok)
        }
        // Without its own id the phone cannot tell.
        var blind = fitted(nil)
        _ = blind.owner("phone-1")
        do {
            let ok = !blind.owner(nil)
            #expect(ok)
        }
    }

    @Test func theAgentDecodesItsSizeOwner() throws {
        let a = try JSONDecoder().decode(
            Agent.self, from: Data(#"{"id":"M/a1","state":"working","sizeOwner":"phone-1","sizeOwnerName":"iPhone"}"#.utf8))
        #expect(a.sizeOwner == "phone-1" && a.sizeOwnerName == "iPhone")
        let local = try JSONDecoder().decode(Agent.self, from: Data(#"{"id":"M/a1","state":"working"}"#.utf8))
        #expect(local.sizeOwner == nil && local.sizeOwnerName == nil)
    }

    @Test func theFixtureEchoesAnOwnerAttachsGrid() async throws {
        let link = FixtureLink(terminalTick: .seconds(60))
        #expect(link.sizing(on: "studio")?.canOwn == true)
        #expect(link.sizing(on: "mini")?.canOwn == false)
        #expect(link.sizing(on: "elsewhere") == nil)
        let owned = try await link.attach(agent: "studio/s1", cols: 37, rows: 25, owner: true)
        var it = owned.frames.makeAsyncIterator()
        #expect(await it.next() == .size(cols: 37, rows: 25))
        owned.resize(cols: 80, rows: 12)
        var sizes: [AttachFrame] = []
        while let f = await it.next() {
            if case .size = f {
                sizes.append(f)
                break
            }
        }
        #expect(sizes == [.size(cols: 80, rows: 12)])
        owned.close()
        // A mirror, and an owner attach on `mini`, get the Mac's grid.
        let mirror = try await link.attach(agent: "studio/s1", cols: 97, rows: 56)
        var m = mirror.frames.makeAsyncIterator()
        #expect(await m.next() == .size(cols: 97, rows: 56))
        mirror.close()
        let old = try await link.attach(agent: "mini/m2", cols: 37, rows: 25, owner: true)
        var o = old.frames.makeAsyncIterator()
        #expect(await o.next() == .size(cols: 97, rows: 56))
        old.close()
    }
}
