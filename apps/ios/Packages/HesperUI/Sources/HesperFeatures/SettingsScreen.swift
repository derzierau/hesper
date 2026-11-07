import HesperDesign
import HesperFleet
import SwiftUI
import UIKit
import UserNotifications

/// Settings: what this iPhone is signed in to, notifications and voice in
/// plain status lines, and Reset this iPhone, the only way back to Hello.
struct SettingsScreen: View {
    @Environment(OnboardingModel.self) private var onboarding
    @Environment(\.openURL) private var openURL
    @Environment(\.scenePhase) private var scenePhase
    @State private var notifications = UNAuthorizationStatus.notDetermined
    @State private var confirmReset = false
    @AppStorage(OnboardingModel.languageKey) private var language: String?

    var body: some View {
        List {
            if let account = onboarding.account {
                Section {
                    LabeledContent("Relay", value: account.relay.host() ?? account.relay.absoluteString).themedRow()
                    LabeledContent("Signed in as", value: account.login).themedRow()
                } header: {
                    Text("This iPhone").listHeading()
                }
            }
            Section {
                HStack {
                    Text(notificationsLine).foregroundStyle(Theme.text2)
                    Spacer()
                    if notifications == .denied {
                        Button("Settings") {
                            if let url = URL(string: UIApplication.openNotificationSettingsURLString) { openURL(url) }
                        }
                        .secondaryAction()
                        .controlSize(.small)
                    }
                }
                .themedRow()
            } header: {
                Text("Notifications").listHeading()
            }
            Section {
                LabeledContent("Language", value: language.map(SpeechLanguages.displayName) ?? "Your keyboard's").themedRow()
            } header: {
                Text("Voice").listHeading()
            }
            Section {
                Button(role: .destructive) {
                    confirmReset = true
                } label: {
                    Text("Reset this iPhone").frame(maxWidth: .infinity)
                }
                .destructiveAction()
                .themedRow()
            } footer: {
                Text("Deletes this iPhone's keys and sign-in, and starts over.").foregroundStyle(Theme.muted)
            }
        }
        .themedList()
        .navigationTitle("Settings")
        .confirmationDialog("Reset this iPhone?", isPresented: $confirmReset, titleVisibility: .visible) {
            Button("Reset", role: .destructive) { Task { await onboarding.reset() } }
        } message: {
            Text("Its keys and sign-in are deleted. Remove the iPhone on your Macs too, in Hesper › Settings › Machines & pairing.")
        }
        .onChange(of: scenePhase, initial: true) { _, phase in
            guard phase == .active else { return }
            Task { notifications = await UNUserNotificationCenter.current().notificationSettings().authorizationStatus }
        }
    }

    private var notificationsLine: String {
        switch notifications {
        case .authorized, .ephemeral, .provisional: "On: agents that need you reach the lock screen"
        case .denied: "Off: you see what needs you only in the app"
        default: "Not asked yet"
        }
    }
}

#Preview {
    NavigationStack { SettingsScreen() }.onboardingPreview(.finished)
}
