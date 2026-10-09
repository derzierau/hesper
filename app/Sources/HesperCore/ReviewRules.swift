import Foundation

// Review's pure rules: reading order, the inbox's ranking, which work may
// be accepted in bulk, the evidence badge and the texts the wall shows.

// MARK: Reading order

/// Files in reading order: hesperd's `order` when it gave one (risky and
/// depended-on first, tests next to their code, formatting-only and
/// generated last), else the same rules here. Never alphabetical: the
/// file read last gets the least attention.
public enum ReviewOrder {
    public static func apply(_ files: [ReviewFile]) -> [ReviewFile] {
        let indexed = Array(files.enumerated())
        if files.contains(where: { $0.order != nil }) {
            return indexed.sorted { a, b in
                let x = a.element.order ?? Int.max, y = b.element.order ?? Int.max
                return x != y ? x < y : a.offset < b.offset
            }.map(\.element)
        }
        // No order from hesperd: rank here. A test follows the code it tests.
        let ranked = indexed.sorted { a, b in
            let x = rank(a.element), y = rank(b.element)
            return x != y ? x < y : a.offset < b.offset
        }.map(\.element)
        let codeStems = Set(ranked.filter { !isTest($0.path) }.map { stem($0.path) })
        var out: [ReviewFile] = []
        var placedTests = Set<String>()
        for f in ranked {
            if isTest(f.path) {
                if !codeStems.contains(stem(f.path)) { out.append(f) } // no code of its own here: in rank order
                continue
            }
            out.append(f)
            for t in ranked where isTest(t.path) && stem(t.path) == stem(f.path) && !placedTests.contains(t.path) {
                out.append(t)
                placedTests.insert(t.path)
            }
        }
        return out
    }

    /// Lower first: folded files last; then risk (high first), schema and
    /// config before code, code before tests and docs.
    static func rank(_ f: ReviewFile) -> Int {
        if f.foldedByDefault { return 900 + (f.generated ? 50 : 0) }
        var r = (2 - f.risk.rank) * 100
        let p = f.path.lowercased()
        if ReviewBulk.sensitive(p) { r += 0 } else if isConfig(p) { r += 10 } else if isTest(p) { r += 30 } else if isDoc(p) { r += 40 } else { r += 20 }
        return r
    }

    static func isConfig(_ p: String) -> Bool {
        let name = (p as NSString).lastPathComponent.lowercased()
        let ext = (name as NSString).pathExtension
        return ["json", "yaml", "yml", "toml", "ini", "plist", "mod", "lock", "env", "cfg", "conf"].contains(ext)
            || ["makefile", "dockerfile", "package.swift", "go.mod", "package.json"].contains(name)
    }

    static func isDoc(_ p: String) -> Bool {
        let ext = (p as NSString).pathExtension.lowercased()
        return ["md", "txt", "rst", "adoc"].contains(ext) || p.lowercased().hasPrefix("docs/")
    }

    static func isTest(_ p: String) -> Bool {
        let l = p.lowercased()
        let name = (l as NSString).lastPathComponent
        return l.contains("/tests/") || l.hasPrefix("tests/") || l.contains("/test/") || l.hasPrefix("test/") || l.contains("__tests__")
            || name.hasSuffix("_test.go") || name.contains(".test.") || name.contains(".spec.") || name.hasSuffix("tests.swift")
            || name.hasSuffix("test.swift") || name.hasPrefix("test_")
    }

    /// "Sources/A/Foo.swift" → "foo"; "Tests/ATests/FooTests.swift" → "foo".
    static func stem(_ p: String) -> String {
        var s = ((p as NSString).lastPathComponent as NSString).deletingPathExtension.lowercased()
        for suffix in [".test", ".spec", "_test", "tests", "test"] where s.hasSuffix(suffix) && s.count > suffix.count {
            s = String(s.dropLast(suffix.count))
            break
        }
        if s.hasPrefix("test_") { s = String(s.dropFirst(5)) }
        return s
    }
}

// MARK: The inbox

public enum ReviewInbox {
    /// Ranked: the cheapest calls first, so the queue drains. Fresh
    /// evidence first (proven work), then stale, missing, none; lower risk
    /// before higher; then the one waiting longest; then by name.
    public static func ranked(_ items: [ReviewItem]) -> [ReviewItem] {
        items.sorted { a, b in
            let ea = evidenceRank(a.evidence), eb = evidenceRank(b.evidence)
            if ea != eb { return ea < eb }
            if a.risk != b.risk { return a.risk < b.risk }
            let ta = a.readyAt ?? .distantFuture, tb = b.readyAt ?? .distantFuture
            if ta != tb { return ta < tb }
            return a.name.localizedStandardCompare(b.name) == .orderedAscending || (a.name == b.name && a.id < b.id)
        }
    }

