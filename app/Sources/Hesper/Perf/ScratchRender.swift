import AppKit
import HesperCore
import SwiftUI

/// Dev tool: `Hesper --render-scratch <dir>` draws scratch projects'
/// surfaces offscreen (no window, no daemon) into PNGs, Dusk and Daylight:
/// a draft tile without a folder ("New scratch" with the name its task
/// gives it), the project sidebar with an active scratch, the Scratch fold
/// open (a kept one, resting ones) and its archived ones shown, and the
/// "Promote to project" sheet; then exits.
@MainActor
enum ScratchRender {
    static func run(_ dir: String) {
        try? FileManager.default.createDirectory(atPath: dir, withIntermediateDirectories: true)
        let f = SidebarFixture()
        for (scheme, appearance) in [("dusk", NSAppearance.Name.darkAqua), ("daylight", .aqua)] {
            renderDraft(appearance: appearance, width: 760, to: dir, name: "scratch-chip-\(scheme)")
            renderSidebar(f, appearance: appearance, to: dir, name: "scratch-sidebar-\(scheme)")
            renderSidebar(f, appearance: appearance, to: dir, name: "scratch-sidebar-\(scheme)-open") { s in tap(s, .scratch) }
            renderSidebar(f, appearance: appearance, to: dir, name: "scratch-sidebar-\(scheme)-archived") { s in
                tap(s, .scratch)
                tap(s, .showArchived)
            }
            renderPromote(appearance: appearance, to: dir, name: "scratch-promote-\(scheme)")
        }
        renderDraft(appearance: .darkAqua, width: 480, to: dir, name: "scratch-chip-dusk-narrow")
    }

    // MARK: The draft's "New scratch" chip

    static func renderDraft(appearance: NSAppearance.Name, width: CGFloat, to dir: String, name: String) {
        let env = AppEnvironment.resolve(arguments: ["Hesper", "--ephemeral", "--socket", "/dev/null/hesper-render.sock"], environment: [:])
        let model = AppModel(env: env)
        model.ingest(.projectsListed([Project(id: "gh", name: "hesper", kind: .repo, paths: ["L": NSHomeDirectory() + "/projects/hesper"])], []))
        model.scratchBook.lifecycle = true
        model.scratchBook.support.byMachine[model.localMachine] = true
        let d = Draft(id: "d-render-scratch", text: "Clean up the CSV export and dedupe the rows")
        model.drafts.edit(d)
        let tile = DraftTileView(draft: d, model: model)
        tile.appearance = NSAppearance(named: appearance)
        tile.frame = NSRect(x: 0, y: 0, width: width, height: 230)
        tile.isSelected = true
        for _ in 0..<4 {
            tile.apply(d)
            tile.needsLayout = true
            tile.layoutSubtreeIfNeeded()
            RunLoop.current.run(until: Date().addingTimeInterval(0.05))
        }
        write(tile, to: dir, name: name)
    }

    // MARK: The sidebar

    static func tap(_ s: ProjectSidebar, _ kind: ProjectNavRow.Kind) {
        if let v = s.rows.first(where: { if case .nav(let r) = $0.item { return r.kind == kind } else { return false } }) { s.clicked(v.item, []) }
    }

    static func renderSidebar(_ f: SidebarFixture, appearance: NSAppearance.Name, to dir: String, name: String,
                              _ setup: (ProjectSidebar) -> Void = { _ in }) {
        let snap = ProjectSidebar.Snapshot(catalog: f.catalog, agents: f.agents, machines: f.machines, local: "laptop", scope: .all,
                                           historyAvailable: true, now: f.now)
        let s = ProjectSidebar(source: { snap })
        s.appearance = NSAppearance(named: appearance)
        s.frame = NSRect(origin: .zero, size: NSSize(width: ProjectSidebar.width, height: 620))
        s.reload()
        setup(s)
        for _ in 0..<3 {
            s.needsLayout = true
            s.layoutSubtreeIfNeeded()
            RunLoop.current.run(until: Date().addingTimeInterval(0.05))
        }
        write(s, to: dir, name: name)
    }

