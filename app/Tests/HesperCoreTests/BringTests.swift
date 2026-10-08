import Foundation
import Testing
@testable import HesperCore

private func rpcError(_ code: Int = -32000, _ message: String = "refused", dataCode: String? = nil, data: [String: JSONValue] = [:]) -> RPCError {
    var d = data
    if let dataCode { d["code"] = .string(dataCode) }
    return RPCError(code: code, message: message, kind: .remote, data: d.isEmpty ? nil : .object(d))
}

private let catalog = ProjectCatalog(projects: [
    Project(id: "x", name: "x", kind: .repo, paths: ["laptop": "/Users/me/projects/x", "mini": "/Users/me/src/x"]),
    Project(id: "y", name: "y", kind: .repo, paths: ["laptop": "/Users/me/projects/y"]),
])

private func note(_ path: String, here: Bool = true) -> FolderNote? {
    FolderNote.make(project: path, machine: "mini", local: "laptop", onTarget: false, existsLocally: here,
                    catalog: catalog, targetName: "mini", hereName: "laptop")
}

@Suite struct BringNoteActions {
    @Test func folderOnlyHereDefaultsToBringingWithChanges() throws {
        let n = try #require(note("/Users/me/projects/y"))
        #expect(n.actions(bring: true) == [.bring(.with), .bring(.clean), .runHere])
        #expect(n.startAction(bring: true) == .bring(.with))
        #expect(n.text(bring: true) == "Folder isn't on mini —")
        #expect(FolderAction.bring(.with).title(target: "mini", here: "laptop") == "Bring it to mini")
        #expect(FolderAction.bring(.clean).title(target: "mini", here: "laptop") == "Bring clean (last commit)")
    }

    @Test func targetsCopyIsTheLastAlternative() throws {
        let n = try #require(note("/Users/me/projects/x"))
        #expect(n.actions(bring: true) == [.bring(.with), .bring(.clean), .runHere, .useCopy("/Users/me/src/x")])
        #expect(FolderAction.useCopy("/a").title(target: "mini", here: "laptop") == "Use mini's copy")
    }

    @Test func olderHesperdKeepsTodaysNote() throws {
        let n = try #require(note("/Users/me/projects/x"))
        #expect(n.actions(bring: false) == [.runHere, .useCopy("/Users/me/src/x")])
        #expect(n.text(bring: false) == "This folder is on laptop, not on mini —")
        #expect(n.startAction(bring: false) == nil)
    }

    @Test func folderNowhereHasNothingToBring() throws {
        let n = try #require(note("/Users/me/projects/x", here: false))
        #expect(n.actions(bring: true) == [.useCopy("/Users/me/src/x")])
        #expect(n.text(bring: true) == "Folder isn't on mini —")
        #expect(n.startAction(bring: true) == nil)
    }

    @Test func failuresSayWhyUnderTheChips() throws {
        let n = try #require(note("/Users/me/projects/x"))
        #expect(n.actions(bring: true, failure: .exists) == [.useExisting, .runHere])
        #expect(n.text(bring: true, failure: .exists) == "mini already has it —")
        #expect(n.startAction(bring: true, failure: .exists) == .useExisting)
        #expect(FolderAction.useExisting.title(target: "mini", here: "laptop") == "Use it")
        #expect(n.actions(bring: true, failure: .tooLarge) == [.runHere, .useCopy("/Users/me/src/x")])
        #expect(n.text(bring: true, failure: .tooLarge) == "Too large to bring (over 100 MB) —")
        #expect(n.startAction(bring: true, failure: .tooLarge) == nil)
        #expect(n.actions(bring: true, failure: .toolMissing("codex")).first == .runHere)
        #expect(n.text(bring: true, failure: .toolMissing("codex")) == "Codex isn't installed on mini —")
        #expect(n.actions(bring: true, failure: .offline).first == .bring(.with)) // try again
        #expect(n.text(bring: true, failure: .offline) == "mini is offline —")
    }
}

@Suite struct BringErrors {
    @Test func codesMapToFailures() {
        #expect(BringFailure(rpcError(dataCode: "too-large")) == .tooLarge)
        #expect(BringFailure(rpcError(dataCode: "offline")) == .offline)
        #expect(BringFailure(rpcError(-32010, "mini is offline")) == .offline)
        #expect(BringFailure(rpcError(dataCode: "tool-missing", data: ["tool": "codex"])) == .toolMissing("codex"))
        #expect(BringFailure(rpcError(dataCode: "tool-missing"), tool: "claude") == .toolMissing("claude"))
        #expect(BringFailure(rpcError(dataCode: "exists")) == .exists)
        #expect(BringFailure(rpcError(-32000, "disk full")) == .other("disk full"))
    }

