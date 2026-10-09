import AppKit
import CoreText
import HesperCore

/// The review sheet's measures (named; spacing, type and colors come from
/// DS / Theme).
enum ReviewLook {
    /// The sheet: min(1440, 94%) × min(940, 88%) of the window, a little
    /// above the middle (like History's).
    static let maxWidth: CGFloat = 1440
    static let maxHeight: CGFloat = 940
    static let widthFraction: CGFloat = 0.94
    static let heightFraction: CGFloat = 0.88
    /// Three columns (inbox · stream · evidence) from this sheet width;
    /// narrower, the evidence sits under the inbox.
    static let wideMin: CGFloat = 1120
    static let inboxWidth: CGFloat = 264
    static let narrowInboxWidth: CGFloat = 240
    static let evidenceWidth: CGFloat = 300
    /// The inbox's share of the left column when narrow.
    static let narrowInboxFraction: CGFloat = 0.42
    static let headerHeight: CGFloat = 48
    static let itemHeaderHeight: CGFloat = 52
    static let inboxRowHeight: CGFloat = 52
    static let noteEditorHeight: CGFloat = 88

    // The stream.
    @MainActor static let codeFont = DS.nsMonoFont(.chrome)
    @MainActor static let codeLineHeight: CGFloat = (codeFont.ascender - codeFont.descender + codeFont.leading).rounded(.up) + DS.Spacing.xs
    static let fileHeaderHeight: CGFloat = 34
    static let hunkHeaderHeight: CGFloat = 26
    static let foldedHeight: CGFloat = 30
    static let hunkGap: CGFloat = 10
    static let provenanceHeight: CGFloat = 28
    /// Line number columns (each fits 5 digits), then the +/− column.
    @MainActor static var numberWidth: CGFloat { (("00000" as NSString).size(withAttributes: [.font: codeFont]).width + DS.Spacing.m).rounded(.up) }
    static let signWidth: CGFloat = 16
    /// The focused hunk's bar on the left.
    static let focusBar: CGFloat = 3
    static let addedAlpha: CGFloat = 0.12
    static let removedAlpha: CGFloat = 0.12
    static let wordAlpha: CGFloat = 0.32
    static let gutterAlpha: CGFloat = 0.06
    /// A rejected hunk draws faded.
    static let rejectedAlpha: CGFloat = 0.38
    static let noteAlpha: CGFloat = 0.10
    static let fileFillAlpha: CGFloat = 0.55

    static func frame(in size: CGSize) -> CGRect {
        let w = min(maxWidth, (size.width * widthFraction).rounded())
        let h = min(maxHeight, (size.height * heightFraction).rounded())
        let y = ((size.height - h) * SearchLook.historyAboveFraction).rounded()
        return CGRect(x: ((size.width - w) / 2).rounded(), y: max(0, y), width: max(0, w), height: max(0, h))
    }

    static func tone(_ r: ReviewRisk) -> Theme.Token {
        switch r {
        case .low: return .dim
        case .medium: return .question
        case .high: return .horizon
        }
    }

    static func tone(_ s: ReviewFileStatus) -> Theme.Token {
        switch s {
        case .added: return .done
        case .deleted: return .error
        case .renamed: return .question
        case .modified: return .working
        }
    }
}

/// One virtualized stream of every file's rows (NSTableView, view-based):
/// code rows at one fixed height drawn with cached CTLines, notes and
/// the line under each hunk the only rows measured. The file being read
/// keeps its header at the top (pushed up by the next one).
@MainActor
final class ReviewStreamView: NSScrollView, NSTableViewDataSource, NSTableViewDelegate {
    let table = ReviewTable()
    weak var session: ReviewSession?
    private let sticky = ReviewRowCell()
    /// Note heights by anchor at a width (only these are measured).
    private var noteHeights: [ReviewNoteAnchor: (width: CGFloat, height: CGFloat)] = [:]
    private var lastWidth: CGFloat = 0
    private var lastHeight: CGFloat = 0
    /// Rows built (perf: what the table mounted).
    private(set) var cellsMade = 0

