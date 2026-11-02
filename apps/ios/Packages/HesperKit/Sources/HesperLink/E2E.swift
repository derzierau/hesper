import CryptoKit
import Foundation
import Synchronization

/// The end-to-end channel between the app and a host (relay/pkg/e2e,
/// relay/docs/protocol.md "End-to-end channel"): the relay forwards its
/// frames and can neither read nor alter them.
public enum E2E {
    /// The channel's version; both handshake payloads carry it.
    public static let version = 1
    /// The replay window: how far behind the newest counter a frame may arrive.
    static let window: UInt64 = 1024

    /// Binds a handshake to the host's machine ID and the controller's relay
    /// device ID. The wire name stays "ghosty-e2e-v1".
    public static func prologue(machine: String, controller: String) -> Data {
        Data("ghosty-e2e-v1\0\(machine)\0\(controller)".utf8)
    }

    /// What the device key signs to bind its static X25519 key:
    /// "ghosty-e2e-bind-v1\n" + base64(static). ECDSA hashes it with SHA-256.
    public static func bindingMessage(static: Data) -> Data {
        Data("ghosty-e2e-bind-v1\n\(`static`.base64EncodedString())".utf8)
    }

    /// SHA-256 of `bindingMessage`: Go's `e2e.BindingDigest`.
    public static func bindingDigest(static: Data) -> Data {
        Data(SHA256.hash(data: bindingMessage(static: `static`)))
    }
}

public enum E2EError: Error, Equatable {
    /// The frame's counter was seen before or fell out of the window.
    case replay
    /// The frame did not authenticate (altered on the way, wrong key).
    case authentication
}

/// An established channel (Go's e2e.Session): explicit counters, associated
/// data "ghosty-e2e-v1\0" + id, and a replay window of 1024 frames.
public final class E2ESession: Sendable {
    public let id: String
    /// The peer's static key.
    public let peer: Data
    private let send: NoiseCipher
    private let receive: NoiseCipher
    private let ad: Data
    private let state = Mutex(State())

    private struct State {
        var next: UInt64 = 0
        var window = ReplayWindow()
    }

    public init(id: String, send: NoiseCipher, receive: NoiseCipher, peer: Data) {
        self.id = id
        self.peer = peer
        self.send = send
        self.receive = receive
        ad = Data("ghosty-e2e-v1\0\(id)".utf8)
    }

    /// How many frames this side sealed.
    public var sent: UInt64 { state.withLock { $0.next } }

    /// Encrypts plaintext with the next counter.
    public func seal(_ plaintext: Data) -> (counter: UInt64, ciphertext: Data) {
        let n = state.withLock { s in
            defer { s.next += 1 }
            return s.next
        }
        return (n, send.seal(plaintext, nonce: n, ad: ad))
    }

    /// Authenticates and decrypts a frame and records its counter.
    public func open(counter: UInt64, ciphertext: Data) throws -> Data {
        // Noise reserves the largest nonce.
        guard counter < .max else { throw E2EError.replay }
        return try state.withLock { s in
            guard s.window.fresh(counter) else { throw E2EError.replay }
            guard let plaintext = try? receive.open(ciphertext, nonce: counter, ad: ad) else { throw E2EError.authentication }
            s.window.mark(counter)
            return plaintext
        }
    }
}

/// A sliding window of `E2E.window` counters below the newest one, as Go's
/// `replay` (and WireGuard) keep it.
struct ReplayWindow {
    private var started = false
    private var max: UInt64 = 0
    private var bits = [UInt64](repeating: 0, count: Int(E2E.window / 64))

    func fresh(_ n: UInt64) -> Bool {
        if !started || n > max { return true }
        if max - n >= E2E.window { return false }
        return bits[Int((n % E2E.window) / 64)] & (1 << (n % 64)) == 0
    }

    mutating func mark(_ n: UInt64) {
        if !started {
            started = true
            max = n
            bits = [UInt64](repeating: 0, count: bits.count)
        } else if n > max {
            if n - max >= E2E.window {
                bits = [UInt64](repeating: 0, count: bits.count)
            } else {
                var i = max + 1
                while i <= n {
                    bits[Int((i % E2E.window) / 64)] &= ~(1 << (i % 64))
                    i += 1
                }
            }
            max = n
        }
        bits[Int((n % E2E.window) / 64)] |= 1 << (n % 64)
    }
}
