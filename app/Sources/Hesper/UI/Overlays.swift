import AppKit
import HesperCore
import SwiftUI

/// The one look every overlay shares: the floating glass of the design
/// system (material, 1 px `line` hairline, radius `panel`, one soft
/// shadow; solid `surface` under Reduce Transparency). Opens with
/// `DS.Motion.quick` (scale 0.98 → 1 and fade), closes with a fade;
/// Reduce Motion: no animation.
enum OverlayLook {
    static let surface = Theme.color(.surface)
    static let border = Theme.color(.line)
    static let line = Theme.color(.line)
    static let selection = Theme.selection
    static let radius = DS.Radius.panel
    static let arrow: CGFloat = 7
    static let rowHeight = DS.chromeMaxHeight
    /// A row's leading mark ("M", "⑂"): a square kbd-sized chip.
    static let markSide = DS.Spacing.xl + DS.Spacing.xxs
    /// Disabled rows (an offline machine).
    static let disabledOpacity = 0.42
    /// The ⌘K scrim; cards that need the user show through its holes.
    static let scrimOpacity = 0.32
    static let scrimHoleRadius = DS.Radius.tile + DS.Spacing.s
    static let openScale = 0.98

    /// Popover widths, by what they hold.
    static func width(_ kind: PopoverKind) -> CGFloat {
        switch kind {
        case .attention: return inboxWidth
        case .background: return backgroundWidth
        case .machines: return 330
        case .layout: return 270
        case .scope: return 300 // window layer
        case .desks: return 390
        case .deskName: return 320
        case .move: return 300
        case .rename: return 320
        case .chip: return 320
        }
    }
    static let inboxWidth: CGFloat = 420
    static let backgroundWidth: CGFloat = 360
    static let completionWidth: CGFloat = 330
    static let paletteWidth: CGFloat = 620
    /// The palette sits this far down the window (at least `paletteMinTop`).
    static let paletteTopFraction: CGFloat = 0.14
    static let paletteMinTop: CGFloat = 60

    static func transition(reduceMotion: Bool) -> AnyTransition {
        guard let open = DS.animation(.quick, reduceMotion: reduceMotion) else { return .identity }
        return .asymmetric(
            insertion: AnyTransition.scale(scale: openScale).combined(with: .opacity).animation(open),
            removal: AnyTransition.opacity.animation(DS.animation(.quick, reduceMotion: reduceMotion)))
    }

    static func fade(reduceMotion: Bool) -> AnyTransition {
        DS.animation(.quick, reduceMotion: reduceMotion).map { AnyTransition.opacity.animation($0) } ?? .identity
    }
}

/// The floating glass in any shape (a popover's Bubble, the panel's
/// rounded rect).
struct GlassSurface<S: Shape>: ViewModifier {
    var shape: S
    @Environment(\.accessibilityReduceTransparency) private var reduceTransparency

    func body(content: Content) -> some View {
        let s = DS.Elevation.floating.shadow!
        return content
            .background {
                if reduceTransparency { shape.fill(Theme.surface) } else { shape.fill(.regularMaterial) }
            }
            .overlay(shape.stroke(OverlayLook.border, lineWidth: 1))
            .compositingGroup()
            .shadow(color: .black.opacity(Double(s.opacity)), radius: s.radius / 2, y: s.y)
    }
}

extension View {
    /// The overlay look (glass) with a radius (the palette, sheets).
    func overlaySurface(radius: CGFloat = OverlayLook.radius) -> some View {
        modifier(GlassSurface(shape: DS.Radius.shape(radius)))
    }

    func overlayGlass<S: Shape>(_ shape: S) -> some View { modifier(GlassSurface(shape: shape)) }
}

/// Frames the overlay host should take clicks in (toasts, the completion
/// list) when nothing modal is open.
struct HotRectsKey: PreferenceKey {
    nonisolated(unsafe) static var defaultValue: [String: CGRect] = [:]
    static func reduce(value: inout [String: CGRect], nextValue: () -> [String: CGRect]) { value.merge(nextValue()) { $1 } }
}

extension View {
    func hotRect(_ id: String) -> some View {
        background(GeometryReader { g in Color.clear.preference(key: HotRectsKey.self, value: [id: g.frame(in: .named("overlay"))]) })
    }
}

/// Click routing of the overlay layer (kept out of SwiftUI's state).
@MainActor
final class OverlayHit {
    var catchAll = false
    var hot: [String: CGRect] = [:]
}

