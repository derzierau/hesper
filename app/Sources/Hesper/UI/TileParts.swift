import HesperCore
import SwiftUI

/// Card header (26 pt Comfortable, 22 Compact: `DS.tileHeader`): the state
/// square, the name (Geist 600), one quiet truncated project · branch
/// segment, then on the right the status (elapsed while working), the
/// focus Kbd on hover and machine · tool in Mono. No chips: a remote
/// machine is only colored `horizon`.
struct TileHeader: View {
    var agent: Agent
    var local: Bool
    var compact = false
    /// Moving to this machine (shown in place of the status).
    var movingTo: String? = nil
    /// The active tile: keys go to the agent.
    var typing = false
    /// The agent's project (projects): its name and 6 pt color square.
    var projectName: String? = nil
    var projectColor: String? = nil
    /// The pointer is over the tile: the focus shortcut shows.
    var hovering = false
    /// The card sits in its own project's band: the heading names the
    /// project, so the header leaves it (and its square) out.
    var inOwnBand = false
    /// The branch the band shows (a branch band): not repeated either.
    var bandBranch: String? = nil
    /// The machine's short name ("mini"); the agent's machine id if nil.
    var machineLabel: String? = nil
    /// Ended by ⌃⌘W: the status says "Killed".
    var killed = false

    /// The focus (open full size) shortcut, shown on hover.
    static let focusKeys = "⌘↩"
    /// The project square: the same 6 pt square as the band heading.
    static let swatch: CGFloat = DS.Spacing.s

    var body: some View {
        HStack(spacing: DS.Spacing.m) {
            StateMark(state: agent.state)
            Text(agent.name)
                .font(.ds(.body, .semibold))
                .foregroundStyle(Theme.fg)
                .lineLimit(1)
                .truncationMode(.tail)
                .layoutPriority(2)
            if !compact, !inOwnBand, let projectColor {
                Rectangle().fill(Theme.color(projectColor)).frame(width: Self.swatch, height: Self.swatch)
                    .accessibilityIdentifier("tile.projectSwatch")
            }
            if !compact, let place = placeLabel {
                Text(place)
                    .font(.ds(.chrome))
                    .foregroundStyle(Theme.dim)
                    .lineLimit(1)
                    .truncationMode(.middle)
                    .help(agent.project ?? place)
            }
            Spacer(minLength: DS.Spacing.s)
            if typing {
                HStack(spacing: DS.Spacing.xs) {
                    Text("typing").font(.ds(.chrome, .medium)).foregroundStyle(Theme.color(.working))
                    if !compact { Kbd("⌘esc") }
                }
                .fixedSize()
                .help("Typing goes to the agent; ⌘esc leaves")
                .accessibilityIdentifier("tile.typing")
            }
            status.layoutPriority(1)
            if hovering && !typing && !compact {
                Kbd(Self.focusKeys)
                    .help("Open full size (\(Self.focusKeys))")
                    .accessibilityIdentifier("tile.focusHint")
            }
            Text(compact ? machine : "\(machine) · \(Theme.kindLabel(agent.kind))")
                .font(.ds(.meta))
                .foregroundStyle(local ? Theme.dim : Theme.color(.horizon))
                .lineLimit(1)
                .fixedSize()
                .accessibilityIdentifier("tile.mark")
        }
        .padding(.horizontal, DS.Spacing.l)
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .leading)
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("tile.header.\(agent.id)")
    }

    private var machine: String { machineLabel ?? agent.machine }

    private var placeLabel: String? {
        let project = projectName ?? agent.project.map { ($0 as NSString).lastPathComponent }
        return TilePlace.label(project: project, branch: agent.branch, ownBand: inOwnBand, bandBranch: bandBranch)
    }

    @ViewBuilder private var status: some View {
        if let movingTo {
            HStack(spacing: DS.Spacing.xs) {
                ProgressView().controlSize(.mini)
                Text("moving to \(movingTo)…").font(.ds(.chrome)).foregroundStyle(Theme.accent)
            }
            .fixedSize()
            .accessibilityIdentifier("tile.moving")
        } else {
            plainStatus
        }
    }

    @ViewBuilder private var plainStatus: some View {
        switch agent.state {
        case .working:
            TimelineView(.periodic(from: .now, by: 1)) { ctx in
                Text(Theme.elapsed(since: agent.stateSince, now: ctx.date) ?? "working")
                    .font(.ds(.meta))
                    .foregroundStyle(Theme.dim)
            }
            .fixedSize()
        case .exited where killed:
            Text(CloseText.killed).font(.ds(.meta, .medium)).foregroundStyle(Theme.fg2).fixedSize()
                .accessibilityIdentifier("tile.killed")
        case .starting, .exited, .done, .idle:
            Text(agent.state.label).font(.ds(.meta)).foregroundStyle(Theme.dim).fixedSize()
        default:
            EmptyView()
        }
    }
}

