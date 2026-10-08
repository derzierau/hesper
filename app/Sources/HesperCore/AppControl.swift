import Foundation

// App control (docs/rebuild-contract.md "As built — app control"): hesperd
// forwards app.state / app.open / app.wall.set / app.desk from hesperctl
// to the app that registered (app.register). This file is the pure part:
// reading the params, naming scopes, bands and projects, and what a
// wall.set does to a wall's view. The AppKit side
// (app/Sources/Hesper/App/AppControl.swift) runs the same actions as the
// menus and keys.

/// The methods hesperd forwards to the app.
public enum AppControlMethod: String, CaseIterable, Sendable {
    case state = "app.state"
    case open = "app.open"
    case wallSet = "app.wall.set"
    case desk = "app.desk"
}

/// An error the app answers with: `code` is the daemon's data.code
/// (invalid, not_found, unavailable), so hesperctl exits as for its own.
public struct AppControlError: Error, Equatable, Sendable, CustomStringConvertible {
    public var code: String
    public var message: String
    public init(_ code: String, _ message: String) { self.code = code; self.message = message }
    public var description: String { message }

    public static func invalid(_ m: String) -> AppControlError { AppControlError("invalid", m) }
    public static func notFound(_ m: String) -> AppControlError { AppControlError("not_found", m) }

    /// As a JSON-RPC error (-32602 for bad params, else -32000).
    public var rpc: RPCError {
        RPCError(code: code == "invalid" ? -32602 : -32000, message: message,
                 kind: DaemonErrorCode(rawValue: code) ?? .unknown, data: ["code": .string(code)])
    }
}

// MARK: Params

private extension JSONValue {
    func string(_ key: String) throws -> String? {
        guard let v = self[key], v != .null else { return nil }
        guard let s = v.stringValue else { throw AppControlError.invalid("\(key) must be a string") }
        let t = s.trimmingCharacters(in: .whitespacesAndNewlines)
        return t.isEmpty ? nil : t
    }

    func bool(_ key: String) throws -> Bool? {
        guard let v = self[key], v != .null else { return nil }
        guard let b = v.boolValue else { throw AppControlError.invalid("\(key) must be true or false") }
        return b
    }

    func strings(_ key: String) throws -> [String]? {
        guard let v = self[key], v != .null else { return nil }
        if let s = v.stringValue { return s.split(separator: ",").map { $0.trimmingCharacters(in: .whitespaces) }.filter { !$0.isEmpty } }
        guard let a = v.arrayValue else { throw AppControlError.invalid("\(key) must be a list of strings") }
        return try a.map {
            guard let s = $0.stringValue else { throw AppControlError.invalid("\(key) must be a list of strings") }
            return s
        }
    }

    func checkObject() throws {
        if self == .null { return }
        guard objectValue != nil else { throw AppControlError.invalid("params must be an object") }
    }
}

/// Which wall a request means.
public enum WallRef: Equatable, Sendable {
    /// The frontmost wall (the key window's, else the last one in front).
    case current
    /// The home wall (the first of the desk).
    case home
    case id(String)

    public init(_ s: String?) {
        switch s?.lowercased() {
        case nil, "", "current", "front": self = .current
        case "home", "first": self = .home
        default: self = .id(s!)
        }
    }

    /// The wall's id among `walls` (desk order, home first); `current`:
    /// the frontmost. A wall may also be named by its 1-based position.
    public func resolve(walls: [String], current: String) throws -> String {
        switch self {
        case .current: return current
        case .home:
            guard let h = walls.first else { throw AppControlError.notFound("no wall is open") }
            return h
        case .id(let id):
            if walls.contains(id) { return id }
            if let n = Int(id), n >= 1, n <= walls.count { return walls[n - 1] }
            throw AppControlError.notFound("no wall \(id) (walls: \(walls.joined(separator: ", ")))")
        }
    }
}

