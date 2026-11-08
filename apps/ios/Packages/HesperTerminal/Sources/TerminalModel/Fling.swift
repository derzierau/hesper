import CoreGraphics
import Foundation

/// The momentum after a flick on the terminal: the lift-off velocity,
/// decaying at UIScrollView's normal deceleration rate until it rests.
public struct Fling: Sendable {
    /// What is left of the velocity after one millisecond
    /// (`UIScrollView.DecelerationRate.normal`).
    public static let rate: CGFloat = 0.998
    /// Points per second below which the fling has stopped.
    public static let rest: CGFloat = 20

    /// Points per second; positive when the finger moved down.
    public private(set) var velocity: CGFloat

    public init(velocity: CGFloat) {
        self.velocity = velocity
    }

    /// The distance in points to move for a tick of `dt`, then the
    /// velocity decays; nil once it is at rest.
    public mutating func step(_ dt: Duration) -> CGFloat? {
        guard abs(velocity) >= Self.rest else { return nil }
        let (seconds, attoseconds) = dt.components
        let ms = CGFloat(seconds) * 1000 + CGFloat(attoseconds) / 1e15
        let distance = velocity * ms / 1000
        velocity *= pow(Self.rate, ms)
        return distance
    }
}
