import Foundation

/// The composer's inline tokens: `@mini` machine, `#project` project,
/// `/codex` profile, `~branch` worktree + branch. A token starts at the
/// beginning of the text or after whitespace (or an opening bracket), so
/// paths (`src/main.go`, `~/projects`), e-mail addresses and issue numbers
/// in prose stay prose unless they resolve.
public enum TokenKind: String, Sendable, CaseIterable, Codable {
    case machine, project, profile, branch

    public var sigil: Character {
        switch self {
        case .machine: return "@"
        case .project: return "#"
        case .profile: return "/"
        case .branch: return "~"
        }
    }

    public init?(sigil: Character) {
        guard let k = TokenKind.allCases.first(where: { $0.sigil == sigil }) else { return nil }
        self = k
    }

    public var title: String {
        switch self {
        case .machine: return "Machine"
        case .project: return "Project"
        case .profile: return "Profile"
        case .branch: return "Worktree"
        }
    }
}

/// A token candidate in the text (UTF-16 range, sigil included).
public struct ComposerToken: Equatable, Sendable {
    public var kind: TokenKind
    public var location: Int
    public var length: Int
    /// The text after the sigil.
    public var query: String
    public init(kind: TokenKind, location: Int, length: Int, query: String) {
        self.kind = kind; self.location = location; self.length = length; self.query = query
    }
    public var end: Int { location + length }
}

/// What the composer knows to resolve and complete tokens.
public struct ComposerContext: Sendable, Equatable {
    public struct ProjectChoice: Sendable, Equatable {
        public var path: String
        public var name: String
        public var recent: Bool
        public init(path: String, name: String? = nil, recent: Bool) {
            self.path = path; self.name = name ?? (path as NSString).lastPathComponent; self.recent = recent
        }
    }
    public var machines: [Machine]
    public var localMachine: String
    /// Recent projects first, then folders under ~/projects.
    public var projects: [ProjectChoice]
    /// Profile name → kind.
    public var profiles: [String: String]
    /// Kind → profile (the app's setting over the daemon's defaults).
    public var kindDefaults: [String: String]
    public var defaultKind: String
    /// Project path → the profile it last ran (the daemon's defaults).
    public var projectProfiles: [String: String]

    public init(machines: [Machine] = [], localMachine: String = "L", projects: [ProjectChoice] = [], profiles: [String: String] = [:],
                kindDefaults: [String: String] = [:], defaultKind: String = "claude", projectProfiles: [String: String] = [:]) {
        self.machines = machines; self.localMachine = localMachine; self.projects = projects; self.profiles = profiles
        self.kindDefaults = kindDefaults; self.defaultKind = defaultKind; self.projectProfiles = projectProfiles
    }

    public func machine(_ q: String) -> Machine? {
        let l = q.lowercased()
        guard !l.isEmpty else { return nil }
        return machines.first { $0.short.lowercased() == l } ?? machines.first { $0.name.lowercased() == l }
            ?? machines.first { $0.name.lowercased().split(separator: ".").first.map(String.init) == l }
    }

    public func project(_ q: String) -> ProjectChoice? {
        let l = q.lowercased()
        guard !l.isEmpty else { return nil }
        return projects.first { $0.name.lowercased() == l } ?? projects.first { $0.path == q }
    }

    /// A profile by name, or by kind ("codex" → that kind's default).
    public func profile(_ q: String) -> String? {
        let l = q.lowercased()
        guard !l.isEmpty else { return nil }
        if let p = profiles.keys.first(where: { $0.lowercased() == l }) { return p }
        if let p = kindDefaults[l], profiles[p] != nil || profiles.isEmpty { return p }
        if Set(profiles.values).contains(l) { return profiles.filter { $0.value == l }.keys.sorted().first }
        return nil
    }

    /// The profile a draft gets without a choice: the project's last one,
    /// else the default kind's.
    public func defaultProfile(project: String?) -> String? {
        if let project, let p = projectProfiles[project] { return p }
        return kindDefaults[defaultKind]
    }
}