/// How `app.open` shows an agent.
public enum AppOpenMode: String, CaseIterable, Sendable {
    /// The focus view of the wall that shows it (⌘↩), or its own window.
    case focus
    /// Selected on the wall that shows it (no focus view).
    case wall
    /// Its own window (⇧-click, ⌥⌘↩).
    case window
    /// A tab of the frontmost agent window (⌥⇧-click).
    case tab
}

/// A new agent's composer, filled in but not started (`open new`).
public struct ComposerPrefill: Equatable, Sendable {
    /// A folder (absolute or ~) or a project's id or name.
    public var project: String?
    public var task: String?
    /// claude | codex | shell: the kind's default profile, unless `profile`.
    public var kind: String?
    public var profile: String?
    public var machine: String?
    public var worktree: Bool?
    public var branch: String?

    public init(project: String? = nil, task: String? = nil, kind: String? = nil, profile: String? = nil, machine: String? = nil,
                worktree: Bool? = nil, branch: String? = nil) {
        self.project = project; self.task = task; self.kind = kind; self.profile = profile; self.machine = machine
        self.worktree = worktree; self.branch = branch
    }

    static func parse(_ v: JSONValue) throws -> ComposerPrefill {
        try v.checkObject()
        let p = ComposerPrefill(project: try v.string("project"), task: v["task"]?.stringValue, kind: try v.string("kind"),
                                profile: try v.string("profile"), machine: try v.string("machine"),
                                worktree: try v.bool("worktree"), branch: try v.string("branch"))
        if let k = p.kind, !["claude", "codex", "shell"].contains(k) {
            throw AppControlError.invalid("kind must be claude, codex or shell, not \(k)")
        }
        return p
    }
}

/// `app.open`: exactly one thing to show.
public struct AppOpenRequest: Equatable, Sendable {
    public enum Target: Equatable, Sendable {
        /// An agent, the way `mode` says.
        case agent(String, AppOpenMode)
        /// A wall forward. With `scope`: the named wall (`wall` given)
        /// takes the scope (the sidebar's click); otherwise a wall that
        /// has the scope comes forward, else a new wall window with it
        /// (the sidebar's ⌥-click). `newWall`: always a new wall window.
        case wall(WallRef?, scope: String?, newWall: Bool)
        /// A draft in the wall's composer (⌘N), prefilled.
        case composer(ComposerPrefill, wall: WallRef)
        /// History (⌘Y), searching `query`.
        case history(query: String?)
        /// The ⌘J inbox (agents needing you).
        case inbox

        /// The wall it shows leaves its focus view first (⌘Esc): showing
        /// a wall or a composer means the wall, not the focused agent.
        public var showsWall: Bool {
            switch self {
            case .wall, .composer: return true
            case .agent(_, let mode): return mode == .wall
            case .history, .inbox: return false
            }
        }
    }

    public var target: Target
    public init(target: Target) { self.target = target }

    /// `{agent?, mode?, wall?, scope?, newWall?, composer?, history?, inbox?}`.
    public static func parse(_ p: JSONValue) throws -> AppOpenRequest {
        try p.checkObject()
        let agent = try p.string("agent")
        let modeText = try p.string("mode")
        var mode: AppOpenMode?
        if let m = modeText {
            guard let v = AppOpenMode(rawValue: m) else {
                throw AppControlError.invalid("mode must be \(AppOpenMode.allCases.map(\.rawValue).joined(separator: ", ")), not \(m)")
            }
            mode = v
        }
        let wallText = try p.string("wall")
        let wall = WallRef(wallText)
        let scope = try p.string("scope")
        let inbox = try p.bool("inbox") ?? false
        var history: String??
        if let h = p["history"], h != .null {
            switch h {
            case .bool(true): history = .some(nil)
            case .bool(false): history = nil
            case .string(let q): history = .some(q.isEmpty ? nil : q)
            case .object: history = .some(try h.string("query"))
            default: throw AppControlError.invalid("history must be {query} or true")
            }
        }
        var composer: ComposerPrefill?
        if let c = p["composer"], c != .null { composer = try ComposerPrefill.parse(c) }

        let picked = [agent != nil, composer != nil, history != nil, inbox].filter { $0 }.count
        guard picked <= 1 else { throw AppControlError.invalid("open one thing: agent, composer, history or inbox") }
        if let agent {
            return AppOpenRequest(target: .agent(agent, mode ?? .focus))
        }
        if let composer { return AppOpenRequest(target: .composer(composer, wall: wall)) }
        if let history { return AppOpenRequest(target: .history(query: history)) }
        if inbox { return AppOpenRequest(target: .inbox) }
        if let mode, mode != .wall { throw AppControlError.invalid("mode \(mode.rawValue) needs an agent") }
        return AppOpenRequest(target: .wall(wallText.map(WallRef.init), scope: scope, newWall: try p.bool("newWall") ?? false))
    }
}