/// The band at a card's (or the focus view's) bottom: the footer's one
/// line (activity, summary), and when the agent needs the user the action
/// strip: approval (Allow ⏎ · Always · Deny · Open), a question's numbered
/// options as pills 1 / 2 / 3, a free-text question's Open, the error.
/// On the wall it slides up over the terminal's bottom rows (TileView).
struct AttentionBar: View {
    var agent: Agent
    var inFocus = false
    var wide = true
    /// A shelf card: the one line (summary, state) with the card's own
    /// actions; never an empty band.
    var shelf = false
    /// A narrow card: no key hints, no Open / Always (double-click opens).
    var tight = false
    /// The buttons show their keys (⏎ A N …) only where those keys work:
    /// on the selected, not active wall tile (focus: the agent has them).
    var keyHints = true
    /// The selected tile's footer hint ("←→↑↓ move · ⏎ type · …"), shown
    /// right of the activity / summary.
    var selectionHint: String? = nil
    var onAnswer: (Decision) -> Void
    var onDenyMessage: () -> Void
    var onOpen: () -> Void
    var onResume: () -> Void
    /// "Deny…" expanded into a one-line message field, in place.
    var deny: DenyField? = nil
    /// Closing asks here, on the tile: needs you, a running command,
    /// uncommitted work in its worktree.
    var closeStrip: CloseStrip? = nil
    /// Ended by ⌃⌘W: "Killed · Resume ⏎ · Close ⌘W".
    var killed = false
    var onClose: () -> Void = {}
    /// A question with numbered options hesperd does not answer itself:
    /// option `i` (0-based) is chosen by typing its number to the agent.
    /// nil: those options are not offered here (focus: type them).
    var onChoose: ((Int) -> Void)? = nil
    /// A move asking first (it's working, processes stay here, the other
    /// Mac can't take it), in the close strip's place (MoveParts).
    var moveStrip: MoveStrip? = nil
    /// The quiet progress line while it moves (agents.moving).
    var moveProgress: MoveLine? = nil
    /// A finished agent's "Continue on mini · Fork on mini".
    var moveOffer: MoveOffer? = nil
    /// Review: "Ready to review · 3 files · tests ✓ · Review ⏎" on a
    /// finished agent with changes (ReviewParts).
    var reviewReady: ReviewReady? = nil

    struct DenyField {
        var text: Binding<String>
        var onSubmit: (String) -> Void
        var onCancel: () -> Void
    }

    struct CloseStrip {
        var ask: CloseConfirm
        /// ⏎: close anyway (the worktree: keep it and close).
        var onPrimary: () -> Void
        /// The worktree's "Discard".
        var onSecondary: () -> Void
        /// Esc: don't close.
        var onCancel: () -> Void
    }

