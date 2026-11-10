import HesperCore
import HesperDesign
import HesperFleet
import SwiftUI
import UIKit
import UserNotifications

/// Notifications declined: one reminder, once, the first time something
/// waited while the app was away (docs/iphone/onboarding.md,
/// "Permissions"). Dismissed, it never returns.
struct NotificationsOffCard: ViewModifier {
    let awaySince: Date?
    @Environment(FleetModel.self) private var model
    @Environment(OnboardingModel.self) private var onboarding: OnboardingModel?
    @Environment(\.openURL) private var openURL
    @AppStorage("notificationsCardDismissed") private var dismissed = false
    /// Held once shown, so answering the agent does not hide it.
    @State private var waited: Waited?

    struct Waited: Equatable {
        var name: String
        var minutes: Int
    }

    func body(content: Content) -> some View {
        content.safeAreaInset(edge: .top) {
            if let waited, !dismissed {
                VStack(alignment: .leading, spacing: DS.Spacing.s) {
                    HStack(alignment: .firstTextBaseline) {
                        Text("\(waited.name) waited \(waited.minutes) min").font(.headline)
                        Spacer()
                        Button("Dismiss", systemImage: "xmark") { dismissed = true }
                            .labelStyle(.iconOnly)
                            .foregroundStyle(Theme.dim)
                            .buttonStyle(.plain)
                    }
                    Text("Turn on notifications to get this on your lock screen.").font(.subheadline).foregroundStyle(Theme.text2)
                    Button("Settings") {
                        if let url = URL(string: UIApplication.openNotificationSettingsURLString) { openURL(url) }
                    }
                    .primaryAction()
                    .controlSize(.small)
                }
                .padding(DS.Spacing.l)
                .neonPanel()
                .padding(.horizontal, DS.Spacing.l)
            }
        }
        // Again when the Macs report in after the app comes back.
        .task(id: "\(awaySince?.timeIntervalSince1970 ?? 0) \(model.fleet.needsYou.count)") { await check() }
    }

    private func check() async {
        guard waited == nil, !dismissed, onboarding?.mode != .samples, awaySince != nil else { return }
        let status = await UNUserNotificationCenter.current().notificationSettings().authorizationStatus
        guard status == .denied, let w = NotificationsNudge.waited(needsYou: model.fleet.needsYou, awaySince: awaySince, now: .now)
        else { return }
        waited = Waited(name: w.agent.name, minutes: w.minutes)
    }
}
