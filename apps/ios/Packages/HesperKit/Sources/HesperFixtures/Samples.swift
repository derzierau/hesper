import Foundation
import HesperCore
import HesperFleet

/// Sample Macs and agents: the ones the wireframes show
/// (docs/iphone/wireframes). Made up; no real session data.
public enum Samples {
    public static let macs: [Machine] = [
        Machine(short: "mini", name: "mini", online: true, rttMs: 3, route: "direct"),
        Machine(short: "studio", name: "studio", online: true, rttMs: 48, route: "relay"),
        Machine(short: "air", name: "air", online: false),
    ]

    /// What each sample Mac publishes (`snapshot.capabilities`): all but
    /// `mini` name a terminal's size owner, so `mini`'s agents show the
    /// mirror and the others fit the terminal to the phone.
    public static let capabilities: [String: [String: Bool]] = [
        "mini": ["e2e": true],
        "studio": ["e2e": true, "sizeOwner": true],
        "air": ["e2e": true, "sizeOwner": true],
    ]

    /// The sample phone: its controller id, its name, and the rights the
    /// sample Macs granted it.
    public static let device = "sample-iphone"
    public static let deviceName = "iPhone"
    public static let rights = ["observe", "answer", "type", "transfer"]

    /// One `agents.link` list event per Mac, as hesperd sends them, so the
    /// fixtures go through the same decoder as the live link.
    public static let listEvents: [String: String] = [
        "mini": """
        {"machine":"mini","full":true,"list":[
          {"id":"mini/m1","machine":"mini","kind":"claude","name":"migrations","project":"hesper","branch":"db/orders",
           "state":"approval","stateSince":"2026-10-31T09:35:00Z",
           "attention":{"kind":"approval","title":"Bash","detail":"npm run db:migrate","options":["allow","always","deny"]}},
          {"id":"mini/m2","machine":"mini","kind":"claude","name":"search-index","project":"hesper","branch":"main",
           "state":"working","stateSince":"2026-10-31T09:29:00Z","activity":"Wiring index into search","size":{"cols":120,"rows":32}},
          {"id":"mini/m3","machine":"mini","kind":"claude","name":"history-bench","project":"hesper","state":"starting",
           "stateSince":"2026-10-31T09:41:00Z"},
          {"id":"mini/m4","machine":"mini","kind":"claude","name":"fix-login","project":"hesper","branch":"fix/login",
           "state":"done","stateSince":"2026-10-31T09:23:00Z","summary":"Fixed the token refresh race; added a test."},
          {"id":"mini/m5","machine":"mini","kind":"claude","name":"regex-help","state":"idle","stateSince":"2026-10-31T07:40:00Z"},
          {"id":"mini/m6","machine":"mini","kind":"codex","name":"bench-notes","state":"exited","stateSince":"2026-10-31T04:40:00Z",
           "exit":{"code":0}}
        ]}
        """,
        "studio": """
        {"machine":"studio","full":true,"list":[
          {"id":"studio/s1","machine":"studio","kind":"claude","name":"relay-tests","project":"hesper","branch":"fix/hub",
           "state":"approval","stateSince":"2026-10-31T09:37:00Z",
           "attention":{"kind":"approval","title":"Edit","detail":"internal/relay/hub.go","options":["allow","always","deny"]}},
          {"id":"studio/s2","machine":"studio","kind":"claude","name":"api-docs","project":"hesper","state":"question",
           "stateSince":"2026-10-31T09:32:00Z",
           "attention":{"kind":"question","detail":"Which format should the API docs use?","options":["OpenAPI","Markdown"]}}
        ]}
        """,
        "air": """
        {"machine":"air","full":true,"list":[
          {"id":"air/a1","machine":"air","kind":"claude","name":"hero-image","project":"website","state":"working",
           "stateSince":"2026-10-31T08:40:00Z","activity":"Exporting hero variants"},
          {"id":"air/a2","machine":"air","kind":"claude","name":"pricing-copy","project":"website","state":"done",
           "stateSince":"2026-10-31T09:01:00Z","summary":"Rewrote the pricing page copy."}
        ]}
        """,
    ]

    /// Tasks kept for later, as `drafts.list` returns them.
    public static let drafts: [String: [Draft]] = [
        "studio": [
            Draft(
                id: "d-flaky1", text: "Look into the flaky relay reconnect test", machine: "studio", project: "hesper", parked: true,
                created: ISO8601DateFormatter().date(from: "2026-10-30T18:12:00Z"))
        ]
    ]

    /// A sample agent's terminal as hesperd would send it after the reply:
    /// a prompt, Claude's tool lines (a green Read with its dim result, a
    /// yellow Bash), a cursor move back up to redraw a line, and a bold
    /// cyan question. Made up, no captured session.
    public static let terminalScript: Data = {
        let esc = "\u{1b}["
        let lines = [
            "\(esc)1m$ claude\(esc)0m",
            "",
            "\(esc)32m⏺\(esc)0m Read(Sources/HesperFleet/Fleet.swift)",
            "\(esc)2m  ⎿  Read 214 lines\(esc)0m",
            "",
            "\(esc)33m⏺\(esc)0m Bash(swift test --filter FleetTests)",
            "\(esc)2m  ⎿  Running…\(esc)0m",
        ]
        // Up one line, clear it, and redraw the Bash result as done.
        let redraw = "\(esc)1A\r\(esc)2K\(esc)2m  ⎿  12 tests passed\(esc)0m\r\n"
        let question = "\r\n\(esc)1;36mShall I commit the fix to fix/login?\(esc)0m\r\n"
        return Data((esc + "H" + esc + "2J" + lines.joined(separator: "\r\n") + "\r\n" + redraw + question).utf8)
    }()

    /// The line the scripted terminal adds every tick, numbered.
    public static func terminalLine(_ n: Int) -> Data {
        Data("\u{1b}[2m  ⎿  still working (\(n))\u{1b}[0m\r\n".utf8)
    }

    /// The grid of the scripted terminal.
    public static let terminalSize = (cols: 97, rows: 56)

    /// When `air` dropped off, in the samples.
    public static let airLost = ISO8601DateFormatter().date(from: "2026-10-31T08:40:00Z")!

    /// The updates a fresh link delivers: the Macs, their reachability and
    /// their lists.
    public static func initialUpdates() -> [LinkUpdate] {
        var updates: [LinkUpdate] = [.macs(macs)]
        for m in macs {
            updates.append(.reachability(m.online ? .live(direct: m.route == "direct") : .unreachable(since: airLost), machine: m.short))
            if let json = listEvents[m.short], let event = try? LinkEvent.decode(Data(json.utf8)) {
                updates.append(.event(event, machine: m.short))
            }
            if m.online { updates.append(.drafts(drafts[m.short] ?? [], machine: m.short)) }
        }
        return updates
    }

    /// The whole sample fleet, folded.
    public static var fleet: Fleet {
        var f = Fleet(macs: macs)
        for u in initialUpdates() {
            switch u {
            case .event(let e, let m): f.apply(e, from: m)
            case .reachability(let r, let m): f.setReachability(r, for: m)
            case .drafts(let list, let m): f.setDrafts(list, on: m)
            case .macs, .draftChanged, .draftRemoved: break
            }
        }
        return f
    }
}
