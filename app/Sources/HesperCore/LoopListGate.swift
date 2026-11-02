import Foundation

/// When LoopHub lists the loops (`loops.list`, `goals.list`, …): one list
/// at a time; a connect while one is out lists again after it (the list
/// may predate the new connection); a failed list retries with backoff
/// (1, 2, 4, 8, 16 s), then waits for the next connect. `loaded` turns on
/// with the first list, or once a list failed and notifications arrived
/// (the subscribe snapshot sends every open and parked loop), so the
/// sheets never wait forever. Pure: the hub runs the lists and timers.
public struct LoopListGate: Equatable, Sendable {
    /// Retries after a failed list before it waits for a connect.
    public static let retries = 5

    public enum Step: Equatable, Sendable {
        /// Nothing to do.
        case idle
        /// List again now (`listing` is already on).
        case list
        /// Call `retry()` after this many seconds.
        case retry(after: TimeInterval)
    }

    public private(set) var listing = false
    public private(set) var loaded = false
    private var again = false
    private var failures = 0
    private var events = false

    public init() {}

    /// A connect: true when the caller starts a list now.
    public mutating func connect() -> Bool {
        failures = 0
        guard !listing else { again = true; return false }
        listing = true
        return true
    }

    /// A retry's wait ended: true when the caller starts a list now.
    public mutating func retry() -> Bool {
        guard !listing else { return false }
        listing = true
        return true
    }

    /// The list came back (ok false: it failed, nothing was applied).
    public mutating func finished(ok: Bool) -> Step {
        listing = false
        if ok {
            loaded = true
            failures = 0
        } else {
            failures += 1
            if events { loaded = true }
        }
        if again {
            again = false
            failures = 0
            listing = true
            return .list
        }
        guard !ok, failures <= Self.retries else { return .idle }
        return .retry(after: Self.backoff(failures))
    }

    /// Notifications were applied to the store.
    public mutating func sawEvents() {
        events = true
        if failures > 0 { loaded = true }
    }

    static func backoff(_ failure: Int) -> TimeInterval { TimeInterval(1 << min(failure - 1, 4)) }
}
