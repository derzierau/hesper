import AppKit
import HesperCore
import SwiftUI

/// G1, the work graph: lanes per machine, dashed project regions, nodes
/// tinted per loop and typed edges, drawn in one Canvas from LoopMap's
/// geometry (built once per store version, `LoopMapCache`). A click on a
/// node opens its loop; the legend says what each line means.
struct LoopMapView: View {
    var map: LoopMap
    /// `colors(map)`, computed when the map was built.
    var colors: [String: Theme.Token]
    var onOpen: (String) -> Void

    /// Loop tints in order of the loops' first node (never signal or
    /// error: those mean "needs you" and "failed").
    static let tints: [Theme.Token] = [.working, .done, .question, .horizon, .text2, .dim]
    static let arrow: CGFloat = 5
    static let dash: [CGFloat] = [4, 3]
    static let dot: [CGFloat] = [1, 3]

    var body: some View {
        if map.isEmpty {
            LoopsEmpty(symbol: "point.3.connected.trianglepath.dotted", title: "Nothing to draw yet",
                       detail: "Agents, their handoffs and the steps they leave you show here as soon as a loop has members.")
        } else {
            VStack(spacing: 0) {
                ScrollView([.horizontal, .vertical]) {
                    Canvas { ctx, _ in draw(&ctx, colors) }
                        .frame(width: map.width, height: map.height)
                        .contentShape(Rectangle())
                        .onTapGesture(coordinateSpace: .local) { p in
                            if let n = map.nodes.first(where: { rect($0.frame).contains(p) }) { onOpen(n.loop) }
                        }
                        .accessibilityElement(children: .ignore)
                        .accessibilityLabel("Map of \(Set(map.nodes.map(\.loop)).count) loops, \(map.nodes.count) nodes")
                        .accessibilityIdentifier("loops.mapCanvas")
                }
                Rectangle().fill(Theme.stroke).frame(height: 1)
                legend
            }
        }
    }

    /// Loop id → tint, by first appearance (the map's order is stable).
    static func colors(_ m: LoopMap) -> [String: Theme.Token] {
        var out: [String: Theme.Token] = [:]
        for n in m.nodes where out[n.loop] == nil { out[n.loop] = tints[out.count % tints.count] }
        return out
    }

    private func rect(_ r: MapRect) -> CGRect { CGRect(x: r.x, y: r.y, width: r.width, height: r.height) }

    private func draw(_ ctx: inout GraphicsContext, _ colors: [String: Theme.Token]) {
        for lane in map.lanes {
            let r = rect(lane.frame)
            ctx.fill(DS.Radius.shape(DS.Radius.tile).path(in: r), with: .color(Theme.color(.surface)))
            ctx.draw(Text(lane.title).font(DS.font(.chrome, .semibold)).foregroundStyle(Theme.fg2),
                     at: CGPoint(x: r.minX + DS.Spacing.m, y: r.minY + DS.Spacing.m), anchor: .leading)
        }
        for region in map.regions {
            let r = rect(region.frame)
            ctx.stroke(DS.Radius.shape(DS.Radius.control).path(in: r), with: .color(Theme.color(.dim2)),
                       style: StrokeStyle(lineWidth: DS.hairline, dash: Self.dash))
            ctx.draw(Text(region.title).font(DS.font(.meta)).foregroundStyle(Theme.dim),
                     at: CGPoint(x: r.minX + DS.Spacing.s, y: r.minY + DS.Spacing.m), anchor: .leading)
        }
        for e in map.edges { edge(&ctx, e) }
        for n in map.nodes { node(&ctx, n, tint: colors[n.loop] ?? .working) }
    }

    static func style(_ k: MapEdgeKind) -> (Theme.Token, StrokeStyle) {
        switch k {
        case .started: return (.dim, StrokeStyle(lineWidth: 1.5))
        case .continued: return (.text2, StrokeStyle(lineWidth: 1.5))
        case .handoff: return (.dim, StrokeStyle(lineWidth: 1.5, dash: dot))
        case .youStep: return (.signal, StrokeStyle(lineWidth: 1.5, dash: dot))
        case .blocks: return (.horizon, StrokeStyle(lineWidth: 2))
        case .brokenParent: return (.error, StrokeStyle(lineWidth: 1.5, dash: dash))
        }
    }

