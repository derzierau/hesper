import Foundation
import Testing
@testable import HesperCore

struct ClosingTests {
    func agent(_ id: String, _ state: AgentState, kind: String = "claude", since: Date? = nil, created: Date? = nil, project: String? = nil) -> Agent {
        var a = Agent(id: id, kind: kind, state: state, stateSince: since, created: created, projectId: project)
        if state == .exited { a.exit = ExitInfo(code: 0) }
        return a
    }

    // MARK: Keys

    @Test func closingKeys() {
        for mode in [AppMode.wall, .focus] {
            #expect(KeyRouter.route(KeyChord(.char("w"), command: true), mode: mode, selectedState: .working) == .closeAgent)
            #expect(KeyRouter.route(KeyChord(.char("w"), command: true, option: true), mode: mode, selectedState: .working) == .backgroundAgent)
            #expect(KeyRouter.route(KeyChord(.char("w"), command: true, control: true), mode: mode, selectedState: .working) == .killAgent)
            #expect(KeyRouter.route(KeyChord(.char("W"), command: true, shift: true), mode: mode, selectedState: nil) == .tidyUp)
            #expect(KeyRouter.route(KeyChord(.char("T"), command: true, shift: true), mode: mode, selectedState: nil) == .reopenClosed)
            #expect(KeyRouter.route(KeyChord(.char("t"), command: true, shift: true), mode: mode, selectedState: nil) == .reopenClosed)
        }
        #expect(KeyRouter.routeActiveTile(KeyChord(.char("w"), command: true, control: true)) == .killAgent)
        #expect(KeyRouter.routeActiveTile(KeyChord(.char("w"), command: true, option: true)) == .backgroundAgent)
        // Composing: ⌘W discards the draft, ⇧⌘W stays the worktree toggle.
        #expect(KeyRouter.route(KeyChord(.char("w"), command: true), mode: .compose, selectedState: nil) == .closeAgent)
        #expect(KeyRouter.route(KeyChord(.char("W"), command: true, shift: true), mode: .compose, selectedState: nil) == .toggleWorktree)
        // Plain ⌃W in focus is the terminal's.
        #expect(KeyRouter.route(KeyChord(.char("w"), control: true), mode: .focus, selectedState: .working) == .passThrough)
    }

    // MARK: The close decision

    @Test func draftsAreDiscarded() {
        #expect(CloseRules.decide(isDraft: true, agent: nil) == .discardDraft)
        #expect(CloseRules.decide(isDraft: false, agent: nil) == .nothing)
    }

    @Test func workingClosesWithoutAsking() {
        #expect(CloseRules.decide(isDraft: false, agent: agent("a", .working)) == .close)
        #expect(CloseRules.decide(isDraft: false, agent: agent("a", .done)) == .close)
        #expect(CloseRules.decide(isDraft: false, agent: agent("a", .error)) == .close)
    }

    @Test func needsYouConfirmsOnce() {
        for s in [AgentState.approval, .question] {
            #expect(CloseRules.decide(isDraft: false, agent: agent("a", s)) == .confirm(.needsYou))
            #expect(CloseRules.decide(isDraft: false, agent: agent("a", s), acks: .needsYou) == .close)
        }
        #expect(CloseConfirm.needsYou.message == "It's waiting for you")
        #expect(CloseConfirm.needsYou.primary == "Close anyway" && CloseConfirm.needsYou.secondary == nil)
    }

    @Test func worktreeIsCheckedThenConfirmed() {
        let a = agent("a", .done)
        #expect(CloseRules.decide(isDraft: false, agent: a, worktreeCheckable: true) == .checkWorktree)
        #expect(CloseRules.decide(isDraft: false, agent: a, worktreeCheckable: true, worktreeChanges: 0) == .close)
        #expect(CloseRules.decide(isDraft: false, agent: a, worktreeCheckable: true, worktreeChanges: 3) == .confirm(.worktree(files: 3)))
        #expect(CloseRules.decide(isDraft: false, agent: a, acks: .worktree, worktreeCheckable: true, worktreeChanges: 3) == .close)
        #expect(CloseConfirm.worktree(files: 3).message == "3 files changed in its worktree")
        #expect(CloseConfirm.worktree(files: 1).message == "1 file changed in its worktree")
        #expect(CloseConfirm.worktree(files: 2).primary == "Keep worktree" && CloseConfirm.worktree(files: 2).secondary == "Discard")
        // Needs you first, then the worktree.
        let q = agent("q", .approval)
        #expect(CloseRules.decide(isDraft: false, agent: q, worktreeCheckable: true, worktreeChanges: 2) == .confirm(.needsYou))
        #expect(CloseRules.decide(isDraft: false, agent: q, acks: .needsYou, worktreeCheckable: true, worktreeChanges: 2) == .confirm(.worktree(files: 2)))
    }

