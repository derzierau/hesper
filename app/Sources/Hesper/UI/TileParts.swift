import AppKit
import HesperCore
import SwiftUI

/// Card header (26 pt Comfortable, 22 Compact: `DS.tileHeader`): the state
/// square, the name (Geist 600), one quiet truncated project · branch
/// segment, then on the right the status (elapsed while working), machine
/// · tool in Mono, and two quiet IconButtons (full opacity on hover):
/// open full size ⌘⏎ and ⋯ for the tile's menu. No chips: a remote
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
    /// The header's buttons: open full size (⌘⏎), the tile's menu (⋯).
    var onOpenFull: (() -> Void)? = nil
    var onMenu: (() -> Void)? = nil

    /// The focus (open full size) shortcut.
    static let focusKeys = TileActions.focusKeys
    /// The buttons' opacity until the pointer is over the tile.
    static let quietOpacity = 0.5
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
            Text(compact ? machine : "\(machine) · \(Theme.kindLabel(agent.kind))")
                .font(.ds(.meta))
                .foregroundStyle(local ? Theme.dim : Theme.color(.horizon))
                .lineLimit(1)
                .fixedSize()
                .accessibilityIdentifier("tile.mark")
            HStack(spacing: 0) {
                IconButton("arrow.up.left.and.arrow.down.right", help: "Open full size", shortcut: Self.focusKeys) { onOpenFull?() }
                    .accessibilityIdentifier("tile.openFull")
                IconButton("ellipsis", help: "More") { onMenu?() }
                    .accessibilityIdentifier("tile.menu")
            }
            .fixedSize()
            .opacity(hovering ? 1 : Self.quietOpacity)
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
    /// A narrow card: no key hints; Always and Open fold behind ⋯ (TileActions).
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
                CloseStripRow(strip: closeStrip)
            } else if let moveStrip {
                MoveStripRow(strip: moveStrip)
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

    /// The strip's verbs for this state (TileActions): what to show and
    /// what a tight tile folds behind ⋯. The band's width isn't known
    /// here: `tight` stands for it.
    private var actions: (shown: [TileAction], overflow: [TileAction]) {
        let all = TileActions.actions(agent, inFocus: inFocus, canChoose: onChoose != nil, killed: killed)
        return TileActions.split(all, width: tight ? 0 : TileActions.tightWidth)
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
                    verbs
                }
            case .question:
                // A screen hesperd answers itself (trust, update): numbered
                // answers, the first one also ⏎. Numbered options typed to
                // the agent: the button sends its number, as pressing it in
                // the agent would. Free text: answered in the agent (Open).
                layout {
                    detail(agent.attention?.title ?? "Question", agent.attention?.detail, mark: .question, color: Theme.Token.question.hex)
                } actions: {
                    verbs
                }
            case .error:
                layout {
                    detail(agent.attention?.title ?? "Error", agent.attention?.detail, mark: .error, color: Theme.Token.error.hex)
                } actions: {
                    verbs
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
                    verbs
                }
                .accessibilityIdentifier("tile.killedStrip")
            case .exited:
                HStack(spacing: DS.Spacing.m) {
                    StateMark(.exited)
                    Text(agent.exit.map { "Exited (\($0.label))" } ?? "Exited").font(.ds(.chrome)).foregroundStyle(Theme.dim)
                    Spacer(minLength: DS.Spacing.m)
                    verbs
                }
            default:
                EmptyView()
            }
        }
    }

    /// The strip's KeyButtons, then ⋯ for what a tight tile folds away.
    @ViewBuilder private var verbs: some View {
        let a = actions
        ForEach(a.shown) { keyButton($0) }
        if !a.overflow.isEmpty {
            IconButton("ellipsis", help: "More") { popUpMenu(overflowMenu(a.overflow)) }
                .accessibilityIdentifier("tile.more")
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

    /// A verb as a KeyButton: the primary tinted with its tone, the key
    /// cap only where the key works (not on a tight tile).
    private func keyButton(_ a: TileAction) -> some View {
        KeyButton(a.title, kbd: keyHints && !tight ? a.key : nil, variant: a.primary ? .primary(a.tone) : .quiet) { run(a) }
            .accessibilityLabel(label(a))
            .accessibilityIdentifier(a.id)
    }

    private func label(_ a: TileAction) -> String {
        if case .option(let i) = a.verb { return "\(a.title), option \(i + 1)" }
        return a.title
    }

    private func run(_ a: TileAction) {
        switch a.verb {
        case .answer(let d): onAnswer(d)
        case .option(let i): onChoose?(i)
        case .denyWithMessage: onDenyMessage()
        case .open: onOpen()
        case .resume: onResume()
        case .close: onClose()
        }
    }

    /// The folded verbs as a menu (the key as its equivalent: "Always  A").
    private func overflowMenu(_ items: [TileAction]) -> NSMenu {
        let menu = NSMenu()
        for a in items {
            let i = NSMenuItem(title: a.title, action: #selector(MenuAction.run), keyEquivalent: "")
            if let chord = a.key.flatMap(KeyChord.init(label:)), let (key, mods) = MenuBuilder.keyEquivalent(chord) {
                i.keyEquivalent = key
                i.keyEquivalentModifierMask = mods
            }
            let act = MenuAction { run(a) }
            i.target = act
            i.representedObject = act
            i.setAccessibilityIdentifier(a.id)
            menu.addItem(i)
        }
        return menu
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

    /// Below this width the band drops key hints and folds Always and
    /// Open into ⋯ (TileActions).
    static func isTight(width: CGFloat) -> Bool { TileActions.isTight(width: width) }

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
/// Close anyway ⏎ · Keep esc", "npm run dev is still running · Close
/// anyway ⏎ · Keep esc", "3 files changed in its worktree · Keep worktree
/// ⏎ · Discard · Keep esc". The strip takes the keyboard when it appears
/// (StripKeys): a ring in its tone says so, and ⏎ / esc are its own.
struct CloseStripRow: View {
    var strip: AttentionBar.CloseStrip
    @State private var focused = false

    var body: some View {
        HStack(spacing: DS.Spacing.m) {
            StateMark(mark)
            Text(strip.ask.message).font(.ds(.chrome, .medium)).foregroundStyle(Theme.fg).lineLimit(1).truncationMode(.middle)
            Spacer(minLength: DS.Spacing.m)
            HStack(spacing: DS.Spacing.s) {
                KeyButton(strip.ask.primary, kbd: "⏎", variant: .primary(tone), action: strip.onPrimary).accessibilityIdentifier("close.primary")
                if let second = strip.ask.secondary {
                    KeyButton(second, action: strip.onSecondary).accessibilityIdentifier("close.secondary")
                }
                KeyButton("Keep", kbd: "esc", action: strip.onCancel).accessibilityIdentifier("close.cancel")
            }
            .fixedSize()
        }
        .stripKeys(tone: tone, focused: $focused, onPrimary: strip.onPrimary, onCancel: strip.onCancel)
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
    private var tone: StateMarkKind.Tone {
        if case .worktree = strip.ask { return .working }
        return .error
    }
}

/// A strip that asks (close, move) has the keyboard while it shows: an
/// invisible view behind the row becomes first responder when it
/// appears, answers ⏎ and esc itself and passes every other key up (the
/// wall's arrows still move), and hands the keyboard back when it goes
/// (the agent of an active tile or the focus view, else the wall). The
/// row draws a `DS.focusRing` ring in its tone while it has the keys.
extension View {
    func stripKeys(tone: StateMarkKind.Tone, focused: Binding<Bool>, onPrimary: @escaping () -> Void, onCancel: @escaping () -> Void) -> some View {
        background(StripKeys(focused: focused, onPrimary: onPrimary, onCancel: onCancel))
            .overlay {
                if focused.wrappedValue {
                    DS.Radius.shape(DS.Radius.control).strokeBorder(Theme.color(Theme.token(tone)), lineWidth: DS.focusRing)
                        .padding(-DS.Spacing.s)
                }
            }
    }
}

private struct StripKeys: NSViewRepresentable {
    @Binding var focused: Bool
    var onPrimary: () -> Void
    var onCancel: () -> Void

    func makeNSView(context: Context) -> StripKeyView {
        let v = StripKeyView()
        v.onFocus = { focused = $0 }
        return v
    }

    func updateNSView(_ v: StripKeyView, context: Context) {
        v.onPrimary = onPrimary
        v.onCancel = onCancel
    }
}

/// The strip's first responder (StripKeys). `MainWindow` routes nothing
/// to the strip: these keys are its own.
@MainActor
final class StripKeyView: NSView {
    var onPrimary: (() -> Void)?
    var onCancel: (() -> Void)?
    var onFocus: ((Bool) -> Void)?

    override var acceptsFirstResponder: Bool { true }

    override func viewDidMoveToWindow() {
        super.viewDidMoveToWindow()
        guard let window else { return }
        DispatchQueue.main.async { [weak self] in
            guard let self, self.window === window else { return }
            window.makeFirstResponder(self)
        }
    }

    override func viewWillMove(toWindow newWindow: NSWindow?) {
        super.viewWillMove(toWindow: newWindow)
        guard newWindow == nil, let window, window.firstResponder === self else { return }
        var v = superview
        while let s = v {
            if let t = s as? TileView { t.handBackFocus(); return }
            if let f = s as? FocusView { f.focusTerminal(); return }
            v = s.superview
        }
    }

    override func becomeFirstResponder() -> Bool {
        let ok = super.becomeFirstResponder()
        if ok { onFocus?(true) }
        return ok
    }

    override func resignFirstResponder() -> Bool {
        let ok = super.resignFirstResponder()
        if ok { onFocus?(false) }
        return ok
    }

    override func mouseDown(with event: NSEvent) { window?.makeFirstResponder(self) }

    override func keyDown(with event: NSEvent) {
        let chord = KeyChord(event: event)
        if chord.plain && !chord.shift {
            if chord.key == .enter { onPrimary?(); return }
            if chord.key == .escape { onCancel?(); return }
        }
        super.keyDown(with: event)
    }
}

/// Pops `menu` under the pointer, from a SwiftUI button (an IconButton's
/// ⋯): the click that pressed it is the current event.
@MainActor func popUpMenu(_ menu: NSMenu) {
    guard let e = NSApp.currentEvent, let v = (e.window ?? NSApp.keyWindow)?.contentView else { return }
    NSMenu.popUpContextMenu(menu, with: e, for: v)
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

/// The deny band: "Deny <tool>:" and a one-line message, Send ⏎ denies
/// with it, Cancel esc folds it back (the text stays for next time).
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
            HStack(spacing: DS.Spacing.s) {
                KeyButton("Send", kbd: "⏎", variant: .primary(.signal)) { field.onSubmit(field.text.wrappedValue) }
                    .accessibilityIdentifier("deny.send")
                KeyButton("Cancel", kbd: "esc", action: field.onCancel)
                    .accessibilityIdentifier("deny.cancel")
            }
            .fixedSize()
        }
        .onAppear { DispatchQueue.main.async { focused = true } }
    }
}
