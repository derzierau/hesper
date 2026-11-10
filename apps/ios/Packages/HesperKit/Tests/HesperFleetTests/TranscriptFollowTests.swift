import Foundation
import HesperCore
import HesperFleet
import Testing

/// When the transcript view gives up, waits, or starts over.
@Suite struct TranscriptFollowTests {
    struct Coded: CodedError, CustomStringConvertible {
        let code: String
        var message = ""
        var description: String { message.isEmpty ? code : message }
    }
    struct Plain: Error {}

    @Test func testOldDaemonsAreUnsupported() {
        let old = Coded(code: "forbidden", message: "Unknown method")
        #expect(TranscriptFollow.isUnsupported(Coded(code: "unsupported")))
        #expect(TranscriptFollow.isUnsupported(old))
        #expect(!TranscriptFollow.isUnsupported(Coded(code: "unavailable")))
        #expect(!TranscriptFollow.isUnsupported(Plain()))
        #expect(TranscriptFollow.wait(after: old, previous: nil) == nil)
    }

    /// The first overflow reopens at once; a run of them backs off.
    @Test func testConsecutiveOverflowsBackOff() {
        #expect(TranscriptFollow.overflowWait(consecutive: 1) == .zero)
        #expect(TranscriptFollow.overflowWait(consecutive: 2) == .seconds(1))
        #expect(TranscriptFollow.overflowWait(consecutive: 3) == .seconds(2))
        #expect(TranscriptFollow.overflowWait(consecutive: 30) == .seconds(10))
    }

    /// A rights refusal is not an old daemon: it stops with its message.
    @Test func testRightsRefusalIsNotUnsupported() {
        let refused = Coded(code: "forbidden", message: "This device may not observe agents")
        #expect(!TranscriptFollow.isUnsupported(refused))
        #expect(TranscriptFollow.wait(after: refused, previous: nil) == nil)
    }

    @Test func testOtherFailuresRetryWithBackoff() {
        var wait: Duration?
        var waits: [Duration] = []
        for _ in 0..<6 {
            wait = TranscriptFollow.wait(after: Coded(code: "link_lost"), previous: wait)
            waits.append(wait ?? .zero)
        }
        #expect(waits == [.seconds(1), .seconds(2), .seconds(4), .seconds(8), .seconds(10), .seconds(10)])
        #expect(TranscriptFollow.wait(after: Plain(), previous: nil) == .seconds(1))
        #expect(TranscriptFollow.wait(after: Coded(code: "timeout"), previous: .seconds(1)) == .seconds(2))
    }

    @Test func testMissingTranscriptRetriesEveryFiveSeconds() {
        #expect(TranscriptFollow.wait(after: Coded(code: "unavailable"), previous: nil) == .seconds(5))
        #expect(TranscriptFollow.wait(after: Coded(code: "unavailable"), previous: .seconds(10)) == .seconds(5))
    }

    @Test func testANewSessionStartsOver() {
        #expect(TranscriptFollow.startsOver(held: "a", now: "b"))
        #expect(!TranscriptFollow.startsOver(held: "a", now: "a"))
        #expect(!TranscriptFollow.startsOver(held: "a", now: nil))
        #expect(!TranscriptFollow.startsOver(held: nil, now: "a"))
    }

    @Test func testRestartKeepsOnlyTheMarkerAndPagesAgain() {
        var log = TranscriptLog([
            TranscriptItem(seq: 4, kind: .user, text: "a"), TranscriptItem(seq: 9, kind: .assistant, text: "b"),
        ])
        let marker = TranscriptItem(seq: -1, kind: .system, text: "Transcript restarted")
        log.restart(marker: marker)
        #expect(log.items == [marker])
        #expect(log.hasOlder)
        #expect(log.firstLineSeq == nil)
        log.insert([TranscriptItem(seq: 2, kind: .user, text: "c")])
        #expect(log.firstSeq == -1 && log.firstLineSeq == 2)
        log.restart()
        #expect(log.items.isEmpty && log.hasOlder)
    }
}
