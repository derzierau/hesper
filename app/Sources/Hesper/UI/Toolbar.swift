import AppKit
import HesperCore
import Observation
import SwiftUI

// The top bar: one flat row in the titlebar, on a glass strip the width of
// the window (RootView's ToolbarGlass; its own in full screen). No NSToolbar
// items: on macOS 26 the system puts every toolbar item into a glass
// capsule, so the bar is one custom view in the titlebar container, to the
// right of the traffic lights:
//
//   ● ● ●  hesper.  [All · 7 | Needs you | Working]   …   Search agents and history ⌘K  ⊞ ＋  ■ laptop ■ mini  ■ 2 need you
//
// The NSToolbar stays, empty, for the unified titlebar height and the
// full-screen auto-hide (reveal on hover).

/// Frames of the bar's controls (in their host's own space).
struct PillFramesKey: PreferenceKey {
    nonisolated(unsafe) static var defaultValue: [String: CGRect] = [:]
    static func reduce(value: inout [String: CGRect], nextValue: () -> [String: CGRect]) { value.merge(nextValue()) { $1 } }
}

// MARK: Data

/// What the bar shows: a snapshot of a wall's model (renderable without one).
struct TopBarData {
    var counts = StateCounts()
    var machines: [Machine] = []
    var scope: WallScope = .all
    var popover: PopoverKind? = nil
    var connected = true
    var connectionMessage: String? = nil
    var arrangement: WallArrangement = .shelf
    /// The wall's scope as the window layer names it ("Overflow", a
    /// project): the first segment's title; nil: "All".
    var scopeName: String? = nil
    /// Agents on this wall (window layer); nil: every agent.
    var scopeCount: Int? = nil
    /// The main wall with an Overflow wall: "7 of 10".
    var scopeOf: Int? = nil
    /// A narrow bar: the search field is an icon, machines are squares (names in their tooltip).
    var compact = false
    /// Agents in the background (the tray pill; hidden at 0).
    var background = BackgroundSummary()

    /// The wordmark's stop is Signal (approvals and questions).
    var wordmarkSignal: Bool { counts.approval + counts.question > 0 }
}

/// What the bar's controls do.
struct TopBarActions {
    var scope: @MainActor (WallScope) -> Void = { _ in }
    var scopeMenu: @MainActor () -> Void = {}
    var popover: @MainActor (PopoverKind) -> Void = { _ in }
    var search: @MainActor () -> Void = {}
    var newAgent: @MainActor () -> Void = {}
}

/// The window layer's name for a wall's scope (the first segment); the
/// menu behind it (overflow, filters, projects) opens from that segment.
@MainActor @Observable
final class ScopeLabel {
    var name: String?
    var count: Int?
    var of: Int?
    @ObservationIgnored var onMenu: (@MainActor () -> Void)?
}

/// Where the bar's views get their data: a live wall, or a fixed snapshot
/// (offscreen rendering).
enum TopBarSource {
    case model(AppModel, ScopeLabel)
    case fixed(TopBarData)

    @MainActor func data(compact: Bool) -> TopBarData {
        switch self {
        case .fixed(var d):
            d.compact = compact
            return d
        case .model(let m, let s):
            return TopBarData(counts: m.counts, machines: m.machines, scope: m.scope, popover: m.popover, connected: m.isConnected,
                              connectionMessage: m.connectionMessage, arrangement: m.arrangement,
                              scopeName: s.name, scopeCount: s.count, scopeOf: s.of, compact: compact, background: m.backgroundSummary)
        }
    }

    @MainActor var actions: TopBarActions {
        guard case .model(let m, let s) = self else { return TopBarActions() }
        return TopBarActions(
            scope: { [weak m] sc in m?.sidebarAction?(.scope(sc)) },
            scopeMenu: { [weak m, weak s] in
                if let f = s?.onMenu { f() } else { m?.openPopover(.scope) }
            },
            popover: { [weak m] k in m?.openPopover(k) },
            search: { [weak m] in m?.perform(.palette) },
            newAgent: { [weak m] in m?.perform(.newAgent) })
    }
}

