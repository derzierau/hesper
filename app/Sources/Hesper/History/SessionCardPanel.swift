import AppKit
import HesperCore
import SwiftUI

/// History's right-hand preview, in plain AppKit (labels set only when
/// their text changed): arrowing through the list costs well under a
/// millisecond per row. A vertical stack: the header (title, "Claude ·
/// ~/projects/x · mini"), a compact key/value list (you asked / it
/// answered / changed / open todos / branch; scrolls when long), and the
/// actions pinned to the bottom: the primary "Resume here ⏎" (or a quiet
/// note when it can't be resumed), "Fork ⌥⏎", "Continue on <Mac> ⌘⏎",
/// then quiet text buttons (C A ⌫ ⌘C). "Continue in …" (editing the
/// brief) is the SwiftUI SessionCardView.
@MainActor
final class SessionCardPanel: NSView {
    let model: SessionCardModel

    // Content (scrolls).
    private let scroll = NSScrollView()
    private let content = FlippedView()
    private let title = NSTextField(wrappingLabelWithString: "")
    private let subtitle = NSTextField(labelWithString: "")
    private let busy = NSTextField(labelWithString: "")
    private var keys: [NSTextField] = []
    private let asked = NSTextField(wrappingLabelWithString: "")
    private let answered = NSTextField(wrappingLabelWithString: "")
    private let changed = FileChips()
    private let todos = NSTextField(wrappingLabelWithString: "")
    private let branch = NSTextField(labelWithString: "")
    private let empty = NSTextField(labelWithString: "Select a session to see where you left off")

    // Actions (pinned to the bottom).
    private let actionsBox = FlippedView()
    private let actionsLine = NSView()
    private let outsideNote = NSTextField(wrappingLabelWithString: "")
    private let note = NSTextField(wrappingLabelWithString: "")
    private var buttons: [ActionButton] = []
    private var quiet: [ActionButton] = []
    private var shownActions: [SessionCardText.Action] = []
    private var shown: SessionCardText?
    /// Only while continuing: a hidden hosting view would still re-render
    /// on every card change.
    private var continueHost: NSHostingView<SessionCardView>?

    /// The keys that get a button (the rest are quiet text buttons).
    static let buttonKeys: Set<String> = ["⏎", "⌥⏎", "⌘⏎"]
    /// The ⏎ action that is a state, not something to do.
    static let outsideTitle = "Running outside Hesper"

    private static func lineHeight(_ f: NSFont) -> CGFloat { ceil(f.ascender - f.descender + f.leading) }
    private static let fTitle = NSFont.ds(.panelTitle, .semibold)
    private static let fText = NSFont.ds(.chrome)
    private static let fMono = NSFont.ds(.meta)

    override var isFlipped: Bool { true }

    init(model: SessionCardModel) {
        self.model = model
        super.init(frame: .zero)
        scroll.documentView = content
        scroll.drawsBackground = false
        scroll.hasVerticalScroller = true
        scroll.autohidesScrollers = true
        scroll.scrollerStyle = .overlay
        scroll.verticalScrollElasticity = .allowed
        addSubview(scroll)
        addSubview(actionsBox)
        actionsLine.wantsLayer = true
        actionsBox.addSubview(actionsLine)

        func style(_ l: NSTextField, _ font: NSFont, _ color: Theme.Token, lines: Int = 1, in parent: NSView) {
            l.font = font
            l.textColor = Theme.ns(color)
            l.maximumNumberOfLines = lines
            l.lineBreakMode = lines == 1 ? .byTruncatingTail : .byWordWrapping
            l.cell?.truncatesLastVisibleLine = true
            l.isSelectable = false
            parent.addSubview(l)
        }
        style(title, Self.fTitle, .text, lines: 2, in: content)
        style(subtitle, Self.fMono, .dim, in: content)
        subtitle.lineBreakMode = .byTruncatingMiddle
        style(busy, Self.fText, .dim, in: content)
        busy.alignment = .right
        for k in ["you asked", "it answered", "changed", "open todos", "branch"] {
            let l = NSTextField(labelWithString: k)
            style(l, Self.fText, .dim, in: content)
            keys.append(l)
        }
        style(asked, Self.fText, .text, lines: 3, in: content)
        style(answered, Self.fText, .text2, lines: 4, in: content)
        content.addSubview(changed)
        changed.setAccessibilityElement(true)
        changed.setAccessibilityIdentifier("history.card.changed")
        style(todos, Self.fText, .text, lines: 2, in: content)
        style(branch, Self.fMono, .text2, in: content)
        branch.lineBreakMode = .byTruncatingMiddle
        style(outsideNote, Self.fText, .dim, lines: 2, in: actionsBox)
        style(note, Self.fMono, .dim, lines: 2, in: actionsBox)
        style(empty, Self.fText, .dim, in: self)
        empty.alignment = .center
        applyTheme()
        setAccessibilityElement(true)
        setAccessibilityIdentifier("history.card")
        setAccessibilityRole(.group)
        setAccessibilityLabel("Preview")
    }

