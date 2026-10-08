import AppKit
import HesperCore
import SwiftUI

/// Token chip colors (the composer's inline chips and the chip row).
enum TokenStyle {
    static func token(_ k: TokenKind) -> Theme.Token {
        switch k {
        case .project: return .working
        case .machine: return .horizon
        case .profile: return .done
        case .branch: return .question
        }
    }
    static func bg(_ k: TokenKind) -> NSColor { Theme.ns(token(k), alpha: 0.2) }
    static func fg(_ k: TokenKind) -> String { token(k).hex }
}

/// Draws background-colored runs as rounded chips.
final class TokenLayoutManager: NSLayoutManager {
    override func fillBackgroundRectArray(_ rectArray: UnsafePointer<NSRect>, count rectCount: Int, forCharacterRange charRange: NSRange, color: NSColor) {
        for i in 0..<rectCount {
            let r = rectArray[i].insetBy(dx: -DS.Spacing.xxs, dy: DS.Spacing.xxs / 4)
            NSBezierPath(roundedRect: r, xRadius: DS.Radius.kbd, yRadius: DS.Radius.kbd).fill()
        }
    }
}

/// The composer's editor, the prompt that comes first: a plain NSTextView
/// (every text editing key) with a block caret, that colors tokens as
/// chips while typing, shows the placeholder, takes dropped and pasted
/// files/screenshots, and hands the composer's keys (completion ↑↓⏎⇥esc,
/// esc, ⌥↩) to the composer.
final class ComposerTextView: NSTextView {
    weak var composer: ComposerModel?
    var placeholder = "Describe the task…   @ machine · # folder · / tool · ~ branch · drop files"
    /// esc / ⌥↩ and other composer commands; true: handled.
    var onCommand: ((AppCommand) -> Bool)?
    var onFocus: (() -> Void)?
    private var highlighting = false

    /// The prompt reads larger than the chrome around it.
    static let bodyFont = NSFont.ds(.panelTitle)
    static let tokenFont = DS.nsMonoFont(.body, .medium)
    /// The block caret: one cell of the prompt's font, the accent at
    /// `caretAlpha` over the character under it.
    static let caretWidth = ceil(("0" as NSString).size(withAttributes: [.font: bodyFont]).width)
    static let caretAlpha: CGFloat = 0.55

    static func make() -> ComposerTextView {
        let storage = NSTextStorage()
        let lm = TokenLayoutManager()
        storage.addLayoutManager(lm)
        let container = NSTextContainer(size: NSSize(width: 100, height: CGFloat.greatestFiniteMagnitude))
        container.widthTracksTextView = true
        lm.addTextContainer(container)
        let tv = ComposerTextView(frame: .zero, textContainer: container)
        tv.isRichText = false
        tv.importsGraphics = false
        tv.allowsUndo = true
        tv.isAutomaticQuoteSubstitutionEnabled = false
        tv.isAutomaticDashSubstitutionEnabled = false
        tv.isAutomaticTextReplacementEnabled = false
        tv.isAutomaticSpellingCorrectionEnabled = false
        tv.isContinuousSpellCheckingEnabled = false
        tv.smartInsertDeleteEnabled = false
        tv.drawsBackground = false
        tv.font = bodyFont
        tv.textColor = Theme.ns(.text)
        tv.insertionPointColor = Theme.ns(.working)
        tv.selectedTextAttributes = [.backgroundColor: Theme.ns(.working, alpha: 0.3)]
        tv.textContainerInset = NSSize(width: 0, height: DS.Spacing.xxs)
        tv.isVerticallyResizable = true
        tv.isHorizontallyResizable = false
        tv.autoresizingMask = [.width]
        tv.minSize = NSSize(width: 0, height: 0)
        tv.maxSize = NSSize(width: CGFloat.greatestFiniteMagnitude, height: CGFloat.greatestFiniteMagnitude)
        tv.typingAttributes = baseAttributes
        tv.registerForDraggedTypes(TerminalDrop.types)
        tv.setAccessibilityIdentifier("composer.text")
        return tv
    }

    static var baseAttributes: [NSAttributedString.Key: Any] {
        let p = NSMutableParagraphStyle()
        p.lineSpacing = DS.Spacing.xs
        return [.font: bodyFont, .foregroundColor: Theme.ns(.text), .paragraphStyle: p]
    }

    override func becomeFirstResponder() -> Bool {
        let ok = super.becomeFirstResponder()
        if ok { onFocus?() }
        return ok
    }

    // MARK: Text

    /// Shows `text` (a draft restored or changed elsewhere) without
    /// touching the undo stack when it is the same.
    func show(_ text: String) {
        guard string != text else { return }
        string = text
        highlight()
    }

