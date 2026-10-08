import AppKit
import HesperCore
import SwiftUI

/// StateMark: the one marker for every agent state, a 7 pt square (the
/// wordmark's full stop). Filled for active states, outlined (1.5 pt) for
/// idle and starting, outlined with a slash for exited; "needs you" pulses
/// slowly (2 s, off under Reduce Motion). Shape and fill carry the meaning
/// as well as the color.
///
/// SwiftUI: `StateMark(state: agent.state)`, `StateMark(.needsYou)`,
/// `StateMark(color: Theme.color(hex))` (a plain filled square, for
/// non-state marks such as a machine being online).
/// AppKit: `StateMarkView` (layer-backed), `StateMark.image(_:)` (menus).
struct StateMark: View {
    enum Content: Equatable {
        case kind(StateMarkKind)
        case color(Color)
    }
    var content: Content
    var side: CGFloat = CGFloat(StateMarkKind.side)

    init(_ kind: StateMarkKind, side: CGFloat = CGFloat(StateMarkKind.side)) { content = .kind(kind); self.side = side }
    init(state: AgentState, side: CGFloat = CGFloat(StateMarkKind.side)) { self.init(StateMarkKind(state), side: side) }
    init(color: Color, side: CGFloat = CGFloat(StateMarkKind.side)) { content = .color(color); self.side = side }

    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @State private var pulsing = false

    var body: some View {
        mark
            .frame(width: side, height: side)
            .opacity(pulsing ? Self.pulseLow : 1)
            .animation(pulses ? .easeInOut(duration: DesignTokens.pulsePeriod / 2).repeatForever(autoreverses: true) : nil, value: pulsing)
            .onAppear { if pulses { pulsing = true } }
            .onChange(of: pulses) { _, p in pulsing = p }
            .accessibilityHidden(true)
    }

    static let pulseLow: Double = 0.4

    private var pulses: Bool {
        if case .kind(let k) = content { return k.pulses(reduceMotion: reduceMotion) }
        return false
    }

    @ViewBuilder private var mark: some View {
        switch content {
        case .color(let c):
            Rectangle().fill(c)
        case .kind(let k):
            let c = Theme.color(Theme.token(k.tone))
            let w = CGFloat(StateMarkKind.stroke)
            switch k.fill {
            case .filled:
                Rectangle().fill(c)
            case .outlined:
                Rectangle().inset(by: w / 2).stroke(c, lineWidth: w)
            case .slashed:
                ZStack {
                    Rectangle().inset(by: w / 2).stroke(c, lineWidth: w)
                    SlashShape().stroke(c, lineWidth: w)
                }
            }
        }
    }

    private struct SlashShape: Shape {
        func path(in r: CGRect) -> Path {
            var p = Path()
            p.move(to: CGPoint(x: r.minX, y: r.maxY))
            p.addLine(to: CGPoint(x: r.maxX, y: r.minY))
            return p
        }
    }

    // MARK: Drawing (AppKit, images)

    /// Draws a mark into `rect` of the current context (y up or down: the
    /// slash goes bottom-left to top-right either way it reads as a slash).
    static func draw(_ kind: StateMarkKind, in rect: CGRect, color: NSColor, context cg: CGContext) {
        let w = CGFloat(StateMarkKind.stroke)
        switch kind.fill {
        case .filled:
            cg.setFillColor(color.cgColor)
            cg.fill(rect)
        case .outlined, .slashed:
            cg.setStrokeColor(color.cgColor)
            cg.setLineWidth(w)
            cg.stroke(rect.insetBy(dx: w / 2, dy: w / 2))
            if kind.fill == .slashed {
                cg.move(to: CGPoint(x: rect.minX, y: rect.minY))
                cg.addLine(to: CGPoint(x: rect.maxX, y: rect.maxY))
                cg.strokePath()
            }
        }
    }