    init(session: ReviewSession) {
        self.session = session
        super.init(frame: .zero)
        let col = NSTableColumn(identifier: .init("r"))
        table.addTableColumn(col)
        table.headerView = nil
        table.usesAutomaticRowHeights = false
        table.intercellSpacing = .zero
        table.backgroundColor = .clear
        table.selectionHighlightStyle = .none
        table.style = .plain
        table.focusRingType = .none
        table.dataSource = self
        table.delegate = self
        table.target = self
        table.action = #selector(clicked)
        table.doubleAction = #selector(doubleClicked)
        table.setAccessibilityIdentifier("review.stream")
        table.setAccessibilityLabel("Changes")
        documentView = table
        drawsBackground = false
        hasVerticalScroller = true
        autohidesScrollers = true
        scrollerStyle = .overlay
        sticky.isHidden = true
        addSubview(sticky)
        contentView.postsBoundsChangedNotifications = true
        NotificationCenter.default.addObserver(forName: NSView.boundsDidChangeNotification, object: contentView, queue: .main) { [weak self] _ in
            MainActor.assumeIsolated { self?.updateSticky() }
        }
    }

    /// The header of the file at the top, when its own row has scrolled
    /// off; the next file's header pushes it up.
    func updateSticky() {
        guard let s = session?.stream, !s.rows.isEmpty else { sticky.isHidden = true; return }
        let visible = contentView.bounds
        let top = table.row(at: NSPoint(x: 0, y: max(0, visible.minY)))
        guard top >= 0, let f = s.fileIndex(atRow: top), let headerRow = s.headerRow(ofFile: f) else { sticky.isHidden = true; return }
        let h = ReviewLook.fileHeaderHeight
        if table.rect(ofRow: headerRow).minY >= visible.minY - 0.5 { sticky.isHidden = true; return }
        var y: CGFloat = 0
        if let next = s.headerRow(ofFile: f + 1) {
            let gap = table.rect(ofRow: next).minY - visible.minY
            if gap < h { y = gap - h }
        }
        sticky.show(row: headerRow, kind: .file(f), session: session)
        // The scroll view isn't flipped: the top is maxY; pushed up is higher.
        let c = contentView.frame
        sticky.frame = NSRect(x: c.minX, y: isFlipped ? c.minY + y : c.maxY - h - y, width: c.width, height: h)
        sticky.isHidden = false
    }

    @available(*, unavailable)
    required init?(coder: NSCoder) { fatalError() }

    private var rows: [ReviewRow] { session?.stream?.rows ?? [] }

    func reload() {
        noteHeights = [:]
        table.reloadData()
        updateSticky()
    }

    /// The focused hunk moved: redraw both hunks, resize their last rows,
    /// bring the new one into view.
    func focusChanged(old: Int?, new: Int?) {
        guard let s = session?.stream else { return }
        var ends = IndexSet()
        for o in [old, new].compactMap({ $0 }) where s.hunks.indices.contains(o) {
            if let e = s.endRow(ofHunk: s.hunks[o]) { ends.insert(e) }
        }
        if !ends.isEmpty { table.noteHeightOfRows(withIndexesChanged: ends) }
        table.enumerateAvailableRowViews { rv, _ in rv.subviews.forEach { $0.needsDisplay = true } }
        if let n = new { revealHunk(n) }
    }

    private func revealHunk(_ n: Int) {
        guard let s = session?.stream, s.hunks.indices.contains(n), let header = s.headerRow(ofHunk: s.hunks[n].id) else { return }
        reveal(header, end: s.endRow(ofHunk: s.hunks[n]) ?? header)
    }

    /// The hunk's header near the top (under the floating file header);
    /// a hunk already fully in view stays put.
    private func reveal(_ header: Int, end: Int) {
        let top = table.rect(ofRow: header), bottom = table.rect(ofRow: end)
        let visible = contentView.bounds
        guard visible.height > 0 else { return } // not laid out yet: layout() reveals it
        let margin = ReviewLook.fileHeaderHeight + DS.Spacing.s
        if top.minY >= visible.minY + margin && bottom.maxY <= visible.maxY { return }
        let y = max(0, min(top.minY - margin, table.bounds.height - visible.height))
        contentView.scroll(to: NSPoint(x: 0, y: y))
        reflectScrolledClipView(contentView)
        updateSticky()
    }

