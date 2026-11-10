import Testing

@testable import HesperFleet

/// Which run of a terminal view may write its shared attach state.
@Suite struct TerminalRunsTests {
    @Test func aSupersededRunThatAttachesLateWritesNothing() {
        var runs = TerminalRuns()
        let old = runs.begin()
        let new = runs.begin()
        // The old run's attach returns after the new run started: it must
        // close its own stream and leave the state alone.
        let oldWrites = runs.attached(old, typing: true)
        #expect(!oldWrites && !runs.typing)
        let newWrites = runs.attached(new, typing: false)
        #expect(newWrites && !runs.typing)
        #expect(runs.owns(new) && !runs.owns(old))
    }

    @Test func aNewRunStartsDetachedAndAnOldRunsEndKeepsItSo() {
        var runs = TerminalRuns()
        let old = runs.begin()
        let attached = runs.attached(old, typing: true)
        #expect(attached && runs.typing)
        // A new run takes over: nothing is attached for it yet, so no
        // keyboard and no input to the old stream.
        let new = runs.begin()
        #expect(!runs.typing)
        // The old run ends later: it touches nothing.
        let oldDetaches = runs.detached(old)
        #expect(!oldDetaches)
        // The new run's attach fails with a retryable error.
        let newDetaches = runs.detached(new)
        #expect(newDetaches && !runs.typing)
    }

    @Test func aFrameInFlightOfAnAttachedRunStopsOnceANewerRunBegins() {
        var runs = TerminalRuns()
        let old = runs.begin()
        let attached = runs.attached(old, typing: false)
        // The old run awaits a frame's push; meanwhile a new run begins.
        #expect(attached && runs.owns(old))
        let new = runs.begin()
        // When the push resumes, the old run may not draw (no live phase,
        // no grid); the new one may.
        #expect(!runs.owns(old) && runs.owns(new))
    }

    @Test func theOwnerDetachesAfterItsStreamEnds() {
        var runs = TerminalRuns()
        let run = runs.begin()
        let attached = runs.attached(run, typing: true)
        let detached = runs.detached(run)
        #expect(attached && detached && !runs.typing && runs.owns(run))
    }
}
