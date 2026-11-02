import HesperCore
import HesperDesign
import HesperFixtures
import HesperFleet
import SwiftUI

/// Onboarding (docs/iphone/onboarding.md, wireframes round 6): one screen
/// per step of `OnboardingModel`, until the first Mac is on the phone or
/// the owner looks around with the sample Macs. On iPad the same screens
/// sit in a centred card over the dusk.
public struct OnboardingView: View {
    @Environment(OnboardingModel.self) private var model
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    public init() {}

    public var body: some View {
        ZStack {
            switch model.step {
            case .starting, .finished: Color.clear
            case .hello: HelloScreen()
            case .scan: ScanScreen()
            case .address: AddressScreen()
            case .signIn: SignInScreen()
            case .pickMac: PickMacScreen()
            case .approveOnMac: ApproveOnMacScreen()
            case .connected: ConnectedScreen()
            case .language: LanguageScreen()
            }
        }
        .transition(.opacity)
        .animation(DS.animation(.settle, reduceMotion: reduceMotion), value: model.step)
        .background(HorizonBackground())
    }
}

/// One onboarding screen: an optional back chevron, the content, and the
/// actions at the bottom. Form-sheet width on iPad.
struct OnboardingPage<Content: View, Footer: View>: View {
    var back: (() -> Void)?
    @ViewBuilder var content: Content
    @ViewBuilder var footer: Footer

    @Environment(\.horizontalSizeClass) private var size

    var body: some View {
        VStack(spacing: 0) {
            HStack {
                if let back {
                    Button("Back", systemImage: "chevron.left", action: back)
                        .labelStyle(.iconOnly)
                        .font(.title3.weight(.medium))
                        .foregroundStyle(Theme.accent)
                        .frame(minWidth: 44, minHeight: 44, alignment: .leading)
                }
                Spacer()
            }
            .frame(minHeight: 44)
            ScrollView {
                VStack(alignment: .leading, spacing: DS.Spacing.l) { content }
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .padding(.top, DS.Spacing.s)
            }
            .scrollBounceBehavior(.basedOnSize)
            VStack(spacing: DS.Spacing.l) { footer }
                .controlSize(.large)
                .padding(.vertical, DS.Spacing.l)
        }
        .padding(.horizontal, DS.Spacing.xxl)
        .frame(maxWidth: size == .regular ? Self.cardWidth : .infinity)
        .background {
            if size == .regular {
                RoundedRectangle(cornerRadius: CGFloat(DesignTokens.Radius.panel) * 2, style: .continuous)
                    .fill(Theme.surface.opacity(0.6))
                    .padding(.vertical, -DS.Spacing.xxl)
            }
        }
        .padding(.vertical, size == .regular ? DS.Spacing.xxl * 3 : 0)
        .foregroundStyle(Theme.text)
    }

    /// A form sheet's width.
    static var cardWidth: CGFloat { 540 }
}

/// The big title and the line under it.
struct OnboardingTitle: View {
    let title: String
    var lead: Text?

    var body: some View {
        VStack(alignment: .leading, spacing: DS.Spacing.m) {
            Text(title).font(.title.weight(.semibold))
            if let lead { lead.font(.body).foregroundStyle(Theme.text2) }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }
}

/// A full-width button in the footer.
struct WideButton: View {
    let title: String
    var primary = true
    let action: () -> Void

    init(_ title: String, primary: Bool = true, action: @escaping () -> Void) {
        self.title = title
        self.primary = primary
        self.action = action
    }

    var body: some View {
        if primary {
            // On the accent, in the background's colour (as the mic circle).
            Button(action: action) { Text(title).foregroundStyle(Theme.background).frame(maxWidth: .infinity) }.primaryAction()
        } else {
            Button(action: action) { Text(title).frame(maxWidth: .infinity) }.secondaryAction()
        }
    }
}

/// A plain text button in the accent ("Enter address instead").
struct LinkButton: View {
    let title: String
    let action: () -> Void

    init(_ title: String, action: @escaping () -> Void) {
        self.title = title
        self.action = action
    }

    var body: some View {
        Button(title, action: action)
            .font(.subheadline.weight(.medium))
            .foregroundStyle(Theme.accent)
            .buttonStyle(.plain)
            .frame(minHeight: 44)
    }
}

/// A done, pending or failed step: ✓, a spinner, or ✕.
struct StepTick: View {
    enum State { case done, pending, failed }
    let state: State
    let title: String

    var body: some View {
        HStack(spacing: DS.Spacing.m) {
            switch state {
            case .done:
                Image(systemName: "checkmark.circle.fill").foregroundStyle(Theme.done)
            case .pending:
                ProgressView().controlSize(.small)
            case .failed:
                Image(systemName: "xmark.circle.fill").foregroundStyle(Theme.error)
            }
            Text(title).font(.subheadline).foregroundStyle(state == .done ? Theme.text : Theme.text2)
        }
        .frame(minHeight: 22)
        .accessibilityElement(children: .combine)
    }
}

#Preview("Onboarding") {
    let model = OnboardingModel(pairing: FixturePairing())
    OnboardingView()
        .environment(model)
        .task { await model.resume() }
}
