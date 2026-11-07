import Foundation
import HesperCore
import HesperFleet
import Synchronization

/// The live `LinkClient`: the owner's relay, and through it one
/// end-to-end channel and one link per Mac that approved this phone.
/// Commands are sent once and never replayed after a reconnect.
public final class LiveLink: LinkClient {
    let context: LinkContext
    private let state = Mutex(State())

    private struct State {
        /// By machine id, for the Macs linked now.
        var channels: [String: HostChannel] = [:]
        /// The fleet's machine name (the host's short name) → machine id.
        var ids: [String: String] = [:]
        var listeners: [UUID: AsyncStream<LinkUpdate>.Continuation] = [:]
        /// One connection however many listen: the relay keeps only the
        /// newest connection of a device.
        var run: Task<Void, Never>?
    }

    public init(context: LinkContext) { self.context = context }

    /// Updates from every paired Mac. Listeners share one connection; it
    /// opens with the first and closes after the last.
    public func updates() -> AsyncStream<LinkUpdate> {
        let (stream, continuation) = AsyncStream.makeStream(of: LinkUpdate.self)
        let key = UUID()
        state.withLock { s in
            s.listeners[key] = continuation
            if s.run == nil { s.run = Task { await self.run() } }
        }
        continuation.onTermination = { [weak self] _ in
            let run = self?.state.withLock { s -> Task<Void, Never>? in
                s.listeners[key] = nil
                guard s.listeners.isEmpty else { return nil }
                defer { s.run = nil }
                return s.run
            }
            run?.cancel()
        }
        return stream
    }

    /// Connects, follows the inventory and links each approved Mac, and
    /// connects again (backing off) when the relay goes away.
    private func run() async {
        let out = Broadcast(link: self)
        var delay = 1.0
        while !Task.isCancelled {
            if let relay = try? await context.connect() {
                delay = 1
                await follow(relay.0, me: relay.1, out)
                relay.0.close()
            }
            try? await Task.sleep(for: .seconds(delay))
            delay = min(delay * 2, 30)
        }
    }

    /// Yields to every listener.
    struct Broadcast: Sendable {
        let link: LiveLink
        func yield(_ update: LinkUpdate) { link.broadcast(update) }
    }

    private func follow(_ relay: RelayConnection, me: ControllerIdentity, _ out: Broadcast) async {
        var links: [String: Task<Void, Never>] = [:]
        var names: [String: String] = [:]
        defer {
            for (id, task) in links {
                task.cancel()
                out.yield(.reachability(.unreachable(since: Date()), machine: names[id] ?? id))
            }
            state.withLock { $0.channels = [:] }
        }
        for await event in relay.events {
            guard case .inventory(let list) = event else { break }
            let hosts = context.hosts.all().filter(\.value.approved)
            let paired = list.filter { hosts[$0.id] != nil }
            for m in paired { names[m.id] = Self.name(m) }
            state.withLock { s in
                s.ids = Dictionary(paired.map { (Self.name($0), $0.id) }, uniquingKeysWith: { a, _ in a })
            }
            out.yield(.macs(paired.map { Machine(short: Self.name($0), name: $0.name, online: $0.online) }))
            for m in paired {
                let name = Self.name(m)
                if m.online, links[m.id] == nil, let host = hosts[m.id] {
                    let channel = HostChannel(relay: relay, machine: m.id, hostKey: host.hostKey, me: me)
                    state.withLock { $0.channels[m.id] = channel }
                    links[m.id] = Task { await self.link(channel, name: name, out) }
                } else if !m.online, let task = links.removeValue(forKey: m.id) {
                    task.cancel()
                    state.withLock { _ = $0.channels.removeValue(forKey: m.id) }
                    out.yield(.reachability(.unreachable(since: Date()), machine: name))
                }
            }
        }
    }

    /// One Mac's link: its drafts, then its events, again after a break
    /// until cancelled. A changed host key ends it for good.
    private func link(_ channel: HostChannel, name: String, _ out: Broadcast) async {
        var delay = 1.0
        while !Task.isCancelled {
            out.yield(.reachability(.connecting, machine: name))
            if let data = try? await channel.call("drafts.list"), let drafts = try? JSONDecoder().decode([Draft].self, from: data) {
                out.yield(.drafts(drafts, machine: name))
            }
            let live = Mutex(false)
            do {
                try await channel.link { event in
                    let first = live.withLock { was in
                        defer { was = true }
                        return !was
                    }
                    if first {
                        out.yield(.reachability(.live(direct: false), machine: name))
                    }
                    out.yield(.event(event, machine: name))
                }
            } catch let e as RelayError where e.code == "key_changed" || e.code == "forbidden" {
                out.yield(.reachability(.unreachable(since: Date()), machine: name))
                return
            } catch {}
            out.yield(.reachability(.unreachable(since: Date()), machine: name))
            if live.withLock({ $0 }) { delay = 1 }
            try? await Task.sleep(for: .seconds(delay))
            delay = min(delay * 2, 30)
        }
    }

