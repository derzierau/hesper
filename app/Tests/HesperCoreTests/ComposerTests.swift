import Foundation
import Testing
@testable import HesperCore

private let ctx = ComposerContext(
    machines: [Machine(short: "L", name: "laptop", online: true, rttMs: 0, route: "local"),
               Machine(short: "M", name: "mini", online: true, rttMs: 44, route: "relay"),
               Machine(short: "S", name: "studio", online: false)],
    localMachine: "L",
    projects: [.init(path: "/u/projects/hesper", recent: true), .init(path: "/u/projects/acme-apps", recent: true),
               .init(path: "/u/projects/acme-web", recent: false)],
    profiles: ["claude-auto-rc": "claude", "claude-bypass": "claude", "codex-full": "codex", "shell": "shell"],
    kindDefaults: ["claude": "claude-auto-rc", "codex": "codex-full", "shell": "shell"],
    projectProfiles: ["/u/projects/acme-apps": "codex-full"])

@Suite struct ComposerTokenParsing {
    @Test func findsTokensAtWordStartsOnly() {
        let t = ComposerParser.tokens(in: "Fix it in #hesper @mini /codex ~fix/badge, see src/x.go and ~/projects and a@b.c")
        #expect(t.map(\.kind) == [.project, .machine, .profile, .branch])
        #expect(t.map(\.query) == ["hesper", "mini", "codex", "fix/badge"])
    }

    @Test func trailingPunctuationEndsTheToken() {
        let t = ComposerParser.tokens(in: "Run it on @mini.")
        #expect(t.first?.query == "mini")
    }

    @Test func gitURLsStayWholeProjectTokens() {
        let t = ComposerParser.tokens(in: "#git@github.com:org/repo.git please")
        #expect(t.count == 1 && t[0].query == "git@github.com:org/repo.git")
        #expect(ComposerCompletion.isGitURL("https://github.com/org/repo"))
        #expect(!ComposerCompletion.isGitURL("hesper"))
        #expect(ComposerCompletion.repoName("https://github.com/org/repo.git") == "repo")
    }

    @Test func tokenAtCaretIncludesAJustTypedSigil() {
        let text = "Deploy @"
        #expect(ComposerParser.token(at: 8, in: text)?.kind == .machine)
        #expect(ComposerParser.token(at: 3, in: text) == nil)
    }

    @Test func onlyKnownValuesResolve() {
        let r = ComposerParser.resolve("#hesper @mini @nope /codex /unknown #123 ~feature/x", context: ctx)
        #expect(r.map(\.value) == ["/u/projects/hesper", "M", "codex-full", "feature/x"])
    }

    @Test func resolutionTokensOverChoicesOverDefaults() {
        let plain = ComposerResolution.make(text: "Fix the flaky badge test", draft: DraftChoices(project: "/u/projects/acme-apps"), context: ctx)
        #expect(plain.machine == "L" && plain.profile == "codex-full" && !plain.worktree && plain.branch == "fix/flaky-badge-test")
        let r = ComposerResolution.make(text: "Fix the race in #hesper\nand prove it @mini /claude-bypass ~fix/race",
                                        draft: DraftChoices(machine: "L", project: "/u/projects/acme-apps", profile: "shell"), context: ctx)
        #expect(r.machine == "M" && r.project == "/u/projects/hesper" && r.profile == "claude-bypass")
        #expect(r.worktree && r.branch == "fix/race")
        #expect(r.task == "Fix the race in hesper\nand prove it")
        #expect(r.name == "Fix the race in hesper")
    }

    @Test func kindAliasesPickTheKindsDefault() {
        #expect(ctx.profile("codex") == "codex-full")
        #expect(ctx.profile("claude") == "claude-auto-rc")
    }

    @Test func branchSuggestions() {
        #expect(ComposerResolution.suggestBranch("Fix the flaky badge test") == "fix/flaky-badge-test")
        #expect(ComposerResolution.suggestBranch("Add push provider for FCM") == "feature/push-provider-fcm")
        #expect(ComposerResolution.suggestBranch("Refactor the vt screen copy") == "refactor/vt-screen-copy")
        #expect(ComposerResolution.suggestBranch("") == "")
    }
}

@Suite struct ComposerCompletions {
    private func items(_ text: String) -> [CompletionItem] {
        let t = ComposerParser.token(at: (text as NSString).length, in: text)!
        return ComposerCompletion.items(for: t, context: ctx, firstLine: "Fix the flaky badge test")
    }

