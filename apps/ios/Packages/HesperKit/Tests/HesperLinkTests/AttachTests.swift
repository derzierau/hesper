import Foundation
import HesperCore
import HesperFleet
import Synchronization
import Testing

@testable import HesperLink

/// Read-only attaches on the live link: the request, the reply line, the
/// inner frames, the channel fan-out and a break.
@Suite struct AttachTests {
    static let d = UInt8(ascii: "D"), x = UInt8(ascii: "X"), e = UInt8(ascii: "E")

    /// An inner attach frame: type, length (uint32 BE), payload
    /// (relay/pkg/wire/frame.go).
    static func inner(_ type: UInt8, _ payload: Data) -> Data {
        var out = Data([type])
        for shift in [24, 16, 8, 0] { out.append(UInt8(truncatingIfNeeded: UInt32(payload.count) >> UInt32(shift))) }
        return out + payload
    }

    /// A linked phone: the live link to one approved Mac, once its first
    /// event arrived. Keep the updates alive: the link closes with its
    /// last listener.
    static func linked(_ host: FakeHost, relay: FakeRelay? = nil) async throws -> (LiveLink, AsyncStream<LinkUpdate>.Iterator) {
        let context = try await FlowTests.context(relay ?? FakeRelay([host]))
        _ = try await LivePairing(context: context, deviceName: "iPhone", pushEnvironment: "sandbox").pair(offer: FlowTests.offer(host))
        let link = LiveLink(context: context)
        var updates = link.updates().makeAsyncIterator()
        while let update = await updates.next() {
            if case .event = update { break }
        }
        return (link, updates)
    }

    /// Collects a stream's frames until it ends.
    static func all(_ stream: any AttachStream) async -> [AttachFrame] {
        var out: [AttachFrame] = []
        for await f in stream.frames { out.append(f) }
        return out
    }

