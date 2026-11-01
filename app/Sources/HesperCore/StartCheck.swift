import Foundation

// The start check (composer strip): while the person types a new task, is
// there an open loop this belongs to, does it tick off one of its loose
// ends, and how many loops already wait on them? No model: token overlap
// with each loop's title, goal and open item texts, case- and
// accent-folded, English and German stop words dropped, the same project
// first. The index holds every loop's tokens and is rebuilt only when the
// store's version moves; a check tokenizes the task alone.

public enum LoopTokens {
    /// Words that say nothing about the work (English and German).
    public static let stopWords: Set<String> = [
        // English
        "a", "an", "the", "and", "or", "but", "to", "of", "in", "on", "at", "for", "with", "from", "by", "is", "are", "be", "was", "were",
        "this", "that", "these", "those", "it", "its", "as", "into", "about", "so", "if", "then", "than", "now", "just", "also", "too",
        "please", "can", "could", "would", "should", "will", "let", "lets", "make", "want", "need", "some", "all", "any", "one", "our",
        "my", "me", "we", "you", "your", "i", "do", "does", "done", "not", "no", "there", "here", "what", "which", "who", "how", "up",
        "out", "again", "still", "new", "get", "use",
        // German
        "der", "die", "das", "den", "dem", "des", "ein", "eine", "einen", "einem", "einer", "eines", "und", "oder", "aber", "zu", "zum",
        "zur", "von", "vom", "mit", "fur", "auf", "im", "am", "an", "ist", "sind", "sein", "war", "dies", "diese", "dieser", "dieses",
        "es", "als", "bei", "nach", "aus", "wie", "was", "wir", "ich", "du", "sie", "er", "bitte", "noch", "auch", "nur", "mal", "schon",
        "dass", "nicht", "kein", "keine", "machen", "mach", "mache", "neu", "neue", "neuen", "jetzt", "dann", "hier", "da", "so", "uber",
    ]

    /// The words of a text: case- and accent-folded, split at anything
    /// that is not a letter or digit, stop words and one-letter words
    /// dropped, a plural "s" cut from longer words ("tests" → "test").
    public static func of(_ text: String) -> Set<String> {
        let folded = text.folding(options: [.caseInsensitive, .diacriticInsensitive, .widthInsensitive], locale: nil)
        var out: Set<String> = []
        for raw in folded.split(whereSeparator: { !($0.isLetter || $0.isNumber) }) {
            var w = String(raw)
            if w.count > 4, w.hasSuffix("s"), !w.hasSuffix("ss") { w.removeLast() }
            guard w.count >= 2, !stopWords.contains(w) else { continue }
            out.insert(w)
        }
        return out
    }
}

/// What the start check found for a task.
public struct StartCheckResult: Equatable, Sendable {
    public struct Match: Equatable, Sendable {
        public var loop: String
        public var title: String
        /// The loop works in the task's project.
        public var sameProject: Bool
        /// Task words found in the loop, of the task's words (0…1).
        public var score: Double
        /// An open loose end (or step, promise) the task would do: offer
        /// "check it off" when the agent starts.
        public var looseEnd: String?
        public var looseEndText: String?
    }

    public var match: Match?
    /// Active loops waiting on the person (looks-done ones left out).
    public var onYou: Int
    public var loopLimit: Int

    /// At the limit or over it: the strip turns red (a hint, never a block).
    public var atLimit: Bool { onYou >= loopLimit }

    public var limitHint: String? {
        atLimit ? "\(onYou) loop\(onYou == 1 ? "" : "s") already wait\(onYou == 1 ? "s" : "") on you (limit \(loopLimit))" : nil
    }
}

public struct StartCheckIndex: Sendable {
    struct Entry: Sendable {
        var id: String
        var title: String
        var projects: [String]
        var machines: Set<String>
        var tokens: Set<String>
        var items: [(id: String, text: String, tokens: Set<String>)]
        var lastChange: Date
    }

    /// The minimum share of the task's words a loop must have; and at
    /// least two shared words, or one in the same project.
    public static let threshold = 0.4
    /// A loose end matches when this share of its own words is in the task
    /// (at least two of them; one for a one-word loose end in the task's
    /// project).
    public static let looseEndThreshold = 0.6

    let entries: [Entry]
    public let onYou: Int
    public let loopLimit: Int
    /// The store version the index was built from.
    public let version: Int

    public init(loops: some Sequence<Loop>, catalog: ProjectCatalog = ProjectCatalog(), loopLimit: Int = LoopSettings.defaultLoopLimit, version: Int = 0) {
        var list: [Entry] = []
        var you = 0
        for l in loops where l.isActive {
            if l.status != .parked, l.next == .you, !l.looksDone { you += 1 }
            guard l.status == .open else { continue }
            let open = l.openItems.filter { $0.kind != .condition }
            var tokens = LoopTokens.of(l.title).union(LoopTokens.of(l.goal ?? ""))
            let items = open.filter { $0.kind == .looseEnd || $0.kind == .step || $0.kind == .promise }
                .map { (id: $0.id, text: $0.text, tokens: LoopTokens.of($0.text)) }
            for it in open { tokens.formUnion(LoopTokens.of(it.text)) }
            // A package's loop is its repository's too.
            let projects = l.projects.flatMap { Array(catalog.lineage($0)) }
            list.append(Entry(id: l.id, title: l.displayTitle, projects: projects, machines: l.machines, tokens: tokens, items: items, lastChange: l.lastChange))
        }
        entries = list
        onYou = you
        self.loopLimit = loopLimit
        self.version = version
    }

    /// The best open loop for a task in `project` on `machine` (nil when
    /// none clears the threshold), and the loop-limit numbers.
    public func check(task: String, project: String? = nil, machine: String? = nil) -> StartCheckResult {
        var result = StartCheckResult(match: nil, onYou: onYou, loopLimit: loopLimit)
        let words = LoopTokens.of(task)
        guard !words.isEmpty else { return result }
        var best: (match: StartCheckResult.Match, key: (Int, Double, Int, Int, Date))?
        for e in entries {
            let shared = words.intersection(e.tokens).count
            guard shared > 0 else { continue }
            let same = project.map { e.projects.contains($0) } ?? false
            let score = Double(shared) / Double(words.count)
            var loose: (id: String, text: String, ratio: Double)?
            for it in e.items where !it.tokens.isEmpty {
                let hit = words.intersection(it.tokens).count
                let ratio = Double(hit) / Double(it.tokens.count)
                // Two shared words; a one-word loose end only in its own project.
                guard hit >= (it.tokens.count == 1 && same ? 1 : 2), ratio >= Self.looseEndThreshold else { continue }
                if loose == nil || ratio > loose!.ratio { loose = (it.id, it.text, ratio) }
            }
            let clears = score >= Self.threshold && (shared >= 2 || same) || loose != nil
            guard clears else { continue }
            let m = StartCheckResult.Match(loop: e.id, title: e.title, sameProject: same, score: min(1, score), looseEnd: loose?.id, looseEndText: loose?.text)
            let onMachine = machine.map { e.machines.contains($0) } ?? false
            let key = (same ? 1 : 0, score, loose == nil ? 0 : 1, onMachine ? 1 : 0, e.lastChange)
            if let b = best {
                let k = b.key
                let better = key.0 != k.0 ? key.0 > k.0 : key.1 != k.1 ? key.1 > k.1 : key.2 != k.2 ? key.2 > k.2 : key.3 != k.3 ? key.3 > k.3 : key.4 > k.4
                if !better { continue }
            }
            best = (m, key)
        }
        result.match = best?.match
        return result
    }
}
