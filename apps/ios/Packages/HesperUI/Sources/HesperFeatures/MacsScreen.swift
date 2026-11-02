import HesperCore
import HesperDesign
import HesperFleet
import SwiftUI

/// The paired Macs in plain words: who is reachable, how, and who needs
/// you there; then Add Mac. On the sample Macs, a pill and Connect yours.
struct MacsScreen: View {
    @Environment(FleetModel.self) private var model
    @Environment(OnboardingModel.self) private var onboarding: OnboardingModel?
    @Environment(AddMacModel.self) private var addMac: AddMacModel?

    var body: some View {
        let samples = onboarding?.mode == .samples
        List {
            if samples, let onboarding {
                Section {
                    HStack(spacing: DS.Spacing.m) {
                        Text("Sample Macs")
                            .font(.caption.weight(.semibold))
                            .padding(.horizontal, DS.Spacing.m)
                            .padding(.vertical, DS.Spacing.xs)
                            .background(Theme.accent.opacity(0.15), in: .capsule)
                            .foregroundStyle(Theme.accent)
                        Text("Made up, to look around.").font(.subheadline).foregroundStyle(Theme.text2)
                        Spacer()
                        Button("Connect yours") { Task { await onboarding.connectYours() } }
                            .primaryAction()
                            .controlSize(.small)
                    }
                    .themedRow()
                }
            }
            if !samples, model.fleet.macList.isEmpty {
                Text("No Mac lets this iPhone in.").foregroundStyle(Theme.text2).themedRow()
            }
            Section {
                ForEach(model.fleet.macList) { mac in
                    HStack(spacing: DS.Spacing.m) {
                        StateMark(mac.reachability.isLive ? StateMarkKind.done : StateMarkKind.idle)
                        VStack(alignment: .leading, spacing: DS.Spacing.xxs) {
                            Text(MachineLabel.name(mac.machine)).font(.body.weight(.semibold))
                            Text(status(mac)).font(.subheadline).foregroundStyle(Theme.text2)
                        }
                        Spacer()
                        let waiting = model.fleet.needsYou.count { $0.machine == mac.id }
                        if waiting > 0 {
                            Text(waiting == 1 ? "1 needs you" : "\(waiting) need you")
                                .font(.caption.weight(.medium))
                                .foregroundStyle(Theme.signal)
                        }
                    }
                    .accessibilityElement(children: .combine)
                    .themedRow()
                }
            }
            if !samples, addMac != nil {
                AddMacSection()
            }
        }
        .themedList()
        .navigationTitle("Macs")
        .toolbar {
            if onboarding != nil {
                NavigationLink {
                    SettingsScreen()
                } label: {
                    Label("Settings", systemImage: "gearshape")
                }
            }
        }
    }

    private func status(_ mac: MacStatus) -> String {
        switch mac.reachability {
        case .connecting: return "Connecting…"
        case .live(let direct):
            let route = direct ? "same network" : "via relay"
            return mac.machine.rttMs.map { "Online · \(route) · \(Int($0)) ms" } ?? "Online · \(route)"
        case .unreachable(let since):
            return "Unreachable since \(since.formatted(date: .omitted, time: .shortened))"
        }
    }
}

#Preview {
    NavigationStack { MacsScreen() }.fixtureModel()
}
