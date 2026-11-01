import Foundation

/// Decoded notifications between the reader thread and the main actor.
/// `push` is true when the queue was empty (the caller schedules one
/// flush); the queue is bounded, the oldest events go first (a list on
/// the next connect repairs the gap).
// @unchecked Sendable: every access to `queue` holds `lock`.
public final class LoopsInbox: @unchecked Sendable {
    public static let limit = 4096
    private let lock = NSLock()
    private var queue: [LoopsEvent] = []

    public init() {}

    public func push(_ e: LoopsEvent) -> Bool {
        lock.lock(); defer { lock.unlock() }
        queue.append(e)
        if queue.count > Self.limit { queue.removeFirst(queue.count - Self.limit) }
        return queue.count == 1
    }

    public func drain() -> [LoopsEvent] {
        lock.lock(); defer { lock.unlock() }
        let q = queue
        queue = []
        return q
    }
}
