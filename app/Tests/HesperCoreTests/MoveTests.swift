import Foundation
import Testing
@testable import HesperCore

private func rpcError(_ code: Int = -32000, _ message: String = "refused", dataCode: String? = nil, data: [String: JSONValue] = [:]) -> RPCError {
    var d = data
    if let dataCode { d["code"] = .string(dataCode) }
    return RPCError(code: code, message: message, kind: .remote, data: d.isEmpty ? nil : .object(d))
}

private let machines = [
    Machine(short: "laptop", name: "ABC123", online: true, route: "local"),
    Machine(short: "mini", name: "Mac mini", online: true, route: "relay"),
    Machine(short: "studio", name: "Studio", online: false),
]

@Suite struct MovePreflightMapping {
    @Test func dataCodesMapToAsks() {
        #expect(MovePreflight(rpcError(dataCode: "busy")) == .busy)
        #expect(MovePreflight(rpcError(dataCode: "no-remote")) == .noRemote)
        #expect(MovePreflight(rpcError(dataCode: "too-large")) == .tooLarge)
        #expect(MovePreflight(rpcError(dataCode: "offline")) == .offline)
        #expect(MovePreflight(rpcError(-32010, "mini is offline")) == .offline)
        #expect(MovePreflight(rpcError(dataCode: "tool-missing", data: ["tool": "codex"])) == .toolMissing("codex"))
        #expect(MovePreflight(rpcError(dataCode: "tool-missing"), tool: "claude") == .toolMissing("claude"))
        #expect(MovePreflight(rpcError(-32000, "worktree is locked", dataCode: "invalid")) == .other("worktree is locked"))
        let procs: JSONValue = [["pid": 412, "command": "/usr/local/bin/npm run dev"], ["pid": 413, "command": "vite --port 5173"]]
        #expect(MovePreflight(rpcError(dataCode: "processes", data: ["processes": procs]))
            == .processes([MoveProcess(pid: 412, command: "/usr/local/bin/npm run dev"), MoveProcess(pid: 413, command: "vite --port 5173")]))
    }

    @Test func olderDaemonsAreNotAsks() {
        #expect(MovePreflight(rpcError(-32601, "method not found")) == nil)
        #expect(MovePreflight(rpcError(-32000, "Unknown method agents.move")) == nil)
        #expect(MoveRPC.missing(rpcError(-32000, "unknown method")))
    }

    @Test func messagesAndActions() {
        #expect(MovePreflight.busy.message(name: "migrations", target: "mini") == "migrations is working")
        #expect(MovePreflight.busy.primary()?.title == "Interrupt and move")
        #expect(MovePreflight.busy.primary()?.options == MoveOptions(interrupt: true))
        let p = MovePreflight.processes([MoveProcess(pid: 1, command: "/usr/local/bin/npm run dev"), MoveProcess(pid: 2, command: "vite")])
        #expect(p.message(name: "a", target: "mini") == "npm run dev and 1 more are still running here")
        #expect(p.primary()?.title == "Move anyway (leave them running)")
        #expect(p.primary(tight: true)?.title == "Move anyway")
        #expect(p.primary()?.options == MoveOptions(leaveProcesses: true))
        #expect(p.detail == "1  /usr/local/bin/npm run dev\n2  vite")
        let three = MovePreflight.processes([MoveProcess(pid: 1, command: "a"), MoveProcess(pid: 2, command: "b"), MoveProcess(pid: 3, command: "c")])
        #expect(three.message(name: "x", target: "mini") == "a and 2 more are still running here")
        #expect(MovePreflight.toolMissing("codex").message(name: "a", target: "mini") == "Codex isn't installed on mini")
        #expect(MovePreflight.noRemote.message(name: "a", target: "mini", project: "api") == "api isn't on mini and has no git remote to clone")
        #expect(MovePreflight.offline.message(name: "a", target: "mini") == "mini is offline")
        #expect(MovePreflight.tooLarge.message(name: "a", target: "mini").contains("200 MB"))
        for ask in [MovePreflight.toolMissing("claude"), .noRemote, .offline, .tooLarge, .other("x")] {
            #expect(ask.primary() == nil && !ask.actionable)
        }
        #expect(MovePreflight.busy.actionable)
    }

    @Test func optionsBecomeParams() {
        #expect(MoveOptions().params(id: "L/a", to: "mini") == ["id": "L/a", "to": "mini"])
        #expect(MoveOptions(fork: true, interrupt: true, leaveProcesses: true).params(id: "L/a", to: "mini")
            == ["id": "L/a", "to": "mini", "fork": true, "interrupt": true, "leaveProcesses": true])
    }

    @Test func shortCommands() {
        #expect(MoveText.shortCommand("/usr/local/bin/node /Users/me/api/node_modules/.bin/vite --port 5173") == "node vite --port")
        #expect(MoveText.shortCommand("npm run dev") == "npm run dev")
    }
}

