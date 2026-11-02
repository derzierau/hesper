import CryptoKit
import Foundation

// Noise_IK_25519_ChaChaPoly_BLAKE2s (noiseprotocol.org, revision 34), as
// relay/pkg/e2e runs it with github.com/flynn/noise. The controller is the
// initiator and knows the host's static key in advance.

public enum NoiseError: Error, Equatable {
    /// A key is not a valid X25519 key, or a DH produced a low-order result.
    case invalidKey
    /// A message is too short to hold what the pattern says it holds.
    case malformed
    /// A ciphertext did not authenticate (wrong key, altered on the way).
    case decryptFailed
    /// A message was written or read out of the pattern's order.
    case outOfOrder
}

/// A CipherState key: ChaChaPoly with Noise's nonce (4 zero bytes ‖ UInt64
/// little-endian).
public struct NoiseCipher: Sendable {
    let key: SymmetricKey

    public init(key: SymmetricKey) { self.key = key }

    public func seal(_ plaintext: Data, nonce: UInt64, ad: Data) -> Data {
        // A 12-byte nonce and a 32-byte key cannot fail.
        let box = try! ChaChaPoly.seal(plaintext, using: key, nonce: Self.nonce(nonce), authenticating: ad)
        return box.ciphertext + box.tag
    }

    public func open(_ ciphertext: Data, nonce: UInt64, ad: Data) throws -> Data {
        guard ciphertext.count >= 16 else { throw NoiseError.decryptFailed }
        do {
            let box = try ChaChaPoly.SealedBox(
                nonce: Self.nonce(nonce), ciphertext: ciphertext.dropLast(16), tag: ciphertext.suffix(16))
            return try ChaChaPoly.open(box, using: key, authenticating: ad)
        } catch {
            throw NoiseError.decryptFailed
        }
    }

    private static func nonce(_ n: UInt64) -> ChaChaPoly.Nonce {
        var bytes = Data(count: 4)
        withUnsafeBytes(of: n.littleEndian) { bytes.append(contentsOf: $0) }
        return try! ChaChaPoly.Nonce(data: bytes)
    }
}

/// The initiator (controller) of IK: `-> e, es, s, ss` then `<- e, ee, se`.
public struct NoiseInitiator: Sendable {
    private var symmetric: SymmetricState
    private let s: Curve25519.KeyAgreement.PrivateKey
    private let e: Curve25519.KeyAgreement.PrivateKey
    private let rs: Data
    private var written = false
    private var done = false

    /// `ephemeral` is for test vectors only; nil draws a fresh one.
    public init(
        static: Curve25519.KeyAgreement.PrivateKey, remoteStatic: Data, prologue: Data,
        ephemeral: Curve25519.KeyAgreement.PrivateKey? = nil
    ) throws {
        guard remoteStatic.count == 32, (try? Curve25519.KeyAgreement.PublicKey(rawRepresentation: remoteStatic)) != nil else {
            throw NoiseError.invalidKey
        }
        s = `static`
        e = ephemeral ?? .init()
        rs = Data(remoteStatic)
        symmetric = SymmetricState()
        symmetric.mixHash(prologue)
        symmetric.mixHash(rs)
    }

    public mutating func writeMessage(payload: Data) throws -> Data {
        guard !written else { throw NoiseError.outOfOrder }
        var st = symmetric
        var message = e.publicKey.rawRepresentation
        st.mixHash(message)
        st.mixKey(try dh(e, rs))
        message += st.encryptAndHash(s.publicKey.rawRepresentation)
        st.mixKey(try dh(s, rs))
        message += st.encryptAndHash(payload)
        symmetric = st
        written = true
        return message
    }

    public mutating func readMessage(_ message: Data) throws -> (payload: Data, send: NoiseCipher, receive: NoiseCipher) {
        guard written, !done else { throw NoiseError.outOfOrder }
        guard message.count >= 32 + 16 else { throw NoiseError.malformed }
        let message = Data(message)
        var st = symmetric
        let re = message.prefix(32)
        st.mixHash(re)
        st.mixKey(try dh(e, re))
        st.mixKey(try dh(s, re))
        let payload = try st.decryptAndHash(message.dropFirst(32))
        let (one, two) = st.split()
        symmetric = st
        done = true
        return (payload, one, two)
    }
}