/// A token that resolved (or a branch, which always does).
public struct ResolvedToken: Equatable, Sendable {
    public var token: ComposerToken
    /// Machine short name, project path, profile name or branch.
    public var value: String
    /// The chip's label ("mini", "hesper", "codex", "fix/x").
    public var label: String
}

public enum ComposerParser {
    static let openers: Set<Character> = ["(", "[", "{", "\"", "'", ","]

    /// Every token candidate, in order.
    public static func tokens(in text: String) -> [ComposerToken] {
        let u = Array(text.utf16)
        var out: [ComposerToken] = []
        var i = 0
        while i < u.count {
            let c = u[i]
            if let ch = Unicode.Scalar(c).map(Character.init), let kind = TokenKind(sigil: ch), startsToken(u, at: i) {
                var j = i + 1
                while j < u.count, allowed(u[j], kind: kind, first: j == i + 1) { j += 1 }
                // A trailing period/comma ends a sentence, not the token.
                while j > i + 1, let last = Unicode.Scalar(u[j - 1]), ".,;:!?)".unicodeScalars.contains(last), kind != .project || !looksLikeURL(u, i + 1, j) { j -= 1 }
                // An empty token only where a sigil was just typed ("@" then
                // a space or the end), not "~/path" or "#/".
                if j == i + 1, j < u.count, let n = Unicode.Scalar(u[j]), !Character(n).isWhitespace {
                    i += 1
                    continue
                }
                let q = String(utf16CodeUnits: Array(u[(i + 1)..<j]), count: j - i - 1)
                out.append(ComposerToken(kind: kind, location: i, length: j - i, query: q))
                i = max(j, i + 1)
                continue
            }
            i += 1
        }
        return out
    }

    static func startsToken(_ u: [UInt16], at i: Int) -> Bool {
        guard i > 0 else { return true }
        guard let prev = Unicode.Scalar(u[i - 1]) else { return false }
        let p = Character(prev)
        return p.isWhitespace || openers.contains(p)
    }

    static func allowed(_ c: UInt16, kind: TokenKind, first: Bool) -> Bool {
        guard let s = Unicode.Scalar(c) else { return false }
        let ch = Character(s)
        if ch.isWhitespace { return false }
        switch kind {
        case .project:
            // Names, and pasted git URLs (anything up to whitespace).
            return !"\"'(){}[]<>,".contains(ch)
        case .branch:
            if first && ch == "/" { return false } // ~/path is a path
            return ch.isLetter || ch.isNumber || "._/-+".contains(ch)
        case .machine, .profile:
            return ch.isLetter || ch.isNumber || "._-".contains(ch)
        }
    }

    static func looksLikeURL(_ u: [UInt16], _ from: Int, _ to: Int) -> Bool {
        let s = String(utf16CodeUnits: Array(u[from..<to]), count: to - from)
        return ComposerCompletion.isGitURL(s)
    }

    /// The token the caret is in or right after (for completion), even an
    /// empty one ("@" just typed).
    public static func token(at caret: Int, in text: String) -> ComposerToken? {
        tokens(in: text).first { caret > $0.location && caret <= $0.end }
    }

    /// Resolves the tokens: unknown machines/projects/profiles stay prose.
    public static func resolve(_ text: String, context: ComposerContext) -> [ResolvedToken] {
        tokens(in: text).compactMap { t in
            switch t.kind {
            case .machine:
                guard let m = context.machine(t.query) else { return nil }
                return ResolvedToken(token: t, value: m.short, label: m.short)
            case .project:
                guard let p = context.project(t.query) else { return nil }
                return ResolvedToken(token: t, value: p.path, label: p.name)
            case .profile:
                guard let p = context.profile(t.query) else { return nil }
                return ResolvedToken(token: t, value: p, label: p)
            case .branch:
                guard !t.query.isEmpty else { return nil }
                return ResolvedToken(token: t, value: t.query, label: t.query)
            }
        }
    }
}

/// A draft's effective properties: tokens over explicit choices (chips)
/// over defaults.
public struct ComposerResolution: Equatable, Sendable {
    public var tokens: [ResolvedToken]
    public var machine: String
    public var project: String?
    public var profile: String?
    public var worktree: Bool
    public var branch: String
    /// The text the agent gets: machine/profile/branch tokens removed,
    /// project tokens as the project's name.
    public var task: String
    /// The first line of the task (the agent's name).
    public var name: String
    /// The project token is a git URL: clone it first.
    public var cloneURL: String?

