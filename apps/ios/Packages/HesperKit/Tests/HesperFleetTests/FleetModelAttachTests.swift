import Foundation
import HesperCore
import HesperFleet
import Testing

/// Records the grid each attach asks for.
private final class AttachSpy: LinkClient, @unchecked Sendable {
    // Written by `attach` and read by the test after it returned; one
    // test, one task, so no two accesses overlap.
    var asked: [(agent: String, cols: Int, rows: Int)] = []

    func updates() -> AsyncStream<LinkUpdate> { AsyncStream { $0.finish() } }
    func perform(_ command: LinkCommand) async throws {}
    func attach(agent: String, cols: Int, rows: Int) async throws -> any AttachStream {
        asked.append((agent, cols, rows))
        return Ended()
    }

    struct Ended: AttachStream {
        let frames = AsyncStream<AttachFrame> { $0.finish() }
        func close() {}
    }
}

@MainActor
@Suite struct FleetModelAttachTests {
    @Test func attachesAtTheMacsSize() async throws {
        let spy = AttachSpy()
        let model = FleetModel(link: spy)
        _ = try await model.attach(Agent(id: "studio/s1", size: GridSize(cols: 97, rows: 56)))
        #expect(spy.asked.map(\.agent) == ["studio/s1"])
        #expect(spy.asked.map(\.cols) == [97])
        #expect(spy.asked.map(\.rows) == [56])
    }

    @Test func attachesAt80By24WhenTheSizeIsUnknown() async throws {
        let spy = AttachSpy()
        _ = try await FleetModel(link: spy).attach(Agent(id: "mini/m2"))
        #expect(spy.asked.map(\.cols) == [80])
        #expect(spy.asked.map(\.rows) == [24])
    }
}
