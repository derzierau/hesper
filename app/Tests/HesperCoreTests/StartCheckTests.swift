import Foundation
import Testing
@testable import HesperCore

struct StartCheckTests {
    typealias F = LoopFixtures

    var catalog: ProjectCatalog {
        ProjectCatalog(projects: [Project(id: "p-app", name: "app", paths: ["L": "/tmp/fake/app"]),
                                  Project(id: "p-pkg", name: "pkg", kind: .package, parentId: "p-app", paths: ["L": "/tmp/fake/app/pkg"]),
                                  Project(id: "p-web", name: "web", paths: ["L": "/tmp/fake/web"])])
    }

    func loops() -> [Loop] {
        [
            F.loop("lp-push", "Push notifications for the iOS app", next: .agents, items: [
                F.item("i1", "Rotate the APNs signing key", owner: .agent, kind: .looseEnd),
                F.item("i2", "Write release notes", kind: .promise),
                F.item("i3", "Maybe add Android later?", kind: .offer),
            ], links: [F.agent("n/a", machine: "L")], projects: ["p-pkg"], updated: F.date(1)),
            F.loop("lp-web-push", "Web push notifications", next: .you, items: [F.item("w", "Pick a provider")], projects: ["p-web"], updated: F.date(2)),
            F.loop("lp-docs", "Überarbeite die Dokumentation für Benachrichtigungen", next: .others, projects: ["p-web"], updated: F.date(0)),
            F.loop("lp-parked", "Push notifications refactor", next: .parked, status: .parked, projects: ["p-app"]),
            F.loop("lp-done", "Push notifications for the iOS app", next: .closed, status: .done, projects: ["p-app"]),
            F.loop("lp-you2", "Something else", next: .you, items: [F.item("x", "Answer")]),
            F.loop("lp-looks", "Looks done", next: .you, looksDone: true),
        ]
    }

    @Test func tokens() {
        #expect(LoopTokens.of("Fix the flaky Tests in the API, please!") == ["fix", "flaky", "test", "api"])
        // Case and accents folded, German stop words dropped.
        #expect(LoopTokens.of("Überarbeite die Dokumentation für die App") == ["uberarbeite", "dokumentation", "app"])
        #expect(LoopTokens.of("a I x 42 v2") == ["42", "v2"])
        #expect(LoopTokens.of("class address") == ["class", "address"]) // "ss" keeps its s
        #expect(LoopTokens.of("").isEmpty)
    }

    @Test func sameProjectFirst() {
        let ix = StartCheckIndex(loops: loops(), catalog: catalog)
        // Both push loops share words; the app's project (a package's repository) wins.
        let r = ix.check(task: "push notifications: badge count", project: "p-app")
        #expect(r.match?.loop == "lp-push" && r.match?.sameProject == true)
        let w = ix.check(task: "push notifications: badge count", project: "p-web")
        #expect(w.match?.loop == "lp-web-push" && w.match?.sameProject == true)
        // No project: the higher score.
        #expect(ix.check(task: "web push notifications").match?.loop == "lp-web-push")
        // Parked and closed loops never match.
        #expect(ix.check(task: "push notifications refactor", project: "p-app").match?.loop == "lp-push")
    }

    @Test func thresholdAndLanguages() {
        let ix = StartCheckIndex(loops: loops(), catalog: catalog)
        // One shared word in another project: below the threshold.
        #expect(ix.check(task: "push the button").match == nil)
        // One shared word in the same project is enough when it is half the task.
        #expect(ix.check(task: "push it", project: "p-web").match?.loop == "lp-web-push")
        // Stop words only, or nothing typed: no match.
        #expect(ix.check(task: "please do it now").match == nil && ix.check(task: "").match == nil)
        // German, accents folded.
        #expect(ix.check(task: "Dokumentation der Benachrichtigungen ueberarbeiten, uberarbeite sie").match?.loop == "lp-docs")
    }

    @Test func looseEndOfferedToCheckOff() {
        let ix = StartCheckIndex(loops: loops(), catalog: catalog)
        let r = ix.check(task: "Rotate the APNs signing key before Friday", project: "p-pkg")
        #expect(r.match?.loop == "lp-push" && r.match?.looseEnd == "i1" && r.match?.looseEndText == "Rotate the APNs signing key")
        // A promise counts too; an offer never does.
        #expect(ix.check(task: "write the release notes").match?.looseEnd == "i2")
        #expect(ix.check(task: "add android later", project: "p-app").match?.looseEnd == nil)
        // A strong loose-end match carries the loop even in another project.
        #expect(ix.check(task: "rotate apns signing key", project: "p-web").match?.loop == "lp-push")
    }

    @Test func limitCounts() {
        let ix = StartCheckIndex(loops: loops(), catalog: catalog, loopLimit: 2, version: 7)
        let r = ix.check(task: "anything")
        // On you: web-push and you2 (looks done left out).
        #expect(r.onYou == 2 && r.loopLimit == 2 && r.atLimit && r.limitHint == "2 loops already wait on you (limit 2)" && ix.version == 7)
        let one = StartCheckIndex(loops: [F.loop("a", next: .you)], loopLimit: 1).check(task: "x")
        #expect(one.limitHint == "1 loop already waits on you (limit 1)")
        #expect(StartCheckIndex(loops: loops()).check(task: "x").limitHint == nil)
    }

    @Test func fastEnoughToRunWhileTyping() {
        var many: [Loop] = []
        for i in 0..<2000 {
            many.append(F.loop("lp-\(i)", "Loop \(i) about widget \(i % 37) and gadget \(i % 11)",
                               items: (0..<5).map { F.item("i\($0)", "loose end \($0) for widget \(i % 37)", kind: .looseEnd) }, projects: ["p-\(i % 9)"]))
        }
        let ix = StartCheckIndex(loops: many)
        let start = Date()
        for _ in 0..<20 { _ = ix.check(task: "fix widget 12 and gadget 3 loose end", project: "p-3") }
        // 20 checks over 2,000 loops: well under a frame each in a debug build.
        #expect(Date().timeIntervalSince(start) < 1.0)
    }
}
