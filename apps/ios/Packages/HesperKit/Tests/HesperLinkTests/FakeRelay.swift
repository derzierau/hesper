import CryptoKit
import Foundation
import Synchronization
import Testing

@testable import HesperLink

/// A queue of socket messages: one side pushes, the other awaits.
final class Pipe: Sendable {
    private let state = Mutex<(buffer: [SocketMessage], waiters: [CheckedContinuation<SocketMessage, any Error>], closed: Bool)>(
        ([], [], false))

    func push(_ m: SocketMessage) {
        let waiter = state.withLock { s -> CheckedContinuation<SocketMessage, any Error>? in
            guard !s.closed else { return nil }
            if s.waiters.isEmpty {
                s.buffer.append(m)
                return nil
            }
            return s.waiters.removeFirst()
        }
        waiter?.resume(returning: m)
    }

    func next() async throws -> SocketMessage {
        try await withCheckedThrowingContinuation { c in
            let ready = state.withLock { s -> Result<SocketMessage, any Error>? in
                if !s.buffer.isEmpty { return .success(s.buffer.removeFirst()) }
                if s.closed { return .failure(RelayError.closed) }
                s.waiters.append(c)
                return nil
            }
            if let ready { c.resume(with: ready) }
        }
    }

    func close() {
        let waiters = state.withLock { s in
            s.closed = true
            defer { s.waiters = [] }
            return s.waiters
        }
        for w in waiters { w.resume(throwing: RelayError.closed) }
    }
}

/// The phone's end of a fake socket.
struct FakeSocket: RelaySocket {
    let inbound: Pipe
    let outbound: @Sendable (SocketMessage) -> Void

    func send(_ message: SocketMessage) async throws { outbound(message) }
    func receive() async throws -> SocketMessage { try await inbound.next() }
    func close() { inbound.close() }
}

/// A Mac behind the fake relay: answers `devices.request`, the channel's
/// handshake and signed requests, and serves its link.
final class FakeHost: Sendable {
    let id: String
    let short: String
    let key = Curve25519.KeyAgreement.PrivateKey()
    /// The offer secret it accepts; nil: a host from before offers (pending).
    let offer: Data?
    let agents: String

    struct State {
        var sessions: [String: E2ESession] = [:]
        var approvedKey: Data?
        var requests: [(method: String, params: Data)] = []
        var streams: [String: Data] = [:]
        /// The served links by stream id, the newest last in `served`.
        var links: [String: (cipher: StreamCipher, pipe: Pipe)] = [:]
        var served: [String] = []
        /// What `agents.attach` answers on the channel: this reply line,
        /// then (when ok) one data frame "hi".
        var attachReply = #"{"ok":true,"cols":97,"rows":56}"#
        /// Accept `agents.attach` but send nothing on the channel.
        var silentAttach = false
        /// The frames the phone sent on its links.
        var received: [LinkFrames.Frame] = []
    }
    let state = Mutex(State())

    init(id: String, short: String, offer: Data? = nil, agents: String = #"[{"id":"M/a1","state":"approval"}]"#) {
        self.id = id
        self.short = short
        self.offer = offer
        self.agents = agents
    }

    var hostKey: Data { key.publicKey.rawRepresentation }
    var methods: [String] { state.withLock { $0.requests.map(\.method) } }
    func params(_ method: String) -> [String: Any]? { state.withLock { $0.requests.last { $0.method == method }?.params }.map(jsonObject) }

    func handle(method: String, params: Data) throws -> Any {
        let p = (try? JSONSerialization.jsonObject(with: params)) as? [String: Any] ?? [:]
        switch method {
        case "devices.request":
            state.withLock { $0.requests.append((method, params)) }
            return try request(p)
        case "e2e.hello":
            var noise = NoiseResponder(static: key, prologue: E2E.prologue(machine: id, controller: "phone-1"))
            _ = try noise.readMessage(Data(base64Encoded: p["h"] as! String)!)
            let session = Data((0..<16).map { _ in UInt8.random(in: 0...255) }).hex
            let reply = try noise.writeMessage(payload: try JSONSerialization.data(withJSONObject: ["v": 1, "session": session]))
            state.withLock { $0.sessions[session] = E2ESession(id: session, send: reply.send, receive: reply.receive, peer: Data()) }
            return ["h": reply.message.base64EncodedString()]
        case "e2e":
            guard let s = state.withLock({ $0.sessions[p["s"] as! String] }) else { throw RelayError(code: "e2e_session") }
            let inner = try s.open(counter: (p["n"] as! NSNumber).uint64Value, ciphertext: Data(base64Encoded: p["c"] as! String)!)
            let answer = try self.inner(inner)
            let (n, c) = s.seal(try JSONSerialization.data(withJSONObject: answer))
            return ["n": n, "c": c.base64EncodedString()]
        default:
            throw RelayError(code: "e2e_required")
        }
    }

