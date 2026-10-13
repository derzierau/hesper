import Foundation
import Testing
@testable import HesperCore

// The menus the registry generates: the menu bar's tree in the spec's
// order, the Dock and status-bar menus, the dynamic titles, and the
// validation outcomes the menus show for the states that matter.

@Suite struct MenuModelTests {
    /// The command ids of `items`, in order (separators left out).
    private func ids(_ items: [MenuModel.Item]) -> [CommandID] {
        items.compactMap { if case .command(let id, _, _) = $0 { return id }; return nil }
    }

    // MARK: Dynamic titles

    @Test func undoTitleNamesTheAction() {
        #expect(MenuModel.undoTitle(nil) == "Undo")
        #expect(MenuModel.undoTitle(.closeAgent) == "Undo Close Agent")
        #expect(MenuModel.undoTitle(.tidyUp) == "Undo Tidy Up")
        #expect(MenuModel.undoTitle(.move) == "Undo Move")
        #expect(MenuModel.undoTitle(.discardDraft) == "Undo Discard Draft")
        #expect(MenuModel.undoTitle(.deleteSession) == "Undo Delete Session")
    }

    @Test func fullScreenTitleToggles() {
        #expect(MenuModel.fullScreenTitle(inFullScreen: false) == "Enter Full Screen")
        #expect(MenuModel.fullScreenTitle(inFullScreen: true) == "Exit Full Screen")
    }

    // MARK: Dock

