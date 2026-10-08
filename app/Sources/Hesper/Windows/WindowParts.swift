import AppKit
import HesperCore
import SwiftUI

/// The ↗ badge on a tile whose agent has its own window: a small pill
/// hanging on the card's top-right corner (outside the header, so it never
/// covers the header's chips).
@MainActor
final class AgentWindowMarker: NSView {
    private let label = NSTextField(labelWithString: "↗")

    override init(frame: NSRect) {
        super.init(frame: frame)
        wantsLayer = true
        DS.Radius.apply(DS.Radius.tile, to: layer)
        layer?.borderWidth = 1
        applyTheme()
        label.font = DS.nsFont(.chrome, .semibold)
        label.textColor = Theme.windowBG
        label.alignment = .center
        addSubview(label)
        isHidden = true
        toolTip = "Open in its own window"
        setAccessibilityIdentifier("tile.windowMarker")
    }

    @available(*, unavailable)
    required init?(coder: NSCoder) { fatalError() }

    override func viewDidChangeEffectiveAppearance() {
        super.viewDidChangeEffectiveAppearance()
        applyTheme()
    }

    private func applyTheme() {
        layer?.backgroundColor = Theme.ns(.working, alpha: 0.92).cg(in: self)
        layer?.borderColor = Theme.windowBG.cg(in: self)
    }

    /// The badge's side and how far it hangs past the card's corner.
    static let side = DS.Spacing.l + DS.Spacing.s
    static let overhang = DS.Spacing.s

    func place(in card: NSRect) {
        let s = Self.side
        frame = NSRect(x: card.maxX - s + Self.overhang, y: card.minY - Self.overhang, width: s, height: s)
        let h = label.intrinsicContentSize.height
        label.frame = NSRect(x: 0, y: ((s - h) / 2).rounded(), width: s, height: h)
    }

    override func hitTest(_ point: NSPoint) -> NSView? { nil }
}

// MARK: Title stop (agent windows)

/// The agent window's full stop: a square in the title bar mirroring the
/// agent's state (StateMarkView: Signal and pulsing only while it needs
/// you). A trailing titlebar accessory, so it stays put across tabs and
/// full screen.
@MainActor
final class TitleStopAccessory: NSTitlebarAccessoryViewController {
    let mark = StateMarkView(.idle)
    /// The accessory's width; its height follows the title bar.
    static let width = DS.Spacing.xxl + DS.Spacing.m

    var kind: StateMarkKind {
        get { mark.kind }
        set {
            mark.kind = newValue
            view.setAccessibilityLabel("State: \(newValue.label)")
        }
    }

    override func loadView() {
        let v = NSView(frame: NSRect(x: 0, y: 0, width: Self.width, height: DS.chromeMaxHeight))
        v.addSubview(mark)
        mark.translatesAutoresizingMaskIntoConstraints = false
        NSLayoutConstraint.activate([
            mark.centerYAnchor.constraint(equalTo: v.centerYAnchor),
            mark.leadingAnchor.constraint(equalTo: v.leadingAnchor, constant: DS.Spacing.s),
        ])
        v.setAccessibilityElement(true)
        v.setAccessibilityRole(.image)
        v.setAccessibilityIdentifier("agentWindow.stop")
        view = v
        layoutAttribute = .trailing
    }
}

// MARK: Scope (top bar)

/// A wall's scope in its top bar: the first segment's name and count
/// ("All · 12", "Overflow · 4", "All · 7 of 10"), and the scope menu
/// (overflow, filters, projects) behind it (click the lit segment, ⌃⌘S).
@MainActor
final class ScopePill {
    private static var byModel: [ObjectIdentifier: ScopePill] = [:]
    static var onClick: ((AppModel) -> Void)?

    private weak var toolbar: MainToolbar?
    weak var model: AppModel?

    static func install() {
        MainToolbar.didCreate = { toolbar in
            let pill = ScopePill(toolbar: toolbar)
            byModel[ObjectIdentifier(toolbar.model)] = pill
            toolbar.scope.onMenu = { [weak toolbar] in
                guard let m = toolbar?.model else { return }
                ScopePill.onClick?(m)
            }
        }
    }

    static func pill(for model: AppModel) -> ScopePill? { byModel[ObjectIdentifier(model)] }
    static func forget(_ model: AppModel) { byModel.removeValue(forKey: ObjectIdentifier(model)) }

    init(toolbar: MainToolbar) {
        self.toolbar = toolbar
        model = toolbar.model
    }

    /// nil name: "All". `of`: the main wall's share when an Overflow wall exists.
    func set(name: String?, count: Int?, of total: Int? = nil) {
        guard let s = toolbar?.scope else { return }
        if s.name != name { s.name = name }
        if s.count != count { s.count = count }
        if s.of != total { s.of = total }
    }

    /// The All segment (the scope popover's anchor) in its window's
    /// coordinates; nil when it isn't shown.
    var anchorInWindow: NSRect? { toolbar?.anchorInWindow(.scope) }
}

// MARK: Size ownership

/// Who sets an agent's PTY size when several windows show it: the
/// focus-role terminal (agent window, or a wall's focus view) in the
/// app's key window. Another Hesper window becoming key (or the owner
/// closing) releases it, and the daemon falls back to its fit; the app
/// merely going to the background keeps it (no resize on every app
/// switch). Applied 250 ms after key changes settle, so flipping between
/// windows never ping-pongs the PTY size.
@MainActor
final class SizeOwnership {
    static let debounce = 0.25
    /// Every focus-role terminal in every window.
    var terminals: () -> [AgentTerminal] = { [] }
    /// Whether a window is one of ours (walls, agent windows).
    var isOurs: (NSWindow) -> Bool = { _ in false }
    private(set) weak var ownerWindow: NSWindow?
    private var generation = 0
    /// Re-attaches done for ownership (tests, perf).
    private(set) var switches = 0

    init() {
        NotificationCenter.default.addObserver(forName: NSWindow.didBecomeKeyNotification, object: nil, queue: .main) { [weak self] n in
            let oid = (n.object as AnyObject?).map(ObjectIdentifier.init)
            MainActor.assumeIsolated {
                guard let self, let w = NSApp.windows.first(where: { ObjectIdentifier($0) == oid }), self.isOurs(w) else { return }
                if self.ownerWindow !== w { self.ownerWindow = w; self.schedule() }
            }
        }
        NotificationCenter.default.addObserver(forName: NSWindow.willCloseNotification, object: nil, queue: .main) { [weak self] n in
            let oid = (n.object as AnyObject?).map(ObjectIdentifier.init)
            MainActor.assumeIsolated {
                guard let self, let o = self.ownerWindow, ObjectIdentifier(o) == oid else { return }
                self.ownerWindow = nil
                self.schedule()
            }
        }
    }

    func schedule() {
        generation += 1
        let gen = generation
        DispatchQueue.main.asyncAfter(deadline: .now() + Self.debounce) { [weak self] in
            guard let self, gen == self.generation else { return }
            self.apply()
        }
    }

    func apply() {
        for t in terminals() {
            guard let w = t.host.window else { continue }
            let owns = w === ownerWindow
            if t.ownsSize != owns {
                t.ownsSize = owns
                if t.role == .focus || t.interactive { switches += 1 }
            }
        }
    }
}
