import CryptoKit
import Foundation
import Synchronization
import Testing

@testable import HesperLink

@Suite struct EnrollmentTests {
    static var credentials: [String: Any] {
        [
            "relay": "ignored", "deviceId": "dev-1", "role": "controller", "token": "access-1", "refreshToken": "refresh-1",
            "expiresAt": "2026-11-02T10:15:00Z", "refreshExpiresAt": "2026-12-02T10:00:00.123456789Z",
        ]
    }

    @Test func pkceChallengeIsTheHashOfTheVerifier() {
        // RFC 7636, Appendix B.
        let pkce = PKCE(verifier: "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk")
        #expect(pkce.challenge == "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM")
        let random = PKCE.random()
        #expect(random.verifier.count == 43)
        #expect(random != PKCE.random())
    }

    @Test func enrollSendsTheChallengeAndTheAppCallback() async throws {
        let sent = Mutex<[String: Any]>([:])
        let api = FakeHTTP.relay { request, body in
            #expect(request.url?.path() == "/v1/auth/enroll")
            sent.withLock { $0 = jsonObject(body) }
            return FakeHTTP.json(
                201,
                [
                    "id": "enr-1", "code": "ABCD1234", "verificationURL": "https://relay.test/auth/login?enrollment=enr-1",
                    "expires": "2026-11-02T10:10:00Z", "interval": 5,
                ])
        }
        let pkce = PKCE.random()
        let e = try await api.enroll(name: "iPhone", pkce: pkce)
        #expect(e.id == "enr-1" && e.code == "ABCD1234" && e.interval == 5)
        let body = sent.withLock { $0 }
        #expect(body["name"] as? String == "iPhone")
        #expect(body["role"] as? String == "controller")
        #expect(body["callback"] as? String == "hesper-ios://auth")
        #expect(body["challenge"] as? String == pkce.challenge)
        #expect(body["previousDevice"] == nil)
    }

    @Test func claimWaitsOnPendingAndKeepsTheRelayOrigin() async throws {
        let calls = Mutex(0)
        let api = FakeHTTP.relay { _, body in
            #expect(jsonObject(body)["verifier"] as? String == "v")
            let n = calls.withLock {
                $0 += 1
                return $0
            }
            return n == 1 ? FakeHTTP.json(409, ["code": "authorization_pending", "message": "x"]) : FakeHTTP.json(200, Self.credentials)
        }
        #expect(try await api.claim(id: "enr-1", verifier: "v") == nil)
        let c = try #require(try await api.claim(id: "enr-1", verifier: "v"))
        #expect(c.relay == api.origin.absoluteString)
        #expect(c.deviceId == "dev-1" && c.token == "access-1")
        #expect(c.refreshExpiresAt != nil)
    }

    @Test func claimFailuresCarryTheRelayCode() async {
        let api = FakeHTTP.relay { _, _ in FakeHTTP.json(403, ["code": "not_allowed", "message": "x"]) }
        await #expect(throws: RelayHTTPError(status: 403, code: "not_allowed")) { try await api.claim(id: "e", verifier: "v") }
    }

    @Test func refreshSendsTheAccessTokenAsProof() async throws {
        let api = FakeHTTP.relay { request, body in
            #expect(request.value(forHTTPHeaderField: "Authorization") == "Bearer access-1")
            #expect(jsonObject(body)["refreshToken"] as? String == "refresh-1")
            var next = Self.credentials
            next["token"] = "access-2"
            next["refreshToken"] = "refresh-2"
            return FakeHTTP.json(200, next)
        }
        let c = Credentials(
            relay: api.origin.absoluteString, deviceId: "dev-1", token: "access-1", refreshToken: "refresh-1", login: "octo")
        let next = try await api.refresh(c)
        #expect(next.token == "access-2" && next.refreshToken == "refresh-2" && next.login == "octo")
    }

