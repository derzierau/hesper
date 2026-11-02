import AuthenticationServices
import HesperFleet
import Observation
import UIKit

/// Shows the relay's sign-in page in `ASWebAuthenticationSession` and
/// returns the callback URL it ended on: the app's `SignInPresenter`.
/// While the sheet is up, `code` is the enrollment code the page shows,
/// for the screen under it.
@Observable
final class WebSignIn: NSObject, ASWebAuthenticationPresentationContextProviding {
    /// The registered callback: `hesper-ios://auth`.
    static let callbackScheme = "hesper-ios"

    private(set) var code: String?
    /// A fresh browser session without Safari's cookies, for "Use another
    /// account".
    var ephemeral = false
    private var session: ASWebAuthenticationSession?

    var presenter: SignInPresenter {
        { [self] page, code, scheme in try await present(page, code: code, scheme: scheme) }
    }

    func present(_ page: URL, code: String, scheme: String) async throws -> URL {
        self.code = code
        defer {
            self.code = nil
            session = nil
        }
        return try await withCheckedThrowingContinuation { (done: CheckedContinuation<URL, any Error>) in
            let session = ASWebAuthenticationSession(url: page, callback: .customScheme(scheme)) { url, error in
                if let url {
                    done.resume(returning: url)
                } else if let error = error as? ASWebAuthenticationSessionError, error.code == .canceledLogin {
                    done.resume(throwing: PairingError.cancelled)
                } else {
                    done.resume(throwing: PairingError.failed(error?.localizedDescription ?? "Signing in stopped."))
                }
            }
            session.presentationContextProvider = self
            session.prefersEphemeralWebBrowserSession = ephemeral
            self.session = session
            if !session.start() { done.resume(throwing: PairingError.failed("The sign-in page didn't open.")) }
        }
    }

    func presentationAnchor(for session: ASWebAuthenticationSession) -> ASPresentationAnchor {
        let scenes = UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }
        let windows = scenes.flatMap(\.windows)
        // Signing in starts from a screen, so there is always a scene.
        return windows.first(where: \.isKeyWindow) ?? windows.first ?? ASPresentationAnchor(windowScene: scenes[0])
    }
}