    /// The mark as an image (NSMenuItem.image): `side` square plus padding,
    /// resolved in the drawing appearance (dynamic colors).
    static func image(_ kind: StateMarkKind, side: CGFloat = CGFloat(StateMarkKind.side), padding: CGFloat = DS.Spacing.xxs) -> NSImage {
        let size = side + 2 * padding
        let img = NSImage(size: NSSize(width: size, height: size), flipped: false) { r in
            guard let cg = NSGraphicsContext.current?.cgContext else { return false }
            draw(kind, in: CGRect(x: padding, y: padding, width: side, height: side), color: Theme.ns(Theme.token(kind.tone)), context: cg)
            return true
        }
        img.accessibilityDescription = kind.label
        return img
    }
}

/// AppKit StateMark: a layer-backed square that redraws in its appearance
/// and pulses (Core Animation, 2 s) for "needs you" unless Reduce Motion.
/// Its intrinsic size is the mark's side.
@MainActor
final class StateMarkView: NSView {
    var kind: StateMarkKind { didSet { if oldValue != kind { refresh() } } }
    /// A plain filled square instead of a state (non-state marks).
    var plainColor: NSColor? { didSet { refresh() } }
    var side: CGFloat { didSet { invalidateIntrinsicContentSize(); refresh() } }
    /// Injectable for tests and the gallery; defaults to the system setting.
    var reduceMotion: () -> Bool = { DS.reduceMotion }

    private let shape = CAShapeLayer()
    private let slash = CAShapeLayer()

    init(_ kind: StateMarkKind = .idle, side: CGFloat = CGFloat(StateMarkKind.side)) {
        self.kind = kind
        self.side = side
        super.init(frame: NSRect(x: 0, y: 0, width: side, height: side))
        wantsLayer = true
        layer?.addSublayer(shape)
        layer?.addSublayer(slash)
        setAccessibilityElement(false)
        refresh()
    }

    @available(*, unavailable)
    required init?(coder: NSCoder) { fatalError() }

    override var intrinsicContentSize: NSSize { NSSize(width: side, height: side) }

    override func viewDidChangeEffectiveAppearance() {
        super.viewDidChangeEffectiveAppearance()
        refresh()
    }

    override func viewDidMoveToWindow() {
        super.viewDidMoveToWindow()
        refresh()
    }

    override func layout() {
        super.layout()
        refresh()
    }

    private func refresh() {
        let w = CGFloat(StateMarkKind.stroke)
        let r = CGRect(x: (bounds.width - side) / 2, y: (bounds.height - side) / 2, width: side, height: side)
        let color = (plainColor ?? Theme.ns(Theme.token(kind.tone))).cg(in: self)
        let fill: StateMarkKind.Fill = plainColor != nil ? .filled : kind.fill
        CATransaction.begin()
        CATransaction.setDisableActions(true)
        shape.frame = bounds
        slash.frame = bounds
        switch fill {
        case .filled:
            shape.path = CGPath(rect: r, transform: nil)
            shape.fillColor = color
            shape.strokeColor = nil
            slash.path = nil
        case .outlined, .slashed:
            shape.path = CGPath(rect: r.insetBy(dx: w / 2, dy: w / 2), transform: nil)
            shape.fillColor = nil
            shape.strokeColor = color
            shape.lineWidth = w
            if fill == .slashed {
                let p = CGMutablePath()
                p.move(to: CGPoint(x: r.minX, y: r.minY))
                p.addLine(to: CGPoint(x: r.maxX, y: r.maxY))
                slash.path = p
                slash.strokeColor = color
                slash.lineWidth = w
            } else {
                slash.path = nil
            }
        }
        CATransaction.commit()
        updatePulse()
    }

    private static let pulseKey = "hesper.pulse"

    private func updatePulse() {
        guard let layer else { return }
        let want = plainColor == nil && kind.pulses(reduceMotion: reduceMotion()) && window != nil
        let has = layer.animation(forKey: Self.pulseKey) != nil
        if want && !has {
            let a = CABasicAnimation(keyPath: "opacity")
            a.fromValue = 1
            a.toValue = StateMark.pulseLow
            a.duration = DesignTokens.pulsePeriod / 2
            a.autoreverses = true
            a.repeatCount = .infinity
            a.timingFunction = CAMediaTimingFunction(name: .easeInEaseOut)
            a.isRemovedOnCompletion = false
            layer.add(a, forKey: Self.pulseKey)
        } else if !want && has {
            layer.removeAnimation(forKey: Self.pulseKey)
        }
    }
}