    private func edge(_ ctx: inout GraphicsContext, _ e: MapEdge) {
        let (token, style) = Self.style(e.kind)
        let a = CGPoint(x: e.start.x, y: e.start.y), b = CGPoint(x: e.end.x, y: e.end.y)
        var p = Path()
        p.move(to: a)
        if e.backward {
            // Back along the lane: bow below both ends.
            let drop = max(abs(b.y - a.y), DS.Spacing.xxl)
            p.addCurve(to: b, control1: CGPoint(x: a.x + DS.Spacing.xxl, y: max(a.y, b.y) + drop), control2: CGPoint(x: b.x - DS.Spacing.xxl, y: max(a.y, b.y) + drop))
        } else {
            let mid = (a.x + b.x) / 2
            p.addCurve(to: b, control1: CGPoint(x: mid, y: a.y), control2: CGPoint(x: mid, y: b.y))
        }
        let color = Theme.color(token)
        ctx.stroke(p, with: .color(color), style: style)
        var head = Path()
        head.move(to: b)
        head.addLine(to: CGPoint(x: b.x - Self.arrow, y: b.y - Self.arrow))
        head.move(to: b)
        head.addLine(to: CGPoint(x: b.x - Self.arrow, y: b.y + Self.arrow))
        ctx.stroke(head, with: .color(color), lineWidth: style.lineWidth)
    }

    private func node(_ ctx: inout GraphicsContext, _ n: MapNode, tint: Theme.Token) {
        let r = rect(n.frame)
        let shape = DS.Radius.shape(DS.Radius.control).path(in: r)
        let missing = n.kind == .missing
        ctx.fill(shape, with: .color(Theme.color(.tile)))
        ctx.fill(shape, with: .color(Color(nsColor: Theme.ns(missing ? .error : tint, alpha: DS.Alpha.tint))))
        ctx.stroke(shape, with: .color(Color(nsColor: Theme.ns(missing ? .error : tint, alpha: DS.Alpha.edge))),
                   style: StrokeStyle(lineWidth: DS.hairline, dash: missing || n.kind == .loop ? Self.dash : []))
        // The state square (a step is the person's: question; a file plain).
        let side = CGFloat(StateMarkKind.side)
        let sq = CGRect(x: r.minX + DS.Spacing.m, y: r.midY - side / 2 - (n.detail == nil ? 0 : DS.Spacing.s), width: side, height: side)
        let kind: StateMarkKind? = {
            switch n.kind {
            case .agent: return n.state.map(StateMarkKind.init) ?? .exited
            case .step: return .question
            case .missing: return .error
            case .file, .loop: return nil
            }
        }()
        if let kind {
            let c = Theme.color(Theme.token(kind.tone))
            switch kind.fill {
            case .filled: ctx.fill(Path(sq), with: .color(c))
            case .outlined: ctx.stroke(Path(sq.insetBy(dx: 0.75, dy: 0.75)), with: .color(c), lineWidth: 1.5)
            case .slashed:
                ctx.stroke(Path(sq.insetBy(dx: 0.75, dy: 0.75)), with: .color(c), lineWidth: 1.5)
                var s = Path(); s.move(to: CGPoint(x: sq.minX, y: sq.maxY)); s.addLine(to: CGPoint(x: sq.maxX, y: sq.minY))
                ctx.stroke(s, with: .color(c), lineWidth: 1)
            }
        } else {
            ctx.draw(Image(systemName: n.kind == .file ? "doc" : "arrow.triangle.2.circlepath"), in: sq.insetBy(dx: -2, dy: -2))
        }
        let x = sq.maxX + DS.Spacing.s
        let w = r.maxX - x - DS.Spacing.s
        ctx.draw(Text(n.title).font(DS.font(.chrome, .medium)).foregroundStyle(Theme.fg),
                 in: CGRect(x: x, y: sq.midY - DS.Spacing.m / 2 - 1, width: w, height: DS.Spacing.l))
        if let d = n.detail {
            ctx.draw(Text(d).font(DS.font(.meta)).foregroundStyle(Theme.dim),
                     in: CGRect(x: x, y: sq.midY + DS.Spacing.s, width: w, height: DS.Spacing.l))
        }
    }

    private var legend: some View {
        HStack(spacing: DS.Spacing.l) {
            ForEach(MapEdgeKind.allCases, id: \.self) { k in
                HStack(spacing: DS.Spacing.xs) {
                    Canvas { ctx, size in
                        var p = Path()
                        p.move(to: CGPoint(x: 0, y: size.height / 2))
                        p.addLine(to: CGPoint(x: size.width, y: size.height / 2))
                        let (t, s) = Self.style(k)
                        ctx.stroke(p, with: .color(Theme.color(t)), style: s)
                    }
                    .frame(width: DS.Spacing.xl, height: DS.Spacing.s)
                    Text(Self.legend(k)).font(DS.font(.meta)).foregroundStyle(Theme.dim)
                }
            }
            Spacer(minLength: 0)
            Text("Colors: one per loop · click a node to open its loop").font(DS.font(.meta)).foregroundStyle(Theme.dim)
        }
        .padding(.horizontal, DS.Spacing.l)
        .frame(height: DS.chromeMaxHeight)
        .accessibilityIdentifier("loops.legend")
    }

    static func legend(_ k: MapEdgeKind) -> String {
        switch k {
        case .started: return "started"
        case .continued: return "continued"
        case .handoff: return "handoff"
        case .youStep: return "step for you"
        case .blocks: return "blocks"
        case .brokenParent: return "parent not found"
        }
    }
}
