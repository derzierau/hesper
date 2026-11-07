import HesperCore
import HesperDesign
import HesperFleet
import SwiftUI

/// "No, and tell Claude what to do instead": the owner writes what the
/// agent should do, and the phone denies with it.
///
/// One `agents.answer` with decision `deny` and the text as `message`
/// (relay/docs/protocol.md: a remote `agents.answer` takes the local
/// params, `AnswerParams{id, decision, message}`; hesperd presses deny
/// and then types the message into the agent, as `hesperctl deny -m`
/// does). One request rather than a deny followed by `agents.input`: the
/// two cannot be split by a lost second request, and the agent is pending
/// for exactly one answer. "Just say no" sends the deny without a message.
struct DenySheet: View {
    let agent: Agent
    @Environment(FleetModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    @State private var text = ""
    @State private var sending = false
    @FocusState private var focused: Bool

    var body: some View {
        let message = text.trimmingCharacters(in: .whitespacesAndNewlines)
        VStack(alignment: .leading, spacing: DS.Spacing.l) {
            Text("Tell Claude what to do instead").font(Typeface.heading()).foregroundStyle(Theme.text)
            Text(AgentCopy.headline(agent)).font(.subheadline).foregroundStyle(Theme.muted).lineLimit(2)
            TextField("What should Claude do instead?", text: $text, axis: .vertical)
                .lineLimit(3...6)
                .textFieldStyle(.plain)
                .focused($focused)
                .padding(DS.Spacing.l)
                .background(Theme.surface)
                .overlay {
                    RoundedRectangle(cornerRadius: DS.Radius.console).strokeBorder(Theme.line, lineWidth: DS.Edge.line)
                }
            HStack(spacing: DS.Spacing.grid) {
                Button {
                    send(nil)
                } label: {
                    Text("Just say no").frame(maxWidth: .infinity)
                }
                .secondaryAction()
                .disabled(sending)
                Button {
                    send(message)
                } label: {
                    Text("Send").frame(maxWidth: .infinity)
                }
                .primaryAction()
                .disabled(message.isEmpty || sending)
            }
            Spacer(minLength: 0)
        }
        .padding(DS.Spacing.xl)
        .presentationDetents([.medium, .large])
        .presentationBackground(Theme.background)
        .onAppear { focused = true }
    }

    /// Closes only once the answer went out; on a failure the sheet and
    /// its text stay, and the "Not sent" alert says why.
    private func send(_ message: String?) {
        guard DecisionOptions.isStillCurrent(agent, in: model.fleet) else {
            model.failure = DecisionOptions.promptChanged
            dismiss()
            return
        }
        let command = LinkCommand.answer(agent: agent.id, .deny, message: message)
        sending = true
        Task {
            let ok = await model.perform(command)
            sending = false
            if ok { dismiss() }
        }
    }
}