    override func layout() {
        super.layout()
        let w = contentSize.width
        table.tableColumns.first?.width = w
        if abs(w - lastWidth) > 0.5 {
            lastWidth = w
            // Only the measured rows change height with the width.
            var idx = IndexSet()
            for (i, r) in rows.enumerated() where !r.fixedHeight { idx.insert(i) }
            if !idx.isEmpty { table.noteHeightOfRows(withIndexesChanged: idx) }
        }
        let h = contentSize.height
        if abs(h - lastHeight) > 0.5 {
            // A new size (first layout, the note editor, the window):
            // the focused hunk stays in view.
            lastHeight = h
            if let f = session?.focused { revealHunk(f) }
        }
        updateSticky()
    }

    // MARK: Data

    func numberOfRows(in tableView: NSTableView) -> Int { rows.count }

    func tableView(_ tableView: NSTableView, heightOfRow row: Int) -> CGFloat {
        guard rows.indices.contains(row) else { return ReviewLook.codeLineHeight }
        switch rows[row] {
        case .file: return ReviewLook.fileHeaderHeight
        case .folded: return ReviewLook.foldedHeight
        case .hunk: return ReviewLook.hunkHeaderHeight
        case .line: return ReviewLook.codeLineHeight
        case .hunkEnd:
            guard let s = session, let o = s.stream?.hunkOrdinal(atRow: row), o == s.focused, s.provenanceAvailable else { return ReviewLook.hunkGap }
            return ReviewLook.provenanceHeight
        case .note(let a):
            let w = max(120, (tableView.tableColumns.first?.width ?? 600))
            if let h = noteHeights[a], abs(h.width - w) < 0.5 { return h.height }
            let h = ReviewNoteCell.height(session?.note(a) ?? "", width: w)
            noteHeights[a] = (w, h)
            return h
        }
    }

    func tableView(_ tableView: NSTableView, rowViewForRow row: Int) -> NSTableRowView? {
        let id = NSUserInterfaceItemIdentifier("reviewRowView")
        let v = tableView.makeView(withIdentifier: id, owner: nil) as? ReviewTableRowView ?? ReviewTableRowView()
        v.identifier = id
        return v
    }

    func tableView(_ tableView: NSTableView, viewFor tableColumn: NSTableColumn?, row: Int) -> NSView? {
        guard rows.indices.contains(row) else { return nil }
        let r = rows[row]
        if case .note(let a) = r {
            let v = tableView.makeView(withIdentifier: ReviewNoteCell.id, owner: nil) as? ReviewNoteCell ?? ReviewNoteCell()
            v.identifier = ReviewNoteCell.id
            v.show(anchor: a, text: session?.note(a) ?? "")
            return v
        }
        let v = tableView.makeView(withIdentifier: ReviewRowCell.id, owner: nil) as? ReviewRowCell ?? { cellsMade += 1; return ReviewRowCell() }()
        v.identifier = ReviewRowCell.id
        v.show(row: row, kind: r, session: session)
        return v
    }

    @objc private func clicked() {
        let r = table.clickedRow
        guard r >= 0 else { return }
        session?.clicked(row: r)
    }

    @objc private func doubleClicked() {
        let r = table.clickedRow
        guard r >= 0, rows.indices.contains(r) else { return }
        switch rows[r] {
        case .folded, .file: session?.toggleFold(row: r)
        case .line: session?.startNote()
        default: break
        }
    }
}

/// The stream's table: no first responder of its own (the sheet routes
/// keys), no type-select.
final class ReviewTable: NSTableView {
    override var acceptsFirstResponder: Bool { false }
}

/// Rows draw their own backgrounds.
final class ReviewTableRowView: NSTableRowView {
    override func drawBackground(in dirtyRect: NSRect) {}
    override func drawSelection(in dirtyRect: NSRect) {}
    override var isOpaque: Bool { false }
}

/// A file header, a folded file, a hunk header, a code line or the line
/// under a hunk, drawn directly (code: one CTLine, words as rects behind
/// it). Reads the session at draw time, so focus and marks need only a
/// redraw.
@MainActor
final class ReviewRowCell: NSView {
    static let id = NSUserInterfaceItemIdentifier("reviewRow")
    private var row = 0
    private var kind: ReviewRow = .file(0)
    private weak var session: ReviewSession?
    /// The code line, shaped once per show.
    private var ctLine: CTLine?
    private var words: [NSRange] = []

    override var isFlipped: Bool { true }
    override var isOpaque: Bool { false }

