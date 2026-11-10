import Foundation
import HesperCore
import HesperFleet
import Testing

@MainActor
@Suite struct TranscriptTextPagerTests {
    private final class Calls { var values: [(Int, Int)] = [] }

    @Test func pagesUseByteOffsetsAndKeepOnlyTheCurrentChunk() async {
        let pager = TranscriptTextPager()
        let asked = Calls()
        let fetch: @MainActor @Sendable (Int, Int) async throws -> TranscriptTextChunk = { offset, limit in
            asked.values.append((offset, limit))
            return offset == 0
                ? TranscriptTextChunk(text: "é", next: 2, more: true)
                : TranscriptTextChunk(text: "end", next: 5, more: false)
        }
        await pager.loadNext(fetch)
        #expect(pager.text == "é" && pager.offset == 0 && pager.canNext)
        await pager.loadNext(fetch)
        #expect(pager.text == "end" && pager.offset == 2 && !pager.canNext && pager.canPrevious)
        await pager.loadPrevious(fetch)
        #expect(pager.text == "é" && pager.offset == 0 && !pager.canPrevious)
        #expect(asked.values.map(\.0) == [0, 2, 0])
        #expect(asked.values.allSatisfy { $0.1 == 65_536 })
    }

    @Test func aFailedPageKeepsTheCurrentTextAndCanRetry() async {
        struct Unavailable: Error {}
        let pager = TranscriptTextPager()
        await pager.loadNext { _, _ in TranscriptTextChunk(text: "first", next: 5, more: true) }
        await pager.loadNext { _, _ in throw Unavailable() }
        #expect(pager.text == "first" && pager.offset == 0 && pager.canNext && pager.failure != nil)
        await pager.loadNext { offset, _ in
            #expect(offset == 5)
            return TranscriptTextChunk(text: "last", next: 9, more: false)
        }
        #expect(pager.text == "last" && pager.failure == nil)
    }
}
