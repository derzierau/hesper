import Foundation

/// A typed relay address: "relay.example.com" is
/// `https://relay.example.com`. Only the origin is kept.
public enum RelayAddress {
    public static func parse(_ text: String) -> URL? {
        var text = text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !text.isEmpty, !text.contains(" ") else { return nil }
        if !text.contains("://") { text = "https://" + text }
        guard let url = URLComponents(string: text), url.scheme == "https" || url.scheme == "http",
            let host = url.host, !host.isEmpty, host.contains(".") || host == "localhost"
        else { return nil }
        var origin = URLComponents()
        origin.scheme = url.scheme
        origin.host = host
        origin.port = url.port
        return origin.url
    }

    /// A host only reachable on a private network (LAN, VPN, Tailscale),
    /// where "Is your VPN on?" is the likely answer.
    public static func isPrivate(_ host: String) -> Bool {
        let host = host.lowercased()
        if !host.contains(".") { return true }
        for suffix in [".local", ".lan", ".internal", ".home.arpa", ".ts.net"] where host.hasSuffix(suffix) { return true }
        let octets = host.split(separator: ".").compactMap { Int($0) }
        guard octets.count == 4 else { return false }
        switch (octets[0], octets[1]) {
        case (10, _), (127, _), (192, 168), (169, 254): return true
        case (172, 16...31), (100, 64...127): return true
        default: return false
        }
    }
}

extension PairingError {
    /// What the screens say (docs/iphone/onboarding.md, "Empty and failure
    /// states"); nil when there is nothing to say (cancelled).
    public func message(macName: String? = nil) -> String? {
        switch self {
        case .notAHesperCode: return "Point at the code in Hesper › Settings › iPhone."
        case .offerExpired: return "This code has expired. Your Mac shows a new one."
        case .relayUnreachable(let host):
            return RelayAddress.isPrivate(host) ? "Can't reach \(host). Is your VPN on?" : "Can't reach \(host)."
        case .notAllowed(let login): return "This relay doesn't let \(login) in. Ask its owner to add your GitHub account."
        case .cancelled: return nil
        case .macOffline(let m): return "\(macName ?? m) is asleep. It approves the iPhone when it wakes."
        case .denied(let m): return "\(macName ?? m) said no to this iPhone."
        case .offerUnsupported(let m): return "\(macName ?? m) needs a newer Hesper to approve by code. Approve it there instead."
        case .keyChanged(let m): return "\(macName ?? m) has a different key than its code. Scan the code again."
        case .failed(let why): return why
        }
    }
}

/// What a Mac lets this phone do, in words: "Can answer, type and start
/// agents · no shells" (rights from docs/remote-shell-contract.md: answer
/// is answering prompts, type is typing, transfer covers starting and
/// stopping agents).
public enum RightsCopy {
    public static func summary(_ rights: [String]) -> String {
        let verbs = [("answer", "answer"), ("type", "type"), ("transfer", "start")].filter { rights.contains($0.0) }.map(\.1)
        let can: String
        switch verbs.count {
        case 0: can = "Can watch agents"
        case 1: can = "Can \(verbs[0]) agents"
        default: can = "Can \(verbs.dropLast().joined(separator: ", ")) and \(verbs.last!) agents"
        }
        return can + (rights.contains("shell") ? " and shells" : " · no shells")
    }
}