    public static func make(text: String, draft: DraftChoices, context: ComposerContext) -> ComposerResolution {
        let resolved = ComposerParser.resolve(text, context: context)
        func value(_ k: TokenKind) -> String? { resolved.last { $0.token.kind == k }?.value }
        let machine = value(.machine) ?? draft.machine ?? context.localMachine
        var project = value(.project) ?? draft.project
        // A #name token names the folder the draft chose (chip, list,
        // picker): that folder, not the first entry of the same name (two
        // folders called "app", a project renamed).
        if let dp = draft.project, let t = resolved.last(where: { $0.token.kind == .project }), t.value != dp,
           Self.token(t.token.query, names: dp, context: context) {
            project = dp
        }
        let profile = value(.profile) ?? draft.profile ?? context.defaultProfile(project: project)
        let task = Self.task(text, resolved: resolved)
        let name = Self.firstLine(task)
        let branchToken = value(.branch)
        let worktree = branchToken != nil || (draft.worktree ?? false)
        let branch = branchToken ?? draft.branch.flatMap { $0.isEmpty ? nil : $0 } ?? Self.suggestBranch(name)
        var clone: String?
        if value(.project) == nil, let t = ComposerParser.tokens(in: text).last(where: { $0.kind == .project }), ComposerCompletion.isGitURL(t.query) {
            clone = t.query
        }
        return ComposerResolution(tokens: resolved, machine: machine, project: project, profile: profile, worktree: worktree,
                                  branch: branch, task: task, name: name, cloneURL: clone)
    }

    /// A #name token names `folder` (its name in the lists, or its last
    /// path component): the token means that folder, not the first entry
    /// of the same name.
    public static func token(_ query: String, names folder: String, context: ComposerContext) -> Bool {
        let q = query.lowercased()
        let names = [context.projects.first { $0.path == folder }?.name, (folder as NSString).lastPathComponent].compactMap { $0?.lowercased() }
        return names.contains(q)
    }

    /// Removes resolved machine/profile/branch tokens, writes project
    /// tokens as names, and tidies the spaces they leave.
    public static func task(_ text: String, resolved: [ResolvedToken]) -> String {
        var s = text as NSString
        for r in resolved.sorted(by: { $0.token.location > $1.token.location }) {
            let range = NSRange(location: r.token.location, length: r.token.length)
            s = s.replacingCharacters(in: range, with: r.token.kind == .project ? r.label : "") as NSString
        }
        // Git URL project tokens leave the text too (they become the project).
        let lines = (s as String).components(separatedBy: "\n").map { line -> String in
            var l = line
            while l.contains("  ") { l = l.replacingOccurrences(of: "  ", with: " ") }
            l = l.replacingOccurrences(of: " ,", with: ",").replacingOccurrences(of: " .", with: ".")
            return l.trimmingCharacters(in: .whitespaces)
        }
        return lines.joined(separator: "\n").trimmingCharacters(in: .whitespacesAndNewlines)
    }

    public static func firstLine(_ s: String) -> String {
        let l = s.split(whereSeparator: \.isNewline).first.map(String.init)?.trimmingCharacters(in: .whitespaces) ?? ""
        return l.count > 60 ? String(l.prefix(60)) : l
    }

    static let stop: Set<String> = ["the", "a", "an", "to", "of", "in", "on", "for", "and", "it", "this", "that", "with", "from", "into", "our", "my", "is", "be"]

