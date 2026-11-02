import Foundation
import Testing
@testable import HesperCore

struct PhonePairingTests {
    let t0 = Date(timeIntervalSince1970: 1_793_650_656)

    func offer(_ id: String, at t: Date, lasts: TimeInterval = 300) -> PhoneOffer {
        PhoneOffer(id: id, payload: "HESPER1:\(id)", expires: t.addingTimeInterval(lasts))
    }

    // MARK: Rights

    @Test func rightsDefaultToEverythingButShells() {
        let r = PhoneRights()
        #expect(r.rights == ["observe", "answer", "type", "transfer"])
        #expect(!r.shell)
    }

    @Test func rightsFollowTheToggles() {
        var r = PhoneRights()
        r[.type] = false
        r[.shell] = true
        #expect(r.rights == ["observe", "answer", "transfer", "shell"])
        r = PhoneRights(answer: false, type: false, transfer: false, shell: false)
        #expect(r.rights == ["observe"])
    }

    @Test func rightsReadBackAsTheToggleTitles() {
        #expect(PhoneRights.summary(["observe", "answer", "type", "transfer"]) == ["Answer and approve", "Type into agents", "Start and stop agents"])
        #expect(PhoneRights.summary(["observe", "shell", "answer"]) == ["Answer and approve", "Shells"])
        #expect(PhoneRights.summary(["observe"]) == [])
        #expect(PhoneRights(["observe", "type", "bogus"]) == PhoneRights(answer: false, type: true, transfer: false, shell: false))
    }

    // MARK: Wire

    @Test func decodesTheOfferAndThePairing() throws {
        let v: JSONValue = ["payload": "HESPER1:abc", "expires": 1_793_650_956, "id": "o1"]
        let o = try #require(PhoneOffer(json: v))
        #expect(o == PhoneOffer(id: "o1", payload: "HESPER1:abc", expires: Date(timeIntervalSince1970: 1_793_650_956)))
        #expect(PhoneOffer(json: ["payload": "HESPER1:abc", "id": "o1"]) == nil)
        #expect(PhoneOffer(json: ["payload": "", "expires": 1, "id": "o1"]) == nil)

        let p: JSONValue = ["offer": "o1", "device": "d1", "name": "iPhone", "rights": ["observe", "answer"]]
        #expect(PhonePaired(params: p) == PhonePaired(offer: "o1", device: "d1", name: "iPhone", rights: ["observe", "answer"]))
        #expect(PhonePaired(params: ["device": "d1"]) == nil)
    }

    @Test func offerParamsCarryTheRights() {
        #expect(PhonePairingRPC.offerParams(PhoneRights()) == ["rights": ["observe", "answer", "type", "transfer"]])
        #expect(PhonePairingRPC.cancelParams("o1") == ["id": "o1"])
    }

    // MARK: Renewal

    @Test func renewsEveryTwoMinutesOrBeforeItExpires() {
        var p = PhonePairing()
        let req = p.request()
        _ = p.offered(offer("o1", at: t0), request: req, at: t0)
        #expect(p.renewDelay(now: t0) == 120)
        #expect(p.renewDelay(now: t0.addingTimeInterval(100)) == 20)
        #expect(p.renewDelay(now: t0.addingTimeInterval(200)) == 0)

        // A short-lived offer renews ahead of its expiry.
        var q = PhonePairing()
        let r2 = q.request()
        _ = q.offered(offer("o2", at: t0, lasts: 60), request: r2, at: t0)
        #expect(q.renewDelay(now: t0) == 60 - PhonePairing.renewMargin)
        // Nothing to renew unless waiting.
        #expect(PhonePairing().renewDelay(now: t0) == nil)
    }

    @Test func renewingKeepsTheEarlierCodesOpen() {
        // A phone that scanned the previous code is still signing in.
        var p = PhonePairing()
        _ = p.offered(offer("o1", at: t0), request: p.request(), at: t0)
        let t1 = t0.addingTimeInterval(120)
        let cancel = p.offered(offer("o2", at: t1), request: p.request(), at: t1)
        #expect(cancel.isEmpty)
        #expect(p.state == .waiting(offer("o2", at: t1)))
        #expect(p.open(at: t1) == ["o1", "o2"])
        // Expired ones drop out of the open list.
        #expect(p.open(at: t0.addingTimeInterval(301)) == ["o2"])
    }