/// The overlay layer over the wall/focus view: ⌘K palette (the only
/// centered one, over a light scrim that keeps attention rings visible),
/// popovers attached to what they act on (⌘J's inbox under the toolbar's
/// attention pill), the composer's completion list, toasts (undo).
struct OverlayRoot: View {
    @Bindable var model: AppModel
    var hit: OverlayHit
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    var body: some View {
        GeometryReader { geo in
            ZStack(alignment: .topLeading) {
                if model.showPalette {
                    Scrim(holes: model.attentionFrames?() ?? [])
                        .onTapGesture { model.showPalette = false }
                        .transition(OverlayLook.fade(reduceMotion: reduceMotion))
                    VStack(spacing: 0) {
                        PaletteView(model: model).frame(width: min(OverlayLook.paletteWidth, geo.size.width - 2 * DS.Spacing.xxl))
                        Spacer(minLength: 0)
                    }
                    .padding(.top, max(OverlayLook.paletteMinTop, geo.size.height * OverlayLook.paletteTopFraction))
                    .frame(width: geo.size.width, height: geo.size.height, alignment: .top)
                    .transition(OverlayLook.transition(reduceMotion: reduceMotion))
                }
                if let p = model.popover {
                    Color.black.opacity(0.001)
                        .onTapGesture { model.closePopover() }
                    popover(p, in: geo.size)
                        .id(p.key)
                        .transition(OverlayLook.transition(reduceMotion: reduceMotion))
                }
                if let id = model.editingDraftID, let c = model.composers[id], let comp = c.completion, model.popover == nil, !model.showPalette,
                   let caret = c.caretRect?() {
                    completion(c, comp, caret: caret, in: geo.size)
                        .transition(OverlayLook.transition(reduceMotion: reduceMotion))
                }
                toasts.frame(width: geo.size.width, height: geo.size.height, alignment: .bottom)
            }
            .frame(width: geo.size.width, height: geo.size.height, alignment: .topLeading)
        }
        .ignoresSafeArea()
        .coordinateSpace(name: "overlay")
        .onPreferenceChange(HotRectsKey.self) { r in MainActor.assumeIsolated { hit.hot = r } }
        .animation(DS.animation(.quick, reduceMotion: reduceMotion), value: model.showPalette)
        .animation(DS.animation(.quick, reduceMotion: reduceMotion), value: model.popover)
        .animation(DS.animation(.quick, reduceMotion: reduceMotion), value: model.undoToast?.id)
    }

    // MARK: Popovers

    private func avoid() -> [Rect] {
        (model.attentionFrames?() ?? []).map { Rect(x: $0.minX, y: $0.minY, width: $0.width, height: $0.height) }
    }

    @ViewBuilder private func popover(_ kind: PopoverKind, in size: CGSize) -> some View {
        if kind == .attention {
            attention(in: size)
        } else if kind == .background {
            backgroundTray(in: size)
        } else {
            bubble(kind, in: size)
        }
    }

    /// ⌘J: the inbox, a glass panel under the toolbar's attention pill.
    private func attention(in size: CGSize) -> some View {
        let entries = model.attentionEntries()
        let width = AttentionPanel.width
        let maxList = max(AttentionPanel.minListHeight, size.height * AttentionPanel.maxHeightFraction - AttentionPanel.chromeHeight)
        let listH = min(AttentionPanel.listHeight(entries, width: width), maxList)
        let h = AttentionPanel.chromeHeight + listH
        let a = model.popoverAnchor
        let placed = PopoverPlacement.place(anchor: Rect(x: a.minX, y: a.minY, width: a.width, height: a.height),
                                            width: width, height: h + OverlayLook.arrow,
                                            container: Rect(x: 0, y: 0, width: size.width, height: size.height),
                                            avoid: avoid())
        return AttentionPanel(model: model, entries: entries, listHeight: listH)
            .frame(width: width, height: h)
            .overlayGlass(DS.Radius.shape(DS.Radius.panel))
            .contentShape(Rectangle())
            .onTapGesture {}
            .offset(x: placed.frame.x, y: placed.frame.y + (placed.below ? OverlayLook.arrow : 0))
            .accessibilityIdentifier("popover.attention")
    }

    /// ⌥⌘W's tray: a glass panel under the toolbar's Background pill.
    private func backgroundTray(in size: CGSize) -> some View {
        let agents = model.backgroundAgents
        let names = MachineLabel.names(model.machines)
        let rows = agents.map { BackgroundPanel.RowData(agent: $0, meta: BackgroundTray.meta($0, machine: names[$0.machine] ?? $0.machine)) }
        let h = BackgroundPanel.height(rows.count)
        let a = model.popoverAnchor
        let placed = PopoverPlacement.place(anchor: Rect(x: a.minX, y: a.minY, width: a.width, height: a.height),
                                            width: BackgroundPanel.width, height: h + OverlayLook.arrow,
                                            container: Rect(x: 0, y: 0, width: size.width, height: size.height),
                                            avoid: avoid())
        let sel = model.popoverList.selection(enabled: rows.map { _ in true })
        return BackgroundPanel(rows: rows, summary: model.backgroundSummary, selected: sel,
                               onBringBack: { [weak model] a in model?.bringBackFromTray(a) },
                               onClose: { [weak model] a in
                                   model?.close(a)
                                   if model?.backgroundAgents.isEmpty == true { model?.closePopover() }
                               })
            .frame(width: BackgroundPanel.width, height: h)
            .overlayGlass(DS.Radius.shape(DS.Radius.panel))
            .contentShape(Rectangle())
            .onTapGesture {}
            .offset(x: placed.frame.x, y: placed.frame.y + (placed.below ? OverlayLook.arrow : 0))
            .accessibilityIdentifier("popover.background")
    }