/// The bar's measures (named; everything else comes from DS).
enum TopBarLook {
    /// The wordmark in the bar (Geist 600, tracking −5.5%).
    static let wordmarkSize: CGFloat = 19
    /// The segmented track's inset around its segments.
    static let trackInset: CGFloat = 3
    /// The search field's width (it never grows past it).
    static let searchWidth: CGFloat = 220
    /// The All segment's menu chevron.
    static let chevronSize: CGFloat = 8
    /// Quiet field fill: the text color at 5% (white on Dusk, ink on Daylight).
    static var fieldFill: Color { Theme.fg.opacity(0.05) }
}

// MARK: Wordmark

/// "hesper." alone, no capsule; the stop is Signal when something needs you.
struct TopBarWordmark: View {
    var source: TopBarSource

    var body: some View {
        Wordmark(needsYou: source.data(compact: false).wordmarkSignal, size: TopBarLook.wordmarkSize)
            .accessibilityIdentifier("topbar.wordmark")
    }
}

// MARK: Segments

/// All · Needs you · Working on one subtle track. The lit segment is filled
/// (working at 18% on Dusk, a raised white chip on Daylight). The All
/// segment carries the wall's scope (its name when it isn't All) and, when
/// lit, the scope menu: overflow, filters, projects (⌃⌘S).
struct TopBarLeading: View {
    var source: TopBarSource
    var compact: Bool
    var onFrames: ([String: CGRect]) -> Void = { _ in }

    var body: some View {
        // The wordmark and the segments in one row: SwiftUI lays them out,
        // so they can never overlap (two hosts placed by measurement did).
        HStack(spacing: DS.Spacing.xl) {
            TopBarWordmark(source: source)
            ScopeSegmentsView(data: source.data(compact: compact), actions: source.actions)
        }
            .coordinateSpace(name: "bar")
            .onPreferenceChange(PillFramesKey.self) { f in MainActor.assumeIsolated { onFrames(f) } }
    }
}

struct ScopeSegmentsView: View {
    var data: TopBarData
    var actions: TopBarActions
    @Environment(\.colorScheme) private var scheme

    var body: some View {
        let lit = ScopeSegment(scope: data.scope) ?? .all
        HStack(spacing: DS.Spacing.xxs) {
            ForEach(ScopeSegment.allCases, id: \.self) { s in
                segment(s, lit: lit == s)
            }
        }
        .padding(TopBarLook.trackInset)
        .background(DS.Radius.shape(DS.Radius.control).fill(TopBarLook.fieldFill))
        .fixedSize()
        .accessibilityElement(children: .contain)
        .accessibilityLabel("Scope")
    }

    private func title(_ s: ScopeSegment) -> String { s == .all ? (data.scopeName ?? s.title) : s.title }

    private func count(_ s: ScopeSegment) -> Int {
        switch s {
        case .all: return data.scopeCount ?? data.counts.total
        case .needsYou: return data.counts.needingYou
        case .working: return data.counts.working
        }
    }

    private func countText(_ s: ScopeSegment, _ n: Int) -> String {
        if s == .all, let of = data.scopeOf, of != n { return "\(n) of \(of)" }
        return "\(n)"
    }

