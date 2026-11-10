import Foundation
import HesperCore

/// Why a transcript stream ended.
public enum TranscriptEnd: Equatable, Sendable {
    /// The Mac closed the channel (`X`): its transcript changed under the
    /// stream, so what the phone holds is stale.
    case closedByMac
    /// The link broke; the stream reopens after its last item.
    case linkLost
    /// The phone's bounded queue filled; reopen after the last item read.
    case overflow
    /// A Mac sent an item above the wire limit; the stream cannot skip it.
    case oversized
    /// The phone closed it, or it never opened.
    case closed
}

/// An open transcript stream: new items as the agent writes them. `items`
/// ends when the Mac closed it, when the link broke, or after `close()`.
public protocol TranscriptStream: Sendable {
    var items: AsyncStream<TranscriptItem> { get }
    /// Why `items` ended; nil while it runs.
    var ended: TranscriptEnd? { get }
    /// Ends the stream; the Mac stops sending. Idempotent.
    func close()
}
