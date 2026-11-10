import Foundation
import Testing
@testable import HesperCore

/// PaneInput: the take-back phase of one terminal pane, and its clicks.
@Suite struct PaneInputTests {
    typealias C = SizeOwner.Candidate
    /// A focus pane another device sized; `awaiting`: its take-back began.
    func pane(remote: Bool = true, awaiting: Bool = false) -> C {
        C(agentID: "a", window: 1, focusRole: true, readWrite: true, remoteOwned: remote, takingBack: awaiting)
    }
    func route(_ c: C, canCome: Bool = true) -> SizeOwner.KeyRoute {
        SizeOwner.keyRoute([c], targeted: [true], ownerCanCome: [canCome], typesIntoAgent: true)
    }

    @Test func settleBeforeSwapKeepsEveryKeyInOrder() {
        // A starts the take-back, B follows; the daemon then reports the
        // owner attach (no remote owner any more) before the owner surface
        // swapped in. C must queue behind A and B, not overtake them.
        var p = PaneInput<String>()
        #expect(route(pane()) == .takeBack(0))
        let began = p.beginTakeBack(at: 10, ownerCanCome: true)
        #expect(began)
        p.holdKey("A")
        #expect(route(pane(awaiting: p.isAwaitingOwner)) == .hold(0))
        p.holdKey("B")
        // Settled: remoteOwned false. The phase alone routes the key.
        #expect(route(pane(remote: false, awaiting: p.isAwaitingOwner)) == .hold(0))
        p.holdKey("C")
        let ready = p.surfaceCurrent(acceptsInput: true)
        #expect(ready)
        // Until the delivery has run, a key still queues behind.
        #expect(route(pane(remote: false, awaiting: p.isAwaitingOwner)) == .hold(0))
        p.holdKey("D")
        let keys = p.deliverHeldKeys()
        #expect(keys == ["A", "B", "C", "D"])
        #expect(p.phase == .normal)
        // Afterwards keys go to the owner surface directly.
        #expect(route(pane(remote: false, awaiting: p.isAwaitingOwner)) == .pass)
    }

    @Test func aViewSurfaceWhileAwaitingChangesNothing() {
        var p = PaneInput<String>()
        _ = p.beginTakeBack(at: 10, ownerCanCome: true)
        p.holdKey("A")
        let view = p.surfaceCurrent(acceptsInput: false) // a re-created view
        #expect(!view)
        #expect(p.isAwaitingOwner && p.heldKeyCount == 1)
        _ = p.surfaceCurrent(acceptsInput: true)
        let keys = p.deliverHeldKeys()
        #expect(keys == ["A"])
    }

    @Test func anOwnerReplacedByAViewBeforeDeliveryKeepsTheKeys() {
        var p = PaneInput<String>()
        _ = p.beginTakeBack(at: 10, ownerCanCome: true)
        p.holdKey("A")
        _ = p.surfaceCurrent(acceptsInput: true)
        _ = p.surfaceCurrent(acceptsInput: false)
        let none = p.deliverHeldKeys()
        #expect(none.isEmpty)
        #expect(p.isAwaitingOwner && p.heldKeyCount == 1)
    }

    @Test func aPasteIntoAnAwaitingPaneIsHeldButStartsNothing() {
        func paste(_ c: C) -> SizeOwner.KeyRoute {
            SizeOwner.keyRoute([c], targeted: [true], ownerCanCome: [true], typesIntoAgent: false, isPaste: true)
        }
        // Awaiting: held behind the typed keys, so it is not lost on the view.
        #expect(paste(pane(awaiting: true)) == .hold(0))
        #expect(paste(pane(remote: false, awaiting: true)) == .hold(0)) // after the settle
        // Not awaiting: a paste takes nothing back (as before the feature).
        #expect(paste(pane()) == .pass)
        // Other shortcuts pass even while awaiting.
        #expect(SizeOwner.keyRoute([pane(awaiting: true)], targeted: [true], ownerCanCome: [true], typesIntoAgent: false) == .pass)
    }

    @Test func noTakeBackBeginsWhenNoOwnerSurfaceCanCome() {
        var p = PaneInput<String>()
        #expect(route(pane(), canCome: false) == .pass)
        let began = p.beginTakeBack(at: 10, ownerCanCome: false)
        #expect(!began)
        #expect(p.phase == .normal)
        let held = p.holdKey("A")
        #expect(!held)
    }

    @Test func anOwnerThatCanNoLongerComeEndsTheTakeBackAndPassesTheKey() {
        // The window went away mid-take-back: the next key ends it at once
        // and goes on as before the feature; the held keys are dropped.
        var p = PaneInput<String>()
        _ = p.beginTakeBack(at: 10, ownerCanCome: true)
        p.holdKey("A")
        #expect(route(pane(awaiting: true), canCome: false) == .cancel(0))
        p.endTakeBack()
        #expect(p.phase == .normal && p.heldKeyCount == 0)
        let none = p.deliverHeldKeys()
        #expect(none.isEmpty)
    }

    @Test func theTimeoutEndsTheTakeBackAndDropsItsKeys() {
        var p = PaneInput<String>()
        _ = p.beginTakeBack(at: 100, ownerCanCome: true)
        p.holdKey("A")
        let early = p.expireTakeBack(now: 100 + SizeOwner.takeBackTimeout - 0.1)
        #expect(!early && p.isAwaitingOwner)
        let due = p.expireTakeBack(now: 100 + SizeOwner.takeBackTimeout)
        #expect(due && p.phase == .normal && p.heldKeyCount == 0)
        let again = p.expireTakeBack(now: 200) // an older timer
        #expect(!again)
    }

    @Test func heldKeysHaveNoAgeLimitWithinTheTimeout() {
        // A slow attach (over the clicks' 1.5 s) still delivers every key.
        var p = PaneInput<String>()
        _ = p.beginTakeBack(at: 0, ownerCanCome: true)
        p.holdKey("A")
        let notYet = p.expireTakeBack(now: 4)
        #expect(!notYet)
        _ = p.surfaceCurrent(acceptsInput: true)
        let keys = p.deliverHeldKeys()
        #expect(keys == ["A"])
    }

    @Test func pastCapacityTheNewestKeysAreDropped() {
        var p = PaneInput<String>(keyCapacity: 2)
        _ = p.beginTakeBack(at: 0, ownerCanCome: true)
        let a = p.holdKey("A"), b = p.holdKey("B"), c = p.holdKey("C")
        #expect(a && b && !c)
        _ = p.surfaceCurrent(acceptsInput: true)
        let keys = p.deliverHeldKeys()
        #expect(keys == ["A", "B"])
    }

    @Test func clicksKeepTheirAgeRuleAndAreUntouchedByTakeBack() {
        var p = PaneInput<String>()
        p.holdClick("old", at: 10)
        p.holdClick("new", at: 11.5)
        let ready = p.surfaceCurrent(acceptsInput: true) // normal: no delivery
        #expect(!ready)
        let clicks = p.drainClicks(now: 12)
        #expect(clicks == ["new"])
    }
}
