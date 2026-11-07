import Foundation
import HesperFleet
import Synchronization

/// The host's answer line on an attach channel (Go's wire.AttachReply):
/// ok with the PTY's size, or an error.
public struct AttachReply: Equatable, Sendable, Decodable {
    public var ok: Bool
    public var cols: Int?
    public var rows: Int?
    public var error: RelayError?

    public init(ok: Bool, cols: Int? = nil, rows: Int? = nil, error: RelayError? = nil) {
        self.ok = ok
        self.cols = cols
        self.rows = rows
        self.error = error
    }

    enum CodingKeys: String, CodingKey { case ok, cols, rows, error }
    struct Failure: Decodable {
        var code: String?
        var message: String?
    }

    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        ok = try c.decodeIfPresent(Bool.self, forKey: .ok) ?? false
        cols = try c.decodeIfPresent(Int.self, forKey: .cols)
        rows = try c.decodeIfPresent(Int.self, forKey: .rows)
        // hesperd sends {code, message}; a bare string is taken as the code.
        if let f = try? c.decodeIfPresent(Failure.self, forKey: .error) {
            error = RelayError(code: f.code ?? "", message: f.message ?? "")
        } else if let code = try? c.decodeIfPresent(String.self, forKey: .error) {
            error = RelayError(code: code, message: "")
        }
    }
}

/// The bytes of an attach channel's `D` frames, decoded: first the reply
/// line (JSON, newline-terminated), then frames of `type (1 byte) | length
/// (uint32 BE) | payload` (relay/pkg/wire/frame.go), split anywhere
/// across `D` frames.
public struct AttachDecoder: Sendable {
    /// The reply line's bound (it is a few dozen bytes) and a frame's
    /// (wire.MaxFrame).
    static let maxReply = 64 << 10, maxFrame = 1 << 24
    private var buffer = Data()
    private var replied = false

    public init() {}

    /// Appends bytes and returns the reply (once) and the frames they
    /// complete. Throws `integrity` on a malformed stream.
    public mutating func append(_ bytes: Data) throws -> (reply: AttachReply?, frames: [AttachFrame]) {
        buffer += bytes
        var reply: AttachReply?
        if !replied {
            guard let nl = buffer.firstIndex(of: 10) else {
                guard buffer.count <= Self.maxReply else { throw RelayError(code: "integrity", message: "Attach reply too long") }
                return (nil, [])
            }
            let line = buffer[buffer.startIndex..<nl]
            guard let r = try? JSONDecoder().decode(AttachReply.self, from: Data(line)) else {
                throw RelayError(code: "integrity", message: "Invalid attach reply")
            }
            reply = r
            replied = true
            buffer = Data(buffer[(nl + 1)...])
        }
        var frames: [AttachFrame] = []
        while buffer.count >= 5 {
            let b = [UInt8](buffer.prefix(5))
            let length = Int(b[1...4].reduce(UInt32(0)) { $0 << 8 | UInt32($1) })
            guard length <= Self.maxFrame else { throw RelayError(code: "integrity", message: "Attach frame too large") }
            guard buffer.count >= 5 + length else { break }
            let start = buffer.startIndex
            let payload = Data(buffer[start + 5..<start + 5 + length])
            buffer = Data(buffer[(start + 5 + length)...])
            if let f = try Self.frame(type: b[0], payload) { frames.append(f) }
        }
        return (reply, frames)
    }

    /// A RESIZE frame (type 1, relay/pkg/wire/frame.go): cols and rows as
    /// uint16 BE. The phone's only frame to the Mac; only the size owner's
    /// counts.
    public static func resize(cols: Int, rows: Int) -> Data {
        var out = Data([1, 0, 0, 0, 4])
        for v in [cols, rows] {
            let n = UInt16(clamping: v)
            out.append(UInt8(n >> 8))
            out.append(UInt8(n & 0xff))
        }
        return out
    }