    @Test func machinesOnlineFirstWithRoundTrip() {
        let m = items("@")
        #expect(m.map(\.mark) == ["L", "M", "S"])
        #expect(m[1].detail == "44 ms · relay" && m[2].detail == "offline" && !m[2].enabled)
        #expect(items("@mi").first?.action == .insert("@M"))
    }

    @Test func projectsRecentFirstThenFolders() {
        let p = items("#acme")
        #expect(p.map(\.title) == ["acme-apps", "acme-web"])
        #expect(p[0].detail.hasSuffix("recent"))
    }

    @Test func pastedGitURLOffersCloneAndStart() {
        let p = items("#https://github.com/org/widgets.git")
        #expect(p.count == 1 && p[0].action == .clone("https://github.com/org/widgets.git") && p[0].title == "Clone widgets and start")
    }

    @Test func profilesMatchByKind() {
        #expect(items("/codex").map(\.title) == ["codex-full"])
    }

    @Test func branchOffersTypedAndSuggested() {
        #expect(items("~").map(\.title) == ["fix/flaky-badge-test"])
        #expect(items("~wip").first?.action == .insert("~wip"))
    }

    @Test func applyReplacesTheTokenAndAddsASpace() {
        let text = "Run on @mi now"
        let t = ComposerParser.token(at: 10, in: text)!
        let r = ComposerCompletion.apply("@M", to: t, in: text)
        #expect(r.text == "Run on @M now" && r.caret == 10)
        let end = ComposerCompletion.apply("@M", to: ComposerParser.token(at: 3, in: "@mi")!, in: "@mi")
        #expect(end.text == "@M " && end.caret == 3)
    }
}

@Suite struct DraftPersistence {
    @Test func draftRoundTripsThroughJSON() throws {
        let d = Draft(id: "d-abc", text: "hi", machine: "M", project: "/p", profile: "codex-full", worktree: true, branch: "fix/x",
                      attachments: ["/a.png"], after: "L/a7f3k2", parked: true, created: Date(timeIntervalSince1970: 1_700_000_000))
        let back = try JSONDecoder().decode(Draft.self, from: JSONEncoder().encode(d))
        #expect(back == d)
        let wire = try JSONDecoder().decode(Draft.self, from: Data(#"{"id":"d-x","text":"t","machine":"","created":"2026-10-06T19:02:11Z"}"#.utf8))
        #expect(wire.machine == nil && !wire.parked && wire.attachments.isEmpty && wire.created != nil)
    }

    @Test func savesAreCoalescedWhileTyping() {
        var b = DraftBook()
        let t0 = Date(timeIntervalSince1970: 0)
        var d = Draft(id: "d-1", created: t0)
        for i in 0..<10 {
            d.text += "x"
            b.edit(d, now: t0.addingTimeInterval(Double(i) * 0.1))
        }
        let early = b.due(now: t0.addingTimeInterval(1.0))
        #expect(early.isEmpty) // still typing, < 2 s
        let quiet = b.due(now: t0.addingTimeInterval(1.25))
        #expect(quiet.map(\.text) == ["xxxxxxxxxx"]) // 300 ms quiet
        #expect(b.inFlight["d-1"] != nil)
        // Continuous typing still saves every 2 s.
        var c = DraftBook()
        for i in 0..<30 { d.text += "y"; c.edit(d, now: t0.addingTimeInterval(Double(i) * 0.1)) }
        let max = c.due(now: t0.addingTimeInterval(2.0))
        #expect(max.count == 1)
    }

    @Test func localEditsWinOverOlderEchoes() {
        var b = DraftBook()
        var d = Draft(id: "d-1", text: "new")
        b.edit(d)
        var old = d
        old.text = "old"
        let echoed = b.applyRemote(old)
        #expect(!echoed)
        #expect(b["d-1"]?.text == "new")
        _ = b.flushAll()
        b.saved(d)
        d.text = "from another app"
        let other = b.applyRemote(d)
        #expect(other)
        #expect(b["d-1"]?.text == "from another app")
    }

    @Test func removedDraftsIgnoreLateEchoesAndRestore() {
        var b = DraftBook()
        let d = Draft(id: "d-1", text: "x")
        b.edit(d)
        _ = b.remove("d-1")
        let late = b.applyRemote(d)
        #expect(!late && b["d-1"] == nil)
        b.restore(d)
        #expect(b["d-1"]?.text == "x" && b.dirty["d-1"] != nil)
    }

    @Test func reconcileDropsDraftsTheDaemonForgot() {
        var b = DraftBook()
        b.applyRemote(Draft(id: "d-1", text: "a"))
        b.applyRemote(Draft(id: "d-2", text: "b"))
        b.edit(Draft(id: "d-3", text: "unsaved"))
        let changed = b.reconcile([Draft(id: "d-2", text: "b")])
        #expect(changed)
        #expect(Set(b.drafts.keys) == ["d-2", "d-3"])
    }

    @Test func wallOrderPutsDraftsRightOfTheirTile() {
        let base = ["L/a", "L/b", "L/c"]
        #expect(WallOrder.arrange(base: base, anchored: [("d-1", "L/a")]) == ["L/a", "d-1", "L/b", "L/c"])
        // ⌥↩ chains: a draft right of a draft; the front; the end.
        #expect(WallOrder.arrange(base: base, anchored: [("d-2", "d-1"), ("d-1", "L/b"), ("d-0", "^"), ("d-9", nil)])
                == ["d-0", "L/a", "L/b", "d-1", "d-2", "L/c", "d-9"])
        // A missing anchor: the end.
        #expect(WallOrder.arrange(base: base, anchored: [("d-1", "L/gone")]) == ["L/a", "L/b", "L/c", "d-1"])
    }
}

@Suite struct UndoStackBehaviour {
    @Test func newestFirstWithinTheWindow() {
        var u = UndoStack<String>(window: 6)
        let t0 = Date(timeIntervalSince1970: 0)
        u.push("stop a", label: "Stopped a", now: t0)
        u.push("stop b", label: "Stopped b", now: t0.addingTimeInterval(1))
        #expect(u.top(now: t0.addingTimeInterval(2))?.label == "Stopped b")
        let b = u.pop(now: t0.addingTimeInterval(2))
        #expect(b?.action == "stop b")
        let a = u.pop(now: t0.addingTimeInterval(6.5))
        #expect(a == nil) // a expired
    }

