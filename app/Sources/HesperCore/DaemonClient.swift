import Foundation

public enum DaemonEvent: Sendable, Equatable {
    case connected(HelloInfo)
    /// A fresh hello on the same connection (every 10 s): which Macs are
    /// online now. Only `hello` changes; nothing reconnects.
    case helloRefreshed(HelloInfo)
    case disconnected(String)
    case changed(Agent)
    /// `reason`: "closed", "finished-in-background", "removed" (nil: an
    /// older hesperd).
    case removed(String, reason: String? = nil)
    /// Every agent the daemon has right after (re)subscribing.
    case reconciled(Set<String>)
    /// Drafts (`drafts.changed` / `drafts.removed`, and `drafts.list` after
    /// each subscribe; nil: the daemon has no drafts methods).
    case draftChanged(Draft)
    case draftRemoved(String)
    case draftsListed([Draft]?)
    /// Projects and groups (`projects.list` + `groups.list` after each
    /// subscribe; nil: the daemon has no projects methods) and their
    /// notifications.
    case projectsListed([Project]?, [ProjectGroup]?)
    case projectChanged(Project)
    case projectRemoved(String)
    case groupChanged(ProjectGroup)
    case groupRemoved(String)
    /// agents.moving: where a move to another Mac is.
    case moving(MoveProgress)
    /// agents.removed of a moved agent (reason "moved"): it continues as
    /// `to`. Comes right before its `.removed`.
    case moved(String, to: String)
    /// agents.bringing: where bringing a draft's folder to another Mac is.
    case bringing(BringProgress)
}

/// The app's one control connection to the local hesperd. It keeps
/// reconnecting (backoff 0.1 s → 2 s), and after each connect does
/// hello → agents.subscribe → agents.list, so the app's view rebuilds itself
/// from the daemon after any restart of either side.
public final class DaemonClient: @unchecked Sendable {
    public let socketPath: String
    public let clientName: String
    public let clientVersion: String
    public let events: AsyncStream<DaemonEvent>
    private let continuation: AsyncStream<DaemonEvent>.Continuation
    private let lock = NSLock()
    private var connection: RPCConnection?
    private var running = false
    private var loop: Task<Void, Never>?
    private var extraNotifications: (@Sendable (String, JSONValue) -> Void)?

    /// Shared history (sessions.*): notifications the agent events don't
    /// cover, delivered on the connection's reader thread (decode there,
    /// hop to main coalesced).
    public var onOtherNotification: (@Sendable (String, JSONValue) -> Void)? {
        get { lock.withLock { extraNotifications } }
        set { lock.withLock { extraNotifications = newValue } }
    }

    /// App control: hesperd's requests (app.state, app.open, …: hesperctl
    /// through hesperd). Set before `start`: every connect then registers
    /// this connection as the app's (app.register; an older hesperd
    /// without it is ignored). Called on the reader thread.
    public typealias AppRequestHandler = @Sendable (_ method: String, _ params: JSONValue) async -> Result<JSONValue, RPCError>
    private var appRequests: AppRequestHandler?
    public var onAppRequest: AppRequestHandler? {
        get { lock.withLock { appRequests } }
        set { lock.withLock { appRequests = newValue } }
    }

    public init(socketPath: String, clientName: String = "Hesper.app", clientVersion: String = "0.1.0") {
        self.socketPath = socketPath
        self.clientName = clientName
        self.clientVersion = clientVersion
        (events, continuation) = AsyncStream.makeStream(of: DaemonEvent.self, bufferingPolicy: .unbounded)
    }

    public var isConnected: Bool { lock.withLock { connection != nil } }

    public func start() {
        let shouldStart: Bool = lock.withLock {
            if running { return false }
            running = true
            return true
        }
        guard shouldStart else { return }
        loop = Task.detached { [weak self] in await self?.runLoop() }
    }

    public func stop() {
        lock.withLock { running = false }
        loop?.cancel()
        lock.withLock { connection }?.close()
        continuation.finish()
    }

