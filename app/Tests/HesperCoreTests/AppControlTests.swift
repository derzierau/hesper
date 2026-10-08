import Foundation
import Testing
@testable import HesperCore

/// App control (docs/rebuild-contract.md "As built — app control"): the
/// params of app.open / app.wall.set / app.desk, scope / band / project /
/// agent names, what wall.set does to a wall's view, and requests from
/// hesperd on the app's connection.
@Suite struct AppControl {
    static let catalog = ProjectCatalog(projects: [
        Project(id: "as", name: "acme-apps", kind: .repo, paths: ["L": "/p/acme-apps"], groups: ["g1"]),
        Project(id: "gh", name: "ghosty-config", kind: .repo, paths: ["L": "/Users/me/projects/ghosty-config"]),
    ], groups: [ProjectGroup(id: "g1", name: "acme apps", projectIds: ["as"], order: 0)])

    func band(_ key: String, _ title: String, project: String? = nil, group: String? = nil) -> Band {
        Band(key: key, title: title, subtitle: "", colorHex: "#000000", level: .project, groupID: group, projectID: project, branch: nil, members: [], continued: false)
    }

    // MARK: app.open

    @Test func openAgentModes() throws {
        #expect(try AppOpenRequest.parse(["agent": "L/a1"]).target == .agent("L/a1", .focus))
        for m in AppOpenMode.allCases {
            #expect(try AppOpenRequest.parse(["agent": "a1", "mode": .string(m.rawValue)]).target == .agent("a1", m))
        }
        #expect(throws: AppControlError.self) { try AppOpenRequest.parse(["agent": "a1", "mode": "sideways"]) }
        #expect(throws: AppControlError.self) { try AppOpenRequest.parse(["agent": "a1", "inbox": true]) }
        #expect(throws: AppControlError.self) { try AppOpenRequest.parse(["mode": "window"]) }
        #expect(throws: AppControlError.self) { try AppOpenRequest.parse("not an object") }
    }

    @Test func openWallComposerHistoryInbox() throws {
        #expect(try AppOpenRequest.parse(.null).target == .wall(nil, scope: nil, newWall: false))
        #expect(try AppOpenRequest.parse(["mode": "wall", "scope": "project:as"]).target == .wall(nil, scope: "project:as", newWall: false))
        #expect(try AppOpenRequest.parse(["wall": "home", "scope": "all"]).target == .wall(.home, scope: "all", newWall: false))
        #expect(try AppOpenRequest.parse(["newWall": true, "scope": "overflow"]).target == .wall(nil, scope: "overflow", newWall: true))
        let c = try AppOpenRequest.parse(["composer": ["project": "~/p", "task": "fix it", "kind": "codex", "worktree": true, "branch": "fix"]])
        #expect(c.target == .composer(ComposerPrefill(project: "~/p", task: "fix it", kind: "codex", worktree: true, branch: "fix"), wall: .current))
        #expect(throws: AppControlError.self) { try AppOpenRequest.parse(["composer": ["kind": "gpt"]]) }
        #expect(try AppOpenRequest.parse(["history": ["query": "login bug"]]).target == .history(query: "login bug"))
        #expect(try AppOpenRequest.parse(["history": true]).target == .history(query: nil))
        #expect(try AppOpenRequest.parse(["history": "redirect"]).target == .history(query: "redirect"))
        #expect(try AppOpenRequest.parse(["inbox": true]).target == .inbox)
    }

    @Test func wallTargetsLeaveTheFocusView() throws {
        // open wall (no args) after open agent: back to the wall, as ⌘Esc.
        #expect(try AppOpenRequest.parse(.null).target.showsWall)
        #expect(try AppOpenRequest.parse(["wall": "home"]).target.showsWall)
        #expect(try AppOpenRequest.parse(["composer": ["task": "x"]]).target.showsWall)
        #expect(try AppOpenRequest.parse(["agent": "a", "mode": "wall"]).target.showsWall)
        #expect(try !AppOpenRequest.parse(["agent": "a"]).target.showsWall)
        #expect(try !AppOpenRequest.parse(["agent": "a", "mode": "window"]).target.showsWall)
        #expect(try !AppOpenRequest.parse(["inbox": true]).target.showsWall)
    }

