import CoreGraphics
import Testing

@testable import TerminalModel

/// A flick keeps scrolling and slows down as a UIScrollView does.
@Suite struct FlingTests {
    @Test func aFlingDeceleratesAndStops() {
        var f = Fling(velocity: 1000)  // pt/s, the finger moved down
        var total: CGFloat = 0
        var steps = 0
        var last = CGFloat.infinity
        while let d = f.step(.milliseconds(16)) {
            #expect(d > 0 && d < last)
            last = d
            total += d
            steps += 1
        }
        // 1000 pt/s at 0.998 per ms comes to rest after about 500 pt.
        #expect(total > 400 && total < 600)
        #expect(steps > 60 && steps < 400)
    }

    @Test func aSlowFlingDoesNothing() {
        var f = Fling(velocity: 10)
        #expect(f.step(.milliseconds(16)) == nil)
    }

    @Test func directionFollowsTheVelocity() {
        var f = Fling(velocity: -800)
        #expect((f.step(.milliseconds(16)) ?? 0) < 0)
    }
}