    private func bubble(_ kind: PopoverKind, in size: CGSize) -> some View {
        let content = model.popoverContent(kind)
        let list = model.popoverLists[kind.key] ?? OverlayList()
        let width = OverlayLook.width(kind)
        let h = PopoverPanel.height(content, filterable: kind.filterable)
        let a = model.popoverAnchor
        let placed = PopoverPlacement.place(anchor: Rect(x: a.minX, y: a.minY, width: a.width, height: a.height),
                                            width: width, height: h,
                                            container: Rect(x: 0, y: 0, width: size.width, height: size.height),
                                            avoid: avoid())
        return PopoverPanel(model: model, kind: kind, content: content, list: list, width: width)
            .frame(width: width, height: h)
            .overlayGlass(Bubble(arrowX: placed.arrowX, below: placed.below))
            .contentShape(Rectangle())
            .onTapGesture {}
            .offset(x: placed.frame.x, y: placed.frame.y)
            .accessibilityIdentifier("popover.\(kind.key.split(separator: ":").first ?? "")")
    }

    private func completion(_ c: ComposerModel, _ comp: ComposerModel.CompletionState, caret: CGRect, in size: CGSize) -> some View {
        let rows = min(comp.items.count, 7)
        let h = CGFloat(rows) * OverlayLook.rowHeight + 2 * DS.Spacing.s + Hints.height + OverlayLook.arrow
        let width = OverlayLook.completionWidth
        let placed = PopoverPlacement.place(anchor: Rect(x: caret.minX - DS.Spacing.s, y: caret.minY, width: 2 * DS.Spacing.s, height: caret.height),
                                            width: width, height: h,
                                            container: Rect(x: 0, y: 0, width: size.width, height: size.height),
                                            avoid: avoid())
        let sel = comp.selected
        return VStack(spacing: 0) {
            ScrollViewReader { proxy in
                ScrollView(showsIndicators: false) {
                    VStack(spacing: 0) {
                        ForEach(Array(comp.items.enumerated()), id: \.element.id) { i, item in
                            OverlayRow(title: item.title, detail: item.detail, mark: item.mark, dot: nil, checked: false,
                                       selected: i == sel, enabled: item.enabled, tint: TokenStyle.fg(item.kind))
                                .id(i)
                                .onTapGesture { if item.enabled { c.accept(item) } }
                        }
                    }
                    .padding(DS.Spacing.s)
                }
                .onChange(of: sel) { if let sel { proxy.scrollTo(sel) } }
            }
            Hints(items: [("↑↓", "select"), ("⏎", "insert"), ("esc", "close")], leading: comp.token.kind.title)
        }
        .padding(placed.below ? .top : .bottom, OverlayLook.arrow)
        .frame(width: width, height: h)
        .overlayGlass(Bubble(arrowX: placed.arrowX, below: placed.below))
        .hotRect("completion")
        .offset(x: placed.frame.x, y: placed.frame.y)
        .accessibilityIdentifier("completion")
    }

    // MARK: Toasts

    @ViewBuilder private var toasts: some View {
        VStack(spacing: DS.Spacing.m) {
            Spacer()
            if let t = model.toast, let action = t.action {
                // "Started fix-relay in news-api · Show ↗": the agent
                // went to a window that shows its project.
                HStack(spacing: DS.Spacing.l) {
                    Text(t.text).font(.ds(.chrome)).foregroundStyle(Theme.fg).lineLimit(1)
                    Button { model.toast = nil; action.run() } label: {
                        Text(action.title).font(.ds(.chrome, .medium)).foregroundStyle(Theme.accent)
                            .padding(.horizontal, DS.Spacing.m).frame(height: Pill.height)
                            .background(DS.Radius.shape(DS.Radius.control).fill(Theme.selection))
                            .contentShape(Rectangle())
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel(action.title.replacingOccurrences(of: " ↗", with: ""))
                    .accessibilityIdentifier("toast.action")
                }
                .padding(.leading, DS.Spacing.l).padding(.trailing, DS.Spacing.m).padding(.vertical, DS.Spacing.s)
                .overlayGlass(DS.Radius.shape(DS.Radius.tile))
                .hotRect("toast")
                .accessibilityElement(children: .contain)
                .accessibilityIdentifier("toast")
                .transition(OverlayLook.fade(reduceMotion: reduceMotion))
            } else if let t = model.toast {
                Text(t.text)
                    .font(.ds(.chrome))
                    .foregroundStyle(Theme.fg)
                    .padding(.horizontal, DS.Spacing.l).padding(.vertical, DS.Spacing.m)
                    .overlayGlass(DS.Radius.shape(DS.Radius.tile))
                    .overlay(DS.Radius.shape(DS.Radius.tile).stroke(t.isError ? Theme.color(.error) : .clear))
                    .accessibilityIdentifier("toast")
                    .transition(OverlayLook.fade(reduceMotion: reduceMotion))
            }
            if let u = model.undoToast {
                UndoToast(label: u.label, reopen: Self.offersReopen(u.action), onUndo: { model.undo(u.id) })
                    .hotRect("undo")
                    .transition(OverlayLook.transition(reduceMotion: reduceMotion))
            }
        }
        .padding(.bottom, DS.Spacing.xl)
    }
}

extension OverlayRoot {
    /// A close's toast also offers ⌘⇧T (the reopen stack).
    static func offersReopen(_ a: UndoAction) -> Bool {
        if case .reopen = a { return true }
        return false
    }
}

/// The undo toast: what happened, then Undo ⌘Z (and for a close, ⌘⇧T
/// reopen: "Closed api · Undo ⌘Z · Reopen ⌘⇧T").
struct UndoToast: View {
    var label: String
    var reopen = false
    var onUndo: () -> Void

