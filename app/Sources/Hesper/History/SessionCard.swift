import AppKit
import HesperCore
import Observation
import SwiftUI

/// What the "where you left off" preview shows; one per panel, updated in
/// place (the hosting view is never rebuilt, SwiftUI diffs the lines).
@MainActor
@Observable
final class SessionCardModel {
    enum Mode: Equatable { case card, continuing }
    var text: SessionCardText?
    var kind: String = "claude"
    var mode: Mode = .card
    /// "Continue in …": the brief (editable) and the kind to start.
    var brief = ""
    var briefLoading = false
    var continueKind = "codex"
    var busy: String?
    /// Buttons call back into the panel (same paths as the keys).
    @ObservationIgnored var onAction: ((SearchKeyAction) -> Void)?
    @ObservationIgnored var onStartContinue: (() -> Void)?
    @ObservationIgnored var onCancelContinue: (() -> Void)?

    /// An action's key → what it does (the preview's buttons).
    static func action(_ key: String) -> SearchKeyAction {
        switch key {
        case "⏎": return .history(.resume)
        case "⌥⏎": return .history(.fork)
        case "⌘⏎": return .continueOnOtherMac
        case "R": return .restoreCheckpoint
        case SearchPreview.restoreScratchKey: return .restoreScratch
        case "F": return .history(.fork)
        case "C": return .history(.continueOther)
        case "A": return .history(.archive)
        case "⌫": return .history(.delete)
        default: return .history(.copyID)
        }
    }
}

/// The card in SwiftUI: the tile's first screen after a resume (compact:
/// you asked / it answered / changed / todos / branch) and the preview's
/// "Continue in …" pane (the brief to edit). Every line has a fixed slot,
/// so the git details arriving later (sessions.show) never move anything.
struct SessionCardView: View {
    @Bindable var model: SessionCardModel
    /// The tile's first screen (resumed): no actions, a note instead.
    var compact = false

    var body: some View {
        if let t = model.text {
            VStack(alignment: .leading, spacing: compact ? DS.Spacing.s : DS.Spacing.m) {
                header(t)
                if model.mode == .continuing && !compact {
                    continuePane(t)
                } else {
                    lines(t)
                }
                Spacer(minLength: 0)
            }
            .padding(compact ? DS.Spacing.m : DS.Spacing.xl)
            .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
            .background {
                if compact { DS.Radius.shape(DS.Radius.tile).fill(Color(nsColor: Theme.ns(.tile))) }
            }
            .overlay {
                if compact { DS.Radius.shape(DS.Radius.tile).strokeBorder(Theme.stroke, lineWidth: 1) }
            }
            .accessibilityElement(children: .contain)
            .accessibilityIdentifier(compact ? "resumeCard" : "history.card")
        } else {
            VStack(spacing: DS.Spacing.m) {
                Image(systemName: "clock.arrow.circlepath").font(DS.font(.sheetTitle)).foregroundStyle(Theme.dim).accessibilityHidden(true)
                Text("Select a session to see where you left off").font(DS.font(.chrome)).foregroundStyle(Theme.dim)
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity)
        }
    }

    private func header(_ t: SessionCardText) -> some View {
        VStack(alignment: .leading, spacing: DS.Spacing.xxs) {
            HStack(spacing: DS.Spacing.m) {
                Text(compact ? "Resumed · where you left off" : t.title).font(DS.font(compact ? .body : .panelTitle, .semibold))
                    .foregroundStyle(Theme.fg).lineLimit(compact ? 1 : 2)
                if let b = model.busy {
                    ProgressView().controlSize(.mini)
                    Text(b).font(DS.font(.chrome)).foregroundStyle(Theme.dim)
                }
            }
            if compact { Text(t.title).font(DS.font(.body, .medium)).foregroundStyle(Theme.fg2).lineLimit(2) }
            Text(t.subtitle).font(DS.font(.meta)).foregroundStyle(Theme.dim).lineLimit(1).truncationMode(.middle)
        }
    }

    private func row<V: View>(_ label: String, @ViewBuilder _ value: () -> V) -> some View {
        HStack(alignment: .firstTextBaseline, spacing: DS.Spacing.l) {
            Text(label).font(DS.font(.chrome)).foregroundStyle(Theme.dim).frame(width: SearchLook.previewLabelWidth, alignment: .leading)
            value().frame(maxWidth: .infinity, alignment: .leading)
        }
    }

    private func lines(_ t: SessionCardText) -> some View {
        VStack(alignment: .leading, spacing: compact ? DS.Spacing.xs : DS.Spacing.s) {
            row("you asked") {
                Text(t.asked.isEmpty ? "—" : t.asked).font(DS.font(.chrome)).foregroundStyle(Theme.fg).lineLimit(3)
            }
            row("it answered") {
                Text(t.answered.isEmpty ? "—" : t.answered).font(DS.font(.chrome)).foregroundStyle(Theme.fg).lineLimit(compact ? 3 : 4)
            }
            row("changed") {
                HStack(spacing: DS.Spacing.s) {
                    if let c = t.changed {
                        Text(c).font(DS.font(.meta)).foregroundStyle(Theme.color(.done)).lineLimit(1)
                        if let u = t.uncommitted { Text("· " + u).font(DS.font(.meta)).foregroundStyle(Theme.color(.question)).lineLimit(1) }
                    } else {
                        Text("reading git…").font(DS.font(.meta)).foregroundStyle(Theme.dim)
                    }
                }
                .accessibilityIdentifier("history.card.changed")
            }
            row("open todos") {
                Text(t.todos.isEmpty ? "none" : t.todos.prefix(3).map { "☐ " + $0 }.joined(separator: "  ·  "))
                    .font(DS.font(.chrome)).foregroundStyle(t.todos.isEmpty ? Theme.dim : Theme.fg2).lineLimit(2)
            }
            row("branch") {
                Text(t.branch).font(DS.font(.meta)).foregroundStyle(Theme.fg2).lineLimit(1).truncationMode(.middle)
            }
        }
    }

