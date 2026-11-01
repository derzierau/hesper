import Foundation

// Loops (docs/rebuild-contract.md "As built — loops (app core)"): lasting
// records of each piece of work, the goals and milestones of a product and
// the Steward's configuration, as hesperd reports them (relay/pkg/wire/
// loops.go). Decoding is lenient like Projects.swift: unknown fields are
// ignored, a missing field gets its default and an unknown enum value its
// safest case, so a newer hesperd never breaks the app. Who acts next
// (`Loop.next`) is computed by hesperd; the app only sorts and draws.

// MARK: Wire names

public enum LoopsRPC {
    public static let list = "loops.list"
    public static let get = "loops.get"
    public static let save = "loops.save"
    public static let link = "loops.link"
    public static let item = "loops.item"
    public static let merge = "loops.merge"
    public static let split = "loops.split"
    public static let settingsGet = "loops.settings.get"
    public static let settingsSet = "loops.settings.set"
    public static let goalsList = "goals.list"
    public static let goalsSave = "goals.save"
    public static let goalsRemove = "goals.remove"
    public static let goalsItem = "goals.item"
    public static let goalsFile = "goals.file"
    public static let goalsAccept = "goals.accept"
    public static let stewardStatus = "steward.status"
    public static let stewardSet = "steward.set"
    public static let stewardInbox = "steward.inbox"
    public static let stewardPlan = "steward.plan"

    /// Notifications (on agents.subscribe; first a snapshot of every open
    /// and parked loop, every goal and milestone, and the Steward).
    public static let loopChanged = "loops.changed"
    public static let loopRemoved = "loops.removed"
    public static let goalChanged = "goals.changed"
    public static let goalRemoved = "goals.removed"
    public static let stewardChanged = "steward.changed"

    /// The method is missing (an older hesperd): no loops there.
    public static func missing(_ e: RPCError) -> Bool { CloseRPC.missing(e) }
}

// MARK: Values

/// `Loop.status`. Unknown values are open: a loop never disappears by
/// accident.
public enum LoopStatus: String, Sendable, CaseIterable, Decodable {
    case open, parked, done, dropped

    public init(from decoder: any Decoder) throws {
        self = LoopStatus(rawValue: try decoder.singleValueContainer().decode(String.self)) ?? .open
    }

    /// Done or dropped: only a person closes a loop.
    public var isClosed: Bool { self == .done || self == .dropped }
}

/// `Goal.status` and `Milestone.status`; `proposed` only for milestones a
/// planning session wrote that a person has not accepted (goals.accept).
public enum MilestoneStatus: String, Sendable, CaseIterable, Decodable {
    case proposed, open, done, dropped

    public init(from decoder: any Decoder) throws {
        self = MilestoneStatus(rawValue: try decoder.singleValueContainer().decode(String.self)) ?? .open
    }
}

/// Who acts next on a loop (`Loop.next`, computed by hesperd). Unknown
/// values are others: never red ("needs you") by accident.
public enum LoopNext: String, Sendable, CaseIterable, Decodable {
    case you, agents, others, parked, closed

    public init(from decoder: any Decoder) throws {
        self = LoopNext(rawValue: try decoder.singleValueContainer().decode(String.self)) ?? .others
    }
}

/// `LoopItem.owner`. Unknown values are the agent's.
public enum ItemOwner: String, Sendable, CaseIterable, Decodable {
    case you, agent, others

    public init(from decoder: any Decoder) throws {
        self = ItemOwner(rawValue: try decoder.singleValueContainer().decode(String.self)) ?? .agent
    }
}

/// `LoopItem.state`. Unknown values are open.
public enum ItemState: String, Sendable, CaseIterable, Decodable {
    case open, done, dropped

    public init(from decoder: any Decoder) throws {
        self = ItemState(rawValue: try decoder.singleValueContainer().decode(String.self)) ?? .open
    }
}

/// `LoopItem.kind`: a step, a loose end, a promise, a question, an offer
/// (shown dimmed in the loop, never in the inbox) or a milestone's "done
/// when" condition. Unknown values are steps.
public enum ItemKind: String, Sendable, CaseIterable, Decodable {
    case step, looseEnd, promise, question, offer, condition

    public init(from decoder: any Decoder) throws {
        self = ItemKind(rawValue: try decoder.singleValueContainer().decode(String.self)) ?? .step
    }
}

/// How an agent's last turn ended (`Agent.ending`, `LoopLink.ending`):
/// it asks the person, names a step that is the person's, offers more
/// work, waits on someone else, or is done. Unknown values decode as nil.
public enum LoopEnding: String, Sendable, CaseIterable, Decodable {
    case asks, yourStep, offer, waits, done

    /// The ending puts the loop on the person (asks, yourStep).
    public var isYourMove: Bool { self == .asks || self == .yourStep }
}

/// `LoopLink.kind`. Unknown kinds keep their raw value (`.other`).
public enum LinkKind: Hashable, Sendable {
    case agent, session, project, url, file, loop, other(String)

    public init(wire: String?) {
        switch wire ?? "" {
        case "agent": self = .agent
        case "session": self = .session
        case "project": self = .project
        case "url": self = .url
        case "file": self = .file
        case "loop": self = .loop
        case let s: self = .other(s)
        }
    }

    public var wire: String {
        switch self {
        case .agent: return "agent"
        case .session: return "session"
        case .project: return "project"
        case .url: return "url"
        case .file: return "file"
        case .loop: return "loop"
        case .other(let s): return s
        }
    }
}

/// `LoopLink.rel`. Unknown relations are members.
public enum LinkRel: String, Sendable, CaseIterable, Decodable {
    case member, started, continued, handoff, blocks, blockedBy, touches

