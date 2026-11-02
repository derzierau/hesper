import HesperCore
import HesperDesign
import HesperFleet
import SwiftUI

/// 3 · Sign in: the card says where (the Mac and the relay), then GitHub
/// in the system sheet. The relay admits only its owner, so the phone
/// enrolls as a controller, like `hesperctl login`.
struct SignInScreen: View {
    @Environment(OnboardingModel.self) private var model
    @State private var web = WebSignIn()

    var body: some View {
        OnboardingPage(back: model.busy ? nil : { model.back() }) {
            content
        } footer: {
            footer
        }
    }

    private var host: String { model.relay?.host() ?? "" }

    @ViewBuilder private var content: some View {
        if case .notAllowed(let login) = model.failure {
            OnboardingTitle(title: "Sign in to your relay")
            card(host: host, error: true)
            VStack(alignment: .leading, spacing: DS.Spacing.s) {
                Text("\(host) doesn't let \(login) in").font(.headline)
                Text("Sign in with the GitHub account the relay allows, or ask its owner to add yours.")
                    .font(.subheadline)
                    .foregroundStyle(Theme.text2)
            }
        } else {
            OnboardingTitle(title: "Sign in to your relay", lead: Text(lead))
            card(host: host, error: false)
            if let message = model.failure?.message() {
                Text(message).font(.subheadline).foregroundStyle(Theme.error)
            }
            if let code = web.code {
                Text("Hesper shows \(Text(code).font(.body.monospaced().weight(.semibold)))")
                    .font(.subheadline)
                    .foregroundStyle(Theme.text2)
                    .frame(maxWidth: .infinity)
            }
        }
    }

    private var lead: String {
        guard let offer = model.offer else { return "Sign in with the GitHub account your relay lets in." }
        return "\(offer.name) is on your own relay. Sign in with the GitHub account it lets in."
    }

    @ViewBuilder private var footer: some View {
        if case .notAllowed = model.failure {
            WideButton("Use another account") {
                web.ephemeral = true
                model.signIn(present: web.presenter)
            }
        } else {
            Button {
                model.signIn(present: web.presenter)
            } label: {
                HStack(spacing: DS.Spacing.m) {
                    if model.busy { ProgressView() }
                    Text(model.failure == nil ? "Continue with GitHub" : "Try again")
                }
                .frame(maxWidth: .infinity)
            }
            .primaryAction()
            .disabled(model.busy)
        }
        Text("This iPhone gets its own keys. They never leave it.")
            .font(.footnote)
            .foregroundStyle(Theme.dim)
    }

    private func card(host: String, error: Bool) -> some View {
        HStack(spacing: DS.Spacing.l) {
            Image(systemName: "macbook")
                .font(.title2)
                .foregroundStyle(Theme.accent)
                .frame(width: 44, height: 44)
                .background(Theme.accent.opacity(0.12), in: .rect(cornerRadius: CGFloat(DesignTokens.Radius.tile)))
            VStack(alignment: .leading, spacing: DS.Spacing.xxs) {
                Text(model.offer?.name ?? host).font(.headline)
                if model.offer != nil {
                    Text(host).font(.subheadline.monospaced()).foregroundStyle(Theme.text2)
                }
            }
            Spacer()
            if model.offer != nil, !error {
                Image(systemName: "checkmark.circle.fill").foregroundStyle(Theme.done)
            }
        }
        .padding(DS.Spacing.l)
        .background(Theme.tile, in: .rect(cornerRadius: CGFloat(DesignTokens.Radius.panel), style: .continuous))
        .overlay {
            // Error red only on the card's edge, never a red button.
            RoundedRectangle(cornerRadius: CGFloat(DesignTokens.Radius.panel), style: .continuous)
                .strokeBorder(error ? Theme.error : Theme.line, lineWidth: error ? 1.5 : 1)
        }
        .accessibilityElement(children: .combine)
    }
}

#Preview {
    SignInScreen().onboardingPreview(.signIn)
}
