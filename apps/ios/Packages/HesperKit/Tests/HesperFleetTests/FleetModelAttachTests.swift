import Foundation
import HesperCore
import HesperFleet
import Testing

/// Records the grid each attach asks for.
private final class AttachSpy: LinkClient, @unchecked Sendable {
    // Written by `attach` and read by the test after it returned; one
    // test, one task, so no two accesses overlap.
    var asked: [(agent: String, cols: Int, rows: Int, owner: Bool)] = []
    var host: HostSizing?
    /// The code an owner attach is refused with.
    var refuseOwner: String?

    struct Refused: CodedError { let code: String }

    func updates() -> AsyncStream<LinkUpdate> { AsyncStream { $0.finish() } }
    func perform(_ command: LinkCommand) async throws {}
    func attach(agent: String, cols: Int, rows: Int, owner: Bool) async throws -> any AttachStream {
        asked.append((agent, cols, rows, owner))
        if owner, let code = refuseOwner { throw Refused(code: code) }
        return Ended()
    }
    func sizing(on machine: String) -> HostSizing? { host }

    struct Ended: AttachStream {
        let frames = AsyncStream<AttachFrame> { $0.finish() }
        func resize(cols: Int, rows: Int) {}
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

    @Test func aCapableHostGetsAnOwnerAttachAtTheFittedGrid() async throws {
        let spy = AttachSpy()
        spy.host = HostSizing(publishesOwner: true, rights: ["observe", "type"], device: "phone-1")
        let model = FleetModel(link: spy)
        let agent = Agent(id: "studio/s1", size: GridSize(cols: 97, rows: 56))
        let plan = model.plan(for: agent, area: CGSize(width: 402, height: 600), cell: CGSize(width: 10.8, height: 24))
        #expect(plan == AttachPlan(mode: .fitted, grid: GridSize(cols: 37, rows: 25)))
        let ran = try await model.attach(agent, plan: plan).plan
        #expect(ran == plan)
        #expect(spy.asked.map(\.owner) == [true] && spy.asked.map(\.cols) == [37] && spy.asked.map(\.rows) == [25])
        #expect(model.device(on: "studio") == "phone-1")
    }

    @Test func aForbiddenOwnerAttachIsMadeAgainAsAMirror() async throws {
        let spy = AttachSpy()
        spy.host = HostSizing(publishesOwner: true, device: "phone-1")
        spy.refuseOwner = "forbidden"
        let model = FleetModel(link: spy)
        let agent = Agent(id: "studio/s1", size: GridSize(cols: 97, rows: 56))
        let ran = try await model.attach(agent, plan: AttachPlan(mode: .fitted, grid: GridSize(cols: 37, rows: 25))).plan
        #expect(ran == AttachPlan(mode: .mirror, grid: GridSize(cols: 97, rows: 56)))
        #expect(spy.asked.map(\.owner) == [true, false] && spy.asked.map(\.cols) == [37, 97])
        // Another refusal is the Mac's answer, not a fallback.
        spy.refuseOwner = "not_found"
        await #expect(throws: AttachSpy.Refused.self) {
            _ = try await model.attach(agent, plan: AttachPlan(mode: .fitted, grid: GridSize(cols: 37, rows: 25)))
        }
    }

    @Test func attachesAt80By24WhenTheSizeIsUnknown() async throws {
        let spy = AttachSpy()
        _ = try await FleetModel(link: spy).attach(Agent(id: "mini/m2"))
        #expect(spy.asked.map(\.cols) == [80])
        #expect(spy.asked.map(\.rows) == [24])
    }
}