@Suite struct MoveProgressText {
    @Test func parsesNotifications() {
        let p = MoveProgress(params: ["id": "L/a", "step": "transfer", "to": "mini", "percent": 42])
        #expect(p == MoveProgress(id: "L/a", to: "mini", step: .transfer, percent: 42))
        #expect(MoveProgress(params: ["id": "L/a", "step": "transfer 64%", "to": "mini"])?.percent == 64)
        #expect(MoveProgress(params: ["id": "L/a", "step": "transfer", "progress": .number(0.5)])?.percent == 50)
        #expect(MoveProgress(params: ["id": "L/a", "step": "transfer", "bytes": 25, "total": 100])?.percent == 25)
        #expect(MoveProgress(params: ["id": "L/a", "step": "resumed"])?.step == .resume)
        #expect(MoveProgress(params: ["id": "L/a", "step": "checkpoint"])?.step == .checkpoint)
        #expect(MoveProgress(params: ["step": "checkpoint"]) == nil)
    }

    @Test func line() {
        #expect(MoveProgress(id: "a", to: "mini").line(target: "mini") == "Moving to mini…")
        let t = MoveProgress(id: "a", to: "mini", step: .transfer, percent: 42)
        #expect(t.line(target: "mini") == "Moving to mini · checkpoint → transfer 42% → worktree → resuming")
        #expect(t.steps.map(\.status) == [.done, .current, .pending, .pending])
        #expect(MoveProgress(id: "a", to: "mini", step: .worktree).line(target: "mini", fork: true)
            == "Forking to mini · checkpoint → transfer → worktree → resuming")
        #expect(MoveProgress(id: "a", to: "mini").steps.allSatisfy { $0.status == .pending })
    }

    @Test func newAgentFromRemovalAndReply() {
        #expect(MoveRemoval.newAgent(["id": "L/a", "reason": "moved", "data": ["to": "M/b"]]) == "M/b")
        #expect(MoveRemoval.newAgent(["id": "L/a", "reason": "moved", "to": "M/b"]) == "M/b")
        #expect(MoveRemoval.newAgent(["id": "L/a", "reason": "closed", "data": ["to": "M/b"]]) == nil)
        #expect(MoveRemoval.newAgent(result: ["agent": "M/b"]) == "M/b")
        #expect(MoveRemoval.newAgent(result: ["agent": ["id": "M/b"]]) == "M/b")
        #expect(MoveRemoval.newAgent(result: [:]) == nil)
        #expect(RemovalReason(rawValue: "moved") == .moved)
    }
}

@Suite struct MoveActions {
    func agent(_ state: AgentState, kind: String = "claude", machine: String = "laptop") -> Agent {
        Agent(id: "\(machine)/a", machine: machine, kind: kind, name: "migrations", state: state)
    }

    @Test func targetsPerStateAndMachine() {
        #expect(MoveRules.targets(agent(.done), machines: machines, supported: nil).map(\.short) == ["mini"]) // offline studio left out
        #expect(MoveRules.targets(agent(.done, machine: "mini"), machines: machines, supported: true).map(\.short) == ["laptop"])
        #expect(MoveRules.targets(agent(.working), machines: machines, supported: nil).map(\.short) == ["mini"]) // asks "busy" first
        #expect(MoveRules.targets(agent(.done), machines: machines, supported: false).isEmpty) // older hesperd there
        #expect(MoveRules.targets(agent(.done, kind: "shell"), machines: machines, supported: true).isEmpty)
        #expect(MoveRules.targets(agent(.starting), machines: machines, supported: true).isEmpty)
        #expect(MoveRules.targets(agent(.done), machines: machines, supported: true, moving: true).isEmpty)
        #expect(MoveRules.targets(agent(.done, kind: "codex"), machines: [machines[0]], supported: true).isEmpty) // one Mac
    }

