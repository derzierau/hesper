import HesperCore
import HesperDesign
import HesperFleet
import SwiftUI

/// What a row or a grid button does when tapped, handed down from Home.
struct Decide {
    let tap: (Agent, DecisionAction) -> Void

    func callAsFunction(_ agent: Agent, _ action: DecisionAction) { tap(agent, action) }
}

extension EnvironmentValues {
    @Entry var decide = Decide { _, _ in }
}

extension FleetModel {
    /// Rows and buttons are off while an answer is on its way or the Mac
    /// is unreachable.
    func canAnswer(_ agent: Agent) -> Bool {
        !pending.contains(agent.id) && fleet.isReachable(agent)
    }
}

extension View {
    /// Handles the decisions of the cards inside: a risky Yes, "don't ask
    /// again" and Stop ask first, No… opens the deny sheet, the rest sends
    /// at once. A dialog or sheet whose prompt changed closes unsent.
    func decisions() -> some View {
        modifier(DecisionHandling())
    }
}

struct DecisionHandling: ViewModifier {
    /// A card on an unreachable Mac, as `AgentRow` shows it.
    static let unreachableOpacity = 0.45

    struct Confirmation {
        var agent: Agent
        var action: DecisionAction
    }

    @Environment(FleetModel.self) private var model
    @State private var confirm: Confirmation?
    @State private var deny: Agent?

    func body(content: Content) -> some View {
        content
            .environment(\.decide, Decide { agent, action in tap(agent, action) })
            .confirmationDialog(
                title,
                isPresented: Binding {
                    confirm != nil
                } set: {
                    if !$0 { confirm = nil }
                },
                titleVisibility: .visible,
                presenting: confirm
            ) { c in
                if c.action == .stop {
                    Button("Stop", role: .destructive) { send(c.agent, c.action) }
                } else {
                    Button("Allow") { send(c.agent, c.action) }
                }
                Button("Cancel", role: .cancel) {}
            } message: { c in
                if c.action == .stop {
                    Text("It stops on \(model.fleet.macName(c.agent.machine)). You can resume it later.")
                }
            }
            .sheet(item: $deny) { DenySheet(agent: $0) }
            .onChange(of: isStale) { _, stale in
                // A dialog or sheet for a prompt that is gone never lingers.
                guard stale else { return }
                confirm = nil
                deny = nil
                model.failure = DecisionOptions.promptChanged
            }
    }

    private var title: String {
        guard let confirm else { return "" }
        if confirm.action == .stop { return "Stop \(confirm.agent.name)?" }
        return DecisionOptions.confirmationTitle(confirm.agent, confirm.action)
    }

    /// The open dialog or sheet answers a prompt the agent no longer
    /// shows. Stop is about the agent, not a prompt, so it stays; an agent
    /// with an answer on its way is expected to change, so a prompt that
    /// changes during the send is not caught here; `send` checks it once.
    private var isStale: Bool {
        if let confirm, confirm.action != .stop, !model.pending.contains(confirm.agent.id),
            !DecisionOptions.isStillCurrent(confirm.agent, in: model.fleet)
        {
            return true
        }
        if let deny, !model.pending.contains(deny.id), !DecisionOptions.isStillCurrent(deny, in: model.fleet) {
            return true
        }
        return false
    }

    private func tap(_ agent: Agent, _ action: DecisionAction) {
        if DecisionOptions.needsConfirmation(agent, action) || action == .stop {
            confirm = Confirmation(agent: agent, action: action)
            return
        }
        switch action {
        case .denyWithMessage: deny = agent
        // Open buttons are navigation links; nothing to send.
        case .open: break
        default: send(agent, action)
        }
    }

    private func send(_ agent: Agent, _ action: DecisionAction) {
        guard let command = DecisionOptions.command(agent, action) else { return }
        // The dialog kept a snapshot; answer only the prompt the owner saw.
        if action != .stop, !DecisionOptions.isStillCurrent(agent, in: model.fleet) {
            confirm = nil
            model.failure = DecisionOptions.promptChanged
            return
        }
        Task { await model.perform(command) }
    }
}
