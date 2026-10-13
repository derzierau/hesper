import Foundation
import Testing
@testable import HesperCore

// The menus the registry generates: the menu bar's tree in the spec's
// order, the Dock and status-bar menus, the dynamic titles, and the
// validation outcomes the menus show for the states that matter.

@Suite struct MenuModelTests {
    // MARK: Dynamic titles

    @Test func undoTitleNamesTheAction() {
        #expect(MenuModel.undoTitle(nil) == "Undo")
        #expect(MenuModel.undoTitle(.closeAgent) == "Undo Close Agent")
        #expect(MenuModel.undoTitle(.tidyUp) == "Undo Tidy Up")
        #expect(MenuModel.undoTitle(.move) == "Undo Move")
        #expect(MenuModel.undoTitle(.discardDraft) == "Undo Discard Draft")
        #expect(MenuModel.undoTitle(.deleteSession) == "Undo Delete Session")
    }
}