    private func request(_ p: [String: Any]) throws -> Any {
        let key = Data(base64Encoded: p["key"] as! String)!
        let strong = Data(base64Encoded: p["strongKey"] as! String)!
        if let offer, let proof = p["offerProof"] as? String {
            let want = OfferProof.make(
                secret: offer, machine: id, name: p["name"] as! String, key: p["key"] as! String, strongKey: p["strongKey"] as! String,
                e2eKey: p["e2eKey"] as? String)
            guard proof == want else { throw RelayError(code: "forbidden") }
            state.withLock { $0.approvedKey = key }
            return ["status": "approved", "name": p["name"]!, "rights": p["rights"]!, "hostKey": hostKey.base64EncodedString()]
        }
        return [
            "status": "pending", "code": ApprovalCode.make(hostKey: hostKey, key: key, strongKey: strong), "expires": 1_793_650_956,
            "hostKey": hostKey.base64EncodedString(),
        ]
    }

    /// An inner request: its signature checked against the approved key
    /// over the params bytes exactly as sent.
    private func inner(_ data: Data) throws -> [String: Any] {
        let o = try JSONSerialization.jsonObject(with: data) as! [String: Any]
        let text = String(decoding: data, as: UTF8.self)
        let raw = text[text.range(of: #","params":"#)!.upperBound..<text.range(of: #","auth":"#, options: .backwards)!.lowerBound]
        let method = o["method"] as! String
        let auth = o["auth"] as! [String: Any]
        let message = RequestSigning.message(
            machine: id, method: method, ts: (auth["ts"] as! NSNumber).int64Value, nonce: auth["nonce"] as! String,
            params: Data(raw.utf8))
        let sig = try P256.Signing.ECDSASignature(derRepresentation: Data(base64Encoded: auth["sig"] as! String)!)
        let approved = state.withLock { $0.approvedKey }
        guard let approved, try P256.Signing.PublicKey(derRepresentation: approved).isValidSignature(sig, for: message) else {
            return ["id": o["id"]!, "error": ["code": "forbidden", "message": "signature"]]
        }
        let params = o["params"] as? [String: Any] ?? [:]
        state.withLock { $0.requests.append((method, Data(raw.utf8))) }
        switch method {
        case "agents.link":
            let stream = "stream-\(UInt32.random(in: 0...UInt32.max))"
            let secret = Data((0..<32).map { _ in UInt8.random(in: 0...255) })
            state.withLock { $0.streams[stream] = secret }
            return ["id": o["id"]!, "result": ["id": stream, "e2e": secret.base64EncodedString()]]
        case "agents.attach":
            guard let stream = params["link"] as? String, state.withLock({ $0.links[stream] != nil }),
                let ch = (params["ch"] as? NSNumber)?.uint32Value, ch > 0
            else { return ["id": o["id"]!, "error": ["code": "not_found", "message": "No such link"]] }
            // As hesperd: the reply line and the first frames travel on
            // the channel, possibly before the call's answer.
            let (reply, silent) = state.withLock { ($0.attachReply, $0.silentAttach) }
            if silent { return ["id": o["id"]!, "result": [String: Any]()] }
            var bytes = Data(reply.utf8) + Data([10])
            if reply.contains(#""ok":true"#) { bytes += AttachTests.inner(0, Data("hi".utf8)) }
            push(kind: UInt8(ascii: "D"), channel: ch, bytes, on: stream)
            return ["id": o["id"]!, "result": [String: Any]()]
        case "drafts.list":
            return ["id": o["id"]!, "result": [[String: Any]]()]
        case "drafts.save":
            return ["id": o["id"]!, "result": params["draft"]!]
        default:
            return ["id": o["id"]!, "result": [String: Any]()]
        }
    }

    /// Serves a link stream: ready, then the agent list as one sealed
    /// event split over two messages.
    func serve(stream: String, to pipe: Pipe) throws {
        guard let secret = state.withLock({ $0.streams[stream] }) else { return pipe.close() }
        let cipher = try StreamCipher(secret: secret, streamID: stream, host: true)
        state.withLock { s in
            s.links[stream] = (cipher, pipe)
            s.served.append(stream)
        }
        pipe.push(.text(#"{"type":"ready"}"#))
        let event = Data(#"{"machine":"\#(short)","list":\#(agents),"full":true}"#.utf8)
        let frame = LinkFrames.encode(kind: UInt8(ascii: "E"), channel: 0, payload: event)
        pipe.push(.data(cipher.seal(kind: StreamCipher.data, payload: frame.prefix(5))))
        pipe.push(.data(cipher.seal(kind: StreamCipher.data, payload: frame.dropFirst(5))))
    }

    /// Sends one sealed link frame on a link (the newest by default).
    func push(kind: UInt8, channel: UInt32, _ payload: Data, on stream: String? = nil) {
        // Sealing and pushing under the lock keeps the counter order.
        state.withLock { s in
            guard let id = stream ?? s.served.last, let l = s.links[id] else { return }
            l.pipe.push(
                .data(l.cipher.seal(kind: StreamCipher.data, payload: LinkFrames.encode(kind: kind, channel: channel, payload: payload))))
        }
    }

    /// A message the phone sent on a link: one sealed frame.
    func receive(stream: String, _ message: SocketMessage) {
        guard case .data(let sealed) = message else { return }
        state.withLock { s in
            guard let l = s.links[stream], let (_, payload) = try? l.cipher.open(sealed) else { return }
            var frames = LinkFrames()
            s.received += (try? frames.append(payload)) ?? []
        }
    }

    var received: [LinkFrames.Frame] { state.withLock { $0.received } }
}

/// The relay: routes requests to its hosts and bridges link streams.
final class FakeRelay: SocketFactory {
    let hosts: [FakeHost]
    let online: Bool
    private let sockets = Mutex<[Pipe]>([])

    init(_ hosts: [FakeHost], online: Bool = true) {
        self.hosts = hosts
        self.online = online
    }

    func connect(_ request: URLRequest, subprotocol: String) async throws -> any RelaySocket {
        let pipe = Pipe()
        sockets.withLock { $0.append(pipe) }
        if request.url?.path() == "/v1/terminal" {
            #expect(expectHeader(request, subprotocol == "ghosty.terminal.v1"))
            let stream = request.value(forHTTPHeaderField: "X-Ghosty-Stream") ?? ""
            for h in hosts { try h.serve(stream: stream, to: pipe) }
            return FakeSocket(inbound: pipe, outbound: { [hosts] m in for h in hosts { h.receive(stream: stream, m) } })
        }
        #expect(expectHeader(request, subprotocol == "ghosty.v2"))
        let machines = hosts.map {
            ["id": $0.id, "name": "\($0.short) Mac", "online": online, "snapshot": ["short": $0.short]] as [String: Any]
        }
        pipe.push(
            .text(
                String(
                    decoding: try JSONSerialization.data(withJSONObject: ["v": 2, "type": "machines", "machines": machines]), as: UTF8.self)
            ))
        return FakeSocket(inbound: pipe) { [hosts] message in
            guard case .text(let text) = message, let e = try? Envelope(parsing: Data(text.utf8)) else { return }
            var answer: [String: Any] = ["v": 2, "type": "result", "id": e.id ?? ""]
            let raw = jsonObject(Data(text.utf8))["params"]
            let params = raw.flatMap { try? JSONSerialization.data(withJSONObject: $0) } ?? Data("{}".utf8)
            if let host = hosts.first(where: { $0.id == e.machineId }) {
                do {
                    answer["result"] = try host.handle(method: jsonObject(Data(text.utf8))["method"] as? String ?? "", params: params)
                } catch let error as RelayError {
                    answer["error"] = ["code": error.code, "message": error.message]
                } catch {
                    answer["error"] = ["code": "invalid_request", "message": "\(error)"]
                }
            } else {
                answer["error"] = ["code": "unavailable", "message": "no such machine"]
            }
            pipe.push(.text(String(decoding: try! JSONSerialization.data(withJSONObject: answer), as: UTF8.self)))
        }
    }

    func dropAll() { for p in sockets.withLock({ $0 }) { p.close() } }
}

/// Checks a socket request's bearer header (and a condition).
func expectHeader(_ request: URLRequest, _ ok: Bool) -> Bool {
    ok && request.value(forHTTPHeaderField: "Authorization") == "Bearer access-1"
}