    @Test func shellWithForegroundCommandAsks() {
        var s = agent("s", .working, kind: "shell")
        s.activity = "npm run dev"
        #expect(CloseRules.decide(isDraft: false, agent: s) == .confirm(.foreground("npm run dev")))
        #expect(CloseConfirm.foreground("npm run dev").message == "npm run dev is still running")
        #expect(CloseRules.decide(isDraft: false, agent: s, acks: .foreground) == .close)
        s.state = .idle
        #expect(CloseRules.decide(isDraft: false, agent: s) == .close)
        var c = agent("c", .working)
        c.activity = "Bash: npm test"
        #expect(CloseRules.decide(isDraft: false, agent: c) == .close) // only shells
    }

    // MARK: Tidy up, auto-tidy

    @Test func tidyUpTakesOnlyFinished() {
        let all = [agent("w", .working, project: "p"), agent("d", .done, project: "p"), agent("i", .idle, project: "q"),
                   agent("x", .exited, project: "p"), agent("ap", .approval, project: "p"), agent("qu", .question, project: "p"),
                   agent("e", .error, project: "p"), agent("s", .starting, project: "p")]
        #expect(TidyUp.select(all, band: nil, bandOf: { $0.projectId }).map(\.id) == ["d", "i", "x"])
        #expect(TidyUp.select(all, band: "p", bandOf: { $0.projectId }).map(\.id) == ["d", "x"])
        #expect(TidyUp.select(all, band: "zzz", bandOf: { $0.projectId }).isEmpty)
    }

    @Test func autoTidyAfterThirtyMinutes() {
        let now = Date(timeIntervalSince1970: 100_000)
        var killed = agent("k", .exited, since: now.addingTimeInterval(-3600))
        killed.ended = "killed"
        let all = [agent("old", .done, since: now.addingTimeInterval(-31 * 60)),
                   agent("new", .idle, since: now.addingTimeInterval(-10 * 60)),
                   agent("w", .working, since: now.addingTimeInterval(-3600)),
                   agent("ap", .approval, since: now.addingTimeInterval(-3600)),
                   killed]
        #expect(AutoTidy.due(all, now: now).map(\.id) == ["old"])
        #expect(AutoTidy.due(all, now: now, exclude: ["old"]).isEmpty)
        #expect(AutoTidy.nextDue(all, now: now) == now) // "old" is due now
        #expect(AutoTidy.nextDue(all, now: now, exclude: ["old"]) == now.addingTimeInterval(20 * 60))
        #expect(AutoTidy.nextDue([all[2]], now: now) == nil)
    }

    // MARK: Reopen stack

    @Test func reopenStackIsLIFODedupedAndCapped() {
        var s = ReopenStack()
        #expect(s.pop() == nil)
        for i in 0..<12 { s.push(ClosedAgent(agentID: "L/\(i)", name: "a\(i)", session: "L:claude:s\(i)", machine: "L")) }
        #expect(s.items.count == ReopenStack.capacity)
        #expect(s.items.first?.agentID == "L/2")
        // The same session again moves to the top, once.
        s.push(ClosedAgent(agentID: "L/5b", name: "a5", session: "L:claude:s5", machine: "L"))
        #expect(s.items.count == ReopenStack.capacity)
        #expect(s.items.filter { $0.session == "L:claude:s5" }.count == 1)
        #expect(s.pop()?.agentID == "L/5b")
        #expect(s.pop()?.agentID == "L/11")
        s.remove(session: "L:claude:s10")
        #expect(s.top?.agentID == "L/9")
        s.remove(agentID: "L/9")
        #expect(s.top?.agentID == "L/8")
    }

    @Test func sessionIDsFollowHesperd() {
        var a = Agent(id: "M/abc", kind: "codex", name: "api")
        #expect(ClosedAgent.sessionID(for: a) == nil)
        a.sessionId = "019a"
        #expect(ClosedAgent.sessionID(for: a) == "M:codex:019a")
        #expect(ClosedAgent(a, band: "p").session == "M:codex:019a")
        #expect(ClosedAgent(a, session: "given").session == "given")
    }

