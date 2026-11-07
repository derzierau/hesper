import HesperCore
import HesperDesign
import HesperFixtures
import HesperFleet
import HesperSurfaces
import SwiftUI
import WidgetKit

/// The app: Home, the owner's control console, at the root of one
/// navigation stack (docs/iphone/neon.md). Its menu pushes Agents, Macs,
/// Search and Settings; the Speak / Type console sits under Home.
public struct RootView: View {
    @Environment(FleetModel.self) private var model
    @State private var path = NavigationPath()
    /// Speak's dictation: the console starts it, the sheet carries on.
    @State private var dictation = Dictation()
    @State private var talk = Talk()
    @State private var capture: Capture?
    @State private var kept: KeptTask?
    /// When the app last went to the background, for Home's
    /// notifications card.
    @State private var awaySince: Date?
    @Environment(\.scenePhase) private var scenePhase

    public init() {
        #if DEBUG
            // `HESPER_CAPTURE=listening` (or `typing`) opens the capture
            // sheet at launch, for screenshots in a headless simulator,
            // where nobody can press Speak. `listening` shows a sample
            // transcript without the microphone.
            switch ProcessInfo.processInfo.environment["HESPER_CAPTURE"] {
            case "listening":
                let dictation = Dictation()
                dictation.pretend("Add a dark mode toggle to the settings screen")
                _dictation = State(initialValue: dictation)
                _capture = State(initialValue: Capture(opening: .listening))
            case "typing":
                _capture = State(initialValue: Capture(opening: .typing))
            default: break
            }
            // `HESPER_OPEN=agent:<id>` (or `agents`, `macs`, `settings`)
            // pushes that screen at launch, for the same screenshots.
            if let open = ProcessInfo.processInfo.environment["HESPER_OPEN"] {
                var path = NavigationPath()
                if open.hasPrefix("agent:") {
                    path.append(AgentRoute(id: String(open.dropFirst("agent:".count))))
                } else if let route = MenuRoute.allCases.first(where: { "\($0)" == open }) {
                    path.append(route)
                }
                _path = State(initialValue: path)
            }
        #endif
    }

    public var body: some View {
        NavigationStack(path: $path) {
            HomeScreen(awaySince: awaySince) { path.append($0) }
                .console(dictation: dictation, talk: $talk, capture: open)
                .agentDestinations()
        }
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
        .preferredColorScheme(.dark)
        .onChange(of: scenePhase) { _, phase in
            if phase == .background { awaySince = .now }
        }
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
    /// The console's height, so the toast sits above it.
    static let consoleClearance: CGFloat = DS.Size.console + DS.Spacing.l + DS.Spacing.m

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
        .neonPanel()
        .padding(.horizontal, DS.Spacing.l)
        .padding(.bottom, Self.consoleClearance)
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
}

#Preview {
    RootView().fixtureModel()
}
