import Foundation

/// What `sessions.resume` (and fork / continueAs) gave: a new agent, or the
/// session is already running in Hesper (error data.code "live" with its
/// agentId): open that one instead, never resume twice.
public enum SessionStart: Equatable, Sendable {
    /// `note`: what did not come along (e.g. "resumed from the mirror on
    /// the pushed branch; uncommitted work stays on mini").
    case agent(Agent, note: String?)
    case live(agentId: String?)

    /// hesperd's answer for a running session (`sessions.resume`,
    /// `sessions.delete`): error `data.code` "live", `data.agentId` the
    /// agent to open (nil: it runs outside Hesper). nil for other errors.
    public static func live(from error: any Error) -> SessionStart? {
        guard let e = error as? RPCError, e.dataCode == "live" else { return nil }
        return .live(agentId: e.data?["agentId"]?.stringValue.flatMap { $0.isEmpty ? nil : $0 })
    }
}

// Shared history methods (docs "As built — shared history (app)"). Every
// call decodes on the calling task (callers run them off the main actor).
extension DaemonClient {
    public func searchSessions(_ q: HistoryQuery, cursor: String? = nil) async throws -> SessionPage {
        SessionPage(json: try await call("sessions.search", q.params(cursor: cursor)))
    }

    public func showSession(_ id: String) async throws -> SessionDetail {
        let v = try await call("sessions.show", .object(["id": .string(id)]))
        guard let d = SessionDetail(json: v["session"] ?? v) else { throw RPCError(code: -32000, message: "sessions.show: no session", kind: .unknown) }
        if d.changes == nil, let c = v["changes"].flatMap(SessionChanges.init(json:)) { return SessionDetail(session: d.session, changes: c) }
        return d
    }

    public func sessionStats() async throws -> SessionStats { SessionStats(json: try await call("sessions.stats")) }

    public func resumeSession(_ id: String, machine: String? = nil) async throws -> SessionStart {
        try await start("sessions.resume", id: id, extra: machine.map { ["machine": .string($0)] } ?? [:])
    }

    public func forkSession(_ id: String, machine: String? = nil) async throws -> SessionStart {
        try await start("sessions.fork", id: id, extra: machine.map { ["machine": .string($0)] } ?? [:])
    }

    /// hesperd builds the brief and starts `kind` in the session's folder.
    public func continueSession(_ id: String, kind: String, machine: String? = nil) async throws -> SessionStart {
        var p: [String: JSONValue] = ["kind": .string(kind)]
        if let machine { p["machine"] = .string(machine) }
        return try await start("sessions.continueAs", id: id, extra: p)
    }

    public func sessionBrief(_ id: String) async throws -> String {
        let v = try await call("sessions.brief", .object(["id": .string(id)]), timeout: 30)
        return v["text"]?.stringValue ?? v.stringValue ?? ""
    }

    public func archiveSession(_ id: String, archived: Bool) async throws {
        _ = try await call("sessions.archive", .object(["id": .string(id), "archived": .bool(archived)]))
    }

    /// hesperd keeps a deleted session 30 s; `undo` takes it back.
    public func deleteSession(_ id: String, undo: Bool = false) async throws {
        var p: [String: JSONValue] = ["id": .string(id)]
        if undo { p["undo"] = .bool(true) }
        _ = try await call("sessions.delete", .object(p))
    }

    private func start(_ method: String, id: String, extra: [String: JSONValue]) async throws -> SessionStart {
        var p = extra
        p["id"] = .string(id)
        do {
            // Resumed or forked on another Mac, the session travels there
            // first: as long as its transfer takes (agents.moving with
            // session set tells the progress).
            let v = try await call(method, .object(p), timeout: method == "sessions.continueAs" ? 120 : Self.transferTimeout)
            let a = v["agent"] ?? v
            return .agent(try a.decode(Agent.self), note: (v["note"] ?? a["note"])?.stringValue.flatMap { $0.isEmpty ? nil : $0 })
        } catch {
            if let live = SessionStart.live(from: error) { return live }
            throw error
        }
    }
}
