import Foundation
import Testing
@testable import HesperCore

/// A minimal JSON-RPC server on a Unix socket, scripted per test.
final class ScriptedServer: @unchecked Sendable {
    let path: String
    private var listenFD: Int32 = -1
    private let lock = NSLock()
    private var clients: [Int32] = []
    private(set) var received: [JSONValue] = []
    var handler: @Sendable (JSONValue, ScriptedServer, Int32) -> Void = { _, _, _ in }

    init() throws {
        signal(SIGPIPE, SIG_IGN)
        path = NSTemporaryDirectory() + "gt-\(UUID().uuidString.prefix(8)).sock"
        try start()
    }

    func start() throws {
        unlink(path)
        listenFD = socket(AF_UNIX, SOCK_STREAM, 0)
        var addr = sockaddr_un()
        addr.sun_family = sa_family_t(AF_UNIX)
        withUnsafeMutableBytes(of: &addr.sun_path) { raw in
            let b = Array(path.utf8); raw.copyBytes(from: b); raw[b.count] = 0
        }
        let rc = withUnsafePointer(to: &addr) { $0.withMemoryRebound(to: sockaddr.self, capacity: 1) { bind(listenFD, $0, socklen_t(MemoryLayout<sockaddr_un>.size)) } }
        guard rc == 0, listen(listenFD, 8) == 0 else { throw ConnectionError.connectFailed("bind") }
        let fd = listenFD
        Thread {
            while true {
                let c = accept(fd, nil, nil)
                if c < 0 { return }
                self.lock.withLock { self.clients.append(c) }
                Thread { self.serve(c) }.start()
            }
        }.start()
    }

    private func serve(_ fd: Int32) {
        var buf = Data()
        var chunk = [UInt8](repeating: 0, count: 4096)
        while true {
            let n = read(fd, &chunk, chunk.count)
            if n <= 0 { return }
            buf.append(contentsOf: chunk[0..<n])
            while let nl = buf.firstIndex(of: 0x0A) {
                let line = buf[buf.startIndex..<nl]
                buf.removeSubrange(buf.startIndex...nl)
                if let v = try? JSONDecoder().decode(JSONValue.self, from: Data(line)) {
                    lock.withLock { received.append(v) }
                    handler(v, self, fd)
                }
            }
        }
    }

    func send(_ fd: Int32, _ v: JSONValue) {
        var d = try! JSONEncoder().encode(v)
        d.append(0x0A)
        _ = d.withUnsafeBytes { write(fd, $0.baseAddress, $0.count) }
    }

    func reply(_ fd: Int32, _ req: JSONValue, result: JSONValue) {
        send(fd, ["jsonrpc": "2.0", "id": req["id"] ?? .null, "result": result])
    }

    func replyError(_ fd: Int32, _ req: JSONValue, code: Int, message: String, dataCode: String) {
        send(fd, ["jsonrpc": "2.0", "id": req["id"] ?? .null, "error": ["code": .number(Double(code)), "message": .string(message), "data": ["code": .string(dataCode)]]])
    }

    func dropClients() {
        lock.withLock { clients.forEach { shutdown($0, SHUT_RDWR); close($0) }; clients = [] }
    }

    func stop() {
        shutdown(listenFD, SHUT_RDWR)
        close(listenFD)
        dropClients()
        unlink(path)
    }

    var methods: [String] { lock.withLock { received.compactMap { $0["method"]?.stringValue } } }
}

private let agentJSON: JSONValue = ["id": "L/aaaaaa", "machine": "L", "kind": "claude", "name": "a", "state": "working", "size": ["cols": 120, "rows": 40]]
private let helloJSON: JSONValue = ["daemon": "test", "version": "1", "machine": "L", "machines": [["short": "L", "name": "laptop", "online": true, "rttMs": 0, "route": "local"]]]

/// The standard daemon behaviour for hello/subscribe/list.
private func standard(_ req: JSONValue, _ s: ScriptedServer, _ fd: Int32) {
    switch req["method"]?.stringValue {
    case "hello": s.reply(fd, req, result: helloJSON)
    case "agents.subscribe":
        s.reply(fd, req, result: [:])
        s.send(fd, ["jsonrpc": "2.0", "method": "agents.changed", "params": ["agent": agentJSON]])
    case "agents.list": s.reply(fd, req, result: [agentJSON])
    case "agents.move": s.replyError(fd, req, code: -32000, message: "remote agents arrive with part R", dataCode: "unavailable")
    case "agents.input": s.reply(fd, req, result: [:])
    case "nope": s.replyError(fd, req, code: -32601, message: "method not found", dataCode: "not_found")
    default: break  // never answers: timeouts
    }
}

