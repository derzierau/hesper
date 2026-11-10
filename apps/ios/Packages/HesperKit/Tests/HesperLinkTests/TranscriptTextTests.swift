import Foundation
import HesperCore
import HesperFleet
import Testing

@testable import HesperLink

@Suite struct TranscriptTextTests {
    @Test func readsAChunkWithSequenceAndByteOffset() async throws {
        let host = FakeHost(id: "MACHINE_ID", short: "M", offer: FlowTests.secret)
        let (link, updates) = try await AttachTests.linked(host)
        defer { withExtendedLifetime(updates) {} }
        let chunk = try await link.transcriptText(agent: "M/a1", seq: 42, offset: 0, limit: 65_536)
        #expect(chunk.text == "é" && chunk.next == 2 && chunk.more)
        let params = try #require(host.params("agents.transcriptText"))
        #expect(params["id"] as? String == "M/a1")
        #expect(params["seq"] as? Int == 42)
        #expect(params["offset"] as? Int == 0)
        #expect(params["limit"] as? Int == 65_536)
    }

    /// A reconnect hesperd cannot continue: the marker, then the newest
    /// items; the phone drops what it held and keeps only the new file's.
    @Test func reconnectPastTheEndRestartsTheLog() async throws {
        let host = FakeHost(id: "MACHINE_ID", short: "M", offer: FlowTests.secret)
        let (link, updates) = try await AttachTests.linked(host)
        defer { withExtendedLifetime(updates) {} }
        var log = TranscriptLog([
            TranscriptItem(seq: 500, kind: .user, text: "old"), TranscriptItem(seq: 900, kind: .assistant, text: "old"),
        ])
        let stream = try await link.transcriptStream(agent: "M/a1", after: 900, limit: 200)
        #expect(try #require(host.params("agents.transcriptStream"))["after"] as? Int == 900)
        var got = 0
        for await item in stream.items {
            log.follow(item)
            got += 1
            if got == 3 { break }
        }
        stream.close()
        #expect(log.items.map(\.seq) == [-1, 10, 42])
        #expect(log.items.allSatisfy { $0.text != "old" })
    }

    @Test func readsAShellScreenAsHesperdSendsIt() async throws {
        let host = FakeHost(id: "MACHINE_ID", short: "M", offer: FlowTests.secret)
        let (link, updates) = try await AttachTests.linked(host)
        defer { withExtendedLifetime(updates) {} }
        let screen = try await link.screen(agent: "M/s1", rows: 0, scrollback: 2000)
        #expect(screen.text == "$ ls\nREADME.md\n$" && screen.rows == 24 && screen.cols == 80 && !screen.alt)
        let params = try #require(host.params("agents.screen"))
        #expect(params["id"] as? String == "M/s1" && params["scrollback"] as? Int == 2000)
    }
}
