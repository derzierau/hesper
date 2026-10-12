import AppKit
import HesperCore
import SwiftUI

// The routines panel's chrome (SwiftUI: small, observes the session): the
// sheet's header, the selected routine with its actions and runs, the
// outcome chip; and the list's rows (AppKit, drawn directly).

/// "Routines  5 · 1 needs you" … "⌘⇧R".
struct RoutinesSheetHeader: View {
    var session: RoutinesSession

    var body: some View {
        let list = session.routines
        let needs = list.filter { $0.lastRun?.needsYou == true }.count
        let running = list.filter { $0.lastRun?.isRunning == true }.count
        HStack(spacing: DS.Spacing.m) {
            Image(systemName: "clock.arrow.2.circlepath").font(.ds(.panelTitle)).foregroundStyle(Theme.dim)
            Text("Routines").font(.ds(.panelTitle, .semibold)).foregroundStyle(Theme.fg)
            Text(subtitle(list.count, needs: needs, running: running)).font(.ds(.chrome)).foregroundStyle(Theme.dim).lineLimit(1)
                .accessibilityIdentifier("routines.subtitle")
            Spacer(minLength: DS.Spacing.m)
            if let busy = session.busy {
                ProgressView().controlSize(.small)
                Text(busy).font(.ds(.chrome)).foregroundStyle(Theme.fg2).lineLimit(1)
            }
            Kbd("⌘⇧R")
        }
        .padding(.horizontal, DS.Spacing.xl)
        .frame(height: RoutineLook.headerHeight)
        .overlay(alignment: .bottom) { Rectangle().fill(Theme.stroke).frame(height: 1) }
    }

    private func subtitle(_ n: Int, needs: Int, running: Int) -> String {
        guard n > 0 else { return "none saved" }
        var parts = ["\(n)"]
        if needs > 0 { parts.append("\(needs) need\(needs == 1 ? "s" : "") you") }
        if running > 0 { parts.append("\(running) running") }
        return parts.joined(separator: " · ")
    }
}

/// The last run's outcome as a chip: a state mark and the words, tinted
/// with the tone (`done`, `signal`, `error`, `dim`; running `working`).
struct RoutineChip: View {
    var run: RoutineRun?

    var body: some View {
        let kind = RoutineLook.mark(run)
        Pill(run.map { RoutineText.outcome($0.outcome) } ?? "never ran", mark: kind)
            .accessibilityLabel("Last run: \(run.map { RoutineText.outcome($0.outcome) } ?? "never ran")")
    }
}

/// An action pill: a key as Kbd; primary is `working`-tinted (never Signal).
struct RoutineButton: View {
    var title: String
    var key: String
    var primary = false
    var enabled = true
    var action: () -> Void

    var body: some View {
        Button(action: action) {
            HStack(spacing: DS.Spacing.s) {
                Text(title).font(.ds(.chrome, .medium)).lineLimit(1)
                Kbd(key)
            }
            .padding(.horizontal, DS.Spacing.m)
            .frame(height: Pill.height)
            .background(DS.Radius.shape(DS.Radius.control).fill(primary ? Theme.color(.working).opacity(AttentionBar.primaryFill) : Theme.chipBG))
            .overlay(DS.Radius.shape(DS.Radius.control).strokeBorder(primary ? Theme.color(.working).opacity(AttentionBar.primaryStroke) : Theme.stroke, lineWidth: 1))
            .foregroundStyle(Theme.fg)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .fixedSize()
        .disabled(!enabled)
        .opacity(enabled ? 1 : SearchLook.disabledOpacity)
        .help("\(title) (\(key))")
    }
}

/// The right-hand side: the selected routine (name, where, cadence, next
/// run), its actions, the status line, the task's first lines, project,
/// Mac, policy; then its runs newest first.
struct RoutineDetailView: View {
    var session: RoutinesSession
    var machineNames: [String: String]
    /// An agent's name when it is on a wall (else its id shows).
    var agentName: (String) -> String?
    /// Read per render: "in 12m" follows the clock while the panel is open.
    private var now: Date { Date() }

