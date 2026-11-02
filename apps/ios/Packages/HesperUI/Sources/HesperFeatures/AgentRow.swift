import HesperCore
import HesperDesign
import HesperFleet
import SwiftUI

/// One agent in a list: mark, name, what it asks or does, Mac and age.
struct AgentRow: View {
    let agent: Agent
    @Environment(FleetModel.self) private var model

    var body: some View {
        HStack(alignment: .firstTextBaseline, spacing: DS.Spacing.m) {
            StateMark(agent.state)
            VStack(alignment: .leading, spacing: DS.Spacing.xxs) {
                Text(agent.name).font(.body.weight(.semibold))
                Text(line)
                    .font(.subheadline)
                    .foregroundStyle(Theme.text2)
                    .lineLimit(2)
            }
            Spacer(minLength: DS.Spacing.s)
            MetaLine(mac: model.fleet.macName(agent.machine), since: agent.stateSince)
        }
        .opacity(model.fleet.isReachable(agent) ? 1 : 0.45)
        .accessibilityElement(children: .combine)
    }

    private var line: String {
        if model.pending.contains(agent.id) { return "Sent, waiting for \(model.fleet.macName(agent.machine))" }
        if !model.fleet.isReachable(agent), case .unreachable(let since)? = model.fleet.macs[agent.machine]?.reachability {
            return "\(model.fleet.macName(agent.machine)) unreachable since \(since.formatted(date: .omitted, time: .shortened))"
        }
        return AgentCopy.headline(agent)
    }
}
