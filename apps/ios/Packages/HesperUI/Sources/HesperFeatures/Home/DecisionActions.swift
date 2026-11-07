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
    /// Handles the decisions of the cards inside: a risky Yes and Stop ask
    /// first, No… opens the deny sheet, the rest sends at once.
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
    }

    private var title: String {
        guard let confirm, confirm.action == .stop else { return DecisionOptions.confirmationTitle }
        return "Stop \(confirm.agent.name)?"
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
        Task { await model.perform(command) }
    }
}

/// The answers to a prompt as native buttons: Allow · Always · Deny for an
/// approval, the numbered choices for a question. The agent view's dock
/// uses it until it gets the option rows.
struct DecisionButtons: View {
    let agent: Agent
    @Environment(FleetModel.self) private var model

    var body: some View {
        let pending = model.pending.contains(agent.id)
        HStack(spacing: DS.Spacing.s) {
            switch agent.state {
            case .approval where RiskRule.isRisky(agent):
                Text("Changes the database or remote state. Open it to answer.")
                    .font(.footnote)
                    .foregroundStyle(Theme.question)
            case .approval:
                Button("Allow") { send(.answer(agent: agent.id, .allow)) }.primaryAction()
                Button("Always") { send(.answer(agent: agent.id, .always)) }.secondaryAction()
                Button("Deny") { send(.answer(agent: agent.id, .deny)) }.secondaryAction()
            case .question:
                let choices = AgentCopy.choices(agent)
                if choices.isEmpty {
                    Text("Open it to answer.").font(.footnote).foregroundStyle(Theme.dim)
                } else {
                    ForEach(choices.prefix(4), id: \.number) { choice in
                        Button("\(choice.number) \(choice.title)") { send(.choose(agent: agent.id, option: choice.number)) }
                            .secondaryAction()
                    }
                }
            default:
                EmptyView()
            }
        }
        .disabled(pending || !model.fleet.isReachable(agent))
        .buttonBorderShape(.capsule)
        .controlSize(.small)
    }

    private func send(_ command: LinkCommand) {
        Task { await model.perform(command) }
    }
}
