import Foundation
import Testing
@testable import HesperCore

struct ReviewTests {
    // MARK: Fixtures

    func line(_ kind: ReviewLineKind, _ text: String, old: Int? = nil, new: Int? = nil) -> ReviewLine {
        ReviewLine(kind: kind, text: text, old: old, new: new)
    }

    func file(_ path: String, risk: ReviewRisk = .low, order: Int? = nil, formatting: Bool = false, generated: Bool = false, hunks: Int = 1, index: Int = 0) -> ReviewFile {
        let hs = (0..<hunks).map { h in
            ReviewHunk(id: "\(index):\(h)", oldStart: 10 * h + 1, oldLines: 2, newStart: 10 * h + 1, newLines: 2,
                       lines: [line(.context, "a", old: 10 * h + 1, new: 10 * h + 1), line(.removed, "b", old: 10 * h + 2), line(.added, "c", new: 10 * h + 2)])
        }
        return ReviewFile(path: path, formattingOnly: formatting, generated: generated, order: order, risk: risk, hunks: hs)
    }

    func item(_ id: String, risk: ReviewRisk = .low, evidence: EvidenceFreshness = .fresh, ready: TimeInterval = 0, machine: String = "mini") -> ReviewItem {
        ReviewItem(id: id, machine: machine, name: id, files: 2, added: 10, removed: 3, risk: risk, evidence: evidence, readyAt: Date(timeIntervalSince1970: ready))
    }

    // MARK: Wire

    @Test func decodesList() throws {
        let json = try JSONDecoder().decode(JSONValue.self, from: Data("""
        [{"id":"M/a1","machine":"mini","name":"api","kind":"codex","project":"/p/api","branch":"hesper/api","state":"done",
          "files":3,"added":120,"removed":14,"risk":"high","riskNotes":["touches auth"],"evidence":"stale","readyAt":"2026-10-09T10:00:00Z"},
         {"id":"L/b2","risk":"weird","evidence":"??"},
         {"name":"no id"}]
        """.utf8))
        let items = ReviewItem.list(json: json)
        #expect(items.count == 2)
        #expect(items[0].machine == "mini" && items[0].kind == "codex" && items[0].files == 3 && items[0].added == 120)
        #expect(items[0].risk == .high && items[0].evidence == .stale && items[0].riskNotes == ["touches auth"])
        #expect(items[0].readyAt != nil && items[0].branch == "hesper/api")
        // Unknown values are the safe ones: medium, missing; the machine from the id.
        #expect(items[1].risk == .medium && items[1].evidence == .missing && items[1].machine == "L" && items[1].name == "L/b2")
        #expect(ReviewItem.list(json: ["items": .array([["id": "x"]])]).map(\.id) == ["x"])
    }

    @Test func decodesDiffInReadingOrder() throws {
        let json = try JSONDecoder().decode(JSONValue.self, from: Data("""
        {"base":"abc123","head":"worktree","files":[
          {"path":"README.md","status":"M","order":2,"risk":"low","hunks":[]},
          {"path":"db/0042.sql","status":"A","order":0,"risk":"high","hunks":[
            {"id":"1:0","oldStart":0,"oldLines":0,"newStart":1,"newLines":2,"lines":[
              {"kind":"+","text":"ALTER TABLE users","new":1,"words":[[0,5]]},
              {"kind":" ","text":"x","old":1,"new":2}]}]},
          {"path":"src/new.ts","oldPath":"src/old.ts","status":"R","order":1,"formattingOnly":true,"hunks":[{"lines":[]}]}
        ]}
        """.utf8))
        let d = ReviewDiff(json: json)
        #expect(d.base == "abc123")
        #expect(d.files.map(\.path) == ["db/0042.sql", "src/new.ts", "README.md"])
        #expect(d.files[0].status == .added && d.files[0].risk == .high)
        #expect(d.files[0].hunks[0].lines[0].words == [NSRange(location: 0, length: 5)])
        #expect(d.files[0].hunks[0].lines[1].kind == .context && d.files[0].hunks[0].lines[1].old == 1)
        #expect(d.files[1].oldPath == "src/old.ts" && d.files[1].status == .renamed && d.files[1].foldedByDefault)
        // A hunk without an id gets "<file>:<hunk>" from its wire position.
        #expect(d.files[1].hunks[0].id == "2:0")
        #expect(d.hunkCount == 2 && d.lineCount == 2)
    }

