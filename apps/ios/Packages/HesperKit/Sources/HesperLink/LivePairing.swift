import Foundation
import HesperCore
import HesperFleet

/// What `LivePairing` and `LiveLink` share: the phone's secrets, its
/// credentials and pinned hosts, and how it opens sockets.
public final class LinkContext: Sendable {
    let secrets: any SecretStore
    public let credentials: CredentialStore
    public let hosts: PairedHosts
    let sockets: any SocketFactory
    let api: @Sendable (URL) -> RelayAPI
    let enclave: Bool

    /// `enclave` false keeps the keys in software (simulator, tests).
    public init(
        secrets: any SecretStore, sockets: any SocketFactory = URLSessionSockets(),
        api: @escaping @Sendable (URL) -> RelayAPI = { RelayAPI(origin: $0) }, enclave: Bool = DeviceKeys.enclaveAvailable
    ) {
        self.secrets = secrets
        self.credentials = CredentialStore(store: secrets, api: api)
        self.hosts = PairedHosts(store: secrets)
        self.sockets = sockets
        self.api = api
        self.enclave = enclave
    }

    func keys() throws -> DeviceKeys { try DeviceKeys.load(from: secrets, enclave: enclave) }

    /// A relay connection with fresh credentials, and who the phone is.
    func connect() async throws -> (RelayConnection, ControllerIdentity) {
        let c = try await credentials.fresh()
        guard let origin = URL(string: c.relay) else { throw RelayError(code: "invalid_relay") }
        let relay = try await RelayConnection.connect(origin: origin, token: c.token, factory: sockets)
        return (relay, ControllerIdentity(device: c.deviceId, keys: try keys()))
    }
}

/// Enrollment and pairing against the owner's relay and Macs
/// (docs/iphone/onboarding.md).
public final class LivePairing: PairingService {
    let context: LinkContext
    /// The device's name on the relay and the Macs ("iPhone", "iPad").
    let deviceName: String
    /// `production` or `sandbox`: the APNs environment of this build.
    let pushEnvironment: String
    /// The rights asked for by code; an offer brings its own.
    static let defaultRights = ["observe", "answer", "type", "transfer"]

    public init(context: LinkContext, deviceName: String, pushEnvironment: String) {
        self.context = context
        self.deviceName = deviceName
        self.pushEnvironment = pushEnvironment
    }

    public func account() async -> RelayAccount? {
        guard let c = await context.credentials.current(), let relay = URL(string: c.relay) else { return nil }
        return RelayAccount(relay: relay, deviceID: c.deviceId, login: c.login ?? "")
    }

    public func signIn(relay: URL, present: SignInPresenter) async throws(PairingError) -> RelayAccount {
        try await mapped(host: relay.host() ?? relay.absoluteString) {
            // Signing in again on the same relay keeps the device id, so the
            // Macs' approvals survive. If the relay refuses the proof at
            // claim, sign in once more as a new device.
            let previous = await context.credentials.current().flatMap { $0.relay == relay.absoluteString ? $0.deviceId : nil }
            do {
                return try await enroll(relay: relay, previous: previous, present: present)
            } catch let e as RelayHTTPError where e.code == "previous_device_rejected" {
                return try await enroll(relay: relay, previous: nil, present: present)
            }
        }
    }

