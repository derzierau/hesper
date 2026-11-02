import HesperCore
import HesperDesign
import HesperFleet
import SwiftUI

/// Start an agent: the task first, then one destination line with a good
/// default (concept, flow 3). Profile and worktree options come later.
struct NewAgentSheet: View {
    @Environment(FleetModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    @State private var task = ""
    @State private var destination: Destination?
    @FocusState private var focused: Bool

    struct Destination: Hashable {
        var project: String
        var machine: String
    }

    var body: some View {
        NavigationStack {
            Form {
                Section {
                    TextField("What should it do?", text: $task, axis: .vertical)
                        .lineLimit(3...8)
                        .focused($focused)
                }
                Section("Where") {
                    Picker("Project", selection: $destination) {
                        ForEach(destinations, id: \.self) { d in
                            Text("\(d.project) on \(model.fleet.macName(d.machine))").tag(Optional(d))
                        }
                    }
                    .pickerStyle(.navigationLink)
                }
            }
            .navigationTitle("New agent")
            .toolbarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel", systemImage: "xmark", role: .cancel) { dismiss() }
                }
                ToolbarItem(placement: .confirmationAction) {
                    Button("Start", role: .confirm) { start() }
                        .disabled(task.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty || destination == nil)
                }
            }
            .onAppear {
                destination = destination ?? destinations.first
                focused = true
            }
        }
    }

    /// Projects that have agents on a reachable Mac; the busiest Mac first.
    /// A stand-in for `projects.recent` across Macs.
    private var destinations: [Destination] {
        var seen: [Destination: Int] = [:]
        for a in model.fleet.agents.values where model.fleet.isReachable(a) {
            if let p = a.project { seen[Destination(project: p, machine: a.machine), default: 0] += 1 }
        }
        return seen.sorted { ($1.value, $0.key.project) < ($0.value, $1.key.project) }.map(\.key)
    }

    private func start() {
        guard let destination else { return }
        let request = SpawnRequest(machine: destination.machine, project: destination.project, task: task, worktree: .auto)
        Task { await model.perform(.spawn(request)) }
        dismiss()
    }
}

#Preview {
    Text("Agents").sheet(isPresented: .constant(true)) { NewAgentSheet() }.fixtureModel()
}
