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
}
