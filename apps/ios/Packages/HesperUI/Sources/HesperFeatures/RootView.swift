import HesperCore
import HesperDesign
import HesperFixtures
import HesperFleet
import HesperSurfaces
import SwiftUI
import WidgetKit

/// The app: three tabs (Inbox, Agents, Macs) and the mic in the circle
/// next to them, for dropping a task or an idea into Hesper (wireframes
/// round 5). The mic opens capture and is never selected, so the tabs
/// stay navigation only. On iPhone the circle is `CaptureBar`; on iPad
/// it is a "New task" item among the system tabs.
public struct RootView: View {
    enum Tab: Hashable { case inbox, agents, macs, capture }

    @Environment(FleetModel.self) private var model
    @State private var tab = Tab.inbox
    /// The mic's dictation: the tab bar starts it, the sheet carries on.
    @State private var dictation = Dictation()
    @State private var talk = Talk()
    @State private var capture: Capture?
    @State private var kept: KeptTask?

    public init() {}

    public var body: some View {
        TabView(selection: selection) {
            SwiftUI.Tab("Inbox", systemImage: "tray", value: Tab.inbox) {
                NavigationStack {
                    InboxScreen().captureBar(tab: $tab, dictation: dictation, talk: $talk, capture: open).agentDestinations()
                }
            }
            .badge(model.fleet.counts.needsYou)

            SwiftUI.Tab("Agents", systemImage: "square.grid.2x2", value: Tab.agents) {
                NavigationStack {
                    AgentsScreen().captureBar(tab: $tab, dictation: dictation, talk: $talk, capture: open).agentDestinations()
                }
            }

            SwiftUI.Tab("Macs", systemImage: "macbook", value: Tab.macs) {
                NavigationStack { MacsScreen().captureBar(tab: $tab, dictation: dictation, talk: $talk, capture: open) }
            }

            // iPad only: the system tabs show it; selecting it opens capture.
            SwiftUI.Tab("New task", systemImage: "mic.fill", value: Tab.capture) {
                Color.clear
            }
        }
        .tabBarMinimizeBehavior(.onScrollDown)
        .sheet(item: $capture) { c in
            CaptureSheet(dictation: dictation, opening: c.opening) { k in withAnimation { kept = k } }
        }
        .overlay(alignment: .top) {
            if talk.isDown && dictation.isListening {
                TalkPanel(dictation: dictation, away: talk.away)
                    .padding(.horizontal, DS.Spacing.l)
                    .transition(.move(edge: .top).combined(with: .opacity))
            }
        }
        .animation(.snappy, value: talk.isDown && dictation.isListening)
        .overlay(alignment: .bottom) {
            if let kept { KeptToast(kept: kept) { withAnimation { self.kept = nil } } }
        }
        .tint(Theme.accent)
        // The voice model loads before the mic is touched.
        .task { dictation.prepare() }
        .onChange(of: model.fleet.speechHints, initial: true) { _, hints in dictation.hints = hints }
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

/// "Kept on mini · hesper", with Undo, for a few seconds after Keep for
/// later. The idea is safe on the Mac; Undo removes it again.
private struct KeptToast: View {
    /// The floating tab bar's height and margin, so the toast sits above it.
    static let tabBarClearance: CGFloat = 80

    let kept: KeptTask
    let done: () -> Void
    @Environment(FleetModel.self) private var model

    var body: some View {
        HStack(spacing: DS.Spacing.s) {
            Image(systemName: "tray.and.arrow.down").foregroundStyle(Theme.accent)
            Text("Kept on \(model.fleet.macName(kept.machine))\(kept.draft.project.map { " · \($0)" } ?? "")")
                .font(.subheadline)
                .lineLimit(1)
            Spacer(minLength: DS.Spacing.s)
            Button("Undo") {
                Task { await model.discard(kept) }
                done()
            }
            .secondaryAction()
            .controlSize(.small)
        }
        .padding(.leading, DS.Spacing.l)
        .padding(.trailing, DS.Spacing.s)
        .padding(.vertical, DS.Spacing.s)
        .glassEffect()
        .padding(.horizontal, DS.Spacing.l)
        .padding(.bottom, Self.tabBarClearance)
        .transition(.move(edge: .bottom).combined(with: .opacity))
        .task(id: kept.id) {
            try? await Task.sleep(for: .seconds(4))
            done()
        }
    }
}

/// Opens an agent from any list.
struct AgentRoute: Hashable {
    var id: String
}

extension View {
    func agentDestinations() -> some View {
        navigationDestination(for: AgentRoute.self) { AgentScreen(id: $0.id).withoutSystemTabBarOnPhone() }
    }
}

extension RootView {
    /// One capture sheet; a new `id` each time, so it opens fresh.
    struct Capture: Identifiable {
        var opening: CaptureSheet.Opening
        let id = UUID()
    }

    private func open(_ opening: CaptureSheet.Opening) {
        capture = Capture(opening: opening)
    }

    /// The tab selection; choosing the mic opens capture and keeps the tab.
    private var selection: Binding<Tab> {
        Binding {
            tab
        } set: { new in
            if new == .capture {
                dictation.start()
                open(.listening)
            } else {
                tab = new
            }
        }
    }
}

#Preview {
    RootView().fixtureModel()
}
