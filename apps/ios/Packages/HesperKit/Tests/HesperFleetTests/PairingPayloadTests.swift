import Foundation
import HesperFleet
import Testing

@Suite struct PairingPayloadTests {
    /// The golden vector from docs/iphone/onboarding.md, "Interfaces"; the
    /// Go side encodes the same text.
    static let golden =
        "HESPER1:eyJ2IjoxLCJyZWxheSI6Imh0dHBzOi8vcmVsYXkuZXhhbXBsZS5jb20iLCJtYWNoaW5lIjoiTUFDSElORV9JRCIsIm5hbWUiOiJzdHVkaW8iLCJob3N0S2V5IjoiQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQT0iLCJyaWdodHMiOlsib2JzZXJ2ZSIsImFuc3dlciIsInR5cGUiLCJ0cmFuc2ZlciJdLCJvZmZlciI6IkFBRUNBd1FGQmdjSUNRb0xEQTBPRHciLCJleHBpcmVzIjoxNzkzNjUwOTU2fQ"
    static let expires = Date(timeIntervalSince1970: 1_793_650_956)
    static let before = expires.addingTimeInterval(-60)

    static let offer = PairingOffer(
        relay: URL(string: "https://relay.example.com")!, machine: "MACHINE_ID", name: "studio", hostKey: Data(count: 32),
        rights: ["observe", "answer", "type", "transfer"], secret: Data(0..<16), expires: expires)

    @Test func readsTheGoldenCode() throws {
        #expect(try PairingPayload.parse(Self.golden, now: Self.before) == Self.offer)
    }

    @Test func writesTheGoldenCode() {
        #expect(PairingPayload.encode(Self.offer) == Self.golden)
    }

    @Test func toleratesWhitespaceAroundTheCode() throws {
        #expect(try PairingPayload.parse("  \(Self.golden)\n", now: Self.before) == Self.offer)
    }

    @Test func anExpiredCodeSaysSo() {
        #expect(throws: PairingError.offerExpired) { try PairingPayload.parse(Self.golden, now: Self.expires) }
    }

    @Test(arguments: [
        "",
        "https://relay.example.com",
        "HESPER2:" + PairingPayloadTests.golden.dropFirst(8),
        "HESPER1:",
        "HESPER1:not base64!",
        // {"v":2,…}: a later version is not a code this app reads.
        "HESPER1:" + PairingPayloadTests.base64url(PairingPayloadTests.json().replacingOccurrences(of: #"{"v":1"#, with: #"{"v":2"#)),
        // A link instead of a relay origin.
        "HESPER1:" + PairingPayloadTests.base64url(PairingPayloadTests.json(relay: "hesper-ios://auth")),
        // A short secret.
        "HESPER1:" + PairingPayloadTests.base64url(PairingPayloadTests.json(offer: "AAEC")),
        // A host key that is not 32 bytes.
        "HESPER1:" + PairingPayloadTests.base64url(PairingPayloadTests.json(hostKey: "AAAA")),
        "HESPER1:" + PairingPayloadTests.base64url(PairingPayloadTests.json(name: "")),
    ])
    func everythingElseIsNotAHesperCode(_ text: String) {
        #expect(throws: PairingError.notAHesperCode) { try PairingPayload.parse(text, now: Self.before) }
    }

    static func json(
        relay: String = "https://relay.example.com", name: String = "studio", hostKey: String = Data(count: 32).base64EncodedString(),
        offer: String = "AAECAwQFBgcICQoLDA0ODw"
    ) -> String {
        #"{"v":1,"relay":"\#(relay)","machine":"MACHINE_ID","name":"\#(name)","hostKey":"\#(hostKey)","rights":["observe"],"offer":"\#(offer)","expires":1793650956}"#
    }

    static func base64url(_ s: String) -> String {
        Data(s.utf8).base64EncodedString().replacingOccurrences(of: "+", with: "-").replacingOccurrences(of: "/", with: "_")
            .replacingOccurrences(of: "=", with: "")
    }
}