    @Test func olderHesperdFallsBackPerMachine() {
        #expect(BringFailure(rpcError(-32601, "no method agents.spawn")) == nil)
        #expect(BringFailure(rpcError(-32000, "Unknown method agents.spawn")) == nil)
        #expect(BringFailure(rpcError(-32602, "unknown param bring")) == nil)
        // It ignored `bring` and looked for the folder.
        #expect(BringFailure(rpcError(-32004, "no directory /Users/me/projects/y")) == nil)
        #expect(BringRPC.unsupported(rpcError(dataCode: "exists")) == false)

        var s = BringSupport()
        #expect(s.offered("mini"))
        s.byMachine["mini"] = false
        #expect(!s.offered("mini"))
        #expect(s.offered("studio"))
    }

    @Test func spawnSendsBring() {
        var r = SpawnRequest(machine: "mini", project: "/Users/me/projects/y", task: "t")
        #expect(r.params["bring"] == nil)
        r.bring = BringRequest(from: "laptop", path: "/Users/me/projects/y", changes: .clean)
        #expect(r.params["bring"]?["from"]?.stringValue == "laptop")
        #expect(r.params["bring"]?["path"]?.stringValue == "/Users/me/projects/y")
        #expect(r.params["bring"]?["changes"]?.stringValue == "clean")
        #expect(BringRequest(from: "laptop", path: "/p").params["changes"]?.stringValue == "with")
    }
}

@Suite struct BringProgressText {
    @Test func parsesStepsAndPercent() throws {
        let p = try #require(BringProgress(params: ["draft": "d1", "step": "transfer", "percent": 42, "to": "mini"]))
        #expect(p.draft == "d1" && p.to == "mini" && p.step == .transfer && p.percent == 42)
        #expect(p.line(target: "mini") == "Bringing to mini · checkpoint → transfer 42% → unpack → starting")
        #expect(p.steps().map(\.status) == [.done, .current, .pending, .pending])
        #expect(BringProgress(params: ["step": "transfer", "progress": .number(0.5), "to": "mini"])?.percent == 50)
        #expect(BringProgress(params: ["step": "transfer", "bytes": 25, "total": 100, "to": "mini"])?.percent == 25)
        #expect(BringProgress(params: ["step": "spawn", "to": "mini"])?.line(target: "mini")
            == "Bringing to mini · checkpoint → transfer → unpack → starting")
        #expect(BringProgress(params: ["step": "bogus"]) == nil)
    }

    @Test func nothingHeardYet() {
        #expect(BringProgress(to: "mini").line(target: "mini") == "Bringing to mini…")
    }

    @Test func cleanTakesNoCheckpoint() throws {
        let p = try #require(BringProgress(params: ["step": "unpack", "to": "mini"]))
        #expect(p.line(target: "mini", changes: .clean) == "Bringing to mini · transfer → unpack → starting")
        #expect(p.steps(.clean).map(\.status) == [.done, .current, .pending])
    }

    @Test func doneAndFailedEnd() throws {
        let done = try #require(BringProgress(params: ["step": "done", "to": "mini"]))
        #expect(done.phase == .done)
        #expect(done.steps().allSatisfy { $0.status == .done })
        let failed = try #require(BringProgress(params: ["step": "failed", "error": "too large", "to": "mini"]))
        #expect(failed.phase == .failed("too large"))
    }

    @Test func trackerMatchesDrafts() throws {
        var t = BringTracker()
        t.start(draft: "d1", to: "mini", changes: .with)
        // By draft.
        #expect(t.apply(try #require(BringProgress(params: ["draft": "d1", "step": "checkpoint", "to": "mini"]))) == "d1")
        // Without draft: the one bring to that Mac.
        #expect(t.apply(try #require(BringProgress(params: ["step": "transfer", "percent": 10, "to": "mini"]))) == "d1")
        #expect(t.progress(draft: "d1")?.progress.percent == 10)
        // A late note never takes the line back.
        t.apply(try #require(BringProgress(params: ["draft": "d1", "step": "unpack", "to": "mini"])))
        t.apply(try #require(BringProgress(params: ["draft": "d1", "step": "transfer", "percent": 99, "to": "mini"])))
        #expect(t.progress(draft: "d1")?.progress.step == .unpack)
        // Not ours.
        #expect(t.apply(try #require(BringProgress(params: ["draft": "other", "step": "unpack", "to": "mini"]))) == nil)
        // Two brings to mini: a note without draft is ambiguous.
        t.start(draft: "d2", to: "mini", changes: .clean)
        #expect(t.apply(try #require(BringProgress(params: ["step": "unpack", "to": "mini"]))) == nil)
        t.finish(draft: "d1")
        #expect(t.progress(draft: "d1") == nil)
        #expect(t.progress(draft: "d2")?.changes == .clean)
    }
}
