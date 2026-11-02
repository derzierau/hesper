import HesperCore
import HesperDesign
import HesperFleet
import SwiftUI

/// 1 · Hello: the mark, one line, one button. Sample Macs is the demo
/// mode (and App Review's way in); it asks for nothing.
struct HelloScreen: View {
    @Environment(OnboardingModel.self) private var model

    var body: some View {
        OnboardingPage {
            VStack(spacing: DS.Spacing.xxl) {
                HesperMark()
                    .padding(.top, DS.Spacing.xxl * 3)
                HStack(spacing: DS.Spacing.s) {
                    StateMark(StateMarkKind.working)
                    Text("working")
                    Text("→").opacity(0.5)
                    StateMark(StateMarkKind.needsYou)
                    Text("needs you")
                }
                .font(.caption2)
                .foregroundStyle(Theme.dim)
                .accessibilityHidden(true)
                VStack(spacing: DS.Spacing.m) {
                    Text("Hesper").font(.title.weight(.semibold))
                    Text("Your agents keep working on your Macs. Hesper tells you when they need you.")
                        .font(.body)
                        .foregroundStyle(Theme.text2)
                }
                .multilineTextAlignment(.center)
            }
            .frame(maxWidth: .infinity)
        } footer: {
            WideButton("Connect a Mac") { model.connectMac() }
            LinkButton("Look around with sample Macs") { model.lookAround() }
        }
    }
}

#Preview {
    HelloScreen().onboardingPreview(.hello)
}