    static func name(_ m: RelayMachine) -> String { m.short ?? m.name }

    public func perform(_ command: LinkCommand) async throws {
        let (machine, method, params) = try Self.request(for: command)
        let channel = state.withLock { s in s.ids[machine].flatMap { s.channels[$0] } }
        guard let channel else { throw RelayError(code: "unavailable", message: "\(machine) is not connected") }
        let strong: Bool
        if case .spawn(let r) = command { strong = r.kind == "shell" } else { strong = false }
        let result = try await channel.call(method, params: params, strong: strong)
        switch command {
        case .saveDraft(let draft, let machine):
            let saved = (try? JSONDecoder().decode(Draft.self, from: result)) ?? draft
            broadcast(.draftChanged(saved, machine: machine))
        case .removeDraft(let id, let machine):
            broadcast(.draftRemoved(id: id, machine: machine))
        default:
            break
        }
    }

    /// A read-only attach on the agent's Mac. While the relay reconnects
    /// it waits (up to 10 s) for the Mac's channel; an attach open when a
    /// link breaks ends (its frames finish without `.exit`) and the caller
    /// attaches again, which waits for the relink.
    public func attach(agent: String, cols: Int, rows: Int) async throws -> any AttachStream {
        let machine = String(agent.split(separator: "/").first ?? "")
        let clock = ContinuousClock()
        let deadline = clock.now + .seconds(10)
        while true {
            let found = state.withLock { s -> (known: Bool, channel: HostChannel?) in
                guard let id = s.ids[machine] else { return (false, nil) }
                return (true, s.channels[id])
            }
            guard found.known else { throw RelayError(code: "unavailable", message: "\(machine) is not connected") }
            // A channel of the previous relay connection may linger until
            // `follow` clears it: only one whose link is up will do.
            if let channel = found.channel, await channel.linked {
                return try await channel.attach(agent: agent, cols: cols, rows: rows, wait: .zero)
            }
            guard clock.now < deadline else { throw RelayError(code: "unavailable", message: "\(machine) is not connected") }
            try await Task.sleep(for: .milliseconds(100))
        }
    }

    fileprivate func broadcast(_ update: LinkUpdate) {
        for l in state.withLock({ Array($0.listeners.values) }) { l.yield(update) }
    }

    /// The Mac, method and params of a command (relay/docs/protocol.md,
    /// "Agents"; the local socket's params).
    static func request(for command: LinkCommand) throws -> (machine: String, method: String, params: Data) {
        func on(_ agent: String) -> String { String(agent.split(separator: "/").first ?? "") }
        let encode = { (value: JSONValue) in try JSONEncoder().encode(value) }
        switch command {
        case .answer(let id, let decision, let message):
            var params: [String: JSONValue] = ["id": .string(id), "decision": .string(decision.rawValue)]
            if let message, !message.isEmpty { params["message"] = .string(message) }
            return (on(id), "agents.answer", try encode(.object(params)))
        case .choose(let id, let option):
            // The option's number, typed: no paste, no Enter (as `hesperctl choose`).
            return (on(id), "agents.input", try encode(["id": .string(id), "text": .string(String(option))]))
        case .reply(let id, let text):
            return (on(id), "agents.input", try encode(["id": .string(id), "text": .string(text), "paste": true, "submit": true]))
        case .spawn(var request):
            guard let machine = request.machine else { throw RelayError(code: "invalid", message: "No Mac to start on") }
            request.machine = nil
            return (machine, "agents.spawn", try encode(request.params))
        case .stop(let id):
            return (on(id), "agents.stop", try encode(["id": .string(id)]))
        case .resume(let id):
            return (on(id), "agents.resume", try encode(["id": .string(id)]))
        case .close(let id):
            return (on(id), "agents.close", try encode(["id": .string(id)]))
        case .saveDraft(let draft, let machine):
            return (machine, "drafts.save", try encode(draft.params))
        case .removeDraft(let id, let machine):
            return (machine, "drafts.remove", try encode(["id": .string(id)]))
        }
    }
}
