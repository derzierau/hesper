import SwiftUI

extension View {
    /// The primary action of a row, dock or sheet: an accent fill. One per
    /// place.
    public func primaryAction() -> some View {
        buttonStyle(NeonButtonStyle(.primary))
    }

    /// A list on the console's surfaces instead of the system greys: the
    /// background behind, surfaces for the rows (`themedRow()`).
    public func themedList() -> some View {
        scrollContentBackground(.hidden).background(Theme.background)
    }

    public func themedRow() -> some View {
        listRowBackground(Theme.surface)
    }

    /// Secondary actions next to a primary one: an accent border.
    public func secondaryAction() -> some View {
        buttonStyle(NeonButtonStyle(.secondary))
    }

    /// An action that ends or refuses something: a signal border.
    public func destructiveAction() -> some View {
        buttonStyle(NeonButtonStyle(.destructive))
    }

    /// A Neon panel: surface fill, 1 pt line border, square corners, and
    /// a top edge in `edge` (the priority card's is accent, 3 pt).
    public func neonPanel(edge: Color = Theme.line, edgeWidth: CGFloat = 1) -> some View {
        let shape = RoundedRectangle(cornerRadius: DS.Radius.console)
        return background(Theme.surface)
            .overlay { shape.strokeBorder(Theme.line, lineWidth: 1) }
            .overlay(alignment: .top) { Rectangle().fill(edge).frame(height: edgeWidth) }
            .clipShape(shape)
    }
}

/// The console's buttons: square corners, solid fills, 1 pt borders. No
/// glass.
public struct NeonButtonStyle: ButtonStyle {
    public enum Kind: Sendable { case primary, secondary, destructive }

    let kind: Kind
    @Environment(\.isEnabled) private var isEnabled

    public init(_ kind: Kind) {
        self.kind = kind
    }

    public func makeBody(configuration: Configuration) -> some View {
        let shape = RoundedRectangle(cornerRadius: DS.Radius.console)
        configuration.label
            .foregroundStyle(label)
            .padding(.horizontal, DS.Spacing.l)
            .frame(minHeight: 44)
            .background(fill, in: shape)
            .overlay {
                if let border { shape.strokeBorder(border, lineWidth: 1) }
            }
            .contentShape(shape)
            .opacity(configuration.isPressed ? 0.7 : isEnabled ? 1 : 0.4)
    }

    private var fill: Color { kind == .primary ? Theme.accent : Theme.surface }

    private var label: Color {
        switch kind {
        case .primary: Theme.onAccent
        case .secondary: Theme.accent
        case .destructive: Theme.signal
        }
    }

    private var border: Color? {
        switch kind {
        case .primary: nil
        case .secondary: Theme.accent
        case .destructive: Theme.signal
        }
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
        .font(Typeface.mono(11, relativeTo: .caption2))
        .foregroundStyle(Theme.muted)
    }
}