    public init(from decoder: any Decoder) throws {
        self = LinkRel(rawValue: try decoder.singleValueContainer().decode(String.self)) ?? .member
    }
}

/// `StewardConfig.level`: watch keeps the records, suggest (the default)
/// also saves drafts, act also starts ready drafts and merges or splits
/// loops. Unknown values are watch: never more than the person chose.
public enum StewardLevel: String, Sendable, CaseIterable, Decodable {
    case watch, suggest, act

    public init(from decoder: any Decoder) throws {
        self = StewardLevel(rawValue: try decoder.singleValueContainer().decode(String.self)) ?? .watch
    }
}

/// `ItemCheck.kind`: what the Steward checks to tick an item off.
public enum ItemCheckKind: String, Sendable, CaseIterable {
    case pr, tag, url, review, loop
}

// MARK: Records

extension Agent {
    /// A Go `time.Time` (zero values, "0001-01-01…", are nil).
    static func wireDate(_ s: String?) -> Date? { date(s).flatMap { $0.timeIntervalSince1970 < 0 ? nil : $0 } }
}

private extension KeyedDecodingContainer {
    func str(_ k: Key) -> String { ((try? decodeIfPresent(String.self, forKey: k)) ?? nil) ?? "" }
    func opt(_ k: Key) -> String? { let s = str(k); return s.isEmpty ? nil : s }
    func bool(_ k: Key) -> Bool { ((try? decodeIfPresent(Bool.self, forKey: k)) ?? nil) ?? false }
    func int(_ k: Key) -> Int {
        if let i = (try? decodeIfPresent(Int.self, forKey: k)) ?? nil { return i }
        return Int(((try? decodeIfPresent(Double.self, forKey: k)) ?? nil) ?? 0)
    }
    func double(_ k: Key) -> Double { ((try? decodeIfPresent(Double.self, forKey: k)) ?? nil) ?? 0 }
    func date(_ k: Key) -> Date? { Agent.wireDate((try? decodeIfPresent(String.self, forKey: k)) ?? nil) }
    func strings(_ k: Key) -> [String] { ((try? decodeIfPresent([String].self, forKey: k)) ?? nil) ?? [] }
    func value<T: Decodable>(_ t: T.Type, _ k: Key) -> T? { (try? decodeIfPresent(T.self, forKey: k)) ?? nil }
    /// A list whose bad entries are skipped (one odd item never loses the
    /// rest).
    func list<T: Decodable>(_ t: T.Type, _ k: Key) -> [T] {
        guard let raw = value([JSONValue].self, k) else { return [] }
        return raw.compactMap { try? $0.decode(T.self) }
    }
}

/// `ItemCheck`: how an item can be ticked off without a person.
public struct ItemCheck: Equatable, Hashable, Sendable, Decodable {
    /// The raw kind (`ItemCheckKind`; a newer one is kept as given).
    public var kind: String
    public var ref: String

    public init(kind: String, ref: String) { self.kind = kind; self.ref = ref }

    public var knownKind: ItemCheckKind? { ItemCheckKind(rawValue: kind) }

    enum CodingKeys: String, CodingKey { case kind, ref }
    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        kind = c.str(.kind); ref = c.str(.ref)
    }

    var json: JSONValue { ["kind": .string(kind), "ref": .string(ref)] }
}

/// A loose end, step, promise, question or offer of a loop, or a "done
/// when" condition of a milestone (`LoopItem`).
public struct LoopItem: Equatable, Sendable, Identifiable, Decodable {
    public var id: String
    public var text: String
    public var owner: ItemOwner
    public var state: ItemState
    public var kind: ItemKind
    /// Sorts a loop's items (fractional).
    public var order: Double
    /// Items that must be done first; `ready`: every one of them is done.
    public var after: [String]
    public var ready: Bool
    /// A draft that does this step once it is ready.
    public var draft: String?
    /// The link (an agent or session ref) the item came from.
    public var source: String?
    public var check: ItemCheck?
    public var created: Date?
    public var done: Date?

    public init(id: String, text: String, owner: ItemOwner = .you, state: ItemState = .open, kind: ItemKind = .step, order: Double = 0,
                after: [String] = [], ready: Bool = true, draft: String? = nil, source: String? = nil, check: ItemCheck? = nil,
                created: Date? = nil, done: Date? = nil) {
        self.id = id; self.text = text; self.owner = owner; self.state = state; self.kind = kind; self.order = order
        self.after = after; self.ready = ready; self.draft = draft; self.source = source; self.check = check
        self.created = created; self.done = done
    }

    enum CodingKeys: String, CodingKey { case id, text, owner, state, kind, order, after, ready, draft, source, check, created, done }
    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        id = c.str(.id)
        text = c.str(.text)
        owner = c.value(ItemOwner.self, .owner) ?? .agent
        state = c.value(ItemState.self, .state) ?? .open
        kind = c.value(ItemKind.self, .kind) ?? .step
        order = c.double(.order)
        after = c.strings(.after)
        ready = c.bool(.ready) || after.isEmpty // nothing to wait for
        draft = c.opt(.draft)
        source = c.opt(.source)
        check = c.value(ItemCheck.self, .check).flatMap { $0.kind.isEmpty ? nil : $0 }
        created = c.date(.created)
        done = c.date(.done)
    }

    public var isOpen: Bool { state == .open }
    /// An open item that is the person's (offers never are: they only
    /// appear dimmed inside the loop).
    public var isYourMove: Bool { isOpen && owner == .you && kind != .offer }
}