    var body: some View {
        Group {
            if let closeStrip {
                CloseStripRow(strip: closeStrip, tight: tight)
            } else if let moveStrip {
                MoveStripRow(strip: moveStrip, tight: tight)
            } else if let moveProgress {
                MoveProgressRow(line: moveProgress)
            } else if agent.state == .approval, let deny {
                DenyRow(agent: agent, wide: wide, field: deny)
            } else {
                content
            }
        }
        .padding(.horizontal, DS.Spacing.l)
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .leading)
        .background(Theme.color(.surface))
        .overlay(alignment: .top) { Rectangle().fill(Color(nsColor: Theme.hairline)).frame(height: 1) }
    }

    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    /// SF Symbols in the band: the chrome size.
    static let iconFont = Font.ds(.chrome)

    /// What the footer shows, as a key: a change crossfades (quick).
    private var contentKey: String {
        [agent.state.rawValue, agent.attention?.title ?? "", agent.attention?.detail ?? "", agent.summary ?? "", agent.activity ?? "", agent.exit?.label ?? ""].joined(separator: "|")
    }

    @ViewBuilder private var content: some View {
        HStack(spacing: DS.Spacing.m) {
            ZStack(alignment: .leading) {
                states
                    .id(contentKey)
                    .transition(.opacity)
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .leading)
            .animation(DS.animation(.quick, reduceMotion: reduceMotion), value: contentKey)
            if reviewReady != nil, agent.state == .done || agent.state == .idle {
                EmptyView() // the review line has its own button
            } else if let moveOffer, MoveRules.showsOnStrip(agent) {
                MoveOfferButtons(offer: moveOffer, tight: tight)
            } else if let selectionHint, !agent.state.needsAttention, agent.state != .exited {
                Text(selectionHint)
                    .font(.ds(.meta))
                    .foregroundStyle(Theme.dim)
                    .lineLimit(1)
                    .fixedSize()
                    .accessibilityIdentifier("tile.keyHint")
            }
        }
    }

    /// The question's options that are typed to the agent (not hesperd's
    /// own decisions), when this band can send them.
    private var typedOptions: [String] {
        guard onChoose != nil, agent.attention?.answers.isEmpty ?? true else { return [] }
        return agent.attention?.options ?? []
    }

    @ViewBuilder private var states: some View {
        Group {
            switch agent.state {
            case .working, .starting:
                HStack(spacing: DS.Spacing.m) {
                    StateMark(state: agent.state)
                    Text(agent.activity ?? (agent.state == .starting ? "Starting…" : "Working"))
                        .font(agent.activity == nil ? .ds(.chrome) : .ds(.meta))
                        .foregroundStyle(agent.activity == nil ? Theme.dim : Theme.fg2)
                        .lineLimit(1).truncationMode(.middle)
                }
                .accessibilityIdentifier("tile.activity")
            case .approval:
                layout {
                    detail(agent.attention?.title ?? "Approval", agent.attention?.detail, mark: .needsYou, color: Theme.Token.signal.hex)
                } actions: {
                    button("Allow", key: "⏎", color: Theme.Token.signal.hex, primary: true) { onAnswer(.allow) }.accessibilityIdentifier("approve.allow")
                    if !tight { button("Always", key: "A") { onAnswer(.always) }.accessibilityIdentifier("approve.always") }
                    button("Deny", key: "N") { onDenyMessage() }.accessibilityIdentifier("approve.deny")
                    if !inFocus && !tight { button("Open", key: TileHeader.focusKeys, action: onOpen).accessibilityIdentifier("approve.open") }
                }
            case .question where !(agent.attention?.answers.isEmpty ?? true):
                // A screen hesperd answers itself: the trust question of a
                // first run (Trust / Exit), Codex's update screen (Skip /
                // Update). Numbered pills; the first one is also ⏎.
                let answers = agent.attention?.answers ?? []
                layout {
                    detail(agent.attention?.title ?? "Question", agent.attention?.detail, mark: .question, color: Theme.Token.question.hex)
                } actions: {
                    ForEach(Array(answers.enumerated()), id: \.element) { i, d in
                        button(d.title, number: i + 1, key: i == 0 ? "⏎" : nil, color: Theme.Token.question.hex, primary: i == 0) { onAnswer(d) }
                            .accessibilityIdentifier("answer.\(d.rawValue)")
                    }
                }
            case .question where !typedOptions.isEmpty:
                // Numbered options typed to the agent: the pill sends its
                // number, as pressing it in the agent would.
                layout {
                    detail(agent.attention?.title ?? "Question", agent.attention?.detail, mark: .question, color: Theme.Token.question.hex)
                } actions: {
                    ForEach(Array(typedOptions.enumerated()), id: \.offset) { i, title in
                        button(title, number: i + 1, color: Theme.Token.question.hex, primary: i == 0) { onChoose?(i) }
                            .accessibilityIdentifier("answer.option.\(i + 1)")
                    }
                    if !inFocus && !tight { button("Open", key: TileHeader.focusKeys, action: onOpen) }
                }
            case .question:
                layout {
                    detail(agent.attention?.title ?? "Question", agent.attention?.detail, mark: .question, color: Theme.Token.question.hex)
                } actions: {
                    // Free-text questions and first-run screens (setup, log
                    // in) are answered in the agent itself.
                    if !inFocus {
                        button("Open", key: TileHeader.focusKeys, color: Theme.Token.question.hex, primary: true, action: onOpen)
                            .accessibilityIdentifier("answer.open")
                    }
                }
            case .error:
                layout {
                    detail(agent.attention?.title ?? "Error", agent.attention?.detail, mark: .error, color: Theme.Token.error.hex)
                } actions: {
                    if agent.exit != nil {
                        button("Resume", color: Theme.Token.error.hex, primary: true, action: onResume).accessibilityIdentifier("tile.resume")
                    }
                    if !inFocus { button("Open", key: TileHeader.focusKeys, action: onOpen) }
                }
            case .done where reviewReady != nil, .idle where reviewReady != nil:
                if let reviewReady { ReviewReadyRow(ready: reviewReady, state: agent.state, keyHints: keyHints, tight: tight) }
            case .done, .idle:
                if (agent.summary ?? "").isEmpty {
                    Text(agent.state == .done ? "Done" : "Idle").font(.ds(.chrome)).foregroundStyle(Theme.dim)
                } else if let s = agent.summary, !s.isEmpty {
                    HStack(spacing: DS.Spacing.m) {
                        StateMark(state: agent.state)
                        Text(s).font(.ds(.chrome)).foregroundStyle(Theme.fg2).lineLimit(1).truncationMode(.tail)
                    }
                    .accessibilityIdentifier("tile.summary")
                }
            case .exited where killed:
                // ⌃⌘W: the pane stays so you can read what went wrong.
                HStack(spacing: DS.Spacing.m) {
                    StateMark(.exited)
                    Text(CloseText.killed).font(.ds(.chrome, .semibold)).foregroundStyle(Theme.fg)
                    if let e = agent.exit { Text(e.label).font(.ds(.meta)).foregroundStyle(Theme.dim) }
                    Spacer(minLength: DS.Spacing.m)
                    button("Resume", key: "⏎", primary: true, action: onResume).accessibilityIdentifier("tile.resume")
                    button("Close", key: "⌘W", action: onClose).accessibilityIdentifier("tile.close")
                }
                .accessibilityIdentifier("tile.killedStrip")
            case .exited:
                HStack(spacing: DS.Spacing.m) {
                    StateMark(.exited)
                    Text(agent.exit.map { "Exited (\($0.label))" } ?? "Exited").font(.ds(.chrome)).foregroundStyle(Theme.dim)
                    Spacer(minLength: DS.Spacing.m)
                    button("Resume", primary: true, action: onResume).accessibilityIdentifier("tile.resume")
                }
            default:
                EmptyView()
            }
        }
    }

    /// One row (detail, then actions on the right) when wide, else two.
    @ViewBuilder
    private func layout<D: View, A: View>(@ViewBuilder _ d: () -> D, @ViewBuilder actions: () -> A) -> some View {
        if wide {
            HStack(spacing: DS.Spacing.m) {
                d()
                Spacer(minLength: DS.Spacing.m)
                HStack(spacing: DS.Spacing.s) { actions() }.fixedSize()
            }
        } else {
            VStack(alignment: .leading, spacing: DS.Spacing.m) {
                d()
                HStack(spacing: DS.Spacing.s) { actions() }.fixedSize()
            }
        }
    }

    private func detail(_ title: String, _ detail: String?, mark: StateMarkKind, color: String) -> some View {
        HStack(spacing: DS.Spacing.s) {
            StateMark(mark)
            Text(title).font(.ds(.chrome, .semibold)).foregroundStyle(Theme.color(color)).fixedSize()
            if let detail {
                Text(detail).font(.ds(.meta)).foregroundStyle(Theme.fg).lineLimit(1).truncationMode(.middle)
            }
        }
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("attention.detail")
    }

    /// An action pill: `number` leads in Mono (1 / 2 / 3), `key` trails as a
    /// Kbd where the key works. Primary: tinted with its color.
    private func button(_ title: String, number: Int? = nil, key: String? = nil, color: String = Theme.Token.working.hex, primary: Bool = false,
                        action: @escaping () -> Void) -> some View {
        Button(action: action) {
            HStack(spacing: DS.Spacing.s) {
                if let number { Text("\(number)").font(.ds(.meta, .medium)).foregroundStyle(Theme.dim) }
                Text(title).font(.ds(.chrome, .medium)).lineLimit(1)
                if let key, keyHints, !tight { Kbd(key) }
            }
            .padding(.horizontal, DS.Spacing.m)
            .frame(height: Pill.height)
            .background(DS.Radius.shape(DS.Radius.control).fill(primary ? Theme.color(color).opacity(Self.primaryFill) : Theme.chipBG))
            .overlay(DS.Radius.shape(DS.Radius.control).strokeBorder(primary ? Theme.color(color).opacity(Self.primaryStroke) : Theme.stroke, lineWidth: 1))
            .foregroundStyle(Theme.fg)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .help(key.map { "\(title) (\($0))" } ?? title)
        .accessibilityLabel(number.map { "\(title), option \($0)" } ?? title)
    }

    /// A primary action's tint: fill and border.
    static let primaryFill = 0.18
    static let primaryStroke = 0.7
}

