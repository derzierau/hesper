import AppKit
import HesperCore
import Observation

/// The review sheet's state and what its keys do: the selected item, its
/// diff (fetched and turned into rows off the main thread), evidence,
/// the hunk cursor, what the reviewer has seen, rejected and noted, and
/// the calls (accept, reject, send back, provenance). The sheet's views
/// read it; the stream table reloads on `onStream` / `onFocus` only.
@MainActor
@Observable
final class ReviewSession {
    @ObservationIgnored let hub: ReviewHub
    /// nil: offscreen renders (fixtures, no calls).
    @ObservationIgnored let client: DaemonClient?

    private(set) var selectedID: String?
    private(set) var diffs: [String: ReviewDiff] = [:]
    @ObservationIgnored private(set) var streams: [String: ReviewStream] = [:]
    private(set) var loading: Set<String> = []
    private(set) var failures: [String: String] = [:]
    private(set) var evidence: [String: ReviewEvidence] = [:]
    /// Macs whose hesperd has no review.evidence / review.provenance.
    private(set) var evidenceOff: Set<String> = []
    private(set) var provenanceOff: Set<String> = []
    private(set) var attention: [String: ReviewAttention] = [:]
    private(set) var notes: [String: ReviewNoteBook] = [:]
    /// The focused hunk (ordinal in the stream's hunks) per item.
    private(set) var focus: [String: Int] = [:]
    /// The code row clicked last (a note goes there).
    @ObservationIgnored private var clickedRow: [String: Int] = [:]
    private(set) var unfolded: [String: Set<String>] = [:]
    /// By "<item>|<hunk key>".
    private(set) var provenance: [String: ReviewProvenance] = [:]
    /// The note being written (the editor under the stream).
    private(set) var editingNote: ReviewNoteAnchor?
    private(set) var confirm: Confirm?
    private(set) var busy: String?
    /// The item header's status line.
    private(set) var status: (text: String, error: Bool)?

    enum Confirm: Equatable {
        /// ⌘↩ with hunks nobody marked seen: ⌘↩ again accepts.
        case accept(id: String, unseen: Int)
        /// ⇧A: these items qualify; ⇧A again accepts them.
        case bulk([String])
    }

    /// The rows changed (another item, a reload, a note, a fold).
    @ObservationIgnored var onStream: (() -> Void)?
    /// The focused hunk moved (old, new ordinal): redraw them, scroll.
    @ObservationIgnored var onFocus: ((Int?, Int?) -> Void)?
    @ObservationIgnored var onToast: ((String, Bool) -> Void)?
    /// ⌥↩: History at the conversation that wrote the focused hunk.
    @ObservationIgnored var openHistory: ((ReviewProvenance, ReviewItem) -> Void)?
    /// The agent's summary (the default commit message).
    @ObservationIgnored var summary: ((String) -> String?)?
    @ObservationIgnored private var rejectQueue: [String: [String]] = [:]
    @ObservationIgnored private var rejecting: Set<String> = []
    @ObservationIgnored private var provenanceWork: DispatchWorkItem?

    init(hub: ReviewHub, client: DaemonClient?) {
        self.hub = hub
        self.client = client
    }

    // MARK: Reading

    var items: [ReviewItem] { hub.items }
    var selected: ReviewItem? { hub.item(selectedID) }
    var stream: ReviewStream? { selectedID.flatMap { streams[$0] } }
    var diff: ReviewDiff? { selectedID.flatMap { diffs[$0] } }
    var focused: Int? { selectedID.flatMap { focus[$0] } }
    var focusedHunk: HunkRef? {
        guard let s = stream, let f = focused, s.hunks.indices.contains(f) else { return nil }
        return s.hunks[f]
    }
    var currentAttention: ReviewAttention { selectedID.flatMap { attention[$0] } ?? ReviewAttention() }
    var currentNotes: ReviewNoteBook { selectedID.flatMap { notes[$0] } ?? ReviewNoteBook() }
    var currentEvidence: ReviewEvidence? { selectedID.flatMap { evidence[$0] } }

    func note(_ a: ReviewNoteAnchor) -> String? { selectedID.flatMap { notes[$0]?[a] } }

    /// The focused hunk's provenance line ("Why here? …"); nil: none yet.
    var focusedProvenance: ReviewProvenance? {
        guard let id = selectedID, let h = focusedHunk else { return nil }
        return provenance["\(id)|\(h.key)"]
    }

