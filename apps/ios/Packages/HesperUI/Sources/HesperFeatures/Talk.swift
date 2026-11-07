import HesperDesign
import SwiftUI

/// A press on Speak.
struct Talk: Equatable {
    /// When the finger came down; nil when it is up.
    var pressed: Date?
    /// The finger left the button: letting go now cancels.
    var away = false
    var isDown: Bool { pressed != nil }
}

/// What push-to-talk hears, at the top of the screen while Speak is held,
/// clear of the thumb on the console.
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
        .neonPanel(edge: Theme.accent)
        .opacity(away ? 0.6 : 1)
    }
}

extension View {
    /// No system tab bar on compact width; pushed screens have none.
    func withoutSystemTabBarOnPhone() -> some View {
        modifier(HideSystemTabBar())
    }
}

private struct HideSystemTabBar: ViewModifier {
    @Environment(\.horizontalSizeClass) private var size

    func body(content: Content) -> some View {
        content.toolbarVisibility(size == .compact ? .hidden : .automatic, for: .tabBar)
    }
}