/// An agent named on the command line: its full id, its local id (the
/// part after the machine) or its name when unique.
public enum AgentRef {
    public static func resolve(_ ref: String, agents: [Agent]) throws -> String {
        if agents.contains(where: { $0.id == ref }) { return ref }
        let local = agents.filter { $0.id.split(separator: "/").last.map(String.init) == ref }
        if local.count == 1 { return local[0].id }
        let named = agents.filter { $0.name == ref }
        if named.count == 1 { return named[0].id }
        if local.count + named.count > 1 {
            let ids = (local + named).map(\.id)
            throw AppControlError.invalid("\(ref) names \(ids.count) agents: use an id (\(ids.joined(separator: ", ")))")
        }
        throw AppControlError.notFound("no agent \(ref)")
    }
}

/// Card width: "dense" (60 characters), "normal" (80) or a number.
public enum Density {
    public static let dense = 60, normal = 80

    public static func minChars(_ s: String) throws -> Int {
        switch s.lowercased() {
        case "dense", "compact": return dense
        case "normal", "comfortable", "default": return normal
        default:
            guard let n = Int(s), (20...400).contains(n) else {
                throw AppControlError.invalid("density must be dense, normal or a number of characters (20–400), not \(s)")
            }
            return n
        }
    }

    public static func name(_ minChars: Int) -> String {
        minChars == dense ? "dense" : minChars == normal ? "normal" : "\(minChars)"
    }
}

/// `app.wall.set`: what changes on one wall (nil: unchanged).
public struct WallSetRequest: Equatable, Sendable {
    public var wall: WallRef = .current
    public var arrangement: WallArrangement?
    public var grouping: Grouping?
    public var minChars: Int?
    /// Bands by key or title (case-insensitive); "all" in collapse: every band.
    public var collapse: [String] = []
    public var expand: [String] = []
    public var bandOrder: [String]?
    public var scope: String?
    public var sidebar: Bool?
    public var ownWalls: OwnWallMode?
    /// Make it the home wall (Window ▸ Make Home Wall).
    public var home = false

    public init() {}

    public var isEmpty: Bool {
        arrangement == nil && grouping == nil && minChars == nil && collapse.isEmpty && expand.isEmpty && bandOrder == nil
            && scope == nil && sidebar == nil && ownWalls == nil && !home
    }

    /// Arrangements by their raw name, also "grid+shelf", "main+stack", "main-stack".
    public static func arrangement(_ s: String) throws -> WallArrangement {
        let k = s.lowercased().replacingOccurrences(of: "+", with: "").replacingOccurrences(of: "-", with: "").replacingOccurrences(of: " ", with: "")
        if let a = WallArrangement.allCases.first(where: { $0.rawValue.lowercased() == k }) { return a }
        switch k {
        case "gridshelf": return .shelf
        case "stack", "main": return .mainStack
        default: throw AppControlError.invalid("arrangement must be \(WallArrangement.allCases.map(\.rawValue).joined(separator: ", ")), not \(s)")
        }
    }

