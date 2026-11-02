import HesperFeatures
import HesperFixtures
import HesperFleet
import SwiftUI

/// The app target is only this: one model over a link, and the root view.
/// It runs on the sample Macs until the mobile link library gives it a
/// live `LinkClient` (docs/iphone/concept.md, "Core work" 1).
@main
struct HesperApp: App {
    @State private var model = FleetModel(link: FixtureLink())
    @Environment(\.scenePhase) private var scenePhase

    var body: some Scene {
        WindowGroup {
            RootView()
                .environment(model)
                // No background sockets: follow the Macs only while active.
                .onChange(of: scenePhase, initial: true) { _, phase in
                    if phase == .active { model.start() } else if phase == .background { model.stop() }
                }
        }
    }
}