/// One thing a loop links (`LoopLink`): an agent, a session, a project, a
/// URL, a file or another loop, and how it relates. Live state (`agent`,
/// `state`) is joined in by hesperd; the rest are settled facts.
public struct LoopLink: Equatable, Sendable, Identifiable, Decodable {
    public var kind: LinkKind
    public var ref: String
    public var rel: LinkRel
    public var from: String?
    public var label: String?
    public var machine: String?
    /// Its agent id here while it is live (nil once gone).
    public var agent: String?
    /// Its live state (agent links only; nil once gone).
    public var state: AgentState?
    public var ending: LoopEnding?
    public var summary: String?
    public var closedReason: String?
    public var closed: Date?
    public var result: String?
    public var added: Date?

    public init(kind: LinkKind, ref: String, rel: LinkRel = .member, from: String? = nil, label: String? = nil, machine: String? = nil,
                agent: String? = nil, state: AgentState? = nil, ending: LoopEnding? = nil, summary: String? = nil,
                closedReason: String? = nil, closed: Date? = nil, result: String? = nil, added: Date? = nil) {
        self.kind = kind; self.ref = ref; self.rel = rel; self.from = from; self.label = label; self.machine = machine
        self.agent = agent; self.state = state; self.ending = ending; self.summary = summary
        self.closedReason = closedReason; self.closed = closed; self.result = result; self.added = added
    }

    enum CodingKeys: String, CodingKey { case kind, ref, rel, from, label, machine, agent, state, ending, summary, closedReason, closed, result, added }
    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        kind = LinkKind(wire: c.opt(.kind))
        ref = c.str(.ref)
        rel = c.value(LinkRel.self, .rel) ?? .member
        from = c.opt(.from)
        label = c.opt(.label)
        machine = c.opt(.machine)
        agent = c.opt(.agent)
        state = c.opt(.state).map { AgentState(rawValue: $0) ?? .unknown }
        ending = c.opt(.ending).flatMap(LoopEnding.init(rawValue:))
        summary = c.opt(.summary)
        closedReason = c.opt(.closedReason)
        closed = c.date(.closed)
        result = c.opt(.result)
        added = c.date(.added)
    }

    /// Unique within a loop: what, how and from where.
    public var id: String { "\(kind.wire)|\(rel.rawValue)|\(from ?? "")|\(ref)" }

    /// The agent is live here (a member agent that is not gone).
    public var isLive: Bool { kind == .agent && agent != nil && closed == nil }

    /// What to call it: its label, else the ref's last part.
    public var title: String {
        if let label, !label.isEmpty { return label }
        switch kind {
        case .file: return (ref.split(separator: ":", maxSplits: 1).last.map(String.init).map { ($0 as NSString).lastPathComponent }) ?? ref
        case .agent: return String(ref.split(separator: "/").last ?? Substring(ref))
        default: return ref
        }
    }

    /// Quiet: not working, not waiting in approval or question.
    public var isQuiet: Bool {
        guard let state, isLive else { return true }
        return !(state == .working || state == .starting || state == .approval || state == .question)
    }

    var json: JSONValue {
        var o: [String: JSONValue] = ["kind": .string(kind.wire), "ref": .string(ref), "rel": .string(rel.rawValue)]
        if let from { o["from"] = .string(from) }
        if let label { o["label"] = .string(label) }
        if let machine { o["machine"] = .string(machine) }
        if let agent { o["agent"] = .string(agent) }
        return .object(o)
    }
}

/// `LoopCounts`: a loop's numbers for boards and chips.
public struct LoopCounts: Equatable, Sendable, Decodable {
    public var agents = 0
    public var working = 0
    public var openYou = 0
    public var openAgents = 0
    public var openOthers = 0
    public var sessions = 0

    public init(agents: Int = 0, working: Int = 0, openYou: Int = 0, openAgents: Int = 0, openOthers: Int = 0, sessions: Int = 0) {
        self.agents = agents; self.working = working; self.openYou = openYou
        self.openAgents = openAgents; self.openOthers = openOthers; self.sessions = sessions
    }

    enum CodingKeys: String, CodingKey { case agents, working, openYou, openAgents, openOthers, sessions }
    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        agents = c.int(.agents); working = c.int(.working); openYou = c.int(.openYou)
        openAgents = c.int(.openAgents); openOthers = c.int(.openOthers); sessions = c.int(.sessions)
    }
}

/// One loop (`loops.list`, `loops.get`, `loops.changed`).
public struct Loop: Equatable, Sendable, Identifiable, Decodable {
    public var id: String
    public var title: String
    public var goal: String?
    public var doneWhen: String?
    public var status: LoopStatus
    /// Who acts next and what (the person's open item, the agent's
    /// question, "Looks done").
    public var next: LoopNext
    public var nextText: String?
    public var looksDone: Bool
    /// The milestone it is filed under (nil: unfiled).
    public var milestone: String?
    public var parkedUntil: Date?
    /// Merged into that loop (listed only by loops.get).
    public var mergedInto: String?
    public var items: [LoopItem]
    public var links: [LoopLink]
    public var counts: LoopCounts
    /// The projects its members work in.
    public var projects: [String]
    public var created: Date?
    public var updated: Date?
    public var closed: Date?

    public init(id: String, title: String, goal: String? = nil, doneWhen: String? = nil, status: LoopStatus = .open, next: LoopNext = .agents,
                nextText: String? = nil, looksDone: Bool = false, milestone: String? = nil, parkedUntil: Date? = nil, mergedInto: String? = nil,
                items: [LoopItem] = [], links: [LoopLink] = [], counts: LoopCounts = LoopCounts(), projects: [String] = [],
                created: Date? = nil, updated: Date? = nil, closed: Date? = nil) {
        self.id = id; self.title = title; self.goal = goal; self.doneWhen = doneWhen; self.status = status; self.next = next
        self.nextText = nextText; self.looksDone = looksDone; self.milestone = milestone; self.parkedUntil = parkedUntil
        self.mergedInto = mergedInto; self.items = items; self.links = links; self.counts = counts; self.projects = projects
        self.created = created; self.updated = updated; self.closed = closed
    }

