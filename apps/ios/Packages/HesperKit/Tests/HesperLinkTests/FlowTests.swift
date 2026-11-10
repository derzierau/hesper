import CryptoKit
import Foundation
import HesperCore
import HesperFleet
import Synchronization
import Testing

@testable import HesperLink

/// Pairing and the live link against a fake relay and Mac.
@Suite struct FlowTests {
    static let secret = Data(0..<16)

    static func context(_ relay: FakeRelay, api: RelayAPI? = nil) async throws -> LinkContext {
        let context = LinkContext(secrets: MemoryStore(), sockets: relay, api: { api ?? RelayAPI(origin: $0) }, enclave: false)
        try await context.credentials.save(
            Credentials(
                relay: api?.origin.absoluteString ?? "https://relay.example.com", deviceId: "phone-1", token: "access-1",
                refreshToken: "refresh-1", expiresAt: .distantFuture, login: "octo"))
        return context
    }

    static func offer(_ host: FakeHost, secret: Data = secret, key: Data? = nil, expires: Date = Date() + 300) -> PairingOffer {
        PairingOffer(
            relay: URL(string: "https://relay.example.com")!, machine: host.id, name: host.short, hostKey: key ?? host.hostKey,
            rights: ["observe", "answer"], secret: secret, expires: expires)
    }

    @Test func scannedOfferApprovesAtOnceAndPinsTheKey() async throws {
        let host = FakeHost(id: "MACHINE_ID", short: "studio", offer: Self.secret)
        let context = try await Self.context(FakeRelay([host]))
        let pairing = LivePairing(context: context, deviceName: "iPhone", pushEnvironment: "sandbox")
        // The Mac as the live link and the fleet name it, not its machine id.
        #expect(try await pairing.pair(offer: Self.offer(host)) == .approved(machine: "studio", rights: ["observe", "answer"]))
        let sent = try #require(host.params("devices.request"))
        #expect(sent["name"] as? String == "iPhone")
        #expect(sent["hardware"] as? Bool == false)
        #expect(sent["e2eBinding"] != nil && sent["offerProof"] != nil)
        let pinned = try #require(context.hosts.all()["MACHINE_ID"])
        #expect(pinned.hostKey == host.hostKey && pinned.approved)
        #expect(try await pairing.unpairedMacs().isEmpty)
    }

    @Test func aWrongSecretIsAnExpiredOffer() async throws {
        let host = FakeHost(id: "MACHINE_ID", short: "studio", offer: Self.secret)
        let pairing = LivePairing(context: try await Self.context(FakeRelay([host])), deviceName: "iPhone", pushEnvironment: "sandbox")
        await #expect(throws: PairingError.offerExpired) { try await pairing.pair(offer: Self.offer(host, secret: Data(count: 16))) }
        await #expect(throws: PairingError.offerExpired) { try await pairing.pair(offer: Self.offer(host, expires: Date() - 1)) }
    }

