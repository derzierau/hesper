import Foundation

// Settings › Steward: the switch, the level and the Mac that runs it
// (decision 3: off by default; one shared record, so exactly one Mac runs
// it). Pure: the app shows `StewardSettings` and sends its patches
// (steward.set).

public struct StewardSettings: Equatable, Sendable {
    public struct Level: Equatable, Sendable, Identifiable {
        public var id: StewardLevel { level }
        public var level: StewardLevel
        public var title: String
        public var detail: String
    }

    public struct MachineChoice: Equatable, Sendable, Identifiable {
        public var id: String { machine }
        public var machine: String
        public var title: String
        public var online: Bool
        public var isLocal: Bool
    }

    public static let levels: [Level] = [
        Level(level: .watch, title: "Watch", detail: "Keeps the loops up to date"),
        Level(level: .suggest, title: "Suggest", detail: "Also saves drafts for next steps"),
        Level(level: .act, title: "Act", detail: "Also starts ready drafts and merges or splits loops"),
    ]

    public let status: StewardStatus
    public let local: String
    /// Every Mac it could run on: this one first, then the others by name
    /// (offline ones too: it starts there when that Mac comes back).
    public let machines: [MachineChoice]

    public init(status: StewardStatus, machines: [Machine], local: String) {
        self.status = status
        self.local = local
        var list = machines.map { MachineChoice(machine: $0.short, title: $0.displayName, online: $0.online, isLocal: $0.short == local) }
        if !list.contains(where: { $0.machine == local }) { list.append(MachineChoice(machine: local, title: local, online: true, isLocal: true)) }
        if let m = status.machine, !list.contains(where: { $0.machine == m }) { list.append(MachineChoice(machine: m, title: m, online: false, isLocal: false)) }
        self.machines = list.sorted { a, b in a.isLocal != b.isLocal ? a.isLocal : (a.title.lowercased(), a.machine) < (b.title.lowercased(), b.machine) }
    }

    public var enabled: Bool { status.enabled }
    public var level: StewardLevel { status.level }
    /// The Mac it runs on (this one when none is set yet).
    public var machine: String { status.machine ?? local }
    public var runsHere: Bool { machine == local }

    /// One line under the switch: "Off", "Runs on mini · 12 wakes today".
    public var summary: String {
        guard status.enabled else { return "Off" }
        let whereText = runsHere ? "Runs on this Mac" : "Runs on \(machines.first { $0.machine == machine }?.title ?? machine)"
        guard runsHere else { return whereText }
        var parts = [whereText, status.wakes == 1 ? "1 wake today" : "\(status.wakes) wakes today"]
        if status.queued > 0 { parts.append("\(status.queued) waiting") }
        if status.agent == nil { parts.append("not running") }
        return parts.joined(separator: " · ")
    }

    // MARK: Intents (steward.set)

    /// Turning it on also pins it to this Mac when no Mac is set yet.
    public func setEnabled(_ on: Bool) -> StewardPatch? {
        guard on != status.enabled else { return nil }
        return StewardPatch(enabled: on, machine: on && status.machine == nil ? local : nil)
    }

    public func setLevel(_ l: StewardLevel) -> StewardPatch? { l == status.level ? nil : StewardPatch(level: l) }

    public func setMachine(_ m: String) -> StewardPatch? {
        guard m != machine, machines.contains(where: { $0.machine == m }) else { return nil }
        return StewardPatch(machine: m)
    }
}
