import AppKit
import HesperCore
import SwiftUI

// The review sheet's chrome (SwiftUI: small, observes the session): the
// sheet's header, the selected item's header with its actions, the
// evidence panel; and the inbox's rows (AppKit, drawn directly).

/// "Review  3 ready · on laptop, mini" … "⌘R".
struct ReviewSheetHeader: View {
    var session: ReviewSession
    var machineNames: [String: String]

    var body: some View {
        let items = session.items
        let macs = Array(Set(items.map(\.machine))).sorted().map { machineNames[$0] ?? $0 }
        HStack(spacing: DS.Spacing.m) {
            Image(systemName: "checklist").font(.ds(.panelTitle)).foregroundStyle(Theme.dim)
            Text("Review").font(.ds(.panelTitle, .semibold)).foregroundStyle(Theme.fg)
            Text(items.isEmpty ? "nothing ready" : "\(items.count) ready" + (macs.isEmpty ? "" : " · " + macs.joined(separator: ", ")))
                .font(.ds(.chrome)).foregroundStyle(Theme.dim).lineLimit(1)
            if !session.hub.support.unsupportedMachines.isEmpty {
                let off = session.hub.support.unsupportedMachines.sorted().map { machineNames[$0] ?? $0 }.joined(separator: ", ")
                Text("· \(off): update hesperd to review there").font(.ds(.chrome)).foregroundStyle(Theme.dim).lineLimit(1)
                    .accessibilityIdentifier("review.unsupported")
            }
            Spacer(minLength: DS.Spacing.m)
            if let busy = session.busy {
                ProgressView().controlSize(.small)
                Text(busy).font(.ds(.chrome)).foregroundStyle(Theme.fg2).lineLimit(1)
            }
            Kbd("⌘R")
        }
        .padding(.horizontal, DS.Spacing.xl)
        .frame(height: ReviewLook.headerHeight)
        .overlay(alignment: .bottom) { Rectangle().fill(Theme.stroke).frame(height: 1) }
    }
}

/// The selected item: name, where, churn; Send back ⇧↩ and Accept ⌘↩; the
/// status line (a confirm, an error).
struct ReviewItemHeader: View {
    var session: ReviewSession
    var machineNames: [String: String]

    var body: some View {
        VStack(alignment: .leading, spacing: DS.Spacing.xs) {
            if let item = session.selected {
                HStack(spacing: DS.Spacing.m) {
                    StateMark(state: item.state)
                    Text(item.name).font(.ds(.panelTitle, .semibold)).foregroundStyle(Theme.fg).lineLimit(1)
                    Text(meta(item)).font(.ds(.meta)).foregroundStyle(Theme.dim).lineLimit(1).truncationMode(.middle)
                    Spacer(minLength: DS.Spacing.m)
                    let n = session.currentNotes.count
                    ReviewButton(title: n == 0 ? "Send back" : "Send back · \(n)", key: "⇧↩", primary: false) { session.sendBack() }
                        .accessibilityIdentifier("review.sendBack")
                    ReviewButton(title: "Accept & commit", key: "⌘↩", primary: true) { session.accept() }
                        .accessibilityIdentifier("review.accept")
                }
                statusLine
            } else if !session.items.isEmpty {
                Text("Pick an item on the left.").font(.ds(.body)).foregroundStyle(Theme.dim)
            }
        }
        .padding(.horizontal, DS.Spacing.l)
        .frame(maxWidth: .infinity, minHeight: ReviewLook.itemHeaderHeight, alignment: .leading)
        .overlay(alignment: .bottom) { Rectangle().fill(Theme.stroke).frame(height: 1) }
    }

    private func meta(_ i: ReviewItem) -> String {
        var parts = [machineNames[i.machine] ?? i.machine]
        if let b = i.branch { parts.append(b) } else if let p = i.project { parts.append(SessionFormat.abbreviate(p)) }
        parts.append(ReviewText.files(i.files))
        parts.append(ReviewText.churn(added: i.added, removed: i.removed))
        return parts.joined(separator: " · ")
    }

    @ViewBuilder private var statusLine: some View {
        switch session.confirm {
        case .accept(_, let unseen):
            line("\(unseen) hunk\(unseen == 1 ? "" : "s") not marked seen (V). ⌘↩ again accepts anyway · esc", tone: .question)
        case .bulk(let ids):
            let names = ids.compactMap { session.hub.item($0)?.name }.joined(separator: ", ")
            line("Accept \(ids.count) low-risk item\(ids.count == 1 ? "" : "s") with fresh tests: \(names). ⇧A again · esc", tone: .question)
        case nil:
            if let s = session.status { line(s.text, tone: s.error ? .error : .dim) }
            else if let f = session.selectedID.flatMap({ session.failures[$0] }) { line("Couldn't load the diff: \(f)", tone: .error) }
            else if let id = session.selectedID, session.loading.contains(id), session.diff == nil { line("Loading the diff…", tone: .dim) }
        }
    }

