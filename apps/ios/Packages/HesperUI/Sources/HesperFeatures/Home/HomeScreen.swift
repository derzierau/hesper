import HesperCore
import HesperDesign
import HesperFleet
import SwiftUI

/// Where the menu button goes: the screens the tab bar used to hold.
enum MenuRoute: Hashable, CaseIterable {
    case agents, macs, search, settings

    var title: String {
        switch self {
        case .agents: "Agents"
        case .macs: "Macs"
        case .search: "Search"
        case .settings: "Settings"
        }
    }
}

/// Home: the owner's control console (docs/iphone/neon.md, "Layout:
/// Home"). The scope bar on top, the one decision that matters most, then
/// the task grids. The root of the app's navigation stack.
struct HomeScreen: View {
    /// When the app last went to the background, for the notifications
    /// card.
    var awaySince: Date?
    /// Pushes a screen from the menu.
    let open: (MenuRoute) -> Void
    @Environment(FleetModel.self) private var model
    @State private var scope: HomeScope

    init(awaySince: Date? = nil, open: @escaping (MenuRoute) -> Void) {
        self.awaySince = awaySince
        self.open = open
        var scope = HomeScope.all
        #if DEBUG
            // `HESPER_HOME_SCOPE=needsYou` (or `working`, `all`) opens on
            // that scope, for screenshots in a headless simulator.
            if let name = ProcessInfo.processInfo.environment["HESPER_HOME_SCOPE"],
                let named = HomeScope.allCases.first(where: { "\($0)" == name })
            {
                scope = named
            }
        #endif
        _scope = State(initialValue: scope)
    }

    var body: some View {
        let fleet = model.fleet
        let sections = fleet.home(scope)
        ScrollView {
            LazyVStack(alignment: .leading, spacing: DS.Spacing.xl) {
                if let priority = sections.priority { PriorityCard(agent: priority) }
                TaskGrid(title: "Needs you", agents: sections.needsYou, kind: .needsYou)
                TaskGrid(title: "Ready to review", agents: sections.readyToReview, kind: .readyToReview)
                TaskGrid(title: "Working", agents: sections.working, kind: .working)
            }
            .padding(.horizontal, DS.Spacing.xl)
            .padding(.vertical, DS.Spacing.xl)
        }
        .overlay {
            if sections.isEmpty { empty(working: fleet.counts.working) }
        }
        .background(Theme.background)
        .modifier(NotificationsOffCard(awaySince: awaySince))
        .safeAreaInset(edge: .top, spacing: 0) {
            ScopeBar(scope: $scope, counts: fleet.homeCounts, menu: open)
        }
        .decisions()
        .toolbar(.hidden, for: .navigationBar)
        .navigationDestination(for: MenuRoute.self) { route in
            switch route {
            case .agents: AgentsScreen()
            case .macs: MacsScreen()
            case .search: AgentsScreen(focusSearch: true)
            case .settings: SettingsScreen()
            }
        }
    }

    /// Ole's empty copy; the Working scope says what it lacks.
    private func empty(working: Int) -> some View {
        Group {
            if scope == .working {
                ContentUnavailableView(
                    "Nothing is working", systemImage: "square.grid.2x2",
                    description: Text("Tap the mic to start an agent."))
            } else {
                ContentUnavailableView(
                    "Nothing needs you",
                    systemImage: "tray",
                    description: Text(
                        working > 0
                            ? "\(working) agents are working. You'll get a notification when one asks for you."
                            : "Tap the mic to start an agent.")
                )
            }
        }
        .foregroundStyle(Theme.muted)
    }
}

#Preview {
    NavigationStack { HomeScreen { _ in } }.fixtureModel()
}