    /// Replaces the text with `text` as one undoable edit (only the part
    /// that changed), the caret at `caret`.
    func replaceAll(with text: String, caret: Int) {
        let old = string as NSString, new = text as NSString
        var p = 0
        while p < old.length && p < new.length && old.character(at: p) == new.character(at: p) { p += 1 }
        var s = 0
        while s < old.length - p && s < new.length - p && old.character(at: old.length - 1 - s) == new.character(at: new.length - 1 - s) { s += 1 }
        let range = NSRange(location: p, length: old.length - p - s)
        let repl = new.substring(with: NSRange(location: p, length: new.length - p - s))
        if shouldChangeText(in: range, replacementString: repl) {
            textStorage?.replaceCharacters(in: range, with: NSAttributedString(string: repl, attributes: Self.baseAttributes))
            didChangeText()
        }
        setSelectedRange(NSRange(location: min(caret, new.length), length: 0))
    }

    override func didChangeText() {
        super.didChangeText()
        highlight()
        composer?.textChanged(string, caret: selectedRange().location)
        needsDisplay = true
    }

    override func setSelectedRanges(_ ranges: [NSValue], affinity: NSSelectionAffinity, stillSelecting: Bool) {
        super.setSelectedRanges(ranges, affinity: affinity, stillSelecting: stillSelecting)
        if !stillSelecting, !highlighting { composer?.caretMoved(selectedRange().location) }
    }

    /// Colors resolved tokens (and the one being typed) as chips.
    func highlight() {
        guard let ts = textStorage, !hasMarkedText() else { return }
        highlighting = true
        defer { highlighting = false }
        let full = NSRange(location: 0, length: ts.length)
        let ctx = composer?.app.composerContext ?? ComposerContext()
        let resolved = ComposerParser.resolve(string, context: ctx)
        let caret = selectedRange().location
        let typing = ComposerParser.token(at: caret, in: string)
        ts.beginEditing()
        ts.setAttributes(Self.baseAttributes, range: full)
        for r in resolved {
            let range = NSRange(location: r.token.location, length: r.token.length)
            ts.addAttributes([.backgroundColor: TokenStyle.bg(r.token.kind), .foregroundColor: Theme.ns(TokenStyle.fg(r.token.kind)),
                              .font: Self.tokenFont], range: range)
        }
        if let t = typing, !resolved.contains(where: { $0.token == t }) {
            ts.addAttributes([.foregroundColor: Theme.ns(TokenStyle.fg(t.kind)), .font: Self.tokenFont],
                             range: NSRange(location: t.location, length: t.length))
        }
        ts.endEditing()
        typingAttributes = Self.baseAttributes
    }

    override func draw(_ dirtyRect: NSRect) {
        super.draw(dirtyRect)
        guard string.isEmpty else { return }
        let attrs: [NSAttributedString.Key: Any] = [.font: Self.bodyFont, .foregroundColor: Theme.ns(.dim)]
        let x = textContainerInset.width + (textContainer?.lineFragmentPadding ?? 5)
        (placeholder as NSString).draw(with: NSRect(x: x, y: textContainerInset.height, width: bounds.width - 2 * x, height: bounds.height),
                                      options: [.usesLineFragmentOrigin, .truncatesLastVisibleLine], attributes: attrs)
    }

    // MARK: Block caret

    override func drawInsertionPoint(in rect: NSRect, color: NSColor, turnedOn flag: Bool) {
        var r = rect
        r.size.width = Self.caretWidth
        if flag {
            color.withAlphaComponent(Self.caretAlpha).setFill()
            r.fill(using: .sourceOver)
        } else {
            setNeedsDisplay(r, avoidAdditionalLayout: true)
        }
    }

    /// The caret's area is the block, not the system's thin line.
    override func setNeedsDisplay(_ rect: NSRect, avoidAdditionalLayout flag: Bool) {
        var r = rect
        r.size.width += Self.caretWidth
        super.setNeedsDisplay(r, avoidAdditionalLayout: flag)
    }

    // MARK: Keys

    override func keyDown(with event: NSEvent) {
        let chord = KeyChord(event: event)
        if let c = composer, c.completion != nil {
            let act = OverlayKeys.route(chord)
            switch act {
            case .up, .down, .activate, .actOn, .close:
                if c.completionKey(act) { return }
            default: break
            }
        }
        let cmd = KeyRouter.route(chord, mode: .compose, selectedState: nil)
        if cmd == .startDraftAndNew || cmd == .leaveDraft, onCommand?(cmd) == true { return }
        super.keyDown(with: event)
    }

    /// The caret (or the token being completed) in `view`'s coordinates.
    func caretRect(at location: Int? = nil, in view: NSView) -> CGRect? {
        guard let window, let lm = layoutManager, let tc = textContainer else { return nil }
        let loc = min(location ?? selectedRange().location, (string as NSString).length)
        let glyph = lm.glyphIndexForCharacter(at: loc)
        var rect: NSRect
        if (string as NSString).length == 0 || glyph >= lm.numberOfGlyphs {
            rect = lm.extraLineFragmentRect.isEmpty ? NSRect(x: 0, y: 0, width: 1, height: 18) : lm.extraLineFragmentRect
            if let last = lm.numberOfGlyphs > 0 ? lm.lineFragmentUsedRect(forGlyphAt: lm.numberOfGlyphs - 1, effectiveRange: nil) : nil,
               lm.extraLineFragmentRect.isEmpty {
                rect = NSRect(x: last.maxX, y: last.minY, width: 1, height: last.height)
            }
        } else {
            rect = lm.boundingRect(forGlyphRange: NSRange(location: glyph, length: 1), in: tc)
            rect.size.width = 1
        }
        rect.origin.x += textContainerOrigin.x
        rect.origin.y += textContainerOrigin.y
        _ = window
        return convert(rect, to: view)
    }