    @available(*, unavailable)
    required init?(coder: NSCoder) { fatalError() }

    override func viewDidChangeEffectiveAppearance() {
        super.viewDidChangeEffectiveAppearance()
        applyTheme()
    }

    private func applyTheme() { actionsLine.layer?.backgroundColor = Theme.ns(.line).cg(in: self) }

    /// Reads the model (call after it changed).
    nonisolated(unsafe) static var maxUpdateMs = 0.0

    func update() {
        let t0 = CACurrentMediaTime()
        defer { Self.maxUpdateMs = max(Self.maxUpdateMs, (CACurrentMediaTime() - t0) * 1000); layoutSubtreeIfNeeded() }
        let continuing = model.mode == .continuing
        if continuing, continueHost == nil {
            let h = NSHostingView(rootView: SessionCardView(model: model))
            h.frame = bounds
            addSubview(h)
            continueHost = h
        } else if !continuing, let h = continueHost {
            h.removeFromSuperview()
            continueHost = nil
        }
        for v in subviews where v !== continueHost { v.isHidden = continuing }
        if continuing { return }
        if busy.stringValue != (model.busy ?? "") { busy.stringValue = model.busy ?? ""; needsLayout = true }
        guard let t = model.text else {
            scroll.isHidden = true
            actionsBox.isHidden = true
            empty.isHidden = false
            shown = nil
            return
        }
        empty.isHidden = true
        scroll.isHidden = false
        actionsBox.isHidden = false
        guard t != shown else { return }
        shown = t
        set(title, t.title)
        title.textColor = Theme.ns(t.title == "(untitled)" ? .dim : .text)
        set(subtitle, t.subtitle)
        set(asked, t.asked.isEmpty ? "—" : t.asked)
        set(answered, t.answered.isEmpty ? "—" : t.answered)
        changed.show(t)
        set(todos, t.todos.isEmpty ? "none" : t.todos.prefix(3).map { "☐ " + $0 }.joined(separator: "  ·  "))
        todos.textColor = Theme.ns(t.todos.isEmpty ? .dim : .text)
        set(branch, t.branch)
        set(note, t.resumeHereNote ?? "")
        if t.actions != shownActions {
            shownActions = t.actions
            for b in buttons + quiet { b.removeFromSuperview() }
            buttons = []
            quiet = []
            for a in t.actions {
                let run = { [weak self] in self?.model.onAction?(SessionCardModel.action(a.key)) ?? () }
                if a.key == "⏎" && a.title == Self.outsideTitle { continue } // a note, not a button
                if Self.buttonKeys.contains(a.key) {
                    let display = a.key == "⏎" && a.title == "Resume" ? "Resume here" : a.title
                    let b = ActionButton(key: a.key, title: display, style: a.key == "⏎" ? .primary : .secondary, action: run)
                    actionsBox.addSubview(b)
                    buttons.append(b)
                } else {
                    let b = ActionButton(key: a.key, title: a.title, style: .quiet, action: run)
                    actionsBox.addSubview(b)
                    quiet.append(b)
                }
            }
            let outside = t.actions.contains { $0.key == "⏎" && $0.title == Self.outsideTitle }
            set(outsideNote, outside ? "Running outside Hesper: open it where it runs, or fork it here." : "")
        }
        needsLayout = true
    }

    private func set(_ l: NSTextField, _ s: String) { if l.stringValue != s { l.stringValue = s } }

    /// A wrapping label's height at `width`, at most `lines` lines.
    private static func height(_ l: NSTextField, width: CGFloat, lines: Int, font: NSFont) -> CGFloat {
        let lh = lineHeight(font)
        guard !l.stringValue.isEmpty, width > 0 else { return lh }
        let h = l.cell?.cellSize(forBounds: NSRect(x: 0, y: 0, width: width, height: .greatestFiniteMagnitude)).height ?? lh
        return min(CGFloat(lines) * lh, max(lh, ceil(h)))
    }

