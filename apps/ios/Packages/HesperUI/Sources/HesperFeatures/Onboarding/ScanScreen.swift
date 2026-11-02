import HesperDesign
import HesperFleet
import SwiftUI

/// 2 · Scan your Mac: what to do on the Mac, then the camera. A code found
/// moves on by itself; Enter address instead is the way without one.
struct ScanScreen: View {
    @Environment(OnboardingModel.self) private var model

    var body: some View {
        OnboardingPage(back: model.back) {
            OnboardingTitle(
                title: "Scan your Mac",
                lead: Text("On your Mac, open **Hesper › Settings › iPhone**, or run `hesperctl phone`."))
            CodeScanner(enterAddress: model.enterAddress, notice: model.scanNotice) { text in
                if case .found = model.scanned(text) { return true }
                return false
            }
        } footer: {
            EmptyView()
        }
    }
}

#Preview {
    ScanScreen().onboardingPreview(.scan)
}
