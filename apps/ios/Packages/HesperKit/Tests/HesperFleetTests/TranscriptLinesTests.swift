import HesperCore
import HesperFixtures
import HesperFleet
import Testing

/// The transcript view's rows: a result folds under its tool call.
@Suite struct TranscriptLinesTests {
    @Test func resultJoinsItsCall() {
        let log = TranscriptLog([
            TranscriptItem(seq: 1, kind: .user, text: "fix it"),
            TranscriptItem(seq: 2, kind: .tool, tool: "Read", summary: "a.swift", toolID: "t1"),
            TranscriptItem(seq: 3, kind: .result, text: "Read 9 lines", of: "t1", ok: true),
            TranscriptItem(seq: 4, kind: .assistant, text: "done"),
        ])
        let lines = log.lines
        #expect(lines.map(\.id) == [1, 2, 4])
        #expect(lines[1].result?.seq == 3)
        #expect(lines[0].result == nil)
    }

    @Test func callWithoutResultYet() {
        let log = TranscriptLog([TranscriptItem(seq: 1, kind: .tool, tool: "Bash", summary: "ls", toolID: "t1")])
        #expect(log.lines.count == 1)
        #expect(log.lines[0].result == nil)
    }

    /// The call is on an older page: the result is not hidden.
    @Test func resultWithoutItsCallStaysARow() {
        let log = TranscriptLog([
            TranscriptItem(seq: 5, kind: .result, text: "ok", of: "t0", ok: true),
            TranscriptItem(seq: 6, kind: .assistant, text: "next"),
        ])
        #expect(log.lines.map(\.id) == [5, 6])
        #expect(log.lines[0].result == nil)
    }

    @Test func sampleTranscriptFoldsEveryResult() {
        let log = TranscriptLog(Samples.transcript)
        #expect(!log.lines.contains { $0.item.kind == .result })
        #expect(log.lines.filter { $0.item.kind == .tool }.allSatisfy { $0.result != nil })
    }
}