    private func segment(_ s: ScopeSegment, lit: Bool) -> some View {
        let n = count(s)
        let menu = s == .all && lit
        let shape = DS.Radius.shape(DS.Radius.control)
        return Button {
            if menu { actions.scopeMenu() } else { actions.scope(ScopeSegment.clicked(s, current: data.scope)) }
        } label: {
            HStack(spacing: DS.Spacing.xs) {
                Text(title(s)).font(.ds(.chrome, .medium))
                if n > 0 {
                    Text("·").font(.ds(.chrome, .medium))
                    Text(countText(s, n)).font(DS.monoFont(.chrome, .medium))
                }
                if menu {
                    Image(systemName: "chevron.down")
                        .font(.system(size: TopBarLook.chevronSize, weight: .semibold))
                        .foregroundStyle(Theme.dim)
                }
            }
            .foregroundStyle(lit ? Theme.fg : (scheme == .dark ? Theme.fg2 : Theme.dim))
            .lineLimit(1)
            .padding(.horizontal, DS.Spacing.m)
            .frame(height: Pill.height)
            .background {
                if lit {
                    if scheme == .dark {
                        shape.fill(Theme.color(.working).opacity(0.18))
                    } else {
                        shape.fill(Theme.color(.tile)).shadow(color: Theme.fg.opacity(0.12), radius: 1, y: 1)
                    }
                }
            }
            .contentShape(shape)
        }
        .buttonStyle(.plain)
        .background(s == .all ? AnyView(Color.clear.barAnchor("scope")) : AnyView(EmptyView()))
        .help(help(s, lit: lit, menu: menu))
        .accessibilityLabel("\(title(s)), \(n)")
        .accessibilityAddTraits(lit ? .isSelected : [])
        .accessibilityIdentifier("topbar.scope.\(s.rawValue)")
    }

    private func help(_ s: ScopeSegment, lit: Bool, menu: Bool) -> String {
        if menu { return "Which agents this wall shows: overflow, filters, projects (⌃⌘S)" }
        if lit { return "Show all agents" }
        return "Show \(s == .all ? "all agents" : s.title.lowercased()) on this wall"
    }
}

// MARK: Trailing: search, layout, new, machines, what needs you

struct TopBarTrailing: View {
    var source: TopBarSource
    var compact: Bool
    var onFrames: ([String: CGRect]) -> Void = { _ in }

    var body: some View {
        TopBarTrailingContent(data: source.data(compact: compact), actions: source.actions)
            .coordinateSpace(name: "bar")
            .onPreferenceChange(PillFramesKey.self) { f in MainActor.assumeIsolated { onFrames(f) } }
    }
}

struct TopBarTrailingContent: View {
    var data: TopBarData
    var actions: TopBarActions
    @State private var machinesHover = false

    var body: some View {
        HStack(spacing: DS.Spacing.l) {
            if !data.connected {
                Label(data.connectionMessage ?? "Disconnected", systemImage: "bolt.horizontal.circle")
                    .foregroundStyle(Theme.color(.question))
                    .lineLimit(1)
                    .accessibilityIdentifier("topbar.disconnected")
            }
            search
            HStack(spacing: DS.Spacing.xxs) {
                let layout = LayoutSwitcher(data.arrangement)
                IconButton(layout.symbol, help: layout.help, shortcut: layout.shortcut) { actions.popover(.layout) }
                    .id(layout.current) // a new button (symbol, tooltip) whenever the wall's arrangement changes
                    .barAnchor("layout")
                    .accessibilityIdentifier("toolbar.layout")
                IconButton("plus", help: "New agent", shortcut: "⌘N") { actions.newAgent() }
                    .accessibilityIdentifier("toolbar.new")
            }
            machines
            BackgroundPill(data: data, actions: actions)
            AttentionPill(data: data, actions: actions)
        }
        .font(.ds(.chrome))
        .lineLimit(1)
        .fixedSize()
    }

    /// The search field affordance: opens the palette (⌘K); an icon when narrow.
    @ViewBuilder private var search: some View {
        if data.compact {
            IconButton("magnifyingglass", help: "Search agents and history", shortcut: "⌘K") { actions.search() }
                .accessibilityIdentifier("toolbar.search")
        } else {
            let shape = DS.Radius.shape(DS.Radius.control)
            Button { actions.search() } label: {
                HStack(spacing: DS.Spacing.s) {
                    Text("Search agents and history").foregroundStyle(Theme.dim).lineLimit(1)
                    Spacer(minLength: DS.Spacing.s)
                    Kbd("⌘K")
                }
                .font(.ds(.chrome))
                .padding(.horizontal, DS.Spacing.m)
                .frame(width: TopBarLook.searchWidth, height: IconButton.side)
                .background(shape.fill(TopBarLook.fieldFill))
                .contentShape(shape)
            }
            .buttonStyle(.plain)
            .help("Agents, projects, machines, actions, history (⌘K)")
            .accessibilityLabel("Search")
            .accessibilityIdentifier("toolbar.search")
        }
    }

