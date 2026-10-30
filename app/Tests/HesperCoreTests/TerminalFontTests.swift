import Testing
@testable import HesperCore

struct TerminalFontTests {
    @Test func keepsLegacyValueUntilANewValueIsStored() {
        #expect(TerminalFont.resolve(stored: nil, legacy: 10.5, fallback: 13) == 10.5)
        #expect(TerminalFont.resolve(stored: 14, legacy: 10.5, fallback: 13) == 14)
        #expect(TerminalFont.resolve(stored: nil, legacy: nil, fallback: 13) == 13)
    }

    @Test func clampsEverySourceToTheSameRange() {
        #expect(TerminalFont.range == 9...16)
        #expect(TerminalFont.resolve(stored: 30, legacy: nil, fallback: 13) == 16)
        #expect(TerminalFont.resolve(stored: nil, legacy: 3, fallback: 13) == 9)
        #expect(TerminalFont.resolve(stored: nil, legacy: nil, fallback: 30) == 16)
        #expect(TerminalFont.clamp(12.5) == 12.5)
    }

    @Test func skipsNonfiniteValues() {
        #expect(TerminalFont.resolve(stored: .nan, legacy: 11, fallback: 13) == 11)
        #expect(TerminalFont.resolve(stored: .infinity, legacy: .nan, fallback: 13) == 13)
        #expect(TerminalFont.resolve(stored: nil, legacy: nil, fallback: .nan) == TerminalFont.defaultSize)
    }

    @Test func movesToANewKey() {
        #expect(TerminalFont.key != TerminalFont.legacyKey)
        #expect(TerminalFont.legacyKey == "tileFontSize")
    }
}
