import Foundation
import Testing
@testable import HesperCore

struct LoopMapTests {
    typealias F = LoopFixtures

    var catalog: ProjectCatalog {
        ProjectCatalog(projects: [Project(id: "p-app", name: "app", paths: ["L": "/tmp/fake/app", "M": "/tmp/fake/m/app"]),
                                  Project(id: "p-api", name: "api", paths: ["L": "/tmp/fake/api", "M": "/tmp/fake/m/api"])])
    }

    func live(_ id: String, loop: String, project: String, parent: String? = nil, state: AgentState = .working) -> Agent {
        var a = Agent(id: id, name: "live " + id, state: state, project: project)
        a.loop = loop; a.parent = parent
        return a
    }

    /// A loop over two Macs: a root on L starts a child on M (through a
    /// brief file) and a child on L; the M child asks a step of the person;
    /// one agent names a parent nobody knows; the loop is blocked by another.
    func world() -> [String: Loop] {
        let push = F.loop("lp-push", "Push", next: .you, items: [
            F.item("it-1", "Approve the store listing", source: "n-m/c1", created: F.date(2)),
            F.item("it-2", "An offer", kind: .offer),
        ], links: [
            F.agent("n-l/root", rel: .member, label: "root", machine: "L", agent: "L/root", state: .done, added: F.date(0)),
            LoopLink(kind: .file, ref: "L:/tmp/fake/app/brief.md", rel: .handoff, from: "n-l/root", added: F.date(0.5)),
            F.agent("n-m/c1", rel: .handoff, from: "L:/tmp/fake/app/brief.md", label: "ios", machine: "M", agent: "M/c1", state: .done, ending: .yourStep, added: F.date(1)),
            F.agent("n-l/c2", rel: .started, from: "n-l/root", label: "api", machine: "L", added: F.date(1)),
            F.agent("n-m/orphan", rel: .started, from: "L/ghost", label: "orphan", machine: "M", added: F.date(1.5)),
            LoopLink(kind: .loop, ref: "lp-auth", rel: .blockedBy),
        ], projects: ["p-app"], created: F.date(0))
        let auth = F.loop("lp-auth", "Auth", next: .agents, links: [
            F.agent("n-l/au", label: "auth", machine: "L", agent: "L/au", state: .working, added: F.date(0)),
            LoopLink(kind: .loop, ref: "lp-push", rel: .blocks),
        ], projects: ["p-api"], created: F.date(-1))
        return [push.id: push, auth.id: auth]
    }

    var agents: [Agent] {
        [live("L/root", loop: "lp-push", project: "/tmp/fake/app", state: .done),
         live("M/c1", loop: "lp-push", project: "/tmp/fake/m/app", state: .done),
         live("L/au", loop: "lp-auth", project: "/tmp/fake/api"),
         // In the loop but not linked yet: joins through its parent.
         live("L/new", loop: "lp-push", project: "/tmp/fake/api", parent: "L/root"),
         // The Steward never shows.
         { var s = live("L/st", loop: "lp-push", project: "/tmp/fake/app"); s.service = "steward"; return s }()]
    }