    public static func parse(_ p: JSONValue) throws -> WallSetRequest {
        try p.checkObject()
        var r = WallSetRequest()
        r.wall = WallRef(try p.string("wall"))
        if let a = try p.string("arrangement") { r.arrangement = try arrangement(a) }
        if let g = try p.string("grouping") {
            guard let v = Grouping(rawValue: g.lowercased()) else {
                throw AppControlError.invalid("grouping must be \(Grouping.allCases.map(\.rawValue).joined(separator: ", ")), not \(g)")
            }
            r.grouping = v
        }
        if let n = p["minChars"]?.doubleValue {
            r.minChars = try Density.minChars(String(Int(n)))
        } else if let d = try p.string("density") ?? p.string("minChars") {
            r.minChars = try Density.minChars(d)
        }
        r.collapse = try p.strings("collapse") ?? []
        r.expand = try p.strings("expand") ?? []
        r.bandOrder = try p.strings("bandOrder")
        r.scope = try p.string("scope")
        r.sidebar = try p.bool("sidebar")
        if let o = try p.string("ownWalls") {
            guard let v = OwnWallMode(rawValue: o.lowercased()) else {
                throw AppControlError.invalid("ownWalls must be \(OwnWallMode.allCases.map(\.rawValue).joined(separator: ", ")), not \(o)")
            }
            r.ownWalls = v
        }
        r.home = try p.bool("home") ?? false
        guard !r.isEmpty else { throw AppControlError.invalid("nothing to set (arrangement, grouping, density, collapse, expand, bandOrder, scope, sidebar, ownWalls, home)") }
        return r
    }
}

/// A wall's view settings, as wall.set changes them.
public struct WallViewSettings: Equatable, Sendable {
    public var arrangement: WallArrangement
    public var grouping: Grouping
    public var minChars: Int
    public var collapsed: Set<String>
    public var bandOrder: [String]
    public var sidebar: Bool
    public var ownWalls: OwnWallMode

    public init(arrangement: WallArrangement = .shelf, grouping: Grouping = .auto, minChars: Int = 80, collapsed: Set<String> = [],
                bandOrder: [String] = [], sidebar: Bool = false, ownWalls: OwnWallMode = .collapsed) {
        self.arrangement = arrangement; self.grouping = grouping; self.minChars = minChars; self.collapsed = collapsed
        self.bandOrder = bandOrder; self.sidebar = sidebar; self.ownWalls = ownWalls
    }

    /// Step 1: everything but bands (grouping first decides the bands).
    public func applying(_ r: WallSetRequest) -> WallViewSettings {
        var s = self
        if let a = r.arrangement { s.arrangement = a }
        if let g = r.grouping { s.grouping = g }
        if let m = r.minChars { s.minChars = m }
        if let b = r.sidebar { s.sidebar = b }
        if let o = r.ownWalls { s.ownWalls = o }
        return s
    }

    /// Step 2: collapse / expand / band order against the wall's bands
    /// after step 1. Unknown bands are an error (nothing changes).
    public func applyingBands(_ r: WallSetRequest, bands: [Band]) throws -> WallViewSettings {
        var s = self
        for ref in r.expand {
            if ref.lowercased() == "all" { s.collapsed = []; continue }
            s.collapsed.remove(try BandRef.resolve(ref, bands: bands))
        }
        for ref in r.collapse {
            if ref.lowercased() == "all" { s.collapsed.formUnion(bands.filter { !$0.isNew }.map(\.key)); continue }
            s.collapsed.insert(try BandRef.resolve(ref, bands: bands))
        }
        if let order = r.bandOrder { s.bandOrder = try order.map { try BandRef.resolve($0, bands: bands) } }
        return s
    }
}

