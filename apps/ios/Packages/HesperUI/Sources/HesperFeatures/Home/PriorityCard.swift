import HesperCore
import HesperDesign
import HesperFleet
import SwiftUI

/// The head of the attention queue as Home's big card: what it asks in
/// the hero face, the command, the owner's task, and the numbered options
/// (docs/iphone/neon.md, "Priority decision card"). Everything above the
/// options opens the agent.
struct PriorityCard: View {
    let agent: Agent
    @Environment(FleetModel.self) private var model
    @Environment(\.decide) private var decide

    var body: some View {
        let rows = DecisionOptions.rows(for: agent)
        VStack(alignment: .leading, spacing: DS.Spacing.m) {
            NavigationLink(value: AgentRoute(id: agent.id)) {
                VStack(alignment: .leading, spacing: DS.Spacing.m) {
                    HStack(alignment: .center) {
                        StateCaption(since: agent.stateSince)
                        Spacer(minLength: DS.Spacing.s)
                        Image(systemName: "chevron.right")
                            .font(.subheadline.weight(.semibold))
                            .foregroundStyle(Theme.accent)
                            .frame(width: DS.Size.openButton, height: DS.Size.openButton)
                            .background(Theme.accentTint)
                            .accessibilityHidden(true)
                    }
                    Text(AgentCopy.headline(agent))
                        .font(Typeface.hero())
                        .foregroundStyle(Theme.text)
                        .fixedSize(horizontal: false, vertical: true)
                    if let command = DecisionOptions.commandLine(agent) { CommandBlock(text: command, compact: false) }
                    if let task = agent.task, !task.isEmpty { YouPanel(task: task, compact: false) }
                    AgentMeta(agent: agent)
                    if rows.isEmpty && agent.state == .question {
                        Text("Open it to answer.").font(.footnote).foregroundStyle(Theme.muted)
                    }
                }
                .frame(maxWidth: .infinity, alignment: .leading)
                .contentShape(.rect)
            }
            .buttonStyle(.plain)
            .accessibilityHint("Opens the agent")

            if !rows.isEmpty {
                VStack(spacing: DS.Spacing.xs) {
                    ForEach(rows) { row in
                        OptionRow(row: row) { decide(agent, row.action) }
                    }
                }
                .disabled(!model.canAnswer(agent))
            }
        }
        .padding(DS.Spacing.l)
        .neonPanel(edge: Theme.accent, edgeWidth: DS.Edge.hero)
        .opacity(model.fleet.isReachable(agent) ? 1 : DecisionHandling.unreachableOpacity)
    }
}

/// "● Needs you · 11m" in signal.
struct StateCaption: View {
    let since: Date?

    var body: some View {
        HStack(spacing: DS.Spacing.s) {
            Circle().fill(Theme.signal).frame(width: DS.Size.captionDot, height: DS.Size.captionDot)
            TimelineView(.everyMinute) { context in
                Text(["Needs you", since.map { Age.short(from: $0, to: context.date) }].compactMap(\.self).joined(separator: " · "))
                    .font(Typeface.mono(12, relativeTo: .caption))
                    .foregroundStyle(Theme.signal)
            }
        }
    }
}