    @Test func whereTheyShow() {
        #expect(MoveRules.showsOnStrip(agent(.done)) && MoveRules.showsOnStrip(agent(.idle)))
        #expect(!MoveRules.showsOnStrip(agent(.working)) && !MoveRules.showsOnStrip(agent(.approval)))
        #expect(MoveRules.showsInInbox(agent(.approval)) && MoveRules.showsInInbox(agent(.question)) && MoveRules.showsInInbox(agent(.error)))
        #expect(MoveText.continueTitle("mini", name: "migrations") == "Continue migrations on mini")
        #expect(MoveText.forkTitle("mini") == "Fork on mini")
        #expect(MoveText.moved("migrations", to: "mini") == "Moved migrations to mini")
    }

    @Test func inboxAndHistoryKeys() {
        #expect(InboxKeys.route(.type("m")) == .moveElsewhere)
        #expect(InboxKeys.route(.type("M")) == .moveElsewhere)
        #expect(SearchKeys.history(KeyChord(.char("r")), characters: "r", focus: .list) == .restoreCheckpoint)
        #expect(SearchKeys.history(KeyChord(.char("r")), characters: "r", focus: .search) != .restoreCheckpoint)
        #expect(SearchKeys.history(KeyChord(.enter, command: true), characters: nil, focus: .list) == .continueOnOtherMac)
    }

    @Test func historyPreviewMovesLiveAgents() {
        let a = agent(.done)
        let live = Session(id: "laptop:claude:s1", sessionId: "s1", machine: "laptop", title: "migrations", live: SessionLive(agentId: a.id))
        #expect(SearchPreview.moveTarget(live, agent: a, machines: machines, supported: nil) == "mini")
        #expect(SearchPreview.moveTarget(live, agent: a, machines: machines, supported: false) == nil)
        #expect(SearchPreview.moveTarget(live, agent: nil, machines: machines, supported: nil) == nil)
        let card = SessionCardText(live, changes: nil, loadingChanges: true, local: "laptop", machines: [:])
        let acts = SearchPreview.actions(live, card: card, local: "laptop", online: ["laptop", "mini"], machines: [:], moveTo: "mini")
        #expect(acts.contains { $0.key == "⌘⏎" && $0.title == "Continue on mini" })
        #expect(SearchPreview.continueNote(live, local: "laptop", online: ["laptop", "mini"], machines: [:], moveTo: "mini")?.contains("moves it to mini") == true)
        // Not live: today's continue-on-other-Mac resume, unchanged.
        let past = Session(id: "laptop:claude:s2", sessionId: "s2", machine: "laptop", title: "old")
        let pc = SessionCardText(past, changes: nil, loadingChanges: true, local: "laptop", machines: [:])
        #expect(SearchPreview.actions(past, card: pc, local: "laptop", online: ["laptop", "mini"], machines: [:]).contains { $0.key == "⌘⏎" && $0.title == "Continue on mini" })
    }

    @Test func historyRestoreCheckpoint() {
        let cp = Checkpoint(ref: "refs/hesper/checkpoints/a", commit: "abc", changed: 3, branch: "users-col")
        let gone = Session(id: "laptop:claude:s1", sessionId: "s1", machine: "laptop", title: "x", removedAt: Date(), checkpoint: cp)
        #expect(SearchPreview.restoreOffered(gone, supported: nil))
        #expect(!SearchPreview.restoreOffered(gone, supported: false))
        var live = gone
        live.live = SessionLive(agentId: "laptop/a")
        #expect(!SearchPreview.restoreOffered(live, supported: true))
        #expect(!SearchPreview.restoreOffered(Session(id: "x"), supported: true))
        let card = SessionCardText(gone, changes: nil, loadingChanges: true, local: "laptop", machines: [:])
        #expect(SearchPreview.actions(gone, card: card, local: "laptop", online: [], machines: [:], restore: true).contains { $0.key == "R" && $0.title == "Restore checkpoint" })
        #expect(!SearchPreview.actions(gone, card: card, local: "laptop", online: [], machines: [:]).contains { $0.key == "R" })
    }
}

