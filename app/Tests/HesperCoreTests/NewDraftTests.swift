import Foundation
import Testing
@testable import HesperCore

/// New drafts have no project (docs/rebuild-contract.md "As built —
/// drafts" › "New drafts have no project"): the "New" area, where an
/// explicit project action's draft runs, a folder change, a missing folder.
@Suite struct NewDrafts {
    static let catalog = ProjectCatalog(projects: [
        Project(id: "as", name: "acme-apps", kind: .repo, paths: ["L": "/p/acme-apps", "M": "/m/acme-apps"], groups: ["g1"]),
        Project(id: "mini", name: "mini-only", kind: .repo, paths: ["M": "/m/mini-only"]),
        Project(id: "dm", name: "default-mini", kind: .repo, paths: ["L": "/p/dm", "M": "/m/dm"], defaults: ProjectDefaults(machine: "M")),
        Project(id: "gh", name: "hesper", kind: .repo, paths: ["L": "/p/hesper"], groups: ["g2"]),
    ], groups: [
        ProjectGroup(id: "g1", name: "acme apps", projectIds: ["as"], order: 0),
        ProjectGroup(id: "g2", name: "tools", projectIds: ["gh"], order: 1),
    ])
    var c: ProjectCatalog { Self.catalog }

    // MARK: The "New" area

    @Test func newDraftsGetTheirOwnFirstBandWithoutProjectColor() {
        let items = [
            ViewItem(id: "a1", projectID: "as"), ViewItem(id: "g1", projectID: "gh"),
            ViewItem(id: "d-new", projectID: nil, isNew: true), ViewItem(id: "a2", projectID: "as"),
        ]
        for level in [GroupLevel.group, .project] {
            let v = ViewResolver.resolve(items, level: level, catalog: c, bandOrder: ["g:g2", "p:gh"])
            #expect(v.showsBands, "\(level)")
            #expect(v.bands.first?.key == ViewResolver.newKey && v.bands.first?.title == "New", "\(level): New is first, even before the wall's own order")
            #expect(v.bands.first?.members == ["d-new"] && v.bands.first?.projectID == nil && v.bands.first?.groupID == nil)
            #expect(v.bands.first?.colorHex == ViewResolver.newColor && v.bands.first?.wallScope == nil, "no project color, no ↗")
            #expect(!v.bands.dropFirst().contains { $0.members.contains("d-new") }, "never inside a project's band")
        }
    }

    @Test func oneProjectAndANewDraftIsStillThePlainWall() {
        let items = [ViewItem(id: "a1", projectID: "as"), ViewItem(id: "d-new", projectID: nil, isNew: true), ViewItem(id: "a2", projectID: "as")]
        let v = ViewResolver.resolve(items, level: .group, catalog: c)
        #expect(!v.showsBands, "New doesn't count: the draft sits in the wall order (right of the selected card)")
        #expect(ViewResolver.resolve(items, level: .none, catalog: c).order == ["a1", "d-new", "a2"])
    }

    @Test func aBandDraftStaysInItsBand() {
        // ＋ in a band: the draft carries the band's project (draftBands).
        let items = [ViewItem(id: "a1", projectID: "as"), ViewItem(id: "g1", projectID: "gh"), ViewItem(id: "d-band", projectID: "gh")]
        let v = ViewResolver.resolve(items, level: .group, catalog: c)
        #expect(v.band(of: "d-band")?.key == "g:g2" && v.band(key: ViewResolver.newKey) == nil)
    }

    // MARK: Where an explicit project's draft runs

    @Test func placeKeepsMachineAndFolderTogether() {
        // This Mac has the folder: here.
        #expect(DraftSeed.place(project: "as", catalog: c, local: "L") == (nil, "/p/acme-apps"))
        // Only the mini has it: the mini, with the mini's folder — never
        // this Mac with the mini's path (hesperd: "no directory").
        #expect(DraftSeed.place(project: "mini", catalog: c, local: "L") == ("M", "/m/mini-only"))
        // The project's default machine.
        #expect(DraftSeed.place(project: "dm", catalog: c, local: "L") == ("M", "/m/dm"))
        // An explicit machine wins when the project is there.
        #expect(DraftSeed.place(project: "as", catalog: c, local: "L", prefer: "M") == ("M", "/m/acme-apps"))
        #expect(DraftSeed.place(project: "gh", catalog: c, local: "L", prefer: "M") == (nil, "/p/hesper"), "not there: where it is")
        // A folder without daemon data; no project at all.
        #expect(DraftSeed.place(project: "scratch:/tmp/x", catalog: c, local: "L") == (nil, "/tmp/x"))
        #expect(DraftSeed.place(project: nil, catalog: c, local: "L") == (nil, nil))
    }

