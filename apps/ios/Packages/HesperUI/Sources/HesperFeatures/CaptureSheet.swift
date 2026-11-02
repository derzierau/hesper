import HesperCore
import HesperDesign
import HesperFleet
import SwiftUI

/// Drop a task or an idea into Hesper (wireframes round 5): say it or
/// type it, then start an agent now or keep it for later as a parked draft
/// on a Mac.
struct CaptureSheet: View {
    /// How the sheet opens.
    enum Opening: Hashable {
        /// A tap on the mic: `dictation` is already listening.
        case listening
        /// Released after holding the mic: what was said.
        case said(String)
        /// The keyboard (VoiceOver's "Type a task", or no speech).
        case typing
    }

    /// The mic's dictation, shared with the tab bar so listening carries on
    /// from the tap into the sheet.
    let dictation: Dictation
    let opening: Opening
    /// Called with the kept task, so the root view can offer Undo.
    var kept: (KeptTask) -> Void

    @Environment(FleetModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    @State private var text = ""
    @State private var destination: Destination?
    @State private var sending = false
    @FocusState private var typing: Bool

    var body: some View {
        NavigationStack {
            VStack(alignment: .leading, spacing: DS.Spacing.l) {
                if dictation.isListening {
                    listening
                } else {
                    TextField("What should happen?", text: $text, axis: .vertical)
                        .font(.title3)
                        .lineLimit(3...8)
                        .focused($typing)
                    if case .unavailable(let why) = dictation.phase {
                        Text(why).font(.footnote).foregroundStyle(Theme.dim)
                    }
                }
                destinationRow
                Spacer(minLength: 0)
                HStack(spacing: DS.Spacing.m) {
                    Button {
                        Task { await keep() }
                    } label: {
                        Text("Keep for later").frame(maxWidth: .infinity)
                    }
                    .secondaryAction()
                    Button {
                        Task { await start() }
                    } label: {
                        Text("Start now").frame(maxWidth: .infinity)
                    }
                    .primaryAction()
                }
                .controlSize(.large)
                .disabled(!canSend)
            }
            .padding(DS.Spacing.l)
            .background(Theme.background)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Discard", systemImage: "xmark", role: .cancel) {
                        dictation.cancel()
                        dismiss()
                    }
                }
                ToolbarItemGroup(placement: .confirmationAction) {
                    if dictation.isListening {
                        Button("Type", systemImage: "keyboard") {
                            Task {
                                await stopListening()
                                typing = true
                            }
                        }
                        Button("Done", systemImage: "stop.fill") { Task { await stopListening() } }
                            .tint(Theme.accent)
                    } else {
                        Button("Record", systemImage: "mic.fill") {
                            typing = false
                            dictation.start()
                        }
                    }
                }
            }
        }
        .presentationDetents([.medium, .large])
        .task {
            destination = destination ?? model.fleet.destinations.first
            switch opening {
            case .listening: break
            case .said(let said): text = said
            case .typing: typing = true
            }
        }
        .onChange(of: dictation.phase) { _, phase in
            if case .unavailable = phase { typing = true }
        }
        .onDisappear { dictation.cancel() }
    }

    /// The live transcript: settled text solid, the guess dimmed.
    private var listening: some View {
        VStack(alignment: .leading, spacing: DS.Spacing.s) {
            HStack(spacing: DS.Spacing.s) {
                Image(systemName: "waveform").symbolEffect(.variableColor.iterative).foregroundStyle(Theme.accent)
                Text("Listening").font(.headline)
                Spacer()
                if case .listening(let since) = dictation.phase {
                    Text(since, style: .timer).font(.footnote.monospacedDigit()).foregroundStyle(Theme.dim)
                }
            }
            let said = text.isEmpty ? dictation.settled : text + " " + dictation.settled
            Text("\(said)\(Text(dictation.guess).foregroundStyle(Theme.dim))")
                .font(.title3)
                .frame(maxWidth: .infinity, minHeight: 60, alignment: .topLeading)
        }
    }

    private var destinationRow: some View {
        LabeledContent("Where") {
            Picker("Where", selection: $destination) {
                ForEach(model.fleet.destinations, id: \.self) { d in
                    Text("\(d.project) on \(model.fleet.macName(d.machine))").tag(Optional(d))
                }
            }
            .pickerStyle(.menu)
        }
        .foregroundStyle(Theme.dim)
    }

    private var task: String { text.trimmingCharacters(in: .whitespacesAndNewlines) }

    private var canSend: Bool {
        !sending && !dictation.isListening && !task.isEmpty && destination != nil
    }

    private func stopListening() async {
        let said = await dictation.stop()
        guard !said.isEmpty else { return }
        text = task.isEmpty ? said : task + " " + said
    }

    private func keep() async {
        guard let destination else { return }
        sending = true
        defer { sending = false }
        if let k = await model.keep(task, at: destination) {
            kept(k)
            dismiss()
        }
    }

    private func start() async {
        guard let destination else { return }
        sending = true
        defer { sending = false }
        await model.start(task, at: destination)
        dismiss()
    }
}

#Preview {
    Text("Inbox")
        .sheet(isPresented: .constant(true)) {
            CaptureSheet(dictation: Dictation(), opening: .said("Add a dark mode toggle to the settings screen")) { _ in }
        }
        .fixtureModel()
}
