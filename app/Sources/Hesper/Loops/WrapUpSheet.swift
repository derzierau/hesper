import AppKit
import HesperCore
import Observation
import SwiftUI

/// The evening wrap-up's state (⇧⌘E, W4): today's loops with one
/// suggestion each, the decisions taken so far (kept across store
/// changes) and the save. Nothing is sent before ⌘↩: until then U undoes.
@MainActor
@Observable
final class WrapUpSheetModel {
    @ObservationIgnored let hub: LoopHub
    @ObservationIgnored private weak var app: AppModel?

    private(set) var wrap = WrapUp(loops: [])
    private(set) var selected: String?
    private(set) var error: String?
    private(set) var saving = false
    /// After a save: how many decisions went through.
    private(set) var saved: Int?
    /// Decisions in the order taken (U undoes the last one).
    @ObservationIgnored private var history: [String] = []
    @ObservationIgnored private var seen = -1

    init(hub: LoopHub, app: AppModel) {
        self.hub = hub
        self.app = app
    }

    func open() {
        seen = -1
        error = nil
        saved = nil
        history = []
        wrap = WrapUp(loops: hub.store.loops.values)
        seen = hub.store.version
        selected = wrap.nextUndecided?.id ?? wrap.entries.first?.id
    }

    func closed() {}

    /// A newer store: the list again, the decisions taken so far kept.
    func sync() {
        guard hub.store.version != seen, !saving else { return }
        seen = hub.store.version
        let decided = wrap.decided
        var w = WrapUp(loops: hub.store.loops.values)
        for (id, d) in decided { w.decide(id, d) }
        wrap = w
        history.removeAll { w.decided[$0] == nil }
        if selected.map({ id in !w.entries.contains { $0.id == id } }) ?? true { selected = w.nextUndecided?.id ?? w.entries.first?.id }
    }

    var entry: WrapUpEntry? { selected.flatMap { id in wrap.entries.first { $0.id == id } } }

    func select(_ id: String) { selected = id }

    func perform(_ a: LoopsKeyAction) -> Bool {
        switch a {
        case .up, .down:
            let ids = wrap.entries.map(\.id)
            guard !ids.isEmpty else { return true }
            let i = ids.firstIndex { $0 == selected } ?? 0
            selected = ids[max(0, min(ids.count - 1, i + (a == .up ? -1 : 1)))]
        case .accept:
            guard let e = entry else { NSSound.beep(); return true }
            take(e.id, e.suggestion)
        case .done: decide(.done)
        case .park: decide(.park)
        case .drop: decide(.drop)
        case .nextStep:
            guard let e = entry, let step = e.loop.yourItems.first else {
                error = "This loop has no step of yours: A in the Loops sheet (⌘L) adds one."
                return true
            }
            take(e.id, .nextStep(step.text))
        case .undo:
            guard let last = history.popLast() else { NSSound.beep(); return true }
            wrap.undo(last)
            selected = last
        case .apply: save()
        default: return false
        }
        return true
    }

    private func decide(_ d: WrapUpDecision) {
        guard let e = entry else { NSSound.beep(); return }
        take(e.id, d)
    }

    private func take(_ id: String, _ d: WrapUpDecision) {
        error = nil
        saved = nil
        wrap.decide(id, d)
        history.removeAll { $0 == id }
        history.append(id)
        if let next = wrap.nextUndecided { selected = next.id }
    }

    /// ⌘↩: every decision that changes a loop, one save each; the ones that
    /// fail stay decided so ⌘↩ can try them again.
    private func save() {
        let patches = wrap.patches
        guard !patches.isEmpty else {
            saved = 0
            error = wrap.decidedCount == 0 ? "Nothing decided yet: ⏎ takes the suggestion." : nil
            return
        }
        saving = true
        error = nil
        Task {
            var failed: String?
            var count = 0
            for p in patches {
                if let e = await hub.run({ try await $0.loopSave(p) }) { failed = failed ?? e } else { count += 1 }
            }
            saving = false
            saved = count
            error = failed
            seen = -1
            sync()
        }
    }
}

/// W4: the tally and today's list on the left, one loop at a time on the
/// right with its four verdict cards (Done D, Next step N, Park P, Drop X;
/// the suggested one carries ⏎).
struct WrapUpSheetView: View {
    var model: WrapUpSheetModel
    var hub: LoopHub
    var onClose: () -> Void

