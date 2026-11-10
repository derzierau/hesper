import Foundation

// Size ownership across windows (docs/rebuild-contract.md "Size rule with
// several windows"). Pure, so it is unit-tested; the AppKit side is
// SizeOwnership (app/Sources/Hesper/Windows/WindowParts.swift) and
// AgentTerminal.
//
// The PTY has one size. A terminal that shows the PTY's raw output stream
// must have exactly that grid: libghostty sizes its grid from its own frame
// (a TIOCSWINSZ on its pty from `hesperd attach` changes nothing there), so
// a raw stream of a 180-column PTY in a 120-column surface wraps and
// cursor-addressed redraws (Claude Code's TUI) land in the wrong cells.
// Hence: a pane attaches read-write (raw stream) only as the size owner,
// whose own grid *is* the PTY's size; every other pane is a view
// (`hesperd attach --fit`), which the daemon renders from its screen copy
// at the pane's own grid, clipped and never reflowed.

public enum SizeOwner {
    /// One terminal that could show an agent.
    public struct Candidate: Equatable, Sendable {
        public var agentID: String
        /// The window it is in (any stable identity); nil: in none.
        public var window: Int?
        /// A focus view (an agent window, a wall's focus view).
        public var focusRole: Bool
        /// Wants to type: a focus view, or the active tile.
        public var readWrite: Bool
        /// Another device owns the agent's size (`remoteOwned(_:local:)`).
        public var remoteOwned: Bool
        /// Typed in while `remoteOwned`: takes the size back.
        public var takingBack: Bool
        public init(agentID: String, window: Int?, focusRole: Bool, readWrite: Bool, remoteOwned: Bool = false, takingBack: Bool = false) {
            self.agentID = agentID; self.window = window; self.focusRole = focusRole; self.readWrite = readWrite
            self.remoteOwned = remoteOwned; self.takingBack = takingBack
        }
    }

    /// Which candidates own their agent's PTY size: the read-write ones in
    /// the owner window (the app's key window among ours), and at most one
    /// per agent (a focus view before a tile, then the first listed). With
    /// no owner window nobody owns (the daemon follows the views' fit).
    /// While another device owns an agent's size, only a pane typed in
    /// (`takingBack`) owns, wherever it is; the rest are views.
    public static func owners(_ cs: [Candidate], ownerWindow: Int?) -> [Bool] {
        var out = Array(repeating: false, count: cs.count)
        var best: [String: Int] = [:]
        func eligible(_ c: Candidate) -> Bool {
            guard c.readWrite else { return false }
            if c.remoteOwned { return c.takingBack }
            return ownerWindow != nil && c.window == ownerWindow
        }
        for (i, c) in cs.enumerated() where eligible(c) {
            if let j = best[c.agentID] {
                if c.focusRole && !cs[j].focusRole { best[c.agentID] = i }
            } else {
                best[c.agentID] = i
            }
        }
        for i in best.values { out[i] = true }
        return out
    }

    /// What a key does to size ownership.
    public enum KeyTakeBack: Equatable, Sendable {
        /// Not a take-back: the key goes on as usual.
        case pass
        /// Candidate `i` takes the size back; the key is held for its
        /// owner surface (dropped after HeldInput's max age if none comes).
        case takeBack(Int)
        /// Candidate `i` is taking the size back and its owner surface is
        /// still swapping in: the key waits for it.
        case hold(Int)
    }

    /// A key takes the size back only if it would type into the agent
    /// (`typesIntoAgent`) and only for the read-write terminal it reaches
    /// (`targeted`; a focused control outside the terminal has none). A
    /// click never does: it only focuses (selecting text, activating the
    /// window). Once taken back, keys wait while the owner surface swaps
    /// in (`swapping`) and then go to it directly.
    public static func keyTakeBack(_ cs: [Candidate], targeted: [Bool], swapping: [Bool], typesIntoAgent: Bool) -> KeyTakeBack {
        guard typesIntoAgent, cs.count == targeted.count, cs.count == swapping.count,
              let i = cs.indices.first(where: { targeted[$0] && cs[$0].remoteOwned && cs[$0].readWrite }) else { return .pass }
        if cs[i].takingBack { return swapping[i] ? .hold(i) : .pass }
        return .takeBack(i)
    }

    /// Seconds a take-back waits for the daemon to report the new owner;
    /// after that (the owner attach failed, the link is down) the pane is a
    /// view again.
    public static let takeBackTimeout: TimeInterval = 5

    /// Whether a take-back started at `since` (uptime) has run out.
    public static func takeBackExpired(since: TimeInterval, now: TimeInterval) -> Bool {
        now - since >= takeBackTimeout
    }

    /// How a terminal attaches.
    public enum Attach: Equatable, Sendable {
        /// `--owner`, read-write: the pane's grid becomes the PTY's size.
        case owner
        /// `--fit`: read-only view at the pane's own grid (clipped), asking
        /// for that grid only while no owner holds the PTY.
        case view
    }

    /// Read-write only as the owner: a read-write pane that does not own
    /// the size would get a raw stream at another grid (garbled).
    public static func attach(readWrite: Bool, owns: Bool) -> Attach {
        readWrite && owns ? .owner : .view
    }

    /// Stands for the agent's own Mac in `owner(_:local:)` (`sizeOwnerHost`
    /// seen on a controller); never a device id.
    public static let hostOwner = "@host"

    /// Who other than this Mac owns the agent's PTY size now, nil if nobody
    /// does or this Mac does: the controller device id in `sizeOwner`, or
    /// `hostOwner` when `sizeOwnerHost` reaches this Mac for another Mac's
    /// agent (that Mac took the size; `local` is false). On the agent's own
    /// Mac `sizeOwnerHost` is this Mac's own owner attach.
    public static func owner(_ a: Agent, local: Bool) -> String? {
        if let id = a.sizeOwner, !id.isEmpty { return id }
        return a.sizeOwnerHost && !local ? hostOwner : nil
    }

    /// Whether another device owns the agent's PTY size now.
    public static func remoteOwned(_ a: Agent, local: Bool) -> Bool { owner(a, local: local) != nil }

    /// Whether a pane's take-back still stands after the agent's owner
    /// (`owner(_:local:)`) went from `old` to `new`: only while the same
    /// remote owner is shown. Cleared (the daemon saw the Mac's owner
    /// attach) or a new remote owner: the normal rule again.
    public static func keepsTakeBack(old: String?, new: String?) -> Bool {
        guard let new, !new.isEmpty else { return false }
        return old == new
    }

    /// The tile's badge while another device owns the size: its name, the
    /// agent's Mac (`machine`) when that Mac owns it, and without a name
    /// (enforcement off) no guess at the device's kind.
    public static func badge(_ a: Agent, local: Bool, machine: String) -> String? {
        switch owner(a, local: local) {
        case nil: return nil
        case hostOwner?: return "On \(machine)"
        default: return "On \(a.sizeOwnerName ?? "another device")"
        }
    }
}

extension AppEnvironment {
    /// The command for a terminal attaching as `mode`.
    public func attachArgv(id: String, _ mode: SizeOwner.Attach) -> [String] {
        switch mode {
        case .owner: attachArgv(id: id, readOnly: false, owner: true)
        case .view: attachArgv(id: id, readOnly: true, owner: false, view: true, fit: true)
        }
    }
}
