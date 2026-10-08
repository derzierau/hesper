import Foundation
import Testing
@testable import HesperCore

/// The toolbar's segmented scope and the sidebar's Machines section.
@Suite struct ChromeModelTests {
    @Test func segmentsMapToScopes() {
        #expect(ScopeSegment.all.scope == .all)
        #expect(ScopeSegment.needsYou.scope == .filter(WallFilter(states: [.needsYou])))
        #expect(ScopeSegment.working.scope == .filter(WallFilter(states: [.working])))
        for s in ScopeSegment.allCases { #expect(ScopeSegment(scope: s.scope) == s) }
    }

    @Test func otherScopesLightNoSegment() {
        #expect(ScopeSegment(scope: .overflow) == nil)
        #expect(ScopeSegment(scope: .project("p")) == nil)
        #expect(ScopeSegment(scope: .group("g")) == nil)
        #expect(ScopeSegment(scope: .filter(WallFilter(machines: ["M"]))) == nil)
        #expect(ScopeSegment(scope: .filter(WallFilter(states: [.needsYou, .working]))) == nil)
    }

    @Test func clickingALitSegmentGoesBackToAll() {
        #expect(ScopeSegment.clicked(.needsYou, current: .all) == ScopeSegment.needsYou.scope)
        #expect(ScopeSegment.clicked(.needsYou, current: ScopeSegment.needsYou.scope) == .all)
        #expect(ScopeSegment.clicked(.working, current: ScopeSegment.needsYou.scope) == ScopeSegment.working.scope)
        #expect(ScopeSegment.clicked(.all, current: .all) == .all)
        #expect(ScopeSegment.clicked(.all, current: .project("p")) == .all)
    }

    @Test func machinesCountTheirAgents() {
        let machines = [Machine(short: "L", name: "laptop"), Machine(short: "M", name: "Oles-Mac-mini.local", online: false)]
        let agents = [
            Agent(id: "L/a", state: .working), Agent(id: "L/b", state: .approval), Agent(id: "M/c", state: .idle),
        ]
        let rows = SidebarMachines.make(machines, agents: agents)
        #expect(rows.map(\.id) == ["m:L", "m:M"])
        #expect(rows.map(\.title) == ["laptop", "M"]) // a host name shows as the short name
        #expect(rows.map(\.count) == [2, 1])
        #expect(rows.map(\.needsYou) == [true, false])
        #expect(rows.map(\.online) == [true, false])
        #expect(rows[1].scope == .filter(WallFilter(machines: ["M"])))
    }
}
