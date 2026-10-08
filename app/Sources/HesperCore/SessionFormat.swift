import Foundation

// Text for History rows, the "where you left off" card and ghost cards
// (pure: computed off the main thread, tested in SessionTests).

public enum SessionFormat {
    /// "312k tokens", "1.2M tokens", "800 tokens".
    public static func tokens(_ n: Int) -> String {
        switch n {
        case ..<1000: return "\(n) tokens"
        case ..<1_000_000:
            let k = Double(n) / 1000
            return (k < 10 ? String(format: "%.1fk", k).replacingOccurrences(of: ".0k", with: "k") : "\(Int(k.rounded()))k") + " tokens"
        default:
            return String(format: "%.1fM", Double(n) / 1_000_000).replacingOccurrences(of: ".0M", with: "M") + " tokens"
        }
    }

    /// What wrote it, when it is not the plain CLI (`origin`): "desktop
    /// app", "IDE", "codex exec"… nil for the terminal CLI / TUI.
    public static func originLabel(_ origin: String?) -> String? {
        guard let o = origin?.trimmingCharacters(in: .whitespaces), !o.isEmpty else { return nil }
        switch o.lowercased() {
        case "cli", "codex-tui", "codex_cli_rs": return nil
        case "claude-desktop", "codex desktop": return "desktop app"
        case "sdk-cli", "sdk-ts", "sdk-py": return "SDK"
        case "codex_exec": return "codex exec"
        case "claude-vscode", "codex_vscode": return "IDE"
        default:
            if o.lowercased().contains("chrome") { return "browser extension" }
            return o
        }
    }

    public static func machineName(_ short: String, _ names: [String: String]) -> String { names[short] ?? short }

    /// "mini · fix/flaky-badge · 46 turns · 312k tokens".
    public static func meta(_ s: Session, machines: [String: String]) -> String {
        var parts: [String] = []
        if !s.machine.isEmpty { parts.append(machineName(s.machine, machines)) }
        if let b = s.branch { parts.append(b) }
        parts.append("\(s.turns) turn\(s.turns == 1 ? "" : "s")")
        if let t = s.tokens, t > 0 { parts.append(tokens(t)) }
        return parts.joined(separator: " · ")
    }

    /// One line: newlines and runs of spaces collapsed, cut at `max`.
    public static func oneLine(_ s: String, max: Int = 220) -> String {
        var out = ""
        out.reserveCapacity(min(s.count, max + 1))
        var space = false
        for c in s {
            if c.isWhitespace || c.isNewline {
                if !out.isEmpty { space = true }
                continue
            }
            if space { out.append(" "); space = false }
            out.append(c)
            if out.count >= max { out.append("…"); break }
        }
        return out
    }

    /// The row's quote: the FTS snippet when searching, else the last
    /// exchange (the answer, else the ask).
    public static func quote(_ s: Session) -> String {
        if let sn = s.snippet { return oneLine(sn) }
        let a = oneLine(s.lastAssistant)
        if !a.isEmpty { return "“" + a + "”" }
        let u = oneLine(s.lastUser.isEmpty ? s.firstPrompt : s.lastUser)
        return u.isEmpty ? "" : "you: " + u
    }

    /// A snippet with `<b>…</b>` or \u{2}…\u{3} marks → plain text and the
    /// highlighted ranges (character offsets in the plain text).
    public static func highlights(_ snippet: String) -> (text: String, ranges: [Range<Int>]) {
        var text = ""
        var ranges: [Range<Int>] = []
        var start: Int?
        var i = snippet.startIndex
        var n = 0
        while i < snippet.endIndex {
            let rest = snippet[i...]
            // hesperd marks matches [like this]; <b>…</b> and \u{2}…\u{3} too.
            if rest.hasPrefix("<b>") || rest.first == "\u{2}" || rest.first == "[" {
                start = n
                i = snippet.index(i, offsetBy: rest.hasPrefix("<b>") ? 3 : 1)
                continue
            }
            if rest.hasPrefix("</b>") || rest.first == "\u{3}" || (rest.first == "]" && start != nil) {
                if let s = start, n > s { ranges.append(s..<n) }
                start = nil
                i = snippet.index(i, offsetBy: rest.hasPrefix("</b>") ? 4 : 1)
                continue
            }
            let c = snippet[i]
            if c.isNewline { text.append(" ") } else { text.append(c) }
            n += 1
            i = snippet.index(after: i)
        }
        if let s = start, n > s { ranges.append(s..<n) }
        return (text, ranges)
    }

