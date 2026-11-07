import Foundation
import HesperCore
import Observation

/// The app's one observable model: the fleet, fed by a link, and the
/// commands in flight. Views read it from the environment.
@MainActor
@Observable
public final class FleetModel {
    public private(set) var fleet: Fleet
    /// Agents with a command sent and not yet confirmed by an event:
    /// their rows read "Sent, waiting for mini".
    public private(set) var pending: Set<String> = []
    /// The last command that failed, for one alert.
    public var failure: String?

    private let link: any LinkClient
    private var task: Task<Void, Never>?

    public init(link: any LinkClient, fleet: Fleet = Fleet()) {
        self.link = link
        self.fleet = fleet
    }

    /// Follows the link until `stop()`. Calling it again is a no-op.
    public func start() {
        guard task == nil else { return }
        let updates = link.updates()
        task = Task { [weak self] in
            for await update in updates {
                self?.receive(update)
            }
        }
    }

    public func stop() {
        task?.cancel()
        task = nil
    }

    func receive(_ update: LinkUpdate) {
        switch update {
        case .event(let event, let machine):
            fleet.apply(event, from: machine)
            switch event {
            case .changed(let a): pending.remove(a.id)
            case .removed(let id): pending.remove(id)
            case .list(_, let agents, _): pending.subtract(agents.map(\.id))
            }
        case .reachability(let r, let machine):
            fleet.setReachability(r, for: machine)
        case .macs(let machines):
            for m in machines where fleet.macs[m.short] == nil {
                fleet.setReachability(.connecting, for: m.short)
            }
        case .drafts(let list, let machine):
            fleet.setDrafts(list, on: machine)
        case .draftChanged(let draft, let machine):
            fleet.saveDraft(draft, on: machine)
        case .draftRemoved(let id, let machine):
            fleet.removeDraft(id: id, on: machine)
        }
    }

    /// Sends a command; the agent stays pending until its next event.
    /// False when it failed (`failure` says why).
    @discardableResult
    public func perform(_ command: LinkCommand) async -> Bool {
        if let id = command.agent { pending.insert(id) }
        do {
            try await link.perform(command)
            return true
        } catch {
            if let id = command.agent { pending.remove(id) }
            failure = String(describing: error)
            return false
        }
    }

    /// Keeps a task for later: a parked draft on the destination's Mac.
    /// Returns it so the caller can offer Undo; nil when the Mac refused.
    public func keep(_ text: String, at destination: Destination) async -> KeptTask? {
        let draft = Draft(text: text, machine: destination.machine, project: destination.project, parked: true)
        let ok = await perform(.saveDraft(draft, machine: destination.machine))
        return ok ? KeptTask(machine: destination.machine, draft: draft) : nil
    }

    /// Starts a task: a new agent on the destination's Mac.
    public func start(_ text: String, at destination: Destination) async {
        await perform(.spawn(SpawnRequest(machine: destination.machine, project: destination.project, task: text, worktree: .auto)))
    }

    /// Starts a kept task where it was kept, then removes its draft. The
    /// draft stays when the start failed.
    public func start(_ kept: KeptTask) async {
        var request = SpawnRequest(machine: kept.machine, project: kept.draft.project ?? "", task: kept.draft.text, worktree: .auto)
        request.scratch = kept.draft.project == nil
        guard await perform(.spawn(request)) else { return }
        await perform(.removeDraft(id: kept.draft.id, machine: kept.machine))
    }

    public func discard(_ kept: KeptTask) async {
        await perform(.removeDraft(id: kept.draft.id, machine: kept.machine))
    }

    public func agent(_ id: String) -> Agent? { fleet.agents[id] }

    /// The grid asked for when the Mac has not reported the agent's size.
    public static let fallbackSize = GridSize(cols: 80, rows: 24)

    /// Watches an agent's terminal at the Mac's current grid size
    /// (`fallbackSize` when it has not said). Throws when the Mac refused
    /// it or is not linked.
    public func attach(_ agent: Agent) async throws -> any AttachStream {
        let size = agent.size ?? Self.fallbackSize
        return try await link.attach(agent: agent.id, cols: size.cols, rows: size.rows)
    }
}
