import AppKit
import HesperCore
import SwiftUI

/// The ↗ badge on a tile whose agent has its own window: a small pill
/// hanging on the card's top-right corner (outside the header, so it never
/// covers the header's chips). A click brings that window forward (like
/// ⇧-click on the tile).
@MainActor
final class AgentWindowMarker: NSView {
    private let label = NSTextField(labelWithString: "↗")
    var onClick: (() -> Void)?

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
        toolTip = "Show its window"
        setAccessibilityElement(true)
        setAccessibilityRole(.button)
        setAccessibilityLabel("Show its window")
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

    override func mouseDown(with event: NSEvent) {}
    override func mouseUp(with event: NSEvent) {
        if bounds.contains(convert(event.locationInWindow, from: nil)) { onClick?() }
    }
    override func accessibilityPerformPress() -> Bool { onClick?(); return true }
}

// MARK: Title stop (agent windows)

/// The agent window's full stop: a square in the title bar mirroring the
/// agent's state (StateMarkView: pulsing while an approval or a question
/// waits). A trailing titlebar accessory, so it stays put across tabs and
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
/// focus-role terminal (agent window, or a wall's focus view) or the
/// active tile in the app's key window, one per agent (SizeOwner); every
/// other terminal of the agent is a view at its own grid. Another Hesper window becoming key (or the owner
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
    private var takeBackMonitor: Any?
    /// Agents another device sized when last seen (sizeOwnerChanged, and
    /// wouldOwn for new surfaces): empty, a key skips the terminal walk.
    /// A closed agent's id may linger; it only costs that walk.
    private var remoteOwned: Set<String> = []
    private var applyQueued = false
    /// This Mac's machine id as last re-read (helloReceived).
    private var localMachine: String?
    /// Terminals that began a take-back: the key monitor must reach them
    /// while they await their owner surface, even once no agent is sized
    /// elsewhere any more (the daemon settled it).
    private let awaiting = NSHashTable<AgentTerminal>.weakObjects()

    init() {
        // Fit to iPhone: a keystroke in a pane whose agent's size another
        // device owns takes the size back at once (takeBack). A click only
        // focuses.
        takeBackMonitor = NSEvent.addLocalMonitorForEvents(matching: [.keyDown]) { [weak self] e in
            let consumed = MainActor.assumeIsolated { self?.takeBack(e) ?? false }
            return consumed ? nil : e
        }
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

    isolated deinit {
        if let takeBackMonitor { NSEvent.removeMonitor(takeBackMonitor) }
    }

    func schedule() {
        generation += 1
        let gen = generation
        DispatchQueue.main.asyncAfter(deadline: .now() + Self.debounce) { [weak self] in
            guard let self, gen == self.generation else { return }
            self.apply()
        }
    }

    /// The owner window: the last of ours that became key; before any did
    /// (launch), the key or main window if it is ours.
    var effectiveOwnerWindow: NSWindow? {
        if let ownerWindow { return ownerWindow }
        if let k = NSApp.keyWindow, isOurs(k) { return k }
        if let m = NSApp.mainWindow, isOurs(m) { return m }
        return nil
    }

    /// Who owns now (SizeOwner.owners): one read-write terminal per agent,
    /// in the owner window. `extra` is a terminal not listed yet (a new one).
    func owners(including extra: AgentTerminal? = nil) -> [(AgentTerminal, Bool)] {
        var ts = terminals()
        if let extra, !ts.contains(where: { $0 === extra }) { ts.append(extra) }
        let id: (NSWindow?) -> Int? = { $0.map { ObjectIdentifier($0).hashValue } }
        let cs = ts.map { SizeOwner.Candidate(agentID: $0.agent.id, window: id($0.host.window), focusRole: $0.role == .focus, readWrite: $0.readWrite,
                                              remoteOwned: $0.remoteOwned, takingBack: $0.takingSizeBack) }
        return Array(zip(ts, SizeOwner.owners(cs, ownerWindow: id(effectiveOwnerWindow))))
    }

    /// Whether `t` owns its agent's size now (AgentTerminal asks when it
    /// makes a surface). Others of the same agent follow in the next apply.
    func wouldOwn(_ t: AgentTerminal) -> Bool {
        noteRemoteOwned(t)
        let all = owners(including: t)
        let owns = all.first { $0.0 === t }?.1 ?? false
        if owns, all.contains(where: { $0.0 !== t && $0.0.agent.id == t.agent.id && $0.0.readWrite && $0.0.ownsSize }) { schedule() }
        return owns
    }

    /// An agent's `sizeOwner` changed: another device taking the size makes
    /// its panes views on the next turn of the main loop; a cleared owner
    /// goes through the debounce.
    func sizeOwnerChanged(_ t: AgentTerminal) {
        noteRemoteOwned(t)
        if t.remoteOwned { applySoon() } else { schedule() }
    }

    /// apply() once the current pass is done (AgentTerminal.update() calls
    /// in the middle of the agent-list fan-out, so swapping surfaces there
    /// would be re-entrant), but without schedule()'s debounce: the PTY is
    /// already at the other device's grid, and an owner pane garbles until
    /// it is a view. Several calls in one pass apply once.
    private func applySoon() {
        guard !applyQueued else { return }
        applyQueued = true
        DispatchQueue.main.async { [weak self] in
            guard let self, self.applyQueued else { return }
            self.applyQueued = false
            self.apply()
        }
    }

    /// hesperd's hello named this Mac (`id`), on every connect. An agent's
    /// `sizeOwnerHost` may read differently with no agent change to say so
    /// (AgentTerminal.update() compares old and new under the same id), so
    /// every terminal is re-read once the event has been applied; ownership
    /// is applied again only if the id really changed
    /// (SizeOwner.localMachineChanged) or a terminal's reading did.
    func helloReceived(_ id: String?) {
        let previous = localMachine
        let idChanged = SizeOwner.localMachineChanged(old: previous, new: id)
        if idChanged { localMachine = id }
        DispatchQueue.main.async { [weak self] in
            guard let self else { return }
            let before = self.remoteOwned
            for t in self.terminals() {
                if idChanged { t.localMachineChanged(from: previous) }
                self.noteRemoteOwned(t)
            }
            if idChanged || self.remoteOwned != before { self.apply() }
        }
    }

    private func noteRemoteOwned(_ t: AgentTerminal) {
        if t.remoteOwned { remoteOwned.insert(t.agent.id) } else { remoteOwned.remove(t.agent.id) }
    }

    /// A key in one of our windows: the pane it goes to, if its agent's
    /// size is another device's, takes the size back (owner at once,
    /// latest owner wins; SizeOwner.keyTakeBack). True: the key is held for
    /// that pane's owner surface (replayed in order once it takes input).
    private func takeBack(_ e: NSEvent) -> Bool {
        if remoteOwned.isEmpty {
            for t in awaiting.allObjects where !t.takingSizeBack { awaiting.remove(t) }
            guard awaiting.count > 0 else { return false }
        }
        guard let w = e.window, isOurs(w) else { return false }
        // An owner change still waiting for applySoon(): apply it now, so
        // the key is judged on panes that already follow it. Safe here: the
        // key monitor runs from the event loop, never inside the agent
        // fan-out that applySoon() keeps apply() out of.
        if applyQueued { applyQueued = false; apply() }
        let ts = terminals().filter { $0.host.window === w && !$0.host.isHiddenOrHasHiddenAncestor && ($0.remoteOwned || $0.takingSizeBack) }
        guard !ts.isEmpty else { return false }
        let responder = w.firstResponder
        let targeted = ts.map { t in
            // A pane without a surface is never the responder (nil === nil).
            if let v = t.surface?.view, responder === v { return true }
            if let wall = responder as? WallView {
                return t.role == .tile && t.interactive && t.host.isDescendant(of: wall)
            }
            if let focus = responder as? FocusView {
                return t.role == .focus && t.host.isDescendant(of: focus)
            }
            return false
        }
        let cs = ts.map { t in
            SizeOwner.Candidate(agentID: t.agent.id, window: nil, focusRole: t.role == .focus,
                                readWrite: t.readWrite, remoteOwned: t.remoteOwned, takingBack: t.takingSizeBack)
        }
        let types = w.isKeyWindow && TerminalKeys.bytes(KeyChord(event: e), characters: e.characters) != nil
        let paste = w.isKeyWindow && CommandRegistry.resolve(KeyChord(event: e), in: .everywhere) == .paste
        switch SizeOwner.keyRoute(cs, targeted: targeted, ownerCanCome: ts.map(\.ownerCanCome), typesIntoAgent: types, isPaste: paste) {
        case .pass:
            return false
        case .hold(let i):
            // Awaiting its owner surface: queued behind the earlier keys,
            // whatever the daemon reports meanwhile (PaneInput).
            ts[i].holdKey(e)
            return true
        case .cancel(let i):
            // Its owner surface can no longer come: called off, and the key
            // goes on as without the feature.
            ts[i].endTakeBack()
            apply()
            return false
        case .takeBack(let i):
            let t = ts[i]
            guard t.beginTakeBack() else { return false }
            awaiting.add(t)
            apply()
            t.holdKey(e)
            DispatchQueue.main.asyncAfter(deadline: .now() + SizeOwner.takeBackTimeout) { [weak self, weak t] in
                guard let self, let t, t.expireTakeBack() else { return }
                self.apply()
            }
            return true
        }
    }

    func apply() {
        for (t, owns) in owners() where t.host.window != nil {
            if t.ownsSize != owns {
                t.ownsSize = owns
                if t.readWrite { switches += 1 }
            }
        }
    }
}