    @Test func changingTheRightsCancelsTheOpenCodes() {
        var p = PhonePairing()
        _ = p.offered(offer("o1", at: t0), request: p.request(), at: t0)
        let cancel = p.setRights(PhoneRights(answer: true, type: true, transfer: true, shell: true), at: t0)
        #expect(cancel == ["o1"])
        #expect(p.open(at: t0).isEmpty)
        #expect(p.state == .starting) // a cancelled code is not shown
        #expect(p.setRights(p.rights, at: t0).isEmpty)
    }

    @Test func aLateAnswerToAnEarlierRequestIsCancelled() {
        var p = PhonePairing()
        let stale = p.request()
        _ = p.setRights(PhoneRights(answer: true, type: false, transfer: true, shell: false), at: t0)
        let fresh = p.request()
        #expect(p.offered(offer("old", at: t0), request: stale, at: t0) == ["old"])
        #expect(p.state == .starting)
        #expect(p.offered(offer("new", at: t0), request: fresh, at: t0).isEmpty)
        #expect(p.state == .waiting(offer("new", at: t0)))
    }

    // MARK: States

    @Test func pairsOnlyWithItsOwnOffers() {
        var p = PhonePairing()
        _ = p.offered(offer("o1", at: t0), request: p.request(), at: t0)
        _ = p.offered(offer("o2", at: t0), request: p.request(), at: t0)
        // hesperctl phone in a terminal paired a phone: not this pane's code.
        #expect(p.paired(PhonePaired(offer: "other", device: "d", name: "iPad", rights: ["observe"]), at: t0) == nil)
        #expect(p.state == .waiting(offer("o2", at: t0)))

        let e = PhonePaired(offer: "o1", device: "d1", name: "iPhone", rights: ["observe", "answer"])
        #expect(p.paired(e, at: t0) == ["o2"])
        #expect(p.state == .paired(e))
        #expect(p.open(at: t0).isEmpty)
        #expect(p.renewDelay(now: t0) == nil)
        #expect(p.title == "iPhone connected")
    }

    @Test func showAnotherCodeStartsOver() {
        var p = PhonePairing()
        _ = p.offered(offer("o1", at: t0), request: p.request(), at: t0)
        _ = p.paired(PhonePaired(offer: "o1", device: "d1", name: "iPhone", rights: ["observe"]), at: t0)
        let req = p.request()
        #expect(p.state == .starting)
        _ = p.offered(offer("o3", at: t0), request: req, at: t0)
        #expect(p.state == .waiting(offer("o3", at: t0)))
    }

    @Test func anOlderHesperdShowsTheManualHint() {
        var p = PhonePairing()
        let req = p.request()
        p.failed(RPCError(code: -32601, message: "no method devices.offer", kind: .notFound), request: req)
        #expect(p.state == .unsupported)
        #expect(p.retryDelay == nil)
    }

    @Test func otherFailuresRetry() {
        var p = PhonePairing()
        let req = p.request()
        p.failed(ConnectionError.notConnected, request: req)
        #expect(p.state == .failed("Not connected to hesperd."))
        #expect(p.retryDelay == PhonePairing.retryAfter)
        let r2 = p.request()
        p.failed(RPCError(code: -32000, message: "no relay enrollment", kind: .unavailable), request: r2)
        #expect(p.state == .failed("no relay enrollment"))
        // A failure of a superseded request changes nothing.
        let r3 = p.request()
        p.failed(ConnectionError.closed, request: r2)
        #expect(p.state == .failed("no relay enrollment"))
        _ = r3
    }

    @Test func closingCancelsWhatIsStillOpen() {
        var p = PhonePairing()
        _ = p.offered(offer("o1", at: t0), request: p.request(), at: t0)
        _ = p.offered(offer("o2", at: t0.addingTimeInterval(120)), request: p.request(), at: t0.addingTimeInterval(120))
        #expect(p.close(at: t0.addingTimeInterval(310)) == ["o2"])
        #expect(p.state == .starting)
        #expect(p.close(at: t0).isEmpty)
    }
}
