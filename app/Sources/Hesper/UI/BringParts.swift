import HesperCore
import SwiftUI

/// A draft bringing its folder to another Mac, in its footer: "Bringing to
/// mini · checkpoint → transfer 42% → unpack → starting" — done steps in
/// text2, the current one in working, the rest dim (the move line's look).
struct BringProgressRow: View {
    var progress: BringProgress
    var changes: BringChanges
    var target: String

    var body: some View {
        HStack(spacing: DS.Spacing.s) {
            StateMark(.working)
            Text("Bringing to \(target)").font(.ds(.chrome, .medium)).foregroundStyle(Theme.fg).fixedSize()
            if progress.step == nil {
                Text("…").font(.ds(.chrome)).foregroundStyle(Theme.dim)
            } else {
                Text("·").font(.ds(.meta)).foregroundStyle(Theme.dim)
                steps.lineLimit(1).truncationMode(.tail)
            }
        }
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(progress.line(target: target, changes: changes))
        .accessibilityIdentifier("draft.bringProgress")
    }

    /// One Text (it truncates as a whole on a narrow tile).
    private var steps: Text {
        var out = Text("")
        for (i, s) in progress.steps(changes).enumerated() {
            if i > 0 { out = out + Text(" → ").foregroundColor(Theme.dim) }
            let color: Color
            switch s.status {
            case .done: color = Theme.fg2
            case .current: color = Theme.color(.working)
            case .pending: color = Theme.dim
            }
            out = out + Text(progress.label(s.step)).font(.ds(.meta, s.status == .current ? .medium : .regular)).foregroundColor(color)
        }
        return out
    }
}