    @Test func refreshRejectsAnotherDevice() async {
        let api = FakeHTTP.relay { _, _ in
            var next = Self.credentials
            next["deviceId"] = "dev-2"
            return FakeHTTP.json(200, next)
        }
        let c = Credentials(relay: api.origin.absoluteString, deviceId: "dev-1", token: "a", refreshToken: "r")
        await #expect(throws: RelayHTTPError.self) { try await api.refresh(c) }
    }

    @Test func renewalRunsOnceForConcurrentCallers() async throws {
        let calls = Mutex(0)
        let api = FakeHTTP.relay { _, _ in
            calls.withLock { $0 += 1 }
            Thread.sleep(forTimeInterval: 0.05)
            var next = Self.credentials
            next["token"] = "access-2"
            next["refreshToken"] = "refresh-2"
            next["expiresAt"] = "2030-01-01T00:00:00Z"
            return FakeHTTP.json(200, next)
        }
        let secrets = MemoryStore()
        let store = CredentialStore(store: secrets, api: { _ in api })
        try await store.save(
            Credentials(relay: api.origin.absoluteString, deviceId: "dev-1", token: "a", refreshToken: "r", expiresAt: Date()))
        async let a = store.fresh()
        async let b = store.fresh()
        let (x, y) = try await (a, b)
        #expect(x.token == "access-2" && y.token == "access-2")
        #expect(calls.withLock { $0 } == 1)
        // Persisted before use: a new store reads the new pair.
        let reread = await CredentialStore(store: secrets).current()
        #expect(reread?.refreshToken == "refresh-2")
        #expect(try await store.fresh().token == "access-2")
        #expect(calls.withLock { $0 } == 1)
    }

    @Test func rejectedRenewalAsksToSignInAgain() async throws {
        let api = FakeHTTP.relay { _, _ in FakeHTTP.json(401, ["code": "unauthorized", "message": "x"]) }
        let store = CredentialStore(store: MemoryStore(), api: { _ in api })
        try await store.save(
            Credentials(relay: api.origin.absoluteString, deviceId: "d", token: "a", refreshToken: "r", expiresAt: .distantPast))
        do {
            _ = try await store.fresh()
            Issue.record("renewed")
        } catch let e as RelayHTTPError {
            #expect(e.signInRequired)
        }
    }

    @Test func callbackStatus() {
        func status(_ s: String) -> EnrollmentCallback? { EnrollmentCallback(URL(string: s)!, enrollment: "enr-1") }
        #expect(status("hesper-ios://auth?enrollment=enr-1&status=approved&approved=1") == .approved)
        #expect(status("hesper-ios://auth?approved=1&enrollment=enr-1") == .approved)
        #expect(status("hesper-ios://auth?enrollment=enr-1&status=denied") == .denied)
        #expect(status("hesper-ios://auth?enrollment=enr-1&status=not_allowed") == .notAllowed(login: ""))
        #expect(status("hesper-ios://auth?enrollment=enr-2&status=approved") == nil)
    }

    /// relay/pkg/devicekey TestReenrollGoldenVector, with the golden key.
    @Test func previousDeviceGoldenVector() throws {
        let challenge = "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
        let message = PreviousDevice.message(id: "DEVICE_ID", challenge: challenge)
        #expect(Data(SHA256.hash(data: message)).hex == "4eee5d0e5cbb4c57533ff78a028c6729288d933742cf1268831935839439e167")
        let golden = try P256.Signing.ECDSASignature(
            derRepresentation: Data(
                base64Encoded: "MEUCIQD6cFeqZh3+VcLAMMtTdKt8H1UDwymYLPJ5jhCHHRzp8AIgTVR+RTia8JN+mnwbN2k/ejFG+F9rOyW2UxWFwvrJggA=")!)
        let key = try RequestSigningTests.goldenKey()
        #expect(key.publicKey.isValidSignature(golden, for: message))
        let proof = try PreviousDevice.sign(id: "DEVICE_ID", challenge: challenge, key: SoftwareSigningKey(key))
        #expect(proof.key == RequestSigningTests.publicKey)
        let sig = try P256.Signing.ECDSASignature(derRepresentation: Data(base64Encoded: proof.sig)!)
        #expect(key.publicKey.isValidSignature(sig, for: message))
    }
}
