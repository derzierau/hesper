import HesperDesign
import SwiftUI
import TipKit

/// The bottom of Home (docs/iphone/neon.md, "Console"): **Speak** and
/// **Type** on a solid bar with a line on top. It replaces the floating
/// tab capsule and mic circle; Agents, Macs, Search and Settings are in
/// Home's menu.
///
/// Speak listens from the moment it is touched. A tap opens the capture
/// sheet still listening; holding is push-to-talk: `TalkPanel`, at the top
/// of the screen, shows what is heard, letting go opens the sheet with it,
/// and sliding off the button before letting go throws it away. Type opens
/// the sheet on the keyboard.
struct Console: View {
    let dictation: Dictation
    /// The press on Speak, shared with the root view's `TalkPanel`.
    @Binding var talk: Talk
    let capture: (CaptureSheet.Opening) -> Void
    /// The press is down. iOS cancels it without an end, for one when the
    /// microphone permission alert appears; then it stops listening.
    @GestureState private var touching = false
    @ScaledMetric(relativeTo: .title2) private var icon = DS.Size.consoleIcon

    var body: some View {
        HStack(spacing: DS.Spacing.grid) {
            speak
                .frame(maxWidth: .infinity)
                .layoutPriority(1)
            type
        }
        .padding(.horizontal, DS.Spacing.xl)
        .padding(.top, DS.Spacing.l)
        .frame(maxWidth: .infinity)
        .background(Theme.background, ignoresSafeAreaEdges: .bottom)
        .overlay(alignment: .top) {
            Rectangle().fill(Theme.line).frame(height: DS.Edge.line)
        }
        .dynamicTypeSize(...DS.chromeTypeLimit)
    }

    private var speak: some View {
        let holding = talk.isDown && dictation.isListening
        let away = talk.away
        let shape = RoundedRectangle(cornerRadius: DS.Radius.console)
        return HStack(spacing: DS.Spacing.m) {
            Image(systemName: away ? "xmark" : "mic.fill").font(.system(size: icon))
            Text("Speak").font(.title2.weight(.semibold))
        }
        .foregroundStyle(Theme.onAccent)
        .frame(maxWidth: .infinity, minHeight: DS.Size.console)
        .background(away ? Theme.muted : Theme.accent, in: shape)
        .popoverTip(MicTip(), arrowEdge: .bottom)
        .tipBackground(Theme.surface)
        .scaleEffect(holding ? 0.97 : 1)
        .animation(.snappy, value: holding)
        .contentShape(shape)
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
        .accessibilityLabel("Speak")
        .accessibilityHint("Starts a new task. Say a task. Hold to talk, let go to review it.")
        .accessibilityAddTraits(.isButton)
        .accessibilityAction {
            dictation.start()
            capture(.listening)
        }
        .accessibilityAction(named: "Type a task") { capture(.typing) }
    }

    private var type: some View {
        Button {
            capture(.typing)
        } label: {
            HStack(spacing: DS.Spacing.s) {
                Image(systemName: "keyboard").font(.system(size: icon))
                Text("Type").font(.headline).lineLimit(1).fixedSize()
            }
            .frame(minWidth: DS.Size.typeButton, minHeight: DS.Size.console)
        }
        .secondaryAction()
    }

    /// Shorter than this is a tap.
    private static let tap: TimeInterval = 0.35
    /// How far the finger may move from where it came down before letting
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
}

extension View {
    /// The console under this screen, on every width.
    func console(
        dictation: Dictation, talk: Binding<Talk>,
        capture: @escaping (CaptureSheet.Opening) -> Void
    ) -> some View {
        safeAreaInset(edge: .bottom, spacing: 0) {
            Console(dictation: dictation, talk: talk, capture: capture)
        }
    }
}

#Preview {
    Color.clear
        .console(dictation: Dictation(), talk: .constant(Talk())) { _ in }
        .background(Theme.background)
}
