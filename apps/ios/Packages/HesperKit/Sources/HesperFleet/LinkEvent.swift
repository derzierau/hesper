import Foundation
import HesperCore

/// One JSON event on a Mac's `agents.link` (channel 0): first the full
/// list, then a change or a removal at every change
/// (relay/docs/protocol.md, "Links").
public enum LinkEvent: Equatable, Sendable {
    /// `{"machine","list":[Agent…],"full":true}`: replaces what the Mac had.
    case list(machine: String, agents: [Agent], full: Bool)
    /// `{"changed":Agent}`: one agent, new or updated.
    case changed(Agent)
    /// `{"removed":"id"}`: the agent is gone (closed).
    case removed(id: String)
}

extension LinkEvent: Decodable {
    private enum Keys: String, CodingKey { case machine, list, full, changed, removed }

    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: Keys.self)
        if let agents = try c.decodeIfPresent([Agent].self, forKey: .list) {
            let machine = try c.decodeIfPresent(String.self, forKey: .machine) ?? agents.first?.machine ?? ""
            self = .list(machine: machine, agents: agents, full: try c.decodeIfPresent(Bool.self, forKey: .full) ?? true)
        } else if let agent = try c.decodeIfPresent(Agent.self, forKey: .changed) {
            self = .changed(agent)
        } else if let id = try c.decodeIfPresent(String.self, forKey: .removed) {
            self = .removed(id: id)
        } else {
            throw DecodingError.dataCorrupted(.init(codingPath: [], debugDescription: "not a link event: no list, changed or removed"))
        }
    }

    /// Decodes one event frame's payload.
    public static func decode(_ data: Data) throws -> LinkEvent {
        try JSONDecoder().decode(LinkEvent.self, from: data)
    }
}
