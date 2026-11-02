import Foundation
import HesperCore
import HesperFleet
import Synchronization

/// A link that plays the samples and answers commands the way a Mac would:
/// an answered agent goes back to work, a stopped one exits. For previews,
/// tests and running the app without a Mac.
public final class FixtureLink: LinkClient {
    private struct State {
        var agents: [String: Agent] = [:]
        var continuations: [UUID: AsyncStream<LinkUpdate>.Continuation] = [:]
        var sent: [LinkCommand] = []
        var nextID = 1
    }

    private let state = Mutex(State())
    private let initial: [LinkUpdate]

    public init(updates: [LinkUpdate] = Samples.initialUpdates()) {
        initial = updates
        state.withLock { s in
            for case .event(.list(_, let agents, _), _) in updates {
                for a in agents { s.agents[a.id] = a }
            }
        }
    }

    /// Every command performed so far, oldest first.
    public var sent: [LinkCommand] { state.withLock { $0.sent } }

    public func updates() -> AsyncStream<LinkUpdate> {
        let (stream, continuation) = AsyncStream<LinkUpdate>.makeStream()
        let id = UUID()
        for u in initial { continuation.yield(u) }
        state.withLock { $0.continuations[id] = continuation }
        continuation.onTermination = { [weak self] _ in
            _ = self?.state.withLock { $0.continuations.removeValue(forKey: id) }
        }
        return stream
    }

    public func perform(_ command: LinkCommand) async throws {
        let update: LinkUpdate? = state.withLock { s in
            s.sent.append(command)
            switch command {
            case .answer(let id, _), .choose(let id, _), .reply(let id, _), .resume(let id):
                return Self.change(&s, id) {
                    $0.state = .working
                    $0.attention = nil
                    $0.exit = nil
                }
            case .stop(let id):
                return Self.change(&s, id) {
                    $0.state = .exited
                    $0.exit = ExitInfo(signal: "stopped")
                }
            case .close(let id):
                guard let a = s.agents.removeValue(forKey: id) else { return nil }
                return .event(.removed(id: id), machine: a.machine)
            case .spawn(let request):
                let machine = request.machine ?? "mini"
                var a = Agent(
                    id: "\(machine)/new\(s.nextID)", machine: machine, name: request.name ?? Self.name(for: request.task),
                    state: .starting, stateSince: .now, project: request.scratch ? nil : request.project)
                a.task = request.task
                s.nextID += 1
                s.agents[a.id] = a
                return .event(.changed(a), machine: machine)
            }
        }
        guard let update else { throw FixtureError.unknownAgent(command.agent ?? "") }
        broadcast(update)
    }

    private func broadcast(_ update: LinkUpdate) {
        for c in state.withLock({ Array($0.continuations.values) }) { c.yield(update) }
    }

    private static func change(_ s: inout State, _ id: String, _ edit: (inout Agent) -> Void) -> LinkUpdate? {
        guard var a = s.agents[id] else { return nil }
        edit(&a)
        a.stateSince = .now
        s.agents[id] = a
        return .event(.changed(a), machine: a.machine)
    }

    /// The task's first longer words, a stand-in for the name hesperd
    /// gives an agent before it names itself.
    static func name(for task: String) -> String {
        let words = task.lowercased().split { !$0.isLetter && !$0.isNumber }.filter { $0.count > 2 }.prefix(3)
        return words.isEmpty ? "agent" : words.map { String($0) }.joined(separator: "-")
    }
}

public enum FixtureError: Error, Equatable, CustomStringConvertible {
    case unknownAgent(String)
    public var description: String {
        switch self {
        case .unknownAgent(let id): "No agent \(id) on the sample Macs."
        }
    }
}