    override func layout() {
        super.layout()
        continueHost?.frame = bounds
        let pad = DS.Spacing.xl, w = max(0, bounds.width - 2 * pad)
        let lx = Self.lineHeight(Self.fText), lm = Self.lineHeight(Self.fMono)
        empty.frame = NSRect(x: pad, y: (bounds.midY - lx / 2).rounded(), width: w, height: lx)

        // Actions, from the bottom up: quiet buttons, the note, the buttons, the outside note.
        let ah = layoutActions(width: bounds.width)
        actionsBox.frame = NSRect(x: 0, y: bounds.height - ah, width: bounds.width, height: ah)

        // Content.
        var y = pad
        let th = Self.height(title, width: w, lines: 2, font: Self.fTitle)
        title.frame = NSRect(x: pad, y: y, width: w, height: th)
        y += th + DS.Spacing.xs
        let busyW = busy.stringValue.isEmpty ? 0 : min(w / 3, ceil(busy.intrinsicContentSize.width))
        busy.frame = NSRect(x: pad + w - busyW, y: y, width: busyW, height: lx)
        subtitle.frame = NSRect(x: pad, y: y, width: max(0, w - busyW - (busyW > 0 ? DS.Spacing.m : 0)), height: lm)
        y += max(lm, busyW > 0 ? lx : 0) + DS.Spacing.xl
        let kw = SearchLook.previewLabelWidth, vx = pad + kw + DS.Spacing.m, vw = max(0, w - kw - DS.Spacing.m)
        let rows: [(NSView, CGFloat)] = [
            (asked, Self.height(asked, width: vw, lines: 3, font: Self.fText)),
            (answered, Self.height(answered, width: vw, lines: 4, font: Self.fText)),
            (changed, changed.height(forWidth: vw)),
            (todos, Self.height(todos, width: vw, lines: 2, font: Self.fText)),
            (branch, lm),
        ]
        for (i, (v, h)) in rows.enumerated() {
            // Labels on the value's first line.
            let ky = v === changed ? y + ((FileChips.chipHeight - lx) / 2).rounded() : (v === branch ? y + ((lm - lx) / 2).rounded() : y)
            keys[i].frame = NSRect(x: pad, y: ky, width: kw, height: lx)
            v.frame = NSRect(x: vx, y: y, width: vw, height: h)
            y += h + DS.Spacing.l
        }
        let contentH = y - DS.Spacing.l + pad
        let visibleH = max(0, bounds.height - ah)
        scroll.frame = NSRect(x: 0, y: 0, width: bounds.width, height: visibleH)
        content.frame = NSRect(x: 0, y: 0, width: scroll.contentSize.width, height: max(contentH, visibleH))
    }

    /// Lays the actions out in `actionsBox` (flipped, from its top); returns its height.
    private func layoutActions(width: CGFloat) -> CGFloat {
        let pad = DS.Spacing.xl, w = max(0, width - 2 * pad)
        var y = pad
        actionsLine.frame = NSRect(x: 0, y: 0, width: width, height: 1)
        if !outsideNote.stringValue.isEmpty {
            let h = Self.height(outsideNote, width: w, lines: 2, font: Self.fText)
            outsideNote.frame = NSRect(x: pad, y: y, width: w, height: h)
            outsideNote.isHidden = false
            y += h + DS.Spacing.m
        } else {
            outsideNote.isHidden = true
        }
        var x = pad
        let bh = ActionButton.height
        for b in buttons {
            let bw = min(w, b.fittingWidth)
            if x > pad && x + bw > pad + w { x = pad; y += bh + DS.Spacing.s }
            b.frame = NSRect(x: x, y: y, width: bw, height: bh)
            x += bw + DS.Spacing.s
        }
        if !buttons.isEmpty { y += bh + DS.Spacing.m }
        if !note.stringValue.isEmpty {
            let h = Self.height(note, width: w, lines: 2, font: Self.fMono)
            note.frame = NSRect(x: pad, y: y, width: w, height: h)
            note.isHidden = false
            y += h + DS.Spacing.m
        } else {
            note.isHidden = true
        }
        x = pad
        let qh = Pill.height
        for b in quiet {
            let bw = b.fittingWidth
            if x > pad && x + bw > pad + w { x = pad; y += qh + DS.Spacing.xs }
            b.frame = NSRect(x: x, y: y, width: bw, height: qh)
            x += bw + DS.Spacing.l
        }
        if !quiet.isEmpty { y += qh }
        return y + pad
    }
}

