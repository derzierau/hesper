import HesperCore
import HesperDesign
import HesperFleet
import HesperSurfaces
import HesperTerminal
import SwiftUI

/// The agent view: a scrolling transcript and live output above its fixed
/// terminal input, or the raw terminal from the menu. Decision actions
/// stay in a dock (a right column in landscape).
struct AgentScreen: View {
    let id: String
    @Environment(FleetModel.self) private var model
    /// Landscape on a phone. Not the area's shape: with the keyboard up
    /// the area is wider than tall in portrait too, and the dock must
    /// not jump to the side then.
    @Environment(\.verticalSizeClass) private var verticalSizeClass
    /// The raw terminal, shared with the dock. Made on first use: a
    /// `@State` initial value is built on every init of the view, and a
    /// terminal allocates a libghostty surface.
    @State private var holder = WatchHolder()
    private var watch: TerminalWatch { holder.watch }
    @State private var store = TranscriptStore()
    /// The transcript view's live section.
    @State private var screen = ScreenWatch()

    /// The dock's width as a column, in landscape.
    private static let dockColumn: CGFloat = 320

    /// The menu's "Raw terminal": the libghostty terminal in place of the
    /// transcript view, remembered per agent (`FleetModel.rawTerminal`).
    private var raw: Bool { model.rawTerminal.contains(id) }

    var body: some View {
        if let agent = model.agent(id) {
            GeometryReader { _ in
                let wide = verticalSizeClass == .compact
                // One layout for both, so turning the phone keeps the
                // terminal (and its attach) instead of making a new one.
                let layout = wide ? AnyLayout(HStackLayout(spacing: 0)) : AnyLayout(VStackLayout(spacing: 0))
                layout {
                    if raw {
                        TerminalView(agent: agent, watch: watch)
                    } else {
                        TranscriptView(agent: agent, store: store, screen: screen)
                    }
                    if raw || agent.state.needsAttention || agent.state == .exited {
                        if wide {
                            Dock(agent: agent, raw: raw, send: send, canSend: watch.canType)
                                .frame(width: Self.dockColumn)
                                .frame(maxHeight: .infinity, alignment: .bottom)
                                .background(Theme.surface, ignoresSafeAreaEdges: .bottom)
                                .overlay(alignment: .leading) { Rectangle().fill(Theme.line).frame(width: DS.Edge.line) }
                        } else {
                            Dock(agent: agent, raw: raw, send: send, canSend: watch.canType)
                        }
                    }
                }
            }
            .navigationTitle(agent.name)
            .navigationSubtitle(subtitle(agent))
            .toolbarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .topBarTrailing) {
                    Menu {
                        Toggle("Raw terminal", isOn: rawBinding)
                    } label: {
                        Label("More", systemImage: "ellipsis")
                    }
                }
            }
            // One attach per agent on this phone: the raw terminal opens
            // its own, so the transcript stream and the live section's
            // attach close while it is shown and open again after.
            .task(id: raw) {
                guard !raw else { return }
                await store.run(agent, model: model)
            }
            .task(id: raw) {
                guard !raw else {
                    screen.stop()
                    return
                }
                await screen.run(agent, model: model)
            }
        } else {
            ContentUnavailableView("Agent closed", systemImage: "xmark.square", description: Text("It is no longer on any Mac."))
        }
    }

    private var rawBinding: Binding<Bool> {
        Binding {
            model.rawTerminal.contains(id)
        } set: { on in
            if on { model.rawTerminal.insert(id) } else { model.rawTerminal.remove(id) }
        }
    }

    /// Raw Terminal's Send button presses Enter.
    private func send() { watch.press(.enter) }

    private func subtitle(_ a: Agent) -> String {
        [model.fleet.macName(a.machine), a.project, a.branch].compactMap(\.self).joined(separator: " · ")
    }
}

/// Holds an agent screen's terminal, made on first use. Cheap to build,
/// so SwiftUI may build and drop it on every init of the view.
@MainActor private final class WatchHolder {
    lazy var watch = TerminalWatch()
}

/// The dock under the terminal, a Neon bar: the priority card's numbered
/// options when the agent needs you (a risky Yes asks first, No… opens the
/// deny sheet), Send in Raw Terminal, and Resume once it exited.
private struct Dock: View {
    let agent: Agent
    let raw: Bool
    /// Press Enter in Raw Terminal.
    let send: () -> Void
    /// The raw terminal's attach may type.
    let canSend: Bool
    @Environment(FleetModel.self) private var model

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
                    Button("Resume") { perform(.resume(agent: agent.id)) }.primaryAction()
                    Button("Close") { perform(.close(agent: agent.id)) }.destructiveAction()
                }
                .frame(maxWidth: .infinity)
            default:
                if raw {
                    Button {
                        send()
                    } label: {
                        Label("Send", systemImage: "arrow.up").frame(maxWidth: .infinity)
                    }
                    .font(.body.weight(.semibold))
                    .primaryAction()
                    .disabled(!canSend)
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

    private func perform(_ command: LinkCommand) {
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
