import HesperCore
import HesperDesign
import HesperFleet
import HesperSurfaces
import SwiftUI

/// The agent view's middle (spec "2. Phone"): the transcript and live
/// output scroll above the terminal's own fixed input row.
struct TranscriptView: View {
    let agent: Agent
    let store: TranscriptStore
    let screen: ScreenWatch
    @Environment(FleetModel.self) private var model
    @State private var position = ScrollPosition(edge: .bottom)
    @State private var typing = false
    @ScaledMetric(relativeTo: .footnote) private var inputLineHeight: CGFloat = 20
    /// A page of older items is being prepended: the scroll keeps its
    /// distance from the bottom, so the rows on screen stay where they are.
    @State private var prepending = false
    @State private var fullTextItem: TranscriptItem?

    /// How close to an edge counts as reaching it.
    private static let edge: CGFloat = 24
    /// How long the bottom anchor holds after a prepend, so the layout of
    /// the new rows happens under it.
    private static let prependSettle: Duration = .milliseconds(150)

    var body: some View {
        let partition = screen.partition(pinInput: pinsInput)
        let transcript = store.log.lines
        VStack(spacing: 0) {
            ScrollView {
                // Keep the live rows in the same lazy layout as history so
                // estimated transcript heights cannot place them too early.
                LazyVStack(alignment: .leading, spacing: 0) {
                    Color.clear.frame(height: DS.Spacing.l)
                    if let lines = store.shellText {
                        ForEach(lines.indices, id: \.self) { i in
                            Text(lines[i].isEmpty ? " " : lines[i])
                                .font(TranscriptRow.font)
                                .foregroundStyle(Theme.dim)
                                .textSelection(.enabled)
                                .padding(.horizontal, DS.Spacing.l)
                        }
                    } else {
                        ForEach(transcript) { line in
                            TranscriptRow(line: line, open: store.open.contains(line.id), store: store) { fullTextItem = $0 }
                                .padding(.horizontal, DS.Spacing.l)
                                .padding(.bottom, line.id == transcript.last?.id ? 0 : DS.Spacing.m)
                        }
                    }
                    note.padding(.horizontal, DS.Spacing.l)
                    Color.clear.frame(height: DS.Spacing.l)
                    Rectangle().fill(Theme.line).frame(height: DS.Edge.line)
                    LiveScreenView(screen: screen, indices: partition.scrolling)
                        .padding(.horizontal, DS.Spacing.l)
                        .padding(.vertical, DS.Spacing.s)
                        .contentShape(Rectangle())
                        .onTapGesture { if screen.canType { typing.toggle() } }
                        .simultaneousGesture(
                            DragGesture(minimumDistance: Self.edge).onEnded { drag in
                                if drag.translation.height > Self.edge { typing = false }
                            })
                }
            }
            .scrollPosition($position)
            .defaultScrollAnchor(.bottom, for: .initialOffset)
            // At the bottom, new rows push the view along; scrolled up, they
            // leave it where it is (and the pill counts them).
            .defaultScrollAnchor(store.atBottom || prepending ? .bottom : .top, for: .sizeChanges)
            .scrollDismissesKeyboard(.interactively)
            .onScrollGeometryChange(for: Bool.self) { g in
                g.visibleRect.maxY >= g.contentSize.height - Self.edge
            } action: { _, bottom in
                store.atBottom = bottom
            }
            .onScrollGeometryChange(for: Bool.self) { g in
                g.visibleRect.minY <= Self.edge
            } action: { _, top in
                store.atTop = top
                if top { Task { await older() } }
            }
            .overlay(alignment: .bottomTrailing) { newPill }

            if let input = partition.input {
                Rectangle().fill(Theme.line).frame(height: DS.Edge.line)
                LiveScreenView(screen: screen, indices: [input])
                    .frame(height: inputLineHeight)
                    .padding(.horizontal, DS.Spacing.l)
                    .padding(.vertical, inputLineHeight / 2 + 2 * DS.Spacing.s)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .background(Theme.background)
                    .contentShape(Rectangle())
                    .onTapGesture { typing.toggle() }
            }
        }
        .background(Theme.background)
        .background { KeyCatcher(active: $typing, text: { screen.send(text: $0) }, key: { screen.send($0) }) }
        .onChange(of: agent.state) { _, state in screen.stateChanged(state) }
        .onChange(of: agent.size) { _, size in screen.sizeChanged(size) }
        .onChange(of: agent.sessionId) { _, id in
            fullTextItem = nil
            store.sessionChanged(id)
        }
        .onChange(of: screen.canType) { _, can in if !can { typing = false } }
        .onChange(of: pinsInput) { _, pins in if !pins { typing = false } }
        .sheet(item: $fullTextItem) { item in
            FullTranscriptTextSheet(agent: agent, item: item)
        }
    }

    private var pinsInput: Bool {
        switch agent.state {
        case .approval, .question, .error, .exited: false
        default: true
        }
    }

    @ViewBuilder private var note: some View {
        switch store.phase {
        case .unsupported(let machine):
            HistoryNote("History needs a newer Hesper on \(model.fleet.macName(machine))")
        case .unavailable(let reason):
            // The store's reason: an oversized item, a refusal, the Mac's message.
            HistoryNote(reason.isEmpty ? "History is not available" : reason)
        case .reconnecting:
            HistoryNote("Reconnecting…")
        case .loading where store.log.items.isEmpty && store.shellText == nil:
            HistoryNote("Loading history…")
        case .loading, .live:
            EmptyView()
        }
    }

    /// Items arrived while scrolled up; a tap goes to the newest.
    @ViewBuilder private var newPill: some View {
        if !store.atBottom, store.unseen > 0 {
            Button("↓ \(store.unseen) new") { position.scrollTo(edge: .bottom) }
                .secondaryAction()
                .padding(DS.Spacing.l)
        }
    }

    /// The top was reached: prepend the page before the first item.
    private func older() async {
        guard store.log.hasOlder, store.shellText == nil, !prepending else { return }
        prepending = true
        await store.loadOlder(agent, model: model)
        try? await Task.sleep(for: Self.prependSettle)
        prepending = false
    }
}

/// One dim centered line in place of history it cannot show.
private struct HistoryNote: View {
    let text: String

    init(_ text: String) { self.text = text }

    var body: some View {
        Text(text)
            .font(TranscriptRow.font)
            .foregroundStyle(Theme.dim)
            .multilineTextAlignment(.center)
            .frame(maxWidth: .infinity)
    }
}

#Preview("Long transcript") {
    NavigationStack { AgentScreen(id: "mini/m4") }.fixtureModel()
}

#Preview("Idle: shrunk live section") {
    NavigationStack { AgentScreen(id: "mini/m5") }.fixtureModel()
}

#Preview("Working: full live section") {
    NavigationStack { AgentScreen(id: "mini/m2") }.fixtureModel()
}

#Preview("Unsupported") {
    HistoryNote("History needs a newer Hesper on Mac mini")
        .padding()
        .background(Theme.background)
}