    static func evidenceRank(_ e: EvidenceFreshness) -> Int {
        switch e {
        case .fresh: return 0
        case .stale: return 1
        case .missing: return 2
        case .none: return 3
        }
    }
}

/// Which Macs answered review.* with "no such method": their entries are
/// left out (an older hesperd there), the rest stays.
public struct ReviewSupport: Equatable, Sendable {
    /// nil: not known yet; false: the local hesperd has no review.*.
    public var local: Bool?
    public var unsupportedMachines: Set<String> = []

    public init(local: Bool? = nil) { self.local = local }

    public var available: Bool { local == true }

    /// Notes a refusal; true when it was "missing" (the caller drops
    /// that Mac's entries).
    @discardableResult
    public mutating func note(_ error: any Error, machine: String) -> Bool {
        guard let e = error as? RPCError, ReviewRPC.missing(e) else { return false }
        unsupportedMachines.insert(machine)
        return true
    }

    public func filter(_ items: [ReviewItem]) -> [ReviewItem] {
        guard available else { return [] }
        return items.filter { !unsupportedMachines.contains($0.machine) }
    }
}

// MARK: Bulk accept

/// ⇧A accepts every item that is low risk, has fresh evidence, and
/// touches no schema, auth or migration path (concept decision 3). The
/// reason an item isn't eligible is shown.
public enum ReviewBulk {
    public enum Verdict: Equatable, Sendable {
        case eligible
        case risk(ReviewRisk)
        case evidence(EvidenceFreshness)
        case sensitive(String)
        /// The diff isn't loaded yet (paths unknown).
        case unknownPaths
    }

    public static func verdict(_ item: ReviewItem, files: [ReviewFile]?) -> Verdict {
        if item.risk != .low { return .risk(item.risk) }
        if item.evidence != .fresh { return .evidence(item.evidence) }
        guard let files else { return .unknownPaths }
        if let f = files.first(where: { $0.risk != .low }) { return .risk(f.risk) }
        for f in files {
            for p in [f.path] + (f.oldPath.map { [$0] } ?? []) where sensitive(p) { return .sensitive(p) }
        }
        return .eligible
    }

    /// Low risk and fresh: worth loading the diff to check its paths.
    public static func candidate(_ item: ReviewItem) -> Bool { item.risk == .low && item.evidence == .fresh }

    /// Schema, auth and migration paths never go in bulk.
    public static func sensitive(_ path: String) -> Bool {
        let p = path.lowercased()
        let parts = p.split(separator: "/").map(String.init)
        let name = parts.last ?? p
        let ext = (name as NSString).pathExtension
        if ["sql", "prisma", "graphql", "gql", "proto", "avsc"].contains(ext) { return true }
        for part in parts {
            if part.contains("migrat") || part.contains("schema") || part.contains("alembic") || part.contains("flyway") { return true }
            if part.contains("auth") && !part.contains("author") || part.contains("login") || part.contains("password") || part.contains("credential")
                || part.contains("secret") || part.contains("permission") || part.contains("rbac") || part.contains("oauth")
                || part.contains("session") && part.contains("token") || part.contains("security") || part.contains("crypto") {
                return true
            }
        }
        return false
    }

    public static func reason(_ v: Verdict) -> String {
        switch v {
        case .eligible: return "low risk, tests fresh"
        case .risk(let r): return "\(r.rawValue) risk"
        case .evidence(let e): return EvidenceBadge(e).explanation
        case .sensitive(let p): return "touches \(p)"
        case .unknownPaths: return "diff not loaded"
        }
    }
}

// MARK: Evidence badge

/// fresh ✓ · stale ! · missing ? (none: –), its tone and words.
public struct EvidenceBadge: Equatable, Sendable {
    public var freshness: EvidenceFreshness
    public init(_ f: EvidenceFreshness) { freshness = f }

    public var symbol: String {
        switch freshness {
        case .fresh: return "✓"
        case .stale: return "!"
        case .missing: return "?"
        case .none: return "–"
        }
    }

    /// "tests ✓".
    public var short: String { "tests \(symbol)" }

