import Foundation
import HesperCore
import HesperFleet
import Synchronization

/// A pairing service that plays each way signing in and pairing can end
/// (docs/iphone/onboarding.md, "Empty and failure states"), for previews,
/// tests, the sample Macs and the app until the mobile link library
/// plugs in. It never shows a sign-in page.
public final class FixturePairing: PairingService {
    /// How `signIn` ends.
    public enum SignIn: String, Sendable, CaseIterable {
        case ok, cancelled, notAllowed, unreachable
    }

    /// How asking a Mac ends.
    public enum Pair: Sendable, Equatable {
        case approved
        /// Pending with a code; approved after `asks` more asks.
        case pending(approvedAfter: Int)
        case offline, denied, unsupported, keyChanged
    }

    public struct Script: Sendable {
        public var signIn = SignIn.ok
        /// For `pair(offer:)`. `unsupported` then follows `pairMachine`.
        public var pairOffer = Pair.approved
        public var pairMachine = Pair.pending(approvedAfter: 1)
        public var unpaired = [Machine(short: "mini", name: "mini"), Machine(short: "air", name: "air", online: false)]
        /// How long each call takes, so the screens show their progress.
        public var delay = Duration.zero

        public init() {}
    }

    private struct State {
        var script: Script
        var account: RelayAccount?
        var asks: [String: Int] = [:]
        var paired: Set<String> = []
        var pushToken: Data?
        var resets = 0
    }

    private let state: Mutex<State>

    public static let login = "jdoe"
    public static let code = "ABC-DEF"

    public init(_ script: Script = Script(), account: RelayAccount? = nil) {
        state = Mutex(State(script: script, account: account))
    }

    /// A script by name, for launch arguments: `notAllowed`, `unreachable`,
    /// `cancelled` (sign in), `pending`, `offline`, `denied`, `unsupported`,
    /// `keyChanged` (pairing); anything else is the happy path.
    public static func script(named name: String?) -> Script {
        var s = Script()
        switch name {
        case let n? where SignIn(rawValue: n) != nil: s.signIn = SignIn(rawValue: n)!
        case "pending": s.pairOffer = .pending(approvedAfter: 2)
        case "offline": s.pairOffer = .offline
        case "denied": s.pairOffer = .denied
        case "unsupported": s.pairOffer = .unsupported
        case "keyChanged": s.pairOffer = .keyChanged
        default: break
        }
        s.delay = .milliseconds(600)
        return s
    }

    /// The sample code a Mac named studio would show, valid for 5 minutes.
    public static func sampleCode(now: Date = .now) -> String {
        PairingPayload.encode(sampleOffer(now: now))
    }

    public static func sampleOffer(now: Date = .now) -> PairingOffer {
        PairingOffer(
            relay: URL(string: "https://relay.example.com")!, machine: "studio", name: "studio", hostKey: Data(count: 32),
            rights: ["observe", "answer", "type", "transfer"], secret: Data(0..<16), expires: now.addingTimeInterval(300))
    }

    /// The token `registerPushToken` got, and how often `reset` ran.
    public var pushToken: Data? { state.withLock { $0.pushToken } }
    public var resets: Int { state.withLock { $0.resets } }

    public func account() async -> RelayAccount? { state.withLock { $0.account } }

    public func signIn(relay: URL, present: SignInPresenter) async throws(PairingError) -> RelayAccount {
        await wait()
        let host = relay.host() ?? relay.absoluteString
        return try state.withLock { (s: inout State) throws(PairingError) -> RelayAccount in
            switch s.script.signIn {
            case .ok:
                let account = RelayAccount(relay: relay, deviceID: "DEVICE_ID", login: Self.login)
                s.account = account
                return account
            case .cancelled: throw .cancelled
            case .notAllowed: throw .notAllowed(login: Self.login)
            case .unreachable: throw .relayUnreachable(host: host)
            }
        }
    }

    public func pair(offer: PairingOffer) async throws(PairingError) -> PairResult {
        await wait()
        let outcome = state.withLock { $0.script.pairOffer }
        if outcome == .unsupported { throw .offerUnsupported(machine: offer.machine) }
        return try answer(outcome, machine: offer.machine, rights: offer.rights)
    }

    public func pair(machine: String) async throws(PairingError) -> PairResult {
        await wait()
        return try answer(state.withLock { $0.script.pairMachine }, machine: machine, rights: ["observe", "answer", "type", "transfer"])
    }

    public func unpairedMacs() async throws(PairingError) -> [Machine] {
        await wait()
        return state.withLock { s in s.script.unpaired.filter { !s.paired.contains($0.short) } }
    }

    public func registerPushToken(_ token: Data) async throws(PairingError) {
        state.withLock { $0.pushToken = token }
    }

    public func reset() async {
        state.withLock {
            $0.account = nil
            $0.asks = [:]
            $0.paired = []
            $0.resets += 1
        }
    }

    private func answer(_ outcome: Pair, machine: String, rights: [String]) throws(PairingError) -> PairResult {
        try state.withLock { (s: inout State) throws(PairingError) -> PairResult in
            guard s.account != nil else { throw .failed("Not signed in.") }
            switch outcome {
            case .approved:
                s.paired.insert(machine)
                return .approved(machine: machine, rights: rights)
            case .pending(let after):
                let asked = s.asks[machine, default: 0]
                s.asks[machine] = asked + 1
                guard asked >= after else {
                    return .pending(machine: machine, code: Self.code, expires: .now.addingTimeInterval(600))
                }
                s.paired.insert(machine)
                return .approved(machine: machine, rights: rights)
            case .offline: throw .macOffline(machine: machine)
            case .denied: throw .denied(machine: machine)
            case .unsupported: throw .offerUnsupported(machine: machine)
            case .keyChanged: throw .keyChanged(machine: machine)
            }
        }
    }

    private func wait() async {
        let delay = state.withLock { $0.script.delay }
        if delay > .zero { try? await Task.sleep(for: delay) }
    }
}
