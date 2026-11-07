import HesperCore
import HesperDesign
import HesperFleet
import SwiftUI

/// The agent view: the live terminal, and under it a dock that follows the
/// state (concept, flow 4). The terminal is a placeholder until the mobile
/// link library opens `agents.attach` and libghostty draws it.
struct AgentScreen: View {
    let id: String
    @Environment(FleetModel.self) private var model

    var body: some View {
        if let agent = model.agent(id) {
            TerminalPlaceholder(agent: agent)
                .safeAreaInset(edge: .bottom) { Dock(agent: agent) }
                .navigationTitle(agent.name)
                .navigationSubtitle(subtitle(agent))
                .toolbarTitleDisplayMode(.inline)
        } else {
            ContentUnavailableView("Agent closed", systemImage: "xmark.square", description: Text("It is no longer on any Mac."))
        }
    }

    private func subtitle(_ a: Agent) -> String {
        [model.fleet.macName(a.machine), a.project, a.branch].compactMap(\.self).joined(separator: " · ")
    }
}

/// Where the terminal goes: bottom-anchored, edge to edge, always dark.
private struct TerminalPlaceholder: View {
    let agent: Agent

    var body: some View {
        VStack(alignment: .leading, spacing: DS.Spacing.s) {
            Spacer()
            if let size = agent.size {
                Text("Mac's size · \(size.cols) × \(size.rows)")
                    .font(.caption2.monospaced())
                    .foregroundStyle(Theme.dim)
            }
            Text(AgentCopy.headline(agent))
                .font(.system(.footnote, design: .monospaced))
                .foregroundStyle(Theme.text)
            Text("The live terminal arrives with the mobile link library.")
                .font(.caption2.monospaced())
                .foregroundStyle(Theme.dim)
        }
        .padding(DS.Spacing.l)
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .bottomLeading)
        .background(Theme.background)
        .environment(\.colorScheme, .dark)
    }
}

/// The dock under the terminal, a Neon panel: the priority card's numbered
/// options when the agent needs you (a risky Yes asks first, No… opens the
/// deny sheet), a reply field while it works, Resume once it exited.
private struct Dock: View {
    let agent: Agent
    @Environment(FleetModel.self) private var model
    @State private var reply = ""

    var body: some View {
        VStack(alignment: .leading, spacing: DS.Spacing.m) {
            switch agent.state {
            case .approval, .question:
                HStack(spacing: DS.Spacing.s) {
                    StateMark(agent.state)
                    Text(AgentCopy.headline(agent)).font(.subheadline).foregroundStyle(Theme.text).lineLimit(2)
                }
                DockOptions(agent: agent)
                if let next = model.fleet.next(after: agent.id) {
                    NavigationLink(value: AgentRoute(id: next.id)) {
                        Label("Next: \(next.name) on \(model.fleet.macName(next.machine))", systemImage: "chevron.right")
                            .font(.footnote)
                            .foregroundStyle(Theme.accent)
                    }
                }
            case .exited, .error:
                HStack(spacing: DS.Spacing.s) {
                    Button("Resume") { send(.resume(agent: agent.id)) }.primaryAction()
                    Button("Close") { send(.close(agent: agent.id)) }.destructiveAction()
                }
                .frame(maxWidth: .infinity)
            default:
                HStack(spacing: DS.Spacing.s) {
                    TextField("Message \(agent.name)", text: $reply, axis: .vertical)
                        .lineLimit(1...4)
                        .textFieldStyle(.plain)
                        .padding(.horizontal, DS.Spacing.l)
                        .padding(.vertical, DS.Spacing.m)
                        .frame(minHeight: DS.Size.control)
                        .background(Theme.surface)
                        .overlay {
                            RoundedRectangle(cornerRadius: DS.Radius.console).strokeBorder(Theme.line, lineWidth: DS.Edge.line)
                        }
                    Button("Send", systemImage: "arrow.up") {
                        send(.reply(agent: agent.id, text: reply))
                        reply = ""
                    }
                    .labelStyle(.iconOnly)
                    .font(.body.weight(.semibold))
                    .primaryAction(square: true)
                    .disabled(reply.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
                }
            }
        }
        .controlSize(.large)
        .disabled(model.pending.contains(agent.id) || !model.fleet.isReachable(agent))
        .padding(DS.Spacing.l)
        .frame(maxWidth: .infinity, alignment: .leading)
        .neonPanel()
        .decisions()
    }

    private func send(_ command: LinkCommand) {
        Task { await model.perform(command) }
    }
}

/// The dock's numbered options, from `DecisionOptions.rows(for:)` like the
/// priority card's; the dock's `decisions()` confirms and sends them.
private struct DockOptions: View {
    let agent: Agent
    @Environment(\.decide) private var decide

    var body: some View {
        let rows = DecisionOptions.rows(for: agent)
        if rows.isEmpty {
            Text("Open it to answer.").font(.footnote).foregroundStyle(Theme.muted)
        } else {
            VStack(spacing: DS.Spacing.xs) {
                ForEach(rows) { row in
                    OptionRow(row: row) { decide(agent, row.action) }
                }
            }
        }
    }
}

#Preview("Needs you") {
    NavigationStack { AgentScreen(id: "studio/s1") }.fixtureModel()
}

#Preview("Working") {
    NavigationStack { AgentScreen(id: "mini/m2") }.fixtureModel()
}