    private func line(_ text: String, tone: Theme.Token) -> some View {
        Text(text).font(.ds(.chrome)).foregroundStyle(Theme.color(tone)).lineLimit(1).truncationMode(.tail)
            .accessibilityIdentifier("review.status")
    }
}

/// An action pill: a key as Kbd; primary is `working`-tinted (never Signal).
struct ReviewButton: View {
    var title: String
    var key: String
    var primary: Bool
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
        .help("\(title) (\(key))")
    }
}

/// The right-hand panel: evidence (fresh ✓ / stale ! / missing ?) with the
/// commands, risk notes, the attention strip, bulk eligibility.
struct ReviewEvidencePanel: View {
    var session: ReviewSession
    var now: Date = Date()

    var body: some View {
        ScrollView(.vertical) {
            VStack(alignment: .leading, spacing: DS.Spacing.xl) {
                if let item = session.selected {
                    evidence(item)
                    risk(item)
                    attention
                    bulk
                }
            }
            .padding(DS.Spacing.l)
            .frame(maxWidth: .infinity, alignment: .leading)
        }
        .scrollIndicators(.automatic)
        .accessibilityIdentifier("review.evidence")
    }

    private func title(_ s: String) -> some View {
        Text(s.uppercased()).font(.ds(.meta, .medium)).foregroundStyle(Theme.dim)
    }

    @ViewBuilder private func evidence(_ item: ReviewItem) -> some View {
        let ev = session.currentEvidence
        let badge = EvidenceBadge(ev?.freshness ?? item.evidence)
        let tone = Theme.token(badge.tone)
        VStack(alignment: .leading, spacing: DS.Spacing.m) {
            title("Evidence")
            HStack(alignment: .top, spacing: DS.Spacing.m) {
                Text(badge.symbol).font(.ds(.panelTitle, .semibold)).foregroundStyle(Theme.color(tone))
                    .frame(width: ReviewLook.inboxRowHeight / 2, height: ReviewLook.inboxRowHeight / 2)
                    .background(DS.Radius.shape(DS.Radius.control).fill(Theme.color(tone).opacity(SearchLook.selectionAlpha)))
                VStack(alignment: .leading, spacing: DS.Spacing.xxs) {
                    Text(badge.title).font(.ds(.body, .semibold)).foregroundStyle(Theme.fg)
                    Text(badge.explanation).font(.ds(.chrome)).foregroundStyle(Theme.fg2).fixedSize(horizontal: false, vertical: true)
                }
            }
            .accessibilityElement(children: .combine)
            .accessibilityIdentifier("review.evidence.badge")
            if let ev {
                if ev.commands.isEmpty {
                    Text("No commands recorded").font(.ds(.chrome)).foregroundStyle(Theme.dim)
                } else {
                    VStack(alignment: .leading, spacing: DS.Spacing.s) {
                        ForEach(Array(EvidenceText.ordered(ev.commands).prefix(8).enumerated()), id: \.offset) { _, c in
                            command(c)
                        }
                    }
                }
                if !ev.attachments.isEmpty {
                    VStack(alignment: .leading, spacing: DS.Spacing.xs) {
                        ForEach(Array(ev.attachments.prefix(6).enumerated()), id: \.offset) { _, a in
                            HStack(spacing: DS.Spacing.s) {
                                Image(systemName: a.kind == .image ? "photo" : a.kind == .video ? "film" : "doc.text").foregroundStyle(Theme.dim)
                                Text((a.path as NSString).lastPathComponent).font(.ds(.meta)).foregroundStyle(Theme.fg2).lineLimit(1).truncationMode(.middle)
                            }
                        }
                    }
                }
            } else if session.evidenceOff.contains(item.machine) {
                Text("Not recorded on this Mac (older hesperd)").font(.ds(.chrome)).foregroundStyle(Theme.dim)
            }
        }
    }

    private func command(_ c: EvidenceCommand) -> some View {
        let tone: Theme.Token = c.running ? .working : c.passed ? .done : .error
        return HStack(alignment: .firstTextBaseline, spacing: DS.Spacing.s) {
            Text(EvidenceText.mark(c)).font(.ds(.meta, .semibold)).foregroundStyle(Theme.color(tone)).frame(width: DS.Spacing.l, alignment: .leading)
            VStack(alignment: .leading, spacing: DS.Spacing.xxs) {
                Text(c.command).font(.ds(.meta)).foregroundStyle(c.kind.proves ? Theme.fg : Theme.fg2).lineLimit(1).truncationMode(.middle)
                Text(EvidenceText.meta(c, now: now)).font(.ds(.meta)).foregroundStyle(Theme.dim).lineLimit(1)
            }
        }
        .accessibilityElement(children: .combine)
    }

