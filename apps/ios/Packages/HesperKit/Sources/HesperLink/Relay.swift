import Foundation
import HesperFleet
import Synchronization

/// An error answer from the relay or a host (relay/docs/protocol.md,
/// "Host operations"): `forbidden`, `unavailable`, `e2e_session`, …
public struct RelayError: Error, Equatable, Sendable {
    public var code: String
    public var message: String

    public init(code: String, message: String = "") {
        self.code = code
        self.message = message
    }

    static let timeout = RelayError(code: "timeout", message: "No answer in time")
    static let closed = RelayError(code: "connection_lost", message: "Connection lost; outcome may be unknown.")
}

/// What a screen shows for it: the message, or the code when there is none.
extension RelayError: CodedError {}

extension RelayError: CustomStringConvertible {
    public var description: String { message.isEmpty ? code : message }
}

/// One machine of the relay's inventory, as much of it as the phone uses.
public struct RelayMachine: Equatable, Sendable {
    public var id: String
    public var name: String
    public var online: Bool
    /// The host's own short name (`snapshot.short`).
    public var short: String?
    /// The host's end-to-end key (`snapshot.e2eKey`), when it publishes one.
    public var e2eKey: Data?
    public var capabilities: [String: Bool]

    public init(id: String, name: String, online: Bool, short: String? = nil, e2eKey: Data? = nil, capabilities: [String: Bool] = [:]) {
        self.id = id
        self.name = name
        self.online = online
        self.short = short
        self.e2eKey = e2eKey
        self.capabilities = capabilities
    }

    init?(json: [String: Any]) {
        guard let id = json["id"] as? String else { return nil }
        let snapshot = json["snapshot"] as? [String: Any]
        self.init(
            id: id, name: json["name"] as? String ?? id, online: json["online"] as? Bool ?? false,
            short: snapshot?["short"] as? String,
            e2eKey: (snapshot?["e2eKey"] as? String).flatMap { Data(base64Encoded: $0) },
            capabilities: snapshot?["capabilities"] as? [String: Bool] ?? [:])
    }
}

/// One message of the v2 envelope (relay/docs/protocol.md, "WebSocket
/// connection"). `result` keeps its JSON bytes for the caller to decode.
struct Envelope {
    var type: String
    var id: String?
    var machineId: String?
    var result: Data?
    var error: RelayError?
    var removed = false
    var machines: [RelayMachine]?

    init(type: String, id: String? = nil, machineId: String? = nil, result: Data? = nil, error: RelayError? = nil) {
        self.type = type
        self.id = id
        self.machineId = machineId
        self.result = result
        self.error = error
    }

    init(parsing data: Data) throws {
        guard let o = try JSONSerialization.jsonObject(with: data) as? [String: Any], o["v"] as? Int == 2, let type = o["type"] as? String
        else { throw RelayError(code: "invalid_message", message: "not a v2 message") }
        self.type = type
        id = o["id"] as? String
        machineId = o["machineId"] as? String
        if let r = o["result"] { result = try JSONSerialization.data(withJSONObject: r, options: [.fragmentsAllowed]) }
        if let e = o["error"] as? [String: Any] {
            error = RelayError(code: e["code"] as? String ?? "", message: e["message"] as? String ?? "")
        }
        removed = o["removed"] as? Bool ?? false
        machines = (o["machines"] as? [[String: Any]])?.compactMap(RelayMachine.init(json:))
    }

