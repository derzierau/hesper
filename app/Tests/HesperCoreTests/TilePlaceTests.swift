import Testing
@testable import HesperCore

struct TilePlaceTests {
    static let catalog = ProjectCatalog(projects: [
        Project(id: "api", name: "acme-api", kind: .repo, paths: ["L": "/p/api"], groups: ["g1"]),
        Project(id: "pkg", name: "plugins", kind: .package, parentId: "api", paths: ["L": "/p/api/plugins"]),
        Project(id: "web", name: "web", kind: .repo, paths: ["L": "/p/web"]),
    ], groups: [ProjectGroup(id: "g1", name: "infra", projectIds: ["api"], order: 0)])

    func band(_ level: GroupLevel, project: String?, branch: String? = nil, key: String = "k") -> Band {
        Band(key: key, title: "", subtitle: "", colorHex: "#000000", level: level, groupID: nil, projectID: project, branch: branch, members: [], continued: false)
    }

    @Test func ownProjectBand() {
        let c = Self.catalog
        #expect(TilePlace.inOwnProjectBand(projectID: "api", band: band(.project, project: "api"), catalog: c))
        #expect(TilePlace.inOwnProjectBand(projectID: "pkg", band: band(.project, project: "api"), catalog: c), "a package in its repo's band")
        #expect(!TilePlace.inOwnProjectBand(projectID: "web", band: band(.project, project: "api"), catalog: c))
        #expect(TilePlace.inOwnProjectBand(projectID: "api", band: band(.branch, project: "api", branch: "fix"), catalog: c))
        #expect(!TilePlace.inOwnProjectBand(projectID: "api", band: band(.group, project: "api"), catalog: c), "group bands are mixed")
        #expect(!TilePlace.inOwnProjectBand(projectID: nil, band: band(.project, project: nil), catalog: c), "No project")
        #expect(!TilePlace.inOwnProjectBand(projectID: "api", band: band(.project, project: "api", key: ViewResolver.newKey), catalog: c))
        #expect(!TilePlace.inOwnProjectBand(projectID: "api", band: nil, catalog: c), "no bands drawn")
    }

    @Test func labels() {
        let p = "acme-api"
        // In its band: no project; only a branch that isn't the default.
        #expect(TilePlace.label(project: p, branch: "main", ownBand: true) == nil)
        #expect(TilePlace.label(project: p, branch: nil, ownBand: true) == nil)
        #expect(TilePlace.label(project: p, branch: "fix/tls", ownBand: true) == "fix/tls")
        #expect(TilePlace.label(project: p, branch: "fix/tls", ownBand: true, bandBranch: "fix/tls") == nil)
        // Mixed contexts keep the project.
        #expect(TilePlace.label(project: p, branch: "main", ownBand: false) == "\(p) · main")
        #expect(TilePlace.label(project: p, branch: "", ownBand: false) == p)
        #expect(TilePlace.label(project: nil, branch: "dev", ownBand: false) == "dev")
        #expect(TilePlace.label(project: nil, branch: nil, ownBand: false) == nil)
    }
}