    @Test func wordRangesAreUTF8BytesConvertedToUTF16() {
        // "ä" is 2 bytes in UTF-8, 1 unit in UTF-16; "😀" is 4 and 2.
        let text = "äb😀c"
        #expect(ReviewLine.utf16Ranges(text, utf8: [(2, 3)]) == [NSRange(location: 1, length: 1)])
        #expect(ReviewLine.utf16Ranges(text, utf8: [(3, 7)]) == [NSRange(location: 2, length: 2)])
        // Inside a character: snapped back to its start; past the end: clamped; empty: dropped.
        #expect(ReviewLine.utf16Ranges(text, utf8: [(1, 3)]) == [NSRange(location: 0, length: 2)])
        #expect(ReviewLine.utf16Ranges(text, utf8: [(7, 99)]) == [NSRange(location: 4, length: 1)])
        #expect(ReviewLine.utf16Ranges(text, utf8: [(3, 3)]).isEmpty)
    }

    @Test func decodesEvidenceAndProvenance() throws {
        let json = try JSONDecoder().decode(JSONValue.self, from: Data("""
        {"freshness":"fresh","lastEditAt":"2026-10-09T10:00:00Z","commands":[
          {"command":"swift test","kind":"test","exitCode":0,"startedAt":"2026-10-09T10:01:00Z","endedAt":"2026-10-09T10:02:00Z"},
          {"command":"ls","kind":"other"},{"kind":"test"}],
         "attachments":[{"path":"shot.png","kind":"image"},{"path":"x","kind":"?"}]}
        """.utf8))
        let e = ReviewEvidence(json: json)
        #expect(e.freshness == .fresh && e.lastEditAt != nil)
        #expect(e.commands.count == 2 && e.commands[0].passed && e.commands[0].kind.proves && e.commands[1].running)
        #expect(e.attachments == [EvidenceAttachment(path: "shot.png", kind: .image), EvidenceAttachment(path: "x", kind: .log)])
        let p = ReviewProvenance(json: ["sessionId": "s1", "turn": 2, "tool": "Edit", "prompt": "add the locale column"])
        #expect(p.turn == 2 && p.sessionId == "s1")
        #expect(ReviewText.provenance(p) == "Why here? turn 2 · Edit · “add the locale column”")
        #expect(ReviewText.provenance(ReviewProvenance()).hasPrefix("Why here? no edit"))
    }

    @Test func notesBecomeParams() {
        let n = ReviewNote(anchor: ReviewNoteAnchor(path: "a.go", line: 4, side: .old), text: "why?")
        #expect(n.param == ["path": "a.go", "line": 4, "side": "old", "text": "why?"])
        let whole = ReviewNote(anchor: ReviewNoteAnchor(path: "a.go"), text: "split this")
        #expect(whole.param["line"] == nil)
    }

    // MARK: Support per Mac

    @Test func missingMethodDropsThatMac() {
        var s = ReviewSupport()
        #expect(s.filter([item("a")]).isEmpty) // not known yet
        s.local = true
        let items = [item("a", machine: "mini"), item("b", machine: "laptop")]
        #expect(s.filter(items).count == 2)
        let unknown = s.note(RPCError(code: -32000, message: "Unknown method review.diff", kind: .remote), machine: "mini")
        let busy = s.note(RPCError(code: -32000, message: "busy", kind: .remote), machine: "laptop")
        #expect(unknown && !busy)
        #expect(s.filter(items).map(\.id) == ["b"])
        let old = s.note(RPCError(code: -32601, message: "no method", kind: .notFound), machine: "laptop")
        #expect(old)
        #expect(s.filter(items).isEmpty)
    }

    // MARK: Order