    /// Each machine a 6 pt square and its short name ("laptop", "mini"):
    /// online a `done` square, offline an outlined dim one and dim text.
    private var machines: some View {
        Button { actions.popover(.machines) } label: {
            HStack(spacing: DS.Spacing.m) {
                ForEach(data.machines.prefix(4), id: \.short) { m in
                    HStack(spacing: DS.Spacing.xs) {
                        if m.online {
                            StateMark(color: Theme.color(.done), side: DS.Spacing.s)
                        } else {
                            StateMark(.idle, side: DS.Spacing.s)
                        }
                        if !data.compact {
                            Text(m.displayName).font(.ds(.chrome, .medium)).foregroundStyle(m.online ? Theme.fg2 : Theme.dim)
                        }
                    }
                    .accessibilityElement(children: .ignore)
                    .accessibilityLabel("\(m.displayName), \(m.online ? "online" : "offline")")
                }
            }
            .padding(.horizontal, DS.Spacing.s)
            .frame(height: Pill.height)
            .background(DS.Radius.shape(DS.Radius.control).fill(data.popover == .machines || machinesHover ? Theme.chipBG : Color.clear))
            .contentShape(DS.Radius.shape(DS.Radius.control))
            .onHover { machinesHover = $0 }
        }
        .buttonStyle(.plain)
        .barAnchor("machines")
        .help(machinesHelp)
        .accessibilityLabel("Machines")
        .accessibilityIdentifier("topbar.machines")
    }

    private var machinesHelp: String {
        let lines = data.machines.map { m -> String in
            var s = "\(m.displayName): \(m.online ? "online" : "offline")"
            if m.online, let rtt = m.rttMs, m.route != "local", rtt > 0 { s += " · \(Int(rtt)) ms" }
            return s
        }
        return (["Machines: online, route, round trip"] + lines).joined(separator: "\n")
    }
}

/// "Background · 2": agents running off every wall (⌥⌘W). A quiet status
/// Pill with a working StateMark, hidden at 0; click → the tray popover
/// (bring back ⏎, close ⌘W). One that needs you shows in ⌘J and the
/// attention pill like any other.
struct BackgroundPill: View {
    var data: TopBarData
    var actions: TopBarActions

    var body: some View {
        let b = data.background
        if b.visible {
            let open = data.popover == .background
            Button { actions.popover(.background) } label: {
                Pill(data.compact ? "\(b.count)" : b.title, variant: .status, mark: .working)
                    .overlay(DS.Radius.shape(DS.Radius.control).strokeBorder(open ? Theme.color(.working).opacity(AttentionBar.primaryStroke) : .clear, lineWidth: 1))
                    .contentShape(DS.Radius.shape(DS.Radius.control))
            }
            .buttonStyle(.plain)
            .fixedSize()
            .barAnchor("background")
            .help("Running in the background (⌥⌘W sends the selected agent here)")
            .accessibilityLabel("\(b.count) in the background")
            .accessibilityIdentifier("topbar.background")
        }
    }
}

/// The far right: what needs you. The only red thing in the chrome
/// (approvals; questions and errors take their own tone), hidden when
/// nothing does. Click → the attention inbox (⌘J).
struct AttentionPill: View {
    var data: TopBarData
    var actions: TopBarActions