    var body: some View {
        HStack(spacing: DS.Spacing.l) {
            Text(label).font(.ds(.body)).foregroundStyle(Theme.fg).lineLimit(1)
            Button(action: onUndo) {
                HStack(spacing: DS.Spacing.xs) {
                    Text("Undo").font(.ds(.chrome, .medium)).foregroundStyle(Theme.accent)
                    Kbd("⌘Z")
                }
                .padding(.horizontal, DS.Spacing.m).frame(height: Pill.height)
                .background(DS.Radius.shape(DS.Radius.control).fill(Theme.selection))
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .accessibilityLabel("Undo (⌘Z)")
            .accessibilityIdentifier("undo.button")
            if reopen {
                HStack(spacing: DS.Spacing.xs) {
                    Text("Reopen").font(.ds(.chrome)).foregroundStyle(Theme.dim)
                    Kbd("⌘⇧T")
                }
                .fixedSize()
                .help("Reopen the last closed agent (⌘⇧T); again for the one before")
                .accessibilityIdentifier("undo.reopen")
            }
        }
        .padding(.leading, DS.Spacing.l).padding(.trailing, DS.Spacing.m).padding(.vertical, DS.Spacing.s)
        .overlayGlass(DS.Radius.shape(DS.Radius.tile))
        .accessibilityIdentifier("undo.toast")
    }
}

// MARK: - The background tray

/// ⌥⌘W's tray: "Background · 2" and how many need you, then a Row per
/// agent (its StateMark, name, "machine · tool · age"), the selected one
/// with ⏎. ⏎ / click brings it back onto its wall (one that needs you
/// opens like ⌘J), ⌘W closes it, esc dismisses.
struct BackgroundPanel: View {
    struct RowData: Identifiable {
        var agent: Agent
        var meta: String
        var id: String { agent.id }
    }

    var rows: [RowData]
    var summary: BackgroundSummary
    var selected: Int?
    var onBringBack: (Agent) -> Void
    var onClose: (Agent) -> Void

    static let width = OverlayLook.backgroundWidth
    static let maxRows = 8
    static let hints = [("⏎", "bring back"), ("⌘W", "close agent"), ("esc", "dismiss")]

    static func height(_ n: Int) -> CGFloat {
        let rows = CGFloat(max(1, min(n, maxRows)))
        return PopoverPanel.titleHeight + rows * Row.height(subtitle: false) + 2 * DS.Spacing.s + Hints.height
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack(spacing: DS.Spacing.m) {
                Text(summary.title).font(.ds(.chrome, .semibold)).foregroundStyle(Theme.muted)
                Spacer(minLength: DS.Spacing.m)
                if summary.needsYou > 0 {
                    Pill("\(summary.needsYou) \(summary.needsYou == 1 ? "needs" : "need") you", variant: .status, mark: .needsYou)
                        .accessibilityIdentifier("background.needsYou")
                }
            }
            .padding(.horizontal, DS.Spacing.m)
            .frame(height: PopoverPanel.titleHeight)
            if rows.isEmpty {
                Text("Nothing in the background").font(.ds(.chrome)).foregroundStyle(Theme.dim)
                    .padding(.horizontal, DS.Spacing.m).frame(height: Row.height(subtitle: false))
            }
            ScrollView(showsIndicators: false) {
                VStack(spacing: 0) {
                    ForEach(Array(rows.enumerated()), id: \.element.id) { i, r in
                        Row(title: r.agent.name, meta: r.meta, mark: StateMarkKind(r.agent.state), kbd: i == selected ? "⏎" : nil, selected: i == selected)
                            .onTapGesture { onBringBack(r.agent) }
                            .contextMenu {
                                Button("Bring Back") { onBringBack(r.agent) }
                                Button("Close") { onClose(r.agent) }
                            }
                            .accessibilityIdentifier("background.row.\(r.id)")
                    }
                }
            }
            .frame(maxHeight: CGFloat(min(max(rows.count, 1), Self.maxRows)) * Row.height(subtitle: false))
            Spacer(minLength: 0)
            Hints(items: Self.hints)
        }
        .padding(.horizontal, DS.Spacing.s)
        .padding(.top, DS.Spacing.s)
        .frame(width: Self.width, alignment: .topLeading)
    }
}


// MARK: - ⌘J inbox

/// ⌘J: what needs you, as an inbox. Each item: its state mark, the agent,
/// its machine (and "other wall" / "other Mac"), the exact question and
/// its answers inline — approvals "1 Allow · 2 Always · 3 Deny",
/// numbered questions one Pill per option, free text "Open". J/K or ↑↓
/// move, 1–9 answer the selected item, ⏎ its first answer, ⌘O (⌘⏎) opens
/// its tile, esc closes. Empty: "Nothing needs you." and it closes itself
/// after a second.
struct AttentionPanel: View {
    @Bindable var model: AppModel
    var entries: [AppModel.AttentionEntry]
    var listHeight: CGFloat

