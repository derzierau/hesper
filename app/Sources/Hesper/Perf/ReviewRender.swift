import AppKit
import HesperCore

/// Dev tool: `Hesper --render-review <dir>` draws the review sheet
/// offscreen (no window, no daemon) into PNGs, Dusk and Daylight, wide
/// (three columns) and narrow (the evidence under the inbox): the inbox
/// across two Macs, one item's stream in reading order (a migration
/// first, code with its test next to it, a folded formatting-only file),
/// a note, a rejected and seen hunks, the provenance line, the evidence
/// panel; plus a confirm and the note editor, and the empty sheet. Then
/// exits.
@MainActor
enum ReviewRender {
    static func run(_ dir: String) {
        try? FileManager.default.createDirectory(atPath: dir, withIntermediateDirectories: true)
        for (scheme, appearance) in [("dusk", NSAppearance.Name.darkAqua), ("daylight", .aqua)] {
            render(size: NSSize(width: 1500, height: 960), appearance: appearance, to: dir, name: "review-wide-\(scheme)") { _ in }
            render(size: NSSize(width: 1000, height: 760), appearance: appearance, to: dir, name: "review-narrow-\(scheme)") { _ in }
            render(size: NSSize(width: 1500, height: 960), appearance: appearance, to: dir, name: "review-note-\(scheme)") { s in
                s.setFocus(2)
                s.showFixtureEditing(ReviewNoteAnchor(path: "Sources/Users/UserStore.swift", line: 41))
                s.showFixtureConfirm(.accept(id: "M/a1", unseen: 3))
            }
            render(size: NSSize(width: 1200, height: 760), appearance: appearance, to: dir, name: "review-empty-\(scheme)", items: []) { _ in }
        }
    }

    static func render(size: NSSize, appearance: NSAppearance.Name, to dir: String, name: String, items: [ReviewItem]? = nil,
                       _ setup: (ReviewSession) -> Void) {
        let hub = ReviewHub.offscreen()
        hub.showFixture(items ?? Fixture.items)
        let session = ReviewSession(hub: hub, client: nil)
        let panel = ReviewPanel(session: session, machineNames: { ["mini": "mini", "laptop": "laptop"] })
        let backdrop = FlippedView(frame: NSRect(origin: .zero, size: size))
        backdrop.wantsLayer = true
        backdrop.appearance = NSAppearance(named: appearance)
        backdrop.layer?.backgroundColor = Theme.ns(.background).cg(in: backdrop)
        panel.frame = backdrop.bounds
        backdrop.addSubview(panel)
        if items == nil {
            session.showFixture("M/a1", diff: Fixture.diff, evidence: Fixture.evidence, attention: Fixture.attention, notes: Fixture.notes,
                                focus: 1, provenance: Fixture.provenance)
        }
        panel.showForRender()
        setup(session)
        for _ in 0..<4 {
            backdrop.needsLayout = true
            backdrop.layoutSubtreeIfNeeded()
            panel.needsLayout = true
            panel.layoutSubtreeIfNeeded()
            panel.stream.table.layoutSubtreeIfNeeded()
            RunLoop.current.run(until: Date().addingTimeInterval(0.05))
        }
        guard let rep = backdrop.bitmapImageRepForCachingDisplay(in: backdrop.bounds) else { return }
        backdrop.cacheDisplay(in: backdrop.bounds, to: rep)
        let path = (dir as NSString).appendingPathComponent(name + ".png")
        if let png = rep.representation(using: .png, properties: [:]) {
            try? png.write(to: URL(fileURLWithPath: path))
            print("wrote \(path)")
        }
    }

    /// Fixtures: made-up projects and code (no real session data).
    enum Fixture {
        static let now = Date()

        static let items: [ReviewItem] = [
            ReviewItem(id: "M/a1", machine: "mini", name: "users locale", kind: "claude", branch: "hesper/users-locale", state: .done, files: 5,
                       added: 64, removed: 12, risk: .high, riskNotes: ["adds a database migration", "changes UserStore, used by 9 files"],
                       evidence: .stale, readyAt: now.addingTimeInterval(-600)),
            ReviewItem(id: "L/b2", machine: "laptop", name: "docs pass", kind: "codex", project: "/projects/api", state: .idle, files: 3,
                       added: 41, removed: 30, risk: .low, evidence: .fresh, readyAt: now.addingTimeInterval(-1500)),
            ReviewItem(id: "M/c3", machine: "mini", name: "flaky e2e retry", kind: "claude", branch: "hesper/e2e-retry", state: .done, files: 2,
                       added: 18, removed: 4, risk: .medium, riskNotes: ["changes test timeouts"], evidence: .missing, readyAt: now.addingTimeInterval(-300)),
            ReviewItem(id: "L/d4", machine: "laptop", name: "toolbar polish", kind: "claude", branch: "hesper/toolbar", state: .done, files: 4,
                       added: 120, removed: 88, risk: .low, evidence: .fresh, readyAt: now.addingTimeInterval(-60)),
        ]

        static func l(_ k: ReviewLineKind, _ t: String, _ o: Int?, _ n: Int?, words: [NSRange] = []) -> ReviewLine {
            ReviewLine(kind: k, text: t, old: o, new: n, words: words)
        }