    func show(row: Int, kind: ReviewRow, session: ReviewSession?) {
        self.row = row
        self.kind = kind
        self.session = session
        ctLine = nil
        words = []
        if case .line(let f, let h, let l) = kind, let s = session?.stream {
            let line = s.files[f].hunks[h].lines[l]
            let (text, w) = ReviewLineText.display(line.text, words: line.words)
            let attr = NSAttributedString(string: text, attributes: [.font: ReviewLook.codeFont, .foregroundColor: Theme.ns(.text)])
            ctLine = CTLineCreateWithAttributedString(attr)
            words = w
        }
        needsDisplay = true
        setAccessibilityElement(true)
        setAccessibilityRole(.staticText)
        setAccessibilityLabel(accessibilityText)
    }

    private var accessibilityText: String {
        guard let s = session?.stream else { return "" }
        switch kind {
        case .file(let f): return "File \(s.files[f].path)"
        case .folded(let f): return "\(s.files[f].path), folded"
        case .hunk(let f, let h): return s.files[f].hunks[h].header
        case .line(let f, let h, let l):
            let line = s.files[f].hunks[h].lines[l]
            return "\(line.kind == .added ? "added" : line.kind == .removed ? "removed" : "") \(line.text)"
        case .hunkEnd: return session?.focusedProvenance.map(ReviewText.provenance) ?? ""
        case .note: return ""
        }
    }

    override func draw(_ dirtyRect: NSRect) {
        guard let session, let s = session.stream else { return }
        let ordinal = s.hunkOrdinal(atRow: row)
        let focused = ordinal != nil && ordinal == session.focused
        let rejected = ordinal.map { session.isRejected($0) } ?? false
        let b = bounds
        switch kind {
        case .file(let f): drawFile(s.files[f], in: b)
        case .folded(let f): drawFolded(s.files[f], in: b)
        case .hunk(let f, let h): drawHunkHeader(s.files[f].hunks[h], in: b, focused: focused, rejected: rejected, seen: isSeen(s, ordinal))
        case .line(let f, let h, let l): drawLine(s.files[f].hunks[h].lines[l], in: b, focused: focused, rejected: rejected)
        case .hunkEnd:
            if focused { drawProvenance(in: b, session: session) }
        case .note: break
        }
    }

    private func isSeen(_ s: ReviewStream, _ o: Int?) -> Bool {
        guard let o, let session else { return false }
        return session.currentAttention.seen.contains(s.hunks[o].key)
    }

    private static func text(_ s: String, _ font: NSFont, _ color: NSColor) -> NSAttributedString {
        NSAttributedString(string: s, attributes: [.font: font, .foregroundColor: color])
    }

    private func drawFile(_ f: ReviewFile, in b: NSRect) {
        PanelView.sheetColor.setFill()
        b.fill()
        Theme.ns(.line, alpha: ReviewLook.fileFillAlpha).setFill()
        b.fill()
        Theme.ns(.line).setFill()
        NSRect(x: 0, y: b.maxY - 1, width: b.width, height: 1).fill()
        var x = DS.Spacing.l
        let midY = b.midY
        let status = Self.text(f.status.rawValue, DS.nsMonoFont(.chrome, .semibold), Theme.ns(ReviewLook.tone(f.status)))
        status.draw(at: NSPoint(x: x, y: midY - status.size().height / 2))
        x += status.size().width + DS.Spacing.m
        let right = NSMutableAttributedString()
        if f.risk != .low { right.append(Self.text("\(f.risk.rawValue) risk   ", .ds(.meta, .medium), Theme.ns(ReviewLook.tone(f.risk)))) }
        if f.binary { right.append(Self.text("binary   ", .ds(.meta), Theme.ns(.dim))) }
        right.append(Self.text("+\(f.added)", .ds(.meta), Theme.ns(.done)))
        right.append(Self.text(" −\(f.removed)", .ds(.meta), Theme.ns(.error)))
        let rs = right.size()
        right.draw(at: NSPoint(x: b.maxX - DS.Spacing.l - rs.width, y: midY - rs.height / 2))
        let path = NSMutableAttributedString(attributedString: Self.text(f.path, .ds(.body, .medium), Theme.ns(.text)))
        if let o = f.oldPath, o != f.path { path.append(Self.text("  ← \(o)", .ds(.chrome), Theme.ns(.dim))) }
        let avail = max(0, b.maxX - DS.Spacing.l - rs.width - DS.Spacing.l - x)
        let ps = path.size()
        path.draw(with: NSRect(x: x, y: midY - ps.height / 2, width: avail, height: ps.height),
                  options: [.usesLineFragmentOrigin, .truncatesLastVisibleLine], context: nil)
    }

