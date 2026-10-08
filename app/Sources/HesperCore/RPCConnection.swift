import Foundation

/// Error codes the daemon puts in `error.data.code`.
public enum DaemonErrorCode: String, Sendable {
    case notFound = "not_found", invalid, exists, unavailable, forbidden, remote, unknown
}

public struct RPCError: Error, Equatable, Sendable, CustomStringConvertible {
    public var code: Int
    public var message: String
    public var kind: DaemonErrorCode
    /// The whole `error.data` (e.g. `sessions.resume`'s "live" with the
    /// agentId to open instead).
    public var data: JSONValue?
    public var description: String { message }
    public init(code: Int, message: String, kind: DaemonErrorCode, data: JSONValue? = nil) {
        self.code = code; self.message = message; self.kind = kind; self.data = data
    }
    /// `error.data.code` as sent (also codes outside DaemonErrorCode).
    public var dataCode: String? { data?["code"]?.stringValue }
}

public enum ConnectionError: Error, Equatable, Sendable, CustomStringConvertible {
    case connectFailed(String)
    case closed
    case timeout(String)
    case notConnected
    case protocolError(String)

    public var description: String {
        switch self {
        case .connectFailed(let s): return "Cannot reach hesperd: \(s)"
        case .closed: return "The connection to hesperd closed"
        case .timeout(let m): return "hesperd did not answer \(m) in time"
        case .notConnected: return "Not connected to hesperd"
        case .protocolError(let s): return "Protocol error: \(s)"
        }
    }
}

/// A Unix-socket line transport: newline-delimited frames in both
/// directions, read on a dedicated thread.
final class LineSocket: @unchecked Sendable {
    private let fd: Int32
    private let writeLock = NSLock()
    private var closed = false
    private let closeLock = NSLock()

    init(path: String) throws {
        fd = socket(AF_UNIX, SOCK_STREAM, 0)
        guard fd >= 0 else { throw ConnectionError.connectFailed(String(cString: strerror(errno))) }
        var addr = sockaddr_un()
        addr.sun_family = sa_family_t(AF_UNIX)
        let bytes = Array(path.utf8)
        guard bytes.count < MemoryLayout.size(ofValue: addr.sun_path) else {
            Darwin.close(fd)
            throw ConnectionError.connectFailed("socket path too long: \(path)")
        }
        withUnsafeMutableBytes(of: &addr.sun_path) { raw in
            raw.copyBytes(from: bytes)
            raw[bytes.count] = 0
        }
        var one: Int32 = 1
        setsockopt(fd, SOL_SOCKET, SO_NOSIGPIPE, &one, socklen_t(MemoryLayout<Int32>.size))
        let rc = withUnsafePointer(to: &addr) {
            $0.withMemoryRebound(to: sockaddr.self, capacity: 1) { Darwin.connect(fd, $0, socklen_t(MemoryLayout<sockaddr_un>.size)) }
        }
        guard rc == 0 else {
            let msg = String(cString: strerror(errno))
            Darwin.close(fd)
            throw ConnectionError.connectFailed("\(path): \(msg)")
        }
    }

    /// Starts the reader thread. `onLine` gets each line without the newline.
    func start(onLine: @escaping @Sendable (Data) -> Void, onClose: @escaping @Sendable () -> Void) {
        let fd = self.fd
        let t = Thread { [weak self] in
            var buffer = Data()
            var chunk = [UInt8](repeating: 0, count: 64 * 1024)
            while true {
                let n = chunk.withUnsafeMutableBytes { Darwin.read(fd, $0.baseAddress, $0.count) }
                if n <= 0 {
                    if n < 0 && errno == EINTR { continue }
                    break
                }
                buffer.append(contentsOf: chunk[0..<n])
                while let nl = buffer.firstIndex(of: 0x0A) {
                    let line = buffer[buffer.startIndex..<nl]
                    buffer.removeSubrange(buffer.startIndex...nl)
                    if !line.isEmpty { onLine(Data(line)) }
                }
            }
            self?.close()
            onClose()
        }
        t.name = "hesper.rpc.reader"
        t.qualityOfService = .userInteractive
        t.start()
    }