    private func runLoop() async {
        var backoff: UInt64 = 100_000_000
        while lock.withLock({ running }) && !Task.isCancelled {
            let closed = AsyncStream.makeStream(of: Void.self)
            do {
                let cont = continuation
                let conn = try RPCConnection(path: socketPath, onNotification: { [weak self] method, params in
                    switch method {
                    case "agents.changed":
                        if let a = params["agent"], let agent = try? a.decode(Agent.self) { cont.yield(.changed(agent)) }
                    case "agents.removed":
                        if let id = params["id"]?.stringValue {
                            if let to = MoveRemoval.newAgent(params) { cont.yield(.moved(id, to: to)) }
                            cont.yield(.removed(id, reason: params["reason"]?.stringValue))
                        }
                    case MoveRPC.progress:
                        if let p = MoveProgress(params: params) { cont.yield(.moving(p)) }
                    case BringRPC.progress:
                        if let p = BringProgress(params: params) { cont.yield(.bringing(p)) }
                    case "drafts.changed":
                        if let d = params["draft"], let draft = try? d.decode(Draft.self) { cont.yield(.draftChanged(draft)) }
                    case "drafts.removed":
                        if let id = params["id"]?.stringValue { cont.yield(.draftRemoved(id)) }
                    case "projects.changed":
                        if let p = params["project"], let project = try? p.decode(Project.self) { cont.yield(.projectChanged(project)) }
                    case "projects.removed":
                        if let id = params["id"]?.stringValue { cont.yield(.projectRemoved(id)) }
                    case "groups.changed":
                        if let g = params["group"], let group = try? g.decode(ProjectGroup.self) { cont.yield(.groupChanged(group)) }
                    case "groups.removed":
                        if let id = params["id"]?.stringValue { cont.yield(.groupRemoved(id)) }
                    default:
                        self?.onOtherNotification?(method, params) // shared history (sessions.*)
                    }
                }, onRequest: { [weak self] method, params, reply in
                    guard let handler = self?.onAppRequest else {
                        reply(.failure(RPCError(code: -32601, message: "no method \(method)", kind: .notFound, data: ["code": "not_found"])))
                        return
                    }
                    Task { reply(await handler(method, params)) }
                }, onClose: { closed.continuation.yield(); closed.continuation.finish() })
                lock.withLock { connection = conn }
                let hello = try await conn.call("hello", ["client": .string(clientName), "version": .string(clientVersion)]).decode(HelloInfo.self)
                continuation.yield(.connected(hello))
                _ = try await conn.call("agents.subscribe")
                let list = try await conn.call("agents.list").decode([Agent].self)
                continuation.yield(.reconciled(Set(list.map(\.id))))
                do {
                    continuation.yield(.draftsListed(try await conn.call("drafts.list").decode([Draft].self)))
                } catch let e as RPCError where e.code == -32601 {
                    continuation.yield(.draftsListed(nil))
                } catch let e as RPCError {
                    // Drafts are not worth the connection: keep the agents.
                    _ = e
                }
                do {
                    let projects = try await conn.call("projects.list").decode([Project].self)
                    let groups = (try? await conn.call("groups.list").decode([ProjectGroup].self)) ?? []
                    continuation.yield(.projectsListed(projects, groups))
                } catch let e as RPCError where e.code == -32601 {
                    continuation.yield(.projectsListed(nil, nil))
                } catch {
                    // Projects are not worth the connection either.
                }
                if onAppRequest != nil {
                    // App control: this connection takes hesperctl's app.* calls
                    // (an older hesperd answers -32601: nothing to do).
                    _ = try? await conn.call("app.register", ["client": .string(clientName)], timeout: 5)
                }
                backoff = 100_000_000
                // Machines come and go (another Mac restarts): refresh the
                // hello while connected, so "online" never goes stale.
                let refresher = Task {
                    while !Task.isCancelled {
                        try? await Task.sleep(nanoseconds: 10_000_000_000)
                        guard !Task.isCancelled else { break }
                        if let h = try? await conn.call("hello", ["client": .string(clientName), "version": .string(clientVersion)], timeout: 5).decode(HelloInfo.self) {
                            continuation.yield(.helloRefreshed(h))
                        }
                    }
                }
                for await _ in closed.stream { break }
                refresher.cancel()
                lock.withLock { connection = nil }
                continuation.yield(.disconnected("hesperd closed the connection"))
            } catch {
                lock.withLock { connection }?.close()
                lock.withLock { connection = nil }
                continuation.yield(.disconnected("\(error)"))
            }
            guard lock.withLock({ running }) else { break }
            try? await Task.sleep(nanoseconds: backoff)
            backoff = min(backoff * 2, 2_000_000_000)
        }
    }