    var provenanceAvailable: Bool { selected.map { !provenanceOff.contains($0.machine) } ?? false }

    /// The hunks marked noted (attention strip).
    var notedKeys: Set<String> {
        guard let s = stream else { return [] }
        return currentNotes.notedHunks(s)
    }

    func isRejected(_ ordinal: Int) -> Bool {
        guard let s = stream, s.hunks.indices.contains(ordinal) else { return false }
        return currentAttention.rejected.contains(s.hunks[ordinal].key)
    }

    // MARK: Selection and loading

    /// The sheet opened: the asked item, else the one shown last, else the first.
    func opened(select id: String?) {
        let want = id.flatMap { i in items.first { $0.id == i }?.id } ?? selectedID.flatMap { i in items.first { $0.id == i }?.id } ?? items.first?.id
        select(want, force: true)
    }

    func select(_ id: String?, force: Bool = false) {
        guard force || id != selectedID else { return }
        selectedID = id
        confirm = nil
        editingNote = nil
        status = nil
        onStream?()
        guard let id else { return }
        if diffs[id] == nil || force { Task { await fetchDiff(id) } }
        if let item = hub.item(id), !evidenceOff.contains(item.machine) { Task { await fetchEvidence(id) } }
        requestProvenance()
    }

    /// The list changed under the sheet: keep the selection when it's
    /// still there, else the item now in its place.
    func itemsChanged(previousIndex: Int?) {
        guard let id = selectedID else {
            if let first = items.first { select(first.id) } // the first item arrived
            return
        }
        guard hub.item(id) == nil else { return }
        let list = items
        select(list.isEmpty ? nil : list[min(previousIndex ?? 0, list.count - 1)].id)
    }

    func step(_ d: Int) {
        let list = items
        guard !list.isEmpty else { return }
        let i = selectedID.flatMap { id in list.firstIndex { $0.id == id } } ?? (d > 0 ? -1 : list.count)
        select(list[max(0, min(list.count - 1, i + d))].id)
    }

    /// review.diff off the main thread; the rows are built there too.
    @discardableResult
    func fetchDiff(_ id: String) async -> Bool {
        guard let client, let item = hub.item(id) else { return false }
        loading.insert(id)
        let anchors = notes[id]?.anchors ?? []
        let open = unfolded[id] ?? []
        let r: Result<(ReviewDiff, ReviewStream), any Error> = await Task.detached {
            do {
                let d = try await client.reviewDiff(id)
                return .success((d, ReviewStream(files: d.files, notes: anchors, unfolded: open)))
            } catch { return .failure(error) }
        }.value
        loading.remove(id)
        switch r {
        case .success(let (d, s)):
            let keptKey = focus[id].flatMap { f in streams[id].flatMap { $0.hunks.indices.contains(f) ? $0.hunks[f].key : nil } }
            diffs[id] = d
            streams[id] = s
            failures[id] = nil
            var a = attention[id] ?? ReviewAttention()
            a.keep(Set(s.hunks.map(\.key)).union(a.rejected))
            attention[id] = a
            focus[id] = keptKey.flatMap { s.ordinal(ofKey: $0) } ?? (s.hunks.isEmpty ? nil : min(focus[id] ?? 0, s.hunks.count - 1))
            if id == selectedID {
                onStream?()
                requestProvenance()
            }
            return true
        case .failure(let e):
            if !hub.noteFailure(e, machine: item.machine) { failures[id] = Self.describe(e) }
            if id == selectedID { onStream?() }
            return false
        }
    }

    private func fetchEvidence(_ id: String) async {
        guard let client, let item = hub.item(id) else { return }
        let r: Result<ReviewEvidence, any Error> = await Task.detached {
            do { return .success(try await client.reviewEvidence(id)) } catch { return .failure(error) }
        }.value
        switch r {
        case .success(let e): evidence[id] = e
        case .failure(let e as RPCError) where ReviewRPC.missing(e): evidenceOff.insert(item.machine)
        case .failure: break
        }
    }

    // MARK: The cursor

    func setFocus(_ ordinal: Int?, scroll: Bool = true) {
        guard let id = selectedID, let s = streams[id] else { return }
        let old = focus[id]
        guard let o = ordinal, s.hunks.indices.contains(o) else { return }
        focus[id] = o
        // A folded file opens when the cursor reaches it.
        let path = s.files[s.hunks[o].file].path
        if s.headerRow(ofHunk: s.hunks[o].id) == nil {
            unfolded[id, default: []].insert(path)
            rebuild(id)
        }
        if scroll || old != o { onFocus?(old, o) }
        requestProvenance()
    }

