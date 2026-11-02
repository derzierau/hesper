import Foundation
import HesperFixtures
import HesperFleet

/// What the app talks to the Macs with. This is where the mobile link
/// library's `LiveLink` and `LivePairing` plug in; until then the app runs
/// on the fixtures. The sample Macs always run on `FixtureLink`.
struct Services {
    var link: any LinkClient
    var pairing: any PairingService

    static func make() -> Services {
        // `-HesperPairingFixture denied` (a launch argument) plays one of
        // FixturePairing's failure paths.
        let script = FixturePairing.script(named: UserDefaults.standard.string(forKey: "HesperPairingFixture"))
        return Services(link: FixtureLink(), pairing: FixturePairing(script))
    }
}
