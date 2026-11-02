import Foundation
import HesperCore
import Observation

/// Onboarding (docs/iphone/onboarding.md): Hello, then scan the Mac's code
/// or type the relay's address, sign in, the Mac approves, Connected (and
/// notifications), the dictation language, done. Every step that is done
/// is remembered, so a relaunch resumes at the first one that is not.
@MainActor
@Observable
public final class OnboardingModel {
    public enum Step: Equatable, Sendable {
        /// Reading what was done before; nothing to show yet.
        case starting
        case hello, scan, address, signIn
        /// Signed in without a scan: which Mac to ask.
        case pickMac
        /// The Mac shows a code to approve (`pending`).
        case approveOnMac
        case connected, language
        /// The app: live, or on the sample Macs.
        case finished
    }

    public enum Mode: Equatable, Sendable {
        case live
        /// Look around with sample Macs: the fixture link, no permissions.
        case samples
    }

    /// The Mac's answer, on Connected.
    public enum Approval: Equatable, Sendable {
        case asking
        case approved(rights: [String])
        case failed(PairingError)
    }

    /// What a scan found.
    public enum ScanOutcome: Equatable, Sendable {
        /// Not a Hesper code: the viewfinder keeps looking.
        case ignored
        case expired
        case found(PairingOffer)
    }

    public private(set) var step = Step.starting
    public private(set) var mode = Mode.live
    /// The scanned code; never stored, it is single use and short-lived.
    public private(set) var offer: PairingOffer?
    /// The relay being signed in to: the code's, or the typed address.
    public private(set) var relay: URL?
    public private(set) var account: RelayAccount?
    /// The Mac this phone paired with, once approved.
    public private(set) var mac: PairedMac?
    public private(set) var approval: Approval?
    /// The code to approve on the Mac, while the Mac waits for the owner.
    public private(set) var pending: PendingPairing?
    /// The owner's Macs on the relay, on Pick a Mac.
    public private(set) var unpaired: [Machine] = []
    /// A call in flight (sign in, loading Macs, asking a Mac).
    public private(set) var busy = false
    /// Why the last step failed, for the screen to say; nil when the owner
    /// cancelled.
    public private(set) var failure: PairingError?
    /// "This code has expired …", under the viewfinder.
    public private(set) var scanNotice: String?
    /// The dictation languages to offer (BCP 47), best first.
    public private(set) var languages: [String] = []
    public var language: String?

    public var isFinished: Bool { step == .finished }

    /// Where onboarding keeps its progress, and the dictation language
    /// capture reads.
    public static let progressKey = "onboarding"
    public static let languageKey = "dictationLanguage"

    private let pairing: any PairingService
    private let defaults: UserDefaults
    private let poll: Duration
    private let now: @Sendable () -> Date
    private var progress: Progress
    /// The call in flight; a new step cancels it.
    private var work: Task<Void, Never>?

    public init(
        pairing: any PairingService, defaults: UserDefaults = .standard, poll: Duration = .seconds(5),
        now: @escaping @Sendable () -> Date = { .now }
    ) {
        self.pairing = pairing
        self.defaults = defaults
        self.poll = poll
        self.now = now
        progress = defaults.data(forKey: Self.progressKey).flatMap { try? JSONDecoder().decode(Progress.self, from: $0) } ?? Progress()
    }

    /// Resumes at the first step not done.
    public func resume() async {
        cancelWork()
        mode = progress.samples ? .samples : .live
        if progress.samples || progress.done {
            step = .finished
            return
        }
        account = await pairing.account()
        guard let account else {
            step = .hello
            return
        }
        relay = account.relay
        mac = progress.mac
        if progress.notificationsAsked {
            step = .language
        } else if let mac {
            approval = .approved(rights: mac.rights)
            step = .connected
        } else if let p = progress.pending, p.expires > now() {
            pending = p
            step = .approveOnMac
            run { await self.waitForApproval(p) }
        } else {
            showPickMac()
        }
    }

    /// Waits for the step in flight; for tests.
    public func settled() async {
        while let w = work {
            await w.value
            if work == w { break }
        }
    }