    // MARK: Files and screenshots

    override func performDragOperation(_ sender: any NSDraggingInfo) -> Bool {
        if insertAttachments(from: sender.draggingPasteboard, at: characterIndexForInsertion(at: convert(sender.draggingLocation, from: nil))) { return true }
        return super.performDragOperation(sender)
    }

    override func paste(_ sender: Any?) {
        let pb = NSPasteboard.general
        if pb.string(forType: .string) == nil, insertAttachments(from: pb, at: selectedRange().location) { return }
        super.paste(sender)
    }

    override var readablePasteboardTypes: [NSPasteboard.PasteboardType] {
        super.readablePasteboardTypes + [.png, .tiff]
    }

    /// Files: their paths; images without a file (and file promises):
    /// saved, then the path — the same reading as a drop on a terminal
    /// (DropReader). Text and links are left to the text view.
    @discardableResult
    func insertAttachments(from pb: NSPasteboard, at index: Int) -> Bool {
        guard let composer else { return false }
        switch DropClassifier.classify(pb.types?.map(\.rawValue) ?? []) {
        case .files, .filePromise, .imageData: break
        default: return false
        }
        DropReader.read(pb, saveDir: composer.app.attachmentsDir(for: composer.id)) { [weak self] payloads, error in
            guard let self, let composer = self.composer else { return }
            if let error { composer.error = error }
            var paths: [String] = []
            for p in payloads {
                switch p {
                case .file(let path, _): paths.append(path)
                case .image(let data, let name): if let s = composer.saveImage(data, name: name) { paths.append(s) }
                case .text: break
                }
            }
            if !paths.isEmpty { self.insertPaths(paths, composer: composer, at: index) }
        }
        return true
    }

    private func insertPaths(_ paths: [String], composer: ComposerModel, at index: Int) {
        let text = composer.attach(paths: paths)
        let ns = string as NSString
        let i = min(max(0, index), ns.length)
        let before = i > 0 ? ns.substring(with: NSRange(location: i - 1, length: 1)) : " "
        let insert = (before == " " || before == "\n" ? "" : " ") + text + " "
        setSelectedRange(NSRange(location: i, length: 0))
        insertText(insert, replacementRange: NSRange(location: i, length: 0))
        window?.makeFirstResponder(self)
    }
}

/// The editor in a scroll view (long tasks scroll inside the tile).
final class ComposerEditor: NSScrollView {
    let textView = ComposerTextView.make()

    override init(frame: NSRect) {
        super.init(frame: frame)
        drawsBackground = false
        hasVerticalScroller = true
        autohidesScrollers = true
        scrollerStyle = .overlay
        borderType = .noBorder
        documentView = textView
        contentView.drawsBackground = false
    }

    @available(*, unavailable)
    required init?(coder: NSCoder) { fatalError() }
}

/// Chip frames, reported by the chip row (popovers point at them).
struct ChipFramesKey: PreferenceKey {
    nonisolated(unsafe) static var defaultValue: [TokenKind: CGRect] = [:]
    static func reduce(value: inout [TokenKind: CGRect], nextValue: () -> [TokenKind: CGRect]) { value.merge(nextValue()) { $1 } }
}

/// Chips left to right, wrapping to the next line when the row is full
/// (narrow tiles): nothing scrolls off. An item wider than the row gets
/// the row's width (its text truncates).
struct ChipFlow: Layout {
    var spacing: CGFloat = DS.Spacing.s
    var lineSpacing: CGFloat = DS.Spacing.xs

    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
        let rows = arrange(width: proposal.width ?? .infinity, subviews: subviews)
        let w = rows.flatMap { $0 }.map { $0.frame.maxX }.max() ?? 0
        let h = rows.last?.map { $0.frame.maxY }.max() ?? 0
        if let pw = proposal.width, pw.isFinite { return CGSize(width: pw, height: h) }
        return CGSize(width: w, height: h)
    }

    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) {
        for row in arrange(width: bounds.width, subviews: subviews) {
            for item in row {
                subviews[item.index].place(at: CGPoint(x: bounds.minX + item.frame.minX, y: bounds.minY + item.frame.minY), anchor: .topLeading,
                                           proposal: ProposedViewSize(width: item.frame.width, height: item.frame.height))
            }
        }
    }

    private struct Item { var index: Int; var frame: CGRect }

    private func arrange(width: CGFloat, subviews: Subviews) -> [[Item]] {
        var rows: [[Item]] = []
        var row: [Item] = []
        var x: CGFloat = 0, y: CGFloat = 0, lineH: CGFloat = 0
        for (i, s) in subviews.enumerated() {
            let ideal = s.sizeThatFits(.unspecified)
            var size = ideal
            if size.width > width { size = s.sizeThatFits(ProposedViewSize(width: width, height: nil)); size.width = min(size.width, width) }
            if !row.isEmpty && x + spacing + size.width > width {
                rows.append(row)
                row = []
                y += lineH + lineSpacing
                x = 0
                lineH = 0
            }
            let originX = row.isEmpty ? 0 : x + spacing
            row.append(Item(index: i, frame: CGRect(x: originX, y: y, width: size.width, height: size.height)))
            x = originX + size.width
            lineH = max(lineH, size.height)
        }
        if !row.isEmpty { rows.append(row) }
        return rows
    }
}

