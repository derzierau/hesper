import AppKit
import HesperCore
import SwiftUI

/// Dev tool: `Hesper --render-close <dir>` draws the closing-agents UI
/// offscreen (no window, no daemon) into PNGs, Dusk and Daylight: a tile
/// asking before it closes (needs you, its worktree, a running shell
/// command), a Killed tile, the close toasts, the top bar with the
/// Background tray pill and the tray popover, then exits.
@MainActor
enum CloseRender {
    static let tileWidth: CGFloat = 640
    static let terminalHeight: CGFloat = 120

    static func run(_ dir: String) {
        try? FileManager.default.createDirectory(atPath: dir, withIntermediateDirectories: true)
        let now = Date()
        var migrations = Agent(id: "M/m1", machine: "mini", kind: "claude", name: "migrations", state: .approval, stateSince: now.addingTimeInterval(-90),
                               attention: Attention(kind: "approval", title: "Edit", detail: "Allow edit to 0042_users.sql?"))
        migrations.project = "/Users/me/projects/api"
        var docs = Agent(id: "L/d1", machine: "laptop", kind: "claude", name: "docs pass", state: .done, stateSince: now.addingTimeInterval(-600),
                         summary: "Rewrote the setup guide; 3 files changed")
        docs.worktree = "/Users/me/projects/api-wt/docs-pass"
        var dev = Agent(id: "L/s1", machine: "laptop", kind: "shell", name: "dev server", state: .working, stateSince: now.addingTimeInterval(-1500))
        dev.activity = "npm run dev"
        var killed = Agent(id: "L/k1", machine: "laptop", kind: "codex", name: "flaky e2e", state: .exited, stateSince: now.addingTimeInterval(-20))
        killed.exit = ExitInfo(code: nil, signal: "SIGTERM")
        killed.ended = "killed"

        let bg = [
            { () -> Agent in var a = Agent(id: "L/b1", machine: "laptop", kind: "codex", name: "backfill users", state: .working, created: now.addingTimeInterval(-12 * 60)); a.background = true; return a }(),
            { () -> Agent in var a = Agent(id: "M/b2", machine: "mini", kind: "claude", name: "e2e suite", state: .approval, stateSince: now.addingTimeInterval(-40), attention: Attention(kind: "approval", title: "Bash", detail: "npx playwright test"), created: now.addingTimeInterval(-3 * 60)); a.background = true; return a }(),
        ]

        for (scheme, appearance) in [("dusk", NSAppearance.Name.darkAqua), ("daylight", .aqua)] {
            let ap = NSAppearance(named: appearance)!
            func out(_ name: String) -> String { (dir as NSString).appendingPathComponent("\(name)-\(scheme).png") }
            render(tile(migrations, strip: .needsYou), width: tileWidth, appearance: ap, to: out("tile-confirm-needsyou"))
            render(tile(docs, strip: .worktree(files: 3)), width: tileWidth, appearance: ap, to: out("tile-confirm-worktree"))
            render(tile(dev, strip: .foreground("npm run dev")), width: tileWidth, appearance: ap, to: out("tile-confirm-shell"))
            render(tile(killed, strip: nil), width: tileWidth, appearance: ap, to: out("tile-killed"))
            render(tile(migrations, strip: .needsYou), width: 380, appearance: ap, to: out("tile-confirm-needsyou-narrow"))
            render(toasts(), width: 520, appearance: ap, to: out("toasts"))
            render(tray(bg, now: now), width: OverlayLook.backgroundWidth + 2 * DS.Spacing.xl, appearance: ap, to: out("background-tray"))
            var c = StateCounts()
            c.total = 9; c.working = 4; c.approval = 1; c.done = 4
            let machines = [Machine(short: "laptop", name: "ABC123456", online: true, route: "local"),
                            Machine(short: "mini", name: "XYZ987654 Mac mini", online: true, rttMs: 41, route: "relay")]
            for width in [CGFloat(1200), 760] {
                let data = TopBarData(counts: c, machines: machines, background: BackgroundSummary(count: 2, needsYou: 1))
                TopBarRender.render(data, width: width, appearance: appearance, to: out("topbar-background-\(Int(width))"))
            }
        }
    }

