import Foundation

/// Settings › iPhone: the code an iPhone scans to connect to this Mac
/// (docs/iphone/onboarding.md, "Interfaces"). hesperd keeps the offers;
/// this is the pane's side: the rights it asks for, when it asks for a
/// new code, and what it shows.
public enum PhonePairingRPC {
    /// `{rights?}` → `{payload, expires, id}`.
    public static let offer = "devices.offer"
    /// `{id}`.
    public static let cancel = "devices.offerCancel"
    /// Event `{offer, device, name, rights}`: an offer was used.
    public static let paired = "devices.paired"

    public static func offerParams(_ rights: PhoneRights) -> JSONValue {
        ["rights": .array(rights.rights.map(JSONValue.string))]
    }

    public static func cancelParams(_ id: String) -> JSONValue { ["id": .string(id)] }
}

/// What the iPhone may do on this Mac, as the pane's toggles. `observe`
/// is always in; shells are off unless switched on.
public struct PhoneRights: Equatable, Sendable {
    public enum Toggle: String, CaseIterable, Sendable {
        case answer, type, transfer, shell

        public var title: String {
            switch self {
            case .answer: return "Answer and approve"
            case .type: return "Type into agents"
            case .transfer: return "Start and stop agents"
            case .shell: return "Shells"
            }
        }

        /// A note after the title (shells need the strong key).
        public var detail: String? { self == .shell ? "Face ID on the iPhone" : nil }
    }

    public var answer: Bool
    public var type: Bool
    public var transfer: Bool
    public var shell: Bool

    public init(answer: Bool = true, type: Bool = true, transfer: Bool = true, shell: Bool = false) {
        self.answer = answer; self.type = type; self.transfer = transfer; self.shell = shell
    }

    /// From wire rights (unknown names are ignored).
    public init(_ rights: [String]) {
        self.init(answer: rights.contains("answer"), type: rights.contains("type"),
                  transfer: rights.contains("transfer"), shell: rights.contains("shell"))
    }

    public subscript(_ t: Toggle) -> Bool {
        get {
            switch t {
            case .answer: return answer
            case .type: return type
            case .transfer: return transfer
            case .shell: return shell
            }
        }
        set {
            switch t {
            case .answer: answer = newValue
            case .type: type = newValue
            case .transfer: transfer = newValue
            case .shell: shell = newValue
            }
        }
    }

    /// The wire rights for `devices.offer`.
    public var rights: [String] { ["observe"] + Toggle.allCases.filter { self[$0] }.map(\.rawValue) }

    /// Granted rights as the toggles' titles, in toggle order (observe
    /// goes without saying).
    public static func summary(_ rights: [String]) -> [String] {
        let r = PhoneRights(rights)
        return Toggle.allCases.filter { r[$0] }.map(\.title)
    }
}

/// `devices.offer`'s answer.
public struct PhoneOffer: Equatable, Sendable {
    public var id: String
    /// The scanned text: `HESPER1:` + base64url JSON.
    public var payload: String
    public var expires: Date

    public init(id: String, payload: String, expires: Date) {
        self.id = id; self.payload = payload; self.expires = expires
    }

    /// nil: not an offer (`expires` is Unix seconds).
    public init?(json v: JSONValue) {
        guard let id = v["id"]?.stringValue, !id.isEmpty,
              let payload = v["payload"]?.stringValue, !payload.isEmpty,
              let expires = v["expires"]?.doubleValue else { return nil }
        self.init(id: id, payload: payload, expires: Date(timeIntervalSince1970: expires))
    }
}

/// `devices.paired`'s params.
public struct PhonePaired: Equatable, Sendable {
    public var offer: String
    public var device: String
    public var name: String
    public var rights: [String]

    public init(offer: String, device: String, name: String, rights: [String]) {
        self.offer = offer; self.device = device; self.name = name; self.rights = rights
    }

    public init?(params v: JSONValue) {
        guard let offer = v["offer"]?.stringValue, let device = v["device"]?.stringValue else { return nil }
        self.init(offer: offer, device: device, name: v["name"]?.stringValue ?? "iPhone",
                  rights: v["rights"]?.arrayValue?.compactMap(\.stringValue) ?? [])
    }
}

