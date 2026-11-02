import HesperCore
import HesperDesign
import HesperFleet
import SwiftUI

/// The search tab: agents by name, project, branch or task. Past sessions
/// join when `sessions.search` works over the relay (concept, Core work 5).
struct SearchScreen: View {
    @Environment(FleetModel.self) private var model
    @State private var query = ""

    var body: some View {
        List(results) { agent in
            NavigationLink(value: AgentRoute(id: agent.id)) { AgentRow(agent: agent) }.themedRow()
        }
        .overlay {
            if !query.isEmpty && results.isEmpty {
                ContentUnavailableView.search(text: query)
            }
        }
        .searchable(text: $query, prompt: "Agents and projects")
        .themedList()
        .navigationTitle("Search")
    }

    private var results: [Agent] {
        let q = query.trimmingCharacters(in: .whitespaces)
        let all = model.fleet.groups.flatMap(\.agents)
        guard !q.isEmpty else { return all }
        return all.filter { a in
            [a.name, a.project, a.branch, a.task].compactMap(\.self).contains { $0.localizedStandardContains(q) }
        }
    }
}

#Preview {
    NavigationStack { SearchScreen() }.fixtureModel()
}
