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

    // MARK: The grid gate

    let mac = TerminalFeeder.Grid(cols: 143, rows: 39)
    let guess = TerminalFeeder.Grid(cols: 73, rows: 39)

    @Test func bytesWaitForTheMacsGridThenArriveInOrder() async {
        let recorder = Recorder()
        let feeder = TerminalFeeder(gateTimeout: .seconds(60), sink: { recorder.record($0) })
        await feeder.push(Data("before ".utf8))
        // Bytes written for the old grid go before the gate closes.
        await feeder.expect(grid: mac, current: guess)
        #expect(recorder.all == [Data("before ".utf8)])
        await feeder.push(Data("history ".utf8))
        await feeder.push(Data(repeating: 7, count: TerminalFeeder.chunk))
        try? await Task.sleep(for: .milliseconds(50))
        #expect(recorder.all.count == 1)
        // A report of another grid keeps it shut; the Mac's opens it.
        await feeder.gridChanged(to: guess)
        #expect(recorder.all.count == 1)
        await feeder.gridChanged(to: mac)
        await feeder.push(Data("live".utf8))
        await feeder.flush()
        var sent = Data("before history ".utf8)
        sent.append(Data(repeating: 7, count: TerminalFeeder.chunk))
        sent.append(Data("live".utf8))
        #expect(recorder.all.reduce(Data(), +) == sent)
    }

    @Test func aSecondSizeInsideTheGateWaitsForTheNewestGrid() async {
        let recorder = Recorder()
        let feeder = TerminalFeeder(gateTimeout: .seconds(60), sink: { recorder.record($0) })
        let other = TerminalFeeder.Grid(cols: 90, rows: 30)
        await feeder.expect(grid: mac, current: guess)
        await feeder.push(Data("a".utf8))
        await feeder.expect(grid: other, current: guess)
        // The first grid no longer opens it; the newest one does.
        await feeder.gridChanged(to: mac)
        #expect(recorder.all.isEmpty)
        await feeder.gridChanged(to: other)
        #expect(recorder.all == [Data("a".utf8)])
    }

    @Test func aSecondSizeBackToTheSurfacesGridOpensTheGate() async {
        let recorder = Recorder()
        let feeder = TerminalFeeder(gateTimeout: .seconds(60), sink: { recorder.record($0) })
        await feeder.expect(grid: mac, current: guess)
        await feeder.push(Data("a".utf8))
        // The Mac went back to the grid the surface has: nothing to wait
        // for, and the held bytes go at once.
        await feeder.expect(grid: guess, current: guess)
        #expect(recorder.all == [Data("a".utf8)])
        await feeder.flush()
    }

    @Test func aSurfaceAlreadyAtTheGridDoesNotWait() async {
        let recorder = Recorder()
        let feeder = TerminalFeeder(gateTimeout: .seconds(60), sink: { recorder.record($0) })
        await feeder.expect(grid: mac, current: mac)
        await feeder.push(Data("x".utf8))
        await feeder.flush()
        #expect(recorder.all == [Data("x".utf8)])
    }

    @Test func theTimeoutReleasesAGridThatNeverComes() async throws {
        let recorder = Recorder()
        let feeder = TerminalFeeder(gateTimeout: .milliseconds(50), sink: { recorder.record($0) })
        await feeder.expect(grid: mac, current: guess)
        await feeder.push(Data("a".utf8))
        await feeder.push(Data("b".utf8))
        #expect(recorder.all.isEmpty)
        for _ in 0..<100 where recorder.all.isEmpty {
            try await Task.sleep(for: .milliseconds(10))
        }
        #expect(recorder.all == [Data("ab".utf8)])
    }

    @Test func flushWaitsForTheGate() async {
        let recorder = Recorder()
        let feeder = TerminalFeeder(gateTimeout: .milliseconds(50), sink: { recorder.record($0) })
        await feeder.expect(grid: mac, current: guess)
        await feeder.push(Data("last screen".utf8))
        // The stream ended: flush returns once the gate opened, bytes delivered.
        await feeder.flush()
        #expect(recorder.all == [Data("last screen".utf8)])
    }
}