/// The preview's "changed" value: one Mono chip per file ("Toolbar.swift
/// +14 −1", counts in done / error), "uncommitted" in question; at most
/// two lines (then "+N more"). "reading git…" until sessions.show answers.
@MainActor
final class FileChips: NSView {
    static let chipHeight: CGFloat = Pill.height - DS.Spacing.xxs
    private var chips: [NSAttributedString] = []
    private var placeholder: NSAttributedString?
    private var laidOut: [(NSAttributedString, NSRect, Bool)] = [] // text, frame, is a chip

    override var isFlipped: Bool { true }

    private static let font = NSFont.ds(.meta)

    func show(_ t: SessionCardText) {
        let f = Self.font
        var out: [NSAttributedString] = []
        placeholder = nil
        if t.changed == nil {
            placeholder = NSAttributedString(string: "reading git…", attributes: [.font: f, .foregroundColor: Theme.ns(.dim)])
        } else if t.changedFiles.isEmpty && t.uncommitted == nil {
            placeholder = NSAttributedString(string: t.changed == "—" ? "—" : "no changes", attributes: [.font: f, .foregroundColor: Theme.ns(.dim)])
        }
        for file in t.changedFiles {
            let a = NSMutableAttributedString(string: (file.path as NSString).lastPathComponent, attributes: [.font: f, .foregroundColor: Theme.ns(.text2)])
            if file.added > 0 { a.append(NSAttributedString(string: " +\(file.added)", attributes: [.font: f, .foregroundColor: Theme.ns(.done)])) }
            if file.removed > 0 { a.append(NSAttributedString(string: " −\(file.removed)", attributes: [.font: f, .foregroundColor: Theme.ns(.error)])) }
            out.append(a)
        }
        if let u = t.uncommitted { out.append(NSAttributedString(string: u, attributes: [.font: f, .foregroundColor: Theme.ns(.question)])) }
        chips = out
        setAccessibilityLabel(t.changed.map { c in [c, t.uncommitted].compactMap { $0 }.filter { !$0.isEmpty }.joined(separator: ", ") } ?? "reading git")
        setAccessibilityValue(t.changed ?? "")
        needsLayout = true
        needsDisplay = true
    }

    private var padH: CGFloat { DS.Spacing.s }

    /// Places the chips in `width` (two lines at most); returns the height.
    @discardableResult
    private func place(width: CGFloat) -> CGFloat {
        laidOut = []
        let h = Self.chipHeight, gap = DS.Spacing.xs
        if let p = placeholder, chips.isEmpty {
            laidOut = [(p, NSRect(x: 0, y: ((h - ceil(p.size().height)) / 2).rounded(), width: width, height: ceil(p.size().height)), false)]
            return h
        }
        var x: CGFloat = 0, line = 0
        for (i, c) in chips.enumerated() {
            let cw = ceil(c.size().width) + 2 * padH
            if x > 0 && x + cw > width { line += 1; x = 0 }
            let remaining = chips.count - i
            if line == 1 && remaining > 1 {
                // Room on the last line for this chip and a "+N more"?
                let more = NSAttributedString(string: "+\(remaining - 1) more", attributes: [.font: Self.font, .foregroundColor: Theme.ns(.dim)])
                let mw = ceil(more.size().width) + 2 * padH
                if x + cw + gap + mw > width {
                    let m2 = NSAttributedString(string: "+\(remaining) more", attributes: [.font: Self.font, .foregroundColor: Theme.ns(.dim)])
                    laidOut.append((m2, NSRect(x: x, y: CGFloat(line) * (h + gap), width: min(width - x, ceil(m2.size().width) + 2 * padH), height: h), true))
                    break
                }
            }
            if line > 1 { break }
            laidOut.append((c, NSRect(x: x, y: CGFloat(line) * (h + gap), width: min(cw, width), height: h), true))
            x += cw + gap
        }
        return CGFloat(line + 1) * h + CGFloat(line) * gap
    }

    func height(forWidth w: CGFloat) -> CGFloat { place(width: w) }

    override func layout() {
        super.layout()
        place(width: bounds.width)
        needsDisplay = true
    }

