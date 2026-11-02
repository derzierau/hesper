import CryptoKit
import Foundation
import Security
import Synchronization

/// A P-256 key that signs: ECDSA over SHA-256 of the message, DER.
public protocol SigningKey: Sendable {
    /// The public key as DER SubjectPublicKeyInfo (91 bytes).
    var publicKeyDER: Data { get }
    func signature(for message: Data) throws -> Data
}

/// Where the phone keeps its secrets. The app uses `KeychainStore`; tests
/// and previews `MemoryStore`.
public protocol SecretStore: Sendable {
    func read(_ account: String) throws -> Data?
    func write(_ data: Data, account: String) throws
    func delete(_ account: String) throws
}

public enum KeyError: Error, Equatable {
    case keychain(OSStatus)
    case invalidKey(String)
}

/// Generic passwords in the Keychain, this device only, after first
/// unlock (the notification extension decrypts while the phone is locked).
public struct KeychainStore: SecretStore {
    public let service: String

    public init(service: String) { self.service = service }

    private func query(_ account: String) -> [String: Any] {
        [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account,
            kSecUseDataProtectionKeychain as String: true,
        ]
    }

    public func read(_ account: String) throws -> Data? {
        var q = query(account)
        q[kSecReturnData as String] = true
        var out: AnyObject?
        let status = SecItemCopyMatching(q as CFDictionary, &out)
        if status == errSecItemNotFound { return nil }
        guard status == errSecSuccess else { throw KeyError.keychain(status) }
        return out as? Data
    }

    public func write(_ data: Data, account: String) throws {
        let update = [kSecValueData as String: data] as CFDictionary
        var status = SecItemUpdate(query(account) as CFDictionary, update)
        if status == errSecItemNotFound {
            var q = query(account)
            q[kSecValueData as String] = data
            q[kSecAttrAccessible as String] = kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly
            status = SecItemAdd(q as CFDictionary, nil)
        }
        guard status == errSecSuccess else { throw KeyError.keychain(status) }
    }

    public func delete(_ account: String) throws {
        let status = SecItemDelete(query(account) as CFDictionary)
        guard status == errSecSuccess || status == errSecItemNotFound else { throw KeyError.keychain(status) }
    }
}

/// Secrets in memory, for tests and previews.
public final class MemoryStore: SecretStore {
    private let items = Mutex<[String: Data]>([:])

    public init() {}

    public func read(_ account: String) throws -> Data? { items.withLock { $0[account] } }
    public func write(_ data: Data, account: String) throws { items.withLock { $0[account] = data } }
    public func delete(_ account: String) throws { items.withLock { $0[account] = nil } }
}

/// A software P-256 key (simulator, Mac tests, devices without an enclave).
public struct SoftwareSigningKey: SigningKey {
    public let key: P256.Signing.PrivateKey

    public init(_ key: P256.Signing.PrivateKey = .init()) { self.key = key }

    public var publicKeyDER: Data { key.publicKey.derRepresentation }

    public func signature(for message: Data) throws -> Data {
        try key.signature(for: message).derRepresentation
    }
}

/// A Secure Enclave P-256 key. The strong key's access control asks for
/// Face ID or Touch ID at each signature.
public struct EnclaveSigningKey: SigningKey {
    public let key: SecureEnclave.P256.Signing.PrivateKey

    public init(_ key: SecureEnclave.P256.Signing.PrivateKey) { self.key = key }

    public var publicKeyDER: Data { key.publicKey.derRepresentation }

    public func signature(for message: Data) throws -> Data {
        try key.signature(for: message).derRepresentation
    }
}

/// The phone's keys (relay/docs/protocol.md, "Device keys" and
/// "End-to-end channel"): the device key signs requests and binds the
/// end-to-end key, the strong key (biometry) signs starting a shell, the
/// X25519 static key opens the channel. Created on first use; creating
/// them asks for nothing.
public struct DeviceKeys: Sendable {
    public let device: any SigningKey
    public let strong: any SigningKey
    public let e2e: Curve25519.KeyAgreement.PrivateKey
    /// Both P-256 keys live in the Secure Enclave (`hardware` in
    /// `devices.request`).
    public let hardware: Bool