@Suite struct CheckpointDecoding {
    @Test func agentAndSessionCarryCheckpoints() throws {
        let json: JSONValue = ["id": "L/a", "machine": "L", "kind": "claude", "name": "a", "state": "done",
                               "checkpoint": ["ref": "refs/hesper/checkpoints/a", "commit": "abc123", "at": "2026-10-08T10:00:00Z", "changed": 3, "branch": "users-col"]]
        let a = try json.decode(Agent.self)
        #expect(a.checkpoint?.changed == 3 && a.checkpoint?.branch == "users-col" && a.checkpoint?.at != nil)
        let round = try JSONDecoder().decode(Agent.self, from: JSONEncoder().encode(a))
        #expect(round.checkpoint == a.checkpoint)
        let none = try (["id": "L/b", "state": "done", "checkpoint": .null] as JSONValue).decode(Agent.self)
        #expect(none.checkpoint == nil)

        let s = Session(json: ["id": "L:claude:s", "checkpoint": ["ref": "refs/hesper/checkpoints/a", "commit": "abc", "changed": 1]])
        #expect(s?.checkpoint?.meta == "checkpoint · 1 file")
        #expect(Session(json: s!.json)?.checkpoint == s?.checkpoint)
        #expect(Session(json: ["id": "L:claude:t"])?.checkpoint == nil)
        #expect(Checkpoint(ref: "r", changed: 3).meta == "checkpoint · 3 files")
        #expect(Checkpoint(ref: "r").meta == "checkpoint")
    }
}

@Suite(.serialized) struct MoveRPCWire {
    @Test func moveSendsOptionsAndEventsArrive() async throws {
        let server = try ScriptedServer()
        defer { server.stop() }
        server.handler = { req, s, fd in
            switch req["method"]?.stringValue {
            case "hello": s.reply(fd, req, result: ["daemon": "t", "version": "1", "machine": "L", "machines": [["short": "L", "name": "l", "online": true]]])
            case "agents.subscribe": s.reply(fd, req, result: [:])
            case "agents.list": s.reply(fd, req, result: [])
            case "agents.move":
                s.send(fd, ["jsonrpc": "2.0", "method": "agents.moving", "params": ["id": "L/a", "step": "transfer", "to": "M", "percent": 30]])
                s.send(fd, ["jsonrpc": "2.0", "method": "agents.removed", "params": ["id": "L/a", "reason": "moved", "data": ["to": "M/b"]]])
                s.reply(fd, req, result: ["agent": "M/b"])
            case "agents.checkpoint":
                s.reply(fd, req, result: ["checkpoint": ["ref": "refs/hesper/checkpoints/a", "commit": "c", "changed": 2]])
            default: break
            }
        }
        let client = DaemonClient(socketPath: server.path)
        client.start()
        var it = client.events.makeAsyncIterator()
        while let e = await it.next() { if case .reconciled = e { break } }
        let id = try await client.move("L/a", to: "M", options: MoveOptions(interrupt: true))
        #expect(id == "M/b")
        #expect(server.received.last { $0["method"] == "agents.move" }?["params"] == ["id": "L/a", "to": "M", "interrupt": true])
        var got: [DaemonEvent] = []
        while got.count < 3, let e = await it.next() {
            switch e {
            case .moving, .moved, .removed: got.append(e)
            default: break
            }
        }
        #expect(got == [.moving(MoveProgress(id: "L/a", to: "M", step: .transfer, percent: 30)), .moved("L/a", to: "M/b"), .removed("L/a", reason: "moved")])
        let cp = try await client.checkpoint("L/a")
        #expect(cp?.changed == 2)
        client.stop()
    }
}