    var body: some View {
        SheetChrome(title: "Wrap-up", symbol: "moon", count: model.wrap.entries.count, closeIdentifier: "wrapup.close",
                    toolbar: { toolbar }, footer: { footer }, onClose: onClose) {
            VStack(spacing: 0) {
                if let e = model.error { LoopsErrorLine(text: e) }
                content.frame(maxWidth: .infinity, maxHeight: .infinity)
            }
        }
        .onChange(of: hub.store.version) { model.sync() }
    }

    @ViewBuilder private var toolbar: some View {
        let w = model.wrap
        Text("\(w.decidedCount) of \(w.entries.count) decided").font(DS.font(.chrome)).foregroundStyle(Theme.fg2)
        ProgressBar(value: w.progress, tone: .done).frame(width: LoopsLook.listWidth / 2)
        if let n = model.saved { Text(n == 1 ? "1 change saved" : "\(n) changes saved").font(DS.font(.chrome)).foregroundStyle(Theme.dim) }
        Spacer()
    }

    @ViewBuilder private var footer: some View {
        KeyButton("Undo", kbd: CommandRegistry.cap(.wrapUpUndo, in: .wrapUp)) { _ = model.perform(.undo) }
        Spacer()
        let n = model.wrap.patches.count
        KeyButton(model.saving ? "Saving…" : n == 0 ? "Save" : "Save \(n) changes", kbd: CommandRegistry.cap(.wrapUpApply, in: .wrapUp),
                  variant: .primary(.done)) { _ = model.perform(.apply) }
            .disabled(model.saving)
            .accessibilityIdentifier("wrapup.save")
    }

    @ViewBuilder private var content: some View {
        if !hub.loaded {
            LoopsEmpty(symbol: "hourglass", title: "Loading loops…", detail: "Hesper asks hesperd for today's loops.")
        } else if !hub.available {
            LoopsEmpty(symbol: "moon", title: "This hesperd has no loops yet", detail: "Update hesperd on this Mac to wrap up the day's work here.")
        } else if model.wrap.entries.isEmpty {
            LoopsEmpty(symbol: "moon.stars", title: "Nothing to wrap up",
                       detail: "Loops that changed today or wait on you show here, one at a time, with a suggestion for each. "
                           + "The evening reminder comes at \(hub.settings.eveningAt).")
        } else {
            HStack(spacing: 0) {
                left.frame(width: LoopsLook.listWidth)
                Rectangle().fill(Theme.stroke).frame(width: 1)
                if let e = model.entry { WrapUpCard(entry: e, decided: model.wrap.decided[e.id], model: model) } else { Spacer() }
            }
        }
    }

    private var left: some View {
        let t = model.wrap.tallies
        return VStack(alignment: .leading, spacing: DS.Spacing.m) {
            HStack(spacing: DS.Spacing.l) {
                tally(t.startedToday, "started")
                tally(t.finishedToday, "finished")
                tally(t.onYou, "on you")
                tally(t.parked, "parked")
            }
            .padding(.horizontal, DS.Spacing.l)
            .padding(.top, DS.Spacing.l)
            ScrollView {
                LazyVStack(spacing: 0) {
                    ForEach(model.wrap.entries) { e in
                        let d = model.wrap.decided[e.id]
                        Row(title: e.loop.displayTitle, subtitle: d.map { "→ \($0.title)" } ?? e.reason, meta: LoopWords.age(e.loop),
                            mark: d == nil ? LoopWords.mark(e.loop) : .done, selected: e.id == model.selected)
                            .onTapGesture { model.select(e.id) }
                            .accessibilityIdentifier("wrapup.row.\(e.id)")
                    }
                }
                .padding(DS.Spacing.s)
            }
        }
    }

    private func tally(_ n: Int, _ label: String) -> some View {
        VStack(alignment: .leading, spacing: DS.Spacing.xxs) {
            Text("\(n)").font(DS.font(.sheetTitle, .medium)).foregroundStyle(Theme.fg)
            Text(label).font(DS.font(.meta)).foregroundStyle(Theme.dim)
        }
        .accessibilityElement(children: .combine)
    }
}

/// One loop of the wrap-up: what it is, why the suggestion, the cards.
struct WrapUpCard: View {
    var entry: WrapUpEntry
    var decided: WrapUpDecision?
    var model: WrapUpSheetModel