    @ViewBuilder private func risk(_ item: ReviewItem) -> some View {
        VStack(alignment: .leading, spacing: DS.Spacing.s) {
            title("Risk")
            HStack(spacing: DS.Spacing.s) {
                StateMark(color: Theme.color(ReviewLook.tone(item.risk)), side: DS.Spacing.s)
                Text(item.risk.rawValue.capitalized).font(.ds(.body, .medium)).foregroundStyle(Theme.fg)
            }
            ForEach(Array(item.riskNotes.prefix(6).enumerated()), id: \.offset) { _, n in
                Text("· \(n)").font(.ds(.chrome)).foregroundStyle(Theme.fg2).fixedSize(horizontal: false, vertical: true)
            }
        }
    }

    @ViewBuilder private var attention: some View {
        if let s = session.stream, !s.hunks.isEmpty {
            let a = session.currentAttention
            let marks = a.marks(s.hunks, noted: session.notedKeys)
            VStack(alignment: .leading, spacing: DS.Spacing.s) {
                title("Attention")
                ReviewAttentionStrip(marks: marks, focused: session.focused) { session.setFocus($0) }
                let rejected = a.rejected.count, notes = session.currentNotes.count
                Text([a.summary(total: s.hunks.count), rejected > 0 ? "\(rejected) rejected" : nil, notes > 0 ? "\(notes) note\(notes == 1 ? "" : "s")" : nil]
                    .compactMap { $0 }.joined(separator: " · "))
                    .font(.ds(.chrome)).foregroundStyle(Theme.fg2)
                    .accessibilityIdentifier("review.attention")
            }
        }
    }

    @ViewBuilder private var bulk: some View {
        if let v = session.bulkVerdict {
            VStack(alignment: .leading, spacing: DS.Spacing.s) {
                title("Bulk accept ⇧A")
                Text(v == .eligible ? "Eligible: \(ReviewBulk.reason(v))" : "Not in bulk: \(ReviewBulk.reason(v))")
                    .font(.ds(.chrome)).foregroundStyle(v == .eligible ? Theme.color(.done) : Theme.dim)
                    .fixedSize(horizontal: false, vertical: true)
            }
        }
    }
}

/// One cell per hunk in reading order: seen `done`, rejected `error`,
/// noted `question`, unseen `line`; the focused one outlined `working`.
struct ReviewAttentionStrip: View {
    var marks: [ReviewAttention.Mark]
    var focused: Int?
    var onPick: (Int) -> Void

    static let cell: CGFloat = 8

    var body: some View {
        // Wraps: rows of cells (hundreds of hunks stay one small block).
        let columns = [GridItem(.adaptive(minimum: Self.cell, maximum: Self.cell), spacing: DS.Spacing.xxs)]
        LazyVGrid(columns: columns, alignment: .leading, spacing: DS.Spacing.xxs) {
            ForEach(Array(marks.enumerated()), id: \.offset) { i, m in
                Rectangle().fill(color(m)).frame(width: Self.cell, height: Self.cell)
                    .overlay(Rectangle().strokeBorder(i == focused ? Theme.color(.working) : .clear, lineWidth: 1.5))
                    .onTapGesture { onPick(i) }
            }
        }
        .accessibilityElement(children: .ignore)
        .accessibilityLabel("Hunks seen: \(marks.filter { $0 == .seen }.count) of \(marks.count)")
    }

    private func color(_ m: ReviewAttention.Mark) -> Color {
        switch m {
        case .seen: return Theme.color(.done)
        case .rejected: return Theme.color(.error)
        case .noted: return Theme.color(.question)
        case .unseen: return Theme.stroke
        }
    }
}

// MARK: The inbox (AppKit)

/// An inbox row: risk square, name (600), the evidence badge trailing;
/// "mini · 3 files · +120 −14" under it. Drawn directly.
@MainActor
final class ReviewInboxCell: NSView {
    static let id = NSUserInterfaceItemIdentifier("reviewInbox")
    private var item = ReviewItem(id: "")
    private var machine = ""
    var selected = false { didSet { if oldValue != selected { needsDisplay = true } } }

    override var isFlipped: Bool { true }

