/// The phone's Neon console colours, the table in docs/iphone/neon.md
/// (the owner's Figma frame "Home — Neon control console"). The Mac keeps
/// HesperCore's `DesignTokens`; these are the iPhone's own. Here and not
/// in HesperDesign so a test pins every value, and so the extensions can
/// read them. Dark only for now; the names stay semantic so a light set
/// can be added later.
public enum NeonPalette {
    public enum Token: String, CaseIterable, Sendable {
        case background, surface, surface2, line, text, muted, accent, onAccent, accentTint, signal, signalTint
    }

    /// The token as sRGB hex, 0xRRGGBB.
    public static func hex(_ t: Token) -> UInt32 {
        switch t {
        case .background: 0x070A0D
        case .surface: 0x0D1419
        case .surface2: 0x101D24
        case .line: 0x304650
        case .text: 0xF0F6F7
        case .muted: 0x9BAFB8
        case .accent: 0x64F5FF
        case .onAccent: 0x070A0D
        case .accentTint: 0x0D2B32
        case .signal: 0xFF55CE
        case .signalTint: 0x291529
        }
    }
}