    @Test func agentNames() throws {
        let agents = [Agent(id: "L/a7f3k2", name: "push"), Agent(id: "M/b1", name: "push2"), Agent(id: "M/c1", name: "dup"), Agent(id: "L/c2", name: "dup")]
        #expect(try AgentRef.resolve("L/a7f3k2", agents: agents) == "L/a7f3k2")
        #expect(try AgentRef.resolve("a7f3k2", agents: agents) == "L/a7f3k2")
        #expect(try AgentRef.resolve("push2", agents: agents) == "M/b1")
        #expect(throws: AppControlError.invalid("dup names 2 agents: use an id (M/c1, L/c2)")) { try AgentRef.resolve("dup", agents: agents) }
        #expect(throws: AppControlError.notFound("no agent zz")) { try AgentRef.resolve("zz", agents: agents) }
    }

    @Test func wallRefs() throws {
        let walls = ["main", "wall-ab12", "wall-cd34"]
        #expect(try WallRef(nil).resolve(walls: walls, current: "wall-ab12") == "wall-ab12")
        #expect(try WallRef("current").resolve(walls: walls, current: "wall-ab12") == "wall-ab12")
        #expect(try WallRef("home").resolve(walls: walls, current: "wall-ab12") == "main")
        #expect(try WallRef("wall-cd34").resolve(walls: walls, current: "main") == "wall-cd34")
        #expect(try WallRef("2").resolve(walls: walls, current: "main") == "wall-ab12")
        #expect(throws: AppControlError.self) { try WallRef("wall-zz").resolve(walls: walls, current: "main") }
    }

    // MARK: Names

    @Test func scopesRoundTrip() throws {
        let c = Self.catalog
        #expect(try ScopeSpec.parse("all", catalog: c) == .all)
        #expect(try ScopeSpec.parse("overflow", catalog: c) == .overflow)
        #expect(try ScopeSpec.parse("needs-you", catalog: c) == ScopeSegment.needsYou.scope)
        #expect(try ScopeSpec.parse("project:acme-apps", catalog: c) == .project("as"))
        #expect(try ScopeSpec.parse("project:as", catalog: c) == .project("as"))
        #expect(try ScopeSpec.parse("ghosty-config", catalog: c) == .project("gh"))
        #expect(try ScopeSpec.parse("group:acme apps", catalog: c) == .group("g1"))
        let f = try ScopeSpec.parse("filter:machine=M,kind=codex,state=needs-you,state=working,project=as", catalog: c)
        #expect(f == .filter(WallFilter(projects: ["as"], machines: ["M"], kinds: ["codex"], states: [.needsYou, .working])))
        for s in [WallScope.all, .overflow, .project("as"), .group("g1"), f, ScopeSegment.working.scope] {
            #expect(try ScopeSpec.parse(ScopeSpec.format(s), catalog: c) == s)
        }
        #expect(throws: AppControlError.self) { try ScopeSpec.parse("project:nope", catalog: c) }
        #expect(throws: AppControlError.self) { try ScopeSpec.parse("filter:colour=red", catalog: c) }
        #expect(throws: AppControlError.self) { try ScopeSpec.parse("everything", catalog: c) }
    }

    @Test func projectNames() {
        let c = Self.catalog
        #expect(ProjectRef.resolve("gh", catalog: c) == "gh")
        #expect(ProjectRef.resolve("Ghosty-Config", catalog: c) == "gh")
        #expect(ProjectRef.resolve("~/projects/ghosty-config", catalog: c, home: "/Users/me") == "gh")
        #expect(ProjectRef.resolve("nope", catalog: c) == nil)
        #expect(ProjectRef.isFolder("~/x") && ProjectRef.isFolder("/x") && !ProjectRef.isFolder("acme-apps"))
    }

