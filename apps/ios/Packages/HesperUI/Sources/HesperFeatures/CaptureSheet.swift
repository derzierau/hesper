import HesperCore
import HesperDesign
import HesperFleet
import SwiftUI

/// Drop a task or an idea into Hesper (wireframes round 5): say it or
/// type it, then start an agent now or keep it for later as a parked draft
/// on a Mac. A Neon panel with the console's accent on top
/// (docs/iphone/neon.md, "Capture sheet").
struct CaptureSheet: View {
    /// How the sheet opens.
    enum Opening: Hashable {
        /// A tap on Speak: `dictation` is already listening.
        case listening
        /// Released after holding Speak: what was said.
        case said(String)
        /// Type, VoiceOver's "Type a task", or no speech.
        case typing
    }

    /// Speak's dictation, shared with the console so listening carries on
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
                        Text(why).font(.footnote).foregroundStyle(Theme.muted)
                    }
                }
                destinationRow
                Spacer(minLength: 0)
                HStack(spacing: DS.Spacing.m) {
                    Button {
                        Task { await keep() }
                    } label: {
                        Text("Keep for later").frame(maxWidth: .infinity, minHeight: DS.Size.sheetAction)
                    }
                    .secondaryAction()
                    Button {
                        Task { await start() }
                    } label: {
                        Text("Start now").frame(maxWidth: .infinity, minHeight: DS.Size.sheetAction)
                    }
                    .primaryAction()
                }
                .font(.headline)
                .disabled(!canSend)
            }
            .padding(DS.Spacing.l)
            .background(Theme.surface)
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
        .neonPanel(edge: Theme.accent)
        .presentationBackground(Theme.surface)
        .presentationDetents([.medium, .large])
        // The Macs may still be arriving when the sheet opens (at launch).
        .onChange(of: model.fleet.destinations, initial: true) { _, all in
            destination = destination ?? all.first
        }
        .task {
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
                Circle().fill(Theme.signal).frame(width: DS.Size.captionDot, height: DS.Size.captionDot)
                Text("Listening…").font(Typeface.heading())
                Spacer()
                Image(systemName: "waveform")
                    .symbolEffect(.variableColor.iterative)
                    .font(.title3)
                    .foregroundStyle(Theme.accent)
                if case .listening(let since) = dictation.phase {
                    Text(since, style: .timer)
                        .font(Typeface.mono(12, relativeTo: .caption))
                        .foregroundStyle(Theme.muted)
                }
            }
            let said = text.isEmpty ? dictation.settled : text + " " + dictation.settled
            Text("\(said)\(Text(dictation.guess).foregroundStyle(Theme.muted))")
                .font(.body)
                .frame(maxWidth: .infinity, minHeight: 60, alignment: .topLeading)
            Text("Tap Done when you have said it, or Type to use the keyboard.")
                .font(.caption2)
                .foregroundStyle(Theme.muted)
        }
    }

    /// Where the task goes, as an option row that opens the picker.
    private var destinationRow: some View {
        Menu {
            Picker("Where", selection: $destination) {
                ForEach(model.fleet.destinations, id: \.self) { d in
                    Text(name(d)).tag(Optional(d))
                }
            }
        } label: {
            HStack(spacing: DS.Spacing.m) {
                Text("Where").font(.subheadline).foregroundStyle(Theme.muted)
                Text(destination.map(name) ?? "No Mac")
                    .font(.subheadline.weight(.medium))
                    .foregroundStyle(Theme.text)
                    .lineLimit(1)
                Spacer(minLength: DS.Spacing.s)
                Image(systemName: "chevron.down").foregroundStyle(Theme.accent)
            }
            .padding(.horizontal, DS.Spacing.l)
            .frame(minHeight: DS.Size.control)
            .background(Theme.surface2, in: .rect(cornerRadius: DS.Radius.console))
            .overlay {
                RoundedRectangle(cornerRadius: DS.Radius.console).strokeBorder(Theme.line, lineWidth: DS.Edge.line)
            }
            .contentShape(.rect)
        }
        .disabled(model.fleet.destinations.isEmpty)
        .accessibilityLabel("Where")
        .accessibilityValue(destination.map(name) ?? "No Mac")
    }

    private func name(_ d: Destination) -> String {
        "\(d.project) on \(model.fleet.macName(d.machine))"
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
