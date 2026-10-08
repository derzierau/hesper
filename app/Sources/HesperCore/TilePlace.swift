import Foundation

/// What a tile header says about where its agent works ("project ·
/// branch"), without repeating the band it sits in: inside its own
/// project's band the project is already the heading, so only a branch
/// that isn't the default (or the band's own) is left.
public enum TilePlace {
    /// Branches that go without saying.
    public static let defaultBranches: Set<String> = ["main", "master"]

    /// Whether a card sits in its own project's band: a project band of
    /// the project (or its root), or any band of a project-scoped wall
    /// (branches, packages). Group bands, "No project" and the New area
    /// are mixed: the project stays on the card.
    public static func inOwnProjectBand(projectID: String?, band: Band?, catalog: ProjectCatalog) -> Bool {
        guard let pid = projectID, let band, !band.isNew, band.pointer == nil else { return false }
        switch band.level {
        case .project: return band.projectID == (catalog.root(pid) ?? pid)
        case .branch: return band.projectID == pid || band.projectID == catalog.root(pid)
        case .group, .none: return false
        }
    }

    /// The header's place segment. `ownBand`: the card is in its
    /// project's band; `bandBranch`: the branch that band shows (branch
    /// level).
    public static func label(project: String?, branch: String?, ownBand: Bool, bandBranch: String? = nil) -> String? {
        let b = branch.flatMap { $0.isEmpty ? nil : $0 }
        if ownBand {
            guard let b, !defaultBranches.contains(b), b != bandBranch else { return nil }
            return b
        }
        switch (project, b) {
        case let (p?, b?): return "\(p) · \(b)"
        case let (p?, nil): return p
        case let (nil, b?): return b
        default: return nil
        }
    }
}
