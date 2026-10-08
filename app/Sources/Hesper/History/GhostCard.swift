import AppKit
import HesperCore

/// A removed agent's ghost on its band's shelf (Grid + Shelf, a day,
/// newest 3 per project; Settings › General turns them off): a dashed
/// card with its exited StateMark and ⏎ Resume / ⌫ Forget (Kbd buttons).
/// Click selects, double-click resumes.
@MainActor
final class GhostCardView: NSView {
    private(set) var session: Session
    private weak var model: AppModel?
    private let border = CAShapeLayer()
    private let mark = StateMarkView(.exited)
    private let title = NSTextField(labelWithString: "")
    private let detail = NSTextField(labelWithString: "")
    private let resumeButton = GhostButton(key: "⏎", title: "Resume")
    private let forgetButton = GhostButton(key: "⌫", title: "Forget")
    private(set) var placed: WallTile?
    var isSelected = false { didSet { if oldValue != isSelected { updateBorder() } } }

    override var isFlipped: Bool { true }

    init(session: Session, model: AppModel) {
        self.session = session
        self.model = model
        super.init(frame: .zero)
        wantsLayer = true
        DS.Radius.apply(DS.Radius.tile, to: layer)
        layer?.backgroundColor = Theme.ns(.tile, alpha: Self.fillAlpha).cg(in: self)
        border.fillColor = nil
        border.lineDashPattern = Self.dash
        layer?.addSublayer(border)
        addSubview(mark)
        title.font = .ds(.chrome, .semibold)
        title.textColor = Theme.ns(.text2)
        detail.font = .ds(.meta)
        detail.textColor = Theme.ns(.dim)
        for l in [title, detail] { l.lineBreakMode = .byTruncatingTail; l.maximumNumberOfLines = 1; addSubview(l) }
        resumeButton.onClick = { [weak self] in guard let self else { return }; self.model?.resumeGhost(self.session) }
        forgetButton.onClick = { [weak self] in guard let self else { return }; self.model?.forgetGhost(self.session) }
        addSubview(resumeButton)
        addSubview(forgetButton)
        setAccessibilityElement(true)
        setAccessibilityRole(.group)
        apply(session)
        updateBorder()
    }

    @available(*, unavailable)
    required init?(coder: NSCoder) { fatalError() }

    override func viewDidChangeEffectiveAppearance() {
        super.viewDidChangeEffectiveAppearance()
        layer?.backgroundColor = Theme.ns(.tile, alpha: Self.fillAlpha).cg(in: self)
        updateBorder()
    }

    /// A ghost is a faded, dashed tile.
    private static let fillAlpha: CGFloat = 0.55
    private static let dash: [NSNumber] = [5, 4]
    private static let borderWidth: CGFloat = 1.2

    var itemID: String { GhostCards.itemID(session) }

    func apply(_ s: Session) {
        session = s
        title.stringValue = s.title.isEmpty ? "(untitled)" : s.title
        var parts = [SessionFormat.kindLabel(s.kind)]
        if let r = s.removedAt { parts.append("removed " + (Theme.elapsed(since: r).map { $0 + " ago" } ?? "")) }
        if let b = s.branch { parts.append(b) }
        detail.stringValue = parts.joined(separator: " · ")
        setAccessibilityIdentifier("ghost.\(itemID)")
        setAccessibilityLabel("Ended \(s.title): Resume or Forget")
    }

    func place(_ t: WallTile, animated: Bool) {
        placed = t
        let f = NSRect(x: t.card.x, y: t.card.y, width: t.card.width, height: t.card.height)
        if animated && !frame.isEmpty { animator().frame = f } else { frame = f }
        needsLayout = true
    }

    private func updateBorder() {
        border.strokeColor = Theme.ns(isSelected ? .working : .line).cg(in: self)
        border.lineWidth = isSelected ? 2 : Self.borderWidth
    }

    override func layout() {
        super.layout()
        border.frame = bounds
        border.path = CGPath(roundedRect: bounds.insetBy(dx: 1, dy: 1), cornerWidth: DS.Radius.tile, cornerHeight: DS.Radius.tile, transform: nil)
        let w = bounds.width, pad = DS.Spacing.l
        let th = ceil(title.intrinsicContentSize.height), dh = ceil(detail.intrinsicContentSize.height)
        let side = mark.side
        mark.frame = NSRect(x: pad, y: (pad + (th - side) / 2).rounded(), width: side, height: side)
        let tx = pad + side + DS.Spacing.m
        title.frame = NSRect(x: tx, y: pad, width: max(0, w - tx - pad), height: th)
        detail.frame = NSRect(x: tx, y: pad + th + DS.Spacing.xxs, width: max(0, w - tx - pad), height: dh)
        let bh = Pill.height
        let y = max(detail.frame.maxY + DS.Spacing.s, bounds.height - bh - DS.Spacing.m)
        let rw = resumeButton.fittingWidth, fw = forgetButton.fittingWidth
        resumeButton.frame = NSRect(x: pad, y: y, width: rw, height: bh)
        forgetButton.frame = NSRect(x: pad + rw + DS.Spacing.s, y: y, width: fw, height: bh)
    }

    override func mouseDown(with event: NSEvent) {
        guard let model else { return }
        if event.clickCount == 2 { model.resumeGhost(session); return }
        model.select(itemID)
        window?.makeFirstResponder(superview)
    }
}

/// A small outlined button on a ghost card: its Kbd, then the title.
final class GhostButton: NSView {
    private let label = NSTextField(labelWithString: "")
    private let kbd: KbdView
    var onClick: (() -> Void)?

    init(key: String, title: String) {
        kbd = KbdView(key)
        super.init(frame: .zero)
        wantsLayer = true
        DS.Radius.apply(DS.Radius.control, to: layer)
        layer?.borderWidth = 1
        layer?.borderColor = Theme.ns(.line).cg(in: self)
        label.stringValue = title
        label.font = .ds(.chrome, .medium)
        label.textColor = Theme.ns(.text)
        addSubview(kbd)
        addSubview(label)
        setAccessibilityElement(true)
        setAccessibilityRole(.button)
        setAccessibilityLabel("\(title) (\(key))")
    }

    @available(*, unavailable)
    required init?(coder: NSCoder) { fatalError() }

    override func viewDidChangeEffectiveAppearance() {
        super.viewDidChangeEffectiveAppearance()
        layer?.borderColor = Theme.ns(.line).cg(in: self)
    }

    var fittingWidth: CGFloat { kbd.intrinsicContentSize.width + DS.Spacing.s + ceil(label.intrinsicContentSize.width) + 2 * DS.Spacing.s }

    override func layout() {
        super.layout()
        let k = kbd.intrinsicContentSize, s = label.intrinsicContentSize
        kbd.frame = NSRect(x: DS.Spacing.s, y: ((bounds.height - k.height) / 2).rounded(), width: k.width, height: k.height)
        label.frame = NSRect(x: kbd.frame.maxX + DS.Spacing.s, y: ((bounds.height - s.height) / 2).rounded(), width: ceil(s.width), height: s.height)
    }

    override func mouseDown(with event: NSEvent) {}
    override func mouseUp(with event: NSEvent) { onClick?() }
    func press() { onClick?() }
}