extension AttentionBar {
    static func hasContent(_ a: Agent) -> Bool {
        switch a.state {
        case .approval, .question, .error, .exited: return true
        case .done, .idle: return !(a.summary ?? "").isEmpty
        default: return false
        }
    }

    /// Below this width the band drops key hints, Always and Open.
    static func isTight(width: CGFloat) -> Bool { width < 420 }

    /// Whether the band fits on one row at `width`.
    static func isWide(_ a: Agent, width: CGFloat, inFocus: Bool = false) -> Bool {
        switch a.state {
        case .approval: return width >= (inFocus ? 520 : 600)
        case .question:
            // Numbered options: about one pill per 120 pt besides the detail.
            let n = a.attention?.options?.count ?? 0
            return width >= max(420, 300 + CGFloat(n) * 120)
        case .error: return width >= 420
        default: return true
        }
    }

    /// The band's heights: one row of the action strip, two rows, the exited
    /// line, the summary line.
    static let oneRow: CGFloat = 46
    static let twoRows: CGFloat = 80
    static let exitedRow: CGFloat = 42
    static let summaryRow: CGFloat = 34

    static func height(_ a: Agent, width: CGFloat, inFocus: Bool = false, confirming: Bool = false) -> CGFloat {
        if confirming { return oneRow }
        switch a.state {
        case .approval, .question, .error: return isWide(a, width: width, inFocus: inFocus) ? oneRow : twoRows
        case .exited: return exitedRow
        case .done, .idle: return hasContent(a) ? summaryRow : 0
        default: return 0
        }
    }
}