    /// "Fix the flaky badge test" → "fix/flaky-badge-test".
    public static func suggestBranch(_ firstLine: String) -> String {
        let words = firstLine.lowercased().split { !($0.isLetter || $0.isNumber) }.map(String.init)
        guard let head = words.first else { return "" }
        let prefixes: [(Set<String>, String)] = [
            (["fix", "fixes", "bug", "bugfix", "hotfix", "repair"], "fix/"),
            (["refactor", "clean", "cleanup", "rename", "move", "simplify", "extract", "split"], "refactor/"),
            (["doc", "docs", "document", "write"], "docs/"),
            (["test", "tests"], "test/"),
        ]
        var prefix = "feature/"
        var rest = words
        for (set, p) in prefixes where set.contains(head) { prefix = p; rest = Array(words.dropFirst()); break }
        if prefix == "feature/", ["add", "implement", "build", "feat", "feature", "support", "create", "make"].contains(head) {
            rest = Array(words.dropFirst())
        }
        let slug = rest.filter { !stop.contains($0) }.prefix(5).joined(separator: "-")
        let fallback = words.filter { !stop.contains($0) }.prefix(5).joined(separator: "-")
        let s = slug.isEmpty ? fallback : slug
        return s.isEmpty ? "" : prefix + s
    }
}

/// The values a draft holds explicitly (chips), under its tokens.
public struct DraftChoices: Equatable, Sendable {
    public var machine: String?
    public var project: String?
    public var profile: String?
    public var worktree: Bool?
    public var branch: String?
    public init(machine: String? = nil, project: String? = nil, profile: String? = nil, worktree: Bool? = nil, branch: String? = nil) {
        self.machine = machine; self.project = project; self.profile = profile; self.worktree = worktree; self.branch = branch
    }
}

/// One row of a completion list.
public struct CompletionItem: Equatable, Sendable, Identifiable {
    public enum Action: Equatable, Sendable {
        /// Replace the token with this text (sigil included) and a space.
        case insert(String)
        /// Clone this URL into the projects root, then use it.
        case clone(String)
    }
    public var id: String
    public var kind: TokenKind
    public var title: String
    public var detail: String
    public var enabled: Bool
    public var action: Action
    /// A short mark before the title (machine short name, "⑂").
    public var mark: String?
}

public enum ComposerCompletion {
    public static func isGitURL(_ s: String) -> Bool {
        let l = s.lowercased()
        if l.hasPrefix("git@") || l.hasPrefix("ssh://") || l.hasPrefix("git://") { return true }
        if l.hasPrefix("https://") || l.hasPrefix("http://") {
            return l.hasSuffix(".git") || ["github.com/", "gitlab.com/", "bitbucket.org/", "codeberg.org/"].contains { l.contains($0) }
        }
        return false
    }

    /// The repository's name in a git URL ("org/repo.git" → "repo").
    public static func repoName(_ url: String) -> String {
        var s = url
        while s.hasSuffix("/") { s.removeLast() }
        if s.hasSuffix(".git") { s.removeLast(4) }
        let last = s.split(whereSeparator: { $0 == "/" || $0 == ":" }).last.map(String.init) ?? s
        return last
    }

    /// 0 = prefix, 1 = contains, 2 = in order; nil = no match.
    public static func score(_ query: String, _ s: String) -> Int? {
        let q = query.lowercased(), t = s.lowercased()
        if q.isEmpty || t.hasPrefix(q) { return 0 }
        if t.contains(q) { return 1 }
        var it = t.makeIterator()
        for c in q {
            var found = false
            while let x = it.next() { if x == c { found = true; break } }
            if !found { return nil }
        }
        return 2
    }