    enum CodingKeys: String, CodingKey {
        case id, title, goal, doneWhen, status, next, nextText, looksDone, milestone, parkedUntil, mergedInto, items, links, counts, projects
        case created, updated, closed
    }

    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        id = try c.decode(String.self, forKey: .id)
        title = c.str(.title)
        goal = c.opt(.goal)
        doneWhen = c.opt(.doneWhen)
        status = c.value(LoopStatus.self, .status) ?? .open
        // Without next (an older shape): from the status.
        next = c.value(LoopNext.self, .next) ?? (status == .parked ? .parked : status.isClosed ? .closed : .others)
        nextText = c.opt(.nextText)
        looksDone = c.bool(.looksDone)
        milestone = c.opt(.milestone)
        parkedUntil = c.date(.parkedUntil)
        mergedInto = c.opt(.mergedInto)
        items = c.list(LoopItem.self, .items).sorted { ($0.order, $0.id) < ($1.order, $1.id) }
        links = c.list(LoopLink.self, .links)
        counts = c.value(LoopCounts.self, .counts) ?? LoopCounts()
        projects = c.strings(.projects)
        created = c.date(.created)
        updated = c.date(.updated)
        closed = c.date(.closed)
    }

    /// What to call it ("Untitled loop" without a title).
    public var displayTitle: String {
        let t = title.trimmingCharacters(in: .whitespacesAndNewlines)
        return t.isEmpty ? "Untitled loop" : t
    }

    public var isClosed: Bool { status.isClosed || next == .closed }
    public var isMerged: Bool { mergedInto != nil }
    /// Shown on the board, in the map and in the inbox (not closed, not
    /// merged away).
    public var isActive: Bool { !isClosed && !isMerged }

    public var openItems: [LoopItem] { items.filter(\.isOpen) }
    /// Open items that are the person's, in order.
    public var yourItems: [LoopItem] { items.filter(\.isYourMove) }
    /// Open offers: listed dimmed inside the loop only.
    public var offers: [LoopItem] { items.filter { $0.isOpen && $0.kind == .offer } }
    public var agentLinks: [LoopLink] { links.filter { $0.kind == .agent } }
    /// The machines its agents and sessions run on.
    public var machines: Set<String> { Set(links.compactMap(\.machine)) }

    /// The last change (updated, else created).
    public var lastChange: Date { updated ?? created ?? .distantPast }

    /// Since when it waits on the person: its oldest open item of theirs,
    /// else its last change.
    public var waitingSince: Date {
        yourItems.compactMap(\.created).min() ?? lastChange
    }
}

/// An outcome a product works toward (`Goal`).
public struct Goal: Equatable, Sendable, Identifiable, Decodable {
    public var id: String
    /// The project or group it belongs to.
    public var scope: String
    public var title: String
    public var order: Double
    public var status: MilestoneStatus

    public init(id: String, scope: String, title: String, order: Double = 0, status: MilestoneStatus = .open) {
        self.id = id; self.scope = scope; self.title = title; self.order = order; self.status = status
    }

    enum CodingKeys: String, CodingKey { case id, scope, title, order, status }
    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        id = try c.decode(String.self, forKey: .id)
        scope = c.str(.scope); title = c.str(.title); order = c.double(.order)
        status = c.value(MilestoneStatus.self, .status) ?? .open
    }
}

/// A release or result of a product (`Milestone`): "done when" conditions,
/// an optional due date and the loops filed under it.
public struct Milestone: Equatable, Sendable, Identifiable, Decodable {
    public var id: String
    public var scope: String
    public var goal: String?
    public var title: String
    public var status: MilestoneStatus
    public var order: Double
    public var due: Date?
    public var autoFile: Bool
    /// Its "done when" conditions (kind condition).
    public var items: [LoopItem]
    /// Computed by hesperd: the loops filed under it, conditions done of
    /// total, stalled (no change in `LoopSettings.stallDays`).
    public var loops: [String]
    public var done: Int
    public var total: Int
    public var stalled: Bool
    public var nextText: String?
    public var created: Date?
    public var updated: Date?

    public init(id: String, scope: String, goal: String? = nil, title: String, status: MilestoneStatus = .open, order: Double = 0, due: Date? = nil,
                autoFile: Bool = false, items: [LoopItem] = [], loops: [String] = [], done: Int? = nil, total: Int? = nil, stalled: Bool = false,
                nextText: String? = nil, created: Date? = nil, updated: Date? = nil) {
        self.id = id; self.scope = scope; self.goal = goal; self.title = title; self.status = status; self.order = order; self.due = due
        self.autoFile = autoFile; self.items = items; self.loops = loops
        self.done = done ?? items.filter { $0.state == .done }.count
        self.total = total ?? items.filter { $0.state != .dropped }.count
        self.stalled = stalled; self.nextText = nextText; self.created = created; self.updated = updated
    }

    enum CodingKeys: String, CodingKey { case id, scope, goal, title, status, order, due, autoFile, items, loops, done, total, stalled, nextText, created, updated }
    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        id = try c.decode(String.self, forKey: .id)
        scope = c.str(.scope); goal = c.opt(.goal); title = c.str(.title)
        status = c.value(MilestoneStatus.self, .status) ?? .open
        order = c.double(.order); due = c.date(.due); autoFile = c.bool(.autoFile)
        items = c.list(LoopItem.self, .items).sorted { ($0.order, $0.id) < ($1.order, $1.id) }
        loops = c.strings(.loops)
        // Without done/total (an older shape): counted from the items.
        done = c.value(Int.self, .done) ?? items.filter { $0.state == .done }.count
        total = c.value(Int.self, .total) ?? items.filter { $0.state != .dropped }.count
        stalled = c.bool(.stalled); nextText = c.opt(.nextText)
        created = c.date(.created); updated = c.date(.updated)
    }

    /// Conditions done of total, 0…1 (0 without conditions).
    public var progress: Double { total > 0 ? min(1, Double(done) / Double(total)) : 0 }
}

