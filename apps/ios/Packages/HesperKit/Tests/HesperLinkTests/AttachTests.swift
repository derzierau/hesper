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

    @Test func aLargeFrameSplitInPiecesDecodes() throws {
        // A 4 MiB frame in 1 MiB pieces, after a size frame.
        let large = Data((0..<(4 << 20)).map { UInt8($0 % 251) })
        let split = Self.inner(2, Data([0, 90, 0, 30])) + Self.inner(0, large)
        var decoder = AttachDecoder()
        _ = try decoder.append(Data("{\"ok\":true}\n".utf8))
        var frames: [AttachFrame] = []
        for start in stride(from: 0, to: split.count, by: 1 << 20) {
            frames += try decoder.append(Data(split[start..<min(start + (1 << 20), split.count)])).frames
        }
        #expect(frames == [.size(cols: 90, rows: 30), .data(large)])
    }

    @Test func tinyFramesOfOnePayloadArriveAsOne() async {
        let stream = AttachChannel(id: 7, onClose: { _ in })
        var payload = Data(#"{"ok":true,"cols":80,"rows":24}"#.utf8) + Data([10])
        let bytes = Data((0..<20_000).map { UInt8($0 & 0xff) })
        for b in bytes { payload += Self.inner(0, Data([b])) }
        stream.receive(payload + Self.inner(4, Data()) + Self.inner(0, Data("x".utf8)))
        stream.close()
        #expect(await Self.all(stream) == [.size(cols: 80, rows: 24), .data(bytes), .scroll, .data(Data("x".utf8))])
    }

    @Test(.timeLimit(.minutes(1))) func aSecondWaitForTheReplyFailsAndTheFirstStillGetsIt() async throws {
        let stream = AttachChannel(id: 7, onClose: { _ in })
        let first = Task { try await stream.reply() }
        // Let the first wait register.
        for _ in 0..<100 where !stream.awaitingReply { try await Task.sleep(for: .milliseconds(5)) }
        await #expect(throws: RelayError(code: "busy", message: "The reply is already awaited")) { _ = try await stream.reply() }
        stream.receive(Data(#"{"ok":true,"cols":80,"rows":24}"#.utf8) + Data([10]))
        #expect(try await first.value == AttachReply(ok: true, cols: 80, rows: 24))
        stream.close()
    }

    @Test func aCancelledReaderClosesTheAttach() async throws {
        let closed = Mutex([UInt32]())
        let stream = AttachChannel(id: 9) { id in closed.withLock { $0.append(id) } }
        let reader = Task { await Self.all(stream) }
        try await Task.sleep(for: .milliseconds(20))
        reader.cancel()
        _ = await reader.value
        // The next frame finds no reader: the attach closes (X).
        stream.receive(Data(#"{"ok":true,"cols":80,"rows":24}"#.utf8) + Data([10]) + Self.inner(0, Data("x".utf8)))
        #expect(closed.withLock { $0 } == [9])
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

    @Test func typedBytesResizesAndCloseReachTheMacInOrder() async throws {
        let host = FakeHost(id: "MACHINE_ID", short: "M", offer: FlowTests.secret)
        let (link, updates) = try await Self.linked(host)
        defer { withExtendedLifetime(updates) {} }
        let stream = try await link.attach(agent: "M/a1", cols: 37, rows: 25, typing: true, owner: true)
        let ch = try #require((stream as? AttachChannel)?.id)
        var want: [LinkFrames.Frame] = []
        for i in 0..<200 {
            stream.write(Data([UInt8(i)]))
            want.append(LinkFrames.Frame(kind: Self.d, channel: ch, payload: AttachDecoder.input(Data([UInt8(i)]))))
            if i % 10 == 9 {
                stream.resize(cols: 40 + i, rows: 20)
                want.append(LinkFrames.Frame(kind: Self.d, channel: ch, payload: AttachDecoder.resize(cols: 40 + i, rows: 20)))
            }
        }
        stream.close()
        want.append(LinkFrames.Frame(kind: Self.x, channel: ch, payload: Data()))
        for _ in 0..<300 where host.received.count < want.count { try await Task.sleep(for: .milliseconds(10)) }
        #expect(host.received == want)
    }

    @Test func aCancelledAttachClosesItsChannelOnTheMac() async throws {
        let host = FakeHost(id: "MACHINE_ID", short: "M", offer: FlowTests.secret)
        host.state.withLock { $0.silentAttach = true }
        let (link, updates) = try await Self.linked(host)
        defer { withExtendedLifetime(updates) {} }
        let opening = Task { try await link.attach(agent: "M/a1", cols: 80, rows: 24, typing: false, owner: false) }
        for _ in 0..<300 where host.params("agents.attach") == nil { try await Task.sleep(for: .milliseconds(10)) }
        let ch = try #require((host.params("agents.attach")?["ch"] as? NSNumber)?.uint32Value)
        opening.cancel()
        // The caller is let go at once, not after the 15 s reply timeout.
        let start = ContinuousClock.now
        await #expect(throws: (any Error).self) { _ = try await opening.value }
        #expect(ContinuousClock.now - start < .seconds(5))
        // The Mac accepted the attach: the phone closes it there.
        for _ in 0..<300 where host.received.isEmpty { try await Task.sleep(for: .milliseconds(10)) }
        #expect(host.received == [LinkFrames.Frame(kind: Self.x, channel: ch, payload: Data())])
    }

    @Test func aRefusedAttachClosesItsChannelOnTheMac() async throws {
        let host = FakeHost(id: "MACHINE_ID", short: "M", offer: FlowTests.secret)
        host.state.withLock { $0.attachReply = #"{"ok":false,"error":{"code":"forbidden","message":"no type right"}}"# }
        let (link, updates) = try await Self.linked(host)
        defer { withExtendedLifetime(updates) {} }
        await #expect(throws: RelayError.self) { _ = try await link.attach(agent: "M/a1", cols: 80, rows: 24) }
        for _ in 0..<300 where host.received.isEmpty { try await Task.sleep(for: .milliseconds(10)) }
        #expect(host.received == [LinkFrames.Frame(kind: Self.x, channel: 1, payload: Data())])
    }

    @Test func anExitedAttachLeavesTheChannelsWithoutAnX() async throws {
        let host = FakeHost(id: "MACHINE_ID", short: "M", offer: FlowTests.secret)
        let (link, updates) = try await Self.linked(host)
        defer { withExtendedLifetime(updates) {} }
        let stream = try await link.attach(agent: "M/a1", cols: 80, rows: 24, typing: true, owner: false)
        let channel = try #require(link.channel(on: "M"))
        // EXIT, and no X yet: hesperd sends its own X once the pipe closes
        // (relay/internal/host/terminal.go), so the phone must not.
        host.push(kind: Self.d, channel: 1, Self.inner(3, Data(#"{"code":0}"#.utf8)))
        #expect(await Self.all(stream) == [.size(cols: 97, rows: 56), .data(Data("hi".utf8)), .exit])
        for _ in 0..<300 where await !channel.openChannels.isEmpty { try await Task.sleep(for: .milliseconds(10)) }
        #expect(await channel.openChannels.isEmpty)
        stream.close()
        stream.write(Data("late".utf8))
        // The Mac's X then finds nothing to end.
        host.push(kind: Self.x, channel: 1, Data())
        try await Task.sleep(for: .milliseconds(100))
        #expect(host.received.isEmpty)
    }

    @Test func aRefusalWithoutAReasonIsNotALinkFailure() async throws {
        let host = FakeHost(id: "MACHINE_ID", short: "M", offer: FlowTests.secret)
        host.state.withLock { $0.attachReply = #"{"ok":false}"# }
        let (link, updates) = try await Self.linked(host)
        defer { withExtendedLifetime(updates) {} }
        await #expect(throws: RelayError(code: "refused", message: "The Mac refused the terminal")) {
            _ = try await link.attach(agent: "M/a1", cols: 80, rows: 24)
        }
    }

    @Test func everyWayAnAttachEndsStopsItsDrainTask() async throws {
        let host = FakeHost(id: "MACHINE_ID", short: "M", offer: FlowTests.secret)
        let relay = FakeRelay([host])
        let (link, updates) = try await Self.linked(host, relay: relay)
        defer { withExtendedLifetime(updates) {} }
        let channel = try #require(link.channel(on: "M"))
        // The Mac's X.
        let a = try await link.attach(agent: "M/a1", cols: 80, rows: 24)
        host.push(kind: Self.x, channel: try #require((a as? AttachChannel)?.id), Data())
        _ = await Self.all(a)
        for _ in 0..<300 where await channel.drains > 0 { try await Task.sleep(for: .milliseconds(10)) }
        #expect(await channel.drains == 0)
        // A close after it ended adds nothing.
        a.close()
        // The link breaking.
        let b = try await link.attach(agent: "M/a1", cols: 80, rows: 24)
        relay.dropAll()
        _ = await Self.all(b)
        for _ in 0..<300 where await channel.drains > 0 { try await Task.sleep(for: .milliseconds(10)) }
        #expect(await channel.drains == 0)
    }

    @Test func theOutboundQueueIsBoundedButAlwaysTakesTheClose() async {
        let queue = OutboundQueue()
        let chunk = Data(count: 64 << 10)
        var queued = 0
        while queue.frame(chunk) { queued += 1 }
        #expect(queued == OutboundQueue.maxBytes / chunk.count)
        queue.finish(with: .close)
        var ops = 0
        var last: HostChannel.Outbound?
        for await op in queue.ops {
            ops += 1
            last = op
        }
        #expect(ops == queued + 1)
        if case .close? = last {} else { Issue.record("X was not queued last") }
    }

    @Test func theOutboundQueueBoundsTinyFramesByCount() {
        let queue = OutboundQueue()
        var queued = 0
        while queued < 10_000, queue.frame(AttachDecoder.input(Data([1]))) { queued += 1 }
        #expect(queued == OutboundQueue.maxFrames)
        queue.finish()
    }

    @Test func inputTheMacDoesNotTakeClosesTheAttach() {
        let closed = Mutex([UInt32]())
        let stream = AttachChannel(id: 4, onClose: { id in closed.withLock { $0.append(id) } }, onWrite: { _, _ in false })
        stream.write(Data("x".utf8))
        #expect(closed.withLock { $0 } == [4])
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

    @Test func aStalledConsumerClosesOnOverflowAndCanReopen() async throws {
        let closed = Mutex([UInt32]())
        let stream = AttachChannel(id: 7) { id in closed.withLock { $0.append(id) } }
        stream.receive(Data(#"{"ok":true,"cols":80,"rows":24}"#.utf8) + Data([10]))
        var stalled = stream.frames.makeAsyncIterator()
        #expect(await stalled.next() == .size(cols: 80, rows: 24))

        // A burst of many small frames is not an overflow: the bound is
        // bytes, and one payload's data frames arrive as one.
        for i in 0..<1000 {
            stream.receive(Self.inner(0, Data([UInt8(i & 0xff)])) + Self.inner(0, Data([1])))
        }
        #expect(closed.withLock { $0.isEmpty })
        // Past the byte bound the attach closes, holding no more than it.
        let chunk = Data(repeating: 0x61, count: AttachChannel.maxQueuedDataFrame)
        for _ in 0...(AttachChannel.maxQueuedBytes / chunk.count) {
            stream.receive(Self.inner(0, chunk))
        }
        try #require(closed.withLock { $0 == [7] })
        let queued = await Self.all(stream)
        let small: [AttachFrame] = (0..<1000).map { .data(Data([UInt8($0 & 0xff), 1])) }
        #expect(Array(queued.prefix(1000)) == small)
        let bytes = queued.reduce(0) { $0 + $1.queuedBytes }
        #expect(bytes <= AttachChannel.maxQueuedBytes)

        let reopened = AttachChannel(id: 8) { id in closed.withLock { $0.append(id) } }
        reopened.receive(Data(#"{"ok":true,"cols":90,"rows":30}"#.utf8) + Data([10]) + Self.inner(0, Data("fresh".utf8)))
        reopened.close()
        #expect(await Self.all(reopened) == [.size(cols: 90, rows: 30), .data(Data("fresh".utf8))])
        #expect(closed.withLock { $0 } == [7, 8])
    }

    @Test func largeDataFrameUsesBoundedChunks() async {
        let stream = AttachChannel(id: 7, onClose: { _ in })
        let data = Data(repeating: 0x61, count: 1 << 20)
        stream.receive(Data(#"{"ok":true,"cols":80,"rows":24}"#.utf8) + Data([10]) + Self.inner(0, data))
        stream.close()
        let chunks = await Self.all(stream).compactMap { frame -> Data? in
            if case .data(let bytes) = frame { return bytes }
            return nil
        }
        #expect(chunks.count == 4)
        #expect(chunks.allSatisfy { $0.count <= 256 << 10 })
        #expect(chunks.reduce(into: Data()) { $0 += $1 } == data)
    }

    @Test func overflowClosesOnlyItsChannelAndAReattachGetsFreshFrames() async throws {
        let host = FakeHost(id: "MACHINE_ID", short: "M", offer: FlowTests.secret)
        let (link, updates) = try await Self.linked(host)
        defer { withExtendedLifetime(updates) {} }
        let stream = try await link.attach(agent: "M/a1", cols: 80, rows: 24)
        let first = try #require((stream as? AttachChannel)?.id)
        // Link payloads hold at most 1 MiB: 17 full ones pass the bound.
        let full = Data(repeating: 0x61, count: LinkFrames.maxPayload - 5)
        for _ in 0..<17 {
            host.push(kind: Self.d, channel: first, Self.inner(0, full))
        }
        let close = LinkFrames.Frame(kind: Self.x, channel: first, payload: Data())
        for _ in 0..<200 {
            if host.received.contains(close) { break }
            try await Task.sleep(for: .milliseconds(10))
        }
        try #require(host.received.contains(close))
        let queued = await Self.all(stream)
        #expect(queued.prefix(2) == [.size(cols: 97, rows: 56), .data(Data("hi".utf8))])

        let again = try await link.attach(agent: "M/a1", cols: 80, rows: 24)
        #expect((again as? AttachChannel)?.id == first + 1)
        again.close()
        #expect(await Self.all(again) == [.size(cols: 97, rows: 56), .data(Data("hi".utf8))])
    }

    @Test func anUnknownMacIsUnavailable() async throws {
        let host = FakeHost(id: "MACHINE_ID", short: "M", offer: FlowTests.secret)
        let (link, updates) = try await Self.linked(host)
        defer { withExtendedLifetime(updates) {} }
        await #expect(throws: RelayError.self) { _ = try await link.attach(agent: "other/a1", cols: 80, rows: 24) }
    }
}
