import HesperCore
import HesperDesign
import HesperFleet
import SwiftUI

/// Which grid a card is in; it decides the title and the buttons.
enum HomeSectionKind {
    case needsYou, readyToReview, working
}

/// A heading with its count and a two-column grid of cards (three on
/// regular width). Nothing when it has no agents.
struct TaskGrid: View {
    let title: String
    let agents: [Agent]
    let kind: HomeSectionKind
    @Environment(\.horizontalSizeClass) private var size

    var body: some View {
        if !agents.isEmpty {
            VStack(alignment: .leading, spacing: DS.Spacing.m) {
                HStack(alignment: .firstTextBaseline) {
                    Text(title).font(Typeface.heading()).foregroundStyle(Theme.text)
                    Spacer()
                    Text("\(agents.count)")
                        .font(Typeface.mono(20, relativeTo: .title3))
                        .foregroundStyle(kind == .needsYou ? Theme.signal : Theme.muted)
                }
                .accessibilityElement(children: .combine)
                .accessibilityAddTraits(.isHeader)
                LazyVGrid(
                    columns: Array(repeating: GridItem(.flexible(), spacing: DS.Spacing.grid), count: size == .regular ? 3 : 2),
                    spacing: DS.Spacing.grid
                ) {
                    ForEach(agents) { TaskCard(agent: $0, section: kind) }
                }
            }
        }
    }
}

/// One agent in a grid: title, what it asks or did, the owner's task, the
/// meta line and small action buttons. The body opens the agent.
struct TaskCard: View {
    let agent: Agent
    let section: HomeSectionKind
    @Environment(FleetModel.self) private var model
    @Environment(\.decide) private var decide

    var body: some View {
        VStack(alignment: .leading, spacing: DS.Spacing.xs) {
            NavigationLink(value: AgentRoute(id: agent.id)) {
                VStack(alignment: .leading, spacing: DS.Spacing.xs) {
                    title
                    detail
                    AgentMeta(agent: agent, age: age)
                }
                .frame(maxWidth: .infinity, alignment: .leading)
                .contentShape(.rect)
            }
            .buttonStyle(.plain)
            Spacer(minLength: DS.Spacing.xs)
            HStack(spacing: DS.Spacing.s) {
                ForEach(DecisionOptions.gridActions(for: agent)) { button($0) }
            }
            .controlSize(.small)
            .font(.subheadline)
            .disabled(!model.canAnswer(agent))
        }
        .padding(DS.Spacing.m)
        .frame(maxHeight: .infinity, alignment: .top)
        .neonPanel()
        .opacity(model.fleet.isReachable(agent) ? 1 : DecisionHandling.unreachableOpacity)
    }

    private var title: some View {
        Group {
            if section == .needsYou {
                Text("\(Text("! ").foregroundStyle(Theme.signal))\(AgentCopy.headline(agent))")
            } else {
                Text(agent.name)
            }
        }
        .font(Typeface.cardTitle())
        .foregroundStyle(Theme.text)
        .lineLimit(3)
        .fixedSize(horizontal: false, vertical: true)
    }

    @ViewBuilder private var detail: some View {
        switch section {
        case .needsYou:
            if let command = DecisionOptions.commandLine(agent) { CommandBlock(text: command, compact: true) }
            if let task = agent.task, !task.isEmpty { YouPanel(task: task, compact: true) }
        case .readyToReview:
            if let summary = agent.summary, !summary.isEmpty {
                Text(summary).font(.caption).foregroundStyle(Theme.text).lineLimit(3)
            }
        case .working:
            Text(AgentCopy.headline(agent)).font(.caption).foregroundStyle(Theme.text).lineLimit(2)
        }
    }

    /// The meta line's last word: how long it has waited or worked.
    private var age: String {
        switch section {
        case .needsYou: "waiting"
        case .working: "working"
        case .readyToReview: agent.state == .idle ? "idle" : "done"
        }
    }

    /// Open is a link (it pushes); everything else goes through `decide`.
    @ViewBuilder private func button(_ row: DecisionRow) -> some View {
        let label = Text(row.label).lineLimit(1).frame(maxWidth: .infinity)
        switch row.action {
        case .open:
            NavigationLink(value: AgentRoute(id: agent.id)) { label }.primaryAction()
        case .allow, .resume:
            Button {
                decide(agent, row.action)
            } label: {
                label
            }.primaryAction()
        default:
            if row.isDestructive {
                Button {
                    decide(agent, row.action)
                } label: {
                    label
                }.destructiveAction()
            } else {
                Button {
                    decide(agent, row.action)
                } label: {
                    label
                }.secondaryAction()
            }
        }
    }
}

/// A command or file in mono accent on the background, with a line edge
/// on the left; long ones scroll sideways.
struct CommandBlock: View {
    let text: String
    let compact: Bool

    var body: some View {
        ScrollView(.horizontal, showsIndicators: false) {
            Text(text)
                .font(compact ? Typeface.mono(10, relativeTo: .caption2) : Typeface.mono(12, relativeTo: .caption))
                .foregroundStyle(Theme.accent)
                .lineLimit(1)
                .padding(.horizontal, compact ? DS.Spacing.m : DS.Spacing.l)
                .padding(.vertical, compact ? DS.Spacing.s : DS.Spacing.m)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(Theme.background)
        .overlay(alignment: .leading) { Rectangle().fill(Theme.line).frame(width: DS.Edge.command) }
    }
}

/// "You  Add the APNs provider to the relay": the task the owner gave.
struct YouPanel: View {
    let task: String
    let compact: Bool

    var body: some View {
        HStack(alignment: .firstTextBaseline, spacing: DS.Spacing.m) {
            Text("You")
                .font(compact ? .caption2.weight(.semibold) : .footnote.weight(.semibold))
                .foregroundStyle(Theme.accent)
            Text(task)
                .font(compact ? .caption : .subheadline)
                .foregroundStyle(Theme.text)
                .lineLimit(compact ? 2 : 3)
        }
        .padding(.horizontal, compact ? DS.Spacing.m : DS.Spacing.l)
        .padding(.vertical, compact ? DS.Spacing.s : DS.Spacing.m)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(Theme.accentTint)
    }
}

/// "push-provider · hesper · mini · ⌥ push/provider", breaking only after
/// a separator; with `age`, "· waiting 8m" at the end.
struct AgentMeta: View {
    let agent: Agent
    var age: String?
    @Environment(FleetModel.self) private var model

    var body: some View {
        TimelineView(.everyMinute) { context in
            Text(line(now: context.date))
                .font(.caption2)
                .foregroundStyle(Theme.muted)
                .fixedSize(horizontal: false, vertical: true)
        }
    }

    private func line(now: Date) -> String {
        var parts = [agent.name, agent.project, model.fleet.macName(agent.machine), agent.branch.map { "⌥ \($0)" }]
        if let age, let since = agent.stateSince { parts.append("\(age) \(Age.short(from: since, to: now))") }
        // Non-breaking spaces inside a part: lines break at the separators.
        return parts.compactMap(\.self).filter { !$0.isEmpty }
            .map { $0.replacingOccurrences(of: " ", with: "\u{00A0}") }
            .joined(separator: " · ")
    }
}

/// "11m", "2h", "3d": how long ago, in one unit.
enum Age {
    static func short(from: Date, to now: Date) -> String {
        Duration.seconds(max(0, now.timeIntervalSince(from)))
            .formatted(.units(allowed: [.days, .hours, .minutes], width: .narrow, maximumUnitCount: 1))
    }
}
