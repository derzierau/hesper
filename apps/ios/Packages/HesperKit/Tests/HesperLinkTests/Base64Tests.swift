import Foundation
import Testing

@testable import HesperLink

@Suite struct Base64Tests {
    @Test func base64URLRoundTrips() {
        let data = Data([0xfb, 0xff, 0x00, 0x01])
        #expect(data.base64URLEncodedString() == "-_8AAQ")
        #expect(Data(base64URLEncoded: "-_8AAQ") == data)
        #expect(Data(base64URLEncoded: "-_8AAQ==") == data)
        #expect(Data(base64URLEncoded: "*") == nil)
    }

    @Test func hexRoundTrips() {
        #expect(Data([0x00, 0xab]).hex == "00ab")
        #expect(Data(hex: "00AB") == Data([0x00, 0xab]))
        #expect(Data(hex: "0") == nil)
    }
}