    // MARK: Hello

    public func connectMac() {
        go(.scan)
    }

    public func lookAround() {
        progress.samples = true
        save()
        mode = .samples
        go(.finished)
    }

    /// "Connect yours", from the sample Macs.
    public func connectYours() async {
        progress.samples = false
        save()
        await resume()
        if step == .hello { step = .scan }
    }

    // MARK: Scan and address

    public func enterAddress() {
        go(.address)
    }

    /// One read from the viewfinder. A code for the relay this phone is
    /// signed in to goes straight to the Mac; otherwise to Sign in.
    @discardableResult
    public func scanned(_ text: String) -> ScanOutcome {
        let offer: PairingOffer
        do throws(PairingError) {
            offer = try PairingPayload.parse(text, now: now())
        } catch .offerExpired {
            scanNotice = "This code has expired. Your Mac shows a new one."
            return .expired
        } catch {
            return .ignored
        }
        scanNotice = nil
        self.offer = offer
        relay = offer.relay
        if let account, account.relay == offer.relay {
            pairScanned(offer)
        } else {
            go(.signIn)
        }
        return .found(offer)
    }

    /// The typed relay address; false when it is not one.
    @discardableResult
    public func submitAddress(_ text: String) -> Bool {
        guard let url = RelayAddress.parse(text) else { return false }
        offer = nil
        relay = url
        go(.signIn)
        return true
    }

    // MARK: Sign in

    public func signIn(present: @escaping SignInPresenter) {
        guard let relay else { return }
        failure = nil
        run {
            self.busy = true
            defer { self.busy = false }
            do throws(PairingError) {
                self.account = try await self.pairing.signIn(relay: relay, present: present)
            } catch .cancelled {
                return
            } catch {
                self.failure = error
                return
            }
            if let offer = self.offer {
                await self.pairOffer(offer)
            } else {
                self.showPickMac()
            }
        }
    }

    // MARK: Pick a Mac and approve on it

    public func reloadMacs() {
        showPickMac()
    }

    /// Asks `machine` to let this phone in by code.
    public func choose(_ machine: Machine) {
        failure = nil
        run {
            self.busy = true
            defer { self.busy = false }
            do throws(PairingError) {
                await self.handle(try await self.pairing.pair(machine: machine.short), name: machine.displayName)
            } catch {
                self.failure = error
            }
        }
    }

    /// The code expired before the Mac approved it: ask again.
    public func askAgain() {
        guard let p = pending else { return }
        choose(Machine(short: p.machine, name: p.name))
    }

    /// Back to the viewfinder, with nothing scanned.
    public func scanAgain() {
        offer = nil
        approval = nil
        go(.scan)
    }

    // MARK: Connected

    /// Asks the scanned Mac again (Try again after a failure).
    public func retry() {
        guard let offer else { return }
        pairScanned(offer)
    }

    /// The notification alert was answered (either way), or Connected was
    /// left while the Mac sleeps: on to the language.
    public func notificationsAnswered() {
        progress.notificationsAsked = true
        save()
        go(.language)
    }

    public func registerPushToken(_ token: Data) async {
        guard mode == .live else { return }
        try? await pairing.registerPushToken(token)
    }

    // MARK: Language

    /// The candidates for this phone (`SpeechLanguages.candidates`). With
    /// none, the screen is skipped.
    public func offerLanguages(_ ids: [String]) {
        languages = ids
        if language == nil || !ids.contains(language ?? "") { language = ids.first }
        if ids.isEmpty { finish() }
    }

    /// Done: saves the language and leaves onboarding.
    public func finish() {
        if let language { defaults.set(language, forKey: Self.languageKey) }
        progress.done = true
        save()
        go(.finished)
    }

    // MARK: Back and reset

    public func back() {
        switch step {
        case .scan: go(.hello)
        case .address: go(.scan)
        case .signIn: go(offer == nil ? .address : .scan)
        case .approveOnMac: showPickMac()
        default: break
        }
    }

