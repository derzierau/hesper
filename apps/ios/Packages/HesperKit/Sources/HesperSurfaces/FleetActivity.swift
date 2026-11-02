#if os(iOS)
    import ActivityKit
    import Foundation
    import HesperFleet

    /// The one fleet Live Activity (docs/iphone/concept.md, "Surfaces outside
    /// the app"). Its content is counts only, so the relay can update it by
    /// push without seeing names; the oldest waiting agent's name is filled in
    /// by the app when it runs.
    public struct FleetActivityAttributes: ActivityAttributes {
        public struct ContentState: Codable, Hashable, Sendable {
            public var counts: FleetCounts
            /// The oldest agent that needs you, when the app knows it.
            public var oldestWaiting: String?
            public var waitingSince: Date?

            public init(counts: FleetCounts, oldestWaiting: String? = nil, waitingSince: Date? = nil) {
                self.counts = counts
                self.oldestWaiting = oldestWaiting
                self.waitingSince = waitingSince
            }
        }

        public init() {}
    }
#endif