    public var title: String {
        switch freshness {
        case .fresh: return "Tests fresh"
        case .stale: return "Tests stale"
        case .missing: return "No tests ran"
        case .none: return "No changes"
        }
    }

    public var explanation: String {
        switch freshness {
        case .fresh: return "a test or build passed after the last edit"
        case .stale: return "edited after the last test or build"
        case .missing: return "no test or build ran"
        case .none: return "nothing changed"
        }
    }

    /// The design system's tone (never Signal: that is "needs you").
    public var tone: StateMarkKind.Tone {
        switch freshness {
        case .fresh: return .done
        case .stale: return .question
        case .missing, .none: return .dim
        }
    }
}

/// A command's line in the evidence box: "✓ swift test", "✗ 1 go test ./...".
public enum EvidenceText {
    public static func mark(_ c: EvidenceCommand) -> String {
        if c.running { return "…" }
        return c.passed ? "✓" : "✗"
    }

    /// "test · 2m ago · exit 1".
    public static func meta(_ c: EvidenceCommand, now: Date) -> String {
        var parts = [c.kind.rawValue]
        if let t = c.endedAt ?? c.startedAt { let a = ProjectNav.age(t, now: now); parts.append(a == "now" ? a : "\(a) ago") }
        if let code = c.exitCode, code != 0 { parts.append("exit \(code)") }
        if c.running { parts.append("running") }
        return parts.joined(separator: " · ")
    }

    /// The commands that prove something first (tests, builds), newest first.
    public static func ordered(_ cs: [EvidenceCommand]) -> [EvidenceCommand] {
        cs.enumerated().sorted { a, b in
            if a.element.kind.proves != b.element.kind.proves { return a.element.kind.proves }
            let ta = a.element.endedAt ?? a.element.startedAt ?? .distantPast, tb = b.element.endedAt ?? b.element.startedAt ?? .distantPast
            return ta != tb ? ta > tb : a.offset < b.offset
        }.map(\.element)
    }
}

// MARK: Texts

public enum ReviewText {
    /// The finished tile's footer: "Ready to review · 3 files · tests ✓".
    public static func footer(_ item: ReviewItem) -> String {
        "Ready to review · \(files(item.files)) · \(EvidenceBadge(item.evidence).short)"
    }

    public static func files(_ n: Int) -> String { "\(n) file\(n == 1 ? "" : "s")" }

    /// "+120 −14".
    public static func churn(added: Int, removed: Int) -> String { "+\(added) −\(removed)" }

    /// The toolbar pill: "Review · 3".
    public static func pill(_ n: Int) -> String { "Review · \(n)" }

    /// The notification: "api on mini is ready to review".
    public static func notificationTitle(_ item: ReviewItem, machineName: String) -> String {
        "\(item.name) on \(machineName) is ready to review"
    }

    /// "3 files · +120 −14 · tests ✓".
    public static func notificationBody(_ item: ReviewItem) -> String {
        "\(files(item.files)) · \(churn(added: item.added, removed: item.removed)) · \(EvidenceBadge(item.evidence).short)"
    }

    /// The provenance line under the focused hunk: "Why here? turn 2 ·
    /// Edit · “add the locale column”".
    public static func provenance(_ p: ReviewProvenance) -> String {
        guard !p.isEmpty else { return "Why here? no edit recorded for this line" }
        var parts: [String] = []
        if let t = p.turn { parts.append("turn \(t)") }
        if let tool = p.tool { parts.append(tool) }
        if let prompt = p.prompt { parts.append("“\(SessionFormat.oneLine(prompt, max: 120))”") }
        return "Why here? " + (parts.isEmpty ? "an earlier turn" : parts.joined(separator: " · "))
    }

    /// The default commit message: the agent's name (and summary).
    public static func commitMessage(_ item: ReviewItem, summary: String?) -> String {
        let s = summary.map { SessionFormat.oneLine($0, max: 200) } ?? ""
        return s.isEmpty ? item.name : "\(item.name): \(s)"
    }
}

/// The items that became ready since the last list (for "X is ready to
/// review"); the first list after a connect announces nothing.
public struct ReviewArrivals: Sendable {
    private var known: Set<String>?

    public init() {}

    public mutating func update(_ items: [ReviewItem]) -> [ReviewItem] {
        defer { known = Set(items.map(\.id)) }
        guard let known else { return [] }
        return items.filter { !known.contains($0.id) }
    }

    /// After a reconnect: the next list is a baseline again.
    public mutating func reset() { known = nil }
}