    func nextHunk() { setFocus(ReviewCursor.next(focused, count: stream?.hunks.count ?? 0)) }
    func previousHunk() { setFocus(ReviewCursor.previous(focused, count: stream?.hunks.count ?? 0)) }
    func nextFile(back: Bool = false) {
        guard let s = stream else { return }
        setFocus(ReviewCursor.nextFile(focused, hunks: s.hunks, back: back))
    }

    /// A click in the stream: that hunk is focused; a code row keeps the
    /// spot for a note.
    func clicked(row: Int) {
        guard let id = selectedID, let s = streams[id] else { return }
        clickedRow[id] = row
        if let o = s.hunkOrdinal(atRow: row) { setFocus(o, scroll: false) }
    }

    /// ⏎ / a double click on a folded file: open or fold it.
    func toggleFold(row: Int? = nil) {
        guard let id = selectedID, let s = streams[id] else { return }
        var file: Int?
        if let row, s.rows.indices.contains(row) {
            switch s.rows[row] {
            case .file(let f), .folded(let f), .hunk(let f, _), .line(let f, _, _), .hunkEnd(let f, _): file = f
            case .note: file = nil
            }
        } else if let h = focusedHunk {
            file = h.file
        }
        guard let f = file, s.files[f].foldedByDefault else { return }
        let path = s.files[f].path
        if unfolded[id]?.contains(path) == true { unfolded[id]?.remove(path) } else { unfolded[id, default: []].insert(path) }
        rebuild(id)
    }

    private func rebuild(_ id: String) {
        guard let d = diffs[id] else { return }
        streams[id] = ReviewStream(files: d.files, notes: notes[id]?.anchors ?? [], unfolded: unfolded[id] ?? [])
        if id == selectedID { onStream?() }
    }

    // MARK: Decisions

    /// V: seen (toggle); on to the next undecided hunk.
    func toggleSeen() {
        guard let id = selectedID, let s = streams[id], let o = focused else { return }
        var a = attention[id] ?? ReviewAttention()
        a.toggleSeen(s.hunks[o].key)
        attention[id] = a
        if confirm != nil { confirm = nil }
        if a.seen.contains(s.hunks[o].key) { advance(from: o) } else { onFocus?(o, o) }
    }

    /// X: revert the hunk in the agent's folder; on to the next undecided.
    func reject() {
        guard let id = selectedID, let s = streams[id], let o = focused else { return }
        let key = s.hunks[o].key
        guard !(attention[id]?.rejected.contains(key) ?? false) else { return }
        var a = attention[id] ?? ReviewAttention()
        a.reject([key])
        attention[id] = a
        advance(from: o)
        rejectQueue[id, default: []].append(key)
        Task { await runRejects(id) }
    }

    private func advance(from o: Int) {
        guard let id = selectedID, let s = streams[id] else { return }
        if let n = ReviewCursor.afterDecision(o, hunks: s.hunks, decided: attention[id]?.decided ?? []) {
            setFocus(n)
        } else {
            onFocus?(o, o)
            status = ("Every hunk decided: ⌘↩ accepts, ⇧↩ sends notes back", false)
        }
    }

    /// One reject at a time, each against the diff as it is now: hunk ids
    /// are positions and shift when one goes.
    private func runRejects(_ id: String) async {
        guard let client, !rejecting.contains(id) else { return }
        rejecting.insert(id)
        defer { rejecting.remove(id) }
        while let key = rejectQueue[id]?.first {
            rejectQueue[id]?.removeFirst()
            guard let s = streams[id], let o = s.ordinal(ofKey: key), let item = hub.item(id) else { continue }
            let hunk = s.hunks[o].id
            let r: Result<Void, any Error> = await Task.detached {
                do { try await client.reviewReject(id, hunks: [hunk]); return .success(()) } catch { return .failure(error) }
            }.value
            switch r {
            case .success:
                await fetchDiff(id)
            case .failure(let e):
                attention[id]?.unreject([key])
                if !hub.noteFailure(e, machine: item.machine) { status = ("Couldn't reject: \(Self.describe(e))", true) }
                if id == selectedID { onFocus?(o, o) }
            }
        }
    }