/// The pane's state machine. Each `devices.offer` call takes a `request()`
/// number, so an answer that a newer request (or a rights change)
/// overtook is cancelled instead of shown. Renewing keeps the earlier
/// codes open until they expire: a phone that scanned one is still
/// signing in. A rights change, a pairing and closing the pane cancel
/// them.
public struct PhonePairing: Equatable, Sendable {
    public enum State: Equatable, Sendable {
        /// Asking hesperd for a code.
        case starting
        case waiting(PhoneOffer)
        case paired(PhonePaired)
        /// Retried after `retryAfter`.
        case failed(String)
        /// hesperd has no `devices.offer`: pair by code instead.
        case unsupported
    }

    /// The pane says "Renews every 2 minutes".
    public static let renewEvery: TimeInterval = 120
    /// A code that would expire sooner is renewed this long before.
    public static let renewMargin: TimeInterval = 15
    public static let retryAfter: TimeInterval = 5

    public private(set) var state = State.starting
    public private(set) var rights: PhoneRights
    /// Offers this pane got and has not cancelled.
    private var issued: [PhoneOffer] = []
    private var shownAt: Date?
    private var generation = 0

    public init(rights: PhoneRights = PhoneRights()) { self.rights = rights }

    /// Before calling `devices.offer`: its number for `offered`/`failed`.
    /// From `paired`, this is "Show another code".
    public mutating func request() -> Int {
        generation += 1
        if case .paired = state { state = .starting }
        return generation
    }

    /// `devices.offer` answered. Returns the ids to cancel (a stale answer).
    public mutating func offered(_ o: PhoneOffer, request: Int, at now: Date) -> [String] {
        guard request == generation else { return [o.id] }
        issued.removeAll { $0.expires <= now }
        issued.append(o)
        state = .waiting(o)
        shownAt = now
        return []
    }

    public mutating func failed(_ error: any Error, request: Int) {
        guard request == generation else { return }
        if let e = error as? RPCError, CloseRPC.missing(e) {
            state = .unsupported
        } else if let e = error as? RPCError {
            state = .failed(e.message)
        } else if let e = error as? ConnectionError, e == .notConnected {
            state = .failed("Not connected to hesperd.")
        } else {
            state = .failed("\(error)")
        }
    }

    /// New toggles: the open codes carry the old rights. Returns the ids
    /// to cancel; the caller asks for a new code.
    public mutating func setRights(_ r: PhoneRights, at now: Date) -> [String] {
        guard r != rights else { return [] }
        rights = r
        generation += 1
        if case .waiting = state { state = .starting }
        return takeOpen(at: now)
    }

    /// `devices.paired`: nil when it isn't one of this pane's offers (say,
    /// `hesperctl phone` in a terminal); else the other ids to cancel.
    public mutating func paired(_ e: PhonePaired, at now: Date) -> [String]? {
        guard issued.contains(where: { $0.id == e.offer }) else { return nil }
        issued.removeAll { $0.id == e.offer }
        generation += 1
        state = .paired(e)
        return takeOpen(at: now)
    }

    /// The pane closes: the ids to cancel.
    public mutating func close(at now: Date) -> [String] {
        generation += 1
        state = .starting
        return takeOpen(at: now)
    }

    /// Unexpired offers, oldest first.
    public func open(at now: Date) -> [String] { issued.filter { $0.expires > now }.map(\.id) }

    /// How long until asking for the next code (nil: not waiting).
    public func renewDelay(now: Date) -> TimeInterval? {
        guard case .waiting(let o) = state, let shownAt else { return nil }
        let at = min(shownAt.addingTimeInterval(Self.renewEvery), o.expires.addingTimeInterval(-Self.renewMargin))
        return max(0, at.timeIntervalSince(now))
    }

    /// How long until asking again after a failure (nil: don't).
    public var retryDelay: TimeInterval? {
        if case .failed = state { return Self.retryAfter }
        return nil
    }

    /// The heading: "Connect your iPhone", or "<name> connected".
    public var title: String {
        if case .paired(let p) = state { return "\(p.name) connected" }
        return "Connect your iPhone"
    }

    private mutating func takeOpen(at now: Date) -> [String] {
        let ids = open(at: now)
        issued.removeAll()
        return ids
    }
}
