import HesperCore

/// The scope bar's segments: which sections Home shows
/// (docs/iphone/neon.md, "Content").
public enum HomeScope: CaseIterable, Sendable {
    case all, needsYou, working

    public var title: String {
        switch self {
        case .all: "All"
        case .needsYou: "Needs you"
        case .working: "Working"
        }
    }
}

/// The numbers in the scope bar. All counts every agent, exited ones
/// included, so it matches the Agents screen.
public struct HomeCounts: Equatable, Sendable {
    public var all: Int
    public var needsYou: Int
    public var working: Int

    public init(all: Int = 0, needsYou: Int = 0, working: Int = 0) {
        self.all = all
        self.needsYou = needsYou
        self.working = working
    }
}

/// What Home shows for one scope: the priority card and the three grids.
/// Exited and unknown agents are never on Home; they stay in Agents.
public struct HomeSections: Equatable, Sendable {
    /// The decision card: the head of the attention queue.
    public var priority: Agent?
    /// The rest of the attention queue, in its order.
    public var needsYou: [Agent]
    /// Done and idle (the Mac's "Done · idle" tier), newest first.
    public var readyToReview: [Agent]
    /// Working and starting, newest first.
    public var working: [Agent]

    public init(priority: Agent? = nil, needsYou: [Agent] = [], readyToReview: [Agent] = [], working: [Agent] = []) {
        self.priority = priority
        self.needsYou = needsYou
        self.readyToReview = readyToReview
        self.working = working
    }

    /// Nothing to show: Home falls back to the empty copy.
    public var isEmpty: Bool { priority == nil && needsYou.isEmpty && readyToReview.isEmpty && working.isEmpty }
}

extension Fleet {
    public var homeCounts: HomeCounts {
        HomeCounts(all: agents.count, needsYou: needsYou.count, working: counts.working)
    }

    public func home(_ scope: HomeScope) -> HomeSections {
        let queue = needsYou
        var s = HomeSections()
        if scope != .working {
            s.priority = queue.first
            s.needsYou = Array(queue.dropFirst())
        }
        if scope == .all {
            s.readyToReview = Self.newestFirst(agents.values.filter { $0.state == .done || $0.state == .idle })
        }
        if scope != .needsYou {
            s.working = Self.newestFirst(agents.values.filter { $0.state == .working || $0.state == .starting })
        }
        return s
    }
}
