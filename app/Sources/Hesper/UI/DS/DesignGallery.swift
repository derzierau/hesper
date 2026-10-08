#if DEBUG
import AppKit
import HesperCore
import SwiftUI

/// DEBUG only: the Design System Gallery (Debug → Design System Gallery).
/// Every primitive, every StateMark, the type scale, spacing, radii and
/// both densities, SwiftUI and AppKit side by side, for screenshot review.
@MainActor
enum DesignGallery {
    private static var window: NSWindow?

    static func show() {
        if window == nil {
            let w = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 1040, height: 860),
                             styleMask: [.titled, .closable, .resizable, .miniaturizable], backing: .buffered, defer: false)
            w.title = "Design System Gallery"
            w.isReleasedWhenClosed = false
            w.contentView = NSHostingView(rootView: DesignGalleryView())
            w.setAccessibilityIdentifier("designGallery")
            w.center()
            window = w
        }
        window?.makeKeyAndOrderFront(nil)
    }
}

struct DesignGalleryView: View {
    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: DS.Spacing.xxl) {
                Wordmark(needsYou: true, size: 28)
                section("State marks") { stateMarks }
                section("Type scale") { typeScale }
                section("Spacing") { spacing }
                section("Radius") { radii }
                section("Colors") { colors }
                section("Pill") { pills }
                section("Kbd · IconButton") { kbdAndButtons }
                section("Row") { rows }
                section("Panel") { panels }
                section("Density") { densities }
                section("Motion") { motion }
            }
            .padding(DS.Spacing.xxl)
            .frame(maxWidth: .infinity, alignment: .leading)
        }
        .background(Theme.color(.background))
    }

    private func section<C: View>(_ title: String, @ViewBuilder _ content: () -> C) -> some View {
        VStack(alignment: .leading, spacing: DS.Spacing.l) {
            Text(title).font(DS.font(.panelTitle, .semibold)).foregroundStyle(Theme.fg)
            content()
        }
    }

    private func caption(_ s: String) -> some View { Text(s).font(DS.font(.meta)).foregroundStyle(Theme.dim) }

    private var stateMarks: some View {
        VStack(alignment: .leading, spacing: DS.Spacing.m) {
            HStack(spacing: DS.Spacing.xl) {
                ForEach(StateMarkKind.allCases, id: \.self) { k in
                    VStack(spacing: DS.Spacing.s) { StateMark(k); caption(k.label) }
                }
            }
            HStack(spacing: DS.Spacing.xl) {
                caption("AppKit")
                ForEach(StateMarkKind.allCases, id: \.self) { k in
                    AppKitBox { StateMarkView(k) }.frame(width: 12, height: 12)
                }
                caption("menu image")
                ForEach(StateMarkKind.allCases, id: \.self) { k in Image(nsImage: StateMark.image(k)) }
            }
            HStack(spacing: DS.Spacing.xl) {
                caption("AgentState →")
                ForEach(AgentState.allCases, id: \.self) { s in
                    HStack(spacing: DS.Spacing.xs) { StateMark(state: s); caption(s.rawValue) }
                }
            }
        }
    }

    private var typeScale: some View {
        VStack(alignment: .leading, spacing: DS.Spacing.s) {
            ForEach(DS.TextStyle.allCases, id: \.self) { t in
                HStack(alignment: .firstTextBaseline, spacing: DS.Spacing.l) {
                    caption("\(t.rawValue) \(Int(t.size))").frame(width: 120, alignment: .leading)
                    ForEach(DS.Weight.allCases, id: \.self) { w in
                        Text("Hesper \(w.rawValue)").font(DS.font(t, w)).foregroundStyle(Theme.fg)
                    }
                }
            }
        }
    }

    private var spacing: some View {
        HStack(alignment: .bottom, spacing: DS.Spacing.l) {
            ForEach(DesignTokens.Spacing.all, id: \.self) { v in
                VStack(spacing: DS.Spacing.xs) {
                    Rectangle().fill(Theme.accent).frame(width: CGFloat(v), height: CGFloat(v))
                    caption("\(Int(v))")
                }
            }
        }
    }

    private var radii: some View {
        HStack(spacing: DS.Spacing.l) {
            ForEach([("kbd", DS.Radius.kbd), ("control", DS.Radius.control), ("tile", DS.Radius.tile), ("panel", DS.Radius.panel)], id: \.0) { name, r in
                VStack(spacing: DS.Spacing.xs) {
                    DS.Radius.shape(r).fill(Theme.surface).overlay(DS.Radius.shape(r).strokeBorder(Theme.stroke)).frame(width: 64, height: 40)
                    caption("\(name) \(Int(r))")
                }
            }
        }
    }

    private var colors: some View {
        LazyVGrid(columns: Array(repeating: GridItem(.fixed(72), spacing: DS.Spacing.m), count: 7), alignment: .leading, spacing: DS.Spacing.m) {
            ForEach(Theme.Token.allCases, id: \.self) { t in
                VStack(spacing: DS.Spacing.xs) {
                    DS.Radius.shape(DS.Radius.kbd).fill(Theme.color(t)).overlay(DS.Radius.shape(DS.Radius.kbd).strokeBorder(Theme.stroke)).frame(height: 28)
                    caption(String(describing: t))
                }
            }
        }
    }

    private var pills: some View {
        VStack(alignment: .leading, spacing: DS.Spacing.m) {
            HStack(spacing: DS.Spacing.xs) {
                Pill("All", variant: .segment(selected: true), count: 7)
                Pill("Needs you", variant: .segment(selected: false))
                Pill("Working", variant: .segment(selected: false))
            }
            HStack(spacing: DS.Spacing.m) {
                Pill("2 need you", mark: .needsYou)
                Pill("question", mark: .question)
                Pill("3 working", mark: .working)
                Pill("mini")
                Pill("", variant: .count, count: 12)
                Pill("Search", variant: .segment(selected: false), kbd: "⌘K")
            }
            HStack(spacing: DS.Spacing.m) {
                caption("AppKit")
                AppKitBox { PillView("All", variant: .segment(selected: true), count: 7) }.frame(width: 70, height: Pill.height)
                AppKitBox { PillView("2 need you", mark: .needsYou) }.frame(width: 110, height: Pill.height)
                AppKitBox { PillView("12", variant: .count) }.frame(width: 40, height: Pill.height)
            }
        }
    }

    private var kbdAndButtons: some View {
        HStack(spacing: DS.Spacing.l) {
            Kbd("⌘K"); Kbd("⏎"); Kbd("⌥⌘←")
            AppKitBox { KbdView("⌘J") }.frame(width: 32, height: 18)
            IconButton("magnifyingglass", help: "Search", shortcut: "⌘K") {}
            IconButton("plus", help: "New agent", shortcut: "⌘N") {}
            AppKitBox { IconButtonView(symbol: "clock", help: "History", shortcut: "⌘Y", target: nil, action: nil) }
                .frame(width: IconButton.side, height: IconButton.side)
        }
    }

    private var rows: some View {
        HStack(alignment: .top, spacing: DS.Spacing.xl) {
            VStack(spacing: DS.Spacing.xxs) {
                Row(title: "migrations", subtitle: "Allow edit to 0042_users.sql?", meta: "mini · 2m", mark: .needsYou, kbd: "⏎", selected: true)
                Row(title: "tone", meta: "laptop", mark: .question)
                Row(title: "auth refactor", meta: "mini · 2h", mark: .done)
                Row(title: "session bugs", meta: "laptop · 3d", mark: .exited)
            }
            .frame(width: 360)
            VStack(spacing: DS.Spacing.xxs) {
                AppKitBox { RowView(title: "migrations", subtitle: "Allow edit to 0042_users.sql?", meta: "mini · 2m", mark: .needsYou, kbd: "⏎") }
                    .frame(height: Row.height(subtitle: true))
                AppKitBox { RowView(title: "api", meta: "3", mark: .working) }.frame(height: Row.height(subtitle: false))
            }
            .frame(width: 360)
        }
    }

    private var panels: some View {
        HStack(alignment: .top, spacing: DS.Spacing.xl) {
            Panel {
                VStack(alignment: .leading, spacing: DS.Spacing.s) {
                    HStack { Text("Needs you · 2").font(DS.font(.body, .semibold)).foregroundStyle(Theme.fg); Spacer(); Kbd("⌘J") }
                    Row(title: "api · migrations", meta: "mini", mark: .needsYou)
                    Row(title: "docs · tone", meta: "laptop", mark: .question)
                }
                .frame(width: 300)
            }
            AppKitBox {
                let p = PanelView(frame: NSRect(x: 0, y: 0, width: 300, height: 120))
                let r = RowView(title: "AppKit panel row", meta: "⌘K", mark: .working)
                r.frame = NSRect(x: DS.Spacing.l, y: DS.Spacing.l, width: 300 - 2 * DS.Spacing.l, height: Row.height(subtitle: false))
                p.contentView.addSubview(r)
                return p
            }
            .frame(width: 300, height: 120)
        }
        .padding(DS.Spacing.xl)
    }

    private var densities: some View {
        HStack(alignment: .top, spacing: DS.Spacing.xl) {
            ForEach(DS.Density.allCases, id: \.self) { d in
                VStack(alignment: .leading, spacing: DS.Spacing.s) {
                    caption("\(d.label): header \(Int(d.tileHeader)) · gutter \(Int(d.tileGutter)) · band gap \(Int(d.bandGap))")
                    HStack(spacing: CGFloat(d.tileGutter)) {
                        ForEach([StateMarkKind.working, .needsYou], id: \.self) { k in tileMock(d, k) }
                    }
                }
            }
        }
    }

    private func tileMock(_ d: DS.Density, _ k: StateMarkKind) -> some View {
        VStack(spacing: 0) {
            HStack(spacing: DS.Spacing.m) {
                StateMark(k)
                Text("migrations").font(DS.font(.chrome, .semibold)).foregroundStyle(Theme.fg)
                Spacer()
                Text("mini · claude").font(DS.font(.meta)).foregroundStyle(Theme.dim)
            }
            .padding(.horizontal, DS.Spacing.m)
            .frame(height: CGFloat(d.tileHeader))
            .background(Theme.surface)
            Rectangle().fill(Color(nsColor: DS.terminalBackground)).frame(height: 90)
        }
        .frame(width: 220)
        .clipShape(DS.Radius.shape(DS.Radius.tile))
        .overlay(DS.Radius.shape(DS.Radius.tile).strokeBorder(k == .needsYou ? Theme.color(.signal) : Theme.stroke))
    }

    @State private var moved = false

    private var motion: some View {
        HStack(spacing: DS.Spacing.l) {
            ForEach(DS.Motion.allCases, id: \.self) { m in
                Button("\(m.rawValue) \(Int(m.duration * 1000)) ms") { DS.withMotion(m) { moved.toggle() } }
            }
            Rectangle().fill(Theme.accent).frame(width: 12, height: 12).offset(x: moved ? 120 : 0)
            caption("reduce motion: \(DS.reduceMotion ? "on" : "off") · reduce transparency: \(DS.reduceTransparency ? "on" : "off")")
        }
    }
}

/// Hosts an AppKit primitive in the gallery.
private struct AppKitBox<V: NSView>: NSViewRepresentable {
    var make: () -> V
    init(_ make: @escaping () -> V) { self.make = make }
    func makeNSView(context: Context) -> V { make() }
    func updateNSView(_ v: V, context: Context) {}
}
#endif
