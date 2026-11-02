import HesperFeatures
import HesperFixtures
import HesperFleet
import SwiftUI
import TipKit
import UIKit
import UserNotifications

/// The app target is only this: onboarding until the first Mac is on the
/// phone (or the owner looks around with the sample Macs), then one model
/// over a link and the root view. The links come from `Services`.
@main
struct HesperApp: App {
    @UIApplicationDelegateAdaptor private var delegate: AppDelegate
    @State private var onboarding: OnboardingModel
    @State private var addMac: AddMacModel
    @State private var live: FleetModel
    @State private var samples = FleetModel(link: FixtureLink())
    @Environment(\.scenePhase) private var scenePhase

    init() {
        let services = Services.make()
        _onboarding = State(initialValue: OnboardingModel(pairing: services.pairing))
        _addMac = State(initialValue: AddMacModel(pairing: services.pairing))
        _live = State(initialValue: FleetModel(link: services.link))
        try? Tips.configure()
    }

    /// The model the screens show: the sample Macs, or the owner's.
    private var model: FleetModel { onboarding.mode == .samples ? samples : live }

    var body: some Scene {
        WindowGroup {
            Group {
                if onboarding.isFinished {
                    RootView()
                } else {
                    OnboardingView()
                }
            }
            .environment(model)
            .environment(onboarding)
            .environment(addMac)
            .task {
                delegate.token = { token in Task { await onboarding.registerPushToken(token) } }
                await onboarding.resume()
                #if DEBUG
                    if let step = DemoStep.launchArgument { await onboarding.walk(to: step) }
                #endif
            }
            // No background sockets: follow the Macs only while active.
            .onChange(of: scenePhase, initial: true) { _, phase in
                if phase == .active { model.start() } else if phase == .background { model.stop() }
            }
            .onChange(of: onboarding.mode) { _, mode in
                (mode == .samples ? live : samples).stop()
                model.start()
            }
        }
    }
}

/// Passes the APNs token to the relay, once notifications are allowed.
final class AppDelegate: NSObject, UIApplicationDelegate {
    var token: ((Data) -> Void)?

    func application(_ application: UIApplication, didFinishLaunchingWithOptions _: [UIApplication.LaunchOptionsKey: Any]? = nil) -> Bool {
        // The token can change; ask for it on every launch once allowed.
        Task {
            if await UNUserNotificationCenter.current().notificationSettings().authorizationStatus == .authorized {
                application.registerForRemoteNotifications()
            }
        }
        return true
    }

    func application(_ application: UIApplication, didRegisterForRemoteNotificationsWithDeviceToken deviceToken: Data) {
        token?(deviceToken)
    }
}

#if DEBUG
    /// `-HesperOnboardingDemo connected` walks onboarding to a screen on
    /// the fixtures, for screenshots in a headless simulator.
    enum DemoStep {
        static var launchArgument: OnboardingModel.Step? {
            let steps: [String: OnboardingModel.Step] = [
                "hello": .hello, "scan": .scan, "address": .address, "signIn": .signIn, "pickMac": .pickMac,
                "approveOnMac": .approveOnMac, "connected": .connected, "language": .language, "samples": .finished,
            ]
            return UserDefaults.standard.string(forKey: "HesperOnboardingDemo").flatMap { steps[$0] }
        }
    }
#endif
