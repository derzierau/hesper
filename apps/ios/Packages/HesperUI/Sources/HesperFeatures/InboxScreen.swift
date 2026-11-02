import HesperCore
import HesperDesign
import HesperFleet
import SwiftUI
import UIKit
import UserNotifications

/// What waits for the owner: Needs you (answered inline), then Ready to
/// review. Empty is the goal.
struct InboxScreen: View {
    /// When the app last went to the background, for the notifications
    /// card.
    var awaySince: Date?
    @Environment(FleetModel.self) private var model

    var body: some View {
        let fleet = model.fleet
        List {
            if !fleet.needsYou.isEmpty {
                Section("Needs you · \(fleet.needsYou.count)") {
                    ForEach(fleet.needsYou) { agent in
                        NeedsYouCard(agent: agent).themedRow()
                    }
                }
            }
            if !fleet.readyToReview.isEmpty {
                Section("Ready to review · \(fleet.readyToReview.count)") {
                    ForEach(fleet.readyToReview) { agent in
                        NavigationLink(value: AgentRoute(id: agent.id)) { AgentRow(agent: agent) }.themedRow()
                    }
                }
            }
        }
        .overlay {
            if fleet.needsYou.isEmpty && fleet.readyToReview.isEmpty {
                ContentUnavailableView(
                    "Nothing needs you",
                    systemImage: "tray",
                    description: Text(
                        fleet.counts.working > 0
                            ? "\(fleet.counts.working) agents are working. You'll get a notification when one asks for you."
                            : "Tap the mic to start an agent.")
                )
            }
        }
        .themedList()
        .modifier(NotificationsOffCard(awaySince: awaySince))
        .navigationTitle("Inbox")
    }
}

/// A needs-you row with its answers. Risky commands only open.
struct NeedsYouCard: View {
    let agent: Agent
    @Environment(FleetModel.self) private var model

    var body: some View {
        VStack(alignment: .leading, spacing: DS.Spacing.m) {
            NavigationLink(value: AgentRoute(id: agent.id)) { AgentRow(agent: agent) }
            DecisionButtons(agent: agent)
        }
        .padding(.vertical, DS.Spacing.xs)
    }
}

/// The answers to a prompt as native buttons: Allow · Always · Deny for an
/// approval, the numbered choices for a question.
struct DecisionButtons: View {
    let agent: Agent
    @Environment(FleetModel.self) private var model

    var body: some View {
        let pending = model.pending.contains(agent.id)
        HStack(spacing: DS.Spacing.s) {
            switch agent.state {
            case .approval where RiskRule.isRisky(agent):
                Text("Changes the database or remote state. Open it to answer.")
                    .font(.footnote)
                    .foregroundStyle(Theme.question)
            case .approval:
                Button("Allow") { send(.answer(agent: agent.id, .allow)) }.primaryAction()
                Button("Always") { send(.answer(agent: agent.id, .always)) }.secondaryAction()
                Button("Deny") { send(.answer(agent: agent.id, .deny)) }.secondaryAction()
            case .question:
                let choices = AgentCopy.choices(agent)
                if choices.isEmpty {
                    Text("Open it to answer.").font(.footnote).foregroundStyle(Theme.dim)
                } else {
                    ForEach(choices.prefix(4), id: \.number) { choice in
                        Button("\(choice.number) \(choice.title)") { send(.choose(agent: agent.id, option: choice.number)) }
                            .secondaryAction()
                    }
                }
            default:
                EmptyView()
            }
        }
        .disabled(pending || !model.fleet.isReachable(agent))
        .buttonBorderShape(.capsule)
        .controlSize(.small)
    }

    private func send(_ command: LinkCommand) {
        Task { await model.perform(command) }
    }
}

/// Notifications declined: one reminder, once, the first time something
/// waited while the app was away (docs/iphone/onboarding.md,
/// "Permissions"). Dismissed, it never returns.
private struct NotificationsOffCard: ViewModifier {
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
                .background(Theme.tile, in: .rect(cornerRadius: DS.Spacing.xl, style: .continuous))
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

#Preview {
    NavigationStack { InboxScreen() }.fixtureModel()
}