        static let diff = ReviewDiff(base: "4f2a9c1", files: [
            ReviewFile(path: "db/migrations/0042_users_locale.sql", status: .added, order: 0, risk: .high, hunks: [
                ReviewHunk(id: "0:0", oldStart: 0, oldLines: 0, newStart: 1, newLines: 3, lines: [
                    l(.added, "ALTER TABLE users ADD COLUMN locale TEXT NOT NULL DEFAULT 'en';", nil, 1),
                    l(.added, "CREATE INDEX users_locale_idx ON users (locale);", nil, 2),
                    l(.added, "", nil, 3),
                ]),
            ]),
            ReviewFile(path: "Sources/Users/UserStore.swift", status: .modified, order: 1, risk: .medium, hunks: [
                ReviewHunk(id: "1:0", oldStart: 12, oldLines: 6, newStart: 12, newLines: 7, lines: [
                    l(.context, "struct User: Codable, Sendable {", 12, 12),
                    l(.context, "    var id: UUID", 13, 13),
                    l(.context, "    var name: String", 14, 14),
                    l(.added, "    var locale: String = \"en\"", nil, 15),
                    l(.context, "    var created: Date", 15, 16),
                    l(.context, "}", 16, 17),
                ]),
                ReviewHunk(id: "1:1", oldStart: 38, oldLines: 7, newStart: 39, newLines: 8, lines: [
                    l(.context, "    func save(_ u: User) async throws {", 38, 39),
                    l(.removed, "        try await db.execute(\"INSERT INTO users (id, name) VALUES (?, ?)\", u.id, u.name)", 39, nil),
                    l(.added, "        try await db.execute(\"INSERT INTO users (id, name, locale) VALUES (?, ?, ?)\", u.id, u.name, u.locale)", nil, 40,
                      words: [NSRange(location: 57, length: 8), NSRange(location: 79, length: 3), NSRange(location: 98, length: 10)]),
                    l(.added, "        log.info(\"saved user \\(u.id) locale=\\(u.locale)\")", nil, 41),
                    l(.context, "    }", 40, 42),
                    l(.context, "", 41, 43),
                    l(.context, "    func find(_ id: UUID) async throws -> User? {", 42, 44),
                ]),
                ReviewHunk(id: "1:2", oldStart: 70, oldLines: 4, newStart: 72, newLines: 3, lines: [
                    l(.context, "    func remove(_ id: UUID) async throws {", 70, 72),
                    l(.removed, "        // TODO: soft delete", 71, nil),
                    l(.context, "        try await db.execute(\"DELETE FROM users WHERE id = ?\", id)", 72, 73),
                    l(.context, "    }", 73, 74),
                ]),
            ]),
            ReviewFile(path: "Tests/UsersTests/UserStoreTests.swift", status: .modified, order: 2, risk: .low, hunks: [
                ReviewHunk(id: "2:0", oldStart: 20, oldLines: 3, newStart: 20, newLines: 9, lines: [
                    l(.context, "    @Test func savesAndFinds() async throws {", 20, 20),
                    l(.context, "        let store = try await UserStore.inMemory()", 21, 21),
                    l(.added, "        var u = User(id: UUID(), name: \"Ada\", created: .now)", nil, 22),
                    l(.added, "        u.locale = \"de\"", nil, 23),
                    l(.added, "        try await store.save(u)", nil, 24),
                    l(.added, "        #expect(try await store.find(u.id)?.locale == \"de\")", nil, 25),
                    l(.context, "    }", 22, 26),
                ]),
            ]),
            ReviewFile(path: "config/app.json", status: .modified, order: 3, risk: .low, hunks: [
                ReviewHunk(id: "3:0", oldStart: 4, oldLines: 3, newStart: 4, newLines: 3, lines: [
                    l(.context, "  \"features\": {", 4, 4),
                    l(.removed, "    \"locales\": false", 5, nil, words: [NSRange(location: 15, length: 5)]),
                    l(.added, "    \"locales\": true", nil, 5, words: [NSRange(location: 15, length: 4)]),
                    l(.context, "  },", 6, 6),
                ]),
            ]),
            ReviewFile(path: "Sources/Users/Formatting.swift", status: .modified, formattingOnly: true, order: 4, risk: .low, hunks: [
                ReviewHunk(id: "4:0", oldStart: 1, oldLines: 2, newStart: 1, newLines: 2, formattingOnly: true, lines: [
                    l(.removed, "func  pad(_ s:String)->String{ s }", 1, nil),
                    l(.added, "func pad(_ s: String) -> String { s }", nil, 1),
                ]),
            ]),
        ])

        static var attention: ReviewAttention {
            let s = ReviewStream(files: diff.files)
            var a = ReviewAttention()
            a.markSeen([s.hunks[0].key])
            a.reject([s.hunks[3].key])
            return a
        }

        static var notes: ReviewNoteBook {
            var b = ReviewNoteBook()
            b.set(ReviewNoteAnchor(path: "Sources/Users/UserStore.swift", line: 15, side: .new),
                  "Make locale optional instead of defaulting to \"en\": existing rows have no locale and the default hides that.")
            return b
        }

        static let evidence = ReviewEvidence(freshness: .stale, lastEditAt: now.addingTimeInterval(-240), commands: [
            EvidenceCommand(command: "swift test --filter UserStoreTests", kind: .test, exitCode: 0, startedAt: now.addingTimeInterval(-560), endedAt: now.addingTimeInterval(-540)),
            EvidenceCommand(command: "swift build", kind: .build, exitCode: 0, startedAt: now.addingTimeInterval(-700), endedAt: now.addingTimeInterval(-660)),
            EvidenceCommand(command: "make lint", kind: .lint, exitCode: 1, startedAt: now.addingTimeInterval(-300), endedAt: now.addingTimeInterval(-290)),
            EvidenceCommand(command: "git status --short", kind: .other, exitCode: 0, startedAt: now.addingTimeInterval(-200), endedAt: now.addingTimeInterval(-199)),
        ], attachments: [EvidenceAttachment(path: "screenshots/settings-locale.png", kind: .image)])

        static let provenance = ReviewProvenance(sessionId: "s-fixture", turn: 2, tool: "Edit",
                                                 prompt: "Add a locale to users, stored in the database, default English", at: now.addingTimeInterval(-900))
    }
}