/// The close's one question, in the agent's band: "It's waiting for you ·
/// Close anyway ⏎ · Esc", "npm run dev is still running · Close anyway ⏎ ·
/// Esc", "3 files changed in its worktree · Keep worktree ⏎ · Discard ·
/// Esc". Its keys work wherever the keyboard is (MainWindow), so they
/// always show (not on a tight card).
struct CloseStripRow: View {
    var strip: AttentionBar.CloseStrip
    var tight = false

    var body: some View {
        HStack(spacing: DS.Spacing.m) {
            StateMark(mark)
            Text(strip.ask.message).font(.ds(.chrome, .medium)).foregroundStyle(Theme.fg).lineLimit(1).truncationMode(.middle)
            Spacer(minLength: DS.Spacing.m)
            HStack(spacing: DS.Spacing.s) {
                action(strip.ask.primary, key: "⏎", primary: true, run: strip.onPrimary).accessibilityIdentifier("close.primary")
                if let second = strip.ask.secondary {
                    action(second, key: nil, primary: false, run: strip.onSecondary).accessibilityIdentifier("close.secondary")
                }
                action(nil, key: "esc", primary: false, run: strip.onCancel).accessibilityIdentifier("close.cancel")
            }
            .fixedSize()
        }
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("tile.closeStrip")
    }