    func show(_ item: ReviewItem, machine: String, selected: Bool) {
        self.item = item
        self.machine = machine
        self.selected = selected
        needsDisplay = true
        setAccessibilityElement(true)
        setAccessibilityRole(.row)
        setAccessibilityLabel("\(item.name) on \(machine), \(ReviewText.files(item.files)), \(item.risk.rawValue) risk, \(EvidenceBadge(item.evidence).title)")
    }

    override func draw(_ dirtyRect: NSRect) {
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
        Theme.ns(ReviewLook.tone(item.risk) == .dim ? .done : ReviewLook.tone(item.risk)).setFill()
        NSRect(x: x, y: top + (th - side) / 2, width: side, height: side).fill()
        let badge = EvidenceBadge(item.evidence)
        let ev = NSAttributedString(string: badge.short, attributes: [.font: NSFont.ds(.meta, .medium), .foregroundColor: Theme.ns(Theme.token(badge.tone))])
        let evW = ev.size().width
        ev.draw(at: NSPoint(x: b.maxX - DS.Spacing.m - evW, y: top + (th - ev.size().height) / 2))
        let tx = x + side + DS.Spacing.m
        let title = NSAttributedString(string: item.name, attributes: [.font: titleFont, .foregroundColor: Theme.ns(.text)])
        title.draw(with: NSRect(x: tx, y: top, width: max(0, b.maxX - DS.Spacing.m - evW - DS.Spacing.s - tx), height: th),
                   options: [.usesLineFragmentOrigin, .truncatesLastVisibleLine], context: nil)
        let meta = NSMutableAttributedString(string: "\(machine) · \(ReviewText.files(item.files)) · ", attributes: [.font: metaFont, .foregroundColor: Theme.ns(.dim)])
        meta.append(NSAttributedString(string: "+\(item.added)", attributes: [.font: metaFont, .foregroundColor: Theme.ns(.done)]))
        meta.append(NSAttributedString(string: " −\(item.removed)", attributes: [.font: metaFont, .foregroundColor: Theme.ns(.error)]))
        if item.risk != .low {
            meta.append(NSAttributedString(string: " · \(item.risk.rawValue) risk", attributes: [.font: metaFont, .foregroundColor: Theme.ns(ReviewLook.tone(item.risk))]))
        }
        meta.draw(with: NSRect(x: tx, y: top + th + DS.Spacing.xxs, width: max(0, b.maxX - DS.Spacing.m - tx), height: mh),
                  options: [.usesLineFragmentOrigin, .truncatesLastVisibleLine], context: nil)
    }
}

// MARK: The tile's footer

extension AttentionBar {
    /// A finished agent ready to review.
    struct ReviewReady {
        var item: ReviewItem
        var onOpen: () -> Void
    }
}

/// "Ready to review · 3 files · tests ✓ … Review ⏎" in a finished tile's
/// footer; ⏎ on the selected tile opens the sheet on it (KeyRouter).
struct ReviewReadyRow: View {
    var ready: AttentionBar.ReviewReady
    var state: AgentState
    var keyHints: Bool
    var tight: Bool

    var body: some View {
        let item = ready.item
        let badge = EvidenceBadge(item.evidence)
        HStack(spacing: DS.Spacing.m) {
            StateMark(state: state)
            HStack(spacing: DS.Spacing.xs) {
                Text("Ready to review").font(.ds(.chrome, .medium)).foregroundStyle(Theme.fg)
                Text("· \(ReviewText.files(item.files)) ·").font(.ds(.chrome)).foregroundStyle(Theme.dim)
                Text(badge.short).font(.ds(.meta, .medium)).foregroundStyle(Theme.color(Theme.token(badge.tone)))
            }
            .lineLimit(1)
            Spacer(minLength: DS.Spacing.m)
            Button(action: ready.onOpen) {
                HStack(spacing: DS.Spacing.s) {
                    Text("Review").font(.ds(.chrome, .medium)).lineLimit(1)
                    if keyHints && !tight { Kbd("⏎") }
                }
                .padding(.horizontal, DS.Spacing.m)
                .frame(height: Pill.height)
                .background(DS.Radius.shape(DS.Radius.control).fill(Theme.color(.working).opacity(AttentionBar.primaryFill)))
                .overlay(DS.Radius.shape(DS.Radius.control).strokeBorder(Theme.color(.working).opacity(AttentionBar.primaryStroke), lineWidth: 1))
                .foregroundStyle(Theme.fg)
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .fixedSize()
            .help("Review (⌘R)")
            .accessibilityIdentifier("tile.review")
        }
        .accessibilityElement(children: .combine)
        .accessibilityLabel(ReviewText.footer(item))
        .accessibilityIdentifier("tile.reviewReady")
    }
}
