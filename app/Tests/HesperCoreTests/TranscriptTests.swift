import Foundation
import Testing
@testable import HesperCore

/// Transcript items as hesperd sends them (`agents.transcript`).
@Suite struct TranscriptTests {
    private func decode(_ line: String) throws -> TranscriptItem {
        try JSONDecoder().decode(TranscriptItem.self, from: Data(line.utf8))
    }

    @Test func testDecodesSpecItems() throws {
        let user = try decode(#"{"seq": 1834211, "at": 1762600000123, "kind": "user", "text": "Fix the login"}"#)
        #expect(user.id == 1_834_211 && user.kind == .user && user.text == "Fix the login")
        let at = try #require(user.at)
        #expect(abs(at.timeIntervalSince1970 - Date(timeIntervalSince1970: 1_762_600_000.123).timeIntervalSince1970) < 0.000_5)
        #expect(!user.truncated && user.ok == nil)

        let assistant = try decode(#"{"seq": 1835077, "kind": "assistant", "text": "On it."}"#)
        #expect(assistant.kind == .assistant && assistant.at == nil && assistant.text == "On it.")

        let tool = try decode(
            #"{"seq": 1836002, "kind": "tool", "tool": "Read", "summary": "TerminalView.swift", "id": "toolu_1"}"#)
        #expect(tool.kind == .tool && tool.tool == "Read" && tool.summary == "TerminalView.swift")
        #expect(tool.toolID == "toolu_1" && tool.id == 1_836_002)

        let result = try decode(
            #"{"seq": 1838410, "kind": "result", "of": "toolu_1", "text": "214 lines", "truncated": true, "ok": true}"#)
        #expect(result.kind == .result && result.of == "toolu_1" && result.text == "214 lines")
        #expect(result.truncated && result.ok == true)

        let system = try decode(#"{"seq": 1900120, "kind": "system", "text": "Context compacted"}"#)
        #expect(system.kind == .system && system.text == "Context compacted" && system.ok == nil)
    }

    @Test func testUnknownKindIsSystem() throws {
        let item = try decode(#"{"seq": 7, "kind": "plan", "text": "Step one"}"#)
        #expect(item.kind == .system && item.text == "Step one")
    }

    @Test func aPageDecodes() throws {
        let page = try JSONDecoder().decode(
            TranscriptPage.self, from: Data(#"{"items": [{"seq": 1, "kind": "user", "text": "hi"}], "more": true}"#.utf8))
        #expect(page.items.map(\.seq) == [1] && page.more)
    }

    @Test func aFullTextChunkDecodesItsByteCursor() throws {
        let chunk = try JSONDecoder().decode(
            TranscriptTextChunk.self, from: Data(#"{"text":"é","next":2,"more":true}"#.utf8))
        #expect(chunk.text == "é" && chunk.next == 2 && chunk.more)
    }

    @Test func anItemRoundTrips() throws {
        let line = #"{"seq": 1834211, "at": 1762600000123, "kind": "tool", "tool": "Bash", "id": "toolu_2", "ok": false}"#
        let item = try decode(line)
        let again = try JSONDecoder().decode(TranscriptItem.self, from: try JSONEncoder().encode(item))
        #expect(again == item)
    }

    /// hesperd's wire.ScreenResult omits `alt` when it is false (a shell
    /// at its prompt) and adds `cursor` while the cursor shows.
    @Test func testDecodesScreenWithoutAlt() throws {
        let line = #"{"text":"$ ls","rows":24,"cols":80,"cursor":{"col":4,"row":0}}"#
        let screen = try JSONDecoder().decode(ScreenText.self, from: Data(line.utf8))
        #expect(screen.text == "$ ls" && screen.rows == 24 && screen.cols == 80 && !screen.alt)
        let alt = try JSONDecoder().decode(ScreenText.self, from: Data(#"{"text":"","rows":24,"cols":80,"alt":true}"#.utf8))
        #expect(alt.alt)
    }
}
