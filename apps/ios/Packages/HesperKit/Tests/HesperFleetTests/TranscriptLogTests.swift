import Foundation
import HesperCore
import HesperFixtures
import HesperFleet
import Testing

/// A transcript as the phone holds it: items by seq, each once, whether
/// they came from a page or the stream.
@Suite struct TranscriptLogTests {
    private func items(_ seqs: [Int64]) -> [TranscriptItem] {
        seqs.map { TranscriptItem(seq: $0, kind: .assistant, text: "line \($0)") }
    }

    @Test func testInsertKeepsOrderAndDedupes() {
        var log = TranscriptLog()
        #expect(log.firstSeq == nil && log.lastSeq == nil && !log.hasOlder)
        #expect(log.insert(items([3, 1])) == 2)
        #expect(log.insert(items([2, 3])) == 1)
        #expect(log.items.map(\.seq) == [1, 2, 3])
        #expect(log.firstSeq == 1 && log.lastSeq == 3)
        // A repeat within one batch counts once.
        #expect(log.insert(items([5, 5, 4])) == 2)
        #expect(log.items.map(\.seq) == [1, 2, 3, 4, 5])
        #expect(log.insert([]) == 0)
    }

    @Test func testLogReconnectAfter() {
        var log = TranscriptLog()
        log.insert(items([1, 2, 3, 4, 5]))
        // The stream reopens after a seq the log already passed: only 6 is new.
        #expect(log.insert(items([4, 5, 6])) == 1)
        #expect(log.items.map(\.seq) == [1, 2, 3, 4, 5, 6])
    }

    @Test func queuedItemsFromAnOldSessionCannotReturnAfterRestart() {
        var log = TranscriptLog(items([1, 2]))
        let oldGeneration = log.generation
        log.restart()
        #expect(log.insert(items([3, 4]), from: oldGeneration) == 0)
        #expect(log.items.isEmpty)
        #expect(log.insert(items([1]), from: log.generation) == 1)
        #expect(log.items.map(\.seq) == [1])
    }

    @Test func anOlderPageGoesInFront() {
        var log = TranscriptLog()
        log.insert(items([10, 11]))
        #expect(log.insert(items([7, 8, 9])) == 3)
        #expect(log.items.map(\.seq) == [7, 8, 9, 10, 11])
    }

    @Test func theSampleTranscriptTellsItsStory() {
        let t = Samples.transcript
        #expect(t.count == 30)
        #expect(t.map(\.seq) == t.map(\.seq).sorted() && Set(t.map(\.seq)).count == 30)
        #expect(t.first?.kind == .user)
        #expect(t.contains { $0.kind == .tool && $0.tool == "Read" })
        #expect(t.contains { $0.kind == .result && $0.ok == false })
        #expect(t.contains { $0.kind == .assistant && $0.text?.contains("**") == true && $0.text?.contains("```") == true })
        #expect(t.contains { $0.kind == .system && $0.text == "Context compacted" })
        // Every result names a tool call before it.
        for r in t where r.kind == .result {
            #expect(t.contains { $0.kind == .tool && $0.toolID == r.of && $0.seq < r.seq })
        }
        let last = t.last!.seq
        #expect(Samples.transcriptItem(1).seq > last && Samples.transcriptItem(2).seq > Samples.transcriptItem(1).seq)
    }

    @Test func testFixtureStream() async throws {
        let link = FixtureLink(terminalTick: .milliseconds(10))
        let stream = try await link.transcriptStream(agent: "studio/s1", after: nil, limit: 50)
        var it = stream.items.makeAsyncIterator()
        for item in Samples.transcript {
            #expect(await it.next() == item)
        }
        // Then one item per tick, for as long as it is open.
        for n in 1...3 {
            #expect(await it.next() == Samples.transcriptItem(n))
        }
        stream.close()
        while await it.next() != nil {}
    }

    @Test func theFixtureStreamContinuesAfterASeq() async throws {
        let link = FixtureLink(terminalTick: .milliseconds(10))
        let after = Samples.transcript[27].seq
        let stream = try await link.transcriptStream(agent: "studio/s1", after: after, limit: 50)
        var it = stream.items.makeAsyncIterator()
        #expect(await it.next() == Samples.transcript[28])
        #expect(await it.next() == Samples.transcript[29])
        #expect(await it.next() == Samples.transcriptItem(1))
        stream.close()
        while await it.next() != nil {}
    }

