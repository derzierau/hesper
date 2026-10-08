import Foundation

/// Undo beats confirm: stop, remove, move and discard happen at once and
/// can be undone for a while (6 s, ⌘Z or the toast's button). The newest
/// entry is undone first; expired entries are handed back once so an
/// action held back for undo (a removal) can be committed.
public struct UndoStack<Action: Sendable>: Sendable {
    public struct Entry: Sendable, Identifiable {
        public let id: UUID
        public var label: String
        public var action: Action
        public var expires: Date
    }

    public var window: TimeInterval
    public private(set) var entries: [Entry] = []

    public init(window: TimeInterval = 6) { self.window = window }

    @discardableResult
    public mutating func push(_ action: Action, label: String, now: Date = Date()) -> Entry {
        let e = Entry(id: UUID(), label: label, action: action, expires: now.addingTimeInterval(window))
        entries.append(e)
        return e
    }

    /// The newest entry that can still be undone (the toast shows it).
    public func top(now: Date = Date()) -> Entry? { entries.last { $0.expires > now } }

    /// Takes the newest undoable entry (⌘Z).
    public mutating func pop(now: Date = Date()) -> Entry? {
        guard let i = entries.lastIndex(where: { $0.expires > now }) else { return nil }
        return entries.remove(at: i)
    }

    /// Takes one entry by id (the toast's own button).
    public mutating func take(_ id: UUID, now: Date = Date()) -> Entry? {
        guard let i = entries.firstIndex(where: { $0.id == id }), entries[i].expires > now else { return nil }
        return entries.remove(at: i)
    }

    /// Removes and returns entries whose time is up, oldest first.
    public mutating func expire(now: Date = Date()) -> [Entry] {
        let gone = entries.filter { $0.expires <= now }
        entries.removeAll { $0.expires <= now }
        return gone
    }

    /// Everything still pending (app quit: commit them).
    public mutating func drain() -> [Entry] {
        defer { entries = [] }
        return entries
    }

    public var nextExpiry: Date? { entries.map(\.expires).min() }

    /// A new label for an entry (its outcome came later: "will stop when
    /// mini is back").
    public mutating func relabel(_ id: UUID, _ label: String) {
        if let i = entries.firstIndex(where: { $0.id == id }) { entries[i].label = label }
    }

    /// Rewrites every pending action (a closed agent's session id arrived).
    public mutating func mapActions(_ f: (Action) -> Action) {
        for i in entries.indices { entries[i].action = f(entries[i].action) }
    }

    /// Drops an entry whatever its time (it can't be undone after all).
    public mutating func drop(_ id: UUID) { entries.removeAll { $0.id == id } }
}
