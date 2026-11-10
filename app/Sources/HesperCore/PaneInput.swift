import Foundation

/// Input a terminal pane holds while a surface that takes input is on its
/// way, and its size take-back (docs/rebuild-contract.md "Size owned by
/// another device"). Pure, so the sequences are unit-tested; AgentTerminal
/// drives it and only sends what it returns.
///
/// Held events (an active tile's clicks, keys of a take-back) go to the
/// next surface that takes input, oldest first, whichever way it becomes
/// current (a mode swap, a new or re-created surface). Events older than
/// `maxAge` when that happens are dropped, as are events beyond `capacity`.
public struct PaneInput<Event> {
    private var held: HeldInput<Event>
    /// When the take-back started (uptime); nil: none pending.
    public private(set) var takeBackSince: Double?

    public init(capacity: Int = 128, maxAge: Double = 1.5) {
        held = HeldInput(capacity: capacity, maxAge: maxAge)
    }

    public var isTakingBack: Bool { takeBackSince != nil }
    public var heldCount: Int { held.count }

    /// Holds an event for the next surface that takes input.
    public mutating func hold(_ e: Event, at time: Double) { held.hold(e, at: time) }

    /// Starts a take-back at `now`; false (and nothing changes) when no
    /// owner surface can come (no window, too narrow, not attachable): the
    /// key then goes on as without the feature.
    public mutating func beginTakeBack(at now: Double, ownerCanCome: Bool) -> Bool {
        guard ownerCanCome else { return false }
        if takeBackSince == nil { takeBackSince = now }
        return true
    }

    /// A surface became current. One that takes input gets the held events
    /// (in order, the stale ones dropped). One that does not: they are
    /// dropped, unless a take-back is pending (its owner surface follows).
    public mutating func surfaceCurrent(acceptsInput: Bool, now: Double) -> [Event] {
        if acceptsInput { return held.drain(now: now) }
        if !isTakingBack { held.clear() }
        return []
    }

    /// The daemon reported another owner (it saw this pane's owner attach,
    /// or a new device took over): the take-back is over. Held keys stay
    /// for the owner surface if it is still swapping in.
    public mutating func takeBackSettled() { takeBackSince = nil }

    /// Ends a take-back the daemon has not settled within `timeout` (the
    /// owner attach failed): its held keys are dropped. True: it ended.
    public mutating func expireTakeBack(now: Double, timeout: Double = SizeOwner.takeBackTimeout) -> Bool {
        guard let since = takeBackSince, now - since >= timeout else { return false }
        takeBackSince = nil
        held.clear()
        return true
    }

    /// Drops everything held (the pane stopped taking input or closed).
    public mutating func clear() { held.clear() }
}