    override func draw(_ dirtyRect: NSRect) {
        for (text, r, chip) in laidOut {
            if chip {
                Theme.ns(.line).setFill()
                NSBezierPath(roundedRect: r, xRadius: DS.Radius.kbd, yRadius: DS.Radius.kbd).fill()
                let th = ceil(text.size().height)
                text.draw(with: NSRect(x: r.minX + padH, y: r.minY + ((r.height - th) / 2).rounded(), width: r.width - 2 * padH, height: th),
                          options: [.usesLineFragmentOrigin, .truncatesLastVisibleLine])
            } else {
                text.draw(with: r, options: [.usesLineFragmentOrigin, .truncatesLastVisibleLine])
            }
        }
    }
}

/// An action of the preview: its title and Kbd. Primary: a `working`
/// fill; secondary: a 1 px `line` outline; quiet: dim text, no box.
@MainActor
final class ActionButton: NSView {
    enum Style { case primary, secondary, quiet }
    static let height: CGFloat = 28

    let key: String
    private let style: Style
    private let label = NSTextField(labelWithString: "")
    private let kbd: KbdView
    private let action: () -> Void
    private var hovering = false { didSet { applyTheme() } }
    private var tracking: NSTrackingArea?

    init(key: String, title: String, style: Style, action: @escaping () -> Void) {
        self.key = key
        self.style = style
        self.action = action
        kbd = KbdView(key)
        super.init(frame: .zero)
        wantsLayer = true
        DS.Radius.apply(DS.Radius.control, to: layer)
        label.font = .ds(.chrome, style == .quiet ? .regular : .medium)
        label.stringValue = title
        label.lineBreakMode = .byTruncatingTail
        addSubview(label)
        addSubview(kbd)
        applyTheme()
        setAccessibilityElement(true)
        setAccessibilityRole(.button)
        setAccessibilityLabel("\(title) (\(key))")
        setAccessibilityIdentifier("history.action.\(key)")
    }

    @available(*, unavailable)
    required init?(coder: NSCoder) { fatalError() }

    override func viewDidChangeEffectiveAppearance() {
        super.viewDidChangeEffectiveAppearance()
        applyTheme()
    }

    private func applyTheme() {
        switch style {
        case .primary:
            layer?.backgroundColor = Theme.ns(.working, alpha: hovering ? 0.88 : 1).cg(in: self)
            layer?.borderWidth = 0
            label.textColor = PanelView.sheetColor
            kbd.tint = PanelView.sheetColor
        case .secondary:
            layer?.backgroundColor = (hovering ? Theme.ns(.line, alpha: 0.6) : .clear).cg(in: self)
            layer?.borderWidth = 1
            layer?.borderColor = Theme.ns(.line).cg(in: self)
            label.textColor = Theme.ns(.text)
        case .quiet:
            layer?.backgroundColor = NSColor.clear.cgColor
            layer?.borderWidth = 0
            label.textColor = Theme.ns(hovering ? .text : .dim)
        }
    }

    private var padH: CGFloat { style == .quiet ? 0 : DS.Spacing.m }

    /// The title's width, measured from its text (with the label's 2 pt insets).
    private var labelWidth: CGFloat { ceil(label.attributedStringValue.size().width) + 2 * DS.Spacing.xxs }

    var fittingWidth: CGFloat { labelWidth + DS.Spacing.s + kbd.intrinsicContentSize.width + 2 * padH }

    override func layout() {
        super.layout()
        let s = label.intrinsicContentSize, k = kbd.intrinsicContentSize
        let kx = bounds.width - padH - k.width
        label.frame = NSRect(x: padH, y: ((bounds.height - s.height) / 2).rounded(), width: max(0, min(labelWidth, kx - DS.Spacing.s - padH)), height: s.height)
        kbd.frame = NSRect(x: kx, y: ((bounds.height - k.height) / 2).rounded(), width: k.width, height: k.height)
    }

    override func updateTrackingAreas() {
        super.updateTrackingAreas()
        if let t = tracking { removeTrackingArea(t) }
        let t = NSTrackingArea(rect: .zero, options: [.mouseEnteredAndExited, .activeInKeyWindow, .inVisibleRect], owner: self, userInfo: nil)
        addTrackingArea(t)
        tracking = t
    }

    override func mouseEntered(with event: NSEvent) { hovering = true }
    override func mouseExited(with event: NSEvent) { hovering = false }
    override func mouseDown(with event: NSEvent) {}
    override func mouseUp(with event: NSEvent) {
        if bounds.contains(convert(event.locationInWindow, from: nil)) { action() }
    }
    override func accessibilityPerformPress() -> Bool { action(); return true }
}