    /// Settings › Reset this iPhone: keys, credentials and progress gone,
    /// back to Hello.
    public func reset() async {
        cancelWork()
        await pairing.reset()
        defaults.removeObject(forKey: Self.progressKey)
        defaults.removeObject(forKey: Self.languageKey)
        progress = Progress()
        mode = .live
        offer = nil
        relay = nil
        account = nil
        mac = nil
        approval = nil
        pending = nil
        unpaired = []
        failure = nil
        scanNotice = nil
        language = nil
        step = .hello
    }

    // MARK: Private

    private func go(_ next: Step) {
        cancelWork()
        failure = nil
        if next != .scan { scanNotice = nil }
        step = next
    }

    private func run(_ body: @escaping @MainActor () async -> Void) {
        work?.cancel()
        work = Task { await body() }
    }

    private func cancelWork() {
        work?.cancel()
        work = nil
        busy = false
    }

    private func showPickMac() {
        go(.pickMac)
        run {
            self.busy = true
            defer { self.busy = false }
            do throws(PairingError) {
                self.unpaired = try await self.pairing.unpairedMacs()
            } catch {
                self.failure = error
            }
        }
    }

    private func pairScanned(_ offer: PairingOffer) {
        go(.connected)
        run { await self.pairOffer(offer) }
    }

    /// Asks the scanned Mac; while it sleeps, asks again until the code
    /// expires. A Mac that cannot approve by code falls back to the
    /// approval code.
    private func pairOffer(_ offer: PairingOffer) async {
        step = .connected
        approval = .asking
        while !Task.isCancelled {
            do throws(PairingError) {
                await handle(try await pairing.pair(offer: offer), name: offer.name)
                return
            } catch .offerUnsupported {
                do throws(PairingError) {
                    await handle(try await pairing.pair(machine: offer.machine), name: offer.name)
                } catch {
                    approval = .failed(error)
                }
                return
            } catch .macOffline(let machine) {
                approval = .failed(.macOffline(machine: machine))
                guard offer.expires > now() else { return }
                try? await Task.sleep(for: poll)
            } catch {
                approval = .failed(error)
                return
            }
        }
    }

    private func handle(_ result: PairResult, name: String) async {
        switch result {
        case .approved(let machine, let rights):
            let paired = PairedMac(machine: machine, name: name, rights: rights)
            mac = paired
            progress.mac = paired
            progress.pending = nil
            save()
            pending = nil
            approval = .approved(rights: rights)
            step = .connected
        case .pending(let machine, let code, let expires):
            let p = PendingPairing(machine: machine, name: name, code: code, expires: expires)
            pending = p
            progress.pending = p
            save()
            step = .approveOnMac
            busy = false
            await waitForApproval(p)
        }
    }

    /// Asks again every `poll` (the same keys get the same pending request)
    /// until the Mac approves or the code expires.
    private func waitForApproval(_ p: PendingPairing) async {
        while !Task.isCancelled, p.expires > now() {
            try? await Task.sleep(for: poll)
            guard !Task.isCancelled else { return }
            do throws(PairingError) {
                let result = try await pairing.pair(machine: p.machine)
                if case .approved = result {
                    await handle(result, name: p.name)
                    return
                }
            } catch {
                failure = error
                return
            }
        }
    }

    private func save() {
        defaults.set(try? JSONEncoder().encode(progress), forKey: Self.progressKey)
    }

    private struct Progress: Codable {
        var samples = false
        var mac: PairedMac?
        var pending: PendingPairing?
        var notificationsAsked = false
        var done = false
    }
}

/// A Mac that approved this phone, and what it lets the phone do.
public struct PairedMac: Codable, Equatable, Sendable {
    public var machine: String
    public var name: String
    public var rights: [String]

    public init(machine: String, name: String, rights: [String]) {
        self.machine = machine
        self.name = name
        self.rights = rights
    }
}

/// A pairing request the Mac's owner has yet to approve there.
public struct PendingPairing: Codable, Equatable, Sendable {
    public var machine: String
    public var name: String
    /// ABC-DEF, shown on both screens.
    public var code: String
    public var expires: Date

    public init(machine: String, name: String, code: String, expires: Date) {
        self.machine = machine
        self.name = name
        self.code = code
        self.expires = expires
    }
}