    var body: some View {
        if let r = session.selected {
            VStack(alignment: .leading, spacing: 0) {
                top(r)
                Rectangle().fill(Theme.stroke).frame(height: 1)
                runs(r)
            }
            .accessibilityIdentifier("routines.detail")
        } else if !session.routines.isEmpty {
            Text("Pick a routine on the left.").font(.ds(.body)).foregroundStyle(Theme.dim).padding(DS.Spacing.l)
                .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
        }
    }

    private func mac(_ r: Routine) -> String { machineNames[r.machine] ?? (r.machine.isEmpty ? "this Mac" : r.machine) }

    @ViewBuilder private func top(_ r: Routine) -> some View {
        VStack(alignment: .leading, spacing: DS.Spacing.m) {
            HStack(spacing: DS.Spacing.m) {
                RoutineChip(run: r.lastRun).accessibilityIdentifier("routines.chip")
                Text(r.name).font(.ds(.panelTitle, .semibold)).foregroundStyle(Theme.fg).lineLimit(1)
                Text([mac(r), RoutineText.cadence(r), r.paused ? "paused" : "next " + RoutineText.nextRun(r, now: now)].joined(separator: " · "))
                    .font(.ds(.meta)).foregroundStyle(r.paused ? Theme.color(.question) : Theme.dim).lineLimit(1).truncationMode(.middle)
                    .accessibilityIdentifier("routines.meta")
                Spacer(minLength: DS.Spacing.m)
            }
            HStack(spacing: DS.Spacing.s) {
                RoutineButton(title: "Run now", key: "⏎", primary: true) { session.runNow() }.accessibilityIdentifier("routines.run")
                RoutineButton(title: r.paused ? "Resume" : "Pause", key: "P") { session.togglePause() }.accessibilityIdentifier("routines.pause")
                RoutineButton(title: "Open run", key: "O", enabled: r.lastRun != nil) { session.open() }.accessibilityIdentifier("routines.open")
                RoutineButton(title: session.confirmRemove == r.id ? "Remove: sure?" : "Remove", key: "⌫") { session.remove() }
                    .accessibilityIdentifier("routines.remove")
            }
            statusLine(r)
            VStack(alignment: .leading, spacing: DS.Spacing.xs) {
                Text(task(r)).font(.ds(.body)).foregroundStyle(Theme.fg2).lineLimit(RoutineLook.taskLines).fixedSize(horizontal: false, vertical: true)
                    .accessibilityIdentifier("routines.task")
                Text(where_(r)).font(.ds(.meta)).foregroundStyle(Theme.dim).lineLimit(1).truncationMode(.middle)
                Text(RoutineText.policy(r.policy)).font(.ds(.meta)).foregroundStyle(Theme.dim).lineLimit(1)
                    .accessibilityIdentifier("routines.policy")
            }
        }
        .padding(DS.Spacing.l)
        .frame(maxWidth: .infinity, alignment: .leading)
    }

    private func task(_ r: Routine) -> String {
        let lines = r.task.split(whereSeparator: \.isNewline).map { $0.trimmingCharacters(in: .whitespaces) }.filter { !$0.isEmpty }
        let shown = lines.prefix(RoutineLook.taskLines).joined(separator: "\n")
        return shown.isEmpty ? "(no task)" : shown + (lines.count > RoutineLook.taskLines ? " …" : "")
    }

    private func where_(_ r: Routine) -> String {
        var parts: [String] = []
        if let p = r.project { parts.append(SessionFormat.abbreviate(p)) }
        parts.append("on " + mac(r))
        if let k = r.kind { parts.append(k) }
        if r.runs > 0 { parts.append("\(r.runs) run\(r.runs == 1 ? "" : "s") so far") }
        return parts.joined(separator: " · ")
    }

    @ViewBuilder private func statusLine(_ r: Routine) -> some View {
        if session.confirmRemove == r.id {
            line("Remove \(r.name) and its runs? ⌫ again removes it · esc keeps it", tone: .question)
        } else if let n = session.note {
            line(n.text, tone: n.error ? .error : .dim)
        } else {
            line(r.paused ? "Paused: the clock skips it; ⏎ still runs it" : "⏎ runs it now without moving the clock", tone: .dim)
        }
    }

