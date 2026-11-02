import Foundation
import HesperCore

/// The one reminder when notifications are off (docs/iphone/onboarding.md,
/// "Permissions"): the first time an agent needed the owner while the app
/// was away, the Inbox says how long it waited.
public enum NotificationsNudge {
    /// The agent that has waited longest of those that came to need the
    /// owner after `awaySince`, and for how many whole minutes; nil when
    /// none did, or it was under a minute.
    public static func waited(needsYou: [Agent], awaySince: Date?, now: Date) -> (agent: Agent, minutes: Int)? {
        guard let awaySince else { return nil }
        let waiting = needsYou.filter { ($0.stateSince ?? .distantPast) >= awaySince }
        guard let first = waiting.min(by: { ($0.stateSince ?? now) < ($1.stateSince ?? now) }), let since = first.stateSince else {
            return nil
        }
        let minutes = Int(now.timeIntervalSince(since) / 60)
        return minutes >= 1 ? (first, minutes) : nil
    }
}
