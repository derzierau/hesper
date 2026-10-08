import Foundation

// The one search surface (⌘K / ⌘Y): its two scopes, the History scope's
// keys, rows, filter menus and preview actions. Pure (computed off the main
// thread where it matters), tested in SearchSurfaceTests.

/// Which scope the search surface shows: ⌘K "All" (agents, actions,
/// projects, history together) or ⌘Y "History" (sessions with filters and
/// a preview). ⇥ switches between them, carrying the query.
public enum SearchScope: String, CaseIterable, Sendable {
    case all, history

    public var title: String { self == .all ? "All" : "History" }
    public var shortcut: String { self == .all ? "⌘K" : "⌘Y" }
    public var other: SearchScope { self == .all ? .history : .all }
}

// MARK: Keys (History scope)

public enum SearchKeyAction: Equatable, Sendable {
    /// What the History list does with it (↑↓, ⏎ resume, ⌥⏎ fork, letters…).
    case history(HistoryKeyAction)
    /// ⌘⏎: resume on the other Mac (moves ownership; `SearchPreview.continueTarget`).
    case continueOnOtherMac
    /// ⇥: the other scope (⌘K's All), with the query.
    case switchScope
    /// ⇧⇥: the search field ↔ the list (where letters are actions).
    case toggleFocus
}

public enum SearchKeys {
    /// The History scope's key table: ⏎ resume, ⌥⏎ fork, ⌘⏎ continue on
    /// the other Mac, ⇥ All scope, ⇧⇥ field ↔ list; everything else as
    /// `HistoryKeys` (↑↓, esc, ⌘F, ⌘C, F C A ⌫ in the list…).
    public static func history(_ k: KeyChord, keyCode: UInt16 = 0, characters: String?, focus: HistoryFocus,
                               fieldHasSelection: Bool = false) -> SearchKeyAction {
        if k.key == .enter && !k.control && !k.shift {
            switch (k.command, k.option) {
            case (false, false): return .history(.resume)
            case (false, true): return .history(.fork)
            case (true, false): return .continueOnOtherMac
            default: return .history(.pass)
            }
        }
        if k.key == .tab && !k.command && !k.option && !k.control { return k.shift ? .toggleFocus : .switchScope }
        return .history(HistoryKeys.route(k, keyCode: keyCode, characters: characters, focus: focus, fieldHasSelection: fieldHasSelection))
    }

    /// A palette detail that is a shortcut ("⌘N", "⌥⌘1", "⌘,") is drawn as
    /// a Kbd; anything else is meta text.
    public static func isShortcut(_ detail: String) -> Bool {
        guard let f = detail.unicodeScalars.first, detail.count <= 6, !detail.contains(" ") else { return false }
        return "⌘⌥⌃⇧".unicodeScalars.contains(f)
    }
}

// MARK: Rows

/// How a session's square reads: live (running in Hesper or outside) is
/// filled `working`; past sessions are outlined idle.
public enum SearchRowMark: Equatable, Sendable { case live, past }

public enum SearchFormat {
    /// The row's age: "now", "5m", "3h", "4d", then "3 Oct" / "3 Oct 2025".
    public static func age(_ d: Date?, now: Date = Date(), calendar: Calendar = .current) -> String {
        guard let d else { return "" }
        let s = now.timeIntervalSince(d)
        if s < 60 { return "now" }
        if s < 3600 { return "\(Int(s / 60))m" }
        if s < 86_400 { return "\(Int(s / 3600))h" }
        if s < 7 * 86_400 { return "\(Int(s / 86_400))d" }
        let sameYear = calendar.component(.year, from: d) == calendar.component(.year, from: now)
        return SessionFormat.formatted(d, sameYear ? "d MMM" : "d MMM yyyy", timeZone: calendar.timeZone)
    }

    /// "mini · 2h" (the row's Mono meta).
    public static func meta(_ s: Session, machines: [String: String], now: Date = Date()) -> String {
        var parts: [String] = []
        if !s.machine.isEmpty { parts.append(SessionFormat.machineName(s.machine, machines)) }
        let a = age(s.lastActivity, now: now)
        if !a.isEmpty { parts.append(a) }
        return parts.joined(separator: " · ")
    }
}

/// One session row of the search surface (History scope and ⌘K's History
/// section), prepared off the main thread: the cell only draws strings.
public struct SearchRowText: Equatable, Sendable {
    public var id: String
    public var kind: String
    public var title: String
    /// The FTS snippet (matches in `highlights`, character offsets) or the
    /// last exchange.
    public var snippet: String
    public var highlights: [Range<Int>]
    /// "mini · 2h".
    public var meta: String
    /// "ended", "● live in Hesper", "moved to mini"… (VoiceOver, tests).
    public var state: String
    public var live: Bool
    public var mark: SearchRowMark