    private func enroll(relay: URL, previous: String?, present: SignInPresenter) async throws -> RelayAccount {
        let keys = try context.keys()
        let api = context.api(relay)
        let pkce = PKCE.random()
        let enrollment: Enrollment
        if let previous {
            let proof = try PreviousDevice.sign(id: previous, challenge: pkce.challenge, key: keys.device)
            do {
                enrollment = try await api.enroll(name: deviceName, pkce: pkce, previousDevice: proof)
            } catch let e as RelayHTTPError where e.status == 400 && ["invalid_previous_device", "invalid_request"].contains(e.code) {
                // A device the relay has no key for, or a relay from before.
                enrollment = try await api.enroll(name: deviceName, pkce: pkce)
            }
        } else {
            enrollment = try await api.enroll(name: deviceName, pkce: pkce)
        }
        let callback = try await present(enrollment.verificationURL, enrollment.code, RelayAPI.callbackScheme)
        switch EnrollmentCallback(callback, enrollment: enrollment.id) {
        case .approved, nil: break
        case .denied: throw PairingError.cancelled
        case .notAllowed(let login): throw PairingError.notAllowed(login: login)
        case .expired: throw PairingError.failed(Self.expired)
        case .other(let status): throw PairingError.failed("Sign-in ended: \(status)")
        }
        while Date() < enrollment.expires {
            if var c = try await api.claim(id: enrollment.id, verifier: pkce.verifier) {
                c.relay = relay.absoluteString
                try await context.credentials.save(c)
                return RelayAccount(relay: relay, deviceID: c.deviceId, login: c.login ?? "")
            }
            try await Task.sleep(for: .seconds(max(1, enrollment.interval)))
        }
        throw PairingError.failed(Self.expired)
    }

    static let expired = "The sign-in expired. Try again."

    public func pair(offer: PairingOffer) async throws(PairingError) -> PairResult {
        guard offer.expires > Date() else { throw .offerExpired }
        return try await mapped(host: offer.relay.host() ?? "", machine: offer.machine) {
            try await request(machine: offer.machine, name: offer.name, hostKey: offer.hostKey, rights: offer.rights, offer: offer.secret)
        }
    }

    public func pair(machine: String) async throws(PairingError) -> PairResult {
        try await mapped(host: await relayHost(), machine: machine) {
            let (relay, _) = try await context.connect()
            defer { relay.close() }
            let list = try await relay.machines()
            guard let m = list.first(where: { $0.id == machine }) ?? list.first(where: { $0.short == machine || $0.name == machine })
            else { throw PairingError.macOffline(machine: machine) }
            return try await request(machine: m.id, name: m.name, hostKey: nil, rights: Self.defaultRights, offer: nil)
        }
    }

    /// `devices.request`, with `offerProof` when the Mac's code was
    /// scanned. The host key is pinned: from the code, else on first sight
    /// (the approval code covers it).
    private func request(machine: String, name: String, hostKey: Data?, rights: [String], offer: Data?) async throws -> PairResult {
        if let hostKey { _ = try context.hosts.pin(machine: machine, name: name, key: hostKey) }
        let (relay, me) = try await context.connect()
        defer { relay.close() }
        let keys = me.keys
        var params: [String: Any] = [
            "name": deviceName, "key": keys.keyBase64, "strongKey": keys.strongKeyBase64, "hardware": keys.hardware,
            "rights": rights, "e2eKey": keys.e2ePublic.base64EncodedString(), "e2eBinding": try keys.binding(),
        ]
        if let offer {
            params["offerProof"] = OfferProof.make(
                secret: offer, machine: machine, name: deviceName, key: keys.keyBase64, strongKey: keys.strongKeyBase64,
                e2eKey: keys.e2ePublic.base64EncodedString())
        }
        var answer: Data
        do {
            answer = try await relay.request(machine: machine, method: "devices.request", params: try body(params), timeout: .seconds(25))
        } catch let e as RelayError where e.code == "invalid_request" && offer != nil {
            // A host from before offers refuses the unknown field: by code.
            params["offerProof"] = nil
            answer = try await relay.request(machine: machine, method: "devices.request", params: try body(params), timeout: .seconds(25))
        } catch let e as RelayError where e.code == "forbidden" && offer != nil {
            throw PairingError.offerExpired
        }
        // The result names the Mac as the live link and the fleet do.
        let label = (try? await relay.machines())?.first { $0.id == machine }.map(LiveLink.name) ?? machine
        guard let status = try JSONSerialization.jsonObject(with: answer) as? [String: Any],
            let key = (status["hostKey"] as? String).flatMap({ Data(base64Encoded: $0) }), key.count == 32
        else { throw PairingError.failed("\(name) sent no valid host key") }
        var host = try context.hosts.pin(machine: machine, name: name, key: key)
        switch status["status"] as? String {
        case "approved":
            let granted = status["rights"] as? [String] ?? rights
            host.rights = granted
            host.approved = true
            try context.hosts.save(host)
            return .approved(machine: label, rights: granted)
        case "pending":
            let code = ApprovalCode.make(hostKey: key, key: keys.device.publicKeyDER, strongKey: keys.strong.publicKeyDER)
            if let theirs = status["code"] as? String, theirs != code { throw PairingError.keyChanged(machine: machine) }
            let expires = (status["expires"] as? NSNumber).map { Date(timeIntervalSince1970: $0.doubleValue) } ?? Date() + 600
            return .pending(machine: label, code: code, expires: expires)
        default:
            throw PairingError.failed("\(name) answered with an unknown status")
        }
    }

