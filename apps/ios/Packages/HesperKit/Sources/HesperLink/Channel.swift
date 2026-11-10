import CryptoKit
import Foundation
import HesperFleet
import Synchronization

/// Who the phone is on the relay: its device id and keys.
public struct ControllerIdentity: Sendable {
    public var device: String
    public var keys: DeviceKeys

    public init(device: String, keys: DeviceKeys) {
        self.device = device
        self.keys = keys
    }
}

/// The end-to-end channel to one host (relay/docs/protocol.md,
/// "End-to-end channel"): a Noise IK handshake against the pinned host
/// key, then every request signed, sealed and sent as one `e2e` request.
public actor HostChannel {
    public let machine: String
    let relay: RelayConnection
    let me: ControllerIdentity
    let hostKey: Data
    private var session: E2ESession?
    private var created = Date.distantPast

    /// The link stream while `link` reads it: its ticket id (the `link`
    /// of `agents.attach`), socket and cipher.
    private struct OpenLink {
        let id: String
        let socket: any RelaySocket
        let cipher: StreamCipher
    }
    private var openLink: OpenLink?
    /// Open attaches by channel. Channel numbers only grow, as the Mac's
    /// own controller does (internal/remote/link.go): a closed channel's
    /// number never meets a late frame of the one before.
    private var attaches: [UInt32: AttachChannel] = [:]
    /// Each attach's queue to the Mac, until it is finished.
    private var queues: [UInt32: OutboundQueue] = [:]
    /// Drain tasks still running (for tests: each ends with its attach).
    private(set) var drains = 0
    private var lastChannel: UInt32 = 0

    public init(relay: RelayConnection, machine: String, hostKey: Data, me: ControllerIdentity) {
        self.relay = relay
        self.machine = machine
        self.hostKey = hostKey
        self.me = me
    }

    /// Sends a request through the channel and returns its result's JSON.
    /// `strong` signs with the strong key (biometry). An unknown session
    /// (the host restarted) handshakes again and resends once: nothing ran.
    public func call(_ method: String, params: Data = Data("{}".utf8), strong: Bool = false) async throws -> Data {
        let params = try Params.normalize(params)
        let auth = try RequestSigning.sign(
            params, machine: machine, method: method, device: me.device, key: strong ? me.keys.strong : me.keys.device)
        let id = Data((0..<16).map { _ in UInt8.random(in: 0...255) }).hex
        var inner = Data(#"{"id":"#.utf8) + Envelope.json(id) + Data(#","method":"#.utf8) + Envelope.json(method)
        inner += Data(#","params":"#.utf8) + params + Data(#","auth":"#.utf8) + (try JSONEncoder().encode(auth)) + Data("}".utf8)
        do {
            return try await send(inner, id: id)
        } catch let e as RelayError where e.code == "e2e_session" {
            session = nil
            return try await send(inner, id: id)
        }
    }

    private func send(_ inner: Data, id: String) async throws -> Data {
        let s = try await open()
        let (n, c) = s.seal(inner)
        let params = try JSONSerialization.data(withJSONObject: ["s": s.id, "n": n, "c": c.base64EncodedString()])
        let answer = try JSONSerialization.jsonObject(with: try await relay.request(machine: machine, method: "e2e", params: params))
        guard let a = answer as? [String: Any], let rn = (a["n"] as? NSNumber)?.uint64Value,
            let rc = (a["c"] as? String).flatMap({ Data(base64Encoded: $0) })
        else { throw RelayError(code: "integrity", message: "Invalid channel answer") }
        let plain = try s.open(counter: rn, ciphertext: rc)
        guard let o = try JSONSerialization.jsonObject(with: plain) as? [String: Any], o["id"] as? String == id else {
            throw RelayError(code: "integrity", message: "The answer is not for this request")
        }
        if let e = o["error"] as? [String: Any] {
            throw RelayError(code: e["code"] as? String ?? "", message: e["message"] as? String ?? "")
        }
        return try JSONSerialization.data(withJSONObject: o["result"] ?? [String: Any](), options: .fragmentsAllowed)
    }

    /// The session, after a handshake when there is none or it is an hour
    /// old. A host that cannot answer with the pinned key fails with
    /// `key_changed`.
    func open() async throws -> E2ESession {
        if let session, Date().timeIntervalSince(created) < 3600 { return session }
        var noise = try NoiseInitiator(
            static: me.keys.e2e, remoteStatic: hostKey, prologue: E2E.prologue(machine: machine, controller: me.device))
        let payload = try JSONSerialization.data(withJSONObject: [
            "v": E2E.version, "ts": Int64(Date().timeIntervalSince1970 * 1000), "binding": try me.keys.binding(),
        ])
        let first = try noise.writeMessage(payload: payload)
        let params = try JSONSerialization.data(withJSONObject: ["v": E2E.version, "h": first.base64EncodedString()])
        let result = try JSONSerialization.jsonObject(with: try await relay.request(machine: machine, method: "e2e.hello", params: params))
        guard let h = ((result as? [String: Any])?["h"] as? String).flatMap({ Data(base64Encoded: $0) }) else {
            throw RelayError(code: "e2e_session", message: "Invalid handshake answer")
        }
        let reply: (payload: Data, send: NoiseCipher, receive: NoiseCipher)
        do {
            reply = try noise.readMessage(h)
        } catch {
            throw RelayError(code: "key_changed", message: "The Mac did not answer with its pinned key")
        }
        guard let p = try JSONSerialization.jsonObject(with: reply.payload) as? [String: Any], p["v"] as? Int == E2E.version,
            let id = p["session"] as? String, id.count == 32, Data(hex: id) != nil
        else { throw RelayError(code: "e2e_session", message: "Invalid handshake answer") }
        let s = E2ESession(id: id, send: reply.send, receive: reply.receive, peer: hostKey)
        session = s
        created = Date()
        return s
    }
}

/// A link's frames (relay/docs/protocol.md, "Links"): `kind | channel
/// (uint32 BE) | length (uint32 BE) | payload`, reassembled across
/// messages.
public struct LinkFrames: Sendable {
    public struct Frame: Equatable, Sendable {
        public var kind: UInt8
        public var channel: UInt32
        public var payload: Data
    }

    static let maxPayload = 1 << 20
    private var buffer = Data()

    public init() {}

    /// Appends bytes and returns the frames they complete.
    public mutating func append(_ bytes: Data) throws -> [Frame] {
        buffer += bytes
        var frames: [Frame] = []
        while buffer.count >= 9 {
            let b = [UInt8](buffer.prefix(9))
            let channel = b[1...4].reduce(UInt32(0)) { $0 << 8 | UInt32($1) }
            let length = Int(b[5...8].reduce(UInt32(0)) { $0 << 8 | UInt32($1) })
            guard length <= Self.maxPayload else { throw RelayError(code: "integrity", message: "Link frame too large") }
            guard buffer.count >= 9 + length else { break }
            let start = buffer.startIndex
            frames.append(Frame(kind: b[0], channel: channel, payload: Data(buffer[start + 9..<start + 9 + length])))
            buffer = Data(buffer[(start + 9 + length)...])
        }
        return frames
    }

    /// Encodes one frame (for tests and a fake host).
    public static func encode(kind: UInt8, channel: UInt32, payload: Data) -> Data {
        var out = Data([kind])
        for shift in [24, 16, 8, 0] { out.append(UInt8(truncatingIfNeeded: channel >> UInt32(shift))) }
        let n = UInt32(payload.count)
        for shift in [24, 16, 8, 0] { out.append(UInt8(truncatingIfNeeded: n >> UInt32(shift))) }
        return out + payload
    }
}

extension HostChannel {
    static let kindEvent = UInt8(ascii: "E"), kindData = UInt8(ascii: "D"), kindClose = UInt8(ascii: "X")

    /// Opens the link (`agents.link`) and reads it until the stream ends;
    /// throws when it breaks. Channel 0's events go to `yield`, `D` frames
    /// to the attach on their channel, `X` ends it; other frames are
    /// dropped. Open attaches end when the link does.
    func link(_ yield: @Sendable (LinkEvent) -> Void) async throws {
        let ticket = try JSONSerialization.jsonObject(with: try await call("agents.link")) as? [String: Any]
        guard let id = ticket?["id"] as? String, let secret = (ticket?["e2e"] as? String).flatMap({ Data(base64Encoded: $0) }) else {
            throw RelayError(code: "unsupported", message: "The Mac's link is not sealed")
        }
        let cipher = try StreamCipher(secret: secret, streamID: id)
        let socket = try await relay.openStream(id)
        openLink = OpenLink(id: id, socket: socket, cipher: cipher)
        defer {
            socket.close()
            linkEnded(id)
        }
        var frames = LinkFrames()
        while !Task.isCancelled {
            guard case .data(let sealed) = try await socket.receive() else {
                throw RelayError(code: "integrity", message: "Text on a sealed stream")
            }
            let (kind, payload) = try cipher.open(sealed)
            guard kind == StreamCipher.data else { continue }
            for f in try frames.append(payload) {
                switch (f.kind, f.channel) {
                case (Self.kindEvent, 0):
                    if let event = try? LinkEvent.decode(f.payload) { yield(event) }
                case (Self.kindData, let ch) where ch > 0:
                    attaches[ch]?.receive(f.payload)
                case (Self.kindClose, let ch) where ch > 0:
                    attaches.removeValue(forKey: ch)?.end(RelayError(code: "closed", message: "The Mac closed the terminal"))
                    queues.removeValue(forKey: ch)?.finish()
                default:
                    continue
                }
            }
        }
    }

    /// The channels of the attaches open now.
    var openChannels: Set<UInt32> { Set(attaches.keys) }

    /// Whether the link stream is open now.
    var linked: Bool { openLink != nil }

    private func linkEnded(_ id: String) {
        guard openLink?.id == id else { return }
        openLink = nil
        let lost = RelayError(code: "link_lost", message: "The link to the Mac broke")
        for a in attaches.values { a.end(lost) }
        attaches = [:]
        for q in queues.values { q.finish() }
        queues = [:]
    }

    /// Opens an attach of `agent` at `cols`x`rows` on a new channel of the
    /// link, waiting up to `wait` for the link to be up: read only,
    /// read-write, or as the size owner (`attachParams`). Returns once the Mac's reply line
    /// said ok; throws its error otherwise, `timeout` when no reply came.
    public func attach(
        agent: String, cols: Int, rows: Int, typing: Bool = false, owner: Bool = false, wait: Duration = .seconds(10)
    ) async throws -> AttachChannel {
        let clock = ContinuousClock()
        let deadline = clock.now + wait
        // Polled: the link comes back on its own retry loop (LiveLink).
        while openLink == nil {
            guard clock.now < deadline else { throw RelayError(code: "unavailable", message: "The Mac is not linked") }
            try await Task.sleep(for: .milliseconds(100))
        }
        guard let link = openLink, lastChannel < UInt32.max else {
            throw RelayError(code: "unavailable", message: "The Mac is not linked")
        }
        lastChannel += 1
        let ch = lastChannel
        // One queue per attach, drained by one task: typed bytes, resizes
        // and the close reach the Mac in the order the phone made them.
        // Every way an attach ends finishes its queue, so the task ends.
        let queue = OutboundQueue()
        let channel = AttachChannel(id: ch) { _ in
            queue.finish(with: .close)
        } onResize: { _, cols, rows in
            queue.frame(AttachDecoder.resize(cols: cols, rows: rows))
        } onWrite: { _, data in
            queue.frame(AttachDecoder.input(data))
        } onExit: { _ in
            queue.finish(with: .forget)
        }
        drains += 1
        Task { [weak self] in
            for await op in queue.ops {
                queue.taken(op)
                await self?.flush(op, on: ch, link: link.id)
            }
            await self?.drained(ch)
        }
        attaches[ch] = channel
        queues[ch] = queue
        let params = try Self.attachParams(link: link.id, ch: ch, agent: agent, cols: cols, rows: rows, typing: typing, owner: owner)
        let timeout = Task {
            try await Task.sleep(for: .seconds(15))
            channel.end(.timeout)
        }
        defer { timeout.cancel() }
        do {
            _ = try await call("agents.attach", params: params)
            let reply = try await channel.reply()
            // A refusal without a reason gets its own code: `unavailable`
            // means the link is down, and that is tried again.
            guard reply.ok else { throw reply.error ?? RelayError(code: "refused", message: "The Mac refused the terminal") }
        } catch {
            channel.end(error as? RelayError ?? RelayError(code: "closed"))
            // The request may have reached the Mac, which then holds the
            // viewer (and an owner's size) until the channel closes.
            queue.finish(with: .close)
            throw error
        }
        return channel
    }

    private func drained(_ ch: UInt32) {
        drains -= 1
        queues[ch] = nil
    }

    /// What an attach sends the Mac, in order.
    enum Outbound: Sendable {
        /// An inner frame (typed bytes or RESIZE) in a `D` frame.
        case frame(Data)
        /// `X`: the phone closed the attach.
        case close
        /// The agent exited: the Mac closes the channel, the phone only
        /// drops it.
        case forget
    }

    /// Sends one queued frame on an attach's channel, if the attach is
    /// still open and its link is still the open one. `close` removes it
    /// and sends `X`, unless the Mac closed the channel first.
    private func flush(_ op: Outbound, on ch: UInt32, link: String) async {
        switch op {
        case .frame(let payload):
            guard attaches[ch] != nil, openLink?.id == link else { return }
            try? await send(frame: LinkFrames.Frame(kind: Self.kindData, channel: ch, payload: payload))
        case .close:
            guard attaches.removeValue(forKey: ch) != nil, openLink?.id == link else { return }
            try? await send(frame: LinkFrames.Frame(kind: Self.kindClose, channel: ch, payload: Data()))
        case .forget:
            attaches[ch] = nil
        }
    }

    /// `agents.attach`'s params (relay/docs/protocol.md, "Links"): the
    /// local attach request, its mode repeated for the rights table. A
    /// typing attach is `rw` (right `type`), any other `ro`; the size
    /// owner (always typing) makes the PTY take `cols`x`rows`. All replay
    /// the history first.
    static func attachParams(link: String, ch: UInt32, agent: String, cols: Int, rows: Int, typing: Bool, owner: Bool) throws -> Data {
        let mode = typing || owner ? "rw" : "ro"
        let request: [String: Any] = ["attach": agent, "mode": mode, "cols": cols, "rows": rows, "owner": owner, "history": true]
        return try JSONSerialization.data(
            withJSONObject: ["link": link, "ch": Int(ch), "mode": mode, "request": request] as [String: Any], options: .sortedKeys)
    }

    /// Sends one frame on the open link: `X` to close a channel, RESIZE
    /// to size an owner attach, typed bytes for a read-write one.
    func send(frame: LinkFrames.Frame) async throws {
        guard let link = openLink else { throw RelayError(code: "unavailable", message: "The Mac is not linked") }
        // Sealed and handed to the socket in one actor turn, so the
        // implicit counters reach the Mac in order.
        let sealed = link.cipher.seal(
            kind: StreamCipher.data, payload: LinkFrames.encode(kind: frame.kind, channel: frame.channel, payload: frame.payload))
        try await link.socket.send(.data(sealed))
    }
}

/// An attach's frames for the Mac, in the order the phone made them. At
/// most `maxBytes` of typed input and resizes wait for a link that does
/// not keep up; past that `frame` refuses and the attach closes. The last
/// op (`X`, or forgetting an exited attach) is always queued.
final class OutboundQueue: Sendable {
    static let maxBytes = 1 << 20
    let ops: AsyncStream<HostChannel.Outbound>
    private let continuation: AsyncStream<HostChannel.Outbound>.Continuation
    private let queued = Mutex(0)

    init() {
        (ops, continuation) = AsyncStream.makeStream(of: HostChannel.Outbound.self)
    }

    /// Queues an inner frame; false, queueing nothing, when it would pass
    /// the bound.
    func frame(_ payload: Data) -> Bool {
        let fits = queued.withLock { q in
            guard q + payload.count <= Self.maxBytes else { return false }
            q += payload.count
            return true
        }
        guard fits else { return false }
        continuation.yield(.frame(payload))
        return true
    }

    /// The drain took `op` off the queue.
    func taken(_ op: HostChannel.Outbound) {
        if case .frame(let payload) = op { queued.withLock { $0 -= payload.count } }
    }

    /// Ends the queue after `last`; the drain sends what is queued, then
    /// stops. Later calls do nothing.
    func finish(with last: HostChannel.Outbound? = nil) {
        if let last { continuation.yield(last) }
        continuation.finish()
    }
}
