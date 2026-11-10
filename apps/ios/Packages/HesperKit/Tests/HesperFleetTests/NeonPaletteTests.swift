import HesperSurfaces
import Testing

/// Pins every Neon token to the table in docs/iphone/neon.md.
@Suite struct NeonPaletteTests {
    @Test func everyTokenMatchesTheSpec() {
        let spec: [NeonPalette.Token: UInt32] = [
            .background: 0x070A0D, .surface: 0x0D1419, .surface2: 0x101D24, .line: 0x304650,
            .text: 0xF0F6F7, .muted: 0x9BAFB8, .accent: 0x64F5FF, .onAccent: 0x070A0D,
            .accentTint: 0x0D2B32, .signal: 0xFF55CE, .signalTint: 0x291529,
        ]
        for t in NeonPalette.Token.allCases { #expect(NeonPalette.hex(t) == spec[t], "\(t)") }
        #expect(spec.count == NeonPalette.Token.allCases.count)
    }
}
