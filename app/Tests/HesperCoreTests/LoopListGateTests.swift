import Foundation
import Testing
@testable import HesperCore

struct LoopListGateTests {
    /// Runs `steps` on one gate and returns what each answered (a mutating
    /// call cannot sit inside #expect).
    struct Run {
        var g = LoopListGate()
        mutating func connect() -> Bool { g.connect() }
        mutating func retry() -> Bool { g.retry() }
        mutating func finished(_ ok: Bool) -> LoopListGate.Step { g.finished(ok: ok) }
    }

    @Test func aConnectDuringAListListsAgain() {
        var r = Run()
        let first = r.connect(), second = r.connect()
        #expect(first && !second) // one list at a time, the second remembered
        let a = r.finished(true)
        #expect(a == .list && r.g.listing && r.g.loaded)
        let b = r.finished(true)
        #expect(b == .idle && !r.g.listing)
    }

    @Test func aFailedListRetriesWithBackoffThenGivesUp() {
        var r = Run()
        _ = r.connect()
        var waits: [TimeInterval] = []
        while case .retry(let after) = r.finished(false) {
            waits.append(after)
            let started = r.retry()
            #expect(started)
        }
        #expect(waits == [1, 2, 4, 8, 16])
        #expect(!r.g.listing && !r.g.loaded) // nothing arrived at all: still loading
        // The next connect starts over.
        let again = r.connect()
        let step = r.finished(false)
        #expect(again && step == .retry(after: 1))
    }

    @Test func aRetryWaitsForAListOut() {
        var r = Run()
        _ = r.connect()
        let failed = r.finished(false)
        let connect = r.connect() // a connect during the wait lists at once
        let retry = r.retry() // the wait's end finds a list out
        let done = r.finished(true)
        #expect(failed == .retry(after: 1) && connect && !retry && done == .idle)
    }

    @Test func theSnapshotLoadsWhenTheListFails() {
        var r = Run()
        _ = r.connect()
        r.g.sawEvents()
        #expect(!r.g.loaded) // the list may still come
        let step = r.finished(false)
        #expect(step == .retry(after: 1) && r.g.loaded)

        // Events after a failure load too.
        var s = Run()
        _ = s.connect()
        _ = s.finished(false)
        #expect(!s.g.loaded)
        s.g.sawEvents()
        #expect(s.g.loaded)
    }
}
