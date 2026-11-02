import CryptoKit
import Foundation

/// The relay's credentials for this phone (relay/docs/protocol.md, "GitHub
/// enrollment"): the access token for the socket, the refresh token that
/// renews it.
public struct Credentials: Codable, Equatable, Sendable {
    public var relay: String
    public var deviceId: String
    public var role: String
    public var token: String
    public var refreshToken: String?
    public var expiresAt: Date?
    public var refreshExpiresAt: Date?
    /// The GitHub login, when the relay tells it; kept for "Signed in as".
    public var login: String?

    public init(
        relay: String, deviceId: String, role: String = "controller", token: String, refreshToken: String? = nil,
        expiresAt: Date? = nil, refreshExpiresAt: Date? = nil, login: String? = nil
    ) {
        self.relay = relay
        self.deviceId = deviceId
        self.role = role
        self.token = token
        self.refreshToken = refreshToken
        self.expiresAt = expiresAt
        self.refreshExpiresAt = refreshExpiresAt
        self.login = login
    }

    /// RFC 3339 dates with or without fractional seconds, as Go writes them.
    static let decoder: JSONDecoder = {
        let d = JSONDecoder()
        d.dateDecodingStrategy = .custom { decoder in
            let text = try decoder.singleValueContainer().decode(String.self)
            guard let date = RFC3339.date(text) else {
                throw DecodingError.dataCorrupted(.init(codingPath: decoder.codingPath, debugDescription: "not RFC 3339: \(text)"))
            }
            return date
        }
        return d
    }()

    static let encoder: JSONEncoder = {
        let e = JSONEncoder()
        e.dateEncodingStrategy = .custom { date, encoder in
            var c = encoder.singleValueContainer()
            try c.encode(RFC3339.string(date))
        }
        return e
    }()
}

enum RFC3339 {
    static func date(_ text: String) -> Date? {
        if let d = try? Date(text, strategy: Date.ISO8601FormatStyle(includingFractionalSeconds: true)) { return d }
        return try? Date(text, strategy: Date.ISO8601FormatStyle())
    }

    static func string(_ date: Date) -> String {
        date.formatted(Date.ISO8601FormatStyle(includingFractionalSeconds: true))
    }
}

/// PKCE for enrollment: the verifier stays on the phone, only its
/// challenge is sent.
public struct PKCE: Equatable, Sendable {
    public let verifier: String
    public var challenge: String { Self.challenge(for: verifier) }

    public init(verifier: String) { self.verifier = verifier }

    /// A fresh verifier: 32 random bytes, base64url.
    public static func random() -> PKCE {
        PKCE(verifier: Data((0..<32).map { _ in UInt8.random(in: 0...255) }).base64URLEncodedString())
    }

    /// base64url(SHA-256(verifier)), no padding.
    public static func challenge(for verifier: String) -> String {
        Data(SHA256.hash(data: Data(verifier.utf8))).base64URLEncodedString()
    }
}

/// `POST /v1/auth/enroll`'s answer.
public struct Enrollment: Codable, Equatable, Sendable {
    public var id: String
    public var code: String
    public var verificationURL: URL
    public var expires: Date
    public var interval: Int
}

/// An error answer of the relay's HTTP API: `{code, message}` and the
/// status. `code` is empty when the body was not the relay's.
public struct RelayHTTPError: Error, Equatable, Sendable {
    public var status: Int
    public var code: String

    /// The relay refused the refresh credential for good: only signing in
    /// again helps (401 `unauthorized`, 403 `not_allowed`).
    public var signInRequired: Bool {
        (status == 401 && code == "unauthorized") || (status == 403 && code == "not_allowed")
    }
}

/// The relay's enrollment and device endpoints. Never follows redirects.
public struct RelayAPI: Sendable {
    public let origin: URL
    let session: URLSession

    /// The registered callback of the iPhone app.
    public static let callback = "hesper-ios://auth"
    public static let callbackScheme = "hesper-ios"

