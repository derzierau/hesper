import HesperCore

/// What a decision row or a grid button does when tapped.
public enum DecisionAction: Equatable, Sendable {
    case allow, always
    /// Opens the deny sheet, where the owner says what to do instead.
    case denyWithMessage
    /// A question's choice, by its number.
    case choose(Int)
    case resume, close, stop
    /// Pushes the agent view.
    case open
}

/// One numbered option on the priority card, or one button in a grid
/// card's action row.
public struct DecisionRow: Equatable, Sendable, Identifiable {
    public var number: Int
    public var label: String
    public var detail: String?
    public var isDestructive: Bool
    public var action: DecisionAction
    public var id: Int { number }

    public init(number: Int, label: String, detail: String? = nil, isDestructive: Bool = false, action: DecisionAction) {
        self.number = number
        self.label = label
        self.detail = detail
        self.isDestructive = isDestructive
        self.action = action
    }
}

/// The options Home offers for an agent (docs/iphone/neon.md, "Priority
/// decision card" and "Task grids"), and what each one sends.
public enum DecisionOptions {
    /// The title of the dialog a risky Yes asks before it sends.
    public static let confirmationTitle = "Changes the database or remote state."

    /// The priority card's numbered rows. Empty for a question without
    /// options: the owner opens the agent to answer.
    public static func rows(for a: Agent) -> [DecisionRow] {
        switch a.state {
        case .approval:
            return [
                // The ellipsis only where a dialog follows (HIG).
                DecisionRow(number: 1, label: needsConfirmation(a, .allow) ? "Yes…" : "Yes", action: .allow),
                DecisionRow(number: 2, label: "Yes, and don't ask again", detail: "asks once more", action: .always),
                DecisionRow(number: 3, label: "No, and tell Claude what to do instead", isDestructive: true, action: .denyWithMessage),
            ]
        case .question:
            return AgentCopy.choices(a).map { DecisionRow(number: $0.number, label: $0.title, action: .choose($0.number)) }
        case .error:
            return [
                DecisionRow(number: 1, label: "Resume", action: .resume),
                DecisionRow(number: 2, label: "Close", isDestructive: true, action: .close),
            ]
        default:
            return []
        }
    }

    /// A grid card's buttons, numbered left to right. A question shows its
    /// first two choices and "Open" when there are more (or none).
    public static func gridActions(for a: Agent) -> [DecisionRow] {
        let labelled: [(String, Bool, DecisionAction)]
        switch a.state {
        case .approval:
            labelled = [(needsConfirmation(a, .allow) ? "Yes…" : "Yes", false, .allow), ("No…", true, .denyWithMessage)]
        case .question:
            let choices = AgentCopy.choices(a)
            var list = choices.prefix(2).map { ($0.title, false, DecisionAction.choose($0.number)) }
            if choices.count != list.count || choices.isEmpty { list.append(("Open", false, .open)) }
            labelled = list
        case .error:
            labelled = [("Resume", false, .resume), ("Close", true, .close)]
        case .done, .idle:
            labelled = [("Open", false, .open), ("Close", true, .close)]
        case .working, .starting:
            labelled = [("Stop", true, .stop), ("Open", false, .open)]
        default:
            labelled = []
        }
        return labelled.enumerated().map { i, b in
            DecisionRow(number: i + 1, label: b.0, isDestructive: b.1, action: b.2)
        }
    }

    /// What the "Not sent" alert says when a dialog or sheet answered a
    /// prompt that is no longer the agent's (`isStillCurrent`).
    public static let promptChanged = "This prompt changed. Look again before answering."

    /// Whether the tap first asks `confirmationTitle(_:_:)`: a Yes on a
    /// risky approval (`RiskRule`), and "don't ask again" on every
    /// approval, as its row's "asks once more" promises.
    public static func needsConfirmation(_ a: Agent, _ action: DecisionAction) -> Bool {
        switch action {
        case .allow: RiskRule.isRisky(a)
        case .always: true
        default: false
        }
    }

    /// The dialog's title for a tap that `needsConfirmation`: the risk for
    /// a risky approval, otherwise the last ask before "don't ask again".
    public static func confirmationTitle(_ a: Agent, _ action: DecisionAction) -> String {
        RiskRule.isRisky(a) ? confirmationTitle : "Allow this without asking again?"
    }

    /// Whether the prompt the owner confirmed is still the one the agent
    /// shows. A dialog or sheet keeps a snapshot and sends by id; if the
    /// prompt was answered elsewhere or a new one arrived, the answer would
    /// land on a prompt the owner never saw. Same state, same `stateSince`
    /// and same attention (docs/iphone/concept.md, flow 1): `stateSince`
    /// tells a repeated identical prompt from the one answered before it.
    public static func isStillCurrent(_ confirmed: Agent, in fleet: Fleet) -> Bool {
        guard let live = fleet.agents[confirmed.id] else { return false }
        return live.state == confirmed.state && live.stateSince == confirmed.stateSince
            && live.attention == confirmed.attention
    }

    /// The command a tap sends; nil for the deny sheet and for opening the
    /// agent, which the UI handles itself.
    public static func command(_ a: Agent, _ action: DecisionAction) -> LinkCommand? {
        switch action {
        case .allow: .answer(agent: a.id, .allow)
        case .always: .answer(agent: a.id, .always)
        case .choose(let n): .choose(agent: a.id, option: n)
        case .resume: .resume(agent: a.id)
        case .close: .close(agent: a.id)
        case .stop: .stop(agent: a.id)
        case .denyWithMessage, .open: nil
        }
    }

    /// The command or file a card shows in its mono block: the approval's
    /// or the error's detail; nil otherwise.
    public static func commandLine(_ a: Agent) -> String? {
        switch a.state {
        case .approval, .error: a.attention?.detail?.nonEmpty
        default: nil
        }
    }
}
