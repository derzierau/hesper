import Foundation
import HesperFixtures
import HesperFleet
import Testing

/// The sample Macs answer attaches with a scripted terminal.
@Suite struct FixtureAttachTests {
    @Test func theStreamYieldsSizeThenDataAndKeepsYielding() async throws {
        let link = FixtureLink(terminalTick: .milliseconds(10))
        let stream = try await link.attach(agent: "studio/s1", cols: 80, rows: 24)
        var it = stream.frames.makeAsyncIterator()
        #expect(await it.next() == .size(cols: 97, rows: 56))
        #expect(await it.next() == .data(Samples.terminalScript))
        // Then one new line per tick, for as long as it is watched.
        for _ in 0..<3 {
            guard case .data(let line) = await it.next() else {
                Issue.record("no line")
                return
            }
            #expect(String(decoding: line, as: UTF8.self).hasSuffix("\r\n"))
        }
        stream.close()
        while await it.next() != nil {}
    }

    @Test func theScriptIsColouredAnsi() {
        let text = String(decoding: Samples.terminalScript, as: UTF8.self)
        #expect(text.contains("$ claude"))
        #expect(text.contains("\u{1b}[32m⏺\u{1b}[0m Read("))
        #expect(text.contains("\u{1b}[2m  ⎿"))
        #expect(text.contains("\u{1b}[33m⏺\u{1b}[0m Bash("))
        #expect(text.contains("\u{1b}[1;36m"))
    }

    @Test func anUnknownAgentHasNoTerminal() async {
        await #expect(throws: FixtureError.unknownAgent("nope/x")) {
            _ = try await FixtureLink().attach(agent: "nope/x", cols: 80, rows: 24)
        }
    }
}