    // MARK: Methods (contract table)

    private func conn() throws -> RPCConnection {
        guard let c = lock.withLock({ connection }) else { throw ConnectionError.notConnected }
        return c
    }

    public func call(_ method: String, _ params: JSONValue? = nil) async throws -> JSONValue {
        try await conn().call(method, params)
    }

    public func call(_ method: String, _ params: JSONValue?, timeout: TimeInterval) async throws -> JSONValue {
        try await conn().call(method, params, timeout: timeout)
    }

    public func list() async throws -> [Agent] { try await call("agents.list").decode([Agent].self) }

    /// A spawn that brings its folder first waits like a move (transfer).
    public func spawn(_ req: SpawnRequest) async throws -> Agent {
        if req.bring != nil { return try await call("agents.spawn", req.params, timeout: Self.transferTimeout).decode(Agent.self) }
        return try await call("agents.spawn", req.params).decode(Agent.self)
    }

    /// `paste`: wrap in bracketed paste when the agent has it on; `submit`:
    /// press Enter after it (hesperd additions, see "As built — Part D").
    public func input(_ id: String, text: String, paste: Bool = false, submit: Bool = false) async throws {
        var p: [String: JSONValue] = ["id": .string(id), "text": .string(text)]
        if paste { p["paste"] = true }
        if submit { p["submit"] = true }
        _ = try await call("agents.input", .object(p))
    }

    /// Where a file goes: an agent (its machine), or a draft's folder on a
    /// machine (a draft started on another machine).
    public enum FileTarget: Sendable, Equatable {
        case agent(String)
        case draft(machine: String, draft: String)
    }

    /// Starts an upload (files.put): returns the upload id and chunk size.
    public func filesPut(_ target: FileTarget, name: String, size: Int64, sha256: String) async throws -> (upload: String, chunk: Int) {
        var p: [String: JSONValue] = ["name": .string(name), "size": .number(Double(size)), "sha256": .string(sha256)]
        switch target {
        case .agent(let id): p["agent"] = .string(id)
        case .draft(let m, let d): p["machine"] = .string(m); p["draft"] = .string(d)
        }
        let r = try await call("files.put", .object(p), timeout: 60)
        guard let u = r["upload"]?.stringValue else { throw RPCError(code: -32000, message: "files.put returned no upload", kind: .remote) }
        return (u, Int(r["chunk"]?.doubleValue ?? Double(AttachmentUpload.chunkSize)))
    }

    /// One chunk (files.chunk); the last returns the path on the agent's
    /// machine.
    public func filesChunk(upload: String, offset: Int64, data: Data, last: Bool) async throws -> String? {
        let p: [String: JSONValue] = ["upload": .string(upload), "offset": .number(Double(offset)),
                                      "data": .string(data.base64EncodedString()), "last": .bool(last)]
        let r = try await call("files.chunk", .object(p), timeout: 90)
        return last ? r["path"]?.stringValue : nil
    }

    public func answer(_ id: String, decision: Decision, message: String? = nil) async throws {
        var p: [String: JSONValue] = ["id": .string(id), "decision": .string(decision.rawValue)]
        if let message, !message.isEmpty { p["message"] = .string(message) }
        _ = try await call("agents.answer", .object(p))
    }