    func assertNoOverlaps(_ m: LoopMap) {
        for (i, a) in m.nodes.enumerated() {
            for b in m.nodes[(i + 1)...] { #expect(!a.frame.intersects(b.frame), "\(a.id) overlaps \(b.id)") }
            // Inside its lane and region.
            #expect(m.lanes.contains { $0.machine == a.machine && $0.frame.contains(a.frame) }, "\(a.id) outside its lane")
            #expect(m.regions.contains { $0.machine == a.machine && $0.project == a.project && $0.frame.contains(a.frame) }, "\(a.id) outside its region")
        }
        for (i, a) in m.regions.enumerated() { for b in m.regions[(i + 1)...] { #expect(!a.frame.intersects(b.frame), "\(a.id) overlaps \(b.id)") } }
        for (i, a) in m.lanes.enumerated() { for b in m.lanes[(i + 1)...] { #expect(!a.frame.intersects(b.frame)) } }
        #expect(m.nodes.allSatisfy { $0.frame.maxX <= m.width && $0.frame.maxY <= m.height })
    }

    @Test func oneLoopLanesNodesEdges() throws {
        let m = LoopMap.build(scope: .loop("lp-push"), loops: world(), agents: agents, catalog: catalog, local: "L")
        // This Mac first, then the other, then other loops.
        #expect(m.lanes.map(\.machine) == ["L", "M", nil] && m.lanes.map(\.title) == ["L", "M", "Other loops"])
        let ids = Set(m.nodes.map(\.id))
        #expect(ids == ["a:n-l/root", "f:L:/tmp/fake/app/brief.md", "a:n-m/c1", "a:n-l/c2", "a:n-m/orphan", "m:L/ghost",
                        "a:live:L/new", "s:lp-push:it-1", "l:lp-auth"])
        let kinds = Dictionary(m.edges.map { ("\($0.from)>\($0.to)", $0.kind) }, uniquingKeysWith: { a, _ in a })
        #expect(kinds["a:n-l/root>f:L:/tmp/fake/app/brief.md"] == .handoff)
        #expect(kinds["f:L:/tmp/fake/app/brief.md>a:n-m/c1"] == .handoff)
        #expect(kinds["a:n-l/root>a:n-l/c2"] == .started)
        #expect(kinds["a:n-l/root>a:live:L/new"] == .started)
        #expect(kinds["m:L/ghost>a:n-m/orphan"] == .brokenParent)
        #expect(kinds["a:n-m/c1>s:lp-push:it-1"] == .youStep)
        #expect(kinds["l:lp-auth>a:n-l/root"] == .blocks)
        #expect(m.edges.count == 7)
        // Depth left to right.
        let n = Dictionary(m.nodes.map { ($0.id, $0) }, uniquingKeysWith: { a, _ in a })
        #expect(n["a:n-l/root"]?.depth == 0 && n["f:L:/tmp/fake/app/brief.md"]?.depth == 1 && n["a:n-m/c1"]?.depth == 2 && n["s:lp-push:it-1"]?.depth == 3)
        #expect(n["a:n-l/root"]!.frame.x < n["a:n-m/c1"]!.frame.x)
        #expect(m.edges.filter { $0.kind != .blocks }.allSatisfy { !$0.backward })
        // Lanes and regions: the file on L in app; the step after its source on M; the missing parent in its child's lane.
        #expect(n["f:L:/tmp/fake/app/brief.md"]?.machine == "L" && n["f:L:/tmp/fake/app/brief.md"]?.project == "p-app")
        #expect(n["s:lp-push:it-1"]?.machine == "M" && n["m:L/ghost"]?.machine == "M")
        #expect(n["a:live:L/new"]?.project == "p-api" && n["a:n-l/root"]?.project == "p-app")
        #expect(Set(m.regions.filter { $0.machine == "L" }.map(\.title)) == ["api", "app"])
        // Live details joined in: live agents carry their id, gone ones not.
        #expect(n["a:n-l/root"]?.agent == "L/root" && n["a:n-l/c2"]?.agent == nil && n["a:n-l/c2"]?.state == nil)
        #expect(n["a:n-m/c1"]?.ending == .yourStep && n["l:lp-auth"]?.title == "Auth")
        // Offers are not steps.
        #expect(!ids.contains("s:lp-push:it-2"))
        assertNoOverlaps(m)
    }

    @Test func allLoopsDrawsBlocksBetweenTheirRoots() {
        let m = LoopMap.build(scope: .all, loops: world(), agents: agents, catalog: catalog, local: "L")
        #expect(!m.nodes.contains { $0.kind == .loop } && !m.lanes.contains { $0.machine == nil })
        #expect(m.edges.contains { $0.kind == .blocks && $0.from == "a:n-l/au" && $0.to == "a:n-l/root" })
        #expect(m.edges.filter { $0.kind == .blocks }.count == 1) // blocks + blockedBy of the same pair: one edge
        assertNoOverlaps(m)
    }

    @Test func deterministicAndCrowdedWithoutOverlaps() {
        // Many loops, many children, across three machines.
        var loops: [String: Loop] = [:]
        for i in 0..<12 {
            var links = [F.agent("n/r\(i)", machine: ["L", "M", "X"][i % 3], added: F.date(Double(i)))]
            for c in 0..<(i % 5) {
                links.append(F.agent("n/c\(i)-\(c)", rel: .started, from: c == 0 ? "n/r\(i)" : "n/c\(i)-\(c - 1)", machine: ["L", "M"][c % 2], added: F.date(Double(i) + 0.1)))
            }
            let l = F.loop("lp-\(i)", next: .agents, items: [F.item("s", "step \(i)", source: "n/r\(i)")], links: links,
                           projects: [i % 2 == 0 ? "p-app" : "p-api"], created: F.date(Double(i)))
            loops[l.id] = l
        }
        let a = LoopMap.build(scope: .all, loops: loops, agents: [], catalog: catalog, local: "M")
        let b = LoopMap.build(scope: .all, loops: loops, agents: [], catalog: catalog, local: "M")
        #expect(a == b)
        #expect(a.lanes.first?.machine == "M" && a.nodes.count == 12 + (0..<12).map { $0 % 5 }.reduce(0, +) + 12)
        #expect(a.nodes.map(\.depth).max() == 4)
        assertNoOverlaps(a)
    }

    @Test func cyclesAndEmpty() {
        // A cycle never hangs and still lays out.
        let l = F.loop("lp-c", links: [F.agent("n/a", rel: .started, from: "n/b"), F.agent("n/b", rel: .started, from: "n/a")])
        let m = LoopMap.build(scope: .loop("lp-c"), loops: [l.id: l], agents: [])
        #expect(m.nodes.count == 2)
        assertNoOverlaps(m)
        #expect(LoopMap.build(scope: .loop("nope"), loops: [:], agents: []).isEmpty)
        #expect(LoopMap.build(scope: .all, loops: [:], agents: []).width == 0)
    }

    @Test func cacheBuildsOncePerVersion() {
        var cache = LoopMapCache()
        let w = world()
        let k = LoopMapCache.Key(scope: .all, version: 1)
        _ = cache.map(k) { LoopMap.build(scope: .all, loops: w, agents: [], catalog: catalog) }
        _ = cache.map(k) { LoopMap.build(scope: .all, loops: w, agents: [], catalog: catalog) }
        #expect(cache.builds == 1)
        _ = cache.map(LoopMapCache.Key(scope: .all, version: 2)) { LoopMap.build(scope: .all, loops: w, agents: []) }
        _ = cache.map(LoopMapCache.Key(scope: .loop("lp-push"), version: 2)) { LoopMap.build(scope: .loop("lp-push"), loops: w, agents: []) }
        #expect(cache.builds == 3)
        cache.invalidate()
        _ = cache.map(LoopMapCache.Key(scope: .loop("lp-push"), version: 2)) { LoopMap(scope: .all) }
        #expect(cache.builds == 4)
    }
}
