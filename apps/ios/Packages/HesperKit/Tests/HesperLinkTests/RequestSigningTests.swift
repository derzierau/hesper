import CryptoKit
import Foundation
import Testing

@testable import HesperLink

/// The golden vector of relay/pkg/devicekey/devicekey_test.go.
@Suite struct RequestSigningTests {
    static let publicKey =
        "MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAEzl43zIIDgn77ReDsdfJBbUZPR4rW1lPcHfHSg8qfTIMtdAV9LOC4yz8Oa1V7H5Khq52cmq3h3IqTuH0vGkZM6g=="
    static let raw = #"{ "target": {"terminalId": "a7"}, "text": "y <ok> & go", "submit": true }"#
    static let params = #"{"target":{"terminalId":"a7"},"text":"y \u003cok\u003e \u0026 go","submit":true}"#
    static let message =
        "ghosty-req-v1\nmini-1\ninput\n1791036424000\nAAECAwQFBgcICQoLDA0ODw==\ncf71dc2fdfb8807e081aa51cee5b2647a0f8631b80a4edadeff209ac296f2496"
    static let digest = "4c15802b986fa4a598295fbf7a1f78d25e5e6d387d2a1e0cfb3c0b129e3726f5"
    static let sig = "MEUCIAfH5o8PerLuc3Y4b93pYxeh1V+bsmlIv/rPN5BWFM3mAiEArRJ1eUaA/WZA1fV0W1B1qdJADbAAEd0NEOKe/mcWtGQ="

    static func goldenKey() throws -> P256.Signing.PrivateKey {
        try P256.Signing.PrivateKey(rawRepresentation: Data(SHA256.hash(data: Data("ghosty golden device key".utf8))))
    }

    @Test func goldenVector() throws {
        let key = try Self.goldenKey()
        #expect(key.publicKey.derRepresentation.base64EncodedString() == Self.publicKey)
        let params = try Params.normalize(Data(Self.raw.utf8))
        #expect(String(decoding: params, as: UTF8.self) == Self.params)
        let message = RequestSigning.message(
            machine: "mini-1", method: "input", ts: 1_791_036_424_000, nonce: "AAECAwQFBgcICQoLDA0ODw==", params: params)
        #expect(String(decoding: message, as: UTF8.self) == Self.message)
        #expect(Data(SHA256.hash(data: message)).hex == Self.digest)
        let golden = try P256.Signing.ECDSASignature(derRepresentation: Data(base64Encoded: Self.sig)!)
        #expect(key.publicKey.isValidSignature(golden, for: message))
    }

    @Test func signsWhatTheHostVerifies() throws {
        let key = SoftwareSigningKey()
        let params = try Params.normalize(Data(#"{"id":"a1"}"#.utf8))
        let auth = try RequestSigning.sign(params, machine: "mini-1", method: "agents.stop", device: "phone-1", key: key)
        #expect(auth.device == "phone-1")
        #expect(Data(base64Encoded: auth.nonce)?.count == 16)
        let message = RequestSigning.message(machine: "mini-1", method: "agents.stop", ts: auth.ts, nonce: auth.nonce, params: params)
        let sig = try P256.Signing.ECDSASignature(derRepresentation: Data(base64Encoded: auth.sig)!)
        #expect(key.key.publicKey.isValidSignature(sig, for: message))
        let other = try RequestSigning.sign(params, machine: "mini-1", method: "agents.stop", device: "phone-1", key: key)
        #expect(other.nonce != auth.nonce)
    }

    static let normalized: [(String, String)] = [
        ("", "{}"),
        (" {} ", "{}"),
        (#"{"a" : [1, 2 ,{"b":3}]}"#, #"{"a":[1,2,{"b":3}]}"#),
        ("null", "null"),
        (#"{"a":{"x":1},"b":{"x":1}}"#, #"{"a":{"x":1},"b":{"x":1}}"#),
        (#"{"t":"<x>&"}"#, #"{"t":"\u003cx\u003e\u0026"}"#),
        ("{\"t\":\"\u{2028}\"}", #"{"t":"\u2028"}"#),
        (#"{"t":"\u2029"}"#, #"{"t":"\u2029"}"#),
        (#"{"t":"\u003c\n"}"#, #"{"t":"\u003c\n"}"#),
        ("[ true , false, -1.5e3 ]", "[true,false,-1.5e3]"),
    ]

    @Test(arguments: normalized)
    func normalizes(input: String, want: String) throws {
        let got = try Params.normalize(Data(input.utf8))
        #expect(String(decoding: got, as: UTF8.self) == want)
        #expect(try Params.normalize(got) == got)
    }

    @Test(arguments: [
        #"{"a":1,"a":2}"#, #"{"a":{"b":1,"b":1}}"#, #"[{"x":1,"x":1}]"#, "{}{}", "{} 1", "{", #"{"a":}"#, "nul",
        String(repeating: "[", count: 70) + String(repeating: "]", count: 70), #"{"a":1,}"#, #"{"a":1,"a":2}"#,
    ])
    func refuses(input: String) {
        #expect(throws: Params.Invalid.self) { try Params.normalize(Data(input.utf8)) }
    }

    @Test func refusesInvalidUTF8() {
        #expect(throws: Params.Invalid.self) { try Params.normalize(Data([0x7b, 0x22, 0x61, 0x22, 0x3a, 0x22, 0xff, 0x22, 0x7d])) }
    }
}
