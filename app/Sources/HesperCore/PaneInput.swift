import Foundation

/// Input a terminal pane holds for a surface that is on its way
/// (docs/rebuild-contract.md "Size owned by another device"). Pure, so the
/// sequences are unit-tested; AgentTerminal and SizeOwnership drive it and
/// only send what it returns.
///
/// Two separate things:
/// - The active tile's clicks and wheel (`holdClick`), exactly as before
///   size take-back existed: a HeldInput with its age rule, drained or
///   dropped where the mode swap finishes.
/// - A size take-back's keys, in one explicit phase. `normal`: no
///   take-back. `awaitingOwner`: a take-back began; every key aimed at the
///   pane is held, in order, whatever the daemon reports meanwhile. The
///   phase ends only when (a) an owner surface that takes input is current
///   and the held keys were delivered to it, (b) the timeout ran out, or
///   (c) no owner surface can come any more; (b) and (c) drop the keys.
///   Held keys have no age limit (the timeout bounds them); past
///   `keyCapacity` the newest are dropped, never reordered.
public struct PaneInput<Event> {
    public enum Phase: Equatable, Sendable { case normal, awaitingOwner }

    public private(set) var phase: Phase = .normal
    /// When the take-back began (uptime); nil in `normal`.
    public private(set) var takeBackSince: Double?
    public let keyCapacity: Int
    private var clicks: HeldInput<Event>
    private var keys: [Event] = []
    /// An owner surface that takes input is current (delivery may run).
    private var ownerCurrent = false

    public init(clickCapacity: Int = 128, clickMaxAge: Double = 1.5, keyCapacity: Int = 128) {
        clicks = HeldInput(capacity: clickCapacity, maxAge: clickMaxAge)
        self.keyCapacity = keyCapacity
    }

    public var isAwaitingOwner: Bool { phase == .awaitingOwner }
    public var heldKeyCount: Int { keys.count }

    // MARK: Clicks (unchanged behaviour)

    public mutating func holdClick(_ e: Event, at time: Double) { clicks.hold(e, at: time) }
    public mutating func drainClicks(now: Double) -> [Event] { clicks.drain(now: now) }
    public mutating func clearClicks() { clicks.clear() }

    // MARK: Take-back

    /// Begins a take-back at `now`: `awaitingOwner`. False (still
    /// `normal`) when no owner surface can come; the key then goes on as
    /// without the feature.
    public mutating func beginTakeBack(at now: Double, ownerCanCome: Bool) -> Bool {
        guard ownerCanCome else { return false }
        if phase == .normal {
            phase = .awaitingOwner
            takeBackSince = now
            ownerCurrent = false
        }
        return true
    }

    /// Holds a key while `awaitingOwner`. False: not held (not awaiting, or
    /// at capacity: the newest is dropped).
    @discardableResult
    public mutating func holdKey(_ e: Event) -> Bool {
        guard phase == .awaitingOwner, keys.count < keyCapacity else { return false }
        keys.append(e)
        return true
    }

    /// The one entry point for every surface that becomes current. True:
    /// an owner surface taking input is current while awaiting; run
    /// `deliverHeldKeys()` on the next main-loop turn. A view surface
    /// changes nothing but that an earlier owner no longer counts.
    public mutating func surfaceCurrent(acceptsInput: Bool) -> Bool {
        guard phase == .awaitingOwner else { return false }
        ownerCurrent = acceptsInput
        return acceptsInput
    }

    /// The held keys, oldest first, for the owner surface that is current;
    /// back to `normal`. Empty (still awaiting) if no such surface is
    /// current any more, or (already `normal`) if nothing is pending.
    public mutating func deliverHeldKeys() -> [Event] {
        guard phase == .awaitingOwner, ownerCurrent else { return [] }
        defer { end() }
        return keys
    }

    /// The take-back is called off at once, its keys dropped: no owner
    /// surface can come any more (window gone, too narrow, agent not
    /// attachable), another device took the size over, or the pane closed.
    public mutating func endTakeBack() { end() }

    /// Ends a take-back older than `timeout` at `now`, dropping its keys.
    /// True: it ended.
    public mutating func expireTakeBack(now: Double, timeout: Double = SizeOwner.takeBackTimeout) -> Bool {
        guard let since = takeBackSince, now - since >= timeout else { return false }
        end()
        return true
    }

    private mutating func end() {
        phase = .normal
        takeBackSince = nil
        ownerCurrent = false
        keys.removeAll()
    }
}