    @Test func bandNames() throws {
        let bands = [band("p:as", "acme-apps", project: "as"), band("g:g1", "acme apps", group: "g1"), band("p:x", "Dup"), band("p:y", "dup")]
        #expect(try BandRef.resolve("p:as", bands: bands) == "p:as")
        #expect(try BandRef.resolve("ACME-APPS", bands: bands) == "p:as")
        #expect(try BandRef.resolve("g1", bands: bands) == "g:g1")
        #expect(throws: AppControlError.self) { try BandRef.resolve("dup", bands: bands) }
        #expect(throws: AppControlError.self) { try BandRef.resolve("nope", bands: bands) }
    }

    // MARK: app.wall.set

    @Test func wallSetParams() throws {
        let r = try WallSetRequest.parse(["wall": "home", "arrangement": "main+stack", "grouping": "project", "density": "dense",
                                          "collapse": ["p:as"], "expand": "p:x,p:y", "sidebar": true, "ownWalls": "full", "home": true])
        #expect(r.wall == .home && r.arrangement == .mainStack && r.grouping == .project && r.minChars == 60)
        #expect(r.collapse == ["p:as"] && r.expand == ["p:x", "p:y"] && r.sidebar == true && r.ownWalls == .full && r.home)
        #expect(try WallSetRequest.parse(["minChars": 100]).minChars == 100)
        #expect(try WallSetRequest.parse(["density": "normal"]).minChars == 80)
        #expect(try WallSetRequest.parse(["arrangement": "Grid + Shelf"]).arrangement == .shelf)
        for a in WallArrangement.allCases { #expect(try WallSetRequest.parse(["arrangement": .string(a.rawValue)]).arrangement == a) }
        #expect(throws: AppControlError.self) { try WallSetRequest.parse([:]) }
        #expect(throws: AppControlError.self) { try WallSetRequest.parse(["arrangement": "spiral"]) }
        #expect(throws: AppControlError.self) { try WallSetRequest.parse(["density": "5"]) }
        #expect(throws: AppControlError.self) { try WallSetRequest.parse(["sidebar": "yes"]) }
    }

    @Test func wallSetMapsToTheView() throws {
        let bands = [band("p:as", "acme-apps", project: "as"), band("p:gh", "ghosty-config", project: "gh"), band(ViewResolver.newKey, "New")]
        let before = WallViewSettings(collapsed: ["p:gh"])
        var r = try WallSetRequest.parse(["arrangement": "columns", "density": "dense", "collapse": ["acme-apps"], "expand": ["ghosty-config"], "bandOrder": "gh,as"])
        let basics = before.applying(r)
        #expect(basics.arrangement == .columns && basics.minChars == 60 && basics.collapsed == ["p:gh"], "bands wait for step 2")
        let full = try basics.applyingBands(r, bands: bands)
        #expect(full.collapsed == ["p:as"] && full.bandOrder == ["p:gh", "p:as"])
        r = try WallSetRequest.parse(["collapse": "all"])
        #expect(try before.applyingBands(r, bands: bands).collapsed == ["p:as", "p:gh"], "all: every band but New")
        r = try WallSetRequest.parse(["expand": "all"])
        #expect(try before.applyingBands(r, bands: bands).collapsed.isEmpty)
        r = try WallSetRequest.parse(["collapse": "nope"])
        #expect(throws: AppControlError.self) { try before.applyingBands(r, bands: bands) }
    }

    // MARK: app.desk

    @Test func deskParamsAndNames() throws {
        #expect(try DeskRequest.parse(.null) == DeskRequest(action: .list))
        #expect(try DeskRequest.parse(["action": "ls"]) == DeskRequest(action: .list))
        #expect(try DeskRequest.parse(["action": "save", "name": "Deep work"]) == DeskRequest(action: .save, name: "Deep work"))
        #expect(try DeskRequest.parse(["action": "rm", "name": "x"]).action == .remove)
        #expect(try DeskRequest.parse(["action": "rename", "name": "a", "newName": "b"]) == DeskRequest(action: .rename, name: "a", newName: "b"))
        #expect(throws: AppControlError.self) { try DeskRequest.parse(["action": "switch"]) }
        #expect(throws: AppControlError.self) { try DeskRequest.parse(["action": "rename", "name": "a"]) }
        #expect(throws: AppControlError.self) { try DeskRequest.parse(["action": "explode", "name": "a"]) }

        let desks = [Desk(id: "d1", name: "Deep work", setup: "ds-other", automatic: false, windows: SavedWindows(), updated: 0),
                     Desk(id: "d2", name: "deep work", setup: "ds-here", automatic: false, windows: SavedWindows(), updated: 0)]
        #expect(DeskRequest.find("DEEP WORK", in: desks, setup: "ds-here")?.id == "d2", "this setup's desk first")
        #expect(DeskRequest.find("d1", in: desks, setup: "ds-here")?.id == "d1")
        #expect(DeskRequest.find("Office", in: desks, setup: "ds-here") == nil)
    }

    @Test func errorsCarryDaemonCodes() {
        let e = AppControlError.notFound("no agent x").rpc
        #expect(e.code == -32000 && e.kind == .notFound && e.dataCode == "not_found")
        #expect(AppControlError.invalid("bad").rpc.code == -32602)
    }
}

/// Requests from hesperd on the app's connection (app.register, then app.*).
@Suite(.serialized) struct AppControlConnection {
    @Test func registersAndAnswersRequests() async throws {
        let server = try ScriptedServer()
        defer { server.stop() }
        let answers = Answers()
        server.handler = { req, s, fd in
            switch req["method"]?.stringValue {
            case "hello": s.reply(fd, req, result: ["daemon": "test", "version": "1", "machine": "L", "machines": []])
            case "agents.subscribe", "app.register": s.reply(fd, req, result: [:])
            case "agents.list", "drafts.list", "projects.list", "groups.list": s.reply(fd, req, result: [])
            case nil: answers.add(req) // a response to our request
            default: break
            }
            if req["method"]?.stringValue == "app.register" {
                s.send(fd, ["jsonrpc": "2.0", "id": "app-1", "method": "app.state", "params": [:]])
                s.send(fd, ["jsonrpc": "2.0", "id": "app-2", "method": "app.desk", "params": ["action": "explode"]])
            }
        }
        let client = DaemonClient(socketPath: server.path)
        client.onAppRequest = { method, params in
            if method == "app.desk" {
                do { _ = try DeskRequest.parse(params) } catch let e as AppControlError { return .failure(e.rpc) } catch {}
            }
            return .success(["method": .string(method)])
        }
        client.start()
        let deadline = Date().addingTimeInterval(5)
        while answers.count < 2 && Date() < deadline { try await Task.sleep(nanoseconds: 20_000_000) }
        client.stop()
        #expect(server.methods.contains("app.register"))
        let got = answers.all
        #expect(got.first { $0["id"] == "app-1" }?["result"] == ["method": "app.state"])
        let err = got.first { $0["id"] == "app-2" }?["error"]
        #expect(err?["code"] == -32602 && err?["data"]?["code"] == "invalid")
    }

    @Test func withoutHandlerRequestsAreRefused() async throws {
        let server = try ScriptedServer()
        defer { server.stop() }
        let answers = Answers()
        server.handler = { req, s, fd in
            if req["method"] == nil { answers.add(req) }
            if req["method"]?.stringValue == "ping" {
                s.reply(fd, req, result: [:])
                s.send(fd, ["jsonrpc": "2.0", "id": "app-9", "method": "app.state"])
            }
        }
        let conn = try RPCConnection(path: server.path, onNotification: { _, _ in }, onClose: {})
        _ = try await conn.call("ping", timeout: 2)
        let deadline = Date().addingTimeInterval(3)
        while answers.count < 1 && Date() < deadline { try await Task.sleep(nanoseconds: 20_000_000) }
        conn.close()
        #expect(answers.all.first?["error"]?["code"] == -32601)
        #expect(!server.methods.contains("app.register"))
    }
}

private final class Answers: @unchecked Sendable {
    private let lock = NSLock()
    private var list: [JSONValue] = []
    func add(_ v: JSONValue) { lock.withLock { list.append(v) } }
    var all: [JSONValue] { lock.withLock { list } }
    var count: Int { lock.withLock { list.count } }
}
