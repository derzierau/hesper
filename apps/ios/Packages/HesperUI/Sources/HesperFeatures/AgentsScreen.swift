import HesperCore
import HesperDesign
import HesperFleet
import SwiftUI

/// Everything that runs, grouped by project like the Mac's bands. Rows
/// carry the agent's own activity line, never a terminal preview. The
/// search field finds agents by name, project, branch or task (past
/// sessions join with `sessions.search`, concept Core work 5); Later lists
/// the tasks kept on every Mac.
struct AgentsScreen: View {
    /// Opened from Home's menu as Search: the field takes the focus.
    var focusSearch = false
    @Environment(FleetModel.self) private var model
    @State private var query = ""
    @FocusState private var searchFocused: Bool

    var body: some View {
        let groups = model.fleet.groups(matching: query)
        let later = query.isEmpty ? model.fleet.later : []
        List {
            ForEach(groups) { group in
                Section {
                    ForEach(group.agents) { agent in
                        NavigationLink(value: AgentRoute(id: agent.id)) { AgentRow(agent: agent) }
                            .agentSwipeActions(agent)
                            .themedRow()
                    }
                } header: {
                    Text(group.project ?? "Scratch").listHeading()
                }
            }
            if !later.isEmpty {
                Section {
                    ForEach(later) { kept in
                        KeptRow(kept: kept).themedRow()
                    }
                } header: {
                    Text("Later · \(later.count)").listHeading()
                }
            }
        }
        .overlay {
            if !query.isEmpty && groups.isEmpty {
                ContentUnavailableView.search(text: query)
            } else if model.fleet.agents.isEmpty && later.isEmpty {
                ContentUnavailableView("No agents", systemImage: "square.grid.2x2", description: Text("Tap the mic to start one."))
            }
        }
        .searchable(text: $query, prompt: "Agents and projects")
        .tint(Theme.accent)
        .searchFocused($searchFocused)
        .task { if focusSearch { searchFocused = true } }
        .themedList()
        .navigationTitle("Agents")
    }
}

/// A task kept for later: its text, where it would run, and Start. Swipe
/// to throw it away.
private struct KeptRow: View {
    let kept: KeptTask
    @Environment(FleetModel.self) private var model

    var body: some View {
        HStack(spacing: DS.Spacing.m) {
            RoundedRectangle(cornerRadius: 1.5)
                .strokeBorder(Theme.dim, style: StrokeStyle(lineWidth: 1.5, dash: [2, 1.5]))
                .frame(width: DS.markSide, height: DS.markSide)
            VStack(alignment: .leading, spacing: DS.Spacing.xxs) {
                Text(kept.draft.text).lineLimit(2)
                MetaLine(
                    mac: [model.fleet.macName(kept.machine), kept.draft.project].compactMap(\.self).joined(separator: " · "),
                    since: kept.draft.updated ?? kept.draft.created)
            }
            Spacer(minLength: 0)
            Button("Start") { Task { await model.start(kept) } }
                .secondaryAction()
                .controlSize(.small)
        }
        .swipeActions(edge: .trailing) {
            Button("Discard", systemImage: "trash", role: .destructive) { Task { await model.discard(kept) } }.tint(Theme.signal)
        }
    }
}

extension View {
    /// Swipe left: Stop or Close. Swipe right on an exited agent: Resume.
    func agentSwipeActions(_ agent: Agent) -> some View {
        modifier(AgentSwipeActions(agent: agent))
    }
}

private struct AgentSwipeActions: ViewModifier {
    let agent: Agent
    @Environment(FleetModel.self) private var model
    @State private var confirmStop = false

    func body(content: Content) -> some View {
        content
            .swipeActions(edge: .trailing) {
                if agent.isRunning {
                    Button("Stop", systemImage: "stop.fill") { confirmStop = true }.tint(Theme.signal)
                } else {
                    Button("Close", systemImage: "xmark") { send(.close(agent: agent.id)) }.tint(Theme.signal)
                }
            }
            .swipeActions(edge: .leading) {
                if !agent.isRunning {
                    Button("Resume", systemImage: "arrow.clockwise") { send(.resume(agent: agent.id)) }.tint(Theme.accent)
                }
            }
            .confirmationDialog("Stop \(agent.name)?", isPresented: $confirmStop, titleVisibility: .visible) {
                Button("Stop", role: .destructive) { send(.stop(agent: agent.id)) }
            } message: {
                Text("It stops on \(model.fleet.macName(agent.machine)). You can resume it later.")
            }
    }

    private func send(_ command: LinkCommand) {
        Task { await model.perform(command) }
    }
}

#Preview {
    NavigationStack { AgentsScreen() }.fixtureModel()
}