    @Test func dockMenuListsTheWaysIn() {
        let items = MenuModel.dockItems()
        #expect(items == [.command(.newAgent, title: "New Agent", chord: KeyChord(.char("n"), command: true)),
                          .command(.nextAttention, title: "Needs You…", chord: KeyChord(.char("j"), command: true)),
                          .command(.history, title: "History", chord: KeyChord(.char("y"), command: true)),
                          .command(.review, title: "Review", chord: KeyChord(.char("r"), command: true))])
        // History leaves when hesperd has no sessions.*, as in the menu bar.
        #expect(ids(MenuModel.dockItems(context: CommandContext(historySupported: false))) == [.newAgent, .nextAttention, .review])

    }

    // MARK: Status bar

    /// The fixed section keeps its labels; a key equivalent only where the
    /// chord is app-wide, Quick Launch's hotkey in its label.
    @Test func statusMenuKeepsItsLabelsAndAppWideChords() {
        let items = MenuModel.statusItems(hotKey: "⌃⌥Space")
        #expect(items == [.command(.showWall, title: "Open Hesper", chord: KeyChord(.char("0"), command: true, shift: true)),
                          .command(.newAgent, title: "New agent…", chord: KeyChord(.char("n"), command: true)),
                          .command(.history, title: "History…", chord: KeyChord(.char("y"), command: true)),
                          .command(.review, title: "Review…", chord: KeyChord(.char("r"), command: true)),
                          .separator,
                          .command(.quickLaunch, title: "Quick Launch…  ⌃⌥Space", chord: nil),
                          .command(.settings, title: "Settings…", chord: KeyChord(.char(","), command: true)),
                          .separator,
                          .command(.quit, title: "Quit Hesper", chord: KeyChord(.char("q"), command: true))])
        #expect(ids(MenuModel.statusItems(hotKey: "⌃⌥Space", context: CommandContext(historySupported: false)))
                == [.showWall, .newAgent, .review, .quickLaunch, .settings, .quit])
    }

    // MARK: Validation



    private func availability(_ id: CommandID, _ c: CommandContext) -> Availability { CommandRegistry.command(id)!.availability(c) }
    private func isDisabled(_ id: CommandID, _ c: CommandContext) -> Bool { if case .disabled = availability(id, c) { return true }; return false }

    static let working = CommandContext(selectedState: .working, selectedRunning: true)
    static let approval = CommandContext(selectedState: .approval, selectedRunning: true)
    static let exited = CommandContext(selectedState: .exited, selectedRunning: false)
    static let nothing = CommandContext()

    /// Allow / Always Allow / Deny… only while the selected agent waits on
    /// an approval.
    @Test func approvalItemsFollowTheSelectedState() {
        for id in [CommandID.answer(.allow), .answer(.always), .denyWithMessage] {
            #expect(availability(id, Self.approval) == .enabled)
            #expect(isDisabled(id, Self.working))
            #expect(isDisabled(id, Self.exited))
            #expect(isDisabled(id, Self.nothing))
            #expect(isDisabled(id, CommandContext(selectedState: .approval, keyWindow: .other)), "no wall in front")
        }
    }

    /// Close, Kill, Background, Move, Rename and Resume per state.
    @Test func lifecycleItemsFollowTheSelectedState() {
        for id in [CommandID.closeAgent, .killAgent, .backgroundAgent, .moveAgent, .renameAgent] {
            #expect(availability(id, Self.working) == .enabled, "\(id)")
            #expect(isDisabled(id, Self.nothing), "\(id) with nothing selected")
        }
        // An ended agent: Resume, not Kill.
        #expect(isDisabled(.killAgent, Self.exited))
        #expect(availability(.resumeAgent, Self.exited) == .enabled)
        #expect(isDisabled(.resumeAgent, Self.working))
        #expect(isDisabled(.resumeAgent, Self.nothing))
        #expect(availability(.closeAgent, Self.exited) == .enabled)
        #expect(availability(.renameAgent, Self.exited) == .enabled)
        // Settings in front: the lifecycle chords do nothing (⌘W closes the window).
        let settings = CommandContext(selectedState: .working, selectedRunning: true, keyWindow: .other)
        for id in [CommandID.killAgent, .backgroundAgent, .moveAgent, .renameAgent, .resumeAgent] { #expect(isDisabled(id, settings), "\(id)") }
        #expect(availability(.closeAgent, settings) == .enabled)
    }

    /// Open in New Window / Tab need a selected agent on a wall.
    @Test func openInNewWindowNeedsASelectedAgentOnAWall() {
        for id in [CommandID.openInNewWindow, .openInNewTab] {
            #expect(availability(id, Self.working) == .enabled)
            #expect(isDisabled(id, Self.nothing))
            #expect(isDisabled(id, CommandContext(selectedState: .working, keyWindow: .agentWindow)))
            #expect(isDisabled(id, CommandContext(selectedState: .working, keyWindow: .other)))
        }
    }

    /// ⌘W per window kind (decision 1): the agent on a wall, the window
    /// in an agent window and in the plain windows, and Close is enabled
    /// there without a selection.
    @Test func closeFollowsTheKeyWindow() {
        #expect(CommandContext(selectedState: .working, keyWindow: .wall).closeTarget == .agent)
        #expect(CommandContext(keyWindow: .wall, hasDraft: true).closeTarget == .agent)
        #expect(CommandContext(selectedState: .working, keyWindow: .agentWindow).closeTarget == .window)
        #expect(CommandContext(keyWindow: .other).closeTarget == .window)
        #expect(availability(.closeAgent, CommandContext(selectedState: .working, keyWindow: .agentWindow)) == .enabled)
        #expect(availability(.closeAgent, CommandContext(keyWindow: .other)) == .enabled)
        #expect(availability(.closeAgent, CommandContext(keyWindow: .wall, hasDraft: true)) == .enabled)
        #expect(isDisabled(.closeAgent, CommandContext(keyWindow: .wall)))
    }

    @Test func makeHomeWallOnlyOnAnotherWall() {

        #expect(availability(.makeHomeWall, CommandContext(isHomeWall: false)) == .enabled)
        #expect(isDisabled(.makeHomeWall, CommandContext(isHomeWall: true)))
        #expect(isDisabled(.makeHomeWall, CommandContext(keyWindow: .agentWindow)))
        #expect(isDisabled(.makeHomeWall, CommandContext(keyWindow: .other)))
    }
}

