import AppKit
import HesperCore
import SwiftUI

/// The quick launch panel takes keys without activating Hesper: the app you
/// were in stays where it is.
final class QuickLaunchPanel: NSPanel {
    var onKey: ((AppCommand) -> Bool)?

    override var canBecomeKey: Bool { true }
    override var canBecomeMain: Bool { false }

    override func performKeyEquivalent(with event: NSEvent) -> Bool {
        let cmd = KeyRouter.route(KeyChord(event: event), mode: .compose, selectedState: nil)
        switch cmd {
        case .startDraft, .startDraftAndNew, .toggleWorktree, .closeAgent:
            if onKey?(cmd) == true { return true }
        default: break
        }
        return super.performKeyEquivalent(with: event)
    }

    override func cancelOperation(_ sender: Any?) { _ = onKey?(.leaveDraft) }
}

/// Quick launch (⌃⌥Space, the menu bar item): the same prompt-first
/// composer as a draft tile, as a floating glass sheet over any app: the
/// task first, the chips under it. ⌘↩ starts the agent (it joins the wall)
/// and a notification takes you to it; esc closes and keeps what you wrote.
@MainActor
final class QuickLaunchController {
    let model: AppModel
    let panel: QuickLaunchPanel
    let composer: ComposerModel
    private let editor = ComposerEditor()
    /// Glass behind the sheet (the app under it shows through); solid
    /// `surface` under Reduce Transparency.
    private let glass = NSVisualEffectView()
    private let card = FlippedView()
    private let chipMeasure = NSHostingController(rootView: AnyView(EmptyView()))
    private var chipsHeight = Pill.height
    private let header: NSHostingView<QuickHeader>
    private let list: NSHostingView<AnyView>
    private let chips: NSHostingView<AnyView>
    private let footer: NSHostingView<DraftFooter>
    static let width: CGFloat = 620
    static let headerHeight = DS.chromeMaxHeight + DS.Spacing.m
    static let editorHeight: CGFloat = 106
    static let completionRows = 6

    init(model: AppModel) {
        self.model = model
        composer = model.composer(for: model.quickDraft.id)
        panel = QuickLaunchPanel(contentRect: NSRect(x: 0, y: 0, width: Self.width, height: Self.editorHeight * 2),
                                 styleMask: [.borderless, .nonactivatingPanel], backing: .buffered, defer: false)
        header = NSHostingView(rootView: QuickHeader())
        list = NSHostingView(rootView: AnyView(EmptyView()))
        chips = NSHostingView(rootView: AnyView(EmptyView()))
        footer = NSHostingView(rootView: DraftFooter(composer: composer, quick: true, onStart: {}))
        panel.isFloatingPanel = true
        panel.level = .floating
        panel.collectionBehavior = [.canJoinAllSpaces, .fullScreenAuxiliary, .transient]
        panel.isOpaque = false
        panel.backgroundColor = .clear
        panel.hasShadow = true
        panel.hidesOnDeactivate = false
        panel.isMovableByWindowBackground = true
        panel.setAccessibilityIdentifier("quicklaunch")
        glass.material = .popover
        glass.blendingMode = .behindWindow
        glass.state = .active
        glass.wantsLayer = true
        DS.Radius.apply(DS.Radius.panel, to: glass.layer, masks: true)
        card.wantsLayer = true
        DS.Radius.apply(DS.Radius.panel, to: card.layer, masks: true)
        card.layer?.borderWidth = 1
        let card = self.card, glass = self.glass
        card.onAppearanceChange = { [unowned card, unowned glass] in
            let solid = DS.reduceTransparency
            glass.isHidden = solid
            card.layer?.backgroundColor = solid ? Theme.ns(.surface).cg(in: card) : NSColor.clear.cgColor
            card.layer?.borderColor = Theme.ns(.line).cg(in: card)
        }
        card.onAppearanceChange?()
        let root = NSView()
        root.addSubview(glass)
        root.addSubview(card)
        panel.contentView = root
        card.addSubview(header)
        card.addSubview(editor)
        card.addSubview(list)
        card.addSubview(chips)
        card.addSubview(footer)
        let tv = editor.textView
        tv.composer = composer
        tv.placeholder = "What should the agent do?   @ machine · # folder · / tool · ~ branch · drop files"
        tv.onCommand = { [weak self] cmd in self?.handle(cmd) ?? false }
        panel.onKey = { [weak self] cmd in self?.handle(cmd) ?? false }
        composer.onReplaceText = { [weak tv] text, caret in tv?.replaceAll(with: text, caret: caret) }
        footer.rootView = DraftFooter(composer: composer, quick: true, onStart: { [weak self] in self?.start() })
        observeChanges { [weak self] in self?.refresh() }
    }

