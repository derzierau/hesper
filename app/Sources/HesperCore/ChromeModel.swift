import Foundation

// The toolbar's segmented scope and the sidebar's Machines section: pure
// rules for the chrome (UI/Toolbar.swift, UI/ProjectSidebar.swift).

/// The toolbar's segmented scope: All · Needs you · Working. Each segment is
/// a wall scope; the scope pill (window layer) still offers Overflow,
/// filters and projects.
public enum ScopeSegment: String, CaseIterable, Sendable {
    case all, needsYou, working

    public var title: String {
        switch self {
        case .all: return "All"
        case .needsYou: return "Needs you"
        case .working: return "Working"
        }
    }

    /// The scope a click on the segment gives the wall.
    public var scope: WallScope {
        switch self {
        case .all: return .all
        case .needsYou: return .filter(WallFilter(states: [.needsYou]))
        case .working: return .filter(WallFilter(states: [.working]))
        }
    }

    /// The segment a scope shows as selected; nil for any other scope
    /// (Overflow, a group, a project, a custom filter): no segment is lit.
    public init?(scope: WallScope) {
        guard let s = Self.allCases.first(where: { $0.scope == scope }) else { return nil }
        self = s
    }

    /// The scope after a click: a lit segment other than All goes back to All.
    public static func clicked(_ s: ScopeSegment, current: WallScope) -> WallScope {
        s != .all && ScopeSegment(scope: current) == s ? .all : s.scope
    }
}

/// One row of the sidebar's Machines section.
public struct SidebarMachine: Equatable, Sendable, Identifiable {
    public var id: String { "m:" + short }
    public var short: String
    public var title: String
    public var online: Bool
    /// Agents on it everywhere (not just this wall).
    public var count: Int
    public var needsYou: Bool
    /// What a click gives the wall: a filter on this machine.
    public var scope: WallScope { .filter(WallFilter(machines: [short])) }

    public init(short: String, title: String, online: Bool, count: Int, needsYou: Bool) {
        self.short = short; self.title = title; self.online = online; self.count = count; self.needsYou = needsYou
    }
}

public enum SidebarMachines {
    /// The machines in the daemon's order, with their agents counted.
    public static func make(_ machines: [Machine], agents: [Agent]) -> [SidebarMachine] {
        machines.map { m in
            let list = agents.filter { $0.machine == m.short }
            return SidebarMachine(short: m.short, title: m.displayName, online: m.online, count: list.count,
                                  needsYou: list.contains { $0.state.needsAttention })
        }
    }
}