    public init(origin: URL, session: URLSession = RelayAPI.session) {
        self.origin = origin
        self.session = session
    }

    /// An ephemeral session that refuses redirects, so no secret follows one.
    public static let session: URLSession = {
        let config = URLSessionConfiguration.ephemeral
        config.timeoutIntervalForRequest = 20
        config.requestCachePolicy = .reloadIgnoringLocalCacheData
        return URLSession(configuration: config, delegate: NoRedirects(), delegateQueue: nil)
    }()

    /// `POST /v1/auth/enroll` as a controller with the app's callback.
    /// `previousDevice` asks the relay to keep an earlier device id.
    public func enroll(name: String, pkce: PKCE, previousDevice: PreviousDevice? = nil) async throws -> Enrollment {
        struct Body: Encodable {
            var name: String
            var role = "controller"
            var challenge: String
            var callback = RelayAPI.callback
            var previousDevice: PreviousDevice?
        }
        let body = Body(name: name, challenge: pkce.challenge, previousDevice: previousDevice)
        return try await post("/v1/auth/enroll", body: body, as: Enrollment.self)
    }

    /// One `POST /v1/auth/claim`; nil while the approval is pending (409).
    public func claim(id: String, verifier: String) async throws -> Credentials? {
        do {
            var c = try await post("/v1/auth/claim", body: ["id": id, "verifier": verifier], as: Credentials.self)
            c.relay = origin.absoluteString
            return c
        } catch let e as RelayHTTPError where e.status == 409 {
            return nil
        }
    }

    /// `POST /v1/auth/refresh`: rotates both secrets once. The current
    /// access token goes along as proof for the relay's lost-response grace.
    public func refresh(_ c: Credentials) async throws -> Credentials {
        guard let refreshToken = c.refreshToken else { throw RelayHTTPError(status: 401, code: "unauthorized") }
        var next = try await post("/v1/auth/refresh", body: ["refreshToken": refreshToken], bearer: c.token, as: Credentials.self)
        next.relay = c.relay
        next.login = next.login ?? c.login
        guard !next.token.isEmpty, next.refreshToken?.isEmpty == false, next.deviceId == c.deviceId else {
            throw RelayHTTPError(status: 0, code: "incomplete")
        }
        return next
    }

    /// `POST /v1/devices/push`: the APNs token of this device.
    public func registerPush(token: Data, environment: String, bearer: String) async throws {
        struct Body: Encodable {
            var token: String
            var environment: String
        }
        _ = try await send("/v1/devices/push", body: Body(token: token.hex, environment: environment), bearer: bearer)
    }

    func post<T: Decodable>(_ path: String, body: some Encodable, bearer: String? = nil, as: T.Type) async throws -> T {
        try Credentials.decoder.decode(T.self, from: await send(path, body: body, bearer: bearer))
    }

    func send(_ path: String, body: some Encodable, bearer: String?) async throws -> Data {
        var request = URLRequest(url: origin.appending(path: path))
        request.httpMethod = "POST"
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        if let bearer { request.setValue("Bearer \(bearer)", forHTTPHeaderField: "Authorization") }
        request.httpBody = try JSONEncoder().encode(body)
        let (data, response) = try await session.data(for: request)
        let status = (response as? HTTPURLResponse)?.statusCode ?? 0
        guard status == 200 || status == 201 || status == 204 else {
            let fault = (try? JSONSerialization.jsonObject(with: data)) as? [String: Any]
            throw RelayHTTPError(status: status, code: fault?["code"] as? String ?? "")
        }
        return data
    }
}

/// The proof of an earlier device key for re-enrollment
/// (docs/iphone/onboarding.md, "Interfaces"): the relay keeps `id` when
/// `sig` verifies with that device's key. The device key signs
/// `"hesper-reenroll-v1\n" + id + "\n" + challenge + "\n" + ts`.
public struct PreviousDevice: Codable, Equatable, Sendable {
    public var id: String
    public var ts: Int64
    public var sig: String