/// `goals.list`'s result.
public struct GoalsList: Equatable, Sendable, Decodable {
    public var goals: [Goal]
    public var milestones: [Milestone]

    public init(goals: [Goal] = [], milestones: [Milestone] = []) { self.goals = goals; self.milestones = milestones }

    enum CodingKeys: String, CodingKey { case goals, milestones }
    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        goals = c.list(Goal.self, .goals); milestones = c.list(Milestone.self, .milestones)
    }
}

/// The Steward's shared configuration and, on the Mac that runs it, how
/// it is doing (`StewardStatus`, which embeds `StewardConfig`).
public struct StewardStatus: Equatable, Sendable, Decodable {
    public var enabled: Bool
    public var level: StewardLevel
    /// The projects-store node of the Mac that runs it, and that Mac's
    /// short name here.
    public var node: String?
    public var machine: String?
    /// Its agent id while it runs here.
    public var agent: String?
    public var queued: Int
    public var wakes: Int
    public var lastWake: Date?
    public var session: String?

    /// Off at level suggest: hesperd's default (decision 3, off by default).
    public init(enabled: Bool = false, level: StewardLevel = .suggest, node: String? = nil, machine: String? = nil, agent: String? = nil,
                queued: Int = 0, wakes: Int = 0, lastWake: Date? = nil, session: String? = nil) {
        self.enabled = enabled; self.level = level; self.node = node; self.machine = machine; self.agent = agent
        self.queued = queued; self.wakes = wakes; self.lastWake = lastWake; self.session = session
    }

    enum CodingKeys: String, CodingKey { case enabled, level, node, machine, agent, queued, wakes, lastWake, session }
    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        enabled = c.bool(.enabled)
        // A missing level is the default; an unknown one is watch.
        level = c.opt(.level).map { StewardLevel(rawValue: $0) ?? .watch } ?? .suggest
        node = c.opt(.node); machine = c.opt(.machine); agent = c.opt(.agent)
        queued = c.int(.queued); wakes = c.int(.wakes); lastWake = c.date(.lastWake); session = c.opt(.session)
    }
}

/// One event `steward.inbox` hands the Steward.
public struct StewardEvent: Equatable, Sendable, Decodable {
    /// settled, closed, loopCreated, review, evening (raw: newer kinds kept).
    public var kind: String
    public var at: Date?
    public var agent: String?
    public var loop: String?
    public var ending: LoopEnding?
    public var summary: String?
    public var reason: String?

    enum CodingKeys: String, CodingKey { case kind, at, agent, loop, ending, summary, reason }
    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        kind = c.str(.kind); at = c.date(.at); agent = c.opt(.agent); loop = c.opt(.loop)
        ending = c.opt(.ending).flatMap(LoopEnding.init(rawValue:))
        summary = c.opt(.summary); reason = c.opt(.reason)
    }
}

/// The shared loop settings (`loops.settings.get/set`); missing or zero
/// values are hesperd's defaults.
public struct LoopSettings: Equatable, Sendable, Decodable {
    public static let defaultLoopLimit = 5
    public static let defaultStallDays = 7
    public static let defaultEveningAt = "18:00"
    public static let defaultMaxWakesPerDay = 40

    /// Loops on the person before the start check shows its hint.
    public var loopLimit: Int
    /// A milestone with no change for this long is stalled.
    public var stallDays: Int
    /// The evening wrap-up time, "HH:MM" local.
    public var eveningAt: String
    public var maxWakesPerDay: Int

    public init(loopLimit: Int = defaultLoopLimit, stallDays: Int = defaultStallDays, eveningAt: String = defaultEveningAt,
                maxWakesPerDay: Int = defaultMaxWakesPerDay) {
        self.loopLimit = loopLimit; self.stallDays = stallDays; self.eveningAt = eveningAt; self.maxWakesPerDay = maxWakesPerDay
    }

    enum CodingKeys: String, CodingKey { case loopLimit, stallDays, eveningAt, maxWakesPerDay }
    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        let limit = c.int(.loopLimit), stall = c.int(.stallDays), wakes = c.int(.maxWakesPerDay)
        loopLimit = limit > 0 ? limit : Self.defaultLoopLimit
        stallDays = stall > 0 ? stall : Self.defaultStallDays
        eveningAt = Self.clock(c.str(.eveningAt)) == nil ? Self.defaultEveningAt : c.str(.eveningAt)
        maxWakesPerDay = wakes > 0 ? wakes : Self.defaultMaxWakesPerDay
    }

    /// "HH:MM" → (hour, minute); nil when it is not a time.
    public static func clock(_ s: String) -> (hour: Int, minute: Int)? {
        let parts = s.split(separator: ":")
        guard parts.count == 2, let h = Int(parts[0]), let m = Int(parts[1]), (0..<24).contains(h), (0..<60).contains(m) else { return nil }
        return (h, m)
    }

    /// Today's wrap-up time in `calendar`'s time zone.
    public func evening(on day: Date, calendar: Calendar = .current) -> Date {
        let (h, m) = Self.clock(eveningAt) ?? (18, 0)
        return calendar.date(bySettingHour: h, minute: m, second: 0, of: day) ?? day
    }

    var json: JSONValue {
        ["loopLimit": .number(Double(loopLimit)), "stallDays": .number(Double(stallDays)), "eveningAt": .string(eveningAt),
         "maxWakesPerDay": .number(Double(maxWakesPerDay))]
    }
}

