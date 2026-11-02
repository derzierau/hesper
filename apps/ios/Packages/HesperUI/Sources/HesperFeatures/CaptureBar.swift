import HesperDesign
import HesperFleet
import SwiftUI
import TipKit

/// The iPhone's tab bar (wireframes round 5): the three tabs in a glass
/// capsule and the mic in its own circle next to them. The system bar has
/// no slot for an action, so on compact width this one replaces it under
/// the three root screens; pushed screens such as the agent view have no
/// bar. iPad keeps the system tabs.
///
/// The mic listens from the moment it is touched. A tap opens the capture
/// sheet still listening; holding is push-to-talk: `TalkPanel`, at the top
/// of the screen, shows what is heard, letting go opens the sheet with it, and sliding off
/// the circle before letting go throws it away.
struct CaptureBar: View {
    @Binding var tab: RootView.Tab
    let dictation: Dictation
    /// The press on the mic, shared with the root view's `TalkPanel`.
    @Binding var talk: Talk
    let capture: (CaptureSheet.Opening) -> Void
    @Environment(FleetModel.self) private var model
    @Namespace private var glass
    /// The press is down. iOS cancels it without an end, for one when the
    /// microphone permission alert appears; then it stops listening.
    @GestureState private var touching = false

    var body: some View {
        let holding = talk.isDown && dictation.isListening
        let away = talk.away
        GlassEffectContainer(spacing: DS.Spacing.m) {
            HStack(spacing: DS.Spacing.m) {
                HStack(spacing: 0) {
                    item(.inbox, "Inbox", "tray", badge: model.fleet.counts.needsYou)
                    item(.agents, "Agents", "square.grid.2x2")
                    item(.macs, "Macs", "macbook")
                }
                .padding(DS.Spacing.xs)
                .glassEffect(.regular.interactive(), in: .capsule)
                .glassEffectID("tabs", in: glass)

                Image(systemName: away ? "xmark" : "mic.fill")
                    .font(.title2)
                    .foregroundStyle(Theme.background)
                    .frame(width: Self.circle, height: Self.circle)
                    .glassEffect(.regular.tint(away ? Theme.dim : Theme.accent).interactive(), in: .circle)
                    .glassEffectID("mic", in: glass)
                    .popoverTip(MicTip(), arrowEdge: .bottom)
                    .scaleEffect(holding ? 1.12 : 1)
                    .animation(.snappy, value: holding)
                    .contentShape(.circle)
                    .gesture(press)
                    .onChange(of: touching) { _, down in
                        if !down, talk.isDown {
                            talk = Talk()
                            dictation.cancel()
                        }
                    }
                    .sensoryFeedback(.start, trigger: talk.isDown) { _, down in down }
                    .sensoryFeedback(.warning, trigger: talk.away) { _, isAway in isAway }
                    .accessibilityElement()
                    .accessibilityLabel("New task")
                    .accessibilityHint("Say a task. Hold to talk, let go to review it.")
                    .accessibilityAddTraits(.isButton)
                    .accessibilityAction {
                        dictation.start()
                        capture(.listening)
                    }
                    .accessibilityAction(named: "Type a task") { capture(.typing) }
            }
        }
        .padding(.horizontal, DS.Spacing.xl)
        .padding(.bottom, DS.Spacing.xs)
    }

    /// The mic circle's side, the height of the capsule beside it.
    private static let circle: CGFloat = 62
    /// Shorter than this is a tap.
    private static let tap: TimeInterval = 0.35
    /// How far the finger may move off the circle's centre before letting
    /// go cancels.
    private static let cancelDistance: CGFloat = 80

    /// Touch down starts listening; letting go decides tap, talk or cancel.
    private var press: some Gesture {
        DragGesture(minimumDistance: 0)
            .updating($touching) { _, down, _ in down = true }
            .onChanged { drag in
                if !talk.isDown {
                    talk.pressed = .now
                    dictation.start()
                }
                talk.away = hypot(drag.translation.width, drag.translation.height) > Self.cancelDistance
            }
            .onEnded { _ in
                let held = Date.now.timeIntervalSince(talk.pressed ?? .now)
                let cancelled = talk.away
                talk = Talk()
                if cancelled {
                    dictation.cancel()
                } else if held < Self.tap {
                    capture(.listening)
                } else {
                    Task {
                        let said = await dictation.stop()
                        capture(said.isEmpty ? .typing : .said(said))
                    }
                }
            }
    }