    /// "14:05" today, "yesterday 18:42", "Mon 09:10" this week, "3 Oct",
    /// "3 Oct 2025" another year.
    public static func when(_ d: Date?, now: Date = Date(), calendar: Calendar = .current) -> String {
        guard let d else { return "" }
        func fmt(_ pattern: String) -> String { formatted(d, pattern, timeZone: calendar.timeZone) }
        if calendar.isDate(d, inSameDayAs: now) { return fmt("HH:mm") }
        if let y = calendar.date(byAdding: .day, value: -1, to: now), calendar.isDate(d, inSameDayAs: y) { return "yesterday " + fmt("HH:mm") }
        if now.timeIntervalSince(d) < 6 * 86_400 && d < now { return fmt("EEE HH:mm") }
        return fmt(calendar.component(.year, from: d) == calendar.component(.year, from: now) ? "d MMM" : "d MMM yyyy")
    }

    // DateFormatter is expensive to make: one per pattern and time zone.
    nonisolated(unsafe) private static var formatters: [String: DateFormatter] = [:]
    private static let formatterLock = NSLock()

    static func formatted(_ d: Date, _ pattern: String, timeZone: TimeZone) -> String {
        formatterLock.withLock {
            let key = pattern + "|" + timeZone.identifier
            let f: DateFormatter
            if let x = formatters[key] {
                f = x
            } else {
                f = DateFormatter()
                f.locale = Locale(identifier: "en_GB")
                f.timeZone = timeZone
                f.dateFormat = pattern
                formatters[key] = f
            }
            return f.string(from: d)
        }
    }

    public enum State: Equatable, Sendable {
        case live, liveExternal, moved(String), external, mirrored(Int), archived, ended
    }

    public static func state(_ s: Session) -> State {
        if let l = s.live { return l.agentId != nil ? .live : .liveExternal }
        if let m = s.movedTo { return .moved(m) }
        if s.archived { return .archived }
        if s.external { return .external }
        if !s.mirrored.isEmpty { return .mirrored(s.mirrored.count + 1) }
        return .ended
    }

    public static func stateLabel(_ s: Session) -> String {
        switch state(s) {
        case .live: return "● live in Hesper"
        case .liveExternal: return "● running outside"
        case .moved(let m): return "moved to \(m)"
        case .external: return "external"
        case .mirrored(let n): return "mirrored on \(n) Macs"
        case .archived: return "archived"
        case .ended: return "ended"
        }
    }

    public static func kindLabel(_ k: String) -> String {
        switch k {
        case "claude": return "Claude"
        case "codex": return "Codex"
        default: return k.prefix(1).uppercased() + k.dropFirst()
        }
    }

    /// "~/projects/hesper" (home abbreviated).
    public static func abbreviate(_ path: String, home: String = NSHomeDirectory()) -> String {
        guard !home.isEmpty, path == home || path.hasPrefix(home + "/") else { return path }
        return "~" + path.dropFirst(home.count)
    }
}

/// One History row, prepared off the main thread (the table only sets text).
public struct HistoryRowText: Equatable, Sendable {
    public var id: String
    public var kind: String
    public var title: String
    public var meta: String
    public var quote: String
    public var highlights: [Range<Int>]
    public var when: String
    public var state: String
    public var live: Bool