    public init(_ s: Session, machines: [String: String], now: Date = Date(), snippetMax: Int = 260) {
        id = s.id
        kind = SessionFormat.kindLabel(s.kind)
        let plainTitle = PlainText.plain(s.title, max: 140)
        title = plainTitle.isEmpty ? "(untitled)" : plainTitle
        // Plain text (no Markdown, no quotes); the matches follow the strip.
        if let sn = s.snippet {
            let h = SessionFormat.highlights(PlainText.prepare(sn))
            let p = PlainText.plain(h.text, highlights: h.ranges, max: snippetMax)
            snippet = p.text
            highlights = p.ranges
        } else {
            snippet = SearchRowText.quote(s, max: snippetMax)
            highlights = []
        }
        // An untitled session's second line: its first prompt.
        if plainTitle.isEmpty && snippet.isEmpty {
            snippet = PlainText.plain(s.firstPrompt.isEmpty ? s.lastUser : s.firstPrompt, max: snippetMax)
        }
        meta = SearchFormat.meta(s, machines: machines, now: now)
        state = SessionFormat.stateLabel(s)
        live = s.isLive
        mark = s.isLive ? .live : .past
    }

    /// The row's second line without a search: the last answer, else the
    /// last ask (plain text, unquoted).
    public static func quote(_ s: Session, max: Int) -> String {
        let a = PlainText.plain(s.lastAssistant, max: max)
        if !a.isEmpty { return a }
        return PlainText.plain(s.lastUser.isEmpty ? s.firstPrompt : s.lastUser, max: max)
    }

    /// VoiceOver: "Claude session Fix the flaky badge, mini · 2h, ended: …".
    public var accessibilityLabel: String {
        [kind + " session " + title, meta, state, snippet].filter { !$0.isEmpty }.joined(separator: ", ")
    }

    /// `highlights` as UTF-16 ranges into `snippet` (NSAttributedString).
    public var utf16Highlights: [NSRange] {
        guard !highlights.isEmpty else { return [] }
        let chars = Array(snippet)
        var out: [NSRange] = []
        for r in highlights where r.lowerBound >= 0 && r.upperBound <= chars.count && !r.isEmpty {
            let loc = (String(chars[0..<r.lowerBound]) as NSString).length
            let len = (String(chars[r]) as NSString).length
            out.append(NSRange(location: loc, length: len))
        }
        return out
    }
}

// MARK: Preview actions

public enum SearchPreview {
    /// ⌘⏎'s target: a session from another Mac comes here (`local`); one
    /// of this Mac goes to the first other Mac online. nil: it can't move
    /// (live, moved already, or no other Mac).
    public static func continueTarget(_ s: Session, local: String, online: [String]) -> String? {
        guard !s.isLive, s.movedTo == nil else { return nil }
        if !s.machine.isEmpty && s.machine != local { return local }
        return online.first { $0 != local && $0 != s.machine }
    }

    /// The preview's actions with their keys: ⏎ resume (as the card says
    /// it), ⌥⏎ fork, ⌘⏎ continue on the other Mac, then C continue in the
    /// other tool, A archive, ⌫ delete, ⌘C copy id.
    public static func actions(_ s: Session, card: SessionCardText, local: String, online: [String], machines: [String: String]) -> [SessionCardText.Action] {
        var out: [SessionCardText.Action] = []
        if let first = card.actions.first { out.append(first) }
        out.append(.init(key: "⌥⏎", title: "Fork", primary: false))
        if let t = continueTarget(s, local: local, online: online) {
            out.append(.init(key: "⌘⏎", title: "Continue on \(SessionFormat.machineName(t, machines))", primary: false))
        }
        out.append(.init(key: "C", title: "Continue in \(SessionFormat.kindLabel(s.otherKind))", primary: false))
        out.append(.init(key: "A", title: s.archived ? "Unarchive" : "Archive", primary: false))
        out.append(.init(key: "⌫", title: "Delete", primary: false))
        out.append(.init(key: "⌘C", title: "Copy id", primary: false))
        return out
    }

    /// ⌘⏎'s consequence, under the actions.
    public static func continueNote(_ s: Session, local: String, online: [String], machines: [String: String]) -> String? {
        guard let t = continueTarget(s, local: local, online: online) else { return nil }
        let from = SessionFormat.machineName(s.machine.isEmpty ? local : s.machine, machines)
        let to = SessionFormat.machineName(t, machines)
        return "⌘⏎ continues on \(to), moves ownership; uncommitted work comes along if \(from) is reachable"
    }
}

// MARK: Filters (History scope)

/// The filter Pills over the History list: Project, Mac, Tool, Age and
/// More, each a menu of options.
public struct HistoryFilterMenu: Equatable, Sendable, Identifiable {
    public enum Kind: String, CaseIterable, Sendable { case project, machine, tool, age, more }

    public enum Action: Equatable, Sendable {
        case scope(HistoryScope)
        case chip(HistoryChip.Kind)
        case time(HistoryTime)
        /// A section title (not choosable).
        case header
    }

    public struct Option: Equatable, Sendable, Identifiable {
        public var id: String
        public var title: String
        public var on: Bool
        public var depth: Int
        public var count: Int?
        public var action: Action
    }

