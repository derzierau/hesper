import AppKit
import HesperCore

/// Dev tool: `Hesper --render-sidebar <dir>` draws the project sidebar
/// offscreen (no window, no daemon) into PNGs, Dusk and Daylight, at rest,
/// with a group open and a quiet project selected, in the full list and
/// filtering "news", from a catalog like a real one (≈45 projects with
/// date-prefixed scratch folders, worktrees and duplicate names; 10 agents
/// on two Macs, 2 needing you), then exits.
@MainActor
enum SidebarRender {
    static let size = NSSize(width: ProjectSidebar.width, height: 900)

    static func run(_ dir: String) {
        try? FileManager.default.createDirectory(atPath: dir, withIntermediateDirectories: true)
        let f = Fixture()
        for (scheme, appearance) in [("dusk", NSAppearance.Name.darkAqua), ("daylight", .aqua)] {
            render(f, scope: .all, appearance: appearance, to: dir, name: "sidebar-\(scheme)")
            render(f, scope: .project("ed"), appearance: appearance, to: dir, name: "sidebar-\(scheme)-recent-selected") { s in
                s.setGroup("g-acme", expanded: true)
            }
            render(f, scope: .all, appearance: appearance, to: dir, name: "sidebar-\(scheme)-search-news") { s in s.setQuery("news") }
            render(f, scope: .all, appearance: appearance, to: dir, name: "sidebar-\(scheme)-all-projects") { s in
                if let more = s.rows.first(where: { if case .nav(let r) = $0.item { return r.kind == .allProjects } else { return false } }) {
                    s.clicked(more.item, [])
                }
            }
        }
        let old = DS.density
        DS.density = .compact
        render(f, scope: .all, appearance: .darkAqua, to: dir, name: "sidebar-dusk-compact")
        DS.density = old
    }

    static func render(_ f: Fixture, scope: WallScope, appearance: NSAppearance.Name, to dir: String, name: String,
                       _ setup: (ProjectSidebar) -> Void = { _ in }) {
        let snap = ProjectSidebar.Snapshot(catalog: f.catalog, agents: f.agents, machines: f.machines, local: "laptop", scope: scope,
                                           historyAvailable: true, now: f.now)
        let s = ProjectSidebar(source: { snap })
        s.appearance = NSAppearance(named: appearance)
        s.frame = NSRect(origin: .zero, size: size)
        s.reload()
        setup(s)
        for _ in 0..<3 {
            s.needsLayout = true
            s.layoutSubtreeIfNeeded()
            RunLoop.current.run(until: Date().addingTimeInterval(0.05))
        }
        guard let rep = s.bitmapImageRepForCachingDisplay(in: s.bounds) else { return }
        s.cacheDisplay(in: s.bounds, to: rep)
        let path = (dir as NSString).appendingPathComponent(name + ".png")
        if let png = rep.representation(using: .png, properties: [:]) {
            try? png.write(to: URL(fileURLWithPath: path))
            print("wrote \(path)")
        }
    }

    /// A catalog shaped like the user's: real repos, date-prefixed scratch
    /// folders, agent worktrees, duplicates across Macs and folders.
    struct Fixture {
        let now = Date()
        let machines = [Machine(short: "laptop", name: "ABC123456", online: true, route: "local"),
                        Machine(short: "mini", name: "XYZ987654 Mac mini", online: true, rttMs: 41, route: "relay")]
        var catalog = ProjectCatalog()
        var agents: [Agent] = []