// MARK: Changes (params)

/// `loops.save`: create a loop (no id) or change the fields given.
public struct LoopPatch: Equatable, Sendable {
    public var id: String?
    public var title: String?
    public var goal: String?
    public var doneWhen: String?
    public var status: LoopStatus?
    public var nextText: String?
    /// "" unfiles it.
    public var milestone: String?
    public var parkedUntil: Date?

    public init(id: String? = nil, title: String? = nil, goal: String? = nil, doneWhen: String? = nil, status: LoopStatus? = nil,
                nextText: String? = nil, milestone: String? = nil, parkedUntil: Date? = nil) {
        self.id = id; self.title = title; self.goal = goal; self.doneWhen = doneWhen; self.status = status
        self.nextText = nextText; self.milestone = milestone; self.parkedUntil = parkedUntil
    }

    public var params: JSONValue {
        var o: [String: JSONValue] = [:]
        if let id { o["id"] = .string(id) }
        if let title { o["title"] = .string(title) }
        if let goal { o["goal"] = .string(goal) }
        if let doneWhen { o["doneWhen"] = .string(doneWhen) }
        if let status { o["status"] = .string(status.rawValue) }
        if let nextText { o["nextText"] = .string(nextText) }
        if let milestone { o["milestone"] = .string(milestone) }
        if let parkedUntil { o["parkedUntil"] = .string(Agent.format(parkedUntil)) }
        return .object(o)
    }
}

/// What an `ItemPatch` does to an item's check: leave it, set it, or
/// clear it (sent as `"check": {"kind": ""}`).
public enum ItemCheckChange: Equatable, Sendable {
    case keep
    case set(ItemCheck)
    case clear

    /// The check it sets (nil: keep or clear).
    public var value: ItemCheck? { if case .set(let c) = self { return c } else { return nil } }

    var json: JSONValue? {
        switch self {
        case .keep: return nil
        case .set(let c): return c.json
        case .clear: return ["kind": ""]
        }
    }
}

/// `loops.item` / `goals.item`: add (no id), change or remove an item; only
/// the fields given change.
public struct ItemPatch: Equatable, Sendable {
    public var id: String?
    public var text: String?
    public var owner: ItemOwner?
    public var state: ItemState?
    public var kind: ItemKind?
    public var order: Double?
    public var after: [String]?
    public var draft: String?
    public var check: ItemCheckChange

    public init(id: String? = nil, text: String? = nil, owner: ItemOwner? = nil, state: ItemState? = nil, kind: ItemKind? = nil,
                order: Double? = nil, after: [String]? = nil, draft: String? = nil, check: ItemCheckChange = .keep) {
        self.id = id; self.text = text; self.owner = owner; self.state = state; self.kind = kind
        self.order = order; self.after = after; self.draft = draft; self.check = check
    }

    /// Tick an item off.
    public static func done(_ id: String) -> ItemPatch { ItemPatch(id: id, state: .done) }

    var json: JSONValue {
        var o: [String: JSONValue] = [:]
        if let id { o["id"] = .string(id) }
        if let text { o["text"] = .string(text) }
        if let owner { o["owner"] = .string(owner.rawValue) }
        if let state { o["state"] = .string(state.rawValue) }
        if let kind { o["kind"] = .string(kind.rawValue) }
        if let order { o["order"] = .number(order) }
        if let after { o["after"] = .array(after.map(JSONValue.string)) }
        if let draft { o["draft"] = .string(draft) }
        if let check = check.json { o["check"] = check }
        return .object(o)
    }
}

/// `goals.save {goal}`: create (no id) or change a goal.
public struct GoalPatch: Equatable, Sendable {
    public var id: String?
    public var scope: String?
    public var title: String?
    public var order: Double?
    public var status: MilestoneStatus?

    public init(id: String? = nil, scope: String? = nil, title: String? = nil, order: Double? = nil, status: MilestoneStatus? = nil) {
        self.id = id; self.scope = scope; self.title = title; self.order = order; self.status = status
    }

    public var params: JSONValue {
        var o: [String: JSONValue] = [:]
        if let id { o["id"] = .string(id) }
        if let scope { o["scope"] = .string(scope) }
        if let title { o["title"] = .string(title) }
        if let order { o["order"] = .number(order) }
        if let status { o["status"] = .string(status.rawValue) }
        return ["goal": .object(o)]
    }
}

/// `goals.save {milestone}`: create (no id) or change a milestone.
public struct MilestonePatch: Equatable, Sendable {
    public var id: String?
    public var scope: String?
    public var goal: String?
    public var title: String?
    public var status: MilestoneStatus?
    public var order: Double?
    public var due: Date?
    public var autoFile: Bool?

    public init(id: String? = nil, scope: String? = nil, goal: String? = nil, title: String? = nil, status: MilestoneStatus? = nil,
                order: Double? = nil, due: Date? = nil, autoFile: Bool? = nil) {
        self.id = id; self.scope = scope; self.goal = goal; self.title = title; self.status = status
        self.order = order; self.due = due; self.autoFile = autoFile
    }

    public var params: JSONValue {
        var o: [String: JSONValue] = [:]
        if let id { o["id"] = .string(id) }
        if let scope { o["scope"] = .string(scope) }
        if let goal { o["goal"] = .string(goal) }
        if let title { o["title"] = .string(title) }
        if let status { o["status"] = .string(status.rawValue) }
        if let order { o["order"] = .number(order) }
        if let due { o["due"] = .string(Agent.format(due)) }
        if let autoFile { o["autoFile"] = .bool(autoFile) }
        return ["milestone": .object(o)]
    }
}

