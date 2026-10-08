import Foundation

/// The History panel's left column: the project sidebar's rows (same
/// component, `SidebarRow`) with session counts instead of agents: All,
/// groups → their projects, other projects, then "Scratch" and
/// "Elsewhere" (sessions whose folder is gone).
public enum HistorySidebar {
    public static let scratchRow = "h:scratch"
    public static let elsewhereRow = "h:elsewhere"

    public static func scope(of row: SidebarRow) -> HistoryScope {
        switch row.id {
        case "all": return .all
        case scratchRow: return .scratch
        case elsewhereRow: return .elsewhere
        default: return row.projectID.map(HistoryScope.project) ?? .all
        }
    }

    public static func isCurrent(_ row: SidebarRow, _ scope: HistoryScope) -> Bool { Self.scope(of: row) == scope }

    public static func make(catalog: ProjectCatalog, stats: SessionStats) -> [SidebarSection] {
        // Projects with sessions (the daemon's ids) plus every known project.
        var counts = stats.byProject
        var scratchCount = stats.scratch
        for (pid, n) in stats.byProject where pid.hasPrefix(ProjectCatalog.scratchPrefix) {
            counts.removeValue(forKey: pid)
            scratchCount += stats.scratch == 0 ? n : 0
        }
        // A package's sessions count for its repository too.
        var rolled: [String: Int] = [:]
        for (pid, n) in counts { for p in catalog.lineage(pid) { rolled[p, default: 0] += n } }
        var known = Set(catalog.projects.keys.filter { !$0.hasPrefix(ProjectCatalog.scratchPrefix) && catalog.projects[$0]?.isScratch != true })
        known.formUnion(counts.keys)
        func name(_ pid: String) -> String { catalog.projects[pid]?.name ?? (pid as NSString).lastPathComponent }
        func row(_ pid: String, group: String?, depth: Int) -> SidebarRow {
            SidebarRow(id: (group.map { "g:\($0)/" } ?? "") + "p:" + pid, kind: .project, title: name(pid), colorHex: catalog.colorHex(project: pid),
                       count: rolled[pid] ?? 0, needsYou: false, depth: depth, groupID: group, projectID: pid, scratch: false, scope: .project(pid))
        }
        func sorted(_ ids: [String]) -> [String] { ids.sorted { name($0).lowercased() < name($1).lowercased() } }
        func children(_ pid: String, group: String?, depth: Int) -> [SidebarRow] {
            sorted(known.filter { catalog.projects[$0]?.parentId == pid && (rolled[$0] ?? 0) > 0 }).map { row($0, group: group, depth: depth) }
        }
        let tops = known.filter { catalog.projects[$0]?.parentId == nil || catalog.projects[catalog.projects[$0]!.parentId!] == nil }

        var sections: [SidebarSection] = [SidebarSection(id: "all", title: nil, rows: [
            SidebarRow(id: "all", kind: .all, title: "All sessions", colorHex: "#7aa2f7", count: stats.total, needsYou: false, depth: 0,
                       groupID: nil, projectID: nil, scratch: false, scope: .all),
        ])]
        var grouped: Set<String> = []
        for g in catalog.orderedGroups {
            let members = sorted(tops.filter { catalog.groupIDs(of: $0).contains(g.id) })
            guard !members.isEmpty else { continue }
            var rows: [SidebarRow] = []
            for pid in members {
                grouped.insert(pid)
                rows.append(row(pid, group: g.id, depth: 0))
                rows += children(pid, group: g.id, depth: 1)
            }
            sections.append(SidebarSection(id: "g:" + g.id, title: g.name, rows: rows))
        }
        let loose = sorted(tops.filter { !grouped.contains($0) })
        if !loose.isEmpty {
            sections.append(SidebarSection(id: "projects", title: catalog.hasGroups ? "Other projects" : "Projects",
                                           rows: loose.flatMap { [row($0, group: nil, depth: 0)] + children($0, group: nil, depth: 1) }))
        }
        // Only when the daemon counts them (its search knows the sentinels).
        if scratchCount > 0 {
            sections.append(SidebarSection(id: "scratch", title: "Scratch", rows: [
                SidebarRow(id: scratchRow, kind: .project, title: "scratch folders", colorHex: "#565f89", count: scratchCount, needsYou: false,
                           depth: 0, groupID: nil, projectID: nil, scratch: true, scope: .all),
            ]))
        }
        if stats.elsewhere > 0 {
            sections.append(SidebarSection(id: "elsewhere", title: "Elsewhere", rows: [
                SidebarRow(id: elsewhereRow, kind: .project, title: "folder gone", colorHex: "#414868", count: stats.elsewhere, needsYou: false,
                           depth: 0, groupID: nil, projectID: nil, scratch: false, scope: .all),
            ]))
        }
        return sections
    }
}
