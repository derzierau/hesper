import Foundation
import HesperCore
import HesperFixtures
import HesperFleet
import Testing

/// The fixture link's transcript calls. The items' decoding is tested
/// with the wire models in HesperCore.
@Suite struct TranscriptTests {
    @Test func theFixtureScreenIsTheScriptWithoutEscapes() async throws {
        let screen = try await FixtureLink().screen(agent: "studio/s1", rows: 40, scrollback: 500)
        #expect(screen.rows == 40 && !screen.alt)
        #expect(screen.text.contains("$ claude") && screen.text.contains("⏺ Read("))
        #expect(!screen.text.contains("\u{1b}"))
    }
}