    @Test func theFixturePagesGoBackwards() async throws {
        let link = FixtureLink()
        let newest = try await link.transcript(agent: "studio/s1", before: nil, limit: 12)
        #expect(newest.items == Array(Samples.transcript.suffix(12)) && newest.more)
        let older = try await link.transcript(agent: "studio/s1", before: newest.items.first?.seq, limit: 12)
        #expect(older.items == Array(Samples.transcript[6..<18]) && older.more)
        let oldest = try await link.transcript(agent: "studio/s1", before: older.items.first?.seq, limit: 12)
        #expect(oldest.items == Array(Samples.transcript.prefix(6)) && !oldest.more)

        var log = TranscriptLog()
        for page in [newest, older, oldest] {
            log.insert(page.items)
            log.hasOlder = page.more
        }
        #expect(log.items == Samples.transcript && !log.hasOlder)
    }

    @Test func anUnknownAgentHasNoTranscript() async {
        await #expect(throws: FixtureError.unknownAgent("nope/x")) {
            _ = try await FixtureLink().transcript(agent: "nope/x", before: nil, limit: 50)
        }
        await #expect(throws: FixtureError.unknownAgent("nope/x")) {
            _ = try await FixtureLink().transcriptStream(agent: "nope/x", after: nil, limit: 50)
        }
    }

    /// A long-running agent's stream cannot grow the log without bound:
    /// the oldest items go, and the Mac pages them back on request.
    @Test func testStreamKeepsTheNewestAndPagesTheRestBack() {
        let max = TranscriptLog.maxItems
        var log = TranscriptLog()
        let added = log.insert((0..<Int64(max + 50)).map { TranscriptItem(seq: $0, kind: .assistant) })
        #expect(added == max + 50)
        #expect(log.items.count == max && log.firstSeq == 50 && log.lastSeq == Int64(max + 49))
        #expect(log.hasOlder)
        // Back at the bottom, the next stream item trims to the bound.
        log.insert([TranscriptItem(seq: Int64(max + 50), kind: .assistant)])
        #expect(log.items.count == max && log.firstSeq == 51)
    }

    /// Large items are bounded by their text's bytes too, more while
    /// reading older history.
    @Test func testLogIsBoundedByBytes() {
        let big = String(repeating: "x", count: 1 << 20)
        var log = TranscriptLog()
        log.insert((0..<40).map { TranscriptItem(seq: $0, kind: .result, text: big) })
        #expect(log.textBytes <= TranscriptLog.maxTextBytes && log.items.count == 16)
        #expect(log.lastSeq == 39 && log.hasOlder)
        log.keepsOlder = true
        log.insert((0..<10).map { TranscriptItem(seq: $0, kind: .result, text: big) })
        log.insert((40..<60).map { TranscriptItem(seq: $0, kind: .result, text: big) })
        #expect(log.textBytes <= TranscriptLog.maxTextBytesReading && log.items.count == 32)
        log.restart()
        #expect(log.textBytes == 0)
    }

    /// The person scrolls up and pages older items in; live items keep
    /// arriving. The page stays (no re-request) until they return to the
    /// bottom, within a hard bound.
    @Test func testOlderPageStaysWhileReadingHistory() {
        let max = TranscriptLog.maxItems
        var log = TranscriptLog()
        log.insert((100..<Int64(max + 100)).map { TranscriptItem(seq: $0, kind: .assistant) })
        log.keepsOlder = true
        log.insert((0..<100).map { TranscriptItem(seq: $0, kind: .assistant) })
        log.hasOlder = false
        log.insert([TranscriptItem(seq: Int64(max + 100), kind: .assistant)])
        #expect(log.firstSeq == 0 && !log.hasOlder)
        // A hard bound still holds.
        let far = Int64(max + 101)
        log.insert((far..<far + Int64(TranscriptLog.maxItemsReading)).map { TranscriptItem(seq: $0, kind: .assistant) })
        #expect(log.items.count == TranscriptLog.maxItemsReading && log.hasOlder)
        log.keepsOlder = false
        log.insert([TranscriptItem(seq: far + Int64(TranscriptLog.maxItemsReading), kind: .assistant)])
        #expect(log.items.count == max)
    }
}
