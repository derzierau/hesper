import CryptoKit
import Foundation
import Synchronization

/// One terminal stream's frames, sealed end to end (Go's e2e.Stream). Both
/// sides derive a key per direction from the stream's secret; the counter
/// is implicit, so a dropped, repeated, reordered or altered frame fails.
/// Seal and open may run concurrently; each direction keeps its own order.
public final class StreamCipher: Sendable {
    /// Frame kinds: terminal bytes, or a resize (`resizePayload`).
    public static let data: UInt8 = 0, resize: UInt8 = 1

    private let send: NoiseCipher
    private let receive: NoiseCipher
    private let ad: Data
    private let sent = Mutex<UInt64>(0)
    private let got = Mutex<UInt64>(0)

    /// Derives the keys with HKDF-SHA256 (salt: the stream ID, info
    /// "ghosty-stream-v1 host" or "… controller"); `host` is true on the
    /// host's side.
    public init(secret: Data, streamID: String, host: Bool = false) throws {
        guard secret.count == 32, !streamID.isEmpty else { throw NoiseError.invalidKey }
        func key(_ side: String) -> NoiseCipher {
            NoiseCipher(
                key: HKDF<SHA256>.deriveKey(
                    inputKeyMaterial: SymmetricKey(data: secret), salt: Data(streamID.utf8),
                    info: Data("ghosty-stream-v1 \(side)".utf8), outputByteCount: 32))
        }
        let fromHost = key("host")
        let fromController = key("controller")
        (send, receive) = host ? (fromHost, fromController) : (fromController, fromHost)
        ad = Data("ghosty-stream-v1\0\(streamID)".utf8)
    }

    /// Encrypts one frame of the given kind with the next counter.
    public func seal(kind: UInt8, payload: Data) -> Data {
        sent.withLock { n in
            defer { n += 1 }
            return send.seal(Data([kind]) + payload, nonce: n, ad: ad)
        }
    }

    /// Decrypts the next frame; throws `E2EError.authentication` when it does
    /// not authenticate at this point of the stream.
    public func open(_ frame: Data) throws -> (kind: UInt8, payload: Data) {
        try got.withLock { n in
            guard let plain = try? receive.open(frame, nonce: n, ad: ad), let kind = plain.first else {
                throw E2EError.authentication
            }
            n += 1
            return (kind, Data(plain.dropFirst()))
        }
    }

    /// A resize: columns and rows, big-endian.
    public static func resizePayload(columns: UInt16, rows: UInt16) -> Data {
        var out = Data()
        withUnsafeBytes(of: columns.bigEndian) { out.append(contentsOf: $0) }
        withUnsafeBytes(of: rows.bigEndian) { out.append(contentsOf: $0) }
        return out
    }
}
