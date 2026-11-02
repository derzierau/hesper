import Foundation
import HesperFixtures
import HesperFleet
import HesperLink
import UIKit

/// What the app talks to the Macs with: the mobile link library's
/// `LiveLink` and `LivePairing` on the owner's relay. The sample Macs
/// always run on `FixtureLink`.
struct Services {
    var link: any LinkClient
    var pairing: any PairingService

    @MainActor static func make() -> Services {
        #if DEBUG
            // `-HesperPairingFixture denied` (a launch argument) plays one of
            // FixturePairing's paths instead of the relay;
            // `-HesperOnboardingDemo` walks the fixtures too.
            let defaults = UserDefaults.standard
            let fixture = defaults.string(forKey: "HesperPairingFixture")
            if fixture != nil || defaults.string(forKey: "HesperOnboardingDemo") != nil {
                return Services(link: FixtureLink(), pairing: FixturePairing(FixturePairing.script(named: fixture)))
            }
        #endif
        let context = LinkContext(secrets: KeychainStore(service: Bundle.main.bundleIdentifier ?? "hesper.ios"))
        #if DEBUG
            let push = "sandbox"
        #else
            let push = "production"
        #endif
        return Services(
            link: LiveLink(context: context),
            pairing: LivePairing(context: context, deviceName: UIDevice.current.model, pushEnvironment: push))
    }
}
