// hesper-keys: this Mac's controller device keys (docs/remote-shell-contract.md, Part K).
//
// Two P-256 signing keys live in the Secure Enclave and never leave it:
//   device         no user interaction; signs ordinary mutating requests
//   device-strong  Touch ID (biometryCurrentSet; .userPresence without
//                  biometry); signs shell.* requests
// Each is stored as its Secure Enclave blob (dataRepresentation, usable only
// on this Mac) in $HESPER_STATE_DIR (default ~/.local/state/hesper) as
// device.key and device-strong.key, mode 0600. A key is created on first use.
//
// Commands (exit 0 on success, 1 with one line on stderr otherwise):
//   hesper-keys info                       JSON: hardware, biometry, software, keys present
//   hesper-keys public [--strong]          base64 DER SubjectPublicKeyInfo
//   hesper-keys sign [--strong] [--reason TEXT] < DIGEST
//                                          base64 DER ECDSA signature of DIGEST
// DIGEST is the SHA-256 digest to sign, exactly 32 raw bytes, or 64 hex
// digits optionally followed by one newline. Anything else is refused.
// --reason is the text of the Touch ID prompt (strong key only).
//
// HESPER_KEYS_SOFTWARE=1 uses software P-256 keys (PEM PKCS#8 files with the
// same names) instead: for tests and machines without a Secure Enclave. A
// software key has no user-presence check, also with --strong.

import CryptoKit
import Foundation
import LocalAuthentication
import Security

struct Failure: Error { let message: String }

func fail(_ message: String) -> Never {
    FileHandle.standardError.write(Data(("hesper-keys: " + message + "\n").utf8))
    exit(1)
}

/// A digest computed by the caller. CryptoKit signs a Digest as given
/// (ECDSA over the 32 bytes), which is what SHA256Digest would do.
struct RawDigest: Digest {
    static var byteCount: Int { 32 }
    let bytes: [UInt8]
    func withUnsafeBytes<R>(_ body: (UnsafeRawBufferPointer) throws -> R) rethrows -> R { try bytes.withUnsafeBytes(body) }
    func makeIterator() -> IndexingIterator<[UInt8]> { bytes.makeIterator() }
    var description: String { "RawDigest" }
}

let environment = ProcessInfo.processInfo.environment
let software = environment["HESPER_KEYS_SOFTWARE"] == "1"

func stateDirectory() throws -> URL {
    let path: String
    if let configured = environment["HESPER_STATE_DIR"], !configured.isEmpty {
        path = configured
    } else {
        path = NSHomeDirectory() + "/.local/state/hesper"
    }
    guard path.hasPrefix("/") else { throw Failure(message: "HESPER_STATE_DIR must be absolute") }
    try FileManager.default.createDirectory(atPath: path, withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])
    return URL(fileURLWithPath: path, isDirectory: true)
}

func keyFile(strong: Bool) throws -> URL {
    try stateDirectory().appendingPathComponent(strong ? "device-strong.key" : "device.key")
}

/// Reads a key file that must be a regular file of this user, not readable by others.
func readPrivate(_ url: URL) throws -> Data? {
    var info = stat()
    if lstat(url.path, &info) != 0 {
        if errno == ENOENT { return nil }
        throw Failure(message: "cannot read \(url.path)")
    }
    guard (info.st_mode & S_IFMT) == S_IFREG else { throw Failure(message: "\(url.path) is not a regular file") }
    guard info.st_uid == getuid() else { throw Failure(message: "\(url.path) belongs to another user") }
    guard (info.st_mode & 0o077) == 0 else { throw Failure(message: "\(url.path) is readable by others; expected mode 0600") }
    guard info.st_size > 0 && info.st_size < 64 * 1024 else { throw Failure(message: "\(url.path) has an invalid size") }
    return try Data(contentsOf: url)
}

/// Writes data to url only if nothing is there yet (link(2) never replaces),
/// so two processes creating a key at once end up using the same one.
func writeNew(_ url: URL, _ data: Data) throws {
    let temp = url.deletingLastPathComponent().appendingPathComponent(".\(url.lastPathComponent).\(getpid()).\(UInt32.random(in: 0...UInt32.max))")
    let fd = open(temp.path, O_WRONLY | O_CREAT | O_EXCL | O_NOFOLLOW, 0o600)
    guard fd >= 0 else { throw Failure(message: "cannot create \(temp.path)") }
    defer { unlink(temp.path) }
    let written = data.withUnsafeBytes { write(fd, $0.baseAddress, $0.count) }
    let synced = fsync(fd)
    close(fd)
    guard written == data.count && synced == 0 else { throw Failure(message: "cannot write \(temp.path)") }
    if link(temp.path, url.path) != 0 && errno != EEXIST {
        throw Failure(message: "cannot store \(url.path)")
    }
}

func biometryAvailable() -> Bool {
    var error: NSError?
    return LAContext().canEvaluatePolicy(.deviceOwnerAuthenticationWithBiometrics, error: &error)
}

enum Key {
    case enclave(SecureEnclave.P256.Signing.PrivateKey)
    case soft(P256.Signing.PrivateKey)

    var publicKey: P256.Signing.PublicKey {
        switch self {
        case .enclave(let k): return k.publicKey
        case .soft(let k): return k.publicKey
        }
    }