    public init(_ s: Session, machines: [String: String], now: Date = Date()) {
        id = s.id
        kind = SessionFormat.kindLabel(s.kind)
        title = s.title.isEmpty ? "(untitled)" : SessionFormat.oneLine(s.title, max: 140)
        meta = SessionFormat.meta(s, machines: machines)
        if let sn = s.snippet {
            let h = SessionFormat.highlights(SessionFormat.oneLine(sn, max: 260))
            quote = h.text
            highlights = h.ranges
        } else {
            quote = SessionFormat.quote(s)
            highlights = []
        }
        when = SessionFormat.when(s.lastActivity, now: now)
        state = SessionFormat.stateLabel(s)
        live = s.isLive
    }
}

/// The "where you left off" card: what it says, line by line.
public struct SessionCardText: Equatable, Sendable {
    public struct Action: Equatable, Sendable {
        public var key: String
        public var title: String
        public var primary: Bool
    }

    public var title: String
    /// "Codex on mini · ~/projects/hesper (worktree fix/flaky-badge)"
    public var subtitle: String
    public var asked: String
    public var answered: String
    /// nil: `sessions.show` hasn't answered yet (the line keeps its space).
    public var changed: String?
    public var changedFiles: [SessionChanges.File]
    public var uncommitted: String?
    public var todos: [String]
    public var branch: String
    public var actions: [Action]
    /// ⌥⏎'s consequence (shown under the actions).
    public var resumeHereNote: String?

    /// - Parameters:
    ///   - local: this Mac's short name; `machines`: short → name.
    public init(_ s: Session, changes: SessionChanges?, loadingChanges: Bool, local: String, machines: [String: String],
                home: String = NSHomeDirectory()) {
        let mac = SessionFormat.machineName(s.machine, machines)
        let here = SessionFormat.machineName(local, machines)
        title = s.title.isEmpty ? "(untitled)" : SessionFormat.oneLine(s.title, max: 160)
        var sub = "\(SessionFormat.kindLabel(s.kind)) on \(mac)"
        if let o = SessionFormat.originLabel(s.origin) { sub += " (\(o))" }
        if !s.cwd.isEmpty { sub += " · " + SessionFormat.abbreviate(s.cwd, home: home) }
        subtitle = sub
        asked = SessionFormat.oneLine(s.lastUser.isEmpty ? s.firstPrompt : s.lastUser, max: 400)
        answered = SessionFormat.oneLine(s.lastAssistant, max: 600)
        if let c = changes {
            changedFiles = c.files
            if c.files.isEmpty {
                changed = c.uncommitted ? "" : "no changes"
            } else {
                let shown = c.files.prefix(4).map { f -> String in
                    var t = (f.path as NSString).lastPathComponent
                    if f.added > 0 { t += " +\(f.added)" }
                    if f.removed > 0 { t += " −\(f.removed)" }
                    return t
                }
                changed = shown.joined(separator: " · ") + (c.files.count > 4 ? " · +\(c.files.count - 4) more" : "")
            }
            uncommitted = c.uncommitted ? (c.uncommittedCount.map { "\($0) uncommitted" } ?? "uncommitted") : nil
        } else {
            changedFiles = []
            changed = loadingChanges ? nil : "—"
            uncommitted = nil
        }
        todos = s.todos.filter { !$0.done }.map { SessionFormat.oneLine($0.text, max: 120) }
        var b = s.branch ?? "no branch"
        if let c = changes {
            if let a = c.ahead, a > 0 { b += " · \(a) ahead" + (c.base.map { " of \($0)" } ?? "") }
            if let bh = c.behind, bh > 0 { b += " · \(bh) behind" }
            b += c.worktreeExists ? " · folder on \(mac)" : " · folder gone (recreated from the branch)"
        }
        branch = b
        var acts: [Action] = []
        if let _ = s.liveAgentID {
            acts.append(Action(key: "⏎", title: "Open (live)", primary: true))
        } else if let m = s.movedTo {
            acts.append(Action(key: "⏎", title: "Open on \(SessionFormat.machineName(m, machines))", primary: true))
        } else if s.live?.external == true {
            acts.append(Action(key: "⏎", title: "Running outside Hesper", primary: true))
        } else {
            acts.append(Action(key: "⏎", title: s.machine == local || s.machine.isEmpty ? "Resume" : "Resume on \(mac)", primary: true))
        }
        if s.machine != local && !s.machine.isEmpty && !s.isLive && s.movedTo == nil {
            acts.append(Action(key: "⌥⏎", title: "Resume here (\(here))", primary: false))
            resumeHereNote = "continues on \(here), moves ownership; uncommitted work comes along if \(mac) is reachable"
        } else {
            resumeHereNote = nil
        }
        acts.append(Action(key: "F", title: "Fork", primary: false))
        acts.append(Action(key: "C", title: "Continue in \(SessionFormat.kindLabel(s.otherKind))", primary: false))
        acts.append(Action(key: "A", title: s.archived ? "Unarchive" : "Archive", primary: false))
        acts.append(Action(key: "⌫", title: "Delete", primary: false))
        acts.append(Action(key: "⌘C", title: "Copy id", primary: false))
        actions = acts
    }
}

