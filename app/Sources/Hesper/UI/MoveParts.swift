import HesperCore
import SwiftUI

// Moving work across Macs, on the agent's band (tile and focus view): the
// preflight question in the close strip's place, the quiet progress line
// while it moves, and a finished agent's "Continue on mini · Fork on mini".

extension AttentionBar {
    /// A move hesperd refused before touching anything (`MovePreflight`).
    struct MoveStrip {
        var message: String
        /// ⏎: go on ("Interrupt and move", "Move anyway"); nil: only the
        /// message (esc dismisses).
        var primary: String?
        /// The tooltip (the processes that stay here).
        var detail: String?
        var actionable: Bool
        var onPrimary: () -> Void
        var onCancel: () -> Void
    }

    struct MoveLine {
        var progress: MoveProgress
        var target: String
        var fork = false
    }

    struct MoveOffer {
        var target: String
        var onMove: () -> Void
        var onFork: () -> Void
    }
}

/// "migrations is working · Interrupt and move ⏎ · esc"; "npm run dev is
/// still running here · Move anyway (leave them running) ⏎ · esc"; "Codex
/// isn't installed on mini · esc". Its keys work wherever the keyboard is
/// (MainWindow), like the close strip's.
struct MoveStripRow: View {
    var strip: AttentionBar.MoveStrip
    var tight = false

    var body: some View {
        HStack(spacing: DS.Spacing.m) {
            StateMark(strip.actionable ? .working : .error)
            Text(strip.message).font(.ds(.chrome, .medium)).foregroundStyle(Theme.fg).lineLimit(1).truncationMode(.middle)
                .help(strip.detail ?? strip.message)
            Spacer(minLength: DS.Spacing.m)
            HStack(spacing: DS.Spacing.s) {
                if let p = strip.primary {
                    StripButton(title: p, key: "⏎", primary: true, tint: .working, tight: tight, run: strip.onPrimary)
                        .accessibilityIdentifier("move.primary")
                }
                StripButton(title: nil, key: "esc", primary: false, tint: .working, tight: tight, run: strip.onCancel)
                    .accessibilityIdentifier("move.cancel")
            }
            .fixedSize()
        }
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("tile.moveStrip")
    }
}

/// "Moving to mini · checkpoint → transfer 42% → worktree → resuming":
/// done steps in text2, the current one in working, the rest dim.
struct MoveProgressRow: View {
    var line: AttentionBar.MoveLine

    var body: some View {
        HStack(spacing: DS.Spacing.s) {
            StateMark(.working)
            Text("\(line.fork ? "Forking" : "Moving") to \(line.target)")
                .font(.ds(.chrome, .medium)).foregroundStyle(Theme.fg).fixedSize()
            if line.progress.step == nil {
                Text("…").font(.ds(.chrome)).foregroundStyle(Theme.dim)
            } else {
                Text("·").font(.ds(.meta)).foregroundStyle(Theme.dim)
                steps.lineLimit(1).truncationMode(.tail)
            }
            Spacer(minLength: 0)
        }
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(line.progress.line(target: line.target, fork: line.fork))
        .accessibilityIdentifier("tile.moveProgress")
    }

    /// One Text (it truncates as a whole on a narrow card).
    private var steps: Text {
        var out = Text("")
        for (i, s) in line.progress.steps.enumerated() {
            if i > 0 { out = out + Text(" → ").foregroundColor(Theme.dim) }
            let color: Color
            switch s.status {
            case .done: color = Theme.fg2
            case .current: color = Theme.color(.working)
            case .pending: color = Theme.dim
            }
            out = out + Text(line.progress.label(s.step)).font(.ds(.meta, s.status == .current ? .medium : .regular)).foregroundColor(color)
        }
        return out
    }
}

/// A finished agent's band, on the right: "Continue on mini · Fork on mini".
struct MoveOfferButtons: View {
    var offer: AttentionBar.MoveOffer
    var tight = false

    var body: some View {
        HStack(spacing: DS.Spacing.s) {
            StripButton(title: MoveText.continueTitle(offer.target), key: nil, primary: false, tint: .working, tight: tight, run: offer.onMove)
                .accessibilityIdentifier("move.continue")
            if !tight {
                StripButton(title: MoveText.forkTitle(offer.target), key: nil, primary: false, tint: .working, tight: tight, run: offer.onFork)
                    .accessibilityIdentifier("move.fork")
            }
        }
        .fixedSize()
    }
}

/// The band's action pill (the close strip's look): a title, a Kbd, or both.
private struct StripButton: View {
    var title: String?
    var key: String?
    var primary: Bool
    var tint: Theme.Token
    var tight: Bool
    var run: () -> Void

    var body: some View {
        Button(action: run) {
            HStack(spacing: DS.Spacing.s) {
                if let title { Text(title).font(.ds(.chrome, .medium)).lineLimit(1) }
                if let key, !tight || title == nil { Kbd(key) }
            }
            .padding(.horizontal, title == nil ? DS.Spacing.xs : DS.Spacing.m)
            .frame(height: Pill.height)
            .background(DS.Radius.shape(DS.Radius.control).fill(primary ? Theme.color(tint).opacity(AttentionBar.primaryFill) : (title == nil ? Color.clear : Theme.chipBG)))
            .overlay(DS.Radius.shape(DS.Radius.control).strokeBorder(primary ? Theme.color(tint).opacity(AttentionBar.primaryStroke) : (title == nil ? Color.clear : Theme.stroke), lineWidth: 1))
            .foregroundStyle(Theme.fg)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .help(key.map { "\(title ?? "Dismiss") (\($0))" } ?? (title ?? ""))
        .accessibilityLabel(title ?? "Dismiss")
    }
}