    public static func items(for token: ComposerToken, context: ComposerContext, firstLine: String = "") -> [CompletionItem] {
        let q = token.query
        switch token.kind {
        case .machine:
            let ranked = context.machines.compactMap { m -> (Machine, Int)? in
                let s = [score(q, m.short), score(q, m.name)].compactMap { $0 }.min()
                return s.map { (m, $0) }
            }
            .sorted { l, r in
                if l.0.online != r.0.online { return l.0.online }
                if l.1 != r.1 { return l.1 < r.1 }
                return false
            }
            return ranked.map { m, _ in
                CompletionItem(id: "machine:\(m.short)", kind: .machine, title: m.displayName, detail: machineDetail(m, local: m.short == context.localMachine),
                               enabled: m.online, action: .insert("@\(m.short)"), mark: m.short)
            }
        case .project:
            if isGitURL(q) {
                return [CompletionItem(id: "clone:\(q)", kind: .project, title: "Clone \(repoName(q)) and start", detail: q, enabled: true,
                                       action: .clone(q), mark: "⤓")]
            }
            var seen = Set<String>()
            let ranked = context.projects.compactMap { p -> (ComposerContext.ProjectChoice, Int)? in
                guard seen.insert(p.path).inserted, let s = score(q, p.name) else { return nil }
                return (p, s)
            }
            .enumerated()
            .sorted { l, r in
                if l.element.1 != r.element.1 { return l.element.1 < r.element.1 }
                if l.element.0.recent != r.element.0.recent { return l.element.0.recent }
                return l.offset < r.offset
            }
            .map(\.element.0)
            return ranked.prefix(40).map { p in
                CompletionItem(id: "project:\(p.path)", kind: .project, title: p.name, detail: abbreviate((p.path as NSString).deletingLastPathComponent) + (p.recent ? " · recent" : ""),
                               enabled: true, action: .insert("#\(p.name)"), mark: nil)
            }
        case .profile:
            return context.profiles.keys.sorted().compactMap { name -> (String, Int)? in
                let kind = context.profiles[name] ?? ""
                let s = [score(q, name), score(q, kind)].compactMap { $0 }.min()
                return s.map { (name, $0) }
            }
            .sorted { $0.1 != $1.1 ? $0.1 < $1.1 : $0.0 < $1.0 }
            .map { name, _ in
                let kind = context.profiles[name] ?? ""
                return CompletionItem(id: "profile:\(name)", kind: .profile, title: name, detail: profileDetail(name, kind: kind), enabled: true,
                                      action: .insert("/\(name)"), mark: nil)
            }
        case .branch:
            var out: [CompletionItem] = []
            if !q.isEmpty {
                out.append(CompletionItem(id: "branch:\(q)", kind: .branch, title: q, detail: "new worktree on this branch", enabled: true, action: .insert("~\(q)"), mark: "⑂"))
            }
            let s = ComposerResolution.suggestBranch(firstLine)
            if !s.isEmpty && s != q && (q.isEmpty || score(q, s) != nil) {
                out.append(CompletionItem(id: "branch:\(s)", kind: .branch, title: s, detail: "suggested from the first line", enabled: true, action: .insert("~\(s)"), mark: "⑂"))
            }
            return out
        }
    }

    /// The machine chip: its name first, then the round trip ("mini · 41
    /// ms · relay", "laptop · this Mac", "studio · offline").
    public static func machineChip(_ m: Machine?, short: String, local: Bool) -> String {
        guard let m else { return short }
        return m.displayName + " · " + machineDetail(m, local: local)
    }

    public static func machineDetail(_ m: Machine, local: Bool) -> String {
        if !m.online { return "offline" }
        if local || m.route == "local" { return "this Mac" }
        let route = (m.route ?? "").isEmpty ? "" : " · \(m.route!)"
        if let r = m.rttMs, r > 0 { return "\(Int(r.rounded())) ms\(route)" }
        return "online\(route)"
    }

    public static func profileDetail(_ name: String, kind: String) -> String {
        let k = kind.isEmpty ? "" : kind.prefix(1).uppercased() + kind.dropFirst()
        if name.contains("bypass") || name.contains("full") { return "\(k) · full access" }
        if name.contains("auto") { return "\(k) · auto mode" }
        return k
    }

    public static func abbreviate(_ p: String, home: String = NSHomeDirectory()) -> String {
        p.hasPrefix(home) ? "~" + p.dropFirst(home.count) : p
    }

    /// Applies an insert completion: the token's text becomes `insert`
    /// plus one space (unless a space follows). Returns the new text and
    /// caret.
    public static func apply(_ insert: String, to token: ComposerToken, in text: String) -> (text: String, caret: Int) {
        let ns = text as NSString
        let after = token.end < ns.length ? ns.substring(with: NSRange(location: token.end, length: 1)) : ""
        let add = after == " " || after == "\n" ? "" : " "
        let out = ns.replacingCharacters(in: NSRange(location: token.location, length: token.length), with: insert + add)
        let caret = token.location + (insert as NSString).length + 1 // after the space
        return (out, min(caret, (out as NSString).length))
    }
}