    static let width = OverlayLook.inboxWidth
    static let emptyText = "Nothing needs you."
    static let hints: [(String, String)] = [("J K", "move"), ("1–9", "answer"), ("⏎", "first"), ("⌘O", "open"), ("esc", "close")]
    /// The empty panel closes itself after this long.
    static let emptyClose: Duration = .seconds(1)
    /// Option titles longer than this are cut (the agent has the full text).
    static let maxAnswerTitle = 32
    static let maxHeightFraction: CGFloat = 0.7
    static let minListHeight = 2 * DS.chromeMaxHeight

    // Geometry (the panel is sized before SwiftUI lays it out: placement).
    static var headerHeight: CGFloat { DS.chromeMaxHeight + DS.Spacing.xs }
    static var chromeHeight: CGFloat { headerHeight + Hints.height + DS.Spacing.s }
    static let nameLine = DS.Spacing.l + DS.Spacing.s
    static let questionFont = DS.nsFont(.chrome)
    static let questionLine = ceil(NSLayoutManager().defaultLineHeight(for: questionFont))
    static let questionLines = 2
    static var itemPadding: CGFloat { DS.Spacing.m }
    static var itemSpacing: CGFloat { DS.Spacing.xxs }

    /// How many lines (1–2) a question takes in the item.
    static func lines(_ question: String, width: CGFloat) -> Int {
        let w = width - 2 * DS.Spacing.s - 2 * itemPadding
        let r = (question as NSString).boundingRect(with: NSSize(width: max(1, w), height: .greatestFiniteMagnitude),
                                                    options: [.usesLineFragmentOrigin, .usesFontLeading], attributes: [.font: questionFont])
        return max(1, min(questionLines, Int(ceil(r.height / questionLine - 0.01))))
    }

    static func itemHeight(_ e: AppModel.AttentionEntry, width: CGFloat) -> CGFloat {
        2 * itemPadding + nameLine + DS.Spacing.xs + CGFloat(lines(e.question, width: width)) * questionLine + DS.Spacing.s + Pill.height
    }

    static func listHeight(_ entries: [AppModel.AttentionEntry], width: CGFloat) -> CGFloat {
        guard !entries.isEmpty else { return minListHeight }
        return entries.reduce(0) { $0 + itemHeight($1, width: width) } + CGFloat(entries.count - 1) * itemSpacing + 2 * DS.Spacing.s
    }

    var body: some View {
        let list = model.popoverLists[PopoverKind.attention.key] ?? OverlayList()
        let sel = list.selection(enabled: entries.map { _ in true })
        VStack(alignment: .leading, spacing: 0) {
            HStack(spacing: DS.Spacing.s) {
                Text("Needs you · \(entries.count)").font(.ds(.body, .semibold)).foregroundStyle(Theme.fg)
                    .accessibilityIdentifier("attention.title")
                Spacer(minLength: DS.Spacing.s)
                Kbd("⌘J")
            }
            .padding(.horizontal, DS.Spacing.l)
            .frame(height: Self.headerHeight)
            .accessibilityElement(children: .combine)
            .accessibilityLabel("Needs you, \(entries.count)")
            Rectangle().fill(OverlayLook.line).frame(height: 1)
            if entries.isEmpty {
                Text(Self.emptyText).font(.ds(.body)).foregroundStyle(Theme.dim)
                    .frame(maxWidth: .infinity, minHeight: listHeight)
                    .accessibilityIdentifier("attention.empty")
            } else {
                ScrollViewReader { proxy in
                    ScrollView(showsIndicators: false) {
                        VStack(spacing: Self.itemSpacing) {
                            ForEach(Array(entries.enumerated()), id: \.element.id) { i, e in
                                AttentionItem(entry: e, selected: i == sel, width: Self.width,
                                              onAnswer: { a in model.answerFromQueue(e, a) },
                                              onSelect: { model.selectAttention(e.id) },
                                              onOpen: { model.openFromQueue(e.agent) })
                                    .id(e.id)
                            }
                        }
                        .padding(DS.Spacing.s)
                    }
                    .frame(height: listHeight)
                    .onChange(of: sel) { if let sel, sel < entries.count { proxy.scrollTo(entries[sel].id) } }
                }
            }
            Spacer(minLength: 0)
            Hints(items: Self.hints)
        }
        // Empty (the last one answered): says so, then closes itself.
        .task(id: entries.isEmpty) {
            guard entries.isEmpty else { return }
            try? await Task.sleep(for: Self.emptyClose)
            if !Task.isCancelled, model.popover == .attention, model.attentionEntries().isEmpty { model.closePopover() }
        }
    }
}

/// One inbox item.
struct AttentionItem: View {
    var entry: AppModel.AttentionEntry
    var selected: Bool
    var width: CGFloat
    var onAnswer: (InboxAnswer) -> Void
    var onSelect: () -> Void
    var onOpen: () -> Void