    @Test func expiredEntriesAreHandedBackOnce() {
        var u = UndoStack<String>(window: 6)
        let t0 = Date(timeIntervalSince1970: 0)
        let e = u.push("remove", label: "Removed", now: t0)
        #expect(u.nextExpiry == t0.addingTimeInterval(6))
        let x5 = u.expire(now: t0.addingTimeInterval(5))
        #expect(x5.isEmpty)
        let x6 = u.expire(now: t0.addingTimeInterval(6))
        #expect(x6.map(\.id) == [e.id])
        let x7 = u.expire(now: t0.addingTimeInterval(7))
        #expect(x7.isEmpty && u.entries.isEmpty)
    }

    @Test func takeByIDAndDrain() {
        var u = UndoStack<Int>()
        let a = u.push(1, label: "a")
        u.push(2, label: "b")
        let took = u.take(a.id)
        #expect(took?.action == 1)
        let rest = u.drain()
        #expect(rest.map(\.action) == [2] && u.entries.isEmpty)
    }
}

@Suite struct OverlayKeyRouting {
    @Test func sameKeysEverywhere() {
        #expect(OverlayKeys.route(KeyChord(.up)) == .up)
        #expect(OverlayKeys.route(KeyChord(.down)) == .down)
        #expect(OverlayKeys.route(KeyChord(.enter)) == .activate)
        #expect(OverlayKeys.route(KeyChord(.enter, command: true)) == .alternate)
        #expect(OverlayKeys.route(KeyChord(.tab)) == .actOn)
        #expect(OverlayKeys.route(KeyChord(.tab, shift: true)) == .back)
        #expect(OverlayKeys.route(KeyChord(.escape)) == .close)
        #expect(OverlayKeys.route(KeyChord(.delete)) == .deleteBackward)
        #expect(OverlayKeys.route(KeyChord(.char("m")), characters: "m") == .type("m"))
        #expect(OverlayKeys.route(KeyChord(.char("k"), command: true), characters: "k") == .pass)
        #expect(OverlayKeys.route(KeyChord(.char("n"), control: true)) == .down)
    }

    @Test func listSkipsDisabledRowsAndWraps() {
        var l = OverlayList()
        let enabled = [true, false, true]
        #expect(l.selection(enabled: enabled) == 0)
        l.move(1, enabled: enabled)
        #expect(l.index == 2)
        l.move(1, enabled: enabled)
        #expect(l.index == 0)
        l.move(-1, enabled: enabled)
        #expect(l.index == 2)
        l.type("ab")
        #expect(l.query == "ab" && l.index == 0)
        l.deleteBackward()
        #expect(l.query == "a")
        #expect(OverlayList(index: 1).selection(enabled: [false, false]) == nil)
    }