    // MARK: Notes

    /// C: a note on the clicked line when it is in the focused hunk, else
    /// on the hunk's first changed line.
    func startNote() {
        guard let id = selectedID, let s = streams[id], let h = focusedHunk else { return }
        var anchor: ReviewNoteAnchor?
        if let r = clickedRow[id], s.hunkOrdinal(atRow: r) == focused, case .line = s.rows[r] { anchor = s.anchor(atRow: r) }
        if anchor == nil, let header = s.headerRow(ofHunk: h.id) {
            // The first changed line under the header.
            let lines = s.hunk(h).lines
            let i = lines.firstIndex { $0.kind != .context } ?? 0
            anchor = s.anchor(atRow: header + 1 + i)
        }
        editingNote = anchor
    }

    func saveNote(_ text: String) {
        guard let id = selectedID, let a = editingNote else { return }
        var book = notes[id] ?? ReviewNoteBook()
        book.set(a, text)
        notes[id] = book
        editingNote = nil
        rebuild(id)
    }

    func cancelNote() { editingNote = nil }

    /// ⇧↩: every note as one instruction to the agent; it goes on working.
    func sendBack() {
        guard let client, let item = selected, busy == nil else { return }
        let id = item.id
        let list = (notes[id] ?? ReviewNoteBook()).ordered(paths: diffs[id]?.files.map(\.path) ?? [])
        guard !list.isEmpty else {
            status = ("No notes yet: C adds one to the focused hunk", false)
            return
        }
        busy = "Sending \(list.count) note\(list.count == 1 ? "" : "s")…"
        Task {
            let r: Result<Void, any Error> = await Task.detached {
                do { try await client.reviewSendBack(id, notes: list); return .success(()) } catch { return .failure(error) }
            }.value
            busy = nil
            switch r {
            case .success:
                notes[id] = nil
                onToast?("Sent \(list.count) note\(list.count == 1 ? "" : "s") to \(item.name)", false)
                forget(id)
            case .failure(let e):
                if !hub.noteFailure(e, machine: item.machine) { status = ("Couldn't send back: \(Self.describe(e))", true) }
            }
        }
    }

    // MARK: Accept

    /// ⌘↩: commit the work in the agent's folder. With hunks nobody marked
    /// seen, the first ⌘↩ says how many; the second accepts.
    func accept() {
        guard let item = selected, busy == nil else { return }
        let unseen = stream.map { currentAttention.unseen($0.hunks) } ?? 0
        if unseen > 0, confirm != .accept(id: item.id, unseen: unseen) {
            confirm = .accept(id: item.id, unseen: unseen)
            return
        }
        confirm = nil
        Task { _ = await commit(item) }
    }

    @discardableResult
    private func commit(_ item: ReviewItem) async -> Bool {
        guard let client else { return false }
        let id = item.id
        let message = ReviewText.commitMessage(item, summary: summary?(id))
        busy = "Committing \(item.name)…"
        let r: Result<String?, any Error> = await Task.detached {
            do { return .success(try await client.reviewAccept(id, message: message)) } catch { return .failure(error) }
        }.value
        busy = nil
        switch r {
        case .success(let commit):
            let short = commit.map { String($0.prefix(7)) }
            onToast?(short.map { "Committed \($0) · \(item.name)" } ?? "Accepted \(item.name)", false)
            forget(id)
            return true
        case .failure(let e):
            if !hub.noteFailure(e, machine: item.machine) {
                let busyAgent = (e as? RPCError)?.dataCode == "busy" || "\(e)".localizedCaseInsensitiveContains("busy")
                status = (busyAgent ? "\(item.name) is working: accept once it's done" : "Couldn't commit: \(Self.describe(e))", true)
            }
            return false
        }
    }