    @State private var hover = false

    private var primaryMark: StateMarkKind {
        switch entry.agent.state {
        case .approval: return .needsYou
        case .question: return .question
        default: return .error
        }
    }

    private var stateLabel: String {
        switch entry.agent.state {
        case .approval: return "needs approval"
        case .question: return "has a question"
        default: return "has an error"
        }
    }

    var body: some View {
        let a = entry.agent
        let lines = AttentionPanel.lines(entry.question, width: width)
        VStack(alignment: .leading, spacing: 0) {
            HStack(spacing: DS.Spacing.s) {
                StateMark(state: a.state)
                Text(a.name).font(.ds(.body, .medium)).foregroundStyle(Theme.fg).lineLimit(1).truncationMode(.middle)
                Text(([a.machine] + entry.whereabouts).joined(separator: " · ")).font(.ds(.meta)).foregroundStyle(Theme.fg2).lineLimit(1)
                Spacer(minLength: DS.Spacing.s)
                if !entry.since.isEmpty { Text(entry.since).font(.ds(.meta)).foregroundStyle(Theme.dim).fixedSize() }
            }
            .frame(height: AttentionPanel.nameLine)
            Text(entry.question)
                .font(.ds(.chrome)).foregroundStyle(Theme.fg2)
                .lineLimit(lines).truncationMode(.tail)
                .frame(maxWidth: .infinity, alignment: .topLeading)
                .frame(height: CGFloat(lines) * AttentionPanel.questionLine, alignment: .topLeading)
                .padding(.top, DS.Spacing.xs)
            HStack(spacing: DS.Spacing.s) {
                ScrollView(.horizontal, showsIndicators: false) {
                    HStack(spacing: DS.Spacing.s) {
                        ForEach(Array(entry.answers.enumerated()), id: \.offset) { i, ans in
                            answerButton(ans, primary: i == 0)
                        }
                    }
                }
                .disabled(entry.answered)
                if entry.answered {
                    Text("Sent").font(.ds(.meta)).foregroundStyle(Theme.dim).fixedSize()
                        .accessibilityIdentifier("attention.sent")
                }
            }
            .frame(height: Pill.height)
            .padding(.top, DS.Spacing.s)
        }
        .padding(AttentionPanel.itemPadding)
        .background(DS.Radius.shape(DS.Radius.control).fill(selected ? Theme.selection : hover ? Theme.chipBG.opacity(0.6) : .clear))
        .opacity(entry.answered ? OverlayLook.disabledOpacity + 0.2 : 1)
        .contentShape(Rectangle())
        .onHover { hover = $0 }
        .onTapGesture(count: 2) { onOpen() }
        .onTapGesture { onSelect() }
        .accessibilityElement(children: .contain)
        .accessibilityLabel("\(a.name), \(a.machine), \(stateLabel): \(entry.question)")
        .accessibilityAddTraits(selected ? .isSelected : [])
        .accessibilityIdentifier("attention.item.\(a.id)")
    }

    private func answerButton(_ ans: InboxAnswer, primary: Bool) -> some View {
        let title = ans.title.count > AttentionPanel.maxAnswerTitle ? String(ans.title.prefix(AttentionPanel.maxAnswerTitle - 1)) + "…" : ans.title
        let key = ans.key.map(String.init) ?? (primary ? "⏎" : nil)
        return Button { onAnswer(ans) } label: {
            Pill(title, variant: .status, mark: primary ? primaryMark : nil, kbd: key)
                .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .help(ans.title)
        .accessibilityLabel(ans.title + (key.map { ", key \($0)" } ?? ""))
        .accessibilityIdentifier("attention.answer.\(entry.agent.id).\(ans.key ?? 0)")
    }
}

// MARK: - Popover list

/// A popover's list (keys come through the window: ↑↓ ⏎ ⌘⏎ esc, typing).
struct PopoverPanel: View {
    @Bindable var model: AppModel
    var kind: PopoverKind
    var content: PopoverContent
    var list: OverlayList
    var width: CGFloat
    @FocusState private var fieldFocused: Bool

    static let titleHeight = DS.chromeMaxHeight
    static let filterHeight = DS.chromeMaxHeight + DS.Spacing.xxs
    static let fieldHeight = DS.chromeMaxHeight + 2 * DS.Spacing.m
    static let noteHeight = DS.chromeMaxHeight + DS.Spacing.xs
    static let maxRows = 9