    private func line(_ text: String, tone: Theme.Token) -> some View {
        Text(text).font(.ds(.chrome)).foregroundStyle(Theme.color(tone)).lineLimit(1).truncationMode(.tail)
            .accessibilityIdentifier("routines.status")
    }

    @ViewBuilder private func runs(_ r: Routine) -> some View {
        let list = session.runs
        ScrollView(.vertical) {
            LazyVStack(alignment: .leading, spacing: 0) {
                HStack {
                    Text("RUNS").font(.ds(.meta, .medium)).foregroundStyle(Theme.dim)
                    if !list.isEmpty { Text("\(list.count)").font(.ds(.meta)).foregroundStyle(Theme.dim) }
                }
                .padding(.horizontal, DS.Spacing.l).padding(.top, DS.Spacing.l).padding(.bottom, DS.Spacing.s)
                if list.isEmpty {
                    Text(session.loadingRuns ? "Loading runs…" : "No runs yet. ⏎ starts one now.").font(.ds(.chrome)).foregroundStyle(Theme.dim)
                        .padding(.horizontal, DS.Spacing.l)
                }
                ForEach(list) { run in
                    RoutineRunRow(run: run, agentName: agentName, now: now) { session.open(run) }
                }
            }
            .padding(.bottom, DS.Spacing.l)
        }
        .scrollIndicators(.automatic)
        .accessibilityIdentifier("routines.runs")
    }
}

/// One run: "#12 · Mon 09:10 · 3m 12s", the chip, the agent, the message or error.
struct RoutineRunRow: View {
    var run: RoutineRun
    var agentName: (String) -> String?
    var now: Date
    var onOpen: () -> Void

    var body: some View {
        Button(action: onOpen) {
            VStack(alignment: .leading, spacing: DS.Spacing.xxs) {
                HStack(spacing: DS.Spacing.m) {
                    Text("#\(run.seq)").font(.ds(.meta, .medium)).foregroundStyle(Theme.dim).frame(width: 36, alignment: .leading)
                    RoutineChip(run: run)
                    Text(meta).font(.ds(.meta)).foregroundStyle(Theme.dim).lineLimit(1)
                    Spacer(minLength: DS.Spacing.s)
                    if let a = run.agent { Text(agentName(a) ?? a).font(.ds(.meta)).foregroundStyle(Theme.fg2).lineLimit(1).truncationMode(.middle) }
                }
                let summary = RoutineText.summaryLine(run)
                if !summary.isEmpty {
                    Text(summary).font(.ds(.chrome)).foregroundStyle(run.error != nil && run.message == nil ? Theme.color(.error) : Theme.fg)
                        .lineLimit(1).truncationMode(.tail).padding(.leading, 36 + DS.Spacing.m)
                }
            }
            .padding(.horizontal, DS.Spacing.l).padding(.vertical, DS.Spacing.s)
            .frame(maxWidth: .infinity, alignment: .leading)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .accessibilityElement(children: .combine)
        .accessibilityLabel("Run \(run.seq), \(RoutineText.outcome(run.outcome)), \(RoutineText.summaryLine(run))")
        .accessibilityIdentifier("routines.run.\(run.seq)")
    }

    private var meta: String {
        var parts: [String] = []
        if let s = run.started { parts.append(SessionFormat.when(s, now: now)) }
        let d = RoutineText.duration(run, now: now)
        if !d.isEmpty { parts.append(d) }
        if run.manual { parts.append("by hand") }
        return parts.joined(separator: " · ")
    }
}

// MARK: The list (AppKit)

/// A routine's row: the last run's mark, name (600), the outcome trailing;
/// "mini · every 1h +5m · in 12m" under it. Drawn directly.
@MainActor
final class RoutineRowCell: NSView {
    static let id = NSUserInterfaceItemIdentifier("routineRow")
    private var routine = Routine(id: "r-")
    private var machine = ""
    var selected = false { didSet { if oldValue != selected { needsDisplay = true } } }

    override var isFlipped: Bool { true }