// MARK: Keys

/// Where the panel's keyboard is: the search field (typing searches) or
/// the list (letters are actions).
public enum HistoryFocus: Equatable, Sendable { case search, list }

public enum HistoryKeyAction: Equatable, Sendable {
    case up, down, pageUp, pageDown, first, last
    /// ⏎ resume where it ran (live: open the agent); ⌥⏎ resume on this Mac.
    case resume, resumeHere
    case fork, continueOther, archive, delete, copyID
    /// ⌘F / typing in the list: back to the search field (with the text).
    case focusSearch(String?)
    /// ⇥ from the search field: the list takes the letters.
    case focusList
    case close
    /// Not the panel's: the field (typing) or the window (⌘ shortcuts).
    case pass
}

/// The History panel's key table (consistent with the overlay family: ↑↓
/// select, ⏎ do, ⌥⏎ the alternative, ⇥ act on, esc close, typing filters).
public enum HistoryKeys {
    /// - Parameters:
    ///   - keyCode: 51 = ⌫ (delete), 117 = ⌦, 116/121 page up/down.
    ///   - fieldHasSelection: ⌘C copies the field's selected text then.
    public static func route(_ k: KeyChord, keyCode: UInt16 = 0, characters: String?, focus: HistoryFocus, fieldHasSelection: Bool = false) -> HistoryKeyAction {
        if k.command && !k.option && !k.control {
            switch k.key {
            case .char("f"), .char("F"): return .focusSearch(nil)
            case .char("c"), .char("C"): return focus == .list || !fieldHasSelection ? .copyID : .pass
            case .char("y"), .char("Y"): return .close // ⌘Y toggles
            case .up: return .first
            case .down: return .last
            default: return .pass
            }
        }
        if k.control { return .pass }
        switch k.key {
        case .up: return .up
        case .down: return .down
        case .home where focus == .list: return .first
        case .end where focus == .list: return .last
        case .enter: return k.option ? .resumeHere : .resume
        case .escape: return .close
        case .tab: return k.shift ? .focusSearch(nil) : .focusList
        default: break
        }
        if keyCode == 116 { return .pageUp }
        if keyCode == 121 { return .pageDown }
        guard focus == .list else { return .pass }
        if keyCode == 51 || keyCode == 117 { return .delete }
        if !k.option, case .char(let c) = k.key {
            switch c {
            case "f", "F": return .fork
            case "c", "C": return .continueOther
            case "a", "A": return .archive
            default: break
            }
        }
        if let c = characters, !c.isEmpty, c.unicodeScalars.allSatisfy({ !CharacterSet.controlCharacters.contains($0) && $0.value < 0xF700 }) {
            return .focusSearch(c)
        }
        return .pass
    }
}