    public init(device: any SigningKey, strong: any SigningKey, e2e: Curve25519.KeyAgreement.PrivateKey, hardware: Bool) {
        self.device = device
        self.strong = strong
        self.e2e = e2e
        self.hardware = hardware
    }

    enum Account {
        static let device = "device-key"
        static let strong = "strong-key"
        static let e2e = "e2e-key"
        static let all = [device, strong, e2e]
    }

    /// Loads the keys from `store`, creating the missing ones. With
    /// `enclave` (the default where there is one) new P-256 keys are made
    /// in the Secure Enclave and the store keeps their encrypted blobs.
    public static func load(from store: any SecretStore, enclave: Bool = SecureEnclave.isAvailable) throws -> DeviceKeys {
        let device = try signingKey(store, account: Account.device, enclave: enclave, biometry: false)
        let strong = try signingKey(store, account: Account.strong, enclave: enclave, biometry: true)
        let e2e: Curve25519.KeyAgreement.PrivateKey
        if let raw = try store.read(Account.e2e) {
            e2e = try Curve25519.KeyAgreement.PrivateKey(rawRepresentation: raw)
        } else {
            e2e = .init()
            try store.write(e2e.rawRepresentation, account: Account.e2e)
        }
        return DeviceKeys(device: device.key, strong: strong.key, e2e: e2e, hardware: device.hardware && strong.hardware)
    }

    /// Forgets every key.
    public static func delete(from store: any SecretStore) throws {
        for account in Account.all { try store.delete(account) }
    }

    /// The stored form: one tag byte (0 software, 1 enclave) and the key.
    private static func signingKey(
        _ store: any SecretStore, account: String, enclave: Bool, biometry: Bool
    ) throws -> (key: any SigningKey, hardware: Bool) {
        if let stored = try store.read(account), let tag = stored.first {
            let body = stored.dropFirst()
            if tag == 1 {
                return (EnclaveSigningKey(try SecureEnclave.P256.Signing.PrivateKey(dataRepresentation: body)), true)
            }
            return (SoftwareSigningKey(try P256.Signing.PrivateKey(rawRepresentation: body)), false)
        }
        if enclave {
            var flags: SecAccessControlCreateFlags = [.privateKeyUsage]
            if biometry { flags.insert(.biometryCurrentSet) }
            var error: Unmanaged<CFError>?
            guard
                let access = SecAccessControlCreateWithFlags(
                    nil, kSecAttrAccessibleWhenUnlockedThisDeviceOnly, flags, &error)
            else { throw KeyError.invalidKey("access control: \(String(describing: error?.takeRetainedValue()))") }
            let key = try SecureEnclave.P256.Signing.PrivateKey(accessControl: access)
            try store.write(Data([1]) + key.dataRepresentation, account: account)
            return (EnclaveSigningKey(key), true)
        }
        let key = P256.Signing.PrivateKey()
        try store.write(Data([0]) + key.rawRepresentation, account: account)
        return (SoftwareSigningKey(key), false)
    }

    /// Standard base64 of the device key's SubjectPublicKeyInfo (`key`).
    public var keyBase64: String { device.publicKeyDER.base64EncodedString() }
    /// The same for the strong key (`strongKey`).
    public var strongKeyBase64: String { strong.publicKeyDER.base64EncodedString() }
    /// The X25519 static public key (32 bytes).
    public var e2ePublic: Data { e2e.publicKey.rawRepresentation }

    /// `binding`: the device key's signature of the end-to-end key, so a
    /// host accepts the key only from the device it approved.
    public func binding() throws -> String {
        try device.signature(for: E2EBinding.message(static: e2ePublic)).base64EncodedString()
    }
}

/// What `binding` signs: `"ghosty-e2e-bind-v1\n" + base64(static key)`;
/// the signature hashes it with SHA-256.
public enum E2EBinding {
    public static func message(static key: Data) -> Data {
        Data(("ghosty-e2e-bind-v1\n" + key.base64EncodedString()).utf8)
    }
}