/// `steward.set`: only the fields given change; `machine` moves it there.
public struct StewardPatch: Equatable, Sendable {
    public var enabled: Bool?
    public var level: StewardLevel?
    public var machine: String?

    public init(enabled: Bool? = nil, level: StewardLevel? = nil, machine: String? = nil) {
        self.enabled = enabled; self.level = level; self.machine = machine
    }

    public var isEmpty: Bool { enabled == nil && level == nil && machine == nil }

    public var params: JSONValue {
        var o: [String: JSONValue] = [:]
        if let enabled { o["enabled"] = .bool(enabled) }
        if let level { o["level"] = .string(level.rawValue) }
        if let machine { o["machine"] = .string(machine) }
        return .object(o)
    }
}

// MARK: Notifications

/// A `loops.*`, `goals.*` or `steward.*` notification, decoded (on the
/// reader thread: `DaemonClient.onLoopsNotification`).
public enum LoopsEvent: Equatable, Sendable {
    case loopChanged(Loop)
    case loopRemoved(String)
    case goalChanged(Goal)
    case milestoneChanged(Milestone)
    /// goals.removed: a goal's or a milestone's id.
    case goalRemoved(String)
    case stewardChanged(StewardStatus)

    /// nil: not a loops notification, or one without its payload.
    public static func decode(method: String, params: JSONValue) -> LoopsEvent? {
        switch method {
        case LoopsRPC.loopChanged:
            return (params["loop"].flatMap { try? $0.decode(Loop.self) }).map(LoopsEvent.loopChanged)
        case LoopsRPC.loopRemoved:
            return params["id"]?.stringValue.flatMap { $0.isEmpty ? nil : .loopRemoved($0) }
        case LoopsRPC.goalChanged:
            if let m = params["milestone"].flatMap({ try? $0.decode(Milestone.self) }) { return .milestoneChanged(m) }
            return (params["goal"].flatMap { try? $0.decode(Goal.self) }).map(LoopsEvent.goalChanged)
        case LoopsRPC.goalRemoved:
            return params["id"]?.stringValue.flatMap { $0.isEmpty ? nil : .goalRemoved($0) }
        case LoopsRPC.stewardChanged:
            return (params["steward"].flatMap { try? $0.decode(StewardStatus.self) }).map(LoopsEvent.stewardChanged)
        default:
            return nil
        }
    }
}

/// The app's copy of hesperd's loops, goals, milestones and Steward,
/// rebuilt from `loops.list` / `goals.list` and the notifications.
/// `version` grows on every change: derived models (LoopMap, StartCheck)
/// recompute only when it moved.
public struct LoopStore: Equatable, Sendable {
    public private(set) var loops: [String: Loop] = [:]
    public private(set) var goals: [String: Goal] = [:]
    public private(set) var milestones: [String: Milestone] = [:]
    public private(set) var steward = StewardStatus()
    public var settings = LoopSettings()
    /// hesperd answers loops.* (false: an older hesperd, no loops UI).
    public private(set) var supported = true
    public private(set) var version = 0

    /// The status LoopHub lists: every one. The store keeps done and
    /// dropped loops (the wrap-up's tallies, a milestone's finished loops);
    /// boards, chips, counts and the start check show active loops only.
    public static let listStatus = "all"

    public init(loops: [Loop] = [], goals: [Goal] = [], milestones: [Milestone] = [], steward: StewardStatus = StewardStatus(),
                settings: LoopSettings = LoopSettings()) {
        for l in loops { self.loops[l.id] = l }
        for g in goals { self.goals[g.id] = g }
        for m in milestones { self.milestones[m.id] = m }
        self.steward = steward
        self.settings = settings
    }

    /// The loops shown anywhere (not closed, not merged away).
    public var activeLoops: [Loop] { loops.values.filter(\.isActive) }

    public mutating func apply(_ e: LoopsEvent) {
        switch e {
        case .loopChanged(let l): loops[l.id] = l
        case .loopRemoved(let id): loops.removeValue(forKey: id)
        case .goalChanged(let g): goals[g.id] = g
        case .milestoneChanged(let m): milestones[m.id] = m
        case .goalRemoved(let id):
            goals.removeValue(forKey: id)
            milestones.removeValue(forKey: id)
        case .stewardChanged(let s): steward = s
        }
        supported = true
        version += 1
    }

    /// A fresh `loops.list` (nil: hesperd has no loops); it replaces every
    /// loop (a loop it lacks is gone).
    public mutating func listed(loops list: [Loop]?) {
        guard let list else { supported = false; loops = [:]; version += 1; return }
        supported = true
        loops = Dictionary(list.map { ($0.id, $0) }, uniquingKeysWith: { _, b in b })
        version += 1
    }

    public mutating func listed(goals list: GoalsList) {
        goals = Dictionary(list.goals.map { ($0.id, $0) }, uniquingKeysWith: { _, b in b })
        milestones = Dictionary(list.milestones.map { ($0.id, $0) }, uniquingKeysWith: { _, b in b })
        version += 1
    }

    public mutating func setSettings(_ s: LoopSettings) {
        guard s != settings else { return }
        settings = s
        version += 1
    }

    /// The loop an agent works in (`Agent.loop`), when it is known here.
    public func loop(of agent: Agent) -> Loop? { agent.loop.flatMap { loops[$0] } }
}

// MARK: Calls

// Every call decodes on the calling task (callers run them off the main
// actor). An older hesperd answers -32601: `LoopsRPC.missing`.
extension DaemonClient {
    static func loopList(_ v: JSONValue) -> [Loop] {
        let raw = v.arrayValue ?? v["loops"]?.arrayValue ?? []
        return raw.compactMap { try? $0.decode(Loop.self) }
    }