    var body: some View {
        let l = entry.loop
        VStack(alignment: .leading, spacing: DS.Spacing.l) {
            VStack(alignment: .leading, spacing: DS.Spacing.s) {
                HStack(spacing: DS.Spacing.s) {
                    Pill(LoopWords.nextLabel(l), mark: LoopWords.mark(l))
                    if let age = LoopWords.age(l) { Text(age).font(DS.font(.meta)).foregroundStyle(Theme.dim) }
                }
                Text(l.displayTitle).font(DS.font(.sheetTitle, .medium)).foregroundStyle(Theme.fg).lineLimit(2)
                if let g = l.goal { Text(g).font(DS.font(.body)).foregroundStyle(Theme.fg2).lineLimit(3) }
                Text("Suggested: \(entry.suggestion.title.lowercased()) · \(entry.reason)").font(DS.font(.chrome)).foregroundStyle(Theme.dim)
            }
            LazyVGrid(columns: [GridItem(.flexible(), spacing: DS.Spacing.m), GridItem(.flexible(), spacing: DS.Spacing.m)], spacing: DS.Spacing.m) {
                verdict(.done, "Done", "Finished: it leaves the board", .loopsDone, .done)
                verdict(.nextStep(l.yourItems.first?.text ?? ""), "Next step", l.yourItems.first?.text ?? "No step of yours yet", .wrapUpNextStep, .nextStep)
                verdict(.park, "Park", "Off the board until tomorrow 09:00", .loopsPark, .park)
                verdict(.drop, "Drop", "Not worth finishing: kept as a record", .loopsDrop, .drop)
            }
            if case .keep = entry.suggestion {
                Text("Or leave it running: \(CommandRegistry.cap(.wrapUpAccept, in: .wrapUp) ?? "⏎") keeps it as it is.")
                    .font(DS.font(.chrome)).foregroundStyle(Theme.dim)
            }
            if !l.yourItems.isEmpty {
                VStack(alignment: .leading, spacing: DS.Spacing.xs) {
                    Text("Your steps").font(DS.font(.chrome, .semibold)).foregroundStyle(Theme.fg2)
                    ForEach(l.yourItems.prefix(LoopsLook.pickerRows)) { it in
                        HStack(spacing: DS.Spacing.s) { StateMark(.question); Text(it.text).font(DS.font(.chrome)).foregroundStyle(Theme.fg2).lineLimit(1) }
                    }
                }
            }
            Spacer(minLength: 0)
        }
        .padding(DS.Spacing.xl)
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
        .accessibilityIdentifier("wrapup.card")
    }

    private func same(_ a: WrapUpDecision, _ b: WrapUpDecision?) -> Bool {
        switch (a, b) {
        case (.done, .done?), (.park, .park?), (.drop, .drop?), (.nextStep, .nextStep?), (.keep, .keep?): return true
        default: return false
        }
    }

    private func verdict(_ d: WrapUpDecision, _ title: String, _ detail: String, _ id: CommandID, _ action: LoopsKeyAction) -> some View {
        let suggested = same(d, entry.suggestion)
        let chosen = same(d, decided)
        return Button { _ = model.perform(action) } label: {
            VStack(alignment: .leading, spacing: DS.Spacing.xs) {
                HStack(spacing: DS.Spacing.s) {
                    Text(title).font(DS.font(.body, .semibold)).foregroundStyle(Theme.fg)
                    if suggested { Pill("suggested", variant: .count) }
                    Spacer(minLength: 0)
                    if suggested, let k = CommandRegistry.cap(.wrapUpAccept, in: .wrapUp) { Kbd(k) }
                    if let k = CommandRegistry.cap(id, in: .wrapUp) { Kbd(k) }
                }
                Text(detail).font(DS.font(.chrome)).foregroundStyle(Theme.dim).lineLimit(2).multilineTextAlignment(.leading)
            }
            .padding(DS.Spacing.m)
            .frame(maxWidth: .infinity, minHeight: LoopsLook.verdictHeight, alignment: .topLeading)
            .background(DS.Radius.shape(DS.Radius.control).fill(chosen ? Theme.selection : Theme.color(.tile)))
            .overlay(DS.Radius.shape(DS.Radius.control).strokeBorder(suggested || chosen ? Theme.accent : Theme.stroke, lineWidth: DS.hairline))
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .accessibilityIdentifier("wrapup.verdict.\(title.lowercased().replacingOccurrences(of: " ", with: ""))")
    }
}

/// A thin progress bar in a tone (a milestone, the wrap-up).
struct ProgressBar: View {
    var value: Double
    var tone: Theme.Token

    var body: some View {
        GeometryReader { g in
            ZStack(alignment: .leading) {
                Capsule().fill(Theme.chipBG)
                Capsule().fill(Theme.color(tone)).frame(width: g.size.width * min(1, max(0, value)))
            }
        }
        .frame(height: LoopsLook.barHeight)
        .accessibilityValue("\(Int((value * 100).rounded())) percent")
    }
}