    var isVisible: Bool { panel.isVisible }

    func toggle() { panel.isVisible ? hide() : show() }

    func show() {
        // No project preset (not the selected card's, not the last used):
        // the folder is chosen here (#, the chip); it stays for the next
        // launch once chosen.
        let d = model.quickDraft
        editor.textView.show(d.text)
        let screen = NSScreen.screens.first { NSMouseInRect(NSEvent.mouseLocation, $0.frame, false) } ?? NSScreen.main
        let vf = screen?.visibleFrame ?? NSRect(x: 0, y: 0, width: 1440, height: 900)
        let h = height()
        panel.setFrame(NSRect(x: vf.midX - Self.width / 2, y: vf.maxY - vf.height * 0.22 - h, width: Self.width, height: h), display: true)
        layout()
        panel.alphaValue = 0
        panel.orderFrontRegardless()
        panel.makeKey()
        panel.makeFirstResponder(editor.textView)
        let tv = editor.textView
        tv.setSelectedRange(NSRange(location: (tv.string as NSString).length, length: 0))
        card.onAppearanceChange?() // Reduce Transparency may have changed
        DS.animate(.quick, { panel.animator().alphaValue = 1 })
        if DS.reduceMotion { panel.alphaValue = 1 }
    }

    /// ⌘N in an agent window or the focus view: preset to that agent's
    /// folder and machine (contract "As built — new agents per window");
    /// what was written stays. A #token in it would decide the folder, so
    /// it goes.
    func show(project: String?, machine: String?) {
        var d = model.quickDraft
        if let t = composer.resolution.tokens.last(where: { $0.token.kind == .project })?.token {
            d.text = (d.text as NSString).replacingCharacters(in: NSRange(location: t.location, length: t.length), with: "")
        }
        d = DraftSeed.folderChanged(d, to: project, machinePinned: true)
        d.machine = machine
        d.machineExplicit = machine != nil
        model.quickDraft = d
        composer.error = nil
        show()
    }

    func hide() {
        composer.completion = nil
        DS.animate(.quick, { panel.animator().alphaValue = 0 }, completion: { self.panel.orderOut(nil) })
    }

    private func handle(_ cmd: AppCommand) -> Bool {
        switch cmd {
        case .startDraft, .startDraftAndNew: start(); return true
        case .leaveDraft, .closeAgent: hide(); return true
        case .toggleWorktree: composer.toggleWorktree(); return true
        default: return false
        }
    }

    private func start() {
        Task { @MainActor in
            if await model.startQuick() == nil {
                editor.textView.show("")
                hide()
            }
        }
    }

    private func refresh() {
        _ = composer.completion
        _ = composer.resolution
        list.rootView = AnyView(QuickCompletion(composer: composer))
        chips.rootView = AnyView(DraftChips(composer: composer, onChip: { [weak self] k in
            guard let self else { return }
            self.panel.makeFirstResponder(self.editor.textView)
            self.composer.complete(k)
        }, compact: true))
        chipsHeight = min(DraftChips.maxHeight, DraftChips.height(chips.rootView, width: Self.width - 2 * DS.Spacing.xl, measure: chipMeasure))
        let h = height()
        if panel.isVisible, abs(panel.frame.height - h) > 0.5 {
            var f = panel.frame
            f.origin.y += f.height - h
            f.size.height = h
            panel.setFrame(f, display: true)
        }
        layout()
    }

