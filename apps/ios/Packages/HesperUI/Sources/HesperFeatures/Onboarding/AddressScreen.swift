import HesperCore
import HesperDesign
import HesperFleet
import SwiftUI

/// The relay's address, typed: for when the camera is off or the Mac is
/// not at hand. The Mac is picked after signing in.
struct AddressScreen: View {
    @Environment(OnboardingModel.self) private var model
    @State private var address = ""
    @State private var invalid = false
    @FocusState private var focused: Bool

    var body: some View {
        OnboardingPage(back: model.back) {
            OnboardingTitle(
                title: "Your relay",
                lead: Text("The address your Macs use, as in `hesperctl login`."))
            TextField("relay.example.com", text: $address)
                .textContentType(.URL)
                .keyboardType(.URL)
                .textInputAutocapitalization(.never)
                .autocorrectionDisabled()
                .submitLabel(.continue)
                .focused($focused)
                .onSubmit(submit)
                .font(.title3)
                .padding(DS.Spacing.l)
                .background(Theme.tile, in: .rect(cornerRadius: CGFloat(DesignTokens.Radius.tile), style: .continuous))
            if invalid {
                Text("That isn't a relay address.").font(.footnote).foregroundStyle(Theme.error)
            }
        } footer: {
            WideButton("Continue", action: submit).disabled(address.isEmpty)
            LinkButton("Scan a code instead") { model.scanAgain() }
        }
        .onAppear { focused = true }
    }

    private func submit() {
        invalid = !model.submitAddress(address)
    }
}

#Preview {
    AddressScreen().onboardingPreview(.address)
}