    public static func message(id: String, challenge: String, ts: Int64) -> Data {
        Data(["hesper-reenroll-v1", id, challenge, String(ts)].joined(separator: "\n").utf8)
    }

    public static func sign(id: String, challenge: String, key: any SigningKey, now: Date = Date()) throws -> PreviousDevice {
        let ts = Int64((now.timeIntervalSince1970 * 1000).rounded(.down))
        let sig = try key.signature(for: message(id: id, challenge: challenge, ts: ts))
        return PreviousDevice(id: id, ts: ts, sig: sig.base64EncodedString())
    }
}

/// Where the relay's sign-in page ended: the app callback's query.
public enum EnrollmentCallback: Equatable, Sendable {
    case approved
    case denied
    case notAllowed(login: String)
    case other(String)

    /// Reads `hesper-ios://auth?enrollment=…&status=…` (or the older
    /// `approved=1`) for enrollment `id`; nil when it is about another one.
    public init?(_ url: URL, enrollment id: String) {
        guard let items = URLComponents(url: url, resolvingAgainstBaseURL: false)?.queryItems else { return nil }
        let query = Dictionary(items.map { ($0.name, $0.value ?? "") }, uniquingKeysWith: { a, _ in a })
        guard query["enrollment"] == id else { return nil }
        switch query["status"] ?? (query["approved"] == "1" ? "approved" : "") {
        case "approved": self = .approved
        case "denied": self = .denied
        case "not_allowed": self = .notAllowed(login: query["login"] ?? "")
        case let s: self = .other(s)
        }
    }
}

private final class NoRedirects: NSObject, URLSessionTaskDelegate {
    func urlSession(
        _ session: URLSession, task: URLSessionTask, willPerformHTTPRedirection response: HTTPURLResponse,
        newRequest request: URLRequest, completionHandler: @escaping @Sendable (URLRequest?) -> Void
    ) { completionHandler(nil) }
}

/// The phone's credentials: kept in the Keychain, renewed one at a time.
/// The new pair is stored before anyone uses it.
public actor CredentialStore {
    private let store: any SecretStore
    private var cached: Credentials?
    private var renewal: Task<Credentials, any Error>?
    private let api: @Sendable (URL) -> RelayAPI

    static let account = "relay-credentials"

    public init(store: any SecretStore, api: @escaping @Sendable (URL) -> RelayAPI = { RelayAPI(origin: $0) }) {
        self.store = store
        self.api = api
    }

    public func current() -> Credentials? {
        if cached == nil, let data = try? store.read(Self.account) {
            cached = try? Credentials.decoder.decode(Credentials.self, from: data)
        }
        return cached
    }

    public func save(_ c: Credentials) throws {
        try store.write(Credentials.encoder.encode(c), account: Self.account)
        cached = c
    }

    public func clear() {
        try? store.delete(Self.account)
        cached = nil
        renewal?.cancel()
        renewal = nil
    }

    /// Credentials good for at least another minute, renewing them if not.
    /// Concurrent callers share one renewal. Throws `RelayHTTPError` with
    /// `signInRequired` when only a new sign-in helps.
    public func fresh(now: Date = Date()) async throws -> Credentials {
        guard let c = current() else { throw RelayHTTPError(status: 401, code: "unauthorized") }
        guard let expires = c.expiresAt, expires.timeIntervalSince(now) <= 60 else { return c }
        if let renewal { return try await renewal.value }
        let api = api
        guard let origin = URL(string: c.relay) else { throw RelayHTTPError(status: 0, code: "invalid_relay") }
        let task = Task { try await api(origin).refresh(c) }
        renewal = task
        defer { renewal = nil }
        let next = try await task.value
        try save(next)
        return next
    }

    /// Forces the next `fresh()` to renew (the socket was refused).
    public func expire() {
        guard var c = current() else { return }
        c.expiresAt = .distantPast
        cached = c
    }
}
