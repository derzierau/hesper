import Foundation

// Removing a device (devices.list, devices.remove): every device of the
// owner as hesperd merges it from the relay and this Mac's files, and the
// one-step removal from all of them. Settings › Machines lists the Macs
// (hosts) and their other sign-ins; the iPhones list shows the phones.

// MARK: Wire

public enum DevicesRPC {
    public static let list = "devices.list"
    public static let remove = "devices.remove"

    /// devices.remove's params; `localOnly` skips the relay (a relay too
    /// old to revoke).
    public static func removeParams(_ id: String, localOnly: Bool) -> JSONValue {
        var p: [String: JSONValue] = ["deviceId": .string(id)]
        if localOnly { p["localOnly"] = true }
        return .object(p)
    }
}

/// One entry of devices.list.
public struct Device: Codable, Equatable, Sendable, Identifiable {
    public static let host = "host", controller = "controller"
    public static let mac = "mac", iphone = "iphone"

    public var id: String
    public var name: String
    /// The machine's short name (machines.json), for hosts.
    public var short: String?
    /// `host` or `controller`.
    public var role: String
    /// `mac` or `iphone`.
    public var kind: String
    /// A live relay connection.
    public var online: Bool
    /// One of this Mac's own two ids (wire: `self`).
    public var isSelf: Bool
    public var approvedHere: Bool
    public var trustedHere: Bool
    public var relay: Bool

    public init(id: String, name: String, short: String? = nil, role: String, kind: String, online: Bool = false,
                isSelf: Bool = false, approvedHere: Bool = false, trustedHere: Bool = false, relay: Bool = false) {
        self.id = id; self.name = name; self.short = short; self.role = role; self.kind = kind; self.online = online
        self.isSelf = isSelf; self.approvedHere = approvedHere; self.trustedHere = trustedHere; self.relay = relay
    }

    enum CodingKeys: String, CodingKey {
        case id, name, short, role, kind, online, isSelf = "self", approvedHere, trustedHere, relay
    }

    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        id = try c.decode(String.self, forKey: .id)
        name = try c.decodeIfPresent(String.self, forKey: .name) ?? id
        short = try c.decodeIfPresent(String.self, forKey: .short)
        role = try c.decodeIfPresent(String.self, forKey: .role) ?? Device.controller
        kind = try c.decodeIfPresent(String.self, forKey: .kind) ?? Device.mac
        online = try c.decodeIfPresent(Bool.self, forKey: .online) ?? false
        isSelf = try c.decodeIfPresent(Bool.self, forKey: .isSelf) ?? false
        approvedHere = try c.decodeIfPresent(Bool.self, forKey: .approvedHere) ?? false
        trustedHere = try c.decodeIfPresent(Bool.self, forKey: .trustedHere) ?? false
        relay = try c.decodeIfPresent(Bool.self, forKey: .relay) ?? false
    }

    public var isHost: Bool { role == Device.host }
    public var isIPhone: Bool { kind == Device.iphone }
    /// hesperd refuses this Mac's own ids: no Remove on them.
    public var removable: Bool { !isSelf }
}

/// The result of devices.list.
public struct DevicesList: Codable, Equatable, Sendable {
    public var devices: [Device]
}

/// devices.list split the way Settings shows it.
public struct DeviceGroups: Equatable, Sendable {
    /// Macs that run agents (hosts); this Mac first.
    public var macs: [Device]
    /// A Mac's controller records that are not this Mac's own.
    public var otherSignIns: [Device]
    public var iPhones: [Device]

    /// Keeps hesperd's order inside each group.
    public init(_ devices: [Device]) {
        let hosts = devices.filter { $0.kind == Device.mac && $0.isHost }
        macs = hosts.filter(\.isSelf) + hosts.filter { !$0.isSelf }
        otherSignIns = devices.filter { $0.kind == Device.mac && $0.role == Device.controller && !$0.isSelf }
        iPhones = devices.filter(\.isIPhone)
    }
}

/// The result of devices.remove: each step "done", "skipped" or its error.
public struct RemoveOutcome: Codable, Equatable, Sendable {
    public var relay: String
    public var approval: String
    public var trust: String
    public var names: String
    /// Offline controller records of the same name the removal also revoked.
    public var signedOut: [String]

    public init(relay: String, approval: String, trust: String, names: String, signedOut: [String] = []) {
        self.relay = relay; self.approval = approval; self.trust = trust; self.names = names; self.signedOut = signedOut
    }

    enum CodingKeys: String, CodingKey { case relay, approval, trust, names, signedOut }

    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        relay = try c.decode(String.self, forKey: .relay)
        approval = try c.decode(String.self, forKey: .approval)
        trust = try c.decode(String.self, forKey: .trust)
        names = try c.decode(String.self, forKey: .names)
        signedOut = try c.decodeIfPresent([String].self, forKey: .signedOut) ?? []
    }

    static func ok(_ step: String) -> Bool { step == "done" || step == "skipped" }

    /// The one line Settings shows after a removal: "Removed" when every
    /// step is done or skipped, else the first step that failed in words.
    public var summary: String {
        let steps = [("the relay", relay), ("this Mac's approvals", approval),
                     ("this Mac's trusted hosts", trust), ("this Mac's machine names", names)]
        var line = "Removed"
        if let (what, err) = steps.first(where: { !Self.ok($0.1) }) { line = "Not removed from \(what): \(err)" }
        if !signedOut.isEmpty {
            line += signedOut.count == 1 ? ", also signed out its controller record" : ", also signed out its \(signedOut.count) controller records"
        }
        return line
    }
}

/// Why devices.list or devices.remove failed, as Settings reacts to it.
public enum DevicesFailure: Equatable, Sendable {
    /// An older hesperd (-32601): the hesperctl text, no Remove buttons.
    case unsupportedDaemon
    /// The relay is down; nothing changed.
    case relayUnreachable
    /// The relay is too old to revoke; nothing changed. Offer "Remove here".
    case relayTooOld
    case failed(String)

    public init(_ error: any Error) {
        if let e = error as? RPCError {
            if e.code == CloseRPC.noMethod { self = .unsupportedDaemon; return }
            switch e.dataCode {
            case "unavailable": self = .relayUnreachable
            case "unsupported": self = .relayTooOld
            default: self = .failed(e.message)
            }
        } else if let e = error as? ConnectionError {
            self = .failed(e.description)
        } else {
            self = .failed(error.localizedDescription)
        }
    }

    /// The inline text for a failure that is not asked about.
    public var text: String {
        switch self {
        case .unsupportedDaemon: return "This Mac's hesperd can't remove devices. Update Hesper."
        case .relayUnreachable: return "Not removed: the relay is unreachable. Try again later."
        case .relayTooOld: return "Your relay is too old to remove devices."
        case .failed(let why): return "Not removed: \(why)"
        }
    }
}
