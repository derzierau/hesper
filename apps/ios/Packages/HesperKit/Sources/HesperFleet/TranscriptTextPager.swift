import HesperCore
import Observation

/// Keeps only the visible part of a long message in memory.
@MainActor @Observable
public final class TranscriptTextPager {
    public static let chunkSize = 65_536

    public private(set) var text: String?
    public private(set) var offset = 0
    public private(set) var next = 0
    public private(set) var more = false
    public private(set) var loading = false
    public private(set) var failure: String?
    private var previousOffsets: [Int] = []

    public init() {}

    public var canNext: Bool { text == nil || more }
    public var canPrevious: Bool { !previousOffsets.isEmpty }

    public func loadNext(_ fetch: @MainActor (Int, Int) async throws -> TranscriptTextChunk) async {
        guard canNext, !loading else { return }
        let target = text == nil ? 0 : next
        await load(target, goingForward: text != nil, fetch: fetch)
    }

    public func loadPrevious(_ fetch: @MainActor (Int, Int) async throws -> TranscriptTextChunk) async {
        guard let target = previousOffsets.last, !loading else { return }
        await load(target, goingForward: false, fetch: fetch)
    }

    private func load(
        _ target: Int, goingForward: Bool,
        fetch: @MainActor (Int, Int) async throws -> TranscriptTextChunk
    ) async {
        loading = true
        failure = nil
        defer { loading = false }
        do {
            let chunk = try await fetch(target, Self.chunkSize)
            guard chunk.text.utf8.count <= Self.chunkSize,
                chunk.next >= target, chunk.next - target == chunk.text.utf8.count,
                !chunk.more || chunk.next > target
            else {
                failure = "The Mac sent an invalid text part."
                return
            }
            if goingForward { previousOffsets.append(offset) } else if text != nil { previousOffsets.removeLast() }
            text = chunk.text
            offset = target
            next = chunk.next
            more = chunk.more
        } catch {
            failure = String(describing: error)
        }
    }
}
