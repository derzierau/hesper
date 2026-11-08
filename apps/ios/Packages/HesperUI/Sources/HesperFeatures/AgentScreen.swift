import HesperCore
import HesperDesign
import HesperFleet
import SwiftUI

/// The agent view: the live terminal, and under it a dock that follows the
/// state (concept, flow 4). In landscape the dock is a column on the right.
struct AgentScreen: View {
    let id: String
    @Environment(FleetModel.self) private var model
    /// The terminal, shared with the dock.
    @State private var watch = TerminalWatch()

    /// The dock's width as a column, in landscape.
    private static let dockColumn: CGFloat = 320

    var body: some View {
        if let agent = model.agent(id) {
            GeometryReader { geometry in
                let wide = geometry.size.width > geometry.size.height
                // One layout for both, so turning the phone keeps the
                // terminal (and its attach) instead of making a new one.
                let layout = wide ? AnyLayout(HStackLayout(spacing: 0)) : AnyLayout(VStackLayout(spacing: 0))
                layout {
                    TerminalView(agent: agent, watch: watch)
                    if wide {
                        Dock(agent: agent)
                            .frame(width: Self.dockColumn)
                            .frame(maxHeight: .infinity, alignment: .bottom)
                            .background(Theme.surface, ignoresSafeAreaEdges: .bottom)
                            .overlay(alignment: .leading) { Rectangle().fill(Theme.line).frame(width: DS.Edge.line) }
                    } else {
                        Dock(agent: agent)
                    }
                }
            }
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

/// The dock under the terminal, a Neon bar: the priority card's numbered
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
        .neonBar()
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
