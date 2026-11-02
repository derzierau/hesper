import Foundation
import Synchronization

@testable import HesperLink

/// A fake relay for the HTTP endpoints: each test registers a handler for
/// its own host, so tests running in parallel do not see each other.
final class FakeHTTP: URLProtocol {
    typealias Handler = @Sendable (URLRequest, Data) -> (Int, Data)

    private static let handlers = Mutex<[String: Handler]>([:])

    /// A fresh origin served by `handler`, and an API on it.
    static func relay(_ handler: @escaping Handler) -> RelayAPI {
        let host = "r\(UInt32.random(in: 0...UInt32.max)).relay.test"
        handlers.withLock { $0[host] = handler }
        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [FakeHTTP.self]
        return RelayAPI(origin: URL(string: "https://\(host)")!, session: URLSession(configuration: config))
    }

    static func json(_ status: Int, _ object: Any) -> (Int, Data) {
        (status, try! JSONSerialization.data(withJSONObject: object))
    }

    static func body(_ request: URLRequest) -> Data {
        if let body = request.httpBody { return body }
        guard let stream = request.httpBodyStream else { return Data() }
        stream.open()
        defer { stream.close() }
        var data = Data()
        var buffer = [UInt8](repeating: 0, count: 4096)
        while stream.hasBytesAvailable {
            let n = stream.read(&buffer, maxLength: buffer.count)
            if n <= 0 { break }
            data.append(buffer, count: n)
        }
        return data
    }

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }

    override func startLoading() {
        let handler = Self.handlers.withLock { $0[request.url?.host() ?? ""] }
        let (status, data) = handler?(request, Self.body(request)) ?? (404, Data())
        let response = HTTPURLResponse(url: request.url!, statusCode: status, httpVersion: "HTTP/1.1", headerFields: nil)!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: data)
        client?.urlProtocolDidFinishLoading(self)
    }

    override func stopLoading() {}
}

func jsonObject(_ data: Data) -> [String: Any] {
    (try? JSONSerialization.jsonObject(with: data)) as? [String: Any] ?? [:]
}
