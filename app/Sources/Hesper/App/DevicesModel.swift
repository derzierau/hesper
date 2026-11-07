import AppKit
import HesperCore
import Observation

/// Settings' devices (Machines, other sign-ins, iPhones) and the Remove…
/// flow: the alert, the relay-too-old second alert, then the result line
/// for 5 s. The rules live in HesperCore (Devices.swift); this only asks
/// hesperd and shows the answer.
@MainActor
@Observable
final class DevicesModel {
    enum State: Equatable {
        case loading
        case loaded(DeviceGroups)
        /// An older hesperd without devices.list: the hesperctl text.
        case unsupported
        case failed(String)
    }

    struct Line: Equatable {
        var text: String
        var isError: Bool
    }

    private let client: DaemonClient
    private(set) var state = State.loading
    /// The last removal's result, cleared after `lineSeconds`.
    private(set) var line: Line?
    private(set) var busy: Set<String> = []
    private var lineTask: Task<Void, Never>?
    static let lineSeconds: UInt64 = 5

    init(client: DaemonClient) { self.client = client }

    var groups: DeviceGroups? { if case .loaded(let g) = state { return g } else { return nil } }

    func refresh() {
        Task { @MainActor in
            do {
                state = .loaded(DeviceGroups(try await client.devices()))
            } catch {
                let f = DevicesFailure(error)
                switch f {
                case .unsupportedDaemon: state = .unsupported
                case .failed(let why): state = .failed(why)
                default: state = .failed(f.text)
                }
            }
        }
    }

    /// Remove… : the pinned alert, then the call.
    func confirmRemove(_ d: Device) {
        guard d.removable, !busy.contains(d.id) else { return }
        let a = NSAlert()
        a.messageText = "Remove “\(d.name)”?"
        a.informativeText = "It can no longer reach this Mac, this Mac forgets it, and it is signed out of your relay. Pair it again to bring it back."
        a.alertStyle = .warning
        a.addButton(withTitle: "Remove")
        a.addButton(withTitle: "Cancel")
        a.buttons.first?.hasDestructiveAction = true
        guard a.runModal() == .alertFirstButtonReturn else { return }
        remove(d, localOnly: false)
    }

    private func confirmLocalOnly(_ d: Device) {
        let a = NSAlert()
        a.messageText = "Remove here only?"
        a.informativeText = "Your relay is too old to remove devices. Remove it from this Mac only?"
        a.alertStyle = .warning
        a.addButton(withTitle: "Remove here")
        a.addButton(withTitle: "Cancel")
        a.buttons.first?.hasDestructiveAction = true
        guard a.runModal() == .alertFirstButtonReturn else { return }
        remove(d, localOnly: true)
    }

    private func remove(_ d: Device, localOnly: Bool) {
        busy.insert(d.id)
        Task { @MainActor in
            var askLocalOnly = false
            do {
                let o = try await client.removeDevice(d.id, localOnly: localOnly)
                show(o.summary, isError: !o.summary.hasPrefix("Removed"))
            } catch {
                let f = DevicesFailure(error)
                if f == .relayTooOld && !localOnly { askLocalOnly = true } else { show(f.text, isError: true) }
            }
            busy.remove(d.id)
            refresh()
            // Nothing changed on a too-old relay: offer this Mac's steps.
            if askLocalOnly { confirmLocalOnly(d) }
        }
    }

    private func show(_ text: String, isError: Bool) {
        line = Line(text: text, isError: isError)
        lineTask?.cancel()
        lineTask = Task { @MainActor [weak self] in
            try? await Task.sleep(nanoseconds: Self.lineSeconds * 1_000_000_000)
            guard !Task.isCancelled else { return }
            self?.line = nil
        }
    }
}
