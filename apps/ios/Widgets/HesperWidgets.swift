import ActivityKit
import HesperCore
import HesperDesign
import HesperFleet
import HesperSurfaces
import SwiftUI
import WidgetKit

@main
struct HesperWidgets: WidgetBundle {
    var body: some Widget {
        FleetWidget()
        FleetLiveActivity()
    }
}

/// Lock-screen and home-screen counts, from what the app last wrote to the
/// app group. Buttons in widgets never approve; a tap opens the app.
struct FleetWidget: Widget {
    var body: some WidgetConfiguration {
        StaticConfiguration(kind: "fleet", provider: CountsProvider()) { entry in
            FleetWidgetView(entry: entry)
                .containerBackground(for: .widget) { Theme.background }
        }
        .configurationDisplayName("Agents")
        .description("What needs you and what is working, on every Mac.")
        .supportedFamilies([.accessoryRectangular, .accessoryCircular, .systemSmall])
    }
}

struct CountsEntry: TimelineEntry {
    var date: Date
    var counts: FleetCounts
    /// When the app wrote them; nil before it ever ran.
    var asOf: Date?
}

struct CountsProvider: TimelineProvider {
    func placeholder(in context: Context) -> CountsEntry {
        CountsEntry(date: .now, counts: FleetCounts(working: 5, needsYou: 2), asOf: .now)
    }

    func getSnapshot(in context: Context, completion: @escaping (CountsEntry) -> Void) {
        completion(context.isPreview ? placeholder(in: context) : current())
    }

    func getTimeline(in context: Context, completion: @escaping (Timeline<CountsEntry>) -> Void) {
        // The app reloads the timeline when the counts change; widget pushes
        // from the relay come later (concept, "Core work" 3).
        completion(Timeline(entries: [current()], policy: .never))
    }

    private func current() -> CountsEntry {
        let stored = SharedCounts().read()
        return CountsEntry(date: .now, counts: stored?.counts ?? FleetCounts(), asOf: stored?.at)
    }
}

struct FleetWidgetView: View {
    let entry: CountsEntry
    @Environment(\.widgetFamily) private var family

    var body: some View {
        switch family {
        case .accessoryCircular:
            VStack(spacing: 0) {
                Text("\(entry.counts.needsYou)").font(.title2.weight(.semibold))
                Text("need you").font(.caption2)
            }
        case .accessoryRectangular:
            VStack(alignment: .leading) {
                Text("Hesper").font(.caption.weight(.semibold))
                Text(entry.counts.summary)
            }
        default:
            VStack(alignment: .leading, spacing: DS.Spacing.s) {
                Text("Hesper").font(.caption.weight(.semibold)).foregroundStyle(Theme.dim)
                Spacer()
                Label {
                    Text("\(entry.counts.needsYou) need you")
                } icon: {
                    StateMark(StateMarkKind.needsYou)
                }
                Label {
                    Text("\(entry.counts.working) working")
                } icon: {
                    StateMark(StateMarkKind.working)
                }
                if let asOf = entry.asOf {
                    Text("as of \(asOf, style: .time)").font(.caption2).foregroundStyle(Theme.dim)
                }
            }
            .foregroundStyle(Theme.text)
            .frame(maxWidth: .infinity, alignment: .leading)
        }
    }
}

/// The one fleet Live Activity: progress first, updated silently; the
/// alert for a prompt is the notification's job.
struct FleetLiveActivity: Widget {
    var body: some WidgetConfiguration {
        ActivityConfiguration(for: FleetActivityAttributes.self) { context in
            VStack(alignment: .leading, spacing: DS.Spacing.s) {
                Text(context.state.counts.summary).font(.headline)
                if let name = context.state.oldestWaiting {
                    Text("Waiting: \(name)").font(.subheadline).foregroundStyle(.secondary)
                }
            }
            .padding()
            .activityBackgroundTint(Theme.background)
        } dynamicIsland: { context in
            DynamicIsland {
                DynamicIslandExpandedRegion(.leading) {
                    Text("\(context.state.counts.needsYou) need you").font(.headline)
                }
                DynamicIslandExpandedRegion(.trailing) {
                    Text("\(context.state.counts.working) working").foregroundStyle(Theme.accent)
                }
                DynamicIslandExpandedRegion(.bottom) {
                    if let name = context.state.oldestWaiting { Text(name).font(.caption) }
                }
            } compactLeading: {
                StateMark(context.state.counts.needsYou > 0 ? StateMarkKind.needsYou : StateMarkKind.working)
            } compactTrailing: {
                Text("\(context.state.counts.needsYou > 0 ? context.state.counts.needsYou : context.state.counts.working)")
            } minimal: {
                StateMark(context.state.counts.needsYou > 0 ? StateMarkKind.needsYou : StateMarkKind.working)
            }
        }
    }
}