    // MARK: Changing the folder

    @Test func aFolderChangeFollowsSilentlyAndADefaultMachineComesHome() {
        let band = Draft(text: "x", machine: "M", project: "/m/dm") // ＋ in a band whose default machine is the mini
        let moved = DraftSeed.folderChanged(band, to: "/p/hesper", machinePinned: false)
        #expect(moved.project == "/p/hesper" && moved.machine == nil, "this Mac's folder list → this Mac")
        let pinned = DraftSeed.folderChanged(band, to: "/p/hesper", machinePinned: true)
        #expect(pinned.project == "/p/hesper" && pinned.machine == "M", "a machine the user chose stays")
    }

    @Test func aTokenNamesTheChosenFolderNotTheFirstOfThatName() {
        let ctx = ComposerContext(projects: [
            .init(path: "/old/clone/app", recent: true),
            .init(path: "/p/app", recent: false),
        ])
        // Chosen in the list (the second "app"): the token "#app" keeps it.
        let r = ComposerResolution.make(text: "fix #app", draft: DraftChoices(project: "/p/app"), context: ctx)
        #expect(r.project == "/p/app")
        // Typed with no choice yet: the first by name, as before.
        #expect(ComposerResolution.make(text: "fix #app", draft: DraftChoices(), context: ctx).project == "/old/clone/app")
        // Another name: the token wins.
        let other = ComposerContext(projects: [.init(path: "/p/app", recent: true), .init(path: "/p/web", recent: true)])
        #expect(ComposerResolution.make(text: "fix #web", draft: DraftChoices(project: "/p/app"), context: other).project == "/p/web")
    }

    // MARK: A missing folder

    @Test func onlyAMissingLocalFolderIsReported() {
        let exists: (String) -> Bool = { $0 == "/p/there" }
        #expect(DraftSeed.missingFolder(project: "/p/new", machine: "L", local: "L", exists: exists) == "/p/new")
        #expect(DraftSeed.missingFolder(project: "/p/there", machine: "L", local: "L", exists: exists) == nil)
        #expect(DraftSeed.missingFolder(project: "/m/x", machine: "M", local: "L", exists: exists) == nil, "another Mac: not checkable here")
        #expect(DraftSeed.missingFolder(project: nil, machine: "L", local: "L", exists: exists) == nil, "no folder: 'Choose folder', not missing")
    }

    @Test func typedFolders() {
        #expect(DraftSeed.typedFolder("~/projects/new-thing", home: "/Users/o") == "/Users/o/projects/new-thing")
        #expect(DraftSeed.typedFolder("/tmp/x/../y") == "/tmp/y")
        #expect(DraftSeed.typedFolder("hesper") == nil && DraftSeed.typedFolder("/") == nil)
    }

    // MARK: Persistence

    @Test func aDraftWithoutProjectSavesNoProject() throws {
        let d = Draft(id: "d-abc", text: "later")
        let json = String(data: try JSONEncoder().encode(d), encoding: .utf8) ?? ""
        #expect(!json.contains("project") && !json.contains("machine"))
        let back = try JSONDecoder().decode(Draft.self, from: Data(#"{"id":"d-abc","text":"later","project":"","machine":""}"#.utf8))
        #expect(back.project == nil && back.machine == nil, "empty means none (restored into the New area)")
    }
}

/// A draft opened with a band's ＋ keeps its project through a save and a
/// reload (the daemon's echo, another window, a restart): it never falls
/// back into the "New" area.
@Suite struct DraftBandPersistence {
    @Test func theBandSurvivesEncodingAndTheDaemonsEcho() throws {
        var d = Draft(id: "d-band1", text: "fix it")
        d.band = "p-nmt"
        let data = try JSONEncoder().encode(d)
        #expect(String(decoding: data, as: UTF8.self).contains("\"band\":\"p-nmt\""))
        let back = try JSONDecoder().decode(Draft.self, from: data)
        #expect(back.band == "p-nmt" && back.text == d.text)
        var book = DraftBook()
        book.edit(Draft(id: "d-band1", text: "fix it"))
        _ = book.flushAll()
        book.saved(back)
        _ = book.applyRemote(back)
        #expect(book["d-band1"]?.band == "p-nmt")
    }

    @Test func aDraftWithoutABandStaysInNew() throws {
        let old = try JSONDecoder().decode(Draft.self, from: Data(#"{"id":"d-old","text":"x","band":""}"#.utf8))
        #expect(old.band == nil)
    }
}