        init() {
            let home = "/Users/me"
            func ago(_ h: Double) -> Date { now.addingTimeInterval(-h * 3600) }
            var ps: [Project] = []
            func repo(_ id: String, _ name: String, _ path: String, machine: String = "laptop", used: Double? = nil, identity: String? = nil,
                      groups: [String] = [], parent: String? = nil, kind: ProjectKind = .repo) {
                ps.append(Project(id: id, name: name, kind: kind, identity: identity, parentId: parent, paths: [machine: home + path], groups: groups,
                                  lastUsed: used.map(ago)))
            }
            repo("gh", "hesper", "/projects/hesper", used: 0.2, identity: "github.com/example/hesper", groups: ["g-tools"])
            repo("news", "news-api", "/projects/news-api", used: 1, identity: "github.com/example/news-api")
            repo("as", "acme-apps", "/projects/acme-apps", used: 30, groups: ["g-acme"])
            repo("ios", "ios-app", "/projects/acme-apps/apps/ios-app", parent: "as", kind: .package)
            repo("ed", "design-system", "/projects/design-system", used: 2, groups: ["g-acme"])
            repo("uw", "acme-web", "/projects/acme-web", used: 50, groups: ["g-acme"])
            repo("ch", "contenthub", "/projects/contenthub", used: 74)
            repo("ring", "news-cms", "/projects/news-cms", used: 120)
            repo("dot", "dotfiles", "/dotfiles", used: 400)
            repo("t3a", "t3code", "/projects/t3code", used: 90)
            repo("t3b", "t3code", "/projects/t3code", machine: "mini", used: 5)
            repo("hoa", "ghosty-handoff-test", "/projects/ghosty-handoff-test", used: 300)
            repo("hob", "ghosty-handoff-test", "/old/ghosty-handoff-test")
            for (i, n) in ["getty-sync", "xandr-report", "gam-tools", "victoria-dash", "relay", "hesper-site", "brand-kit", "aerie",
                           "ads-ml", "bq-notebooks", "sso-proxy", "slack-bot", "mcp-acme", "infra-live", "terraform-modules", "ios-widgets",
                           "android-app", "kotlin-sdk", "design-tokens", "docs-site", "playground"].enumerated() {
                repo("r\(i)", n, "/projects/" + n, used: Double(200 + i * 40))
            }
            for (i, n) in ["2026-10-04-scratch-check-the-fonts", "2026-10-06-please-understand-the-relay-handoff", "2026-10-05-try-libghostty",
                           "2026-10-02-perf-notes", "2026-09-28-scratch", "2026-10-07-icon-variants", "2026-10-03-ghostty-config-diff",
                           "2026-10-01-tmux-vs-hesper"].enumerated() {
                repo("s\(i)", n, "/scratch/" + n, used: Double(6 + i * 9), identity: i == 1 ? "github.com/example/news-api" : nil, kind: .folder)
            }
            for (i, n) in ["agent-a81c16ae35bb85273", "agent-afcdbc5d1b14bc5b9", "agent-a2da373525737e01e"].enumerated() {
                repo("wt\(i)", n, "/projects/hesper/.claude/worktrees/" + n, used: Double(1 + i), identity: "github.com/example/hesper")
            }
            catalog = ProjectCatalog(projects: ps, groups: [
                ProjectGroup(id: "g-acme", name: "acme apps", projectIds: ["as", "ed", "uw"], order: 0),
                ProjectGroup(id: "g-tools", name: "tools", projectIds: ["gh"], order: 1),
            ])
            func agent(_ id: String, _ pid: String, _ m: String, _ st: AgentState, mins: Double, branch: String? = "main") -> Agent {
                var a = Agent(id: "\(m)/\(id)", machine: m, state: st, projectId: pid, branch: branch)
                a.stateSince = now.addingTimeInterval(-mins * 60)
                return a
            }
            agents = [
                agent("1", "gh", "laptop", .working, mins: 1),
                agent("2", "gh", "laptop", .done, mins: 12),
                agent("3", "wt0", "laptop", .working, mins: 3, branch: "worktree-agent-a81c"),
                agent("4", "news", "mini", .approval, mins: 2),
                agent("5", "news", "mini", .working, mins: 4),
                agent("6", "as", "laptop", .question, mins: 6, branch: "feature/push-provider-fcm"),
                agent("7", "ios", "laptop", .working, mins: 8, branch: "feature/push-provider-fcm"),
                agent("8", "t3b", "mini", .done, mins: 40),
                agent("9", "s1", "laptop", .working, mins: 9),
                agent("10", "ch", "mini", .done, mins: 25, branch: "fix/feed-dedupe"),
            ]
        }
    }
}
