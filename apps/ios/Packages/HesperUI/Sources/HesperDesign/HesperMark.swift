import HesperCore
import SwiftUI

/// The app icon's "h.", large: its full stop is a state mark. It starts in
/// the accent (working), then turns to signal and pulses (needs you), the
/// one thing Hesper does. Under Reduce Motion it is simply signal.
public struct HesperMark: View {
    var size: CGFloat
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @State private var needsYou = false
    @State private var dimmed = false

    public init(size: CGFloat = 120) {
        self.size = size
    }

    public var body: some View {
        let signal = needsYou || reduceMotion
        let tone = signal ? Theme.signal : Theme.accent
        let side = size * 0.22
        HStack(alignment: .lastTextBaseline, spacing: size * 0.1) {
            Text("h")
                .font(.system(size: size, weight: .semibold))
                .foregroundStyle(Theme.text)
            RoundedRectangle(cornerRadius: side * 0.12, style: .continuous)
                .fill(tone)
                .frame(width: side, height: side)
                .background {
                    RoundedRectangle(cornerRadius: side * 0.4, style: .continuous)
                        .fill(tone.opacity(0.18))
                        .padding(-side * 0.35)
                }
                .shadow(color: tone.opacity(0.5), radius: side * 0.65)
                .opacity(dimmed ? 0.45 : 1)
        }
        .accessibilityElement()
        .accessibilityLabel("Hesper")
        .task {
            guard !reduceMotion else { return }
            try? await Task.sleep(for: .seconds(1.2))
            withAnimation(.easeInOut(duration: DesignTokens.pulsePeriod / 4)) { needsYou = true }
            try? await Task.sleep(for: .seconds(DesignTokens.pulsePeriod / 4))
            withAnimation(.easeInOut(duration: DesignTokens.pulsePeriod / 2).repeatForever()) { dimmed = true }
        }
    }
}

/// The onboarding background: the console's dark with a low accent glow
/// below. Hesper is the evening star.
public struct HorizonBackground: View {
    public init() {}

    public var body: some View {
        Theme.background
            .overlay {
                GeometryReader { g in
                    RadialGradient(
                        colors: [Theme.accent.opacity(0.14), .clear], center: UnitPoint(x: 0.5, y: 1.08),
                        startRadius: 0, endRadius: max(g.size.width, g.size.height) * 0.62
                    )
                }
            }
            .ignoresSafeArea()
    }
}

#Preview {
    VStack(spacing: DS.Spacing.xxl) {
        HesperMark()
        HesperMark(size: 38)
    }
    .frame(maxWidth: .infinity, maxHeight: .infinity)
    .background(HorizonBackground())
}
