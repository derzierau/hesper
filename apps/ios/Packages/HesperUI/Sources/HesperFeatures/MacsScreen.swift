import HesperCore
import HesperDesign
import HesperFleet
import SwiftUI

/// The paired Macs in plain words: who is reachable, how, and who needs
/// you there. Pairing comes with the mobile link library.
struct MacsScreen: View {
    @Environment(FleetModel.self) private var model

    var body: some View {
        List {
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
            Section {
                Label("Add a Mac", systemImage: "plus")
                    .foregroundStyle(Theme.dim)
                    .themedRow()
            } footer: {
                Text("Pairing arrives with the mobile link library (docs/iphone/concept.md, Core work 1).")
            }
        }
        .themedList()
        .navigationTitle("Macs")
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
