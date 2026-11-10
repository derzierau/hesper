import SwiftUI

/// The Neon console's two bundled faces (docs/iphone/neon.md, Type):
/// Space Grotesk Bold for headlines, JetBrains Mono for commands, counts
/// and captions. Everything else is SF Pro at the system text styles. The
/// files are in App/Fonts and registered with `UIAppFonts`; every size
/// scales with Dynamic Type.
public enum Typeface {
    /// PostScript names; the app checks them at launch in debug builds,
    /// since SwiftUI falls back to San Francisco silently.
    public static let groteskBold = "SpaceGrotesk-Bold"
    public static let monoRegular = "JetBrainsMono-Regular"

    /// The priority card's question.
    public static func hero() -> Font {
        .custom(groteskBold, size: 36, relativeTo: .largeTitle)
    }

    /// Section headings on Home and in lists.
    public static func heading() -> Font {
        .custom(groteskBold, size: 19, relativeTo: .title3)
    }

    /// A grid card's title.
    public static func cardTitle() -> Font {
        .custom(groteskBold, size: 15, relativeTo: .subheadline)
    }

    /// Commands, files, counts and state captions.
    public static func mono(_ size: CGFloat, relativeTo style: Font.TextStyle) -> Font {
        .custom(monoRegular, size: size, relativeTo: style)
    }
}