    func sign(_ digest: RawDigest) throws -> P256.Signing.ECDSASignature {
        switch self {
        case .enclave(let k): return try k.signature(for: digest)
        case .soft(let k): return try k.signature(for: digest)
        }
    }
}

func newEnclaveKey(strong: Bool) throws -> SecureEnclave.P256.Signing.PrivateKey {
    var error: Unmanaged<CFError>?
    let protection = strong ? kSecAttrAccessibleWhenUnlockedThisDeviceOnly : kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly
    var flags: SecAccessControlCreateFlags = [.privateKeyUsage]
    if strong {
        flags.insert(biometryAvailable() ? .biometryCurrentSet : .userPresence)
    }
    guard let access = SecAccessControlCreateWithFlags(nil, protection, flags, &error) else {
        throw Failure(message: "cannot create the key's access control")
    }
    return try SecureEnclave.P256.Signing.PrivateKey(accessControl: access)
}

/// Loads the key, creating it on first use. reason (strong key only) is the
/// Touch ID prompt shown when the key signs.
func loadKey(strong: Bool, reason: String?) throws -> Key {
    let url = try keyFile(strong: strong)
    if software {
        if try readPrivate(url) == nil {
            try writeNew(url, Data(P256.Signing.PrivateKey().pemRepresentation.utf8))
        }
        guard let data = try readPrivate(url), let pem = String(data: data, encoding: .utf8), pem.hasPrefix("-----BEGIN PRIVATE KEY-----") else {
            throw Failure(message: "\(url.path) is not a software key (unset HESPER_KEYS_SOFTWARE for Secure Enclave keys)")
        }
        return .soft(try P256.Signing.PrivateKey(pemRepresentation: pem))
    }
    guard SecureEnclave.isAvailable else {
        throw Failure(message: "no Secure Enclave on this Mac; set HESPER_KEYS_SOFTWARE=1 for software keys")
    }
    if try readPrivate(url) == nil {
        try writeNew(url, try newEnclaveKey(strong: strong).dataRepresentation)
    }
    guard let blob = try readPrivate(url) else { throw Failure(message: "cannot read \(url.path)") }
    if blob.starts(with: Data("-----BEGIN".utf8)) {
        throw Failure(message: "\(url.path) is a software key; set HESPER_KEYS_SOFTWARE=1 or remove it")
    }
    let context = LAContext()
    if let reason = reason, !reason.isEmpty {
        context.localizedReason = reason
    }
    return .enclave(try SecureEnclave.P256.Signing.PrivateKey(dataRepresentation: blob, authenticationContext: context))
}

func readDigest() throws -> RawDigest {
    var input = Data()
    while input.count <= 66 {
        let chunk = FileHandle.standardInput.availableData
        if chunk.isEmpty { break }
        input.append(chunk)
    }
    if input.count == 32 {
        return RawDigest(bytes: Array(input))
    }
    var text = input
    if text.last == 0x0a { text.removeLast() }
    guard text.count == 64 else { throw Failure(message: "stdin must be a 32-byte digest, raw or as 64 hex digits") }
    var bytes: [UInt8] = []
    var index = text.startIndex
    while index < text.endIndex {
        guard let pair = String(data: text[index..<text.index(index, offsetBy: 2)], encoding: .ascii),
              pair.allSatisfy({ $0.isHexDigit }), let byte = UInt8(pair, radix: 16) else {
            throw Failure(message: "stdin must be a 32-byte digest, raw or as 64 hex digits")
        }
        bytes.append(byte)
        index = text.index(index, offsetBy: 2)
    }
    return RawDigest(bytes: bytes)
}

func run(_ arguments: [String]) throws {
    guard let command = arguments.first else {
        throw Failure(message: "usage: hesper-keys info | public [--strong] | sign [--strong] [--reason TEXT] < DIGEST")
    }
    var strong = false
    var reason: String?
    var rest = Array(arguments.dropFirst())
    while !rest.isEmpty {
        let flag = rest.removeFirst()
        switch flag {
        case "--strong" where command != "info":
            strong = true
        case "--reason" where command == "sign":
            guard !rest.isEmpty else { throw Failure(message: "--reason needs a text") }
            reason = rest.removeFirst()
        default:
            throw Failure(message: "unknown argument \(flag)")
        }
    }
    switch command {
    case "info":
        let fm = FileManager.default
        let info: [String: Any] = [
            "hardware": !software && SecureEnclave.isAvailable,
            "secureEnclave": SecureEnclave.isAvailable,
            "biometry": biometryAvailable(),
            "software": software,
            "device": fm.fileExists(atPath: try keyFile(strong: false).path),
            "deviceStrong": fm.fileExists(atPath: try keyFile(strong: true).path),
        ]
        let data = try JSONSerialization.data(withJSONObject: info, options: [.sortedKeys])
        print(String(decoding: data, as: UTF8.self))
    case "public":
        print(try loadKey(strong: strong, reason: nil).publicKey.derRepresentation.base64EncodedString())
    case "sign":
        let digest = try readDigest()
        let key = try loadKey(strong: strong, reason: strong ? (reason ?? "sign a Hesper request") : nil)
        print(try key.sign(digest).derRepresentation.base64EncodedString())
    default:
        throw Failure(message: "unknown command \(command)")
    }
}

do {
    try run(Array(CommandLine.arguments.dropFirst()))
} catch let failure as Failure {
    fail(failure.message)
} catch {
    fail("\(error.localizedDescription)")
}
