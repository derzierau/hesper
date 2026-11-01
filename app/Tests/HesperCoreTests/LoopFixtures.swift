import Foundation
@testable import HesperCore

// Synthetic loops for the loops tests (no real names, paths or hosts).
enum LoopFixtures {
    static let t0 = Date(timeIntervalSince1970: 1_793_000_000) // a fixed instant

    static func date(_ hours: Double) -> Date { t0.addingTimeInterval(hours * 3600) }

    static func item(_ id: String, _ text: String, owner: ItemOwner = .you, state: ItemState = .open, kind: ItemKind = .step,
                     order: Double = 0, source: String? = nil, created: Date? = nil, check: ItemCheck? = nil) -> LoopItem {
        LoopItem(id: id, text: text, owner: owner, state: state, kind: kind, order: order, source: source, check: check, created: created)
    }

    static func agent(_ ref: String, rel: LinkRel = .member, from: String? = nil, label: String? = nil, machine: String? = "L",
                      agent: String? = nil, state: AgentState? = nil, ending: LoopEnding? = nil, summary: String? = nil,
                      closed: Date? = nil, added: Date? = nil) -> LoopLink {
        LoopLink(kind: .agent, ref: ref, rel: rel, from: from, label: label, machine: machine, agent: agent, state: state, ending: ending,
                 summary: summary, closed: closed, added: added)
    }

    static func loop(_ id: String, _ title: String = "", next: LoopNext = .agents, status: LoopStatus = .open, looksDone: Bool = false,
                     items: [LoopItem] = [], links: [LoopLink] = [], projects: [String] = [], milestone: String? = nil,
                     created: Date? = nil, updated: Date? = nil, closed: Date? = nil, parkedUntil: Date? = nil, nextText: String? = nil) -> Loop {
        Loop(id: id, title: title.isEmpty ? id : title, status: status, next: next, nextText: nextText, looksDone: looksDone, milestone: milestone,
             parkedUntil: parkedUntil, items: items, links: links, projects: projects, created: created ?? t0, updated: updated ?? created ?? t0, closed: closed)
    }

    static func decode<T: Decodable>(_ t: T.Type, _ json: String) throws -> T {
        try JSONDecoder().decode(T.self, from: Data(json.utf8))
    }

    static func value(_ json: String) throws -> JSONValue { try decode(JSONValue.self, json) }

    /// A loop exactly as relay/pkg/wire/loops.go marshals it (every json
    /// tag, Go's time format, omitempty fields present).
    static let wireLoop = """
    {"id":"lp-0a1b2c3d4e5f","title":"Push provider for the demo app","goal":"Ship push on both platforms",
     "doneWhen":"PR merged","status":"open","next":"you","nextText":"Pick the provider","looksDone":false,
     "milestone":"ms-00aa11bb22cc","parkedUntil":"2026-11-02T09:00:00Z","mergedInto":"",
     "items":[
      {"id":"it-0000aaaa","text":"Pick the provider","owner":"you","state":"open","kind":"question","order":2,
       "after":["it-0000bbbb"],"ready":true,"draft":"d-123","source":"n-mini/a1b2c3",
       "check":{"kind":"pr","ref":"https://example.com/acme/demo/pull/7"},"created":"2026-10-31T08:00:00.123456789Z","done":"0001-01-01T00:00:00Z"},
      {"id":"it-0000bbbb","text":"Write the brief","owner":"agent","state":"done","kind":"step","order":1,
       "created":"2026-10-31T07:00:00Z","done":"2026-10-31T07:30:00Z"}],
     "links":[
      {"kind":"agent","ref":"n-mini/a1b2c3","rel":"started","from":"n-mini/root01","label":"push-provider","machine":"mini",
       "agent":"mini/a1b2c3","state":"done","ending":"asks","summary":"Which provider?","closedReason":"","result":"",
       "added":"2026-10-31T07:00:00Z"},
      {"kind":"file","ref":"L:/tmp/fake/brief.md","rel":"handoff","from":"n-mini/a1b2c3","added":"2026-10-31T07:10:00Z"},
      {"kind":"loop","ref":"lp-ffffffffffff","rel":"blockedBy","added":"2026-10-31T07:20:00Z"}],
     "counts":{"agents":1,"working":0,"openYou":1,"openAgents":0,"openOthers":0,"sessions":2},
     "projects":["p-demo"],"created":"2026-10-31T07:00:00Z","updated":"2026-10-31T09:00:00Z"}
    """

    static let wireMilestone = """
    {"id":"ms-00aa11bb22cc","scope":"g-demo","goal":"gl-00aa11bb22cc","title":"Beta","status":"open","order":1,
     "due":"2026-11-15T00:00:00Z","autoFile":true,
     "items":[{"id":"it-0000cccc","text":"Push works","owner":"agent","state":"done","kind":"condition","order":0,"created":"2026-10-30T00:00:00Z"},
              {"id":"it-0000dddd","text":"Store listing","owner":"you","state":"open","kind":"condition","order":1,"created":"2026-10-30T00:00:00Z"}],
     "loops":["lp-0a1b2c3d4e5f"],"done":1,"total":2,"stalled":true,"nextText":"Store listing",
     "created":"2026-10-30T00:00:00Z","updated":"2026-10-31T00:00:00Z"}
    """

    static let wireGoal = """
    {"id":"gl-00aa11bb22cc","scope":"g-demo","title":"Demo app in the stores","order":1,"status":"open"}
    """

    static let wireSteward = """
    {"enabled":true,"level":"act","node":"n-mini","machine":"mini","agent":"mini/st0001","queued":3,"wakes":12,
     "lastWake":"2026-10-31T17:00:00Z","session":"s-0001"}
    """

    static let wireSettings = """
    {"loopLimit":4,"stallDays":10,"eveningAt":"19:30","maxWakesPerDay":20}
    """
}