    /// The loops (all nil: every open and parked loop; status "all" adds
    /// done and dropped).
    public func loopsList(status: String? = nil, milestone: String? = nil, project: String? = nil, agent: String? = nil) async throws -> [Loop] {
        var p: [String: JSONValue] = [:]
        if let status { p["status"] = .string(status) }
        if let milestone { p["milestone"] = .string(milestone) }
        if let project { p["project"] = .string(project) }
        if let agent { p["agent"] = .string(agent) }
        return Self.loopList(try await call(LoopsRPC.list, .object(p), timeout: 30))
    }

    public func loopGet(_ id: String) async throws -> Loop { try await call(LoopsRPC.get, ["id": .string(id)]).decode(Loop.self) }

    /// Creates (no id) or changes a loop; returns it.
    @discardableResult
    public func loopSave(_ patch: LoopPatch) async throws -> Loop { try await call(LoopsRPC.save, patch.params).decode(Loop.self) }

    /// Adds (or removes) a link of loop `id`.
    public func loopLink(_ id: String, link: LoopLink, remove: Bool = false) async throws {
        var p: [String: JSONValue] = ["id": .string(id), "link": link.json]
        if remove { p["remove"] = .bool(true) }
        _ = try await call(LoopsRPC.link, .object(p))
    }

    /// Adds (no item id), changes or removes an item of loop `id`.
    public func loopItem(_ id: String, item: ItemPatch, remove: Bool = false) async throws {
        var p: [String: JSONValue] = ["id": .string(id), "item": item.json]
        if remove { p["remove"] = .bool(true) }
        _ = try await call(LoopsRPC.item, .object(p))
    }

    /// Merges loop `from` into loop `into`.
    public func loopMerge(into: String, from: String) async throws {
        _ = try await call(LoopsRPC.merge, ["into": .string(into), "from": .string(from)])
    }

    /// Moves the link `ref` (an agent with its descendants) of loop `id`
    /// into a new loop.
    public func loopSplit(_ id: String, ref: String, title: String? = nil) async throws {
        var p: [String: JSONValue] = ["id": .string(id), "ref": .string(ref)]
        if let title, !title.isEmpty { p["title"] = .string(title) }
        _ = try await call(LoopsRPC.split, .object(p))
    }

    public func loopSettings() async throws -> LoopSettings { try await call(LoopsRPC.settingsGet, .object([:])).decode(LoopSettings.self) }

    public func setLoopSettings(_ s: LoopSettings) async throws { _ = try await call(LoopsRPC.settingsSet, s.json) }

    /// Goals and milestones (scope nil: every product's).
    public func goalsList(scope: String? = nil) async throws -> GoalsList {
        try await call(LoopsRPC.goalsList, scope.map { ["scope": .string($0)] } ?? .object([:]), timeout: 30).decode(GoalsList.self)
    }

    public func goalSave(_ patch: GoalPatch) async throws { _ = try await call(LoopsRPC.goalsSave, patch.params) }

    public func milestoneSave(_ patch: MilestonePatch) async throws { _ = try await call(LoopsRPC.goalsSave, patch.params) }

    /// Removes a goal or a milestone (a person only).
    public func goalRemove(_ id: String) async throws { _ = try await call(LoopsRPC.goalsRemove, ["id": .string(id)]) }

    /// Adds, changes or removes a "done when" condition of milestone `id`.
    public func milestoneItem(_ id: String, item: ItemPatch, remove: Bool = false) async throws {
        var p: [String: JSONValue] = ["id": .string(id), "item": item.json]
        if remove { p["remove"] = .bool(true) }
        _ = try await call(LoopsRPC.goalsItem, .object(p))
    }

    /// Files loop `loop` under `milestone` (remove: unfiles it).
    public func goalFile(milestone: String, loop: String, remove: Bool = false) async throws {
        var p: [String: JSONValue] = ["milestone": .string(milestone), "loop": .string(loop)]
        if remove { p["remove"] = .bool(true) }
        _ = try await call(LoopsRPC.goalsFile, .object(p))
    }

    /// Accepts the proposed milestones of `scope` (ids nil: all of them).
    public func goalsAccept(scope: String, ids: [String]? = nil) async throws {
        var p: [String: JSONValue] = ["scope": .string(scope)]
        if let ids { p["ids"] = .array(ids.map(JSONValue.string)) }
        _ = try await call(LoopsRPC.goalsAccept, .object(p))
    }

    public func stewardStatus() async throws -> StewardStatus { try await call(LoopsRPC.stewardStatus, .object([:])).decode(StewardStatus.self) }

    @discardableResult
    public func stewardSet(_ patch: StewardPatch) async throws -> StewardStatus {
        let v = try await call(LoopsRPC.stewardSet, patch.params)
        return (try? v.decode(StewardStatus.self)) ?? StewardStatus()
    }

    /// The Steward's waiting events (the Steward's own call; shown in
    /// Settings for a look only).
    public func stewardInbox() async throws -> [StewardEvent] {
        let v = try await call(LoopsRPC.stewardInbox, .object([:]))
        return (v.arrayValue ?? v["events"]?.arrayValue ?? []).compactMap { try? $0.decode(StewardEvent.self) }
    }

    /// Starts a planning session for a project or group; returns the
    /// planner agent's id when hesperd names it.
    @discardableResult
    public func stewardPlan(scope: String, prompt: String? = nil) async throws -> String? {
        var p: [String: JSONValue] = ["scope": .string(scope)]
        if let prompt, !prompt.isEmpty { p["prompt"] = .string(prompt) }
        let v = try await call(LoopsRPC.stewardPlan, .object(p), timeout: 30)
        return v["id"]?.stringValue ?? v["agent"]?.stringValue
    }
}
