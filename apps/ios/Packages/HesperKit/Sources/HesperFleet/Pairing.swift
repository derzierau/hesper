import Foundation
import HesperCore

/// What a Mac's pairing code holds (docs/iphone/onboarding.md, "Interfaces";
/// relay/docs/protocol.md, "Pairing offers"). Parsed from the scanned text.
public struct PairingOffer: Equatable, Sendable {
    /// The relay's HTTPS origin.
    public var relay: URL
    /// The Mac's machine id and display name.
    public var machine: String
    public var name: String
    /// The Mac's end-to-end key (X25519, 32 bytes), pinned on pairing.
    public var hostKey: Data
    /// The rights the Mac offers (`observe`, `answer`, `type`, `transfer`,
    /// `shell`).
    public var rights: [String]
    /// The one-time secret (16 bytes). Never sent; it keys `offerProof`.
    public var secret: Data
    public var expires: Date

    public init(relay: URL, machine: String, name: String, hostKey: Data, rights: [String], secret: Data, expires: Date) {
        self.relay = relay
        self.machine = machine
        self.name = name
        self.hostKey = hostKey
        self.rights = rights
        self.secret = secret
        self.expires = expires
    }
}

/// The relay account this phone is enrolled with.
public struct RelayAccount: Equatable, Sendable {
    public var relay: URL
    public var deviceID: String
    /// The GitHub login, for "Signed in as".
    public var login: String

    public init(relay: URL, deviceID: String, login: String) {
        self.relay = relay
        self.deviceID = deviceID
        self.login = login
    }
}

/// How asking a Mac to let this phone in ended. `machine` names the Mac
/// as the link and the fleet do (its short name), not by machine id.
public enum PairResult: Equatable, Sendable {
    /// The Mac approved this phone with these rights.
    case approved(machine: String, rights: [String])
    /// The Mac waits for the owner to approve `code` (ABC-DEF) there.
    case pending(machine: String, code: String, expires: Date)
}

/// Why signing in or pairing failed, in the terms the screens use.
public enum PairingError: Error, Equatable, Sendable {
    case notAHesperCode
    case offerExpired
    case relayUnreachable(host: String)
    /// The relay's allowlist does not include this GitHub account.
    case notAllowed(login: String)
    case cancelled
    case macOffline(machine: String)
    case denied(machine: String)
    /// The Mac's hesperd cannot approve by offer; use the code flow.
    case offerUnsupported(machine: String)
    case keyChanged(machine: String)
    case failed(String)
}

/// Shows the relay's sign-in page and returns the callback URL it ended on.
/// The app implements it with `ASWebAuthenticationSession`; `code` is the
/// enrollment code to show under the sheet.
public typealias SignInPresenter = @MainActor @Sendable (_ page: URL, _ code: String, _ callbackScheme: String) async throws -> URL

/// Enrollment and pairing for onboarding and the Macs tab. The live
/// implementation is the mobile link library; `FixturePairing` stands in
/// for previews, tests and the sample Macs.
public protocol PairingService: Sendable {
    /// The account the phone is enrolled with, or nil.
    func account() async -> RelayAccount?
    /// Enrolls this phone as a controller on `relay` with GitHub, creating
    /// its device, strong and end-to-end keys on first use.
    func signIn(relay: URL, present: SignInPresenter) async throws(PairingError) -> RelayAccount
    /// Asks the offer's Mac to let this phone in, proving the scanned
    /// secret (`offerProof`). Approved at once when the Mac supports it.
    func pair(offer: PairingOffer) async throws(PairingError) -> PairResult
    /// Asks `machine` to let this phone in by code (`devices.request`).
    func pair(machine: String) async throws(PairingError) -> PairResult
    /// The owner's Macs on the relay that have not approved this phone.
    func unpairedMacs() async throws(PairingError) -> [Machine]
    /// Registers the APNs device token with the relay.
    func registerPushToken(_ token: Data) async throws(PairingError)
    /// Forgets the credentials and keys on this phone.
    func reset() async
}