/// The responder (host) of IK; the app uses it only in tests and a fake host.
public struct NoiseResponder: Sendable {
    private var symmetric: SymmetricState
    private let s: Curve25519.KeyAgreement.PrivateKey
    private let e: Curve25519.KeyAgreement.PrivateKey
    private var re: Data?
    private var done = false
    /// The initiator's static key, once its first message was read.
    public private(set) var remoteStatic: Data?

    public init(static: Curve25519.KeyAgreement.PrivateKey, prologue: Data, ephemeral: Curve25519.KeyAgreement.PrivateKey? = nil) {
        s = `static`
        e = ephemeral ?? .init()
        symmetric = SymmetricState()
        symmetric.mixHash(prologue)
        symmetric.mixHash(s.publicKey.rawRepresentation)
    }

    /// Reads the first message and returns its payload; then `remoteStatic`
    /// is set.
    public mutating func readMessage(_ message: Data) throws -> Data {
        guard re == nil else { throw NoiseError.outOfOrder }
        guard message.count >= 32 + 48 + 16 else { throw NoiseError.malformed }
        let message = Data(message)
        var st = symmetric
        let re = message.prefix(32)
        st.mixHash(re)
        st.mixKey(try dh(s, re))
        let rs = try st.decryptAndHash(message.dropFirst(32).prefix(48))
        st.mixKey(try dh(s, rs))
        let payload = try st.decryptAndHash(message.dropFirst(80))
        symmetric = st
        self.re = Data(re)
        remoteStatic = rs
        return payload
    }

    public mutating func writeMessage(payload: Data) throws -> (message: Data, send: NoiseCipher, receive: NoiseCipher) {
        guard let re, let rs = remoteStatic, !done else { throw NoiseError.outOfOrder }
        var st = symmetric
        var message = e.publicKey.rawRepresentation
        st.mixHash(message)
        st.mixKey(try dh(e, re))
        st.mixKey(try dh(e, rs))
        message += st.encryptAndHash(payload)
        let (one, two) = st.split()
        symmetric = st
        done = true
        return (message, two, one)
    }
}

/// Noise's SymmetricState with its CipherState.
private struct SymmetricState: Sendable {
    static let name = Data("Noise_IK_25519_ChaChaPoly_BLAKE2s".utf8)

    var ck: Data
    var h: Data
    var k: NoiseCipher?
    var n: UInt64 = 0

    init() {
        // The name is longer than HASHLEN, so h starts as its hash.
        h = BLAKE2s.hash(Self.name)
        ck = h
    }

    mutating func mixHash(_ data: Data) { h = BLAKE2s.hash(h + data) }

    mutating func mixKey(_ ikm: Data) {
        let (ck, key) = BLAKE2s.hkdf(chainingKey: ck, ikm)
        self.ck = ck
        k = NoiseCipher(key: SymmetricKey(data: key))
        n = 0
    }

    mutating func encryptAndHash(_ plaintext: Data) -> Data {
        guard let k else {
            mixHash(plaintext)
            return plaintext
        }
        let ciphertext = k.seal(plaintext, nonce: n, ad: h)
        n += 1
        mixHash(ciphertext)
        return ciphertext
    }

    mutating func decryptAndHash(_ ciphertext: Data) throws -> Data {
        let ciphertext = Data(ciphertext)
        guard let k else {
            mixHash(ciphertext)
            return ciphertext
        }
        let plaintext = try k.open(ciphertext, nonce: n, ad: h)
        n += 1
        mixHash(ciphertext)
        return plaintext
    }

    func split() -> (NoiseCipher, NoiseCipher) {
        let (one, two) = BLAKE2s.hkdf(chainingKey: ck, Data())
        return (NoiseCipher(key: SymmetricKey(data: one)), NoiseCipher(key: SymmetricKey(data: two)))
    }
}

private func dh(_ key: Curve25519.KeyAgreement.PrivateKey, _ publicKey: Data) throws -> Data {
    do {
        let peer = try Curve25519.KeyAgreement.PublicKey(rawRepresentation: publicKey)
        return try key.sharedSecretFromKeyAgreement(with: peer).withUnsafeBytes { Data($0) }
    } catch {
        throw NoiseError.invalidKey
    }
}
