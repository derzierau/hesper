import SwiftUI

extension View {
    /// The primary action of a row, dock or sheet: a capsule in the accent
    /// (glass on iOS 26). One per place.
    public func primaryAction() -> some View {
        buttonStyle(.glassProminent).tint(Theme.accent)
    }

    /// A list on the Mac's surfaces instead of the system greys: the
    /// background behind, tiles for the rows (`themedRow()`).
    public func themedList() -> some View {
        scrollContentBackground(.hidden).background(Theme.background)
    }

    public func themedRow() -> some View {
        listRowBackground(Theme.tile)
    }

    /// Secondary actions next to a primary one.
    public func secondaryAction() -> some View {
        buttonStyle(.glass)
    }
}

/// "studio · 4m": the Mac and how long the agent has been in its state.
public struct MetaLine: View {
    let mac: String
    let since: Date?

    public init(mac: String, since: Date?) {
        self.mac = mac
        self.since = since
    }

    public var body: some View {
        HStack(spacing: DS.Spacing.xs) {
            Text(mac)
            if let since {
                Text("·")
                Text(since, format: .relative(presentation: .numeric, unitsStyle: .narrow))
            }
        }
        .font(.caption.monospaced())
        .foregroundStyle(Theme.dim)
    }
}
