import Foundation
import HesperFleet

extension OnboardingModel {
    /// Walks a fresh model on a `FixturePairing` to `step` the way the
    /// owner would, for previews and screenshots of each screen.
    public func walk(to step: Step) async {
        let present: SignInPresenter = { page, _, _ in page }
        await resume()
        guard step != .hello, step != .starting else { return }
        if step == .finished {
            lookAround()
            return
        }
        connectMac()
        switch step {
        case .scan: return
        case .address:
            enterAddress()
            return
        case .pickMac, .approveOnMac:
            enterAddress()
            submitAddress("relay.example.com")
            signIn(present: present)
            await settled()
            if step == .approveOnMac, let mac = unpaired.first {
                choose(mac)
                for _ in 0..<200 where self.step != .approveOnMac { try? await Task.sleep(for: .milliseconds(10)) }
            }
            return
        default: break
        }
        scanned(FixturePairing.sampleCode())
        guard step != .signIn else { return }
        signIn(present: present)
        await settled()
        guard step != .connected else { return }
        notificationsAnswered()
    }
}