    /// One frame; nil for RESIZE (it only goes to the Mac) and unknown types.
    static func frame(type: UInt8, _ payload: Data) throws -> AttachFrame? {
        switch type {
        case 0: return .data(payload)
        case 2:
            guard payload.count == 4 else { throw RelayError(code: "integrity", message: "Invalid size frame") }
            let b = [UInt8](payload)
            return .size(cols: Int(b[0]) << 8 | Int(b[1]), rows: Int(b[2]) << 8 | Int(b[3]))
        case 3: return .exit
        case 4: return .scroll
        default: return nil
        }
    }
}

/// One attach on a channel of a host's link. The link's reader feeds it;
/// `resize` sends RESIZE, `close()` sends `X` and ends the stream.
public final class AttachChannel: AttachStream {
    public let id: UInt32
    public let frames: AsyncStream<AttachFrame>
    private let continuation: AsyncStream<AttachFrame>.Continuation
    private let onClose: @Sendable (UInt32) -> Void
    private let onResize: @Sendable (UInt32, Int, Int) -> Void

    private struct State {
        var decoder = AttachDecoder()
        var reply: Result<AttachReply, RelayError>?
        var waiter: CheckedContinuation<AttachReply, any Error>?
        var ended = false
    }
    private let state = Mutex(State())

    init(
        id: UInt32, onClose: @escaping @Sendable (UInt32) -> Void,
        onResize: @escaping @Sendable (UInt32, Int, Int) -> Void = { _, _, _ in }
    ) {
        self.id = id
        self.onClose = onClose
        self.onResize = onResize
        (frames, continuation) = AsyncStream.makeStream(of: AttachFrame.self, bufferingPolicy: .unbounded)
    }

    /// The payload of one `D` frame for this channel. A malformed stream
    /// ends the attach and closes the channel.
    func receive(_ payload: Data) {
        let out = state.withLock { s -> Result<(reply: AttachReply?, frames: [AttachFrame]), RelayError>? in
            guard !s.ended else { return nil }
            do {
                return .success(try s.decoder.append(payload))
            } catch {
                return .failure(error as? RelayError ?? RelayError(code: "integrity", message: "\(error)"))
            }
        }
        switch out {
        case .none: return
        case .failure(let e):
            close(e)
        case .success(let (reply, frames)):
            if let reply {
                if reply.ok, let cols = reply.cols, let rows = reply.rows, cols > 0, rows > 0 {
                    continuation.yield(.size(cols: cols, rows: rows))
                }
                resolve(.success(reply))
            }
            for f in frames { continuation.yield(f) }
            if frames.contains(.exit) { end(RelayError(code: "closed", message: "The agent exited")) }
        }
    }

    /// Waits for the reply line; fails when the channel ends first.
    func reply() async throws -> AttachReply {
        try await withCheckedThrowingContinuation { c in
            let ready = state.withLock { s -> Result<AttachReply, RelayError>? in
                if let r = s.reply { return r }
                s.waiter = c
                return nil
            }
            if let ready { c.resume(with: ready) }
        }
    }

    private func resolve(_ r: Result<AttachReply, RelayError>) {
        let waiter = state.withLock { s -> CheckedContinuation<AttachReply, any Error>? in
            guard s.reply == nil else { return nil }
            s.reply = r
            defer { s.waiter = nil }
            return s.waiter
        }
        waiter?.resume(with: r.mapError { $0 })
    }

    /// Ends the stream without telling the host (it closed the channel,
    /// or the link is gone). A pending reply fails with `reason`. True
    /// the first time.
    @discardableResult func end(_ reason: RelayError) -> Bool {
        let first = state.withLock { s in
            defer { s.ended = true }
            return !s.ended
        }
        guard first else { return false }
        resolve(.failure(reason))
        continuation.finish()
        return true
    }

    private func close(_ reason: RelayError) {
        if end(reason) { onClose(id) }
    }

    public func close() { close(RelayError(code: "closed", message: "Closed")) }

    public func resize(cols: Int, rows: Int) {
        guard cols > 0, rows > 0, !state.withLock({ $0.ended }) else { return }
        onResize(id, cols, rows)
    }
}
