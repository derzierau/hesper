import HesperCore
import HesperDesign
import HesperFleet
import SwiftUI
import UIKit
import UserNotifications

/// 4 · Connected: the pay-off (the Mac, its live summary, what it lets the
/// phone do), then the context for notifications and one Continue that
/// opens the system alert (HIG: one button, not "Allow", no Not now).
struct ConnectedScreen: View {
    @Environment(OnboardingModel.self) private var model
    @Environment(FleetModel.self) private var fleet
    @State private var asking = false

    var body: some View {
        let name = model.mac?.name ?? model.offer?.name ?? model.pending?.name ?? "Your Mac"
        OnboardingPage {
            VStack(alignment: .leading, spacing: DS.Spacing.s) {
                StepTick(state: model.account == nil ? .pending : .done, title: "Signed in as \(model.account?.login ?? "…")")
                StepTick(state: approvalTick, title: "\(name) approved this iPhone")
                StepTick(state: channelTick, title: "End-to-end channel")
            }
            .padding(.bottom, DS.Spacing.s)
            switch model.approval {
            case .approved(let rights):
                OnboardingTitle(title: "\(name) is connected")
                summary
                Text("\(RightsCopy.summary(rights)). Set on \(name).").font(.subheadline).foregroundStyle(Theme.text2)
                notificationPreview(name)
            case .failed(let error):
                OnboardingTitle(title: name, lead: Text(error.message(macName: name) ?? ""))
                if case .macOffline = error { notificationPreview(name) }
            case .asking, nil:
                OnboardingTitle(title: "Connecting to \(name)…")
            }
        } footer: {
            switch model.approval {
            case .approved, .failed(.macOffline):
                Button {
                    Task { await askForNotifications() }
                } label: {
                    Text("Continue").foregroundStyle(Theme.background).frame(maxWidth: .infinity)
                }
                .primaryAction()
                .disabled(asking)
            case .failed(.denied), .failed(.keyChanged):
                WideButton("Scan again") { model.scanAgain() }
            case .failed:
                WideButton("Try again") { model.retry() }
                LinkButton("Scan again") { model.scanAgain() }
            case .asking, nil:
                EmptyView()
            }
        }
    }

    private var approvalTick: StepTick.State {
        switch model.approval {
        case .approved: .done
        case .failed(.macOffline), .asking, nil: .pending
        case .failed: .failed
        }
    }

    private var channelTick: StepTick.State {
        guard case .approved = model.approval, let mac = model.mac else { return .pending }
        return fleet.fleet.macs[mac.machine]?.reachability.isLive == true ? .done : .pending
    }

    /// "3 working · 1 needs you", live from the link.
    @ViewBuilder private var summary: some View {
        let machine = model.mac?.machine ?? ""
        let agents = fleet.fleet.agents.values.filter { $0.machine == machine }
        let working = agents.count { $0.state == .working || $0.state == .starting }
        let needsYou = fleet.fleet.needsYou.count { $0.machine == machine }
        HStack(spacing: DS.Spacing.l) {
            if agents.isEmpty {
                Text("nothing running").foregroundStyle(Theme.text2)
            }
            if working > 0 {
                HStack(spacing: DS.Spacing.s) {
                    StateMark(StateMarkKind.working)
                    Text("\(working) working")
                }
            }
            if needsYou > 0 {
                HStack(spacing: DS.Spacing.s) {
                    StateMark(StateMarkKind.needsYou)
                    Text(needsYou == 1 ? "1 needs you" : "\(needsYou) need you")
                }
            }
        }
        .font(.subheadline.weight(.medium))
        .accessibilityElement(children: .combine)
    }

    /// What a Hesper notification looks like on the lock screen.
    private func notificationPreview(_ name: String) -> some View {
        VStack(alignment: .leading, spacing: DS.Spacing.l) {
            VStack(alignment: .leading, spacing: DS.Spacing.s) {
                HStack(spacing: DS.Spacing.s) {
                    HesperMark(size: 14).fixedSize()
                    Text("Hesper").font(.caption.weight(.semibold))
                    Spacer()
                    Text("now").font(.caption).foregroundStyle(Theme.dim)
                }
                Text("relay-tests on \(name) wants to edit hub.go").font(.subheadline)
                HStack(spacing: DS.Spacing.s) {
                    Text("Allow").padding(.horizontal, DS.Spacing.l).padding(.vertical, DS.Spacing.xs).background(.quaternary, in: .capsule)
                    Text("Deny").padding(.horizontal, DS.Spacing.l).padding(.vertical, DS.Spacing.xs).background(.quaternary, in: .capsule)
                }
                .font(.caption.weight(.medium))
            }
            .padding(DS.Spacing.l)
            .glassEffect(.regular, in: .rect(cornerRadius: DS.Spacing.xxl))
            .accessibilityElement(children: .combine)
            .accessibilityLabel("Example notification: relay-tests on \(name) wants to edit hub.go")
            Text("When an agent needs you, it shows up on your lock screen, and you can answer from there.")
                .font(.subheadline)
                .foregroundStyle(Theme.text2)
        }
        .padding(.top, DS.Spacing.l)
    }

    /// Explicit authorization (alert, sound, badge), not provisional:
    /// provisional notifications never reach the lock screen. Either answer
    /// goes on; with it, the APNs token goes to the relay (the app delegate
    /// passes it to `registerPushToken`).
    private func askForNotifications() async {
        asking = true
        defer { asking = false }
        let granted = (try? await UNUserNotificationCenter.current().requestAuthorization(options: [.alert, .sound, .badge])) ?? false
        if granted { UIApplication.shared.registerForRemoteNotifications() }
        model.notificationsAnswered()
    }
}

#Preview {
    ConnectedScreen().onboardingPreview(.connected)
}
