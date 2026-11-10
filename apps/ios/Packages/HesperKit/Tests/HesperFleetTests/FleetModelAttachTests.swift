import Foundation
import HesperCore
import HesperFleet
import Testing

/// Records the grid each attach asks for.
private final class AttachSpy: LinkClient, @unchecked Sendable {
    // Written by `attach` and read by the test after it returned; one
    // test, one task, so no two accesses overlap.
    var asked: [(agent: String, cols: Int, rows: Int, typing: Bool, owner: Bool, view: Bool)] = []
    var host: HostSizing?
    /// The code an owner attach is refused with.
    var refuseOwner: String?
    /// The code a read-write mirror attach is refused with.
    var refuseTyping: String?

    struct Refused: CodedError { let code: String }

    func updates() -> AsyncStream<LinkUpdate> { AsyncStream { $0.finish() } }
    func perform(_ command: LinkCommand) async throws {}
    func attach(agent: String, cols: Int, rows: Int, typing: Bool, owner: Bool, view: Bool) async throws -> any AttachStream {
        asked.append((agent, cols, rows, typing, owner, view))
        if owner, let code = refuseOwner { throw Refused(code: code) }
        if typing, !owner, let code = refuseTyping { throw Refused(code: code) }
        return Ended()
    }
    func transcript(agent: String, before: Int64?, limit: Int) async throws -> TranscriptPage {
        TranscriptPage(items: [], more: false)
    }
    func transcriptText(agent: String, seq: Int64, offset: Int, limit: Int) async throws -> TranscriptTextChunk {
        throw Refused(code: "unsupported")
    }
    func transcriptStream(agent: String, after: Int64?, limit: Int) async throws -> any TranscriptStream {
        throw Refused(code: "unsupported")
    }
    func screen(agent: String, rows: Int, scrollback: Int) async throws -> ScreenText {
        throw Refused(code: "unsupported")
    }
    func sizing(on machine: String) -> HostSizing? { host }

    struct Ended: AttachStream {
        let frames = AsyncStream<AttachFrame> { $0.finish() }
        func resize(cols: Int, rows: Int) {}
        func write(_ data: Data) {}
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
        #expect(ran == AttachPlan(mode: .mirror, grid: GridSize(cols: 97, rows: 56), typing: true))
        #expect(spy.asked.map(\.owner) == [true, false] && spy.asked.map(\.cols) == [37, 97])
        // Another refusal is the Mac's answer, not a fallback.
        spy.refuseOwner = "not_found"
        await #expect(throws: AttachSpy.Refused.self) {
            _ = try await model.attach(agent, plan: AttachPlan(mode: .fitted, grid: GridSize(cols: 37, rows: 25)))
        }
    }

    @Test func aMirrorAttachesReadWriteWhenThePhoneMayType() async throws {
        let spy = AttachSpy()
        spy.host = HostSizing(publishesOwner: false, rights: ["type"], device: "phone-1")
        let model = FleetModel(link: spy)
        let agent = Agent(id: "studio/s1", size: GridSize(cols: 97, rows: 56))
        let plan = model.plan(for: agent, area: CGSize(width: 402, height: 600), cell: CGSize(width: 10.8, height: 24))
        #expect(plan == AttachPlan(mode: .mirror, grid: GridSize(cols: 97, rows: 56), typing: true))
        _ = try await model.attach(agent, plan: plan)
        #expect(spy.asked.map(\.typing) == [true] && spy.asked.map(\.owner) == [false])
    }

    @Test func aForbiddenRwMirrorIsMadeAgainReadOnly() async throws {
        let spy = AttachSpy()
        spy.refuseTyping = "forbidden"
        let model = FleetModel(link: spy)
        let agent = Agent(id: "studio/s1", size: GridSize(cols: 97, rows: 56))
        let ran = try await model.attach(agent, plan: AttachPlan(mode: .mirror, grid: GridSize(cols: 97, rows: 56), typing: true)).plan
        #expect(ran.typing == false && spy.asked.map(\.typing) == [true, false])
        // Any other refusal is the Mac's answer, not a fallback.
        spy.refuseTyping = "not_found"
        await #expect(throws: AttachSpy.Refused.self) {
            _ = try await model.attach(agent, plan: AttachPlan(mode: .mirror, grid: GridSize(cols: 97, rows: 56), typing: true))
        }
        #expect(spy.asked.count == 3)
    }

    @Test func aForbiddenOwnerAttachKeepsTypingInTheMirror() async throws {
        let spy = AttachSpy()
        spy.refuseOwner = "forbidden"
        let model = FleetModel(link: spy)
        let agent = Agent(id: "studio/s1", size: GridSize(cols: 97, rows: 56))
        let ran = try await model.attach(agent, plan: AttachPlan(mode: .fitted, grid: GridSize(cols: 37, rows: 25), typing: true)).plan
        #expect(ran == AttachPlan(mode: .mirror, grid: GridSize(cols: 97, rows: 56), typing: true))
        #expect(spy.asked.map(\.typing) == [true, true] && spy.asked.map(\.owner) == [true, false])
    }

    @Test func attachesAt80By24WhenTheSizeIsUnknown() async throws {
        let spy = AttachSpy()
        _ = try await FleetModel(link: spy).attach(Agent(id: "mini/m2"))
        #expect(spy.asked.map(\.cols) == [80])
        #expect(spy.asked.map(\.rows) == [24])
    }

    @Test func aTypingTextViewNeverOwnsTheTerminalSize() async throws {
        let spy = AttachSpy()
        let agent = Agent(id: "studio/s1", size: GridSize(cols: 97, rows: 56))
        _ = try await FleetModel(link: spy).attachView(agent, size: agent.size!, typing: true)
        #expect(spy.asked.map(\.view) == [true])
        #expect(spy.asked.map(\.owner) == [false])
        #expect(spy.asked.map(\.typing) == [true])
    }

    @Test func aReadOnlyTextViewNeverAsksToType() async throws {
        let spy = AttachSpy()
        let agent = Agent(id: "studio/s1", size: GridSize(cols: 97, rows: 56))
        _ = try await FleetModel(link: spy).attachView(agent, size: agent.size!, typing: false)
        #expect(spy.asked.map(\.view) == [true])
        #expect(spy.asked.map(\.owner) == [false])
        #expect(spy.asked.map(\.typing) == [false])
    }
}