    @Test func composerKeysAreTheComposers() {
        #expect(KeyRouter.route(KeyChord(.enter, command: true), mode: .compose, selectedState: nil) == .startDraft)
        #expect(KeyRouter.route(KeyChord(.enter, option: true), mode: .compose, selectedState: nil) == .startDraftAndNew)
        #expect(KeyRouter.route(KeyChord(.char("w"), command: true, shift: true), mode: .compose, selectedState: nil) == .toggleWorktree)
        #expect(KeyRouter.route(KeyChord(.escape), mode: .compose, selectedState: nil) == .leaveDraft)
        #expect(KeyRouter.route(KeyChord(.char("a"), command: true), mode: .compose, selectedState: nil) == .passThrough)
        #expect(KeyRouter.route(KeyChord(.char("z"), command: true), mode: .wall, selectedState: nil) == .undo)
        #expect(KeyRouter.route(KeyChord(.enter), mode: .wall, selectedState: nil, selectedIsDraft: true) == .editDraft)
        #expect(KeyRouter.route(KeyChord(.enter), mode: .wall, selectedState: .question, selectedTrust: true) == .answer(.trust))
        #expect(KeyRouter.route(KeyChord(.enter), mode: .wall, selectedState: .question, selectedAnswers: [.skip, .update]) == .answer(.skip))
        #expect(Attention(kind: "question", title: "Update", options: ["skip", "update"]).answers == [.skip, .update])
        #expect(Attention(kind: "approval", options: ["allow", "always", "deny"]).answers.isEmpty)
    }

    @Test func activeTileKeysGoToTheAgentExceptAppShortcuts() {
        let agent: [KeyChord] = [KeyChord(.escape), KeyChord(.enter), KeyChord(.up), KeyChord(.char("c"), control: true),
                                 KeyChord(.char("c"), command: true), KeyChord(.char("v"), command: true), KeyChord(.char("a"))]
        for k in agent { #expect(KeyRouter.routeActiveTile(k) == .passThrough) }
        #expect(KeyRouter.routeActiveTile(KeyChord(.char("n"), command: true)) == .newAgent)
        #expect(KeyRouter.routeActiveTile(KeyChord(.char("k"), command: true)) == .palette)
        #expect(KeyRouter.routeActiveTile(KeyChord(.char("j"), command: true)) == .nextAttention)
        #expect(KeyRouter.routeActiveTile(KeyChord(.char("w"), command: true)) == .closeAgent)
        #expect(KeyRouter.routeActiveTile(KeyChord(.enter, command: true)) == .toggleFocus)
        #expect(KeyRouter.routeActiveTile(KeyChord(.escape, command: true)) == .exitFocus) // leaves the tile
        #expect(KeyRouter.routeActiveTile(KeyChord(.char("1"), command: true, option: true)) == .arrangement(0))
        // On the wall, ⏎ on a selected tile makes it active; on an approval it allows.
        #expect(KeyRouter.route(KeyChord(.enter), mode: .wall, selectedState: .working) == .activateTile)
        #expect(KeyRouter.route(KeyChord(.enter), mode: .wall, selectedState: .approval) == .answer(.allow))
    }

    @Test func popoversAvoidCardsThatNeedYou() {
        let container = Rect(x: 0, y: 0, width: 1000, height: 800)
        let anchor = Rect(x: 100, y: 100, width: 100, height: 30)
        let below = PopoverPlacement.place(anchor: anchor, width: 200, height: 200, container: container)
        #expect(below.below && below.frame.y > anchor.maxY && below.arrowX > 0)
        // A ringed card below: no room above, so shift right while the arrow still reaches.
        let ring = Rect(x: 0, y: 140, width: 120, height: 400)
        let p = PopoverPlacement.place(anchor: anchor, width: 200, height: 200, container: container, avoid: [ring])
        #expect(!PopoverPlacement.intersects(p.frame, ring) || p.frame.maxY <= ring.y)
        // The anchor's own card is never in the way.
        let own = PopoverPlacement.place(anchor: anchor, width: 200, height: 200, container: container, avoid: [Rect(x: 90, y: 90, width: 300, height: 300)])
        #expect(own.below)
    }
}
