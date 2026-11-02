import HesperFixtures
import HesperFleet
import SwiftUI

extension View {
    /// A model on the sample Macs, for previews.
    func fixtureModel() -> some View {
        let model = FleetModel(link: FixtureLink(), fleet: Samples.fleet)
        model.start()
        return environment(model)
    }

    /// Onboarding walked to `step` on a `FixturePairing`, for previews.
    func onboardingPreview(_ step: OnboardingModel.Step, _ script: FixturePairing.Script = .init()) -> some View {
        let defaults = UserDefaults(suiteName: "preview.\(UUID())")!
        let model = OnboardingModel(pairing: FixturePairing(script), defaults: defaults, poll: .seconds(30))
        return environment(model).task { await model.walk(to: step) }.fixtureModel()
    }
}