    public func stopAgent(_ id: String) async throws { _ = try await call("agents.stop", ["id": .string(id)]) }
    /// agents.close: graceful (interrupt, wait for idle, end), then gone
    /// from the agents; its session stays in History. Returns the History
    /// session id when hesperd says. -32601: an older hesperd.
    @discardableResult
    public func closeAgent(_ id: String) async throws -> String? {
        let r = try await call("agents.close", ["id": .string(id)])
        return r["session"]?.stringValue
    }
    /// agents.kill: hard stop now; the agent stays (`exited`, ended "killed").
    public func killAgent(_ id: String) async throws { _ = try await call("agents.kill", ["id": .string(id)]) }
    /// agents.background: on no wall (true) or back (false); never touches the process.
    public func setBackground(_ id: String, _ on: Bool) async throws {
        _ = try await call("agents.background", ["id": .string(id), "background": .bool(on)])
    }
    public func resume(_ id: String) async throws -> Agent { try await call("agents.resume", ["id": .string(id)]).decode(Agent.self) }
    public func remove(_ id: String) async throws { _ = try await call("agents.remove", ["id": .string(id)]) }
    public func rename(_ id: String, name: String) async throws -> Agent { try await call("agents.rename", ["id": .string(id), "name": .string(name)]).decode(Agent.self) }
    /// agents.move: checkpoint, carry and resume on `machine` (hesperd
    /// closes it here unless `fork`). Returns the new agent's id. Preflight
    /// refusals carry `data.code` (`MovePreflight`); -32601: an older
    /// hesperd. A move carries a bundle: as long as its transfer takes
    /// (transferTimeout).
    @discardableResult
    public func move(_ id: String, to machine: String, options: MoveOptions = MoveOptions()) async throws -> String? {
        MoveRemoval.newAgent(result: try await call(MoveRPC.move, options.params(id: id, to: machine), timeout: Self.transferTimeout))
    }

    /// How long a call that carries work to another Mac (a move, a bring,
    /// a session resumed or forked there) is waited for: hesperd fails a
    /// transfer when nothing moved for a minute and bounds it at 2 hours,
    /// so the app never gives up on one that progresses (agents.moving /
    /// agents.bringing show it).
    public static let transferTimeout: TimeInterval = 2 * 3600 + 120
    /// agents.checkpoint: a checkpoint now (nil: not a git folder).
    public func checkpoint(_ id: String) async throws -> Checkpoint? {
        Checkpoint(json: try await call(MoveRPC.checkpoint, ["id": .string(id)], timeout: 60)["checkpoint"])
    }
    /// checkpoints.restore: a gone agent's checkpoint as a worktree on its
    /// Mac (the session's). Returns the worktree's path when hesperd says.
    public func restoreCheckpoint(session: String, checkpoint: Checkpoint, machine: String?) async throws -> String? {
        var p: [String: JSONValue] = ["session": .string(session), "ref": .string(checkpoint.ref), "commit": .string(checkpoint.commit)]
        if let machine, !machine.isEmpty { p["machine"] = .string(machine) }
        let r = try await call(MoveRPC.restore, .object(p), timeout: 120)
        return r["path"]?.stringValue ?? r["worktree"]?.stringValue
    }
    public func recentProjects() async throws -> [RecentProject] { try await call("projects.recent").decode([RecentProject].self) }
    public func profiles() async throws -> ProfilesInfo { try await call("profiles.list").decode(ProfilesInfo.self) }
    public func saveDraft(_ d: Draft) async throws -> Draft { try await call("drafts.save", d.params).decode(Draft.self) }
    public func removeDraft(_ id: String) async throws { _ = try await call("drafts.remove", ["id": .string(id)]) }
    /// fs.stat: is `path` a folder on `machine` (nil: this one)? Throws
    /// RPCError -32601 on a daemon without it.
    public func folderExists(_ path: String, machine: String?) async throws -> Bool {
        var p: [String: JSONValue] = ["path": .string(path)]
        if let machine { p["machine"] = .string(machine) }
        let v = try await call("fs.stat", .object(p), timeout: 20)
        return v["exists"] == .bool(true) && v["isDir"] == .bool(true)
    }

    // MARK: Projects and groups (data agent's step 1; see "As built — projects (views)")