    static func height(_ c: PopoverContent, filterable: Bool) -> CGFloat {
        var h: CGFloat = 2 * DS.Spacing.s + OverlayLook.arrow
        if c.title != nil { h += titleHeight }
        if filterable { h += filterHeight }
        if c.field != nil { h += fieldHeight }
        h += CGFloat(max(c.field == nil ? 1 : 0, min(c.items.count, maxRows))) * OverlayLook.rowHeight
        if c.note != nil { h += noteHeight }
        h += Hints.height
        return h
    }

    var body: some View {
        let sel = list.selection(enabled: content.items.map(\.enabled))
        VStack(alignment: .leading, spacing: 0) {
            if let t = content.title {
                Text(t).font(.ds(.chrome, .semibold)).foregroundStyle(Theme.muted).lineLimit(1)
                    .padding(.horizontal, DS.Spacing.m).frame(height: Self.titleHeight, alignment: .leading)
            }
            if kind.filterable {
                HStack(spacing: DS.Spacing.s) {
                    Image(systemName: "magnifyingglass").font(.ds(.meta)).foregroundStyle(Theme.dim)
                    Text(list.query.isEmpty ? "Type to filter" : list.query)
                        .font(list.query.isEmpty ? .ds(.chrome) : DS.monoFont(.chrome))
                        .foregroundStyle(list.query.isEmpty ? Theme.dim : Theme.fg).lineLimit(1)
                    Spacer()
                }
                .padding(.horizontal, DS.Spacing.m).frame(height: Self.filterHeight - DS.Spacing.xs)
                .background(DS.Radius.shape(DS.Radius.control).fill(Theme.chipBG.opacity(0.6)))
                .padding(.horizontal, DS.Spacing.xs).padding(.bottom, DS.Spacing.xs)
                .accessibilityIdentifier("popover.filter")
            }
            if let f = content.field, case .rename(let id) = kind {
                field(f, text: Binding(get: { model.renameText[id] ?? "" }, set: { model.renameText[id] = $0 }), id: "rename.field") {
                    if let a = model.agent(id), let n = model.renameText[id], !n.isEmpty { model.rename(a, to: n) }
                }
            }
            if let f = content.field, case .deskName(let id) = kind {
                field(f, text: $model.deskNameText, id: "desk.name") {
                    let n = model.deskNameText
                    if !n.isEmpty { model.closePopover(); model.deskNameSubmit?(id, n) }
                }
            }
            if content.items.isEmpty && content.field == nil {
                Text(content.empty).font(.ds(.chrome)).foregroundStyle(Theme.dim).padding(.horizontal, DS.Spacing.m).frame(height: OverlayLook.rowHeight)
            }
            ScrollViewReader { proxy in
                ScrollView(showsIndicators: false) {
                    VStack(spacing: 0) {
                        ForEach(Array(content.items.enumerated()), id: \.element.id) { i, item in
                            if i > 0, item.section != content.items[i - 1].section {
                                Rectangle().fill(OverlayLook.line).frame(height: 1).padding(.vertical, DS.Spacing.xxs)
                            }
                            OverlayRow(title: item.title, detail: item.detail, mark: item.mark, dot: item.dot, checked: item.checked,
                                       selected: i == sel, enabled: item.enabled, stateMark: item.stateMark)
                                .id(i)
                                .onTapGesture { if item.enabled { var l = list; l.index = i; model.popoverList = l; _ = model.popoverKey(.activate) } }
                                .accessibilityIdentifier("popover.row.\(item.id)")
                        }
                    }
                }
                .frame(maxHeight: CGFloat(min(content.items.count, Self.maxRows)) * OverlayLook.rowHeight + DS.Spacing.m)
                .onChange(of: sel) { if let sel { proxy.scrollTo(sel) } }
            }
            if let n = content.note {
                Rectangle().fill(OverlayLook.line).frame(height: 1).padding(.top, DS.Spacing.xxs)
                Text(n).font(.ds(.chrome)).foregroundStyle(Theme.muted).lineLimit(2)
                    .padding(.horizontal, DS.Spacing.m).frame(height: Self.noteHeight - DS.Spacing.xs, alignment: .leading)
            }
            Hints(items: content.hints)
        }
        .padding(.horizontal, DS.Spacing.s)
        .padding(.top, DS.Spacing.s)
        .frame(width: width, alignment: .topLeading)
    }

    private func field(_ placeholder: String, text: Binding<String>, id: String, submit: @escaping () -> Void) -> some View {
        TextField(placeholder, text: text)
            .textFieldStyle(.plain)
            .font(.ds(.body))
            .padding(.horizontal, DS.Spacing.m).frame(height: Self.fieldHeight - 2 * DS.Spacing.m + DS.Spacing.xxs)
            .background(DS.Radius.shape(DS.Radius.control).fill(Theme.color(.tile)))
            .overlay(DS.Radius.shape(DS.Radius.control).stroke(Theme.accent.opacity(0.6)))
            .focused($fieldFocused)
            .onSubmit(submit)
            .onExitCommand { model.closePopover() }
            .padding(.horizontal, DS.Spacing.xs).padding(.vertical, DS.Spacing.m - DS.Spacing.xxs / 2)
            .onAppear { DispatchQueue.main.async { fieldFocused = true } }
            .accessibilityIdentifier(id)
    }
}

/// One row: mark or dot, title, detail on the right, a check.
struct OverlayRow: View {
    var title: String
    var detail: String
    var mark: String?
    var dot: String?
    var checked: Bool
    var selected: Bool
    var enabled: Bool
    var tint: String? = nil
    var stateMark: StateMarkKind? = nil

