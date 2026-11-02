import Foundation

/// The text a Mac's pairing code holds (docs/iphone/onboarding.md,
/// "Interfaces"): `HESPER1:` and base64url, without padding, of compact
/// JSON. Not a URL, so the system camera shows it as text and the secret
/// never travels as a link.
public enum PairingPayload {
    public static let prefix = "HESPER1:"

    /// The offer in `text`. Throws `notAHesperCode` for anything that is
    /// not a version 1 code and `offerExpired` once it has expired.
    public static func parse(_ text: String, now: Date = .now) throws(PairingError) -> PairingOffer {
        let text = text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard text.hasPrefix(prefix), let data = base64urlDecoded(String(text.dropFirst(prefix.count))),
            let wire = try? JSONDecoder().decode(Wire.self, from: data), wire.v == 1,
            let relay = URL(string: wire.relay), relay.scheme == "https" || relay.scheme == "http", relay.host() != nil,
            !wire.machine.isEmpty, !wire.name.isEmpty,
            let hostKey = Data(base64Encoded: wire.hostKey), hostKey.count == 32,
            let secret = base64urlDecoded(wire.offer), secret.count == 16
        else { throw .notAHesperCode }
        let expires = Date(timeIntervalSince1970: TimeInterval(wire.expires))
        guard expires > now else { throw .offerExpired }
        return PairingOffer(
            relay: relay, machine: wire.machine, name: wire.name, hostKey: hostKey, rights: wire.rights, secret: secret, expires: expires)
    }

    /// The code for `offer`, as the Mac writes it; for fixtures and tests.
    public static func encode(_ offer: PairingOffer) -> String {
        // In the Mac's key order, which JSONEncoder does not keep.
        let fields: [(String, any Encodable)] = [
            ("v", 1), ("relay", offer.relay.absoluteString), ("machine", offer.machine), ("name", offer.name),
            ("hostKey", offer.hostKey.base64EncodedString()), ("rights", offer.rights), ("offer", base64url(offer.secret)),
            ("expires", Int64(offer.expires.timeIntervalSince1970)),
        ]
        let encoder = JSONEncoder()
        encoder.outputFormatting = .withoutEscapingSlashes
        // Encoding plain strings and numbers cannot fail.
        let body = fields.map { key, value in "\"\(key)\":" + String(decoding: try! encoder.encode(value), as: UTF8.self) }
        return prefix + base64url(Data("{\(body.joined(separator: ","))}".utf8))
    }

    private struct Wire: Decodable {
        var v: Int
        var relay: String
        var machine: String
        var name: String
        var hostKey: String
        var rights: [String]
        var offer: String
        var expires: Int64
    }

    static func base64url(_ data: Data) -> String {
        data.base64EncodedString().replacingOccurrences(of: "+", with: "-").replacingOccurrences(of: "/", with: "_")
            .replacingOccurrences(of: "=", with: "")
    }

    static func base64urlDecoded(_ s: String) -> Data? {
        guard !s.isEmpty, !s.contains("="), !s.contains("+"), !s.contains("/") else { return nil }
        var b = s.replacingOccurrences(of: "-", with: "+").replacingOccurrences(of: "_", with: "/")
        b += String(repeating: "=", count: (4 - b.count % 4) % 4)
        return Data(base64Encoded: b)
    }
}