    public func unpairedMacs() async throws(PairingError) -> [Machine] {
        try await mapped(host: await relayHost()) {
            let (relay, _) = try await context.connect()
            defer { relay.close() }
            let approved = context.hosts.all().filter(\.value.approved)
            return try await relay.machines().filter { approved[$0.id] == nil }
                .map { Machine(short: $0.short ?? $0.name, name: $0.name, online: $0.online) }
        }
    }

    public func registerPushToken(_ token: Data) async throws(PairingError) {
        try await mapped(host: await relayHost()) {
            let c = try await context.credentials.fresh()
            guard let origin = URL(string: c.relay) else { throw PairingError.failed("No relay") }
            try await context.api(origin).registerPush(token: token, environment: pushEnvironment, bearer: c.token)
        }
    }

    public func reset() async {
        await context.credentials.clear()
        context.hosts.clear()
        try? DeviceKeys.delete(from: context.secrets)
    }

    private func relayHost() async -> String {
        await context.credentials.current().flatMap { URL(string: $0.relay)?.host() } ?? ""
    }

    private func body(_ params: [String: Any]) throws -> Data {
        try Params.normalize(JSONSerialization.data(withJSONObject: params, options: [.sortedKeys]))
    }

    /// Runs `work`, turning its errors into the screens' terms.
    private func mapped<T>(host: String, machine: String = "", _ work: () async throws -> T) async throws(PairingError) -> T {
        do {
            return try await work()
        } catch {
            throw Self.pairingError(error, host: host, machine: machine)
        }
    }

    static func pairingError(_ error: any Error, host: String, machine: String) -> PairingError {
        switch error {
        case let e as PairingError: return e
        case is CancellationError: return .cancelled
        case let e as RelayError:
            switch e.code {
            case "key_changed", "e2e_binding": return .keyChanged(machine: machine)
            case "unavailable", "timeout", "offline": return .macOffline(machine: machine)
            case "forbidden": return .denied(machine: machine)
            case "unsupported": return .offerUnsupported(machine: machine)
            case "connection_lost": return .relayUnreachable(host: host)
            default: return .failed(e.message.isEmpty ? e.code : e.message)
            }
        case let e as RelayHTTPError:
            if e.status == 403 && e.code == "not_allowed" { return .notAllowed(login: "") }
            if e.signInRequired { return .failed("Sign in again to reach your Macs.") }
            return .failed("The relay refused (\(e.code.isEmpty ? String(e.status) : e.code))")
        case is URLError: return .relayUnreachable(host: host)
        default:
            let ns = error as NSError
            // ASWebAuthenticationSessionError.canceledLogin
            if ns.domain == "com.apple.AuthenticationServices.WebAuthenticationSession" && ns.code == 1 { return .cancelled }
            return .failed(ns.localizedDescription)
        }
    }
}
