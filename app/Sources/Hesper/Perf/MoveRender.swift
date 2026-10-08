import AppKit
import HesperCore
import SwiftUI

/// Dev tool: `Hesper --render-move <dir>` draws moving work across Macs
/// offscreen (no window, no daemon) into PNGs, Dusk and Daylight: the
/// tile's progress line (sent, transfer 42 %, resuming), the preflight
/// strips (busy, processes, tool missing, no remote, offline, too large),
/// a finished tile offering "Continue on mini · Fork on mini", and the
/// move toast; then exits.
@MainActor
enum MoveRender {
    static let tileWidth = CloseRender.tileWidth

    static func run(_ dir: String) {
        try? FileManager.default.createDirectory(atPath: dir, withIntermediateDirectories: true)
        let now = Date()
        var migrations = Agent(id: "L/m1", machine: "laptop", kind: "claude", name: "migrations", state: .done, stateSince: now.addingTimeInterval(-120),
                               summary: "Added users.locale; 3 files changed")
        migrations.project = "/Users/me/projects/api"
        migrations.branch = "users-col"
        var busy = migrations
        busy.state = .working
        busy.activity = "Bash: npm test"
        let id = migrations.id
        let processes = MovePreflight.processes([MoveProcess(pid: 4121, command: "/usr/local/bin/npm run dev"),
                                                 MoveProcess(pid: 4188, command: "node server.js --port 5173")])
        let asks: [(String, Agent, MovePreflight)] = [
            ("busy", busy, .busy), ("processes", migrations, processes), ("tool-missing", migrations, .toolMissing("codex")),
            ("no-remote", migrations, .noRemote), ("offline", migrations, .offline), ("too-large", migrations, .tooLarge),
        ]
        for (scheme, appearance) in [("dusk", NSAppearance.Name.darkAqua), ("daylight", .aqua)] {
            let ap = NSAppearance(named: appearance)!
            func out(_ name: String) -> String { (dir as NSString).appendingPathComponent("\(name)-\(scheme).png") }
            for (name, p) in [("sent", MoveProgress(id: id, to: "mini")),
                              ("transfer", MoveProgress(id: id, to: "mini", step: .transfer, percent: 42)),
                              ("resuming", MoveProgress(id: id, to: "mini", step: .resume))] {
                let line = AttentionBar.MoveLine(progress: p, target: "mini")
                CloseRender.render(tile(migrations, moving: "mini", line: line), width: tileWidth, appearance: ap, to: out("tile-progress-\(name)"))
            }
            CloseRender.render(tile(migrations, moving: "mini", line: AttentionBar.MoveLine(progress: MoveProgress(id: id, to: "mini", step: .transfer, percent: 42), target: "mini"), width: 380),
                               width: 380, appearance: ap, to: out("tile-progress-narrow"))
            for (name, a, ask) in asks {
                CloseRender.render(tile(a, strip: strip(ask, a, tight: false)), width: tileWidth, appearance: ap, to: out("tile-preflight-\(name)"))
            }
            CloseRender.render(tile(migrations, strip: strip(processes, migrations, tight: true), width: 380), width: 380, appearance: ap, to: out("tile-preflight-processes-narrow"))
            CloseRender.render(tile(migrations, offer: AttentionBar.MoveOffer(target: "mini", onMove: {}, onFork: {})), width: tileWidth, appearance: ap,
                               to: out("tile-offer"))
            CloseRender.render(toast(), width: 520, appearance: ap, to: out("toast"))
        }
    }

    static func strip(_ ask: MovePreflight, _ a: Agent, tight: Bool) -> AttentionBar.MoveStrip {
        AttentionBar.MoveStrip(message: ask.message(name: a.name, target: "mini", project: "api"), primary: ask.primary(tight: tight)?.title,
                               detail: ask.detail, actionable: ask.actionable, onPrimary: {}, onCancel: {})
    }

    /// A wall card: header, a terminal's last lines, the band.
    static func tile(_ a: Agent, moving: String? = nil, line: AttentionBar.MoveLine? = nil, strip: AttentionBar.MoveStrip? = nil,
                     offer: AttentionBar.MoveOffer? = nil, width w: CGFloat = tileWidth) -> some View {
        let bandH = max(Metrics.footer, AttentionBar.height(a, width: w, confirming: strip != nil))
        return VStack(spacing: 0) {
            TileHeader(agent: a, local: true, movingTo: moving, projectName: "api", machineLabel: "laptop")
                .frame(height: DS.tileHeader)
            Rectangle().fill(Color(nsColor: Theme.hairline)).frame(height: 1)
            ZStack(alignment: .bottomLeading) {
                Color(nsColor: DS.terminalBackground)
                Text("⏺ Update(db/migrations/0042_users.sql)\n  ⎿ Add users.locale column with default 'en'\n> ")
                    .font(DS.monoFont(.chrome))
                    .foregroundStyle(Color(white: 0.78))
                    .padding(DS.Spacing.m)
            }
            .frame(height: CloseRender.terminalHeight)
            AttentionBar(agent: a, wide: AttentionBar.isWide(a, width: w), tight: AttentionBar.isTight(width: w), keyHints: true,
                         onAnswer: { _ in }, onDenyMessage: {}, onOpen: {}, onResume: {},
                         moveStrip: strip, moveProgress: line, moveOffer: offer)
                .frame(height: bandH)
        }
        .background(Theme.color(.tile))
        .clipShape(DS.Radius.shape(DS.Radius.tile))
        .overlay(DS.Radius.shape(DS.Radius.tile).strokeBorder(strip != nil ? Theme.color(.working).opacity(0.8) : Theme.stroke, lineWidth: strip != nil ? 2 : 1))
        .opacity(moving != nil ? TileView.movingAlpha : 1)
        .padding(DS.Spacing.xl)
        .background(Theme.color(.background))
    }

    static func toast() -> some View {
        UndoToast(label: MoveText.moved("migrations", to: "mini"), reopen: false, onUndo: {})
            .padding(DS.Spacing.xl)
            .frame(maxWidth: .infinity)
            .background(Theme.color(.background))
    }
}