    private func item(_ value: RootView.Tab, _ title: String, _ image: String, badge: Int = 0) -> some View {
        Button {
            tab = value
        } label: {
            VStack(spacing: DS.Spacing.xxs) {
                Image(systemName: image).font(.title3)
                Text(title).font(.caption2.weight(.medium))
            }
            .frame(maxWidth: .infinity, minHeight: Self.circle - 2 * DS.Spacing.xs)
            .overlay(alignment: .top) {
                if badge > 0 {
                    Text("\(badge)")
                        .font(.caption2.bold())
                        .foregroundStyle(Theme.background)
                        .padding(.horizontal, DS.Spacing.xs)
                        .background(Theme.signal, in: .capsule)
                        .offset(x: DS.Spacing.l)
                }
            }
            .foregroundStyle(tab == value ? Theme.accent : Theme.text)
            .background {
                if tab == value { Capsule().fill(Theme.text.opacity(0.08)) }
            }
            .contentShape(.capsule)
        }
        .buttonStyle(.plain)
        .accessibilityAddTraits(tab == value ? .isSelected : [])
    }
}

/// A press on the mic.
struct Talk: Equatable {
    /// When the finger came down; nil when it is up.
    var pressed: Date?
    /// The finger left the circle: letting go now cancels.
    var away = false
    var isDown: Bool { pressed != nil }
}

/// What push-to-talk hears, at the top of the screen while the mic is held,
/// clear of the thumb on the bar.
struct TalkPanel: View {
    let dictation: Dictation
    let away: Bool

    var body: some View {
        VStack(alignment: .leading, spacing: DS.Spacing.s) {
            HStack(spacing: DS.Spacing.s) {
                Image(systemName: away ? "xmark.circle" : "waveform")
                    .symbolEffect(.variableColor.iterative, isActive: !away)
                    .foregroundStyle(away ? Theme.dim : Theme.accent)
                Text(away ? "Let go to cancel" : "Listening").font(.headline)
                Spacer()
                if case .listening(let since) = dictation.phase {
                    Text(since, style: .timer).font(.footnote.monospacedDigit()).foregroundStyle(Theme.dim)
                }
            }
            if dictation.settled.isEmpty && dictation.guess.isEmpty {
                Text("Say the task. Let go to review it, slide away to cancel.")
                    .font(.subheadline)
                    .foregroundStyle(Theme.dim)
            } else {
                Text("\(dictation.settled)\(Text(dictation.guess).foregroundStyle(Theme.dim))")
                    .font(.body)
                    .lineLimit(5)
            }
        }
        .padding(DS.Spacing.l)
        .frame(maxWidth: .infinity, alignment: .leading)
        .glassEffect(.regular, in: .rect(cornerRadius: DS.Spacing.xxl))
        .opacity(away ? 0.6 : 1)
    }
}

extension View {
    /// On compact width: the capture bar under this root screen instead of
    /// the system tab bar.
    func captureBar(
        tab: Binding<RootView.Tab>, dictation: Dictation, talk: Binding<Talk>,
        capture: @escaping (CaptureSheet.Opening) -> Void
    ) -> some View {
        modifier(CaptureBarModifier(tab: tab, dictation: dictation, talk: talk, capture: capture))
    }

    /// No system tab bar on compact width (the capture bar replaces it on
    /// root screens; pushed screens have none).
    func withoutSystemTabBarOnPhone() -> some View {
        modifier(HideSystemTabBar())
    }
}

private struct CaptureBarModifier: ViewModifier {
    @Binding var tab: RootView.Tab
    let dictation: Dictation
    @Binding var talk: Talk
    let capture: (CaptureSheet.Opening) -> Void
    @Environment(\.horizontalSizeClass) private var size

    func body(content: Content) -> some View {
        if size == .compact {
            content
                .safeAreaInset(edge: .bottom) { CaptureBar(tab: $tab, dictation: dictation, talk: $talk, capture: capture) }
                .toolbarVisibility(.hidden, for: .tabBar)
        } else {
            content
        }
    }
}

private struct HideSystemTabBar: ViewModifier {
    @Environment(\.horizontalSizeClass) private var size

    func body(content: Content) -> some View {
        content.toolbarVisibility(size == .compact ? .hidden : .automatic, for: .tabBar)
    }
}
