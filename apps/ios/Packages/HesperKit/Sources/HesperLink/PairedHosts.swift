import CryptoKit
import Foundation

/// A Mac that approved this phone, or that it asked: its end-to-end key,
/// pinned on first sight (as `trusted-hosts.json` on a Mac controller),
/// and the rights it granted.
public struct PairedHost: Codable, Equatable, Sendable {
    public var machine: String
    public var name: String
    public var hostKey: Data
    /// Empty while the approval is pending.
    public var rights: [String]
    public var approved: Bool

    public init(machine: String, name: String, hostKey: Data, rights: [String] = [], approved: Bool = false) {
        self.machine = machine
        self.name = name
        self.hostKey = hostKey
        self.rights = rights
        self.approved = approved
    }
}

/// The pinned hosts, in the secret store (one JSON item).
public struct PairedHosts: Sendable {
    let store: any SecretStore
    static let account = "paired-hosts"

    public init(store: any SecretStore) { self.store = store }

    public func all() -> [String: PairedHost] {
        guard let data = try? store.read(Self.account) else { return [:] }
        return (try? JSONDecoder().decode([String: PairedHost].self, from: data)) ?? [:]
    }

    public func save(_ host: PairedHost) throws {
        var hosts = all()
        hosts[host.machine] = host
        try store.write(JSONEncoder().encode(hosts), account: Self.account)
    }

    public func clear() { try? store.delete(Self.account) }

    /// Pins `key` for `machine`, or checks it against the pinned one.
    /// A different key is `key_changed`: someone between the phone and the
    /// Mac (the relay) may be swapping it.
    public func pin(machine: String, name: String, key: Data) throws -> PairedHost {
        if let known = all()[machine] {
            guard known.hostKey == key else { throw RelayError(code: "key_changed", message: "\(name) presents a different key") }
            return known
        }
        let host = PairedHost(machine: machine, name: name, hostKey: key)
        try save(host)
        return host
    }
}

/// `offerProof` (docs/iphone/onboarding.md, "Interfaces"): HMAC-SHA256,
/// keyed with the offer's 16 secret bytes, over the request's fields. The
/// secret itself never leaves the phone.
public enum OfferProof {
    public static func make(secret: Data, machine: String, name: String, key: String, strongKey: String, e2eKey: String?) -> String {
        let message = ["hesper-offer-v1", machine, name, key, strongKey, e2eKey ?? ""].joined(separator: "\n")
        let mac = HMAC<SHA256>.authenticationCode(for: Data(message.utf8), using: SymmetricKey(data: secret))
        return Data(mac).base64EncodedString()
    }
}

/// The code both sides show while a Mac approves by code: the first 30
/// bits of SHA-256(host key ‖ key DER ‖ strong key DER) in base32, as
/// `ABC-DEF` (`devicekey.ApprovalCode`). The phone computes it from the
/// keys it holds, so a relay that swaps a key shows a different code.
public enum ApprovalCode {
    public static func make(hostKey: Data, key: Data, strongKey: Data) -> String {
        let digest = [UInt8](SHA256.hash(data: hostKey + key + strongKey))
        let alphabet = Array("ABCDEFGHIJKLMNOPQRSTUVWXYZ234567")
        // 30 bits: the first four bytes are enough.
        let bits = digest[0...3].reduce(UInt32(0)) { $0 << 8 | UInt32($1) }
        let chars = (0..<6).map { alphabet[Int((bits >> UInt32(27 - 5 * $0)) & 31)] }
        return String(chars[0..<3]) + "-" + String(chars[3..<6])
    }
}
