import Foundation
import Testing

@testable import TerminalModel

/// The view follows the cursor unless the owner panned in the last 3 s.
@Suite struct FollowRuleTests {
    let now = Date(timeIntervalSinceReferenceDate: 1_000)

    @Test func followsWhenNeverPanned() {
        #expect(FollowRule().shouldFollow(lastPan: nil, now: now))
    }

    @Test func holdsStillRightAfterAPan() {
        #expect(!FollowRule().shouldFollow(lastPan: now.addingTimeInterval(-1), now: now))
        #expect(!FollowRule().shouldFollow(lastPan: now.addingTimeInterval(-2.99), now: now))
    }

    @Test func followsAgainAfterThreeIdleSeconds() {
        #expect(FollowRule.idle == .seconds(3))
        #expect(FollowRule().shouldFollow(lastPan: now.addingTimeInterval(-3), now: now))
        #expect(FollowRule().shouldFollow(lastPan: now.addingTimeInterval(-60), now: now))
    }
}