    @Test func anotherHostKeyThanTheCodeIsAChangedKey() async throws {
        let host = FakeHost(id: "MACHINE_ID", short: "studio", offer: Self.secret)
        let pairing = LivePairing(context: try await Self.context(FakeRelay([host])), deviceName: "iPhone", pushEnvironment: "sandbox")
        await #expect(throws: PairingError.keyChanged(machine: "MACHINE_ID")) {
            try await pairing.pair(offer: Self.offer(host, key: Data(count: 32)))
        }
    }

    @Test func anOlderMacFallsBackToTheCode() async throws {
        let host = FakeHost(id: "MACHINE_ID", short: "mini")
        let context = try await Self.context(FakeRelay([host]))
        let pairing = LivePairing(context: context, deviceName: "iPhone", pushEnvironment: "sandbox")
        #expect(try await pairing.unpairedMacs() == [Machine(short: "mini", name: "mini Mac", online: true)])
        guard case .pending(let machine, let code, _) = try await pairing.pair(machine: "mini") else {
            Issue.record("not pending")
            return
        }
        let keys = try context.keys()
        #expect(machine == "mini")
        #expect(code == ApprovalCode.make(hostKey: host.hostKey, key: keys.device.publicKeyDER, strongKey: keys.strong.publicKeyDER))
        #expect(host.params("devices.request")?["offerProof"] == nil)
    }

    @Test func liveLinkFollowsAnApprovedMac() async throws {
        let host = FakeHost(id: "MACHINE_ID", short: "M", offer: Self.secret)
        let context = try await Self.context(FakeRelay([host]))
        _ = try await LivePairing(context: context, deviceName: "iPhone", pushEnvironment: "sandbox").pair(offer: Self.offer(host))
        let link = LiveLink(context: context)
        var updates = link.updates().makeAsyncIterator()
        var seen: [LinkUpdate] = []
        while let update = await updates.next() {
            seen.append(update)
            if case .event = update { break }
        }
        #expect(seen.first == .macs([Machine(short: "M", name: "M Mac", online: true)]))
        #expect(seen.contains(.reachability(.connecting, machine: "M")))
        #expect(seen.contains(.drafts([], machine: "M")))
        #expect(seen.contains(.reachability(.live(direct: false), machine: "M")))
        guard case .event(.list(let machine, let agents, true), "M") = seen.last else {
            Issue.record("no list: \(seen)")
            return
        }
        #expect(machine == "M" && agents.map(\.id) == ["M/a1"])

        try await link.perform(.answer(agent: "M/a1", .allow))
        #expect(host.params("agents.answer")?["decision"] as? String == "allow")
        try await link.perform(.reply(agent: "M/a1", text: "go <on>"))
        #expect(host.params("agents.input")?["text"] as? String == "go <on>")
        #expect(host.params("agents.input")?["submit"] as? Bool == true)
        let draft = Draft(id: "d1", text: "later")
        try await link.perform(.saveDraft(draft, machine: "M"))
        while let update = await updates.next() {
            if case .draftChanged(let d, "M") = update {
                #expect(d.id == "d1")
                break
            }
        }
        await #expect(throws: RelayError.self) { try await link.perform(.stop(agent: "other/a1")) }
    }

    @Test func signInClaimsAndStoresTheCredentials() async throws {
        let api = FakeHTTP.relay { request, body in
            switch request.url?.path() {
            case "/v1/auth/enroll":
                #expect(jsonObject(body)["previousDevice"] == nil)
                return FakeHTTP.json(
                    201,
                    [
                        "id": "enr-1", "code": "ABCD1234", "verificationURL": "https://relay.test/auth/login?enrollment=enr-1",
                        "expires": "2099-01-01T00:00:00Z", "interval": 0,
                    ])
            default:
                return FakeHTTP.json(
                    200, ["deviceId": "phone-1", "role": "controller", "token": "t", "refreshToken": "r", "relay": "x"])
            }
        }
        let context = LinkContext(secrets: MemoryStore(), sockets: FakeRelay([]), api: { _ in api }, enclave: false)
        let pairing = LivePairing(context: context, deviceName: "iPhone", pushEnvironment: "sandbox")
        let account = try await pairing.signIn(relay: api.origin) { page, code, scheme in
            #expect(code == "ABCD1234" && scheme == "hesper-ios")
            #expect(page.absoluteString.hasPrefix("https://relay.test/auth/login"))
            return URL(string: "hesper-ios://auth?enrollment=enr-1&status=approved")!
        }
        #expect(account == RelayAccount(relay: api.origin, deviceID: "phone-1", login: ""))
        #expect(await pairing.account() == account)
        #expect(try context.keys().e2ePublic.count == 32)
    }

    @Test func signInAgainProvesThePreviousDevice() async throws {
        let api = FakeHTTP.relay { request, body in
            let previous = jsonObject(body)["previousDevice"] as? [String: Any]
            #expect(previous?["id"] as? String == "phone-1")
            #expect(previous?["key"] != nil && previous?["sig"] != nil)
            return FakeHTTP.json(403, ["code": "not_allowed", "message": "x"])
        }
        let pairing = LivePairing(
            context: try await Self.context(FakeRelay([]), api: api), deviceName: "iPhone", pushEnvironment: "sandbox")
        await #expect(throws: PairingError.notAllowed(login: "")) {
            try await pairing.signIn(relay: api.origin) { _, _, _ in URL(string: "hesper-ios://auth")! }
        }
    }

    @Test func aRejectedPreviousDeviceSignsInAsNew() async throws {
        let enrolls = Mutex<[Bool]>([])
        let api = FakeHTTP.relay { request, body in
            if request.url?.path() == "/v1/auth/enroll" {
                let previous = jsonObject(body)["previousDevice"] != nil
                enrolls.withLock { $0.append(previous) }
                return FakeHTTP.json(
                    201,
                    [
                        "id": previous ? "e1" : "e2", "code": "c", "verificationURL": "https://relay.test/x",
                        "expires": "2099-01-01T00:00:00Z", "interval": 0,
                    ])
            }
            if jsonObject(body)["id"] as? String == "e1" {
                return FakeHTTP.json(403, ["code": "previous_device_rejected", "message": "x"])
            }
            return FakeHTTP.json(200, ["relay": "x", "deviceId": "phone-2", "role": "controller", "token": "t", "refreshToken": "r"])
        }
        let pairing = LivePairing(
            context: try await Self.context(FakeRelay([]), api: api), deviceName: "iPhone", pushEnvironment: "sandbox")
        let account = try await pairing.signIn(relay: api.origin) { _, _, _ in URL(string: "hesper-ios://auth")! }
        #expect(account.deviceID == "phone-2")
        #expect(enrolls.withLock { $0 } == [true, false])
    }

    @Test func signInEndsAtTheCallbackStatus() async throws {
        let api = FakeHTTP.relay { _, _ in
            FakeHTTP.json(
                201, ["id": "e", "code": "c", "verificationURL": "https://relay.test/x", "expires": "2099-01-01T00:00:00Z", "interval": 5])
        }
        let pairing = LivePairing(
            context: LinkContext(secrets: MemoryStore(), sockets: FakeRelay([]), api: { _ in api }, enclave: false),
            deviceName: "iPhone", pushEnvironment: "sandbox")
        await #expect(throws: PairingError.notAllowed(login: "")) {
            try await pairing.signIn(relay: api.origin) { _, _, _ in URL(string: "hesper-ios://auth?enrollment=e&status=not_allowed")! }
        }
        await #expect(throws: PairingError.failed("The sign-in expired. Try again.")) {
            try await pairing.signIn(relay: api.origin) { _, _, _ in URL(string: "hesper-ios://auth?enrollment=e&status=expired")! }
        }
        await #expect(throws: PairingError.failed("Sign-in didn't finish. Try again in a minute.")) {
            try await pairing.signIn(relay: api.origin) { _, _, _ in URL(string: "hesper-ios://auth?enrollment=e&status=failed")! }
        }
        await #expect(throws: PairingError.cancelled) {
            try await pairing.signIn(relay: api.origin) { _, _, _ in throw CancellationError() }
        }
    }

    @Test func pushTokenGoesToTheRelay() async throws {
        let sent = Mutex<[String: Any]>([:])
        let api = FakeHTTP.relay { request, body in
            #expect(request.url?.path() == "/v1/devices/push")
            #expect(request.value(forHTTPHeaderField: "Authorization") == "Bearer access-1")
            sent.withLock { $0 = jsonObject(body) }
            return (204, Data())
        }
        let pairing = LivePairing(
            context: try await Self.context(FakeRelay([]), api: api), deviceName: "iPhone", pushEnvironment: "production")
        try await pairing.registerPushToken(Data([0xab, 0x01]))
        #expect(sent.withLock { $0["token"] as? String } == "ab01")
        #expect(sent.withLock { $0["environment"] as? String } == "production")
    }

    @Test func resetForgetsEverything() async throws {
        let host = FakeHost(id: "MACHINE_ID", short: "studio", offer: Self.secret)
        let context = try await Self.context(FakeRelay([host]))
        let pairing = LivePairing(context: context, deviceName: "iPhone", pushEnvironment: "sandbox")
        _ = try await pairing.pair(offer: Self.offer(host))
        let key = try context.keys().keyBase64
        await pairing.reset()
        #expect(await pairing.account() == nil)
        #expect(context.hosts.all().isEmpty)
        #expect(try context.keys().keyBase64 != key)
    }
}

