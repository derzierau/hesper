import Testing
@testable import HesperCore

struct TileActionsTests {
    func agent(_ state: AgentState, attention: Attention? = nil, exit: ExitInfo? = nil) -> Agent {
        var a = Agent(id: "L/a1", state: state, attention: attention)
        a.exit = exit
        return a
    }

    @Test func approvalShowsAllowAlwaysDenyOpen() {
        let acts = TileActions.actions(agent(.approval))
        #expect(acts.map(\.title) == ["Allow", "Always", "Deny", "Open"])
        #expect(acts.map(\.key) == ["⏎", "A", "N", TileActions.focusKeys])
        #expect(acts.map(\.primary) == [true, false, false, false])
        #expect(acts[0].tone == .signal)
        #expect(acts.map(\.id) == ["approve.allow", "approve.always", "approve.deny", "approve.open"])
        // In focus the agent fills the window: no Open.
        #expect(TileActions.actions(agent(.approval), inFocus: true).map(\.title) == ["Allow", "Always", "Deny"])
    }

    @Test func tightTileFoldsAlwaysAndOpenIntoTheOverflow() {
        let acts = TileActions.actions(agent(.approval))
        let wide = TileActions.split(acts, width: 600)
        #expect(wide.shown.count == 4 && wide.overflow.isEmpty)
        let tight = TileActions.split(acts, width: 380)
        #expect(tight.shown.map(\.title) == ["Allow", "Deny"])
        #expect(tight.overflow.map(\.title) == ["Always", "Open"])
        #expect(TileActions.isTight(width: 419) && !TileActions.isTight(width: 420))
    }

    @Test func questionWithAnswersNumbersThemAndTheFirstIsEnter() {
        let acts = TileActions.actions(agent(.question, attention: Attention(kind: "question", options: ["trust", "exit"])))
        #expect(acts.map(\.title) == ["Trust", "Exit"])
        #expect(acts.map(\.key) == ["⏎", "2"])
        #expect(acts.map(\.primary) == [true, false])
        #expect(acts.map(\.verb) == [.answer(.trust), .answer(.exit)])
        #expect(acts.map(\.id) == ["answer.trust", "answer.exit"])
        // Numbered answers never fold: each one is the way to answer.
        #expect(TileActions.split(acts, width: 300).overflow.isEmpty)
    }

    @Test func questionWithTypedOptionsSendsNumbers() {
        let q = agent(.question, attention: Attention(kind: "question", options: ["Yes", "No", "Later"]))
        let acts = TileActions.actions(q)
        #expect(acts.map(\.verb) == [.option(0), .option(1), .option(2), .open])
        #expect(acts.map(\.key) == ["1", "2", "3", TileActions.focusKeys])
        #expect(acts[0].primary && acts[0].tone == .question)
        #expect(acts.map(\.id) == ["answer.option.1", "answer.option.2", "answer.option.3", "answer.open"])
        // The focus view leaves typed options to the terminal: nothing to click.
        #expect(TileActions.actions(q, inFocus: true, canChoose: false).isEmpty)
    }

    @Test func freeTextQuestionOffersOpenOnly() {
        let acts = TileActions.actions(agent(.question, attention: Attention(kind: "question", detail: "Which file?")))
        #expect(acts.map(\.verb) == [.open])
        #expect(acts[0].primary && acts[0].id == "answer.open")
        #expect(TileActions.actions(agent(.question), inFocus: true).isEmpty)
    }

    @Test func errorResumesWhenItExited() {
        #expect(TileActions.actions(agent(.error)).map(\.verb) == [.open])
        let acts = TileActions.actions(agent(.error, exit: ExitInfo(code: 1)))
        #expect(acts.map(\.verb) == [.resume, .open])
        #expect(acts[0].primary && acts[0].tone == .error && acts[0].id == "tile.resume")
    }

    @Test func endedAgentsResume() {
        let ended = TileActions.actions(agent(.exited))
        #expect(ended.map(\.title) == ["Resume"] && ended[0].primary && ended[0].key == nil)
        let killed = TileActions.actions(agent(.exited), killed: true)
        #expect(killed.map(\.title) == ["Resume", "Close"])
        #expect(killed.map(\.key) == ["⏎", "⌘W"])
        #expect(killed.map(\.id) == ["tile.resume", "tile.close"])
    }

    @Test func workingTilesHaveNoVerbs() {
        for s in [AgentState.working, .starting, .idle, .done, .unknown] {
            #expect(TileActions.actions(agent(s)).isEmpty, "\(s)")
        }
    }
}
