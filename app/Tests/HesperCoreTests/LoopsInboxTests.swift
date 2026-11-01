import Foundation
import Testing
@testable import HesperCore

struct LoopsInboxTests {
    @Test func firstPushSchedulesOneFlush() {
        let inbox = LoopsInbox()
        #expect(inbox.push(.loopRemoved("a")))
        #expect(!inbox.push(.loopRemoved("b")))
        #expect(inbox.drain() == [.loopRemoved("a"), .loopRemoved("b")])
        #expect(inbox.drain().isEmpty)
        #expect(inbox.push(.loopRemoved("c"))) // empty again: schedule again
    }

    @Test func overflowDropsTheOldest() {
        let inbox = LoopsInbox()
        let n = LoopsInbox.limit + 10
        for i in 0..<n { _ = inbox.push(.loopRemoved("lp-\(i)")) }
        let q = inbox.drain()
        #expect(q.count == LoopsInbox.limit)
        #expect(q.first == .loopRemoved("lp-10") && q.last == .loopRemoved("lp-\(n - 1)"))
    }
}
