import HesperDesign
import HesperFleet
import SwiftUI

/// One numbered option on the priority card: "1 Yes…". A destructive one
/// ("No, and tell Claude what to do instead", "Close") is in signal.
struct OptionRow: View {
    let row: DecisionRow
    let action: () -> Void

    var body: some View {
        let tint = row.isDestructive ? Theme.signal : Theme.accent
        Button(action: action) {
            HStack(alignment: .firstTextBaseline, spacing: DS.Spacing.l) {
                Text("\(row.number)")
                    .font(Typeface.mono(17, relativeTo: .body))
                    .foregroundStyle(tint)
                VStack(alignment: .leading, spacing: DS.Spacing.xxs) {
                    Text(row.label)
                        .font(.subheadline.weight(.medium))
                        .foregroundStyle(row.isDestructive ? Theme.signal : Theme.text)
                    if let detail = row.detail {
                        Text(detail).font(.caption2).foregroundStyle(Theme.muted)
                    }
                }
                Spacer(minLength: 0)
            }
            .padding(.horizontal, DS.Spacing.l)
            .padding(.vertical, DS.Spacing.m)
            .frame(maxWidth: .infinity, minHeight: DS.Size.control, alignment: .leading)
            .background(row.isDestructive ? Theme.signalTint : Theme.surface2)
            .overlay {
                RoundedRectangle(cornerRadius: DS.Radius.console)
                    .strokeBorder(row.isDestructive ? Theme.signal : Theme.line, lineWidth: DS.Edge.line)
            }
            .clipShape(.rect(cornerRadius: DS.Radius.console))
            .contentShape(.rect)
        }
        .buttonStyle(OptionPress())
        .accessibilityLabel("\(row.number). \(row.label)")
    }
}

/// Dims while pressed or disabled, like `NeonButtonStyle`.
private struct OptionPress: ButtonStyle {
    @Environment(\.isEnabled) private var isEnabled

    func makeBody(configuration: Configuration) -> some View {
        configuration.label.opacity(configuration.isPressed ? 0.7 : isEnabled ? 1 : 0.4)
    }
}