    /// ⇧A: every item that is low risk with fresh evidence and touches no
    /// schema, auth or migration path. The first ⇧A says which; the
    /// second accepts them one by one.
    func bulkAccept() {
        guard busy == nil else { return }
        if case .bulk(let ids) = confirm {
            confirm = nil
            Task {
                var done = 0
                for id in ids {
                    guard let item = hub.item(id) else { continue }
                    if await commit(item) { done += 1 }
                }
                if done > 0 { onToast?("Accepted \(done) item\(done == 1 ? "" : "s") in bulk", false) }
            }
            return
        }
        let candidates = items.filter(ReviewBulk.candidate)
        guard !candidates.isEmpty else {
            status = ("Nothing to accept in bulk: that needs low risk and fresh tests, and no schema, auth or migration files", false)
            return
        }
        busy = "Checking \(candidates.count) item\(candidates.count == 1 ? "" : "s")…"
        Task {
            for c in candidates where diffs[c.id] == nil { await fetchDiff(c.id) }
            busy = nil
            let ok = candidates.filter { ReviewBulk.verdict($0, files: diffs[$0.id]?.files) == .eligible }.map(\.id)
            if ok.isEmpty {
                status = ("Nothing to accept in bulk: the low-risk items touch schema, auth or migration files", false)
            } else {
                confirm = .bulk(ok)
            }
        }
    }

    /// Why the selected item may (not) go in bulk.
    var bulkVerdict: ReviewBulk.Verdict? {
        guard let item = selected else { return nil }
        return ReviewBulk.verdict(item, files: diff?.files)
    }

    // MARK: Provenance

    /// The focused hunk's "Why here?", fetched once per hunk (debounced:
    /// J held down asks for the hunk it stops on).
    private func requestProvenance() {
        provenanceWork?.cancel()
        guard let client, let item = selected, !provenanceOff.contains(item.machine), let s = stream, let h = focusedHunk else { return }
        let key = "\(item.id)|\(h.key)"
        guard provenance[key] == nil else { return }
        let path = s.files[h.file].path, line = s.provenanceLine(h), id = item.id
        let w = DispatchWorkItem { [weak self] in
            MainActor.assumeIsolated { self?.loadProvenance(client, key: key, id: id, machine: item.machine, path: path, line: line) }
        }
        provenanceWork = w
        DispatchQueue.main.asyncAfter(deadline: .now() + 0.15, execute: w)
    }

    private func loadProvenance(_ client: DaemonClient, key: String, id: String, machine: String, path: String, line: Int) {
        Task { [weak self] in
            let r: Result<ReviewProvenance, any Error> = await Task.detached {
                do { return .success(try await client.reviewProvenance(id, path: path, line: line)) } catch { return .failure(error) }
            }.value
            guard let self else { return }
            switch r {
            case .success(let p): self.provenance[key] = p
            case .failure(let e as RPCError) where ReviewRPC.missing(e): self.provenanceOff.insert(machine)
            case .failure: return
            }
            if self.selectedID == id, let f = self.focused { self.onFocus?(f, f) }
        }
    }

    /// ⌥↩: the conversation that wrote the focused hunk, in History.
    func openProvenance() {
        guard let item = selected else { return }
        openHistory?(focusedProvenance ?? ReviewProvenance(), item)
    }

    // MARK: Confirm

    func cancelConfirm() -> Bool {
        guard confirm != nil || editingNote != nil else { return false }
        confirm = nil
        editingNote = nil
        return true
    }

    // MARK: Helpers

    private func forget(_ id: String) {
        let index = items.firstIndex { $0.id == id }
        diffs[id] = nil
        streams[id] = nil
        evidence[id] = nil
        attention[id] = nil
        focus[id] = nil
        hub.dropped(id)
        if selectedID == id {
            selectedID = nil
            itemsChangedAfterDrop(index)
        }
    }

    private func itemsChangedAfterDrop(_ index: Int?) {
        let list = items
        select(list.isEmpty ? nil : list[min(index ?? 0, list.count - 1)].id)
    }

    static func describe(_ e: any Error) -> String {
        if let r = e as? RPCError { return r.message }
        return "\(e)"
    }

    // MARK: Fixtures (offscreen renders)

    func showFixture(_ id: String, diff: ReviewDiff, evidence ev: ReviewEvidence?, attention a: ReviewAttention = ReviewAttention(),
                     notes book: ReviewNoteBook = ReviewNoteBook(), focus f: Int? = 0, provenance p: ReviewProvenance? = nil) {
        selectedID = id
        diffs[id] = diff
        notes[id] = book
        attention[id] = a
        evidence[id] = ev
        let s = ReviewStream(files: diff.files, notes: book.anchors)
        streams[id] = s
        focus[id] = f
        if let p, let f, s.hunks.indices.contains(f) { provenance["\(id)|\(s.hunks[f].key)"] = p }
        onStream?()
    }

    func showFixtureEditing(_ a: ReviewNoteAnchor?) { editingNote = a }
    func showFixtureConfirm(_ c: Confirm?) { confirm = c }
}