    private func continuePane(_ t: SessionCardText) -> some View {
        VStack(alignment: .leading, spacing: DS.Spacing.m) {
            HStack(spacing: DS.Spacing.m) {
                Text("Continue in").font(DS.font(.chrome)).foregroundStyle(Theme.dim)
                Picker("", selection: $model.continueKind) {
                    Text("Claude").tag("claude")
                    Text("Codex").tag("codex")
                }
                .pickerStyle(.segmented).labelsHidden().fixedSize()
                .accessibilityLabel("Continue in")
                Spacer()
                if model.briefLoading { ProgressView().controlSize(.small) }
            }
            Text("The brief starts the new agent (edit it first):").font(DS.font(.chrome)).foregroundStyle(Theme.dim)
            TextEditor(text: $model.brief)
                .font(DS.font(.meta))
                .scrollContentBackground(.hidden)
                .padding(DS.Spacing.s)
                .background(DS.Radius.shape(DS.Radius.control).fill(Color(nsColor: Theme.tileBG)))
                .overlay(DS.Radius.shape(DS.Radius.control).strokeBorder(Theme.stroke, lineWidth: 1))
                .frame(maxHeight: .infinity)
                .accessibilityLabel("Brief")
                .accessibilityIdentifier("history.brief")
            HStack(spacing: DS.Spacing.m) {
                Button { model.onStartContinue?() } label: {
                    Pill("Start in \(SessionFormat.kindLabel(model.continueKind))", variant: .segment(selected: true), kbd: "⌘⏎")
                }
                .buttonStyle(.plain)
                .accessibilityIdentifier("history.brief.start")
                Button { model.onCancelContinue?() } label: {
                    Pill("Back", variant: .segment(selected: false), kbd: "esc")
                }
                .buttonStyle(.plain)
            }
        }
    }
}

/// A resumed agent's tile shows the card as its first screen until the
/// CLI has drawn (at least 0.8 s, at most 12 s; typing into it hides it).
@MainActor
final class SessionCardOverlay: NSView {
    private let host: NSHostingView<SessionCardView>
    private weak var tile: TileView?
    private var timer: Timer?
    private let shownAt = Date()

    override var isFlipped: Bool { true }

    /// Called for every new tile (WallView.sync): a card when the agent
    /// was just resumed from History.
    static func attachIfResumed(_ tile: TileView, model: AppModel) {
        let hub = HistoryHub.shared(for: model)
        guard !tile.subviews.contains(where: { $0 is SessionCardOverlay }), let c = hub.takeResumeCard(tile.agent.id) else { return }
        let m = SessionCardModel()
        m.text = SearchLook.plain(SessionCardText(c.session, changes: c.changes, loadingChanges: false, local: model.localMachine,
                                                  machines: MachineLabel.names(model.machines)),
                                  c.session)
        let o = SessionCardOverlay(model: m, tile: tile)
        tile.addSubview(o)
        o.follow()
        DispatchQueue.main.async { o.follow() }
    }

    private init(model: SessionCardModel, tile: TileView) {
        host = NSHostingView(rootView: SessionCardView(model: model, compact: true))
        self.tile = tile
        super.init(frame: .zero)
        wantsLayer = true
        layer?.backgroundColor = Theme.tileBG.cg(in: self)
        layer?.cornerRadius = Metrics.radius
        layer?.maskedCorners = [.layerMinXMinYCorner, .layerMaxXMinYCorner] // the bottom (unflipped layer)
        addSubview(host)
        setAccessibilityElement(true)
        setAccessibilityIdentifier("tile.resumeCard")
        timer = Timer.scheduledTimer(withTimeInterval: 0.25, repeats: true) { [weak self] _ in
            MainActor.assumeIsolated { self?.check() }
        }
    }

    @available(*, unavailable)
    required init?(coder: NSCoder) { fatalError() }

    override func viewDidChangeEffectiveAppearance() {
        super.viewDidChangeEffectiveAppearance()
        layer?.backgroundColor = Theme.tileBG.cg(in: self)
    }

    /// Below the tile's header, over its terminal (the tile may still be
    /// placed or animating: re-read on every check).
    func follow() {
        guard let tile else { return }
        let top = Metrics.wall.header
        let f = NSRect(x: 0, y: top, width: tile.bounds.width, height: max(0, tile.bounds.height - top))
        if frame != f { frame = f }
    }

    override func layout() {
        super.layout()
        host.frame = bounds.insetBy(dx: DS.Spacing.m, dy: DS.Spacing.m)
    }

    override func hitTest(_ point: NSPoint) -> NSView? { nil } // clicks reach the tile

    private func check() {
        guard let tile else { return remove() }
        follow()
        let age = Date().timeIntervalSince(shownAt)
        if age > 12 || tile.isActive { return remove() }
        guard age > 0.8 else { return }
        if tile.onShelf { return remove() }
        let lines = tile.terminal.surface?.readScreen()?.split(whereSeparator: \.isNewline).filter { !$0.allSatisfy(\.isWhitespace) }.count ?? 0
        if lines >= 2 { remove() }
    }

    private func remove() {
        timer?.invalidate()
        timer = nil
        DS.animate(.move, { self.animator().alphaValue = 0 }, completion: { [weak self] in self?.removeFromSuperview() })
    }
}