    var body: some View {
        let c = data.counts
        if c.needingYou > 0 {
            let mark: StateMarkKind = c.approval > 0 ? .needsYou : c.question > 0 ? .question : .error
            let open = data.popover == .attention
            Button { actions.popover(.attention) } label: {
                HStack(spacing: DS.Spacing.s) {
                    StateMark(mark)
                    Text("\(c.needingYou) \(c.needingYou == 1 ? "needs you" : "need you")").font(.ds(.chrome, .medium)).foregroundStyle(Theme.fg)
                }
                .padding(.horizontal, DS.Spacing.m).frame(height: Pill.height)
                .background(DS.Radius.shape(DS.Radius.control).fill(Theme.color(Theme.token(mark.tone)).opacity(open ? 0.30 : 0.16)))
                .contentShape(DS.Radius.shape(DS.Radius.control))
            }
            .buttonStyle(.plain)
            .fixedSize()
            .barAnchor("attention")
            .help("What needs you (⌘J)")
            .accessibilityLabel("\(c.needingYou) need\(c.needingYou == 1 ? "s" : "") you")
            .accessibilityIdentifier("topbar.approvals")
        }
    }
}

extension View {
    fileprivate func barAnchor(_ name: String) -> some View {
        background(GeometryReader { g in Color.clear.preference(key: PillFramesKey.self, value: [name: g.frame(in: .named("bar"))]) })
    }
}

// MARK: AppKit

/// A hosting view in the bar: tells the bar when its size changes;
/// `passThrough` (the wordmark) lets clicks fall to the titlebar (drag,
/// double-click to zoom).
final class BarHostingView<Content: View>: NSHostingView<Content> {
    var passThrough = false

    override func hitTest(_ point: NSPoint) -> NSView? { passThrough ? nil : super.hitTest(point) }
    override var mouseDownCanMoveWindow: Bool { passThrough }
    /// The titlebar is vibrant: without this it washes our colors out
    /// (the wordmark's red stop turned pale lavender).
    override var allowsVibrancy: Bool { false }
    override func invalidateIntrinsicContentSize() {
        super.invalidateIntrinsicContentSize()
        superview?.needsLayout = true
    }
}

/// The glass behind the bar: the wall scrolls under it. The same
/// material as Panel (DS.Elevation.floating); solid `surface` under Reduce
/// Transparency. A hairline at its bottom edge, no blur over terminals
/// anywhere else.
@MainActor
final class ToolbarGlass: NSView {
    private let effect = NSVisualEffectView()
    private let solid = NSView()
    private let hairline = NSView()
    /// Solid `surface` regardless of Reduce Transparency (offscreen renders:
    /// a visual effect view draws nothing there).
    var forceSolid = false { didSet { applyTheme() } }

    override var isFlipped: Bool { true }

    override init(frame: NSRect) {
        super.init(frame: frame)
        wantsLayer = true
        effect.material = .popover
        effect.blendingMode = .withinWindow
        effect.state = .active
        solid.wantsLayer = true
        hairline.wantsLayer = true
        addSubview(solid)
        addSubview(effect)
        addSubview(hairline)
        setAccessibilityElement(false)
        NSWorkspace.shared.notificationCenter.addObserver(self, selector: #selector(accessibilityChanged),
                                                          name: NSWorkspace.accessibilityDisplayOptionsDidChangeNotification, object: nil)
        applyTheme()
    }

    @available(*, unavailable)
    required init?(coder: NSCoder) { fatalError() }

    @objc private func accessibilityChanged() { applyTheme() }

    override func viewDidChangeEffectiveAppearance() {
        super.viewDidChangeEffectiveAppearance()
        applyTheme()
    }

    override func layout() {
        super.layout()
        solid.frame = bounds
        effect.frame = bounds
        let line: CGFloat = 1
        hairline.frame = NSRect(x: 0, y: bounds.height - line, width: bounds.width, height: line)
    }

    /// Clicks go to the bar (above) or the wall (where there is none).
    override func hitTest(_ point: NSPoint) -> NSView? { nil }

    private func applyTheme() {
        let solidOnly = DS.reduceTransparency || forceSolid
        effect.isHidden = solidOnly
        solid.isHidden = !solidOnly
        solid.layer?.backgroundColor = Theme.ns(.surface).cg(in: self)
        hairline.layer?.backgroundColor = Theme.ns(.line).cg(in: self)
    }
}

