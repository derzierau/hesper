import CryptoKit
import Foundation
import Testing

@testable import HesperLink

@Suite struct KeysTests {
    @Test func createsOnceAndLoadsTheSameKeys() throws {
        let store = MemoryStore()
        let first = try DeviceKeys.load(from: store, enclave: false)
        let again = try DeviceKeys.load(from: store, enclave: false)
        #expect(first.keyBase64 == again.keyBase64)
        #expect(first.strongKeyBase64 == again.strongKeyBase64)
        #expect(first.e2ePublic == again.e2ePublic)
        #expect(first.keyBase64 != first.strongKeyBase64)
        #expect(!first.hardware)
        #expect(first.device.publicKeyDER.count == 91)
        try DeviceKeys.delete(from: store)
        #expect(try DeviceKeys.load(from: store, enclave: false).keyBase64 != first.keyBase64)
    }

    @Test func bindingVerifiesWithTheDeviceKey() throws {
        let keys = try DeviceKeys.load(from: MemoryStore(), enclave: false)
        let sig = try P256.Signing.ECDSASignature(derRepresentation: Data(base64Encoded: try keys.binding())!)
        let device = try P256.Signing.PublicKey(derRepresentation: keys.device.publicKeyDER)
        let strong = try P256.Signing.PublicKey(derRepresentation: keys.strong.publicKeyDER)
        let message = E2E.bindingMessage(static: keys.e2ePublic)
        #expect(device.isValidSignature(sig, for: message))
        #expect(!strong.isValidSignature(sig, for: message))
        #expect(String(decoding: message, as: UTF8.self) == "ghosty-e2e-bind-v1\n" + keys.e2ePublic.base64EncodedString())
    }
}