    /// Needs you keeps its Signal mark (it is what waits); a running
    /// command is working; a worktree is a question.
    private var mark: StateMarkKind {
        switch strip.ask {
        case .needsYou: return .needsYou
        case .foreground: return .working
        case .worktree: return .question
        }
    }

    /// Closing anyway drops something: the primary is tinted `error`;
    /// keeping the worktree is the safe default (`working`).
    private var tint: Theme.Token {
        if case .worktree = strip.ask { return .working }
        return .error
    }

    private func action(_ title: String?, key: String?, primary: Bool, run: @escaping () -> Void) -> some View {
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
        .help(key.map { "\(title ?? "Cancel") (\($0))" } ?? (title ?? ""))
        .accessibilityLabel(title ?? "Cancel")
    }
}

/// On a tile scrolled back: how far, and how much new output waits below;
/// a click goes back to live (also End, or scrolling to the bottom).
struct ScrollPill: View {
    var offset: Int
    var newLines: Int
    var onLive: () -> Void

    var body: some View {
        Button(action: onLive) {
            HStack(spacing: DS.Spacing.s) {
                Image(systemName: "arrow.down").font(.ds(.meta, .semibold))
                Text(newLines > 0 ? "\(newLines) new line\(newLines == 1 ? "" : "s")" : "Back to live")
                    .font(.ds(.chrome, .medium))
                Text("↑\(offset)").font(.ds(.meta)).foregroundStyle(Theme.dim)
            }
            .padding(.horizontal, DS.Spacing.m).frame(height: Pill.height)
            .background(DS.Radius.shape(DS.Radius.control).fill(Theme.color(.surface)))
            .overlay(DS.Radius.shape(DS.Radius.control).strokeBorder(Theme.accent.opacity(AttentionBar.primaryStroke), lineWidth: 1))
            .foregroundStyle(Theme.accent)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .help("Back to the live output (End)")
        .accessibilityIdentifier("tile.scrollPill")
    }
}

/// The deny band: "Deny <tool>:" and a one-line message, ⏎ denies with it,
/// esc folds it back (the text stays for next time).
struct DenyRow: View {
    var agent: Agent
    var wide: Bool
    var field: AttentionBar.DenyField
    @FocusState private var focused: Bool

    var body: some View {
        HStack(spacing: DS.Spacing.m) {
            StateMark(.needsYou)
            Text("Deny \(agent.attention?.title ?? "")").font(.ds(.chrome, .semibold)).foregroundStyle(Theme.color(.signal)).fixedSize()
            TextField("Tell the agent what to do instead (optional)", text: field.text)
                .textFieldStyle(.plain)
                .font(.ds(.chrome))
                .foregroundStyle(Theme.fg)
                .padding(.horizontal, DS.Spacing.m).frame(height: Pill.height)
                .background(DS.Radius.shape(DS.Radius.control).fill(Theme.color(.tile)))
                .overlay(DS.Radius.shape(DS.Radius.control).strokeBorder(Theme.color(.signal).opacity(AttentionBar.primaryStroke), lineWidth: 1))
                .focused($focused)
                .onSubmit { field.onSubmit(field.text.wrappedValue) }
                .onExitCommand { field.onCancel() }
                .accessibilityIdentifier("deny.field")
            HStack(spacing: DS.Spacing.xs) {
                Kbd("⏎"); Text("deny").font(.ds(.meta)).foregroundStyle(Theme.dim)
                Kbd("esc"); Text("back").font(.ds(.meta)).foregroundStyle(Theme.dim)
            }
            .fixedSize()
        }
        .onAppear { DispatchQueue.main.async { focused = true } }
    }
}
