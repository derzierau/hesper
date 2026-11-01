import Foundation

// What a tile, the composer and the sidebar show of an agent's loop: a
// short title and who acts next. Built once per store version
// (`LoopChip.all`), looked up per agent by `Agent.loop`; never per render.

public struct LoopChip: Equatable, Sendable {
    /// The longest title a chip shows before it ends in "…".
    public static let maxTitle = 28

    public var loop: String
    /// The loop's title, cut to `maxTitle`.
    public var text: String
    public var next: LoopNext
    public var looksDone: Bool
    /// What is next, in words (hesperd's `nextText`), for the tooltip.
    public var detail: String?
    /// Open items on the person (offers left out).
    public var openYou: Int

    public init(_ l: Loop) {
        loop = l.id
        text = Self.cut(l.displayTitle)
        next = l.status == .parked ? .parked : l.next
        looksDone = l.looksDone
        detail = l.nextText
        openYou = l.yourItems.count
    }

    /// On the person and not only "looks done": the chip's "your move".
    public var needsYou: Bool { next == .you && !looksDone }

    /// "Your move", "Agents on it", "Looks done" (the chip's second word).
    public var nextLabel: String {
        if looksDone && next == .you { return "Looks done" }
        return LoopNextText.label(next)
    }

    static func cut(_ s: String) -> String {
        s.count <= maxTitle ? s : String(s.prefix(maxTitle - 1)).trimmingCharacters(in: .whitespaces) + "…"
    }

    /// A chip for every active loop, by loop id.
    public static func all(_ store: LoopStore) -> [String: LoopChip] {
        var out: [String: LoopChip] = [:]
        for l in store.loops.values where l.isActive { out[l.id] = LoopChip(l) }
        return out
    }

    /// Active loops on the person that need a step (the board's "your
    /// move": looks-done loops left out, parked ones too).
    public static func onYouCount(_ store: LoopStore) -> Int {
        store.loops.values.reduce(0) { n, l in LoopColumn.of(l) == .you && !l.looksDone ? n + 1 : n }
    }
}

/// The words for who acts next, the same on the board's columns, chips
/// and the detail.
public enum LoopNextText {
    public static func label(_ n: LoopNext) -> String {
        switch n {
        case .you: return "Your move"
        case .agents: return "Agents on it"
        case .others: return "Waiting on others"
        case .parked: return "Parked"
        case .closed: return "Closed"
        }
    }
}

/// An error from a loops call, said for the sheets' inline error line.
public enum LoopsErrorText {
    public static func of(_ error: any Error) -> String {
        if let e = error as? RPCError {
            if LoopsRPC.missing(e) { return "This hesperd has no loops yet: update it to use them." }
            switch e.kind {
            case .forbidden:
                return "Only a person can do this (an agent may not): do it here or with hesperctl in your own terminal."
            case .notFound: return "It is gone: another Mac or the Steward changed it. The list is up to date again."
            case .unavailable, .remote: return "That Mac is not reachable right now: try again when it is online."
            default: break
            }
            return e.message.isEmpty ? "hesperd refused it." : e.message
        }
        return String(describing: error)
    }
}