/// The bar as one view: the wordmark, the segments after the traffic
/// lights; search, layout, New Agent, machines and the attention pill at
/// the far end; everything centered on the traffic lights' row. Empty
/// parts (and the wordmark) let clicks through to the titlebar, so the
/// window drags and double-click zooms as usual. Right-to-left: mirrored.
@MainActor
final class TopBar: NSView {
    let source: TopBarSource
    private let wordmark: BarHostingView<TopBarWordmark>
    private let leading: BarHostingView<TopBarLeading>
    private let trailing: BarHostingView<TopBarTrailing>
    /// Our own glass in full screen (the system's toolbar window has the
    /// bar there, not the window's RootView glass).
    let glass = ToolbarGlass()
    /// The wall window (in full screen the bar lives in the system's
    /// toolbar window).
    weak var hostWindow: NSWindow?
    /// Room before the wordmark when the traffic lights can't be measured
    /// (offscreen rendering, no window).
    /// Room for the traffic lights when they can't be measured (another
    /// wall's window, mid-transition): never less than macOS's own.
    static let lightsRoom: CGFloat = 78
    var fallbackLead = TopBar.lightsRoom
    /// Show the glass whatever the window (offscreen rendering).
    var alwaysShowsGlass = false
    private(set) var compact = false
    /// The width the full (non-compact) bar needs, measured while it was full.
    private var fullNeed: CGFloat = 0
    private var leadingFrames: [String: CGRect] = [:]
    private var trailingFrames: [String: CGRect] = [:]

    override var isFlipped: Bool { true }
    override var mouseDownCanMoveWindow: Bool { true }

    init(source: TopBarSource) {
        self.source = source
        wordmark = BarHostingView(rootView: TopBarWordmark(source: source))
        leading = BarHostingView(rootView: TopBarLeading(source: source, compact: false))
        trailing = BarHostingView(rootView: TopBarTrailing(source: source, compact: false))
        super.init(frame: .zero)
        wordmark.passThrough = true
        wordmark.sizingOptions = [.intrinsicContentSize]
        leading.sizingOptions = [.intrinsicContentSize]
        trailing.sizingOptions = [.intrinsicContentSize]
        glass.isHidden = true
        addSubview(glass)
        addSubview(wordmark)
        addSubview(leading)
        addSubview(trailing)
        setRoots()
        setAccessibilityElement(true)
        setAccessibilityRole(.toolbar)
        setAccessibilityLabel("Hesper")
        setAccessibilityIdentifier("topbar")
    }

    @available(*, unavailable)
    required init?(coder: NSCoder) { fatalError() }

    private func setRoots() {
        leading.rootView = TopBarLeading(source: source, compact: compact) { [weak self] f in self?.leadingFrames = f }
        trailing.rootView = TopBarTrailing(source: source, compact: compact) { [weak self] f in self?.trailingFrames = f }
    }

    private var inFullScreen: Bool { hostWindow?.styleMask.contains(.fullScreen) ?? false }
    private var rightToLeft: Bool { (hostWindow ?? window)?.windowTitlebarLayoutDirection == .rightToLeft }

    /// The traffic lights in this view's space (nil without a window, or
    /// when they are hidden or elsewhere).
    private func trafficLights() -> NSRect? {
        guard let window else { return nil }
        let w = hostWindow ?? window
        var r: NSRect?
        for k in [NSWindow.ButtonType.closeButton, .miniaturizeButton, .zoomButton] {
            guard let b = w.standardWindowButton(k), !b.isHiddenOrHasHiddenAncestor, b.window === window else { continue }
            let f = convert(b.bounds, from: b)
            r = r.map { $0.union(f) } ?? f
        }
        return r
    }

