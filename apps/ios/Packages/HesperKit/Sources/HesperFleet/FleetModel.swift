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
        }
    }

    /// Sends a command; the agent stays pending until its next event.
    public func perform(_ command: LinkCommand) async {
        if let id = command.agent { pending.insert(id) }
        do {
            try await link.perform(command)
        } catch {
            if let id = command.agent { pending.remove(id) }
            failure = String(describing: error)
        }
    }

    public func agent(_ id: String) -> Agent? { fleet.agents[id] }
}
