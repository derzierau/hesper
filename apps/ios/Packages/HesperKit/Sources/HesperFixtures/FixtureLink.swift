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
        var drafts: [String: [String: Draft]] = [:]
        var continuations: [UUID: AsyncStream<LinkUpdate>.Continuation] = [:]
        var sent: [LinkCommand] = []
        var nextID = 1
    }

    private let state = Mutex(State())
    private let initial: [LinkUpdate]
    private let terminalTick: Duration

    /// `terminalTick`: how often a watched terminal prints a new line.
    public init(updates: [LinkUpdate] = Samples.initialUpdates(), terminalTick: Duration = .seconds(2)) {
        initial = updates
        self.terminalTick = terminalTick
        state.withLock { s in
            for case .event(.list(_, let agents, _), _) in updates {
                for a in agents { s.agents[a.id] = a }
            }
            for case .drafts(let list, let machine) in updates {
                for d in list { s.drafts[machine, default: [:]][d.id] = d }
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
            case .answer(let id, _, _), .choose(let id, _), .reply(let id, _), .resume(let id):
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
            case .saveDraft(var draft, let machine):
                draft.updated = .now
                s.drafts[machine, default: [:]][draft.id] = draft
                return .draftChanged(draft, machine: machine)
            case .removeDraft(let id, let machine):
                guard s.drafts[machine]?.removeValue(forKey: id) != nil else { return nil }
                return .draftRemoved(id: id, machine: machine)
            }
        }
        guard let update else {
            if case .removeDraft(let id, let machine) = command { throw FixtureError.unknownDraft(id, machine: machine) }
            throw FixtureError.unknownAgent(command.agent ?? "")
        }
        broadcast(update)
    }

    /// Any sample agent's terminal: the script, then a new line every
    /// tick until closed. A mirror gets the Mac's size; an owner attach
    /// sizes the terminal as hesperd does: its grid and every RESIZE come
    /// back as `.size`, and the agent names the phone as its size owner
    /// until it lets go.
    public func attach(agent: String, cols: Int, rows: Int, owner: Bool) async throws -> any AttachStream {
        guard state.withLock({ $0.agents[agent] != nil }) else { throw FixtureError.unknownAgent(agent) }
        guard owner, sizing(on: String(agent.split(separator: "/").first ?? ""))?.canOwn == true else {
            return ScriptedTerminal(tick: terminalTick)
        }
        setSizeOwner(agent, Samples.device)
        return ScriptedTerminal(tick: terminalTick, grid: GridSize(cols: cols, rows: rows)) { [weak self] in
            self?.setSizeOwner(agent, nil)
        }
    }

    public func sizing(on machine: String) -> HostSizing? {
        guard Samples.macs.contains(where: { $0.short == machine }) else { return nil }
        return HostSizing(
            publishesOwner: Samples.capabilities[machine]?["sizeOwner"] == true, rights: Samples.rights, device: Samples.device)
    }

    private func setSizeOwner(_ id: String, _ owner: String?) {
        let update: LinkUpdate? = state.withLock { s in
            guard var a = s.agents[id] else { return nil }
            a.sizeOwner = owner
            a.sizeOwnerName = owner == nil ? nil : Samples.deviceName
            s.agents[id] = a
            return .event(.changed(a), machine: a.machine)
        }
        if let update { broadcast(update) }
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

/// A scripted terminal stream (`Samples.terminalScript`). With `grid` it
/// is the size owner's: that grid first, and every RESIZE echoed.
final class ScriptedTerminal: AttachStream {
    let frames: AsyncStream<AttachFrame>
    private let continuation: AsyncStream<AttachFrame>.Continuation
    private let player: Task<Void, Never>
    private let owner: Bool

    init(tick: Duration, grid: GridSize? = nil, onEnd: (@Sendable () -> Void)? = nil) {
        let (stream, continuation) = AsyncStream.makeStream(of: AttachFrame.self)
        frames = stream
        self.continuation = continuation
        owner = grid != nil
        let size = grid ?? GridSize(cols: Samples.terminalSize.cols, rows: Samples.terminalSize.rows)
        player = Task {
            continuation.yield(.size(cols: size.cols, rows: size.rows))
            continuation.yield(.data(Samples.terminalScript))
            var n = 1
            while (try? await Task.sleep(for: tick)) != nil {
                continuation.yield(.data(Samples.terminalLine(n)))
                n += 1
            }
            continuation.finish()
        }
        continuation.onTermination = { [player] _ in
            player.cancel()
            onEnd?()
        }
    }

    func resize(cols: Int, rows: Int) {
        guard owner, cols > 0, rows > 0 else { return }
        continuation.yield(.size(cols: cols, rows: rows))
    }

    func close() { player.cancel() }
}

public enum FixtureError: Error, Equatable, CustomStringConvertible {
    case unknownAgent(String)
    case unknownDraft(String, machine: String)
    public var description: String {
        switch self {
        case .unknownAgent(let id): "No agent \(id) on the sample Macs."
        case .unknownDraft(let id, let machine): "No draft \(id) on \(machine)."
        }
    }
}