    var body: some View {
        HStack(spacing: DS.Spacing.m) {
            if let stateMark { StateMark(stateMark) } else if let dot { StateMark(color: Theme.color(dot)) }
            if let mark {
                Text(mark).font(DS.monoFont(.meta, .semibold))
                    .foregroundStyle(tint.map(Theme.color) ?? Theme.fg2)
                    .frame(minWidth: OverlayLook.markSide, minHeight: OverlayLook.markSide)
                    .background(DS.Radius.shape(DS.Radius.kbd).fill(Theme.chipBG))
            }
            Text(title).font(.ds(.body)).foregroundStyle(Theme.fg).lineLimit(1).truncationMode(.middle)
            Spacer(minLength: DS.Spacing.m)
            Text(detail).font(.ds(.meta)).foregroundStyle(Theme.muted).lineLimit(1).truncationMode(.middle)
            if checked { Image(systemName: "checkmark").font(.ds(.meta, .semibold)).foregroundStyle(Theme.accent) }
        }
        .padding(.horizontal, DS.Spacing.m)
        .frame(height: OverlayLook.rowHeight)
        .background(DS.Radius.shape(DS.Radius.control).fill(selected ? OverlayLook.selection : Color.clear))
        .opacity(enabled ? 1 : OverlayLook.disabledOpacity)
        .contentShape(Rectangle())
    }
}

/// The footer every overlay has: its keys, each a Kbd.
struct Hints: View {
    var items: [(String, String)]
    var leading: String? = nil

    static let height = DS.chromeMaxHeight

    var body: some View {
        HStack(spacing: DS.Spacing.m) {
            if let leading { Text(leading).font(.ds(.meta, .medium)).foregroundStyle(Theme.dim) }
            Spacer(minLength: 0)
            ForEach(Array(items.enumerated()), id: \.offset) { _, h in
                HStack(spacing: DS.Spacing.xs) {
                    Kbd(h.0)
                    Text(h.1).font(.ds(.chrome)).foregroundStyle(Theme.dim)
                }
                .fixedSize()
                .accessibilityElement(children: .combine)
            }
        }
        .padding(.horizontal, DS.Spacing.m)
        .frame(height: Self.height)
        .overlay(alignment: .top) { Rectangle().fill(OverlayLook.line).frame(height: 1) }
    }
}

/// A rounded panel with an arrow on its top (below the anchor) or bottom.
struct Bubble: Shape {
    var arrowX: CGFloat
    var below: Bool
    var radius: CGFloat = OverlayLook.radius
    var arrow: CGFloat = OverlayLook.arrow

    func path(in rect: CGRect) -> Path {
        let body = below ? CGRect(x: rect.minX, y: rect.minY + arrow, width: rect.width, height: rect.height - arrow)
                         : CGRect(x: rect.minX, y: rect.minY, width: rect.width, height: rect.height - arrow)
        var p = Path(roundedRect: body, cornerRadius: radius, style: .continuous)
        let x = max(body.minX + radius + arrow, min(body.maxX - radius - arrow, rect.minX + arrowX))
        var tri = Path()
        if below {
            tri.move(to: CGPoint(x: x - arrow - 1, y: body.minY + 0.5))
            tri.addLine(to: CGPoint(x: x, y: rect.minY))
            tri.addLine(to: CGPoint(x: x + arrow + 1, y: body.minY + 0.5))
        } else {
            tri.move(to: CGPoint(x: x - arrow - 1, y: body.maxY - 0.5))
            tri.addLine(to: CGPoint(x: x, y: rect.maxY))
            tri.addLine(to: CGPoint(x: x + arrow + 1, y: body.maxY - 0.5))
        }
        p.addPath(tri)
        return p.normalized()
    }
}

/// ⌘K's light scrim: dims the wall, but cards that need the user show
/// through it (their rings stay visible).
struct Scrim: View {
    var holes: [CGRect]
    @Environment(\.accessibilityReduceTransparency) private var reduceTransparency
    var body: some View {
        GeometryReader { g in
            Path { p in
                p.addRect(CGRect(origin: .zero, size: g.size))
                for h in holes {
                    p.addRoundedRect(in: h.insetBy(dx: -DS.Spacing.m, dy: -DS.Spacing.m),
                                     cornerSize: CGSize(width: OverlayLook.scrimHoleRadius, height: OverlayLook.scrimHoleRadius), style: .continuous)
                }
            }
            .fill(Color(nsColor: SearchLook.scrimColor(reduceTransparency: reduceTransparency)), style: FillStyle(eoFill: true)) // ⌘K's sheet scrim
        }
        .contentShape(Rectangle())
        .accessibilityIdentifier("scrim")
    }
}
