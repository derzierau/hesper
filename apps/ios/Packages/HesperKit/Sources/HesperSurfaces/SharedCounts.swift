import Foundation
import HesperFleet

/// The app group the app and its extensions share. The suffix after the
/// bundle prefix is fixed; the prefix comes from the build (Info.plist key
/// `HesperAppGroup`, set from `$(HESPER_APP_GROUP)`).
public enum AppGroup {
    public static let infoKey = "HesperAppGroup"

    /// The group from the running bundle's Info.plist, nil when unset
    /// (tests, previews).
    public static var identifier: String? {
        Bundle.main.object(forInfoDictionaryKey: infoKey) as? String
    }
}

/// The last fleet counts, written by the app and read by the widget: the
/// widget never talks to a Mac. Counts only, like the Live Activity.
public struct SharedCounts: Sendable {
    static let key = "fleet.counts"
    private let suite: String?

    public init(suite: String? = AppGroup.identifier) {
        self.suite = suite
    }

    private var defaults: UserDefaults? { suite.flatMap(UserDefaults.init(suiteName:)) }

    public func write(_ counts: FleetCounts, at date: Date = .now) {
        guard let defaults, let data = try? JSONEncoder().encode(Stamped(counts: counts, at: date)) else { return }
        defaults.set(data, forKey: Self.key)
    }

    /// The counts and when they were written; nil before the first write.
    public func read() -> (counts: FleetCounts, at: Date)? {
        guard let data = defaults?.data(forKey: Self.key),
            let s = try? JSONDecoder().decode(Stamped.self, from: data)
        else { return nil }
        return (s.counts, s.at)
    }

    private struct Stamped: Codable {
        var counts: FleetCounts
        var at: Date
    }
}
