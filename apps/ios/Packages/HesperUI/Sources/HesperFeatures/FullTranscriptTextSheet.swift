import HesperCore
import HesperDesign
import HesperFleet
import SwiftUI

/// Reads one user or assistant message in bounded parts on demand.
struct FullTranscriptTextSheet: View {
    let agent: Agent
    let item: TranscriptItem
    @Environment(FleetModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    @State private var pager = TranscriptTextPager()

    var body: some View {
        NavigationStack {
            VStack(alignment: .leading, spacing: DS.Spacing.m) {
                if let text = pager.text {
                    if pager.canPrevious {
                        Text("Continued from the previous part")
                            .font(.caption)
                            .foregroundStyle(Theme.dim)
                    }
                    ScrollView {
                        Text(text)
                            .font(TranscriptRow.font)
                            .foregroundStyle(Theme.text)
                            .textSelection(.enabled)
                            .frame(maxWidth: .infinity, alignment: .leading)
                    }
                } else if pager.loading {
                    ProgressView("Loading full text…")
                        .frame(maxWidth: .infinity, maxHeight: .infinity)
                }
                if let failure = pager.failure {
                    Text(failure).foregroundStyle(Theme.error)
                }
                HStack(spacing: DS.Spacing.m) {
                    Button("Previous part") { Task { await previous() } }
                        .disabled(!pager.canPrevious || pager.loading)
                    Spacer()
                    Button(pager.text == nil ? "Retry" : "Next part") { Task { await next() } }
                        .disabled(!pager.canNext || pager.loading)
                }
            }
            .padding(DS.Spacing.l)
            .background(Theme.background)
            .navigationTitle(item.kind == .user ? "Your full message" : "Full reply")
            .toolbar { ToolbarItem(placement: .topBarTrailing) { Button("Done") { dismiss() } } }
        }
        .task { if pager.text == nil { await next() } }
    }

    private func next() async {
        await pager.loadNext { offset, limit in
            try await model.transcriptText(agent, seq: item.seq, offset: offset, limit: limit)
        }
    }

    private func previous() async {
        await pager.loadPrevious { offset, limit in
            try await model.transcriptText(agent, seq: item.seq, offset: offset, limit: limit)
        }
    }
}