/// A band named on the command line: its key ("p:<project>", "g:<group>",
/// "b:br:<branch>"), its title, or the project / group id it shows.
public enum BandRef {
    public static func resolve(_ ref: String, bands: [Band]) throws -> String {
        if let b = bands.first(where: { $0.key == ref }) { return b.key }
        let l = ref.lowercased()
        let byTitle = bands.filter { $0.title.lowercased() == l }
        if byTitle.count == 1 { return byTitle[0].key }
        if byTitle.count > 1 { throw AppControlError.invalid("\(ref) names \(byTitle.count) bands: use a key (\(byTitle.map(\.key).joined(separator: ", ")))") }
        if let b = bands.first(where: { $0.projectID == ref || $0.groupID == ref || $0.branch == ref }) { return b.key }
        let known = bands.map { "\($0.title) (\($0.key))" }.joined(separator: ", ")
        throw AppControlError.notFound("no band \(ref) on this wall" + (known.isEmpty ? " (it shows no bands)" : ": \(known)"))
    }
}

/// A project named on the command line: its id, its name, or a folder
/// (one of its paths).
public enum ProjectRef {
    public static func resolve(_ ref: String, catalog: ProjectCatalog, home: String = NSHomeDirectory()) -> String? {
        if catalog.projects[ref] != nil { return ref }
        let l = ref.lowercased()
        let named = catalog.projects.values.filter { !$0.synthesized && $0.name.lowercased() == l }.sorted { $0.id < $1.id }
        if let p = named.first { return p.id }
        let path = expand(ref, home: home)
        return catalog.projects.values.sorted { $0.id < $1.id }.first { $0.paths.values.contains(path) }?.id
    }

    public static func expand(_ path: String, home: String = NSHomeDirectory()) -> String {
        if path == "~" { return home }
        if path.hasPrefix("~/") { return home + String(path.dropFirst(1)) }
        return path
    }

    public static func isFolder(_ ref: String) -> Bool { ref.hasPrefix("/") || ref.hasPrefix("~") || ref.hasPrefix(".") }
}

/// Scopes as text: "all", "overflow", "needs-you", "working",
/// "project:<id|name>", "group:<id|name>", "filter:project=…,group=…,
/// machine=…,kind=…,state=needsYou|working|quiet|ended" (repeat a key for
/// more values: OR within a key, AND across keys).
public enum ScopeSpec {
    public static let help = "all | overflow | needs-you | working | project:<id|name> | group:<id|name> | filter:machine=M,kind=K,state=needsYou|working|quiet|ended,project=P,group=G"

    public static func parse(_ s: String, catalog: ProjectCatalog) throws -> WallScope {
        let t = s.trimmingCharacters(in: .whitespaces)
        let (head, rest) = split(t)
        switch head.lowercased() {
        case "all": return .all
        case "overflow": return .overflow
        case "needs-you", "needsyou", "attention": return ScopeSegment.needsYou.scope
        case "working": return ScopeSegment.working.scope
        case "project":
            guard let r = rest, let id = ProjectRef.resolve(r, catalog: catalog) else { throw AppControlError.notFound("no project \(rest ?? "") (hesperctl projects ls lists them)") }
            return .project(id)
        case "group":
            guard let r = rest else { throw AppControlError.invalid("group:<id|name>") }
            if catalog.groups[r] != nil { return .group(r) }
            if let g = catalog.groups.values.sorted(by: { $0.id < $1.id }).first(where: { $0.name.lowercased() == r.lowercased() }) { return .group(g.id) }
            throw AppControlError.notFound("no group \(r)")
        case "filter":
            var f = WallFilter()
            for part in (rest ?? "").split(separator: ",") {
                let kv = part.split(separator: "=", maxSplits: 1).map { $0.trimmingCharacters(in: .whitespaces) }
                guard kv.count == 2, !kv[1].isEmpty else { throw AppControlError.invalid("filter terms are key=value: \(part)") }
                switch kv[0].lowercased() {
                case "machine": f.machines.insert(kv[1])
                case "kind": f.kinds.insert(kv[1])
                case "state":
                    guard let g = StateGroup.allCases.first(where: { $0.rawValue.lowercased() == kv[1].lowercased().replacingOccurrences(of: "-", with: "") }) else {
                        throw AppControlError.invalid("state must be \(StateGroup.allCases.map(\.rawValue).joined(separator: ", ")), not \(kv[1])")
                    }
                    f.states.insert(g)
                case "project":
                    guard let id = ProjectRef.resolve(kv[1], catalog: catalog) else { throw AppControlError.notFound("no project \(kv[1])") }
                    f.projects.insert(id)
                case "group":
                    guard let g = catalog.groups[kv[1]]?.id ?? catalog.groups.values.first(where: { $0.name.lowercased() == kv[1].lowercased() })?.id else {
                        throw AppControlError.notFound("no group \(kv[1])")
                    }
                    f.groups.insert(g)
                default: throw AppControlError.invalid("filter keys are machine, kind, state, project, group; not \(kv[0])")
                }
            }
            return .filter(f)
        default:
            // A bare project or group name.
            if rest == nil, let id = ProjectRef.resolve(t, catalog: catalog) { return .project(id) }
            if rest == nil, let g = catalog.groups.values.first(where: { $0.id == t || $0.name.lowercased() == t.lowercased() }) { return .group(g.id) }
            throw AppControlError.invalid("scope must be \(help); not \(s)")
        }
    }

