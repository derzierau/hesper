import Foundation

/// When the terminal view follows the cursor: always, unless the owner
/// panned within the last `idle` seconds; then it holds still so they can
/// read.
public struct FollowRule: Sendable {
    public static let idle: Duration = .seconds(3)

    public init() {}

    public func shouldFollow(lastPan: Date?, now: Date) -> Bool {
        guard let lastPan else { return true }
        let idle =
            Double(Self.idle.components.seconds)
            + Double(Self.idle.components.attoseconds) / 1e18
        return now.timeIntervalSince(lastPan) >= idle
    }
}
