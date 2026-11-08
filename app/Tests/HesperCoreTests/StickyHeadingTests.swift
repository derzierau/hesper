import Foundation
import Testing
@testable import HesperCore

// A band wider than the viewport on a wall that scrolls sideways: its
// heading's title follows the visible left edge while the band is on
// screen, and stops where it would run into the heading's buttons.

@Suite struct StickyHeadingTests {
    let header = Rect(x: 100, y: 20, width: 2000, height: 46)

    @Test func staysAtTheBandStartWhileThatIsVisible() {
        #expect(StickyHeading.offset(header: header, visibleMinX: 0, keep: 300) == 0)
        #expect(StickyHeading.offset(header: header, visibleMinX: 100, keep: 300) == 0)
    }

    @Test func followsTheVisibleLeftEdgeInsideTheBand() {
        #expect(StickyHeading.offset(header: header, visibleMinX: 160, keep: 300) == 60)
        #expect(StickyHeading.offset(header: header, visibleMinX: 1500, keep: 300) == 1400)
    }

    @Test func stopsAtTheBandEndAndLeavesWithIt() {
        // keep 300: the title and buttons need the last 300 pt of the band.
        #expect(StickyHeading.offset(header: header, visibleMinX: 1800, keep: 300) == 1700)
        #expect(StickyHeading.offset(header: header, visibleMinX: 5000, keep: 300) == 1700)
    }

    @Test func clearsTheLeftEdgeFadeOnceScrolledPastIt() {
        // The fade (56 pt) shows as soon as the wall scrolls; the title's
        // floor grows with the scroll until it clears the fade, no jump.
        #expect(StickyHeading.offset(header: header, visibleMinX: 0, fade: 56, keep: 300) == 0)
        #expect(StickyHeading.offset(header: header, visibleMinX: 60, fade: 56, keep: 300) == 16)
        #expect(StickyHeading.offset(header: header, visibleMinX: 160, fade: 56, keep: 300) == 116)
        #expect(StickyHeading.offset(header: header, visibleMinX: 5000, fade: 56, keep: 300) == 1700)
    }

    @Test func aBandNarrowerThanItsContentNeverMoves() {
        let narrow = Rect(x: 100, y: 20, width: 200, height: 46)
        #expect(StickyHeading.offset(header: narrow, visibleMinX: 250, keep: 300) == 0)
    }
}