/// The prompt's chips, under the task: machine, folder, tool (profile),
/// worktree, and the project the folder belongs to. Pills that fill
/// themselves from the tokens typed in the task (@ machine, # folder,
/// / tool, ~ branch); each opens its list, its sigil is its key. A problem
/// with the folder is said right under the chips, with the one-click way
/// out (never hesperd's raw text, no modal, no toast).
struct DraftChips: View {
    var composer: ComposerModel
    var onChip: (TokenKind) -> Void
    var compact = false

    var body: some View {
        let r = composer.resolution
        let app = composer.app
        let m = app.machine(r.machine)
        VStack(alignment: .leading, spacing: DS.Spacing.xs) {
            // Wraps (never scrolls): the machine chip is always the first,
            // always visible, with the machine's name.
            ChipFlow {
                chip(.machine, on: true, title: ComposerCompletion.machineChip(m, short: r.machine, local: r.machine == app.localMachine),
                     mono: true, detail: nil, warn: m.map { !$0.online } ?? false)
                if composer.startsScratch {
                    // No folder: a new scratch (scratch contract); # replaces it.
                    chip(.project, on: true, title: ScratchDraft.chipTitle, mono: false, detail: ScratchDraft.detail(task: r.task), warn: false)
                } else {
                    chip(.project, on: r.project != nil || r.cloneURL != nil, title: folderTitle(r), mono: r.project != nil && r.cloneURL == nil,
                         detail: nil, warn: (r.project == nil && r.cloneURL == nil) || composer.folderNote != nil || composer.missingFolder != nil)
                }
                chip(.profile, on: r.profile != nil, title: r.profile.map { Theme.kindLabel(app.composerContext.profiles[$0] ?? $0) } ?? "Tool",
                     mono: false, detail: r.profile, warn: false)
                if !compact || r.worktree {
                    chip(.branch, on: r.worktree, title: r.worktree ? "new worktree" : "no worktree", mono: false,
                         detail: r.branch.isEmpty || !r.worktree ? nil : r.branch, warn: false)
                }
                if r.project != nil || composer.projectLocked { projectPill(r) }
            }
            note
        }
        .frame(maxWidth: .infinity, alignment: .topLeading)
        .coordinateSpace(name: "chips")
        // Live: whenever machine or folder change, ask the machine.
        .task(id: composer.targetKey) { await composer.checkTarget() }
    }

    private func folderTitle(_ r: ComposerResolution) -> String {
        if let url = r.cloneURL { return "clone \(ComposerCompletion.repoName(url))" }
        guard let p = r.project else { return "Choose folder" }
        return compact ? (p as NSString).lastPathComponent : ComposerCompletion.abbreviate(p)
    }

    /// The project the folder belongs to (a band on the wall), or "no
    /// project". Says where the agent will show up; not a choice — except
    /// in a project window, where it carries a lock (the window's project):
    /// a click unlocks it and opens the folder list.
    @ViewBuilder private func projectPill(_ r: ComposerResolution) -> some View {
        if composer.projectLocked, let band = composer.draft.band { lockedPill(band) } else { folderProjectPill(r) }
    }

