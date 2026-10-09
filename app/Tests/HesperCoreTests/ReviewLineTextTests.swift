import Foundation
import Testing
@testable import HesperCore

struct ReviewLineTextTests {
    @Test func tabsExpandAndWordsMove() {
        let (t, w) = ReviewLineText.display("\tif x {\t// y", words: [NSRange(location: 1, length: 2), NSRange(location: 8, length: 4)])
        #expect(t == "    if x {  // y")
        #expect(w == [NSRange(location: 4, length: 2), NSRange(location: 12, length: 4)])
        let (plain, pw) = ReviewLineText.display("abc", words: [NSRange(location: 0, length: 1)])
        #expect(plain == "abc" && pw == [NSRange(location: 0, length: 1)])
        #expect(ReviewLineText.display("a\r", words: []).text == "a")
    }

    @Test func longLinesAreCut() {
        let long = String(repeating: "x", count: ReviewLineText.maxLength + 50)
        let (t, w) = ReviewLineText.display(long, words: [NSRange(location: 990, length: 40), NSRange(location: 1020, length: 5)])
        #expect(t.utf16.count == ReviewLineText.maxLength + 1 && t.hasSuffix("…"))
        #expect(w == [NSRange(location: 990, length: 10)])
        let exact = String(repeating: "y", count: ReviewLineText.maxLength)
        #expect(ReviewLineText.display(exact + "\t", words: []).text == exact + "…")
    }
}