    private var completionHeight: CGFloat {
        guard let c = composer.completion else { return 0 }
        return CGFloat(min(c.items.count, Self.completionRows)) * OverlayLook.rowHeight + 2 * DS.Spacing.s
    }

    /// Header, the prompt, the completion list (when open), the chips, the
    /// footer, with the sheet's padding between them.
    private func height() -> CGFloat {
        Self.headerHeight + DS.Spacing.xs + Self.editorHeight + DS.Spacing.xs + completionHeight
            + DS.Spacing.m + chipsHeight + DS.Spacing.l + DraftFooter.height + DS.Spacing.l
    }

    private func layout() {
        let w = Self.width, h = panel.frame.height
        let pad = DS.Spacing.xl
        panel.contentView?.frame = NSRect(x: 0, y: 0, width: w, height: h)
        glass.frame = NSRect(x: 0, y: 0, width: w, height: h)
        card.frame = NSRect(x: 0, y: 0, width: w, height: h)
        header.frame = NSRect(x: 0, y: 0, width: w, height: Self.headerHeight)
        var y = Self.headerHeight + DS.Spacing.xs
        editor.frame = NSRect(x: pad - DS.Spacing.xs, y: y, width: w - 2 * pad + 2 * DS.Spacing.xs, height: Self.editorHeight)
        y += Self.editorHeight + DS.Spacing.xs
        let ch = completionHeight
        list.frame = NSRect(x: pad - DS.Spacing.s, y: y, width: w - 2 * pad + 2 * DS.Spacing.s, height: ch)
        list.isHidden = ch == 0
        y += ch + DS.Spacing.m
        chips.frame = NSRect(x: pad, y: y, width: w - 2 * pad, height: chipsHeight)
        footer.frame = NSRect(x: pad, y: h - DS.Spacing.l - DraftFooter.height, width: w - 2 * pad, height: DraftFooter.height)
    }
}

struct QuickHeader: View {
    var body: some View {
        HStack(spacing: DS.Spacing.m) {
            StateMark(.starting)
            Text("New agent").font(.ds(.body, .semibold)).foregroundStyle(Theme.fg)
            Text("quick launch").font(.ds(.chrome)).foregroundStyle(Theme.dim)
            Spacer()
            Image(systemName: "rectangle.grid.2x2").font(.ds(.meta)).foregroundStyle(Theme.dim)
            Text("joins the wall").font(.ds(.chrome)).foregroundStyle(Theme.dim)
        }
        .padding(.horizontal, DS.Spacing.xl)
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .overlay(alignment: .bottom) { Rectangle().fill(OverlayLook.line).frame(height: 1) }
    }
}

/// The completion list inline in the panel (the panel grows for it).
struct QuickCompletion: View {
    var composer: ComposerModel
    var body: some View {
        if let c = composer.completion {
            let sel = c.selected
            VStack(spacing: 0) {
                ForEach(Array(c.items.prefix(QuickLaunchController.completionRows).enumerated()), id: \.element.id) { i, item in
                    OverlayRow(title: item.title, detail: item.detail, mark: item.mark, dot: nil, checked: false,
                               selected: i == sel, enabled: item.enabled, tint: TokenStyle.fg(item.kind))
                        .onTapGesture { if item.enabled { composer.accept(item) } }
                }
            }
            .padding(DS.Spacing.xs)
            .background(DS.Radius.shape(DS.Radius.tile).fill(Theme.chipBG.opacity(0.5)))
            .overlay(DS.Radius.shape(DS.Radius.tile).strokeBorder(OverlayLook.border, lineWidth: 1))
            .accessibilityIdentifier("quicklaunch.completion")
        }
    }
}
