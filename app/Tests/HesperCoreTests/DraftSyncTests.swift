import Foundation
import Testing
@testable import HesperCore

/// Machine and folder agree (docs/rebuild-contract.md "As built — drafts" ›
/// "Machine and folder agree"): the live bug's three drafts (an old
/// machine:mini draft with a laptop-only folder; a #token naming another
/// folder than the stored project; an explicit mini choice), the
/// machineExplicit migration, the chip row's note and the machine chip.
@Suite struct DraftSyncs {
    static let home = "/Users/o/projects"
    static let apiDir = home + "/acme-api"
    static let hesper = home + "/hesper"
    static let catalog = ProjectCatalog(projects: [
        Project(id: "gh", name: "hesper", kind: .repo, paths: ["L": hesper, "M": "/Users/mini/projects/hesper"]),
        Project(id: "mo", name: "mini-only", kind: .repo, paths: ["M": "/Users/mini/projects/mini-only"]),
    ])
    static let ctx = ComposerContext(machines: [Machine(short: "L", name: "laptop", online: true, rttMs: 0, route: "local"),
                                                Machine(short: "M", name: "mini", online: true, rttMs: 41, route: "relay")],
                                     localMachine: "L",
                                     projects: [.init(path: hesper, recent: true), .init(path: apiDir, recent: false)])
    /// This Mac's folders.
    static func here(_ p: String) -> Bool { [apiDir, hesper].contains(p) }

    func normalize(_ d: Draft, previous: String? = nil) -> Draft {
        DraftSync.normalize(d, previousText: previous, context: Self.ctx, catalog: Self.catalog, existsLocally: Self.here)
    }

    // MARK: The live bug's drafts