@Suite struct WireTests {
    @Test func offerProofGoldenVector() {
        let proof = OfferProof.make(
            secret: Data(0..<16), machine: "MACHINE_ID", name: "iPhone", key: "KEY_DER_B64", strongKey: "STRONG_DER_B64", e2eKey: "E2E_B64")
        #expect(proof == "egD7PtFnM87JDQYgwg/Ufl0e0cOSazrK/X4p5ukb1Wc=")
    }

    @Test func approvalCodeMatchesGo() {
        // relay/pkg/devicekey TestApprovalCode.
        let host = Data(SHA256.hash(data: Data("host".utf8)))
        #expect(ApprovalCode.make(hostKey: host, key: Data("a".utf8), strongKey: Data("b".utf8)) == "ILY-TIQ")
        #expect(ApprovalCode.make(hostKey: host, key: Data("a".utf8), strongKey: Data("c".utf8)) != "ILY-TIQ")
    }

    @Test func commandsBecomeHostRequests() throws {
        let (machine, method, params) = try LiveLink.request(for: .choose(agent: "M/a1", option: 2))
        #expect(machine == "M" && method == "agents.input")
        #expect(jsonObject(params)["text"] as? String == "2" && jsonObject(params)["submit"] == nil)
        let spawn = try LiveLink.request(for: .spawn(SpawnRequest(machine: "M", project: "/p", task: "t")))
        #expect(spawn.machine == "M" && spawn.method == "agents.spawn" && jsonObject(spawn.params)["machine"] == nil)
        #expect(try LiveLink.request(for: .close(agent: "M/a1")).method == "agents.close")
        #expect(try LiveLink.request(for: .removeDraft(id: "d", machine: "L")).machine == "L")
    }

    @Test func aDenyCarriesWhatToDoInstead() throws {
        let deny = try LiveLink.request(for: .answer(agent: "M/a1", .deny, message: "Use a rebase"))
        #expect(deny.method == "agents.answer")
        #expect(jsonObject(deny.params)["decision"] as? String == "deny")
        #expect(jsonObject(deny.params)["message"] as? String == "Use a rebase")
        #expect(jsonObject(try LiveLink.request(for: .answer(agent: "M/a1", .deny)).params)["message"] == nil)
    }
}
