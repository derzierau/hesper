import Foundation
import Testing
@testable import HesperCore

/// PaneInput: held input and the size take-back of one terminal pane.
@Suite struct PaneInputTests {
    @Test func heldKeysGoFirstToANewOwnerSurfaceThenLaterKeysFollow() {
        // A and B are held while the take-back waits; the owner surface
        // arrives by re-creation (not a swap): it gets A, B before C.
        var p = PaneInput<String>()
        let r1 = p.beginTakeBack(at: 10, ownerCanCome: true)
        #expect(r1)
        p.hold("A", at: 10)
        p.hold("B", at: 10.1)
        let r2 = p.surfaceCurrent(acceptsInput: true, now: 10.2)
        #expect(r2 == ["A", "B"])
        // C: the owner takes input now, so it goes there directly.
        let c = SizeOwner.Candidate(agentID: "a", window: 1, focusRole: true, readWrite: true, remoteOwned: true, takingBack: p.isTakingBack)
        #expect(SizeOwner.keyTakeBack([c], targeted: [true], ownerReady: [true], ownerCanCome: [true], typesIntoAgent: true) == .pass)
        #expect(p.heldCount == 0)
    }

    @Test func noTakeBackWhenNoOwnerSurfaceCanCome() {
        var p = PaneInput<String>()
        let r3 = p.beginTakeBack(at: 10, ownerCanCome: false)
        #expect(!r3)
        #expect(!p.isTakingBack)
        // So the key decision passes the key on, as before the feature.
        let c = SizeOwner.Candidate(agentID: "a", window: 1, focusRole: true, readWrite: true, remoteOwned: true)
        #expect(SizeOwner.keyTakeBack([c], targeted: [true], ownerReady: [false], ownerCanCome: [false], typesIntoAgent: true) == .pass)
    }

    @Test func aViewSurfaceKeepsTheKeysOfAPendingTakeBack() {
        var p = PaneInput<String>()
        _ = p.beginTakeBack(at: 10, ownerCanCome: true)
        p.hold("A", at: 10)
        // A re-created view comes first (the owner swap follows).
        let r4 = p.surfaceCurrent(acceptsInput: false, now: 10.1)
        #expect(r4 == [])
        let r5 = p.surfaceCurrent(acceptsInput: true, now: 10.3)
        #expect(r5 == ["A"])
    }

    @Test func aViewSurfaceDropsHeldClicksWithoutATakeBack() {
        // An active tile's clicks were held for a read-write surface that
        // did not come: dropped, as before.
        var p = PaneInput<String>()
        p.hold("click", at: 10)
        let r6 = p.surfaceCurrent(acceptsInput: false, now: 10.1)
        #expect(r6 == [])
        let r7 = p.surfaceCurrent(acceptsInput: true, now: 10.2)
        #expect(r7 == [])
    }

    @Test func settledTakeBackStillDeliversToTheSwappingOwner() {
        // The daemon saw the owner attach before the swap finished.
        var p = PaneInput<String>()
        _ = p.beginTakeBack(at: 10, ownerCanCome: true)
        p.hold("A", at: 10)
        p.takeBackSettled()
        #expect(!p.isTakingBack)
        let r8 = p.surfaceCurrent(acceptsInput: true, now: 10.4)
        #expect(r8 == ["A"])
    }

    @Test func anUnsettledTakeBackRunsOutAndDropsItsKeys() {
        var p = PaneInput<String>()
        _ = p.beginTakeBack(at: 100, ownerCanCome: true)
        p.hold("A", at: 100)
        let r9 = p.expireTakeBack(now: 100 + SizeOwner.takeBackTimeout - 0.1)
        #expect(!r9)
        #expect(p.isTakingBack)
        let r10 = p.expireTakeBack(now: 100 + SizeOwner.takeBackTimeout)
        #expect(r10)
        #expect(!p.isTakingBack && p.heldCount == 0)
        // A second expiry (an older timer) changes nothing.
        let r11 = p.expireTakeBack(now: 200)
        #expect(!r11)
    }

    @Test func staleAndOverflowingEventsAreDropped() {
        var p = PaneInput<String>(capacity: 2, maxAge: 1.5)
        _ = p.beginTakeBack(at: 10, ownerCanCome: true)
        p.hold("old", at: 10)
        p.hold("new", at: 11.5)
        p.hold("over", at: 11.6) // beyond capacity
        let r12 = p.surfaceCurrent(acceptsInput: true, now: 12)
        #expect(r12 == ["new"])
    }
}