    /// `projects.update {id, …fields}` → Project (name, color, defaults, groups).
    public func updateProject(_ id: String, _ fields: [String: JSONValue]) async throws -> Project {
        var p = fields
        p["id"] = .string(id)
        return try await call("projects.update", .object(p)).decode(Project.self)
    }

    /// `projects.promote {machine?, path, name?, kind?}` → Project (a scratch folder becomes a project).
    public func promoteProject(path: String, machine: String?, name: String? = nil, kind: ProjectKind? = nil) async throws -> Project {
        var p: [String: JSONValue] = ["path": .string(path)]
        if let machine { p["machine"] = .string(machine) }
        if let name, !name.isEmpty { p["name"] = .string(name) }
        if let kind { p["kind"] = .string(kind.rawValue) }
        return try await call("projects.promote", .object(p)).decode(Project.self)
    }

    public func removeProject(_ id: String) async throws { _ = try await call("projects.remove", ["id": .string(id)]) }

    // MARK: Scratch projects (scratch contract; -32601: an older hesperd)

    /// `projects.scratch {name?, task?, machine?}` → Project.
    public func createScratch(name: String? = nil, task: String? = nil, machine: String? = nil) async throws -> Project {
        var p: [String: JSONValue] = [:]
        if let name, !name.isEmpty { p["name"] = .string(name) }
        if let task, !task.isEmpty { p["task"] = .string(task) }
        if let machine { p["machine"] = .string(machine) }
        return try await call(ScratchRPC.create, .object(p), timeout: 60).decode(Project.self)
    }

    /// `projects.scratchKeep {id, keep}`.
    public func keepScratch(_ id: String, keep: Bool) async throws {
        _ = try await call(ScratchRPC.keep, ["id": .string(id), "keep": .bool(keep)])
    }

    /// `projects.scratchArchive {id}` (moves the folder under ~/scratch/.archive).
    public func archiveScratch(_ id: String) async throws { _ = try await call(ScratchRPC.archive, ["id": .string(id)], timeout: 60) }

    /// `projects.scratchRestore {id}` (back from the archive, resting).
    public func restoreScratch(_ id: String) async throws { _ = try await call(ScratchRPC.restore, ["id": .string(id)], timeout: 60) }

    /// `projects.promote {id, name?, createRepo?: "github"}` → Project: the
    /// scratch's folder becomes `~/projects/<name>`, same id. hesperd
    /// refuses ("busy") while agents run in it.
    public func promoteScratch(_ id: String, name: String?, createRepo: Bool) async throws -> Project {
        var p: [String: JSONValue] = ["id": .string(id)]
        if let name, !name.isEmpty { p["name"] = .string(name) }
        if createRepo { p["createRepo"] = .string("github") }
        return try await call(ScratchRPC.promote, .object(p), timeout: 120).decode(Project.self)
    }

    /// `settings.get {keys}` → hesperd's values for `keys`.
    public func settings(_ keys: [String]) async throws -> JSONValue {
        try await call(ScratchRPC.settingsGet, ["keys": .array(keys.map(JSONValue.string))], timeout: 10)
    }

    /// `settings.set {values}`.
    public func setSettings(_ params: JSONValue) async throws { _ = try await call(ScratchRPC.settingsSet, params, timeout: 10) }

    /// `groups.save {group}` → Group (creates with an empty id, else replaces).
    public func saveGroup(_ g: ProjectGroup) async throws -> ProjectGroup {
        let data = try JSONEncoder().encode(g)
        let v = try JSONDecoder().decode(JSONValue.self, from: data)
        return try await call("groups.save", .object(["group": v])).decode(ProjectGroup.self)
    }

    /// projects.list + groups.list now (an agent named an unknown project).
    public func listProjects() async throws -> ([Project], [ProjectGroup]) {
        let p = try await call("projects.list").decode([Project].self)
        let g = (try? await call("groups.list").decode([ProjectGroup].self)) ?? []
        return (p, g)
    }

    public func removeGroup(_ id: String) async throws { _ = try await call("groups.remove", ["id": .string(id)]) }
}
