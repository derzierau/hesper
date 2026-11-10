import HesperLink
import Testing

@Suite struct RelayErrorTests {
    @Test func describesItselfByItsMessage() {
        #expect(String(describing: RelayError(code: "forbidden", message: "No observe right")) == "No observe right")
    }

    @Test func fallsBackToTheCode() {
        #expect(String(describing: RelayError(code: "unavailable")) == "unavailable")
    }
}