private func collect(_ client: DaemonClient, until: @escaping ([DaemonEvent]) -> Bool) async -> [DaemonEvent] {
    var events: [DaemonEvent] = []
    let deadline = Date().addingTimeInterval(5)
    for await e in client.events {
        events.append(e)
        if until(events) || Date() > deadline { break }
    }
    return events
}

@Suite(.serialized) struct RPCClient {
    @Test func handshakeSubscribeAndReconcile() async throws {
        let server = try ScriptedServer()
        defer { server.stop() }
        server.handler = standard
        let client = DaemonClient(socketPath: server.path)
        client.start()
        let events = await collect(client) { $0.contains { if case .reconciled = $0 { return true }; return false } }
        client.stop()
        guard case .connected(let h) = events.first else { Issue.record("no hello: \(events)"); return }
        #expect(h.machine == "L" && h.machines.first?.route == "local")
        #expect(events.contains(.changed(try agentJSON.decode(Agent.self))))
        #expect(events.contains(.reconciled(["L/aaaaaa"])))
        #expect(server.methods.prefix(3) == ["hello", "agents.subscribe", "agents.list"])
        #expect(server.received.first?["params"]?["client"] == "Hesper.app")
    }

    @Test func errorsCarryDaemonCodes() async throws {
        let server = try ScriptedServer()
        defer { server.stop() }
        server.handler = standard
        let client = DaemonClient(socketPath: server.path)
        client.start()
        _ = await collect(client) { $0.contains { if case .reconciled = $0 { return true }; return false } }
        do {
            _ = try await client.move("L/aaaaaa", to: "M")
            Issue.record("expected an error")
        } catch let e as RPCError {
            #expect(e.kind == .unavailable && e.message.contains("part R"))
        }
        do {
            _ = try await client.call("nope")
            Issue.record("expected an error")
        } catch let e as RPCError {
            #expect(e.code == -32601 && e.kind == .notFound)
        }
        try await client.input("L/aaaaaa", text: "hi", paste: true, submit: true)
        let input = server.received.last { $0["method"] == "agents.input" }
        #expect(input?["params"] == ["id": "L/aaaaaa", "text": "hi", "paste": true, "submit": true])
        client.stop()
    }

    @Test func reconnectsAfterTheDaemonGoesAway() async throws {
        let server = try ScriptedServer()
        defer { server.stop() }
        server.handler = standard
        let client = DaemonClient(socketPath: server.path)
        client.start()
        var connects = 0
        var sawDisconnect = false
        let deadline = Date().addingTimeInterval(8)
        for await e in client.events {
            switch e {
            case .connected: connects += 1
            case .reconciled where connects == 1: server.dropClients()
            case .disconnected: sawDisconnect = true
            default: break
            }
            if connects == 2 || Date() > deadline { break }
        }
        client.stop()
        #expect(sawDisconnect && connects == 2)
    }

    @Test func notConnectedAndUnreachable() async throws {
        let client = DaemonClient(socketPath: NSTemporaryDirectory() + "missing-\(UUID().uuidString.prefix(6)).sock")
        await #expect(throws: ConnectionError.notConnected) { try await client.list() }
        client.start()
        let first = await collect(client) { _ in true }
        client.stop()
        guard case .disconnected(let why) = first.first else { Issue.record("\(first)"); return }
        #expect(why.contains("Cannot reach hesperd"))
    }

    @Test func callsTimeOut() async throws {
        let server = try ScriptedServer()
        defer { server.stop() }
        server.handler = standard
        let conn = try RPCConnection(path: server.path, onNotification: { _, _ in }, onClose: {})
        await #expect(throws: ConnectionError.timeout("silent")) { try await conn.call("silent", timeout: 0.2) }
        conn.close()
        await #expect(throws: ConnectionError.closed) { try await conn.call("hello") }
    }
}