    private static func split(_ s: String) -> (String, String?) {
        guard let i = s.firstIndex(of: ":") else { return (s, nil) }
        let r = s[s.index(after: i)...].trimmingCharacters(in: .whitespaces)
        return (String(s[..<i]), r.isEmpty ? nil : r)
    }

    /// The text `parse` reads back.
    public static func format(_ scope: WallScope) -> String {
        switch scope {
        case .all: return "all"
        case .overflow: return "overflow"
        case .project(let p): return "project:\(p)"
        case .group(let g): return "group:\(g)"
        case .filter(let f):
            if f == WallFilter(states: [.needsYou]) { return "needs-you" }
            if f == WallFilter(states: [.working]) { return "working" }
            var parts: [String] = []
            parts += f.machines.sorted().map { "machine=\($0)" }
            parts += f.kinds.sorted().map { "kind=\($0)" }
            parts += StateGroup.allCases.filter { f.states.contains($0) }.map { "state=\($0.rawValue)" }
            parts += f.projects.sorted().map { "project=\($0)" }
            parts += f.groups.sorted().map { "group=\($0)" }
            return "filter:" + parts.joined(separator: ",")
        }
    }
}

/// `app.desk`.
public struct DeskRequest: Equatable, Sendable {
    public enum Action: String, CaseIterable, Sendable { case list, save, `switch`, rename, remove }
    public var action: Action
    public var name: String?
    public var newName: String?

    public init(action: Action, name: String? = nil, newName: String? = nil) { self.action = action; self.name = name; self.newName = newName }

    public static func parse(_ p: JSONValue) throws -> DeskRequest {
        try p.checkObject()
        let a = try p.string("action") ?? "list"
        let aliases: [String: Action] = ["ls": .list, "rm": .remove, "delete": .remove, "use": .switch, "select": .switch]
        guard let action = Action(rawValue: a) ?? aliases[a] else {
            throw AppControlError.invalid("action must be \(Action.allCases.map(\.rawValue).joined(separator: ", ")), not \(a)")
        }
        let r = DeskRequest(action: action, name: try p.string("name"), newName: try p.string("newName"))
        if action != .list && r.name == nil { throw AppControlError.invalid("desk \(action.rawValue) needs a name") }
        if action == .rename && r.newName == nil { throw AppControlError.invalid("desk rename needs newName") }
        return r
    }

    /// The desk a name means among `desks` (id, else name, case-insensitive;
    /// this setup's desks first).
    public static func find(_ name: String, in desks: [Desk], setup: String) -> Desk? {
        if let d = desks.first(where: { $0.id == name }) { return d }
        let l = name.lowercased()
        let named = desks.filter { $0.name.lowercased() == l }
        return named.first { $0.setup == setup } ?? named.first
    }
}
