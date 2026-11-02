import HesperDesign
import HesperFixtures
import HesperFleet
import HesperSurfaces
import SwiftUI
import WidgetKit

/// The app: four tabs (Inbox, Agents, Macs, Search) and New agent as the
/// bottom accessory on every tab (docs/iphone/concept.md, "Information
/// architecture"). Tabs navigate; they are never actions.
public struct RootView: View {
    enum Tab: Hashable { case inbox, agents, macs, search }

    @Environment(FleetModel.self) private var model
    @State private var tab = Tab.inbox
    @State private var newAgent = false

    public init() {}

    public var body: some View {
        TabView(selection: $tab) {
            SwiftUI.Tab("Inbox", systemImage: "tray", value: Tab.inbox) {
                NavigationStack { InboxScreen().agentDestinations() }
            }
            .badge(model.fleet.counts.needsYou)

            SwiftUI.Tab("Agents", systemImage: "square.grid.2x2", value: Tab.agents) {
                NavigationStack { AgentsScreen().agentDestinations() }
            }

            SwiftUI.Tab("Macs", systemImage: "macbook", value: Tab.macs) {
                NavigationStack { MacsScreen() }
            }

            SwiftUI.Tab(value: Tab.search, role: .search) {
                NavigationStack { SearchScreen().agentDestinations() }
            }
        }
        .tabBarMinimizeBehavior(.onScrollDown)
        .tabViewBottomAccessory {
            Button {
                newAgent = true
            } label: {
                Label("New agent", systemImage: "plus")
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
            .padding(.horizontal, DS.Spacing.l)
        }
        .sheet(isPresented: $newAgent) { NewAgentSheet() }
        .tint(Theme.accent)
        .onChange(of: model.fleet.counts, initial: true) { _, counts in
            SharedCounts().write(counts)
            WidgetCenter.shared.reloadTimelines(ofKind: "fleet")
        }
        .alert(
            "Not sent",
            isPresented: Binding {
                model.failure != nil
            } set: {
                if !$0 { model.failure = nil }
            }
        ) {
            Button("OK", role: .cancel) {}
        } message: {
            Text(model.failure ?? "")
        }
    }
}

/// Opens an agent from any list.
struct AgentRoute: Hashable {
    var id: String
}

extension View {
    func agentDestinations() -> some View {
        navigationDestination(for: AgentRoute.self) { AgentScreen(id: $0.id) }
    }
}

#Preview {
    RootView().fixtureModel()
}
