import CoreGraphics
import HesperCore

/// What a Mac lets this phone do with an agent's terminal size.
public struct HostSizing: Equatable, Sendable {
    /// The host publishes `capabilities.sizeOwner`: it names a terminal's
    /// size owner in the agent's info (`Agent.sizeOwner`).
    public var publishesOwner: Bool
    /// The rights the Mac granted this phone; empty when not recorded.
    public var rights: [String]
    /// This phone's controller id there, as `Agent.sizeOwner` names it.
    public var device: String

    public init(publishesOwner: Bool, rights: [String] = [], device: String) {
        self.publishesOwner = publishesOwner
        self.rights = rights
        self.device = device
    }

    /// The phone may attach read-write: the `type` right. Unknown rights
    /// try it; a `forbidden` reply falls back to read only.
    public var canType: Bool { rights.isEmpty || rights.contains("type") }

    /// The phone may take the size: the capability, and the `type` right
    /// a read-write attach needs. Unknown rights try it; a `forbidden`
    /// reply falls back to the mirror.
    public var canOwn: Bool { publishesOwner && canType }
}

/// How the agent view attaches (docs/iphone/writable-terminal.md, "C. Take
/// over"): fitted, the Mac's terminal sized to the phone's terminal area
/// at the phone's font, or a mirror of the Mac's own grid.
public struct AttachPlan: Equatable, Sendable {
    public enum Mode: Equatable, Sendable {
        /// The Mac's grid, scaled and panned to the phone; read-write
        /// when the phone may type, never the size owner.
        case mirror
        /// The phone owns the size: the grid is what fits its area.
        case fitted
    }

    public var mode: Mode
    public var grid: GridSize
    /// The attach is `rw`: what the terminal writes back (wheel input as
    /// mouse reports or arrow keys) goes to the agent. Always true fitted.
    public var typing: Bool
    /// A fitted attach is the size owner (`owner: true`, mode `rw`).
    public var owner: Bool { mode == .fitted }

    /// The smallest grid the phone asks for.
    public static let minimum = GridSize(cols: 20, rows: 5)
    /// The largest hesperd accepts.
    public static let maximum = 1000
    /// The mirror's grid when the Mac has not reported the agent's size.
    public static let fallbackSize = GridSize(cols: 80, rows: 24)

    /// `typing` nil is `rw` exactly when fitted; a fitted plan always types.
    public init(mode: Mode, grid: GridSize, typing: Bool? = nil) {
        self.mode = mode
        self.grid = grid
        self.typing = mode == .fitted || (typing ?? false)
    }

    /// A mirror at the Mac's grid, read-write when `typing`.
    public static func mirror(_ agent: Agent, typing: Bool = false) -> AttachPlan {
        AttachPlan(mode: .mirror, grid: agent.size ?? fallbackSize, typing: typing)
    }

    /// A read-only mirror: what is left when the Mac refused `rw`.
    public static func readOnly(_ agent: Agent) -> AttachPlan { mirror(agent, typing: false) }

    /// Fitted when the host lets the phone own the size and the area is
    /// measured; the mirror otherwise (an older host, no `type` right),
    /// read-write when the phone may type.
    public static func make(_ agent: Agent, host: HostSizing?, area: CGSize, cell: CGSize) -> AttachPlan {
        guard let host, host.canOwn, let grid = fit(area: area, cell: cell) else {
            return mirror(agent, typing: host?.canType ?? false)
        }
        return AttachPlan(mode: .fitted, grid: grid)
    }

    /// The whole cells that fit `area` at `cell`, never below `minimum`;
    /// nil until both are measured.
    public static func fit(area: CGSize, cell: CGSize) -> GridSize? {
        guard area.width > 0, area.height > 0, cell.width > 0, cell.height > 0 else { return nil }
        let cols = Int((area.width / cell.width).rounded(.down))
        let rows = Int((area.height / cell.height).rounded(.down))
        return GridSize(cols: min(maximum, max(minimum.cols, cols)), rows: min(maximum, max(minimum.rows, rows)))
    }

    /// An owner attach refused with this code is made again as a mirror:
    /// `forbidden` (no `type` right) or `unsupported`.
    public static func retriesAsMirror(_ code: String) -> Bool {
        code == "forbidden" || code == "unsupported"
    }
}

/// Whether a fitted attach still owns the size. The Mac takes it back on
/// its next keystroke or click (latest owner wins); the phone sees that as
/// a `.size` it did not ask for, or as the agent's `sizeOwner` naming
/// someone else after it named this phone. Then the view is a mirror.
public struct FitState: Equatable, Sendable {
    public private(set) var mode: AttachPlan.Mode
    /// The grids asked for (the attach's, then each RESIZE) from the
    /// oldest the Mac may still answer with; the newest last.
    private var asked: [GridSize]
    /// This phone's controller id on the Mac; nil when unknown.
    private let device: String?
    /// The agent's `sizeOwner` has named this phone.
    private var owned = false

    public init(plan: AttachPlan, device: String?) {
        mode = plan.mode
        asked = plan.owner ? [plan.grid] : []
        self.device = device
    }

    public var isFitted: Bool { mode == .fitted }

    /// The grid last asked for; nil as a mirror.
    public var grid: GridSize? { isFitted ? asked.last : nil }

    /// A `.size` frame. A grid the phone asked for keeps the fit (and
    /// answers the asks before it); any other is the Mac's own. True when
    /// it turned the view into a mirror.
    public mutating func size(_ grid: GridSize) -> Bool {
        guard isFitted else { return false }
        if let i = asked.lastIndex(of: grid) {
            asked.removeFirst(i)
            return false
        }
        return drop()
    }

    /// The area now fits `grid`: the RESIZE to send (recorded as asked),
    /// or nil as a mirror or when it is the grid already asked for.
    public mutating func resize(to grid: GridSize) -> GridSize? {
        guard isFitted, grid != asked.last else { return nil }
        asked.append(grid)
        return grid
    }

    /// The agent's `sizeOwner`, from its info. Once it named this phone,
    /// any other value means the Mac took the size back. True when it
    /// turned the view into a mirror.
    public mutating func owner(_ owner: String?) -> Bool {
        guard isFitted, let device else { return false }
        if owner == device {
            owned = true
            return false
        }
        return owned ? drop() : false
    }

    private mutating func drop() -> Bool {
        mode = .mirror
        asked = []
        return true
    }
}