    public var kind: Kind
    /// What the Pill says ("hesper", "Any Mac", "last 7 days").
    public var title: String
    /// A filter is set (the Pill is drawn selected).
    public var active: Bool
    public var options: [Option]
    public var id: String { kind.rawValue }

    /// VoiceOver / the Pill's tooltip ("Project: hesper").
    public var label: String {
        switch kind {
        case .project: return "Project: \(title)"
        case .machine: return "Mac: \(title)"
        case .tool: return "Tool: \(title)"
        case .age: return "Age: \(title)"
        case .more: return "More filters: \(title)"
        }
    }
}

public enum HistoryFilters {
    /// - Parameters:
    ///   - context: the project "this project" means (the wall's).
    ///   - projects: `HistorySidebar.make` (rows with session counts).
    public static func make(_ q: HistoryQuery, context: String?, contextName: String?, machines: [(short: String, name: String)],
                            projects: [SidebarSection]) -> [HistoryFilterMenu] {
        var out: [HistoryFilterMenu] = []

        // Project: this project, then the sidebar's rows (sections as headers).
        var popts: [HistoryFilterMenu.Option] = []
        var current: String?
        if let context {
            let on = q.scope == .project(context)
            popts.append(.init(id: "this", title: "This project (\(contextName ?? "current"))", on: on, depth: 0, count: nil, action: .scope(.project(context))))
        }
        for sec in projects {
            if let t = sec.title { popts.append(.init(id: "h:" + sec.id, title: t, on: false, depth: 0, count: nil, action: .header)) }
            for r in sec.rows {
                let scope = HistorySidebar.scope(of: r)
                let on = HistorySidebar.isCurrent(r, q.scope)
                if on && current == nil { current = r.id == "all" ? nil : r.title }
                popts.append(.init(id: r.id, title: r.title, on: on, depth: r.depth, count: r.count, action: .scope(scope)))
            }
        }
        if current == nil, case .project(let p) = q.scope { current = p == context ? contextName : (p as NSString).lastPathComponent }
        if current == nil {
            switch q.scope {
            case .scratch: current = "scratch folders"
            case .elsewhere: current = "folder gone"
            default: break
            }
        }
        out.append(HistoryFilterMenu(kind: .project, title: current ?? "All projects", active: q.scope != .all, options: popts))

        // Mac: independent toggles; none on means every Mac.
        let macs = machines.map { m in
            HistoryFilterMenu.Option(id: "mac:" + m.short, title: m.name, on: q.machines.contains(m.short), depth: 0, count: nil, action: .chip(.machine(m.short)))
        }
        let onMacs = machines.filter { q.machines.contains($0.short) }.map(\.name)
        out.append(HistoryFilterMenu(kind: .machine, title: onMacs.isEmpty ? "Any Mac" : onMacs.joined(separator: ", "), active: !onMacs.isEmpty, options: macs))

        // Tool: Claude / Codex (on = shown).
        let kinds = [("claude", "Claude"), ("codex", "Codex")]
        let tools = kinds.map { k, t in
            HistoryFilterMenu.Option(id: "kind:" + k, title: t, on: q.kinds.isEmpty || q.kinds.contains(k), depth: 0, count: nil, action: .chip(.kind(k)))
        }
        let shown = kinds.filter { q.kinds.isEmpty || q.kinds.contains($0.0) }.map(\.1)
        out.append(HistoryFilterMenu(kind: .tool, title: q.kinds.isEmpty ? "Any tool" : shown.joined(separator: ", "), active: !q.kinds.isEmpty, options: tools))

        // Age.
        let ages = HistoryTime.allCases.map { t in
            HistoryFilterMenu.Option(id: "time:" + t.rawValue, title: t.label, on: q.time == t, depth: 0, count: nil, action: .time(t))
        }
        out.append(HistoryFilterMenu(kind: .age, title: q.time.label, active: q.time != .any, options: ages))

        // More: live, external, archived, moved.
        let more: [(HistoryChip.Kind, String, String, Bool)] = [
            (.live, "live", "live in Hesper", q.liveOnly), (.external, "external", "external", q.externalOnly),
            (.archived, "archived", "archived", q.archived), (.moved, "moved", "moved to another Mac", q.moved),
        ]
        let mopts = more.map { HistoryFilterMenu.Option(id: $0.1, title: $0.2, on: $0.3, depth: 0, count: nil, action: .chip($0.0)) }
        let onMore = more.filter(\.3).map(\.2)
        out.append(HistoryFilterMenu(kind: .more, title: onMore.isEmpty ? "More" : onMore.joined(separator: ", "), active: !onMore.isEmpty, options: mopts))
        return out
    }

    /// A chosen option.
    public static func apply(_ a: HistoryFilterMenu.Action, to q: HistoryQuery, context: String?) -> HistoryQuery {
        var q = q
        switch a {
        case .scope(let s):
            // Choosing the current project again goes back to all of them.
            q.scope = q.scope == s && s != .all ? .all : s
        case .chip(let k):
            q = HistoryChips.toggle(k, in: q, context: context)
        case .time(let t):
            q.time = t
        case .header:
            break
        }
        return q
    }
}
