import Foundation

/// The ⌘J attention queue as an inbox (pure rules, unit-tested): what each
/// item asks, the answers it offers inline, where it is, and its keys.
/// Answers use what the app already has: agents.answer decisions for
/// approvals and the questions hesperd answers itself (trust/exit,
/// skip/update); a number key (agents.input) for a question that lists
/// numbered options; anything else opens the agent.
public struct InboxAnswer: Equatable, Sendable {
    public enum Action: Equatable, Sendable {
        /// agents.answer with this decision.
        case decision(Decision)
        /// These keys to the agent (agents.input): a numbered option.
        case keys(String)
        /// Open the agent's tile (free text, errors).
        case open
    }

    public var title: String
    public var action: Action
    /// Its number key (1–9) in the inbox; nil: ⏎ / ⌘O only.
    public var key: Int?

    public init(title: String, action: Action, key: Int?) {
        self.title = title; self.action = action; self.key = key
    }
}

/// A question's text split into the question and its numbered options
/// ("Which tone? 1. Formal 2. Casual").
public struct NumberedQuestion: Equatable, Sendable {
    public var question: String
    public var options: [String]
}

public enum AttentionInbox {
    /// The decisions an approval offers, in order. hesperd sends
    /// `options` [allow, always, deny]; "Always" only when it is offered.
    /// No options (an older daemon): Allow and Deny.
    public static func approvalDecisions(_ attention: Attention?) -> [Decision] {
        guard let opts = attention?.options, !opts.isEmpty else { return [.allow, .deny] }
        let offered = Set(opts.compactMap(Decision.init(rawValue:)))
        let out = [Decision.allow, .always, .deny].filter { offered.contains($0) }
        return out.isEmpty ? [.allow, .deny] : out
    }

    /// The inline answers of an agent that needs the user; the first one
    /// is the primary (⏎). Never empty for an agent in the queue.
    public static func answers(state: AgentState, attention: Attention?) -> [InboxAnswer] {
        func numbered(_ list: [(String, InboxAnswer.Action)]) -> [InboxAnswer] {
            list.enumerated().map { i, a in InboxAnswer(title: a.0, action: a.1, key: i < 9 ? i + 1 : nil) }
        }
        switch state {
        case .approval:
            return numbered(approvalDecisions(attention).map { ($0.title, .decision($0)) })
        case .question:
            if let a = attention, !a.answers.isEmpty {
                return numbered(a.answers.map { ($0.title, .decision($0)) })
            }
            if let q = numberedOptions(attention?.detail ?? ""), q.options.count <= 9 {
                return numbered(q.options.enumerated().map { i, o in (o, .keys("\(i + 1)")) })
            }
            return [InboxAnswer(title: "Open", action: .open, key: nil)]
        default:
            return [InboxAnswer(title: "Open", action: .open, key: nil)]
        }
    }

    /// The exact question an item shows.
    public static func question(state: AgentState, attention: Attention?) -> String {
        let title = attention?.title?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        let detail = attention?.detail?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        switch state {
        case .approval:
            if title.isEmpty { return detail.isEmpty ? "Approval" : detail }
            return detail.isEmpty ? title : "\(title): \(detail)"
        case .question:
            if attention?.answers.isEmpty ?? true, let q = numberedOptions(detail) { return q.question.isEmpty ? "Question" : q.question }
            if !detail.isEmpty { return detail }
            return title.isEmpty ? "Question" : title
        default:
            if !detail.isEmpty { return title.isEmpty || title == "Agent error" ? detail : "\(title): \(detail)" }
            return title.isEmpty ? "Error" : title
        }
    }

    /// Where an item is when that isn't here: "other wall" (not on this
    /// wall), "other Mac" (runs on another machine).
    public static func whereabouts(machine: String, local: String, onThisWall: Bool) -> [String] {
        var out: [String] = []
        if !onThisWall { out.append("other wall") }
        if machine != local { out.append("other Mac") }
        return out
    }

    /// Numbered options in a question ("1. A\n2. B", "1) A 2) B",
    /// "[1] A [2] B"): at least two, numbered 1, 2, 3 … in order; nil
    /// otherwise. Prose numbers ("in 2 steps") don't count.
    public static func numberedOptions(_ text: String) -> NumberedQuestion? {
        let s = text as NSString
        guard s.length > 0,
              let re = try? NSRegularExpression(pattern: #"(?:^|(?<=\s))[\(\[]?([1-9])[\.\)\]](?=\s)"#, options: [.anchorsMatchLines])
        else { return nil }
        let matches = re.matches(in: text, range: NSRange(location: 0, length: s.length))
        // The longest run 1, 2, 3 … starting at a "1".
        var best: [NSTextCheckingResult] = []
        var i = 0
        while i < matches.count {
            if s.substring(with: matches[i].range(at: 1)) == "1" {
                var run = [matches[i]]
                var next = 2
                for m in matches[(i + 1)...] where s.substring(with: m.range(at: 1)) == "\(next)" {
                    run.append(m)
                    next += 1
                }
                if run.count > best.count { best = run }
            }
            i += 1
        }
        guard best.count >= 2 else { return nil }
        let trim = CharacterSet.whitespacesAndNewlines.union(CharacterSet(charactersIn: ",;"))
        var options: [String] = []
        for (k, m) in best.enumerated() {
            let start = m.range.location + m.range.length
            let end = k + 1 < best.count ? best[k + 1].range.location : s.length
            var o = s.substring(with: NSRange(location: start, length: max(0, end - start))).trimmingCharacters(in: trim)
            // "A, or" / "A or" before the next option.
            for tail in [" or", ",or"] where o.lowercased().hasSuffix(tail) { o = String(o.dropLast(tail.count)).trimmingCharacters(in: trim) }
            guard !o.isEmpty else { return nil }
            options.append(o)
        }
        let question = s.substring(to: best[0].range.location).trimmingCharacters(in: trim)
        return NumberedQuestion(question: question, options: options)
    }
}

/// The inbox's keys: J/K or ↑/↓ move, 1–9 answer the selected item, ⏎ its
/// primary answer, ⌘⏎ (and ⌘O, routed by the window) open its tile, esc
/// closes.
public enum InboxKeyAction: Equatable, Sendable {
    case move(Int)
    /// 0-based: the answer with number key `n + 1`.
    case answer(Int)
    case primary, open, close, none
}

public enum InboxKeys {
    public static func route(_ action: OverlayKeyAction) -> InboxKeyAction {
        switch action {
        case .up: return .move(-1)
        case .down: return .move(1)
        case .activate: return .primary
        case .alternate: return .open
        case .close: return .close
        case .type(let s):
            switch s {
            case "j", "J": return .move(1)
            case "k", "K": return .move(-1)
            default:
                if s.count == 1, let n = Int(s), (1...9).contains(n) { return .answer(n - 1) }
                return .none
            }
        case .actOn, .back, .deleteBackward, .pass:
            return .none
        }
    }

    /// The answer a number key picks (`InboxAnswer.key`), if any.
    public static func answer(_ index: Int, in answers: [InboxAnswer]) -> InboxAnswer? {
        answers.first { $0.key == index + 1 }
    }

    /// ⏎: the first answer.
    public static func primary(_ answers: [InboxAnswer]) -> InboxAnswer? { answers.first }
}
