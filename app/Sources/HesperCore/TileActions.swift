import Foundation

/// One verb on a tile's action strip (the app's AttentionBar): its title,
/// the key cap it carries, whether it is the strip's default (the tinted
/// primary button) and whether a tight tile folds it into the ⋯ overflow
/// instead of showing it.
public struct TileAction: Equatable, Sendable, Identifiable {
    public enum Verb: Equatable, Sendable {
        /// A decision hesperd answers itself (allow, always, trust, …).
        case answer(Decision)
        /// A question's option typed to the agent as its number (0-based).
        case option(Int)
        /// "Deny…": the message field opens.
        case denyWithMessage
        /// Open the tile full size.
        case open
        case resume
        case close
    }

    public var verb: Verb
    public var title: String
    /// The key that does the same where the keys work (the selected tile).
    public var key: String?
    public var primary: Bool
    /// The tone the primary is tinted with.
    public var tone: StateMarkKind.Tone
    /// A tight tile moves it into the ⋯ overflow (the verbs that used to
    /// disappear there: Always, Open).
    public var foldsWhenTight: Bool
    /// The accessibility identifier the self-tests click.
    public var id: String

    public init(_ verb: Verb, _ title: String, key: String? = nil, primary: Bool = false, tone: StateMarkKind.Tone = .working,
                foldsWhenTight: Bool = false, id: String) {
        self.verb = verb; self.title = title; self.key = key; self.primary = primary; self.tone = tone
        self.foldsWhenTight = foldsWhenTight; self.id = id
    }
}

/// The action strip's verbs per state, exactly as the tile shows them:
/// Allow ⏎ · Always A · Deny N (· Open ⌘↩) for an approval, numbered
/// answers for a question, Resume ⏎ for an ended agent, nothing while the
/// agent works. On a tight tile the secondaries that fold go behind ⋯.
public enum TileActions {
    /// The focus (open full size) shortcut, as the registry spells it.
    public static var focusKeys: String { CommandRegistry.cap(.toggleFocus) ?? "⌘⏎" }
    /// The wall's ⏎ (allow, the first answer, resume), A, N and ⌘W caps.
    static var enterKey: String { CommandRegistry.cap(.activateTile, in: .wall) ?? "⏎" }
    static var alwaysKey: String { CommandRegistry.cap(.answer(.always), in: .wall) ?? "A" }
    static var denyKey: String { CommandRegistry.cap(.denyWithMessage, in: .wall) ?? "N" }
    static var closeKey: String { CommandRegistry.cap(.closeAgent, in: .wall) ?? "⌘W" }
    /// Below this width the strip is tight: no key caps, the folding
    /// verbs in the overflow.
    public static let tightWidth: Double = 420

    public static func isTight(width: Double) -> Bool { width < tightWidth }

    /// `inFocus`: the agent fills the window (no Open). `canChoose`: the
    /// strip can type a question's numbered option to the agent (the
    /// focus view leaves them to the terminal). `killed`: ended by ⌃⌘W.
    public static func actions(_ a: Agent, inFocus: Bool = false, canChoose: Bool = true, killed: Bool = false) -> [TileAction] {
        let open = TileAction(.open, "Open", key: focusKeys, foldsWhenTight: true, id: "approve.open")
        switch a.state {
        case .approval:
            var out = [TileAction(.answer(.allow), "Allow", key: enterKey, primary: true, tone: .signal, id: "approve.allow"),
                       TileAction(.answer(.always), "Always", key: alwaysKey, foldsWhenTight: true, id: "approve.always"),
                       TileAction(.denyWithMessage, "Deny", key: denyKey, id: "approve.deny")]
            if !inFocus { out.append(open) }
            return out
        case .question:
            let answers = a.attention?.answers ?? []
            if !answers.isEmpty {
                // A screen hesperd answers itself (trust, update): the first
                // answer is also ⏎.
                return answers.enumerated().map { i, d in
                    TileAction(.answer(d), d.title, key: i == 0 ? enterKey : "\(i + 1)", primary: i == 0, tone: .question, id: "answer.\(d.rawValue)")
                }
            }
            let options = canChoose ? a.attention?.options ?? [] : []
            if !options.isEmpty {
                var out = options.enumerated().map { i, title in
                    TileAction(.option(i), title, key: "\(i + 1)", primary: i == 0, tone: .question, id: "answer.option.\(i + 1)")
                }
                if !inFocus { out.append(TileAction(.open, "Open", key: focusKeys, foldsWhenTight: true, id: "answer.open")) }
                return out
            }
            // Free text and first-run screens are answered in the agent.
            return inFocus ? [] : [TileAction(.open, "Open", key: focusKeys, primary: true, tone: .question, id: "answer.open")]
        case .error:
            var out: [TileAction] = []
            if a.exit != nil { out.append(TileAction(.resume, "Resume", primary: true, tone: .error, id: "tile.resume")) }
            if !inFocus { out.append(TileAction(.open, "Open", key: focusKeys, id: "approve.open")) }
            return out
        case .exited where killed:
            return [TileAction(.resume, "Resume", key: enterKey, primary: true, id: "tile.resume"),
                    TileAction(.close, "Close", key: closeKey, id: "tile.close")]
        case .exited:
            return [TileAction(.resume, "Resume", primary: true, id: "tile.resume")]
        default:
            return []
        }
    }

    /// The strip's buttons and the ⋯ overflow's items at `width`: wide
    /// shows everything; tight keeps the primary and the verbs that don't
    /// fold, and the folding ones go behind ⋯.
    public static func split(_ actions: [TileAction], width: Double) -> (shown: [TileAction], overflow: [TileAction]) {
        guard isTight(width: width) else { return (actions, []) }
        return (actions.filter { !$0.foldsWhenTight }, actions.filter(\.foldsWhenTight))
    }
}
