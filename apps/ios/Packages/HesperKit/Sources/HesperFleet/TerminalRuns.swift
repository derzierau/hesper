/// Which run of a terminal view owns its shared attach state (the stream,
/// the input hook, the keyboard). SwiftUI's `.task(id:)` starts a new run
/// without waiting for the one it cancels, so an old run can still return
/// from an attach or a flush. Only the newest run writes; a superseded
/// one closes its own stream and stops.
public struct TerminalRuns: Sendable, Equatable {
    /// The newest run's number.
    public private(set) var current = 0
    /// The newest run has a read-write attach: the keyboard is offered and
    /// input goes to its stream.
    public private(set) var typing = false

    public init() {}

    /// A run starts and owns the state from now on, detached.
    public mutating func begin() -> Int {
        current += 1
        typing = false
        return current
    }

    /// Whether `run` may still write the shared state. Asked again after
    /// every await: a frame or a flush in flight can resume after a newer
    /// run began, and must then draw nothing.
    public func owns(_ run: Int) -> Bool { run == current }

    /// `run` attached. False for a superseded run: it must close its stream
    /// and touch nothing.
    public mutating func attached(_ run: Int, typing: Bool) -> Bool {
        guard owns(run) else { return false }
        self.typing = typing
        return true
    }

    /// `run`'s attach ended or failed. False for a superseded run, which
    /// leaves the newer run's state alone.
    public mutating func detached(_ run: Int) -> Bool {
        guard owns(run) else { return false }
        typing = false
        return true
    }
}
