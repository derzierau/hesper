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
        public init(agentID: String, window: Int?, focusRole: Bool, readWrite: Bool) {
            self.agentID = agentID; self.window = window; self.focusRole = focusRole; self.readWrite = readWrite
        }
    }

    /// Which candidates own their agent's PTY size: the read-write ones in
    /// the owner window (the app's key window among ours), and at most one
    /// per agent (a focus view before a tile, then the first listed). With
    /// no owner window nobody owns (the daemon follows the views' fit).
    public static func owners(_ cs: [Candidate], ownerWindow: Int?) -> [Bool] {
        var out = Array(repeating: false, count: cs.count)
        guard let ownerWindow else { return out }
        var best: [String: Int] = [:]
        for (i, c) in cs.enumerated() where c.readWrite && c.window == ownerWindow {
            if let j = best[c.agentID] {
                if c.focusRole && !cs[j].focusRole { best[c.agentID] = i }
            } else {
                best[c.agentID] = i
            }
        }
        for i in best.values { out[i] = true }
        return out
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
