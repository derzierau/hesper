import CryptoKit
import Foundation

/// The device-key signature of a host request (relay/docs/protocol.md,
/// "Device keys"): `auth` on every request but `snapshot`, `ping`,
/// `devices.request` and the channel's own.
public struct RequestAuth: Codable, Equatable, Sendable {
    public var device: String
    public var ts: Int64
    public var nonce: String
    public var sig: String

    public init(device: String, ts: Int64, nonce: String, sig: String) {
        self.device = device
        self.ts = ts
        self.nonce = nonce
        self.sig = sig
    }
}

/// What a request's signature covers, as `pkg/devicekey` builds it.
public enum RequestSigning {
    /// Starts every signed message. Wire name kept from before the rename.
    public static let label = "ghosty-req-v1"

    /// The text that is hashed and signed. `params` must be normalized.
    public static func message(machine: String, method: String, ts: Int64, nonce: String, params: Data) -> Data {
        let sum = Data(SHA256.hash(data: params)).hex
        return Data([label, machine, method, String(ts), nonce, sum].joined(separator: "\n").utf8)
    }

    /// Signs a request for `machine` with the device key (or the strong
    /// key). `params` must be normalized; they are the bytes to send.
    public static func sign(
        _ params: Data, machine: String, method: String, device: String, key: any SigningKey, now: Date = Date()
    ) throws -> RequestAuth {
        let nonce = Data((0..<16).map { _ in UInt8.random(in: 0...255) }).base64EncodedString()
        let ts = Int64((now.timeIntervalSince1970 * 1000).rounded(.down))
        let sig = try key.signature(for: message(machine: machine, method: method, ts: ts, nonce: nonce, params: params))
        return RequestAuth(device: device, ts: ts, nonce: nonce, sig: sig.base64EncodedString())
    }
}

/// Request params in the form that is signed and sent: compact JSON with
/// `<`, `>`, `&`, U+2028 and U+2029 escaped, as Go's `encoding/json`
/// writes a raw message, so the relay's re-encoding leaves it unchanged.
/// Params with a repeated object key, invalid UTF-8 or more than one value
/// are refused, as the host refuses them.
public enum Params {
    public enum Invalid: Error, Equatable {
        case notJSON
        case repeatedKey(String)
        case tooDeep
    }

    /// Normalizes raw JSON; empty input is `{}`.
    public static func normalize(_ raw: Data) throws(Invalid) -> Data {
        var p = Parser(bytes: Array(raw))
        p.skipSpace()
        if p.i == p.bytes.count { return Data("{}".utf8) }
        guard String(data: raw, encoding: .utf8) != nil else { throw .notJSON }
        try p.value(depth: 0)
        p.skipSpace()
        guard p.i == p.bytes.count else { throw .notJSON }
        return Data(p.out)
    }

    /// Encodes a value with `JSONEncoder` and normalizes it.
    public static func encode(_ value: some Encodable) throws -> Data {
        try normalize(JSONEncoder().encode(value))
    }

    private struct Parser {
        let bytes: [UInt8]
        var i = 0
        var out: [UInt8] = []

        init(bytes: [UInt8]) {
            self.bytes = bytes
            out.reserveCapacity(bytes.count)
        }

        mutating func skipSpace() {
            while i < bytes.count, [0x20, 0x09, 0x0a, 0x0d].contains(bytes[i]) { i += 1 }
        }

        mutating func value(depth: Int) throws(Invalid) {
            guard depth <= 64 else { throw .tooDeep }
            skipSpace()
            guard i < bytes.count else { throw .notJSON }
            switch bytes[i] {
            case UInt8(ascii: "{"):
                out.append(bytes[i])
                i += 1
                var seen: Set<String> = []
                skipSpace()
                if i < bytes.count, bytes[i] == UInt8(ascii: "}") {
                    out.append(bytes[i])
                    i += 1
                    return
                }
                while true {
                    skipSpace()
                    guard i < bytes.count, bytes[i] == UInt8(ascii: "\"") else { throw .notJSON }
                    let start = out.count
                    try string()
                    let key = Self.decodeString(Array(out[start...]))
                    guard let key else { throw .notJSON }
                    guard seen.insert(key).inserted else { throw .repeatedKey(key) }
                    skipSpace()
                    guard i < bytes.count, bytes[i] == UInt8(ascii: ":") else { throw .notJSON }
                    out.append(bytes[i])
                    i += 1
                    try value(depth: depth + 1)
                    skipSpace()
                    guard i < bytes.count else { throw .notJSON }
                    out.append(bytes[i])
                    i += 1
                    if bytes[i - 1] == UInt8(ascii: "}") { return }
                    guard bytes[i - 1] == UInt8(ascii: ",") else { throw .notJSON }
                }
            case UInt8(ascii: "["):
                out.append(bytes[i])
                i += 1
                skipSpace()
                if i < bytes.count, bytes[i] == UInt8(ascii: "]") {
                    out.append(bytes[i])
                    i += 1
                    return
                }
                while true {
                    try value(depth: depth + 1)
                    skipSpace()
                    guard i < bytes.count else { throw .notJSON }
                    out.append(bytes[i])
                    i += 1
                    if bytes[i - 1] == UInt8(ascii: "]") { return }
                    guard bytes[i - 1] == UInt8(ascii: ",") else { throw .notJSON }
                }
            case UInt8(ascii: "\""):
                try string()
            default:
                try scalar()
            }
        }

        /// Copies a string literal, escaping what Go's HTMLEscape escapes.
        mutating func string() throws(Invalid) {
            out.append(bytes[i])
            i += 1
            while i < bytes.count {
                let b = bytes[i]
                switch b {
                case UInt8(ascii: "\""):
                    out.append(b)
                    i += 1
                    return
                case UInt8(ascii: "\\"):
                    guard i + 1 < bytes.count else { throw .notJSON }
                    out.append(contentsOf: bytes[i...i + 1])
                    i += 2
                case UInt8(ascii: "<"), UInt8(ascii: ">"), UInt8(ascii: "&"):
                    out.append(contentsOf: Array(String(format: "\\u00%02x", b).utf8))
                    i += 1
                case 0xe2 where i + 2 < bytes.count && bytes[i + 1] == 0x80 && (bytes[i + 2] == 0xa8 || bytes[i + 2] == 0xa9):
                    out.append(contentsOf: Array((bytes[i + 2] == 0xa8 ? "\\u2028" : "\\u2029").utf8))
                    i += 3
                case 0..<0x20:
                    throw .notJSON
                default:
                    out.append(b)
                    i += 1
                }
            }
            throw .notJSON
        }

        /// A number, `true`, `false` or `null`, checked by JSONSerialization.
        mutating func scalar() throws(Invalid) {
            let start = i
            while i < bytes.count, !Set(",]} \t\n\r".utf8).contains(bytes[i]) { i += 1 }
            let token = Array(bytes[start..<i])
            guard !token.isEmpty,
                (try? JSONSerialization.jsonObject(with: Data(token), options: .fragmentsAllowed)) != nil
            else { throw .notJSON }
            out.append(contentsOf: token)
        }

        static func decodeString(_ literal: [UInt8]) -> String? {
            try? JSONSerialization.jsonObject(with: Data(literal), options: .fragmentsAllowed) as? String
        }
    }
}
