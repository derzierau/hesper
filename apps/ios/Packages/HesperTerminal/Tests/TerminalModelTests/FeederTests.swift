import Foundation
import Synchronization
import Testing

@testable import TerminalModel

/// Bursts reach the surface in few, ordered chunks.
@Suite struct FeederTests {
    /// What the sink saw, from whatever thread it ran on.
    final class Recorder: Sendable {
        let chunks = Mutex<[Data]>([])
        func record(_ data: Data) { chunks.withLock { $0.append(data) } }
        var all: [Data] { chunks.withLock { $0 } }
    }

    @Test func aTwoMegabyteBurstArrivesInFewOrderedChunks() async {
        let recorder = Recorder()
        let feeder = TerminalFeeder(sink: { recorder.record($0) })
        var sent = Data()
        for i in 0..<2048 {
            let piece = Data(repeating: UInt8(i % 251), count: 1024)
            sent.append(piece)
            await feeder.push(piece)
        }
        await feeder.flush()
        let chunks = recorder.all
        #expect(chunks.count <= 40)
        #expect(chunks.reduce(Data(), +) == sent)
    }

    @Test func aSmallWriteArrivesAfterTheWindow() async throws {
        let recorder = Recorder()
        let feeder = TerminalFeeder(sink: { recorder.record($0) })
        await feeder.push(Data("$ ".utf8))
        await feeder.push(Data("ls\r\n".utf8))
        #expect(recorder.all.isEmpty)
        // No flush: the 16 ms window hands it over on its own.
        for _ in 0..<100 where recorder.all.isEmpty {
            try await Task.sleep(for: .milliseconds(10))
        }
        #expect(recorder.all == [Data("$ ls\r\n".utf8)])
    }

    @Test func aFullChunkGoesAtOnce() async {
        let recorder = Recorder()
        let feeder = TerminalFeeder(sink: { recorder.record($0) })
        await feeder.push(Data(repeating: 1, count: TerminalFeeder.chunk))
        await feeder.flush()
        #expect(recorder.all.map(\.count) == [TerminalFeeder.chunk])
    }
}