    func show(_ r: Routine, machine: String, selected: Bool) {
        routine = r
        self.machine = machine
        self.selected = selected
        needsDisplay = true
        setAccessibilityElement(true)
        setAccessibilityRole(.row)
        let now = Date()
        setAccessibilityLabel("\(r.name) on \(machine), \(RoutineText.cadence(r)), \(RoutineText.nextRun(r, now: now)), \(RoutineText.lastRun(r, now: now))")
        setAccessibilityIdentifier("routines.row.\(r.id)")
    }

    override func draw(_ dirtyRect: NSRect) {
        let r = routine
        let now = Date()
        let b = bounds.insetBy(dx: DS.Spacing.s, dy: DS.Spacing.xxs)
        if selected {
            Theme.ns(.working, alpha: SearchLook.selectionAlpha).setFill()
            NSBezierPath(roundedRect: b, xRadius: DS.Radius.control, yRadius: DS.Radius.control).fill()
        }
        let side = CGFloat(StateMarkKind.side)
        let x = b.minX + DS.Spacing.m
        let titleFont = NSFont.ds(.body, .semibold), metaFont = NSFont.ds(.meta)
        let th = (titleFont.ascender - titleFont.descender).rounded(.up), mh = (metaFont.ascender - metaFont.descender).rounded(.up)
        let top = b.minY + ((b.height - th - DS.Spacing.xxs - mh) / 2).rounded()
        // The last run's mark (outlined when idle / never ran, slashed when exited).
        let kind = RoutineLook.mark(r.lastRun)
        let markColor = Theme.ns(Theme.token(kind.tone))
        let markRect = NSRect(x: x, y: top + (th - side) / 2, width: side, height: side)
        switch kind.fill {
        case .filled:
            markColor.setFill()
            markRect.fill()
        case .outlined, .slashed:
            markColor.setStroke()
            let w = CGFloat(StateMarkKind.stroke)
            let p = NSBezierPath(rect: markRect.insetBy(dx: w / 2, dy: w / 2))
            p.lineWidth = w
            p.stroke()
            if kind.fill == .slashed {
                let s = NSBezierPath()
                s.move(to: NSPoint(x: markRect.minX, y: markRect.maxY))
                s.line(to: NSPoint(x: markRect.maxX, y: markRect.minY))
                s.lineWidth = w
                s.stroke()
            }
        }
        let tone = r.lastRun.map { RoutineText.tone($0.outcome) } ?? .neutral
        let chipColor = r.lastRun?.isRunning == true ? Theme.ns(.working) : Theme.ns(RoutineLook.token(tone))
        let chip = NSAttributedString(string: RoutineText.lastRun(r, now: now), attributes: [.font: NSFont.ds(.meta, .medium), .foregroundColor: chipColor])
        let chipW = chip.size().width
        chip.draw(at: NSPoint(x: b.maxX - DS.Spacing.m - chipW, y: top + (th - chip.size().height) / 2))
        let tx = x + side + DS.Spacing.m
        let title = NSAttributedString(string: r.name, attributes: [.font: titleFont, .foregroundColor: Theme.ns(r.paused ? .dim : .text)])
        title.draw(with: NSRect(x: tx, y: top, width: max(0, b.maxX - DS.Spacing.m - chipW - DS.Spacing.s - tx), height: th),
                   options: [.usesLineFragmentOrigin, .truncatesLastVisibleLine], context: nil)
        let next = RoutineText.nextRun(r, now: now)
        let meta = NSMutableAttributedString(string: "\(machine) · \(RoutineText.cadence(r)) · ", attributes: [.font: metaFont, .foregroundColor: Theme.ns(.dim)])
        meta.append(NSAttributedString(string: next, attributes: [.font: metaFont, .foregroundColor: Theme.ns(r.paused ? .question : next == "due" ? .working : .dim)]))
        meta.draw(with: NSRect(x: tx, y: top + th + DS.Spacing.xxs, width: max(0, b.maxX - DS.Spacing.m - tx), height: mh),
                  options: [.usesLineFragmentOrigin, .truncatesLastVisibleLine], context: nil)
    }
}
