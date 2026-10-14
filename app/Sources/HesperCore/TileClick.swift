import Foundation

/// What a plain click on a wall tile does (docs/rebuild-contract.md "Two
/// levels of focus on a wall tile"). One click on a live tile makes it
/// active: typing, clicks and the wheel go to its terminal at once. The
/// keyboard keeps the two levels (arrows only select).
public enum TileClick: Equatable, Sendable {
    /// Double click: the focus view.
    case focus
    /// The active tile: the click is the terminal's.
    case input
    /// Typing, clicks and the wheel go to the agent (read-write attach).
    case activate
    /// The wall keeps the keys.
    case select

    /// - `answers`: the selected tile's one-key answers (a question's).
    ///   A tile with an approval or such a question selects first, so
    ///   ⏎ / A / N answer it; a click on it once selected types.
    public static func resolve(clickCount: Int, active: Bool, selected: Bool, running: Bool, onShelf: Bool,
                               state: AgentState, answers: [Decision]) -> TileClick {
        if clickCount >= 2 { return .focus }
        if active { return .input }
        // A shelf card has no terminal; an ended agent nothing to type to.
        if !running || onShelf { return .select }
        let answersByKey = state == .approval || (state == .question && !answers.isEmpty)
        if answersByKey && !selected { return .select }
        return .activate
    }

    /// Whether the tile, not its terminal, takes a click: the second click
    /// of the click that made it active (a double click on a tile that was
    /// not active opens focus, whether or not its read-write terminal has
    /// swapped in between the two clicks).
    public static func claimsSecondClick(clickCount: Int, sinceActivation: Double?, doubleClickInterval: Double) -> Bool {
        guard clickCount >= 2, let t = sinceActivation else { return false }
        return t >= 0 && t <= doubleClickInterval
    }
}

/// Input that arrived for the active tile before its read-write terminal
/// swapped in (clicks, the wheel), replayed in order once it has. Bounded:
/// when full the newest is dropped (a gesture keeps its start), and
/// nothing older than `maxAge` is replayed.
public struct HeldInput<Event> {
    public let capacity: Int
    public let maxAge: Double
    private var held: [(event: Event, at: Double)] = []

    public init(capacity: Int, maxAge: Double) {
        self.capacity = capacity
        self.maxAge = maxAge
    }

    public var count: Int { held.count }
    public var isEmpty: Bool { held.isEmpty }

    public mutating func hold(_ event: Event, at time: Double) {
        guard held.count < capacity else { return }
        held.append((event, time))
    }

    /// The held events still fresh at `now`, oldest first; empties.
    public mutating func drain(now: Double) -> [Event] {
        defer { held.removeAll() }
        return held.filter { now - $0.at <= maxAge }.map(\.event)
    }

    public mutating func clear() { held.removeAll() }
}