    /// A request: `params` are sent as given (normalized JSON).
    static func request(id: String, machine: String, method: String, params: Data, auth: RequestAuth? = nil) -> Data {
        var out = Data(#"{"v":2,"type":"request","id":"#.utf8)
        out += json(id) + Data(#","machineId":"#.utf8) + json(machine) + Data(#","method":"#.utf8) + json(method)
        out += Data(#","params":"#.utf8) + params
        if let auth, let a = try? JSONEncoder().encode(auth) { out += Data(#","auth":"#.utf8) + a }
        return out + Data("}".utf8)
    }

    /// A JSON string literal.
    static func json(_ s: String) -> Data {
        (try? JSONEncoder().encode(s)) ?? Data("\"\"".utf8)
    }
}

/// A WebSocket message.
public enum SocketMessage: Equatable, Sendable {
    case text(String)
    case data(Data)
}

/// One WebSocket. The app uses `URLSessionSocket`; tests a fake relay.
public protocol RelaySocket: Sendable {
    func send(_ message: SocketMessage) async throws
    /// The next message; throws when the socket closed.
    func receive() async throws -> SocketMessage
    func close()
}

/// Opens sockets: `request` carries the URL, bearer and stream headers,
/// `subprotocol` the protocol name.
public protocol SocketFactory: Sendable {
    func connect(_ request: URLRequest, subprotocol: String) async throws -> any RelaySocket
}

/// Sockets on `URLSessionWebSocketTask`.
public struct URLSessionSockets: SocketFactory {
    let session: URLSession

    public init(session: URLSession = URLSession(configuration: .ephemeral)) { self.session = session }

    public func connect(_ request: URLRequest, subprotocol: String) async throws -> any RelaySocket {
        var request = request
        request.setValue(subprotocol, forHTTPHeaderField: "Sec-WebSocket-Protocol")
        let task = session.webSocketTask(with: request)
        task.maximumMessageSize = 2 << 20
        task.resume()
        return URLSessionSocket(task: task)
    }
}

struct URLSessionSocket: RelaySocket {
    let task: URLSessionWebSocketTask

    func send(_ message: SocketMessage) async throws {
        switch message {
        case .text(let s): try await task.send(.string(s))
        case .data(let d): try await task.send(.data(d))
        }
    }

    func receive() async throws -> SocketMessage {
        switch try await task.receive() {
        case .string(let s): return .text(s)
        case .data(let d): return .data(d)
        @unknown default: throw RelayError.closed
        }
    }

    func close() { task.cancel(with: .normalClosure, reason: nil) }
}

/// What the relay connection reports besides answers.
public enum RelayEvent: Equatable, Sendable {
    /// The inventory after a change: every machine the owner has.
    case inventory([RelayMachine])
    /// The socket closed; nothing more comes.
    case closed
}

/// The controller's connection to the relay (`/v1/connect`, `ghosty.v2`):
/// requests with their answers, and the owner's machines.
public final class RelayConnection: Sendable {
    public let origin: URL
    let token: String
    let factory: any SocketFactory
    private let socket: any RelaySocket
    private let state = Mutex(State())
    public let events: AsyncStream<RelayEvent>
    private let continuation: AsyncStream<RelayEvent>.Continuation

    private struct State {
        var pending: [String: CheckedContinuation<Data, any Error>] = [:]
        var machines: [String: RelayMachine] = [:]
        var inventory = false
        var inventoryWaiters: [CheckedContinuation<[RelayMachine], any Error>] = []
        var closed = false
        var next = 0
        var reader: Task<Void, Never>?
    }

    /// Connects with the access token. Throws when the socket cannot open.
    public static func connect(origin: URL, token: String, factory: any SocketFactory) async throws -> RelayConnection {
        let socket = try await factory.connect(socketRequest(origin, path: "/v1/connect", token: token), subprotocol: "ghosty.v2")
        let c = RelayConnection(origin: origin, token: token, factory: factory, socket: socket)
        c.start()
        return c
    }

    static func socketRequest(_ origin: URL, path: String, token: String) -> URLRequest {
        var u = URLComponents(url: origin, resolvingAgainstBaseURL: false)!
        u.scheme = u.scheme == "http" ? "ws" : "wss"
        u.path = path
        var r = URLRequest(url: u.url!)
        r.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
        return r
    }

    private init(origin: URL, token: String, factory: any SocketFactory, socket: any RelaySocket) {
        self.origin = origin
        self.token = token
        self.factory = factory
        self.socket = socket
        (events, continuation) = AsyncStream.makeStream(bufferingPolicy: .bufferingNewest(16))
    }

    private func start() {
        let task = Task { [weak self, socket] in
            while true {
                guard let message = try? await socket.receive() else { break }
                guard case .text(let text) = message, let e = try? Envelope(parsing: Data(text.utf8)) else { continue }
                self?.handle(e)
            }
            self?.shutdown()
        }
        state.withLock { $0.reader = task }
    }

    private func handle(_ e: Envelope) {
        switch e.type {
        case "result":
            guard let id = e.id, let waiter = state.withLock({ $0.pending.removeValue(forKey: id) }) else { return }
            if let error = e.error { waiter.resume(throwing: error) } else { waiter.resume(returning: e.result ?? Data("{}".utf8)) }
        case "machines", "machine.updated":
            let (list, waiters) = state.withLock { s in
                if e.type == "machines" { s.machines = [:] }
                if e.removed, let id = e.machineId { s.machines[id] = nil }
                for m in e.machines ?? [] { s.machines[m.id] = m }
                s.inventory = true
                let waiters = s.inventoryWaiters
                s.inventoryWaiters = []
                return (s.machines.values.sorted { $0.id < $1.id }, waiters)
            }
            for w in waiters { w.resume(returning: list) }
            continuation.yield(.inventory(list))
        default:
            break
        }
    }

    private func shutdown() {
        let (pending, waiters) = state.withLock { s in
            s.closed = true
            defer {
                s.pending = [:]
                s.inventoryWaiters = []
            }
            return (Array(s.pending.values), s.inventoryWaiters)
        }
        for p in pending { p.resume(throwing: RelayError.closed) }
        for w in waiters { w.resume(throwing: RelayError.closed) }
        continuation.yield(.closed)
        continuation.finish()
    }

    /// The owner's machines, once the relay sent them.
    public func machines() async throws -> [RelayMachine] {
        try await withCheckedThrowingContinuation { (c: CheckedContinuation<[RelayMachine], any Error>) in
            let ready: [RelayMachine]?? = state.withLock { s in
                if s.closed { return .some(nil) }
                if s.inventory { return s.machines.values.sorted { $0.id < $1.id } }
                s.inventoryWaiters.append(c)
                return nil
            }
            switch ready {
            case .some(.some(let list)): c.resume(returning: list)
            case .some(.none): c.resume(throwing: RelayError.closed)
            case .none: break
            }
        }
    }

    /// Sends one request to `machine` and waits for its result's JSON.
    /// Never retried: a timeout or a lost connection leaves the outcome
    /// unknown.
    public func request(
        machine: String, method: String, params: Data, auth: RequestAuth? = nil, timeout: Duration = .seconds(30)
    ) async throws -> Data {
        let id = state.withLock { s in
            s.next += 1
            return "p\(s.next)"
        }
        let frame = Envelope.request(id: id, machine: machine, method: method, params: params, auth: auth)
        let timer = Task { [weak self] in
            try? await Task.sleep(for: timeout)
            guard !Task.isCancelled else { return }
            self?.fail(id, RelayError.timeout)
        }
        defer { timer.cancel() }
        return try await withCheckedThrowingContinuation { (c: CheckedContinuation<Data, any Error>) in
            let open = state.withLock { s in
                guard !s.closed else { return false }
                s.pending[id] = c
                return true
            }
            guard open else { return c.resume(throwing: RelayError.closed) }
            Task { [socket] in
                do { try await socket.send(.text(String(decoding: frame, as: UTF8.self))) } catch { self.fail(id, RelayError.closed) }
            }
        }
    }

    private func fail(_ id: String, _ error: RelayError) {
        state.withLock { $0.pending.removeValue(forKey: id) }?.resume(throwing: error)
    }

    /// Opens a terminal stream (`/v1/terminal`) for a ticket and waits for
    /// the relay's `ready`.
    func openStream(_ ticket: String) async throws -> any RelaySocket {
        var request = Self.socketRequest(origin, path: "/v1/terminal", token: token)
        request.setValue(ticket, forHTTPHeaderField: "X-Ghosty-Stream")
        let socket = try await factory.connect(request, subprotocol: "ghosty.terminal.v1")
        guard case .text(let text) = try await socket.receive(),
            let o = try? JSONSerialization.jsonObject(with: Data(text.utf8)) as? [String: Any], o["type"] as? String == "ready"
        else {
            socket.close()
            throw RelayError(code: "unavailable", message: "The terminal stream did not open")
        }
        return socket
    }

    public func close() {
        socket.close()
        state.withLock { $0.reader }?.cancel()
    }

    public var isClosed: Bool { state.withLock { $0.closed } }
}