    override func hitTest(_ point: NSPoint) -> NSView? {
        guard !isHidden else { return nil }
        if let lights = trafficLights(), lights.insetBy(dx: -DS.Spacing.xs, dy: -DS.Spacing.xs).contains(convert(point, from: superview)) { return nil }
        let hit = super.hitTest(point)
        return hit === self ? nil : hit
    }

    override func layout() {
        super.layout()
        glass.frame = bounds
        glass.isHidden = !(alwaysShowsGlass || inFullScreen)
        let margin = DS.Spacing.l
        let lights = trafficLights()
        let rtl = rightToLeft
        var lo = margin, hi = bounds.width - margin
        if let lights {
            if rtl { hi = lights.minX - margin } else { lo = lights.maxX + margin }
            // Never under the lights, whatever the measurement said.
            if rtl { hi = min(hi, bounds.width - Self.lightsRoom) } else { lo = max(lo, Self.lightsRoom) }
        } else if rtl {
            hi = bounds.width - fallbackLead
        } else {
            lo = fallbackLead
        }
        let midY = lights?.midY ?? bounds.midY
        let flexible = DS.Spacing.xl // the least room between the two ends
        let avail = hi - lo

        // The wordmark is part of `leading` (one SwiftUI row); its own host
        // stays hidden and takes no room.
        var wm = NSSize.zero, ld = leading.fittingSize, tr = trailing.fittingSize
        let gap: CGFloat = 0
        var need = wm.width + gap + ld.width + flexible + tr.width
        if !compact {
            fullNeed = need
            if need > avail {
                compact = true
                setRoots()
                ld = leading.fittingSize
                tr = trailing.fittingSize
                need = wm.width + gap + ld.width + flexible + tr.width
            }
        } else if avail >= fullNeed {
            compact = false
            setRoots()
            ld = leading.fittingSize
            tr = trailing.fittingSize
            need = wm.width + gap + ld.width + flexible + tr.width
        }
        // Still too narrow: the segments go, then the wordmark.
        let showSegments = need <= avail
        let showWordmark = showSegments || wm.width + flexible + tr.width <= avail
        leading.isHidden = !showSegments
        wordmark.isHidden = true
        if !showWordmark { wm = .zero }

        func y(_ s: NSSize) -> CGFloat { (midY - s.height / 2).rounded() }
        if rtl {
            wordmark.frame = NSRect(x: hi - wm.width, y: y(wm), width: wm.width, height: wm.height)
            leading.frame = NSRect(x: wordmark.frame.minX - gap - ld.width, y: y(ld), width: ld.width, height: ld.height)
            trailing.frame = NSRect(x: lo, y: y(tr), width: tr.width, height: tr.height)
        } else {
            wordmark.frame = NSRect(x: lo, y: y(wm), width: wm.width, height: wm.height)
            leading.frame = NSRect(x: wordmark.frame.maxX + gap, y: y(ld), width: ld.width, height: ld.height)
            trailing.frame = NSRect(x: hi - tr.width, y: y(tr), width: tr.width, height: tr.height)
        }
    }

    /// A popover's control: its host and its rect in the host's space.
    func control(for kind: PopoverKind) -> (NSView, NSRect)? {
        let host: NSView, frames: [String: CGRect], key: String
        switch kind {
        case .scope: host = leading; frames = leadingFrames; key = "scope"
        case .machines: host = trailing; frames = trailingFrames; key = "machines"
        case .layout: host = trailing; frames = trailingFrames; key = "layout"
        case .attention: host = trailing; frames = trailingFrames; key = "attention"
        case .background: host = trailing; frames = trailingFrames; key = "background"
        default: return nil
        }
        guard !host.isHiddenOrHasHiddenAncestor, let f = frames[key] else { return nil }
        // The frames are in the root's space: the hosting view's (flipped, top-left).
        let local = host.isFlipped ? f : NSRect(x: f.minX, y: host.bounds.height - f.maxY, width: f.width, height: f.height)
        return (host, local)
    }
}