    /// A few real projects, an active scratch, resting ones (one kept),
    /// two archived.
    struct SidebarFixture {
        let now = Date()
        let machines = [Machine(short: "laptop", name: "ABC123456", online: true, route: "local"),
                        Machine(short: "mini", name: "XYZ987654 Mac mini", online: true, rttMs: 41, route: "relay")]
        var catalog = ProjectCatalog()
        var agents: [Agent] = []

        init() {
            let home = "/Users/me"
            func ago(_ h: Double) -> Date { now.addingTimeInterval(-h * 3600) }
            var ps = [
                Project(id: "gh", name: "hesper", kind: .repo, paths: ["laptop": home + "/projects/hesper"], lastUsed: ago(0.2)),
                Project(id: "news", name: "news-api", kind: .repo, paths: ["laptop": home + "/projects/news-api"], lastUsed: ago(5)),
                Project(id: "ed", name: "design-system", kind: .repo, paths: ["laptop": home + "/projects/design-system"], lastUsed: ago(30)),
            ]
            func scratch(_ id: String, _ name: String, _ date: String, _ st: ScratchState, keep: Bool = false, used: Double, machine: String = "laptop") {
                let folder = st == .archived ? "/scratch/.archive/" : "/scratch/"
                ps.append(Project(id: id, name: name, kind: .scratch, paths: [machine: home + folder + date + "-" + name.replacingOccurrences(of: " ", with: "-")],
                                  lastUsed: ago(used), scratch: ScratchInfo(state: st, keep: keep, home: machine)))
            }
            scratch("s-csv", "csv cleanup", "2026-10-08", .active, used: 0.1)
            scratch("s-font", "font fallback check", "2026-10-07", .resting, keep: true, used: 20)
            scratch("s-relay", "relay latency notes", "2026-10-05", .resting, used: 70, machine: "mini")
            scratch("s-icon", "icon variants", "2026-10-02", .resting, used: 140)
            scratch("s-tmux", "tmux vs hesper", "2026-09-12", .archived, used: 600)
            scratch("s-perf", "perf notes", "2026-09-03", .archived, used: 820)
            catalog = ProjectCatalog(projects: ps)
            func agent(_ id: String, _ pid: String, _ st: AgentState, mins: Double) -> Agent {
                var a = Agent(id: "laptop/\(id)", machine: "laptop", state: st, projectId: pid, branch: "main")
                a.stateSince = now.addingTimeInterval(-mins * 60)
                return a
            }
            agents = [agent("1", "gh", .working, mins: 1), agent("2", "s-csv", .working, mins: 2), agent("3", "s-csv", .done, mins: 9),
                      agent("4", "news", .approval, mins: 3)]
        }
    }

    // MARK: The promote sheet

    static func renderPromote(appearance: NSAppearance.Name, to dir: String, name: String) {
        let view = PromoteScratchSheet(scratchName: "csv cleanup", folder: "/Users/me/scratch/2026-10-08-csv-cleanup", suggested: "csv-cleanup",
                                       createRepo: true, onPromote: { _, _ in }, onCancel: {})
        let host = NSHostingView(rootView: view)
        host.appearance = NSAppearance(named: appearance)
        host.frame = NSRect(origin: .zero, size: host.fittingSize)
        for _ in 0..<3 {
            host.layoutSubtreeIfNeeded()
            RunLoop.current.run(until: Date().addingTimeInterval(0.05))
        }
        write(host, to: dir, name: name)
    }

    static func write(_ v: NSView, to dir: String, name: String) {
        guard let rep = v.bitmapImageRepForCachingDisplay(in: v.bounds) else { return }
        v.cacheDisplay(in: v.bounds, to: rep)
        let path = (dir as NSString).appendingPathComponent(name + ".png")
        if let png = rep.representation(using: .png, properties: [:]) {
            try? png.write(to: URL(fileURLWithPath: path))
            print("wrote \(path)")
        }
    }
}