    // MARK: Background

    @Test func backgroundCountsAndOrder() {
        let t = Date(timeIntervalSince1970: 1000)
        let all = [agent("a", .working, created: t.addingTimeInterval(20)), agent("b", .approval, since: t, created: t),
                   agent("c", .working, created: t.addingTimeInterval(10)), agent("d", .done)]
        let bg: Set<String> = ["a", "b", "c"]
        let s = BackgroundTray.summary(all, isBackground: { bg.contains($0.id) })
        #expect(s == BackgroundSummary(count: 3, needsYou: 1))
        #expect(s.visible && s.title == "Background · 3")
        #expect(!BackgroundSummary().visible)
        #expect(BackgroundTray.agents(all, isBackground: { bg.contains($0.id) }).map(\.id) == ["b", "c", "a"])
    }

    @Test func backgroundMetaAndFinish() {
        let now = Date(timeIntervalSince1970: 10_000)
        let a = agent("a", .working, kind: "codex", created: now.addingTimeInterval(-12 * 60))
        #expect(BackgroundTray.meta(a, machine: "laptop", now: now) == "laptop · codex · 12m")
        #expect(BackgroundTray.age(since: now.addingTimeInterval(-5), now: now) == "5s")
        #expect(BackgroundTray.age(since: now.addingTimeInterval(-7300), now: now) == "2h")
        #expect(BackgroundTray.justFinished(agent("a", .done), from: .working))
        #expect(BackgroundTray.justFinished(agent("a", .exited), from: .approval))
        #expect(!BackgroundTray.justFinished(agent("a", .done), from: .done))
        #expect(!BackgroundTray.justFinished(agent("a", .working), from: .starting))
        #expect(!BackgroundTray.justFinished(agent("a", .done), from: nil))
    }

    @Test func agentDecodesBackgroundAndEnded() throws {
        let json = #"{"id":"L/1","state":"exited","background":true,"ended":"killed"}"#
        let a = try JSONDecoder().decode(Agent.self, from: Data(json.utf8))
        #expect(a.background && a.isKilled)
        let b = try JSONDecoder().decode(Agent.self, from: Data(#"{"id":"L/2","state":"working","ended":""}"#.utf8))
        #expect(!b.background && !b.isKilled && b.ended == nil)
        let round = try JSONDecoder().decode(Agent.self, from: try JSONEncoder().encode(a))
        #expect(round.background && round.ended == "killed")
    }

    @Test func menuBarHasABackgroundSection() {
        let all = [agent("w", .working), agent("bw", .working), agent("ba", .approval), agent("d", .done)]
        let g = StatusMenuGroups.groups(all, isBackground: { $0.id.hasPrefix("b") })
        #expect(g.map(\.group) == [.needsYou, .working, .background, .recent])
        #expect(g[0].agents.map(\.id) == ["ba"]) // needs you stays there
        #expect(g[1].agents.map(\.id) == ["w"])
        #expect(g[2].agents.map(\.id) == ["bw"])
        #expect(StatusMenuGroups.Group.background.title == "Background")
    }

    // MARK: Text

    @Test func removalReasonsAndToasts() {
        #expect(CloseText.notification(RemovalReason(rawValue: "finished-in-background"), name: "api") == "api finished in the background")
        #expect(CloseText.notification(.closed, name: "api") == nil)
        #expect(CloseText.notification(nil, name: "api") == nil)
        #expect(RemovalReason(rawValue: "removed") == .removed)
        #expect(CloseText.closed("api") == "Closed api")
        #expect(CloseText.closed("api", queuedFor: "mini") == "Closed api · will stop when mini is back")
        #expect(CloseText.tidied(["a"]) == "Closed a")
        #expect(CloseText.tidied(["a", "b", "c"]) == "Closed 3 finished agents")
    }
}

@Suite struct CloseFallbackOnOlderMacs {
    @Test func anOlderMacsRefusalFallsBack() {
        #expect(CloseRPC.missing(RPCError(code: -32601, message: "no method agents.close", kind: .notFound)))
        #expect(CloseRPC.missing(RPCError(code: -32003, message: "Unknown method", kind: .remote)))
        #expect(!CloseRPC.missing(RPCError(code: -32010, message: "machine offline: mini", kind: .remote)))
    }
}