    private func drawFolded(_ f: ReviewFile, in b: NSRect) {
        let why = f.binary ? "Binary" : f.generated ? "Generated" : "Formatting only"
        let n = f.hunks.count
        let s = Self.text("\(why) · \(n) hunk\(n == 1 ? "" : "s") folded · ⏎ or double-click to show", .ds(.chrome), Theme.ns(.dim))
        s.draw(at: NSPoint(x: ReviewLook.numberWidth * 2 + ReviewLook.signWidth, y: b.midY - s.size().height / 2))
    }

    private func drawHunkHeader(_ h: ReviewHunk, in b: NSRect, focused: Bool, rejected: Bool, seen: Bool) {
        Theme.ns(.text, alpha: ReviewLook.gutterAlpha).setFill()
        b.fill()
        if focused { drawFocusBar(b) }
        var x = ReviewLook.numberWidth * 2 + ReviewLook.signWidth
        let head = Self.text(h.header, DS.nsMonoFont(.meta), Theme.ns(.dim))
        head.draw(at: NSPoint(x: x, y: b.midY - head.size().height / 2))
        x += head.size().width + DS.Spacing.m
        var tags: [(String, Theme.Token)] = []
        if rejected { tags.append(("rejected", .error)) } else if seen { tags.append(("seen ✓", .done)) }
        if h.formattingOnly { tags.append(("formatting only", .dim)) }
        if h.moved { tags.append(("moved", .question)) }
        for (t, tone) in tags {
            let s = Self.text(t, .ds(.meta, .medium), Theme.ns(tone))
            s.draw(at: NSPoint(x: x, y: b.midY - s.size().height / 2))
            x += s.size().width + DS.Spacing.m
        }
    }

    private func drawLine(_ l: ReviewLine, in b: NSRect, focused: Bool, rejected: Bool) {
        guard let ctx = NSGraphicsContext.current?.cgContext else { return }
        if rejected { ctx.setAlpha(ReviewLook.rejectedAlpha) }
        defer { ctx.setAlpha(1) }
        let nw = ReviewLook.numberWidth
        switch l.kind {
        case .added: Theme.ns(.done, alpha: ReviewLook.addedAlpha).setFill(); b.fill()
        case .removed: Theme.ns(.error, alpha: ReviewLook.removedAlpha).setFill(); b.fill()
        case .context: break
        }
        Theme.ns(.text, alpha: ReviewLook.gutterAlpha).setFill()
        NSRect(x: 0, y: 0, width: nw * 2, height: b.height).fill()
        if focused { drawFocusBar(b) }
        let font = ReviewLook.codeFont
        let baseline = ((b.height - (font.ascender - font.descender)) / 2 + font.ascender).rounded()
        // Line numbers: right-aligned in their columns.
        let numColor = Theme.ns(.dim2)
        for (i, n) in [l.old, l.new].enumerated() {
            guard let n else { continue }
            let s = Self.text("\(n)", font, numColor)
            s.draw(at: NSPoint(x: nw * CGFloat(i + 1) - DS.Spacing.s - s.size().width, y: baseline - font.ascender))
        }
        let sign = l.kind == .added ? "+" : l.kind == .removed ? "−" : ""
        if !sign.isEmpty {
            let s = Self.text(sign, font, Theme.ns(l.kind == .added ? .done : .error))
            s.draw(at: NSPoint(x: nw * 2 + DS.Spacing.xs, y: baseline - font.ascender))
        }
        guard let line = ctLine else { return }
        let x0 = nw * 2 + ReviewLook.signWidth
        // Changed words behind the text.
        if !words.isEmpty, l.kind != .context {
            Theme.ns(l.kind == .added ? .done : .error, alpha: ReviewLook.wordAlpha).setFill()
            for w in words {
                let a = CTLineGetOffsetForStringIndex(line, w.location, nil)
                let e = CTLineGetOffsetForStringIndex(line, w.location + w.length, nil)
                NSBezierPath(roundedRect: NSRect(x: x0 + a, y: DS.Spacing.xxs / 2, width: max(1, e - a), height: b.height - DS.Spacing.xxs),
                             xRadius: DS.Spacing.xxs, yRadius: DS.Spacing.xxs).fill()
            }
        }
        ctx.saveGState()
        ctx.textMatrix = CGAffineTransform(scaleX: 1, y: -1)
        ctx.textPosition = CGPoint(x: x0, y: baseline)
        CTLineDraw(line, ctx)
        ctx.restoreGState()
    }