    @Test func innerFramesDecode() throws {
        var decoder = AttachDecoder()
        let reply = Data(#"{"ok":true,"cols":97,"rows":56}"#.utf8) + Data("\n".utf8)
        let bytes =
            reply + Self.inner(0, Data("hi".utf8)) + Self.inner(1, Data([0, 80, 0, 24])) + Self.inner(2, Data([0, 120, 0, 40]))
            + Self.inner(4, Data(#"{"offset":0,"max":9,"new":0}"#.utf8)) + Self.inner(3, Data(#"{"code":0}"#.utf8))
            + Self.inner(9, Data([1]))
        // Split anywhere: the reply line and the frames reassemble.
        var replies: [AttachReply] = []
        var frames: [AttachFrame] = []
        for i in stride(from: 0, to: bytes.count, by: 3) {
            let out = try decoder.append(Data(bytes[i..<min(i + 3, bytes.count)]))
            if let r = out.reply { replies.append(r) }
            frames += out.frames
        }
        #expect(replies == [AttachReply(ok: true, cols: 97, rows: 56)])
        // RESIZE (1) only goes to the Mac, an unknown type is skipped.
        #expect(frames == [.data(Data("hi".utf8)), .size(cols: 120, rows: 40), .scroll, .exit])
    }

    @Test func aReplyErrorIsAnObjectOrAString() throws {
        var object = AttachDecoder()
        let r = try object.append(Data(#"{"ok":false,"error":{"code":"forbidden","message":"no observe right"}}"#.utf8) + Data([10]))
        #expect(r.reply == AttachReply(ok: false, error: RelayError(code: "forbidden", message: "no observe right")))
        var string = AttachDecoder()
        #expect(try string.append(Data(#"{"ok":false,"error":"gone"}"#.utf8) + Data([10])).reply?.error?.code == "gone")
        var bad = AttachDecoder()
        #expect(throws: RelayError.self) { try bad.append(Data("not json\n".utf8)) }
    }

    @Test func attachSendsTheReadOnlyRequest() async throws {
        let host = FakeHost(id: "MACHINE_ID", short: "M", offer: FlowTests.secret)
        let (link, updates) = try await Self.linked(host)
        defer { withExtendedLifetime(updates) {} }
        let stream = try await link.attach(agent: "M/a1", cols: 97, rows: 56)
        defer { stream.close() }
        let p = try #require(host.params("agents.attach"))
        let streamID = try #require(host.state.withLock { $0.links.keys.first })
        #expect(p["link"] as? String == streamID)
        #expect(p["ch"] as? Int == 1)
        #expect(p["mode"] as? String == "ro")
        let request = try #require(p["request"] as? [String: Any])
        #expect(request["attach"] as? String == "M/a1")
        #expect(request["mode"] as? String == "ro")
        #expect(request["cols"] as? Int == 97 && request["rows"] as? Int == 56)
        #expect(request["owner"] as? Bool == false && request["history"] as? Bool == true)
        #expect(Set(request.keys) == ["attach", "mode", "cols", "rows", "owner", "history"])
        #expect(Set(p.keys) == ["link", "ch", "mode", "request"])
    }

    @Test func anOwnerAttachIsReadWriteAtItsGrid() throws {
        let params = try HostChannel.attachParams(link: "L1", ch: 3, agent: "M/a1", cols: 37, rows: 25, typing: true, owner: true)
        let got = try #require(try JSONSerialization.jsonObject(with: params) as? NSDictionary)
        let want = try JSONSerialization.jsonObject(
            with: Data(
                #"{"ch":3,"link":"L1","mode":"rw","request":{"attach":"M/a1","cols":37,"history":true,"mode":"rw","owner":true,"rows":25}}"#
                    .utf8))
        #expect(got == want as? NSDictionary)
    }

    @Test func aTypingMirrorIsReadWriteWithoutOwner() throws {
        let params = try HostChannel.attachParams(link: "L1", ch: 3, agent: "M/a1", cols: 97, rows: 56, typing: true, owner: false)
        let got = try #require(try JSONSerialization.jsonObject(with: params) as? NSDictionary)
        let want = try JSONSerialization.jsonObject(
            with: Data(
                #"{"ch":3,"link":"L1","mode":"rw","request":{"attach":"M/a1","cols":97,"history":true,"mode":"rw","owner":false,"rows":56}}"#
                    .utf8))
        #expect(got == want as? NSDictionary)
    }

    @Test func inputFrameBytes() {
        // Type 0, length uint32 BE, the bytes.
        #expect(AttachDecoder.input(Data("ab".utf8)) == Data([0, 0, 0, 0, 2, 0x61, 0x62]))
        #expect(AttachDecoder.input(Data(count: 300)).prefix(5) == Data([0, 0, 0, 0x01, 0x2c]))
    }

    @Test func writeSendsAnInputFrameOnItsChannel() async throws {
        let host = FakeHost(id: "MACHINE_ID", short: "M", offer: FlowTests.secret)
        let (link, updates) = try await Self.linked(host)
        defer { withExtendedLifetime(updates) {} }
        let stream = try await link.attach(agent: "M/a1", cols: 97, rows: 56, typing: true, owner: false)
        let ch = try #require((stream as? AttachChannel)?.id)
        #expect(host.params("agents.attach")?["mode"] as? String == "rw")
        let up = Data([0x1b, 0x5b, 0x41])
        stream.write(up)
        for _ in 0..<200 where host.received.isEmpty { try await Task.sleep(for: .milliseconds(10)) }
        stream.close()
        for _ in 0..<200 where host.received.count < 2 { try await Task.sleep(for: .milliseconds(10)) }
        #expect(
            host.received == [
                LinkFrames.Frame(kind: Self.d, channel: ch, payload: AttachDecoder.input(up)),
                LinkFrames.Frame(kind: Self.x, channel: ch, payload: Data()),
            ])
        // A closed attach sends no more.
        stream.write(up)
        try await Task.sleep(for: .milliseconds(50))
        #expect(host.received.count == 2)
    }

    @Test func resizeFrameBytes() {
        // Type 1, length 4, cols and rows uint16 BE; clamped to the range.
        #expect(AttachDecoder.resize(cols: 37, rows: 25) == Data([1, 0, 0, 0, 4, 0, 37, 0, 25]))
        #expect(AttachDecoder.resize(cols: 300, rows: 70_000) == Data([1, 0, 0, 0, 4, 0x01, 0x2c, 0xff, 0xff]))
        #expect(AttachDecoder.resize(cols: -1, rows: 5) == Data([1, 0, 0, 0, 4, 0, 0, 0, 5]))
    }

    @Test func anOwnerAttachSendsRwAndResizesOnItsChannel() async throws {
        let host = FakeHost(id: "MACHINE_ID", short: "M", offer: FlowTests.secret)
        let (link, updates) = try await Self.linked(host)
        defer { withExtendedLifetime(updates) {} }
        // The fake relay's host publishes no `sizeOwner` capability.
        #expect(link.sizing(on: "M")?.publishesOwner == false)
        #expect(link.sizing(on: "M")?.device == "phone-1")
        let stream = try await link.attach(agent: "M/a1", cols: 37, rows: 25, typing: true, owner: true)
        let ch = try #require((stream as? AttachChannel)?.id)
        let p = try #require(host.params("agents.attach"))
        #expect(p["mode"] as? String == "rw")
        let request = try #require(p["request"] as? [String: Any])
        #expect(request["mode"] as? String == "rw" && request["owner"] as? Bool == true && request["history"] as? Bool == true)
        #expect(request["cols"] as? Int == 37 && request["rows"] as? Int == 25)
        stream.resize(cols: 80, rows: 12)
        for _ in 0..<200 where host.received.isEmpty { try await Task.sleep(for: .milliseconds(10)) }
        stream.close()
        for _ in 0..<200 where host.received.count < 2 { try await Task.sleep(for: .milliseconds(10)) }
        // RESIZE in a D frame, then X: never a data frame.
        #expect(
            host.received == [
                LinkFrames.Frame(kind: Self.d, channel: ch, payload: Data([1, 0, 0, 0, 4, 0, 80, 0, 12])),
                LinkFrames.Frame(kind: Self.x, channel: ch, payload: Data()),
            ])
        // A closed attach sends no more.
        stream.resize(cols: 90, rows: 12)
        try await Task.sleep(for: .milliseconds(50))
        #expect(host.received.count == 2)
    }

    @Test func demuxRoutesByChannel() async throws {
        let host = FakeHost(id: "MACHINE_ID", short: "M", offer: FlowTests.secret)
        var (link, updates) = try await Self.linked(host)
        let stream = try await link.attach(agent: "M/a1", cols: 80, rows: 24)
        let ch = try #require((stream as? AttachChannel)?.id)
        // Another channel's bytes are dropped; channel 0's events still arrive.
        host.push(kind: Self.d, channel: ch + 1, Self.inner(0, Data("not mine".utf8)))
        host.push(kind: Self.d, channel: ch, Self.inner(0, Data("more".utf8)))
        host.push(kind: Self.e, channel: 0, Data(#"{"removed":"M/a9"}"#.utf8))
        host.push(kind: Self.x, channel: ch, Data())
        #expect(await Self.all(stream) == [.size(cols: 97, rows: 56), .data(Data("hi".utf8)), .data(Data("more".utf8))])
        while let update = await updates.next() {
            if case .event(.removed(let id), "M") = update {
                #expect(id == "M/a9")
                break
            }
        }
    }

    @Test func replyErrorEndsTheAttach() async throws {
        let host = FakeHost(id: "MACHINE_ID", short: "M", offer: FlowTests.secret)
        host.state.withLock { $0.attachReply = #"{"ok":false,"error":{"code":"forbidden","message":"no observe right"}}"# }
        let (link, updates) = try await Self.linked(host)
        defer { withExtendedLifetime(updates) {} }
        await #expect(throws: RelayError(code: "forbidden", message: "no observe right")) {
            _ = try await link.attach(agent: "M/a1", cols: 80, rows: 24)
        }
    }

    @Test func closeSendsX() async throws {
        let host = FakeHost(id: "MACHINE_ID", short: "M", offer: FlowTests.secret)
        let (link, updates) = try await Self.linked(host)
        defer { withExtendedLifetime(updates) {} }
        let stream = try await link.attach(agent: "M/a1", cols: 80, rows: 24)
        let ch = try #require((stream as? AttachChannel)?.id)
        stream.close()
        #expect(await Self.all(stream) == [.size(cols: 97, rows: 56), .data(Data("hi".utf8))])
        for _ in 0..<200 where host.received.isEmpty { try await Task.sleep(for: .milliseconds(10)) }
        // X and nothing else: the phone never types into a watched terminal.
        #expect(host.received == [LinkFrames.Frame(kind: Self.x, channel: ch, payload: Data())])
        // A closed channel's number is not reused on the same link.
        let next = try await link.attach(agent: "M/a1", cols: 80, rows: 24)
        #expect((next as? AttachChannel)?.id == ch + 1)
        next.close()
    }

    @Test func aBreakEndsTheAttachAndAFreshOneFollowsTheRelink() async throws {
        let host = FakeHost(id: "MACHINE_ID", short: "M", offer: FlowTests.secret)
        let relay = FakeRelay([host])
        let (link, updates) = try await Self.linked(host, relay: relay)
        defer { withExtendedLifetime(updates) {} }
        let stream = try await link.attach(agent: "M/a1", cols: 80, rows: 24)
        let first = try #require(host.params("agents.attach")?["link"] as? String)
        relay.dropAll()
        // Ended without an exit frame: the link broke, not the agent.
        #expect(await Self.all(stream) == [.size(cols: 97, rows: 56), .data(Data("hi".utf8))])
        // The next attach waits for the relink and runs on the new link.
        let again = try await link.attach(agent: "M/a1", cols: 80, rows: 24)
        defer { again.close() }
        let second = try #require(host.params("agents.attach")?["link"] as? String)
        #expect(second != first)
        var it = again.frames.makeAsyncIterator()
        #expect(await it.next() == .size(cols: 97, rows: 56))
    }

    @Test func anUnknownMacIsUnavailable() async throws {
        let host = FakeHost(id: "MACHINE_ID", short: "M", offer: FlowTests.secret)
        let (link, updates) = try await Self.linked(host)
        defer { withExtendedLifetime(updates) {} }
        await #expect(throws: RelayError.self) { _ = try await link.attach(agent: "other/a1", cols: 80, rows: 24) }
    }
}
