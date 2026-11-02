import Foundation
import HesperCore
import Observation

/// Add Mac, from the Macs tab (docs/iphone/onboarding.md, "Adding more
/// Macs"): the owner's Macs on the relay that do not let this phone in
/// yet, each with Pair, and Scan a code.
@MainActor
@Observable
public final class AddMacModel {
    public enum Row: Equatable, Sendable {
        case idle
        case asking
        /// "Waiting for air · ABC-DEF" until that Mac approves.
        case waiting(code: String, expires: Date)
        case approved(rights: [String])
        case failed(PairingError)
    }

    /// On your relay · not paired.
    public private(set) var unpaired: [Machine] = []
    public private(set) var rows: [String: Row] = [:]
    public private(set) var loading = false
    public private(set) var failure: PairingError?

    private let pairing: any PairingService
    private let poll: Duration
    private let now: @Sendable () -> Date
    private var waits: [String: Task<Void, Never>] = [:]

    public init(pairing: any PairingService, poll: Duration = .seconds(5), now: @escaping @Sendable () -> Date = { .now }) {
        self.pairing = pairing
        self.poll = poll
        self.now = now
    }

    public func row(_ machine: String) -> Row { rows[machine] ?? .idle }

    public func load() async {
        loading = true
        defer { loading = false }
        do throws(PairingError) {
            unpaired = try await pairing.unpairedMacs()
            failure = nil
        } catch {
            failure = error
        }
    }

    /// Pair: sends the request and waits, asking again every `poll`, until
    /// the Mac approves or the code expires. Returns when it is settled.
    public func pair(_ machine: String) async {
        waits[machine]?.cancel()
        let task = Task { await self.ask(machine) }
        waits[machine] = task
        await task.value
    }

    /// A scanned code: the Mac approves at once. Nil when it was not a
    /// Hesper code (the viewfinder keeps looking).
    public func scanned(_ text: String) async -> Result<PairResult, PairingError>? {
        let offer: PairingOffer
        do throws(PairingError) {
            offer = try PairingPayload.parse(text, now: now())
        } catch .notAHesperCode {
            return nil
        } catch {
            return .failure(error)
        }
        do throws(PairingError) {
            let result = try await pairing.pair(offer: offer)
            if case .approved(_, let rights) = result { rows[offer.machine] = .approved(rights: rights) }
            return .success(result)
        } catch {
            return .failure(error)
        }
    }

    /// Stops waiting (the screen closed); pending requests stay on the Macs.
    public func stop() {
        for t in waits.values { t.cancel() }
        waits = [:]
    }

    private func ask(_ machine: String) async {
        rows[machine] = .asking
        while !Task.isCancelled {
            do throws(PairingError) {
                switch try await pairing.pair(machine: machine) {
                case .approved(_, let rights):
                    rows[machine] = .approved(rights: rights)
                    return
                case .pending(_, let code, let expires):
                    rows[machine] = .waiting(code: code, expires: expires)
                    try? await Task.sleep(for: poll)
                    if now() >= expires {
                        rows[machine] = .failed(.offerExpired)
                        return
                    }
                }
            } catch {
                rows[machine] = .failed(error)
                return
            }
        }
    }
}