    @Test func fallbackOrderPutsRiskFirstTestsByTheirCodeAndNoiseLast() {
        let files = [
            file("README.md"),
            file("Tests/UsersTests.swift"),
            file("gen/api.pb.go", generated: true),
            file("Sources/Users.swift", risk: .medium),
            file("src/fmt.ts", formatting: true),
            file("config.json"),
            file("db/migrations/0042_users.sql", risk: .high),
        ]
        #expect(ReviewOrder.apply(files).map(\.path) == [
            "db/migrations/0042_users.sql", "Sources/Users.swift", "Tests/UsersTests.swift", "config.json", "README.md", "src/fmt.ts", "gen/api.pb.go",
        ])
        // hesperd's order wins when present (ties keep wire order).
        let ordered = [file("b", order: 1), file("a", order: 0), file("c", order: 1), file("d")]
        #expect(ReviewOrder.apply(ordered).map(\.path) == ["a", "b", "c", "d"])
    }

    // MARK: Inbox, bulk accept, badges

    @Test func inboxRanksFreshAndLowRiskFirst() {
        let items = [item("missing", evidence: .missing), item("staleLow", evidence: .stale), item("freshHigh", risk: .high),
                     item("freshLowNew", ready: 20), item("freshLowOld", ready: 10)]
        #expect(ReviewInbox.ranked(items).map(\.id) == ["freshLowOld", "freshLowNew", "freshHigh", "staleLow", "missing"])
    }

    @Test func bulkAcceptOnlyLowRiskFreshAndNotSensitive() {
        let ok = item("a")
        #expect(ReviewBulk.candidate(ok))
        #expect(ReviewBulk.verdict(ok, files: nil) == .unknownPaths)
        #expect(ReviewBulk.verdict(ok, files: [file("src/a.ts"), file("README.md")]) == .eligible)
        #expect(ReviewBulk.verdict(item("m", risk: .medium), files: []) == .risk(.medium))
        #expect(ReviewBulk.verdict(item("s", evidence: .stale), files: []) == .evidence(.stale))
        #expect(ReviewBulk.verdict(item("x", evidence: .missing), files: []) == .evidence(.missing))
        #expect(ReviewBulk.verdict(ok, files: [file("src/a.ts", risk: .medium)]) == .risk(.medium))
        for p in ["db/migrations/0042.sql", "prisma/schema.prisma", "src/auth/login.ts", "api/schema.graphql", "internal/oauth.go",
                  "src/Permissions.swift", "alembic/versions/1.py", "app/models/password_reset.rb", "x/y.sql"] {
            #expect(ReviewBulk.sensitive(p), "\(p)")
            #expect(ReviewBulk.verdict(ok, files: [file(p)]) == .sensitive(p))
        }
        for p in ["src/users.ts", "README.md", "app/Sources/Hesper/UI/Toolbar.swift", "AUTHORS.md", "docs/authoring.md"] {
            #expect(!ReviewBulk.sensitive(p), "\(p)")
        }
        // A rename from a sensitive path counts.
        var r = file("src/x.ts")
        r.oldPath = "src/auth.ts"
        #expect(ReviewBulk.verdict(ok, files: [r]) == .sensitive("src/auth.ts"))
        #expect(ReviewBulk.reason(.evidence(.stale)) == "edited after the last test or build")
    }

    @Test func evidenceBadgeAndTexts() {
        #expect(EvidenceBadge(.fresh).short == "tests ✓" && EvidenceBadge(.fresh).tone == .done)
        #expect(EvidenceBadge(.stale).short == "tests !" && EvidenceBadge(.stale).tone == .question)
        #expect(EvidenceBadge(.missing).short == "tests ?" && EvidenceBadge(.missing).tone == .dim)
        #expect(EvidenceBadge(.none).symbol == "–")
        let i = ReviewItem(id: "M/a", machine: "mini", name: "api", files: 3, added: 120, removed: 14, evidence: .fresh)
        #expect(ReviewText.footer(i) == "Ready to review · 3 files · tests ✓")
        #expect(ReviewText.footer(ReviewItem(id: "x", files: 1, evidence: .stale)) == "Ready to review · 1 file · tests !")
        #expect(ReviewText.pill(4) == "Review · 4")
        #expect(ReviewText.notificationTitle(i, machineName: "mini") == "api on mini is ready to review")
        #expect(ReviewText.notificationBody(i) == "3 files · +120 −14 · tests ✓")
        #expect(ReviewText.commitMessage(i, summary: "Add the locale column\nand more") == "api: Add the locale column and more")
        let now = Date(timeIntervalSince1970: 1000)
        let c = EvidenceCommand(command: "go test ./...", kind: .test, exitCode: 1, startedAt: now.addingTimeInterval(-200), endedAt: now.addingTimeInterval(-120))
        #expect(EvidenceText.mark(c) == "✗" && EvidenceText.meta(c, now: now) == "test · 2m ago · exit 1")
        let ordered = EvidenceText.ordered([EvidenceCommand(command: "ls", endedAt: now), c,
                                            EvidenceCommand(command: "make build", kind: .build, exitCode: 0, endedAt: now)])
        #expect(ordered.map(\.command) == ["make build", "go test ./...", "ls"])
    }

    @Test func arrivalsAnnounceOnlyNewItems() {
        var a = ReviewArrivals()
        #expect(a.update([item("a")]).isEmpty) // the first list is the baseline
        #expect(a.update([item("a"), item("b")]).map(\.id) == ["b"])
        #expect(a.update([item("b")]).isEmpty)
        #expect(a.update([item("a"), item("b")]).map(\.id) == ["a"]) // ready again
        a.reset()
        #expect(a.update([item("c")]).isEmpty)
    }

    // MARK: Stream

    @Test func streamRowsFoldsAndNotes() {
        let files = [file("a.ts", hunks: 2, index: 0), file("fmt.ts", formatting: true, index: 1)]
        let s = ReviewStream(files: files)
        // a.ts: header, 2 × (hunk, 3 lines, end); fmt.ts: header, folded.
        #expect(s.rows.count == 1 + 2 * 5 + 2)
        #expect(s.rows[0] == .file(0) && s.rows[1] == .hunk(0, 0) && s.rows[5] == .hunkEnd(0, 0) && s.rows.last == .folded(1))
        #expect(s.hunks.map(\.id) == ["0:0", "0:1", "1:0"])
        // Keys: the path and changed lines, not positions or numbers.
        #expect(s.hunks[0].key == files[0].hunks[0].fingerprint(path: "a.ts") && s.hunks[0].key != s.hunks[2].key)
        #expect(s.hunks[1].key == s.hunks[0].key + "#2") // the same change twice in one file
        var moved = files[0].hunks[0]
        moved.id = "0:5"
        moved.newStart = 99
        #expect(moved.fingerprint(path: "a.ts") == s.hunks[0].key && s.ordinal(ofKey: s.hunks[2].key) == 2)
        #expect(s.headerRow(ofHunk: "0:1") == 6 && s.headerRow(ofHunk: "1:0") == nil)
        #expect(s.endRow(ofHunk: s.hunks[0]) == 5)
        #expect(s.hunkOrdinal(atRow: 3) == 0 && s.hunkOrdinal(atRow: 0) == nil && s.hunkOrdinal(atRow: 7) == 1)
        #expect(ReviewRow.line(0, 0, 0).fixedHeight && !ReviewRow.note(ReviewNoteAnchor(path: "a")).fixedHeight)
        let open = ReviewStream(files: files, unfolded: ["fmt.ts"])
        #expect(open.headerRow(ofHunk: "1:0") != nil && !open.rows.contains(.folded(1)))

        // Notes: under their line (new side, old side), unknown lines under the header.
        let onNew = ReviewNoteAnchor(path: "a.ts", line: 2, side: .new)
        let onOld = ReviewNoteAnchor(path: "a.ts", line: 12, side: .old)
        let stale = ReviewNoteAnchor(path: "a.ts", line: 999, side: .new)
        let whole = ReviewNoteAnchor(path: "a.ts")
        let n = ReviewStream(files: files, notes: [onNew, onOld, stale, whole])
        #expect(n.rows[1] == .note(whole) && n.rows[2] == .note(stale))
        #expect(n.row(ofNote: onNew).map { n.rows[$0 - 1] } == .line(0, 0, 2))
        #expect(n.row(ofNote: onOld).map { n.rows[$0 - 1] } == .line(0, 1, 1))
        #expect(n.anchor(atRow: n.headerRow(ofHunk: "0:0")! + 2) == ReviewNoteAnchor(path: "a.ts", line: 2, side: .old))
        #expect(n.anchor(atRow: n.headerRow(ofHunk: "0:0")! + 3) == onNew)
        #expect(s.provenanceLine(s.hunks[1]) == 12)
    }

    @Test func cursorMovesAndAutoAdvances() {
        let hunks = [HunkRef(file: 0, hunk: 0, id: "0:0"), HunkRef(file: 0, hunk: 1, id: "0:1"), HunkRef(file: 1, hunk: 0, id: "1:0"), HunkRef(file: 2, hunk: 0, id: "2:0")]
        #expect(ReviewCursor.next(nil, count: 4) == 0 && ReviewCursor.next(3, count: 4) == 3 && ReviewCursor.previous(0, count: 4) == 0)
        #expect(ReviewCursor.next(nil, count: 0) == nil)
        #expect(ReviewCursor.nextFile(0, hunks: hunks) == 2 && ReviewCursor.nextFile(3, hunks: hunks) == 3)
        #expect(ReviewCursor.nextFile(1, hunks: hunks, back: true) == 0 && ReviewCursor.nextFile(2, hunks: hunks, back: true) == 0)
        #expect(ReviewCursor.afterDecision(0, hunks: hunks, decided: ["0:0", "0:1"]) == 2)
        #expect(ReviewCursor.afterDecision(3, hunks: hunks, decided: ["0:1", "1:0", "2:0"]) == 0)
        #expect(ReviewCursor.afterDecision(1, hunks: hunks, decided: Set(hunks.map(\.id))) == nil)
    }

    @Test func attentionTracksSeenRejectedNoted() {
        var a = ReviewAttention()
        let hunks = [HunkRef(file: 0, hunk: 0, id: "h0"), HunkRef(file: 0, hunk: 1, id: "h1"), HunkRef(file: 0, hunk: 2, id: "h2")]
        a.toggleSeen("h0")
        a.reject(["h1"])
        #expect(a.marks(hunks, noted: ["h2"]) == [.seen, .rejected, .noted])
        #expect(a.summary(total: 3) == "2 of 3 hunks seen" && a.unseen(hunks) == 1)
        a.toggleSeen("h0")
        #expect(a.decided == ["h1"])
        a.unreject(["h1"])
        #expect(a.marks(hunks, noted: []) == [.unseen, .seen, .unseen])
        a.keep(["h0"])
        #expect(a.decided.isEmpty)
    }

    @Test func notesBatchIntoOneInstruction() {
        var book = ReviewNoteBook()
        book.set(ReviewNoteAnchor(path: "b.ts", line: 2), "  rename to locale ")
        book.set(ReviewNoteAnchor(path: "a.ts", line: 9, side: .old), "why was this removed?")
        book.set(ReviewNoteAnchor(path: "a.ts"), "split this file")
        book.set(ReviewNoteAnchor(path: "c.ts", line: 1), "x")
        book.set(ReviewNoteAnchor(path: "c.ts", line: 1), "   ") // empty removes
        #expect(book.count == 3)
        let notes = book.ordered(paths: ["a.ts", "b.ts"])
        #expect(notes.map(\.text) == ["split this file", "why was this removed?", "rename to locale"])
        #expect(ReviewNotes.instruction(notes, message: "Close, but:") == """
        Close, but:
        Review notes (3):
        1. a.ts: split this file
        2. a.ts:9 (removed line): why was this removed?
        3. b.ts:2: rename to locale
        Address each note, then stop for another review.
        """)
        #expect(ReviewNotes.instruction([], message: nil).isEmpty)
        let s = ReviewStream(files: [file("b.ts", index: 0)], notes: book.anchors)
        #expect(book.notedHunks(s) == [s.hunks[0].key])
    }

    @Test func keys() {
        func r(_ k: Key, cmd: Bool = false, shift: Bool = false, opt: Bool = false, note: Bool = false) -> ReviewKeyAction {
            ReviewKeys.route(KeyChord(k, command: cmd, shift: shift, option: opt), editingNote: note)
        }
        #expect(r(.char("j")) == .nextHunk && r(.char("k")) == .previousHunk && r(.char("n")) == .nextFile)
        #expect(r(.char("N"), shift: true) == .previousFile && r(.char("v")) == .seen && r(.char("x")) == .reject && r(.char("c")) == .note)
        #expect(r(.enter, shift: true) == .sendBack && r(.enter, cmd: true) == .accept && r(.enter, opt: true) == .provenance)
        #expect(r(.char("A"), shift: true) == .bulkAccept && r(.char("a")) == .pass)
        #expect(r(.escape) == .close && r(.char("r"), cmd: true) == .close && r(.enter) == .toggleFold)
        #expect(r(.up) == .previousItem && r(.down) == .nextItem && r(.char("k"), cmd: true) == .pass)
        // In a note the text field gets the keys: ⌘↩ / ⇧↩ save, esc cancels.
        #expect(r(.char("j"), note: true) == .pass && r(.enter, note: true) == .pass)
        #expect(r(.enter, cmd: true, note: true) == .saveNote && r(.enter, shift: true, note: true) == .saveNote && r(.escape, note: true) == .cancelNote)
    }
}