    @Test func anOldMiniDraftWithALaptopOnlyFolderComesToTheLaptop() throws {
        // d-iuxom0tp, saved before machineExplicit existed.
        let old = try JSONDecoder().decode(Draft.self, from: Data(#"{"id":"d-iuxom0tp","text":"","machine":"mini","project":"\#(Self.apiDir)"}"#.utf8))
        #expect(!old.machineExplicit, "migration: no flag = not chosen")
        var d = old
        d.machine = "M"
        let n = normalize(d)
        #expect(n.machine == nil && n.project == Self.apiDir, "the folder is on this Mac only: the machine follows it")
    }

    @Test func anOldMiniDraftOnAProjectTheMiniHasStaysOnTheMini() {
        // ＋ in a band whose project's default machine is the mini: the
        // catalog has exactly this folder there.
        let band = Draft(id: "d-band", machine: "M", project: "/Users/mini/projects/hesper")
        #expect(normalize(band) == band)
        // The laptop's folder of the same project, machine not chosen: here.
        let d = Draft(id: "d-3tovbfu2", machine: "M", project: Self.hesper)
        #expect(normalize(d).machine == nil)
        // A folder only the mini has: there.
        let mini = Draft(id: "d-m", project: "/Users/mini/projects/mini-only")
        #expect(normalize(mini).machine == "M")
        // No folder: this Mac.
        #expect(normalize(Draft(id: "d-x", machine: "M")).machine == nil)
    }

    @Test func anExplicitMiniChoiceIsKept() throws {
        let d = Draft(id: "d-pick", machine: "M", project: Self.apiDir, machineExplicit: true)
        #expect(normalize(d) == d, "chosen: kept (the chip row says the folder isn't there)")
        let json = String(data: try JSONEncoder().encode(d), encoding: .utf8) ?? ""
        #expect(json.contains(#""machineExplicit":true"#))
        let back = try JSONDecoder().decode(Draft.self, from: Data(json.utf8))
        #expect(back.machineExplicit)
        #expect(!(String(data: try JSONEncoder().encode(Draft(id: "d-n")), encoding: .utf8) ?? "").contains("machineExplicit"))
    }

    @Test func aTokenNamingAnotherFolderFixesTheProjectOnRestore() {
        // d-cu0pareo: "#acme-api …" but project hesper.
        let d = Draft(id: "d-cu0pareo", text: "#acme-api\nplease analyse the application", project: Self.hesper)
        let n = normalize(d)
        #expect(n.project == Self.apiDir && n.machine == nil)
        // Not resolvable yet (the lists not loaded): left alone, not cleared.
        let early = DraftSync.sync(d, previousText: nil, context: ComposerContext(localMachine: "L"))
        #expect(early.project == Self.hesper)
    }

    // MARK: Tokens on every edit

    @Test func theLastTokenWinsAndRemovingItClearsTheProject() {
        var d = Draft(id: "d-t", text: "#hesper then #acme-api")
        d = normalize(d, previous: "")
        #expect(d.project == Self.apiDir, "the last token")
        let before = d.text
        d.text = "then nothing"
        d = normalize(d, previous: before)
        #expect(d.project == nil, "the token went: back to Choose folder")
        // A folder chosen in the chip (no token) isn't cleared by edits.
        var chosen = Draft(id: "d-c", text: "fix it", project: Self.hesper)
        let t = chosen.text
        chosen.text = "fix it now"
        #expect(normalize(chosen, previous: t).project == Self.hesper)
        // A token naming the chosen folder keeps it (two folders "app").
        let ctx = ComposerContext(projects: [.init(path: "/old/app", recent: true), .init(path: "/p/app", recent: false)])
        let app = Draft(id: "d-a", text: "fix #app", project: "/p/app")
        #expect(DraftSync.sync(app, previousText: "fix", context: ctx).project == "/p/app")
    }

    @Test func anAtTokenIsAnExplicitMachineUntilRemoved() {
        var d = Draft(id: "d-at", text: "@mini #acme-api go")
        d = normalize(d, previous: "")
        #expect(d.machine == "M" && d.machineExplicit && d.project == Self.apiDir, "kept on the mini although the folder is here")
        let before = d.text
        d.text = "#acme-api go"
        d = normalize(d, previous: before)
        #expect(d.machine == nil && !d.machineExplicit, "the token went: the machine follows the folder again")
    }

    // MARK: The chip row's note

    @Test func aFolderOnlyHereIsSaidWithRunHereAndTheMinisCopy() {
        let n = FolderNote.make(project: Self.apiDir, machine: "M", local: "L", onTarget: false, existsLocally: true, catalog: Self.catalog,
                                targetName: "mini", hereName: "laptop")
        #expect(n?.kind == .elsewhere && n?.text == "This folder is on laptop, not on mini —" && n?.copy == nil)
        let g = FolderNote.make(project: Self.hesper, machine: "M", local: "L", onTarget: false, existsLocally: true, catalog: Self.catalog,
                                targetName: "mini", hereName: "laptop")
        #expect(g?.copy == "/Users/mini/projects/hesper", "Use mini's copy")
        #expect(FolderNote.make(project: Self.apiDir, machine: "M", local: "L", onTarget: nil, existsLocally: true, catalog: Self.catalog,
                                targetName: "mini", hereName: "laptop") == nil, "not checked yet: nothing")
        #expect(FolderNote.make(project: Self.apiDir, machine: "M", local: "L", onTarget: true, existsLocally: true, catalog: Self.catalog,
                                targetName: "mini", hereName: "laptop") == nil)
        #expect(FolderNote.make(project: Self.apiDir, machine: "L", local: "L", onTarget: false, existsLocally: false, catalog: Self.catalog,
                                targetName: "laptop", hereName: "laptop") == nil, "this Mac: the Create note instead")
        let gone = FolderNote.make(project: "/nowhere", machine: "M", local: "L", onTarget: false, existsLocally: false, catalog: Self.catalog,
                                   targetName: "mini", hereName: "laptop")
        #expect(gone?.kind == .missing && gone?.text == "Folder isn't on mini")
    }

    @Test func hesperdsNoDirectoryIsMappedNotShown() {
        #expect(DraftSync.missingDirectory(in: "no directory /Users/me/projects/acme-api") == "/Users/me/projects/acme-api")
        #expect(DraftSync.missingDirectory(in: "mini: no directory /x") == "/x")
        #expect(DraftSync.missingDirectory(in: "machine M is offline") == nil)
    }

    // MARK: The machine chip

    @Test func theMachineChipShowsTheName() {
        let mini = Self.ctx.machines[1]
        #expect(ComposerCompletion.machineChip(mini, short: "M", local: false) == "mini · 41 ms · relay")
        #expect(ComposerCompletion.machineChip(Self.ctx.machines[0], short: "L", local: true) == "laptop · this Mac")
        #expect(ComposerCompletion.machineChip(Machine(short: "mbp", name: "My-MacBook-Pro.local", online: true, route: "local"), short: "mbp", local: true)
                == "mbp · this Mac", "a host name: the short name")
        #expect(ComposerCompletion.machineChip(Machine(short: "S", name: "studio", online: false), short: "S", local: false) == "studio · offline")
        #expect(ComposerCompletion.machineChip(nil, short: "Q", local: false) == "Q")
    }
}
