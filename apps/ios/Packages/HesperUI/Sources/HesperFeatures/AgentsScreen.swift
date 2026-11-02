import HesperCore
import HesperDesign
import HesperFleet
import SwiftUI

/// Everything that runs, grouped by project like the Mac's bands. Rows
/// carry the agent's own activity line, never a terminal preview.
struct AgentsScreen: View {
    @Environment(FleetModel.self) private var model

    var body: some View {
        List {
            ForEach(model.fleet.groups) { group in
                Section(group.project ?? "Scratch") {
                    ForEach(group.agents) { agent in
                        NavigationLink(value: AgentRoute(id: agent.id)) { AgentRow(agent: agent) }
                            .agentSwipeActions(agent)
                            .themedRow()
                    }
                }
            }
        }
        .overlay {
            if model.fleet.agents.isEmpty {
                ContentUnavailableView("No agents", systemImage: "square.grid.2x2", description: Text("Start one from the bar below."))
            }
        }
        .themedList()
        .navigationTitle("Agents")
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
                    Button("Stop", systemImage: "stop.fill") { confirmStop = true }.tint(Theme.error)
                } else {
                    Button("Close", systemImage: "xmark") { send(.close(agent: agent.id)) }
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