    /// A wall card: header, a terminal's last lines, the band (with the
    /// close strip when asked).
    static func tile(_ a: Agent, strip: CloseConfirm?) -> some View {
        let closeStrip = strip.map { AttentionBar.CloseStrip(ask: $0, onPrimary: {}, onSecondary: {}, onCancel: {}) }
        let bandH = max(Metrics.footer, AttentionBar.height(a, width: tileWidth, confirming: strip != nil))
        return VStack(spacing: 0) {
            TileHeader(agent: a, local: a.machine == "laptop", killed: a.isKilled)
                .frame(height: DS.tileHeader)
            Rectangle().fill(Color(nsColor: Theme.hairline)).frame(height: 1)
            ZStack(alignment: .bottomLeading) {
                Color(nsColor: DS.terminalBackground)
                Text(sample(a))
                    .font(DS.monoFont(.chrome))
                    .foregroundStyle(Color(white: 0.78))
                    .padding(DS.Spacing.m)
            }
            .frame(height: terminalHeight)
            AttentionBar(agent: a, wide: AttentionBar.isWide(a, width: tileWidth), keyHints: true,
                         onAnswer: { _ in }, onDenyMessage: {}, onOpen: {}, onResume: {},
                         closeStrip: closeStrip, killed: a.isKilled, onClose: {})
                .frame(height: bandH)
        }
        .background(Theme.color(.tile))
        .clipShape(DS.Radius.shape(DS.Radius.tile))
        .overlay(DS.Radius.shape(DS.Radius.tile).strokeBorder(strip != nil ? Theme.color(.working).opacity(0.8) : Theme.stroke, lineWidth: strip != nil ? 2 : 1))
        .padding(DS.Spacing.xl)
        .background(Theme.color(.background))
    }

    static func sample(_ a: Agent) -> String {
        if a.state == .done { return "⏺ Updated docs/setup.md, docs/relay.md, README.md\n  Rewrote the setup guide.\n> " }
        switch a.kind {
        case "shell": return "$ npm run dev\n  VITE v5.4.2  ready in 412 ms\n  ➜  Local:   http://localhost:5173/"
        case "codex": return "› running e2e/checkout.spec.ts\n  ✗ pays with a saved card (timeout 30000ms)\n^C"
        default: return "⏺ Update(db/migrations/0042_users.sql)\n  ⎿ Add users.locale column with default 'en'\n  Do you want to make this edit?"
        }
    }

    static func toasts() -> some View {
        VStack(spacing: DS.Spacing.l) {
            UndoToast(label: CloseText.closed("api"), reopen: true, onUndo: {})
            UndoToast(label: CloseText.closed("api", queuedFor: "mini"), reopen: true, onUndo: {})
            UndoToast(label: CloseText.tidied(["a", "b", "c"]), reopen: true, onUndo: {})
        }
        .padding(DS.Spacing.xl)
        .frame(maxWidth: .infinity)
        .background(Theme.color(.background))
    }

    static func tray(_ agents: [Agent], now: Date) -> some View {
        let rows = agents.map { BackgroundPanel.RowData(agent: $0, meta: BackgroundTray.meta($0, machine: $0.machine, now: now)) }
        let summary = BackgroundTray.summary(agents, isBackground: { $0.background })
        let ordered = BackgroundTray.agents(agents, isBackground: { $0.background }).compactMap { a in rows.first { $0.id == a.id } }
        return BackgroundPanel(rows: ordered, summary: summary, selected: 0, onBringBack: { _ in }, onClose: { _ in })
            .frame(width: OverlayLook.backgroundWidth, height: BackgroundPanel.height(rows.count))
            .overlayGlass(DS.Radius.shape(DS.Radius.panel))
            .padding(DS.Spacing.xl)
            .background(Theme.color(.background))
    }

    static func render<V: View>(_ view: V, width: CGFloat, appearance: NSAppearance, to path: String) {
        let host = NSHostingView(rootView: view.frame(width: width).fixedSize(horizontal: false, vertical: true))
        host.appearance = appearance
        host.frame = NSRect(x: 0, y: 0, width: width, height: 10)
        for _ in 0..<3 {
            let h = host.fittingSize.height
            host.frame = NSRect(x: 0, y: 0, width: width, height: max(h, 10))
            host.layoutSubtreeIfNeeded()
            RunLoop.current.run(until: Date().addingTimeInterval(0.05))
        }
        guard let rep = host.bitmapImageRepForCachingDisplay(in: host.bounds) else { return }
        host.cacheDisplay(in: host.bounds, to: rep)
        if let png = rep.representation(using: .png, properties: [:]) {
            try? png.write(to: URL(fileURLWithPath: path))
            print("wrote \(path)")
        }
    }
}