/// A wall window's top bar and its (empty) toolbar: the toolbar gives the
/// unified titlebar its height and full screen its auto-hide; the bar is
/// one custom view in the titlebar (TopBar), so nothing is put into the
/// system's glass capsules. Full screen: the titlebar (and the bar with
/// it) hides and comes back when the pointer reaches the top edge
/// (FullScreenChrome).
@MainActor
final class MainToolbar: NSObject, NSToolbarDelegate {
    let model: AppModel
    let toolbar = NSToolbar(identifier: "HesperMain")
    /// The wall's scope name and menu (the window layer fills it).
    let scope = ScopeLabel()
    let bar: TopBar
    private weak var window: NSWindow?

    /// Called for every new toolbar (window layer: ScopePill).
    static var didCreate: ((MainToolbar) -> Void)?

    init(model: AppModel) {
        self.model = model
        bar = TopBar(source: .model(model, scope))
        super.init()
        toolbar.delegate = self
        toolbar.displayMode = .iconOnly
        toolbar.allowsUserCustomization = false
        Self.didCreate?(self)
    }

    /// Puts the toolbar on `w` and the bar into its titlebar.
    func install(in w: NSWindow) {
        window = w
        bar.hostWindow = w
        w.toolbar = toolbar
        w.toolbarStyle = .unifiedCompact
        attach()
        let nc = NotificationCenter.default
        for name in [NSWindow.didEnterFullScreenNotification, NSWindow.didExitFullScreenNotification,
                     NSWindow.willEnterFullScreenNotification, NSWindow.didResizeNotification, NSWindow.didBecomeKeyNotification] {
            nc.addObserver(self, selector: #selector(windowChanged), name: name, object: w)
        }
    }

    @objc private func windowChanged() {
        attach()
        bar.needsLayout = true
    }

    /// The bar in the titlebar container (the view that holds the traffic
    /// lights and the toolbar, and moves to the system's toolbar window in
    /// full screen); again if AppKit rebuilt the titlebar.
    private func attach() {
        // In place (in full screen it moved along with the container).
        if bar.superview != nil, bar.window != nil { return }
        guard let w = window, let close = w.standardWindowButton(.closeButton), let frame = w.contentView?.superview else { return }
        var container: NSView = close
        while let s = container.superview, s !== frame { container = s }
        guard container.superview === frame else { return }
        bar.removeFromSuperview()
        bar.frame = container.bounds
        bar.autoresizingMask = [.width, .height]
        container.addSubview(bar)
    }

    /// A popover's anchor in screen coordinates (in full screen the bar
    /// lives in its own window); nil when the control isn't shown.
    func anchor(for kind: PopoverKind) -> NSRect? {
        guard let (v, r) = bar.control(for: kind), let w = v.window else { return nil }
        return w.convertToScreen(v.convert(r, to: nil))
    }

    /// A control's frame in its window's coordinates (self-tests).
    func anchorInWindow(_ kind: PopoverKind) -> NSRect? {
        guard let (v, r) = bar.control(for: kind), v.window != nil else { return nil }
        return v.convert(r, to: nil)
    }

    func toolbarDefaultItemIdentifiers(_ toolbar: NSToolbar) -> [NSToolbarItem.Identifier] { [.flexibleSpace] }
    func toolbarAllowedItemIdentifiers(_ toolbar: NSToolbar) -> [NSToolbarItem.Identifier] { [.flexibleSpace] }
    func toolbar(_ toolbar: NSToolbar, itemForItemIdentifier id: NSToolbarItem.Identifier, willBeInsertedIntoToolbar flag: Bool) -> NSToolbarItem? { nil }
}

/// Full screen: the toolbar (and the menu bar) hide and come back when the
/// pointer reaches the top edge; the wall gets the whole screen. Popovers
/// anchored in the toolbar hang from the top edge while it is away
/// (RootView.anchor).
enum FullScreenChrome {
    static func options(_ proposed: NSApplication.PresentationOptions) -> NSApplication.PresentationOptions {
        proposed.union([.fullScreen, .autoHideMenuBar, .autoHideToolbar])
    }
}