    private func lockedPill(_ id: String) -> some View {
        let app = composer.app
        let name = app.catalog.name(project: id)
        return Button { composer.unlockProject(); onChip(.project) } label: {
            HStack(spacing: DS.Spacing.s) {
                StateMark(color: Theme.color(app.catalog.colorHex(project: id)))
                Text(name).font(.ds(.chrome, .medium)).lineLimit(1).foregroundStyle(Theme.fg)
                Image(systemName: "lock.fill").font(.ds(.meta)).foregroundStyle(Theme.dim)
            }
            .padding(.horizontal, DS.Spacing.m).frame(height: Pill.height)
            .background(DS.Radius.shape(DS.Radius.control).fill(Theme.chipBG))
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .fixedSize()
        .help("This window's project: the agent starts here. Click to choose another folder")
        .accessibilityLabel("Project \(name), locked to this window")
        .accessibilityHint("Unlocks the project and opens the folder list")
        .accessibilityIdentifier("draft.chip.projectLock")
    }

    @ViewBuilder private func folderProjectPill(_ r: ComposerResolution) -> some View {
        let app = composer.app
        let id = app.folderProjectID(r.project, machine: r.machine)
        let known = id.flatMap { app.catalog.projects[$0] }
        HStack(spacing: DS.Spacing.s) {
            if let id, known != nil { StateMark(color: Theme.color(app.catalog.colorHex(project: id))) }
            Text(known.map { _ in app.catalog.name(project: id) } ?? "no project")
                .font(.ds(.chrome, .medium)).lineLimit(1)
                .foregroundStyle(known == nil ? Theme.dim : Theme.fg2)
        }
        .padding(.horizontal, DS.Spacing.m).frame(height: Pill.height)
        .overlay(DS.Radius.shape(DS.Radius.control).strokeBorder(Theme.stroke, lineWidth: 1))
        .fixedSize()
        .help(known == nil ? "This folder is in no project: the agent shows under New" : "The project this folder belongs to")
        .accessibilityElement(children: .combine)
        .accessibilityLabel(known.map { _ in "Project \(app.catalog.name(project: id))" } ?? "No project")
        .accessibilityIdentifier("draft.chip.projectName")
    }

    /// Under the chips: the folder isn't on the machine it runs on, or
    /// isn't there at all; each with its one-click fix. A folder only this
    /// Mac has: "Folder isn't on mini — Bring it to mini ⌘⏎ · Bring clean
    /// (last commit) · Run on laptop · Use mini's copy" (wraps on narrow
    /// tiles); a bring that didn't start says why here.
    @ViewBuilder private var note: some View {
        if composer.app.bringProgress(draft: composer.id) != nil {
            EmptyView() // the footer's progress line says it
        } else if let note = composer.folderNote {
            let actions = composer.folderActions
            let bring = actions.contains { if case .bring = $0 { return true } else { return false } }
            ChipFlow {
                HStack(spacing: DS.Spacing.s) {
                    Image(systemName: "exclamationmark.triangle.fill").font(.ds(.meta)).foregroundStyle(Theme.color(.question))
                    Text(composer.folderNoteText ?? note.text).font(.ds(.chrome)).foregroundStyle(Theme.color(.question)).lineLimit(2)
                        .fixedSize(horizontal: false, vertical: true)
                }
                .frame(minHeight: Pill.height)
                ForEach(Array(actions.enumerated()), id: \.element) { i, a in
                    fixButton(a.title(target: note.target, here: note.here), id: a.id, key: i == 0 && bring && !compact ? "⌘⏎" : nil) {
                        composer.perform(a)
                    }
                    .help(a.help(folder: ComposerCompletion.abbreviate(note.folder), target: note.target))
                }
            }
            .help("\(note.folder) is not on \(note.target)")
            .accessibilityElement(children: .contain)
            .accessibilityIdentifier("draft.folder.elsewhere")
        } else if let missing = composer.missingFolder {
            // Created on start (or now).
            HStack(spacing: DS.Spacing.s) {
                Image(systemName: "folder.badge.plus").font(.ds(.meta)).foregroundStyle(Theme.color(.question))
                Text("Folder doesn't exist").font(.ds(.chrome)).foregroundStyle(Theme.color(.question))
                fixButton("Create", id: "draft.folder.create") { composer.createMissingFolder() }
            }
            .fixedSize()
            .help("\(missing) is created when the agent starts")
            .accessibilityElement(children: .contain)
            .accessibilityIdentifier("draft.folder.missing")
        }
    }

    private func fixButton(_ title: String, id: String, key: String? = nil, action: @escaping () -> Void) -> some View {
        Button(action: action) {
            HStack(spacing: DS.Spacing.s) {
                Text(title).font(.ds(.chrome, .medium)).foregroundStyle(Theme.accent).lineLimit(1)
                if let key { Kbd(key) }
            }
                .padding(.horizontal, DS.Spacing.s).frame(height: Pill.height - DS.Spacing.xs)
                .background(DS.Radius.shape(DS.Radius.control).fill(Theme.selection))
                .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .fixedSize()
        .accessibilityIdentifier(id)
    }

    /// The row's height for `width` (the tile sizes the row to it).
    @MainActor static func height(_ root: AnyView, width: CGFloat, measure: NSHostingController<AnyView>) -> CGFloat {
        measure.rootView = root
        return max(Pill.height, ceil(measure.sizeThatFits(in: CGSize(width: width, height: .greatestFiniteMagnitude)).height))
    }

    /// The tallest the row gets: three lines of chips and a two-line note
    /// whose buttons wrap once.
    static var maxHeight: CGFloat { 4 * Pill.height + 4 * DS.Spacing.xs + 2 * DS.chromeMaxHeight }

    private func chip(_ kind: TokenKind, on: Bool, title: String, mono: Bool, detail: String?, warn: Bool) -> some View {
        let tint: Theme.Token = warn ? .question : TokenStyle.token(kind)
        return Button { onChip(kind) } label: {
            HStack(spacing: DS.Spacing.s) {
                Text(title).font(mono ? DS.monoFont(.chrome, .medium) : .ds(.chrome, .medium)).lineLimit(1)
                    .foregroundStyle(warn ? Theme.color(.question) : on ? Theme.fg : Theme.dim)
                if let detail, !compact { Text(detail).font(.ds(.meta)).foregroundStyle(Theme.dim).lineLimit(1) }
                if !compact { Kbd(String(kind.sigil)) }
            }
            .padding(.horizontal, DS.Spacing.m).frame(height: Pill.height)
            .background(DS.Radius.shape(DS.Radius.control).fill(on || warn ? Color(nsColor: Theme.ns(tint, alpha: 0.14)) : Theme.chipBG))
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .background(GeometryReader { g in Color.clear.preference(key: ChipFramesKey.self, value: [kind: g.frame(in: .named("chips"))]) })
        .help("\(kind.title): click, or type \(String(kind.sigil)) in the task")
        .accessibilityLabel("\(kind.title): \(title)")
        .accessibilityIdentifier("draft.chip.\(kind.rawValue)")
    }
}

/// The footer: the token keys, the start keys and the Start button.
struct DraftFooter: View {
    var composer: ComposerModel
    var quick = false
    var narrow = false
    var onStart: () -> Void

    static let height = Pill.height + DS.Spacing.xs

    var body: some View {
        let bringing = quick ? nil : composer.app.bringProgress(draft: composer.id)
        HStack(spacing: DS.Spacing.l) {
            if let b = bringing {
                BringProgressRow(progress: b.progress, changes: b.changes, target: composer.app.machineName(b.to))
            } else if let e = composer.error {
                Label(e, systemImage: "exclamationmark.triangle.fill").font(.ds(.chrome)).foregroundStyle(Theme.color(.question))
                    .lineLimit(1).accessibilityIdentifier("draft.error")
            } else {
                if !narrow {
                    Text("@ machine · # folder · / tool").font(.ds(.chrome)).foregroundStyle(Theme.dim).lineLimit(1)
                        .accessibilityIdentifier("draft.tokenHint")
                }
                hint("⌘⏎", "start")
                if !narrow && !quick { hint("⌥⏎", "+ new") }
                if !narrow { hint("esc", quick ? "close" : "keep") }
            }
            Spacer(minLength: DS.Spacing.s)
            Button(action: onStart) {
                HStack(spacing: DS.Spacing.s) {
                    if composer.busy { ProgressView().controlSize(.mini) }
                    Text(bringing != nil ? "Bringing…" : composer.busy ? "Starting…" : "Start").font(.ds(.chrome, .semibold))
                }
                .padding(.horizontal, DS.Spacing.l).frame(height: Self.height)
                .background(DS.Radius.shape(DS.Radius.control).fill(Theme.accent))
                .foregroundStyle(Theme.color(.background))
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .disabled(composer.busy || composer.needsFolder)
            .opacity(composer.needsFolder ? OverlayLook.disabledOpacity : 1)
            .help(composer.needsFolder ? "Choose a folder first (⌘⏎ opens the list)" : composer.startsScratch ? "Start in a new scratch (⌘⏎)" : "Start (⌘⏎)")
            .accessibilityIdentifier("draft.start")
        }
    }

    private func hint(_ k: String, _ what: String) -> some View {
        HStack(spacing: DS.Spacing.xs) {
            Kbd(k)
            Text(what).font(.ds(.chrome)).foregroundStyle(Theme.dim)
        }
        .fixedSize()
        .accessibilityElement(children: .combine)
    }
}

/// The draft tile's header: its starting mark, "New agent", the save state.
struct DraftHeader: View {
    var title: String
    var status: String
    var editing: Bool
    var compact = false

    var body: some View {
        HStack(spacing: DS.Spacing.m) {
            StateMark(.starting)
            Text(title).font(.ds(.body, .semibold)).foregroundStyle(editing ? Theme.accent : Theme.fg).lineLimit(1).truncationMode(.tail)
            Spacer(minLength: DS.Spacing.s)
            if !compact {
                Text(status).font(.ds(.meta)).foregroundStyle(Theme.dim).lineLimit(1)
                    .fixedSize()
                    .accessibilityIdentifier("draft.status")
            }
        }
        .padding(.horizontal, DS.Spacing.l)
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .leading)
    }
}

/// A draft on the wall, the same prompt-first composer as ⌘N's: the task
/// first, the chips under it, the keys in the footer. Typing happens here;
/// ⌘↩ turns this card into the agent's tile in place. A tile: solid
/// (flat), the selection ring in `working`.
@MainActor
final class DraftTileView: NSView {
    /// Widths below which parts fold away.
    private enum Fold {
        static let header: CGFloat = 260
        static let footer: CGFloat = 520
        static let chips: CGFloat = 640
        /// Before the first layout.
        static let assumed: CGFloat = 600
    }
    /// A draft being started, a parked one.
    private static let busyAlpha: CGFloat = 0.85
    private static let parkedAlpha: CGFloat = 0.82
    private static let ringWidth: CGFloat = 2

    private weak var model: AppModel?
    private(set) var draft: Draft
    let composer: ComposerModel
    let editor = ComposerEditor()
    private let body = FlippedView()
    private let header: NSHostingView<DraftHeader>
    private let divider = NSView()
    private let chips: NSHostingView<AnyView>
    private let footer: NSHostingView<DraftFooter>
    private let preview = NSTextField(labelWithString: "")
    private(set) var placed: WallTile?
    private(set) var onShelf = false
    private(set) var chipFrames: [TokenKind: CGRect] = [:]
    /// Measures the chip row's wrapped height (re-measured when the tile's
    /// width or the row's content changes).
    private let chipMeasure = NSHostingController(rootView: AnyView(EmptyView()))
    private var chipsMeasured: CGFloat?
    private var chipsMeasuredWidth: CGFloat = 0
    /// The chip row (tests: its frame, the machine chip inside it).
    var chipRow: NSView { chips }
    var isSelected = false { didSet { if oldValue != isSelected { updateRing() } } }
    var editing = false { didSet { if oldValue != editing { updateRing(); refresh() } } }

    override var isFlipped: Bool { true }

    init(draft: Draft, model: AppModel) {
        self.draft = draft
        self.model = model
        composer = model.composer(for: draft.id)
        header = NSHostingView(rootView: DraftHeader(title: "New agent", status: "", editing: false))
        chips = NSHostingView(rootView: AnyView(EmptyView()))
        footer = NSHostingView(rootView: DraftFooter(composer: composer, onStart: {}))
        super.init(frame: .zero)
        wantsLayer = true
        DS.Radius.apply(DS.Radius.tile, to: layer)
        layer?.masksToBounds = false
        DS.Elevation.flat.applyShadow(to: layer, radius: DS.Radius.tile)
        body.wantsLayer = true
        DS.Radius.apply(DS.Radius.tile, to: body.layer, masks: true)
        header.wantsLayer = true
        divider.wantsLayer = true
        applyTheme()
        preview.font = .ds(.chrome)
        preview.textColor = Theme.ns(.text)
        preview.lineBreakMode = .byTruncatingTail
        addSubview(body)
        body.addSubview(header)
        body.addSubview(divider)
        body.addSubview(editor)
        body.addSubview(chips)
        body.addSubview(footer)
        body.addSubview(preview)
        let tv = editor.textView
        tv.composer = composer
        tv.show(draft.text)
        tv.onCommand = { [weak model] cmd in model?.perform(cmd); return true }
        tv.onFocus = { [weak self, weak model] in
            guard let self, let model, model.editingDraftID != self.draft.id else { return }
            model.editDraft(self.draft.id)
        }
        composer.onReplaceText = { [weak tv] text, caret in tv?.replaceAll(with: text, caret: caret) }
        composer.caretRect = { [weak self] in
            guard let self, let root = self.rootView else { return nil }
            let loc = self.composer.completion?.token.location
            return self.editor.textView.caretRect(at: loc, in: root)
        }
        registerForDraggedTypes(TerminalDrop.types)
        setAccessibilityElement(true)
        setAccessibilityRole(.group)
        setAccessibilityIdentifier("draft.\(draft.id)")
        refresh()
        updateRing()
        observeChanges { [weak self] in self?.observedRefresh() }
    }

    @available(*, unavailable)
    required init?(coder: NSCoder) { fatalError() }

    private var rootView: NSView? { window?.contentView }

    func apply(_ d: Draft) {
        draft = d
        // Another app (or a restart) changed the text: show it unless we
        // are typing in it.
        if !(window?.firstResponder === editor.textView) || editor.textView.string.isEmpty { editor.textView.show(d.text) }
        refresh()
    }

    /// Reads the composer's observable state (resolution, busy, errors) so
    /// the chips and footer follow it.
    private func observedRefresh() {
        _ = composer.resolution
        _ = composer.busy
        _ = composer.error
        _ = composer.folderCheck
        _ = composer.targetFolders
        _ = composer.bringFailures
        guard let model else { return }
        _ = model.bringProgress(draft: draft.id)
        _ = model.drafts.dirty[draft.id] == nil
        refresh()
    }

    private func refresh() {
        guard let model else { return }
        let w = bounds.width > 0 ? bounds.width : Fold.assumed
        let first = ComposerResolution.firstLine(draft.text)
        let title = onShelf || (!editing && draft.parked) ? (first.isEmpty ? "Empty draft" : first) : "New agent"
        header.rootView = DraftHeader(title: title, status: model.saveLabel(draft.id), editing: editing, compact: w < Fold.header)
        let id = draft.id
        chips.rootView = AnyView(DraftChips(composer: composer, onChip: { [weak model] k in model?.openPopover(.chip(id, k)) }, compact: w < Fold.chips)
            .onPreferenceChange(ChipFramesKey.self) { [weak self] f in MainActor.assumeIsolated { self?.chipFrames = f } })
        footer.rootView = DraftFooter(composer: composer, narrow: w < Fold.footer, onStart: { [weak model] in model?.startDraft(id) })
        chipsMeasured = nil // the chip row may wrap differently
        needsLayout = true
        preview.stringValue = first.isEmpty ? "Empty draft · ⏎ to edit" : "Draft · ⏎ to edit"
        editor.textView.isEditable = !composer.busy
        alphaValue = composer.busy ? Self.busyAlpha : (draft.parked && !editing ? Self.parkedAlpha : 1)
    }

    /// A chip's frame in `view` (popovers point at it).
    func chipFrame(_ k: TokenKind, in view: NSView) -> CGRect? {
        guard let f = chipFrames[k] else { return nil }
        return chips.convert(f, to: view)
    }

    override func viewDidChangeEffectiveAppearance() {
        super.viewDidChangeEffectiveAppearance()
        applyTheme()
        updateRing()
    }

    private func applyTheme() {
        body.layer?.backgroundColor = Theme.tileBG.cg(in: self)
        header.layer?.backgroundColor = Theme.headerBG.cg(in: self)
        divider.layer?.backgroundColor = Theme.hairline.cg(in: self)
    }

    private func updateRing() { themed { updateRingInAppearance() } }

    private func updateRingInAppearance() {
        guard let layer else { return }
        // Flat: a ring, no shadow (tiles are solid; glass is for floating layers).
        if editing || isSelected {
            layer.borderColor = Theme.ns(.working, alpha: editing ? 1 : Self.busyAlpha).cgColor
            layer.borderWidth = Self.ringWidth
        } else {
            layer.borderColor = Theme.ns(.line).cgColor
            layer.borderWidth = 1
        }
    }

    func place(_ t: WallTile, animated: Bool) {
        placed = t
        let target = NSRect(x: t.card.x, y: t.card.y, width: t.card.width, height: t.card.height)
        if onShelf != t.quiet {
            onShelf = t.quiet
            refresh()
        }
        if frame.width != target.width { needsLayout = true; DispatchQueue.main.async { [weak self] in self?.refresh() } }
        if animated && !frame.isEmpty && frame != target { animator().frame = target } else if frame != target { frame = target }
        needsLayout = true
    }

    /// Takes over a frame (the card it was before a restart of the view).
    func adoptFrame(_ f: NSRect) { frame = f }

    override func layout() {
        super.layout()
        body.frame = bounds
        let w = bounds.width, h = bounds.height
        let hh = DS.tileHeader
        header.frame = NSRect(x: 0, y: 0, width: w, height: hh)
        divider.frame = NSRect(x: 0, y: hh - 1, width: w, height: 1)
        let shelf = onShelf
        editor.isHidden = shelf
        chips.isHidden = shelf
        footer.isHidden = shelf
        preview.isHidden = !shelf
        let pad = DS.Spacing.l
        if shelf {
            let lineH = ceil(preview.intrinsicContentSize.height)
            preview.frame = NSRect(x: pad, y: hh + DS.Spacing.m, width: w - 2 * pad, height: lineH)
            return
        }
        // The prompt first; the chips under it wrap on narrow tiles (two or
        // three lines, a note under them), never scroll: the machine chip
        // stays visible.
        let footerH = DraftFooter.height
        if chipsMeasuredWidth != w || chipsMeasured == nil {
            chipsMeasured = DraftChips.height(chips.rootView, width: w - 2 * pad, measure: chipMeasure)
            chipsMeasuredWidth = w
        }
        let chipsH = min(max(Pill.height, chipsMeasured ?? Pill.height), DraftChips.maxHeight)
        footer.frame = NSRect(x: pad, y: h - pad - footerH, width: w - 2 * pad, height: footerH)
        chips.frame = NSRect(x: pad, y: footer.frame.minY - DS.Spacing.m - chipsH, width: w - 2 * pad, height: chipsH)
        let editorY = hh + DS.Spacing.m
        editor.frame = NSRect(x: pad - DS.Spacing.xs, y: editorY, width: w - 2 * pad + 2 * DS.Spacing.xs,
                              height: max(ComposerTextView.bodyFont.boundingRectForFont.height, chips.frame.minY - DS.Spacing.m - editorY))
    }

    /// Like an agent's tile: a click selects the draft, a click on the
    /// selected draft (or ⏎, or typing) opens its editor.
    override func mouseDown(with event: NSEvent) {
        guard let model else { return }
        if editing && !onShelf { focusEditor(); return }
        if model.selectedID == draft.id || event.clickCount == 2 {
            model.editDraft(draft.id)
            focusEditor()
        } else {
            model.select(draft.id)
            var v = superview
            while let s = v, !(s is WallView) { v = s.superview }
            if let v { window?.makeFirstResponder(v) }
        }
    }

    func focusEditor() {
        guard !onShelf else { return }
        window?.makeFirstResponder(editor.textView)
        let tv = editor.textView
        if tv.selectedRange().location == 0 && !tv.string.isEmpty { tv.setSelectedRange(NSRange(location: (tv.string as NSString).length, length: 0)) }
    }

    // Drops anywhere on the card go into the task.
    override func draggingEntered(_ sender: any NSDraggingInfo) -> NSDragOperation { onShelf ? [] : .copy }
    override func performDragOperation(_ sender: any NSDraggingInfo) -> Bool {
        let tv = editor.textView
        let pb = sender.draggingPasteboard
        if tv.insertAttachments(from: pb, at: tv.selectedRange().location) { return true }
        // Text and links dropped on the card (not the editor): at the caret.
        guard let s = (pb.readObjects(forClasses: [NSURL.self], options: nil) as? [URL])?.first?.absoluteString ?? pb.string(forType: .string) else { return false }
        focusEditor()
        tv.insertText(s, replacementRange: tv.selectedRange())
        return true
    }
}