    private func drawProvenance(in b: NSRect, session: ReviewSession) {
        guard session.provenanceAvailable else { return }
        let x = ReviewLook.numberWidth * 2 + ReviewLook.signWidth
        let text = session.focusedProvenance.map(ReviewText.provenance) ?? "Why here? …"
        let s = NSMutableAttributedString(attributedString: Self.text(text, .ds(.chrome), Theme.ns(.text2)))
        s.append(Self.text("   ⌥↩ open the conversation", .ds(.meta), Theme.ns(.dim)))
        let h = s.size().height
        s.draw(with: NSRect(x: x, y: (b.height - h) / 2, width: max(0, b.width - x - DS.Spacing.l), height: h),
               options: [.usesLineFragmentOrigin, .truncatesLastVisibleLine], context: nil)
    }

    private func drawFocusBar(_ b: NSRect) {
        Theme.ns(.working).setFill()
        NSRect(x: 0, y: 0, width: ReviewLook.focusBar, height: b.height).fill()
    }
}

/// A note under its line: Question-toned card, the anchor, the text
/// (wrapped; the only text the stream measures).
@MainActor
final class ReviewNoteCell: NSView {
    static let id = NSUserInterfaceItemIdentifier("reviewNote")
    private var anchor = ReviewNoteAnchor(path: "")
    private var text = ""

    override var isFlipped: Bool { true }

    static var inset: CGFloat { ReviewLook.numberWidth * 2 + ReviewLook.signWidth }
    static let pad = DS.Spacing.m
    private static let labelFont = NSFont.ds(.meta, .medium)
    private static let bodyFont = NSFont.ds(.body)

    func show(anchor: ReviewNoteAnchor, text: String) {
        self.anchor = anchor
        self.text = text
        needsDisplay = true
        setAccessibilityElement(true)
        setAccessibilityRole(.staticText)
        setAccessibilityLabel("Note on \(ReviewNotes.where_(anchor)): \(text)")
    }

    static func height(_ text: String, width: CGFloat) -> CGFloat {
        let w = max(40, width - inset - DS.Spacing.l - 2 * pad)
        let body = (text as NSString).boundingRect(with: NSSize(width: w, height: .greatestFiniteMagnitude), options: [.usesLineFragmentOrigin],
                                                   attributes: [.font: bodyFont]).height.rounded(.up)
        let label = (labelFont.ascender - labelFont.descender).rounded(.up)
        return label + DS.Spacing.xs + body + 2 * pad + 2 * DS.Spacing.xs
    }

    override func draw(_ dirtyRect: NSRect) {
        let card = NSRect(x: Self.inset, y: DS.Spacing.xs, width: max(0, bounds.width - Self.inset - DS.Spacing.l), height: bounds.height - 2 * DS.Spacing.xs)
        Theme.ns(.question, alpha: ReviewLook.noteAlpha).setFill()
        NSBezierPath(roundedRect: card, xRadius: DS.Radius.control, yRadius: DS.Radius.control).fill()
        Theme.ns(.question).setFill()
        NSRect(x: card.minX, y: card.minY + DS.Spacing.xs, width: ReviewLook.focusBar, height: card.height - 2 * DS.Spacing.xs).fill()
        let label = NSAttributedString(string: "Note · \(ReviewNotes.where_(anchor))", attributes: [.font: Self.labelFont, .foregroundColor: Theme.ns(.question)])
        label.draw(at: NSPoint(x: card.minX + Self.pad, y: card.minY + Self.pad))
        let lh = (Self.labelFont.ascender - Self.labelFont.descender).rounded(.up)
        let body = NSAttributedString(string: text, attributes: [.font: Self.bodyFont, .foregroundColor: Theme.ns(.text)])
        body.draw(with: NSRect(x: card.minX + Self.pad, y: card.minY + Self.pad + lh + DS.Spacing.xs, width: card.width - 2 * Self.pad,
                               height: card.height - lh - 2 * Self.pad), options: [.usesLineFragmentOrigin], context: nil)
    }
}
