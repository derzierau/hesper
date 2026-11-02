import HesperCore
import SwiftUI

/// The square state mark, the same shape as on the Mac. Only "needs you"
/// pulses, and not under Reduce Motion.
public struct StateMark: View {
    let kind: StateMarkKind
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @State private var dimmed = false

    public init(_ state: AgentState) { kind = StateMarkKind(state) }
    public init(_ kind: StateMarkKind) { self.kind = kind }

    public var body: some View {
        let tone = Theme.tone(kind.tone)
        let shape = RoundedRectangle(cornerRadius: 1.5, style: .continuous)
        ZStack {
            switch kind.fill {
            case .filled:
                shape.fill(tone)
            case .outlined:
                shape.strokeBorder(tone, lineWidth: CGFloat(StateMarkKind.stroke))
            case .slashed:
                shape.strokeBorder(tone, lineWidth: CGFloat(StateMarkKind.stroke))
                Path { p in
                    p.move(to: CGPoint(x: 0, y: DS.markSide))
                    p.addLine(to: CGPoint(x: DS.markSide, y: 0))
                }
                .stroke(tone, lineWidth: CGFloat(StateMarkKind.stroke))
            }
        }
        .frame(width: DS.markSide, height: DS.markSide)
        .opacity(dimmed ? 0.45 : 1)
        .onAppear {
            guard kind.pulses(reduceMotion: reduceMotion) else { return }
            withAnimation(.easeInOut(duration: DesignTokens.pulsePeriod / 2).repeatForever()) { dimmed = true }
        }
        .accessibilityLabel(kind.label)
    }
}

#Preview(traits: .sizeThatFitsLayout) {
    HStack(spacing: DS.Spacing.l) {
        ForEach(StateMarkKind.allCases, id: \.self) { StateMark($0) }
    }
    .padding()
}