    func send(_ data: Data) throws {
        writeLock.lock()
        defer { writeLock.unlock() }
        var payload = data
        payload.append(0x0A)
        try payload.withUnsafeBytes { raw in
            var off = 0
            while off < raw.count {
                let n = Darwin.write(fd, raw.baseAddress! + off, raw.count - off)
                if n < 0 {
                    if errno == EINTR { continue }
                    throw ConnectionError.closed
                }
                off += n
            }
        }
    }

    func close() {
        closeLock.lock()
        defer { closeLock.unlock() }
        guard !closed else { return }
        closed = true
        shutdown(fd, SHUT_RDWR)
        Darwin.close(fd)
    }
}

/// JSON-RPC 2.0 over a LineSocket.
public final class RPCConnection: @unchecked Sendable {
    private let socket: LineSocket
    private let lock = NSLock()
    private var nextID = 1
    private var pending: [Int: CheckedContinuation<JSONValue, any Error>] = [:]
    private var isClosed = false
    private let onNotification: @Sendable (String, JSONValue) -> Void
    private let onClose: @Sendable () -> Void

    public init(path: String,
                onNotification: @escaping @Sendable (String, JSONValue) -> Void,
                onClose: @escaping @Sendable () -> Void) throws {
        socket = try LineSocket(path: path)
        self.onNotification = onNotification
        self.onClose = onClose
        socket.start(onLine: { [weak self] line in self?.handle(line) },
                     onClose: { [weak self] in self?.closedByPeer() })
    }

    public func call(_ method: String, _ params: JSONValue? = nil, timeout: TimeInterval = 10) async throws -> JSONValue {
        let id: Int = lock.withLock { defer { nextID += 1 }; return nextID }
        var msg: [String: JSONValue] = ["jsonrpc": "2.0", "id": .number(Double(id)), "method": .string(method)]
        if let params { msg["params"] = params }
        let data = try JSONEncoder().encode(JSONValue.object(msg))
        return try await withCheckedThrowingContinuation { cont in
            let closed: Bool = lock.withLock {
                if isClosed { return true }
                pending[id] = cont
                return false
            }
            if closed { cont.resume(throwing: ConnectionError.closed); return }
            do {
                try socket.send(data)
            } catch {
                if let c = lock.withLock({ pending.removeValue(forKey: id) }) { c.resume(throwing: error) }
                return
            }
            DispatchQueue.global().asyncAfter(deadline: .now() + timeout) { [weak self] in
                guard let self, let c = self.lock.withLock({ self.pending.removeValue(forKey: id) }) else { return }
                c.resume(throwing: ConnectionError.timeout(method))
            }
        }
    }

    public func close() {
        socket.close()
        failAll()
    }

    private func closedByPeer() {
        failAll()
        onClose()
    }

    private func failAll() {
        let conts: [CheckedContinuation<JSONValue, any Error>] = lock.withLock {
            isClosed = true
            defer { pending.removeAll() }
            return Array(pending.values)
        }
        conts.forEach { $0.resume(throwing: ConnectionError.closed) }
    }

    private func handle(_ line: Data) {
        guard let msg = try? JSONDecoder().decode(JSONValue.self, from: line) else { return }
        if let idValue = msg["id"], case .number(let n) = idValue, msg["method"] == nil {
            let id = Int(n)
            guard let cont = lock.withLock({ pending.removeValue(forKey: id) }) else { return }
            if let err = msg["error"] {
                cont.resume(throwing: Self.rpcError(err))
            } else {
                cont.resume(returning: msg["result"] ?? .null)
            }
        } else if let method = msg["method"]?.stringValue {
            onNotification(method, msg["params"] ?? .null)
        }
    }

    static func rpcError(_ v: JSONValue) -> RPCError {
        let code = Int(v["code"]?.doubleValue ?? 0)
        let message = v["message"]?.stringValue ?? "unknown error"
        let kind = v["data"]?["code"]?.stringValue.flatMap(DaemonErrorCode.init(rawValue:)) ?? .unknown
        return RPCError(code: code, message: message, kind: kind, data: v["data"])
    }
}
