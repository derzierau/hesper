import Foundation

/// How the transcript view follows its stream across failures, kept pure
/// so it is tested without a link.
public enum TranscriptFollow {
    /// The first wait after an open failed or the link broke; it doubles
    /// up to `longestWait`.
    public static let firstWait: Duration = .seconds(1)
    public static let longestWait: Duration = .seconds(10)
    /// The wait after the daemon answered `unavailable`: the agent has no
    /// transcript file yet, so it is not the link that failed.
    public static let missingWait: Duration = .seconds(5)

    /// The Mac's daemon has no transcripts: `unsupported`, or `forbidden`
    /// from an older daemon whose rights table lacks the method ("Unknown
    /// method"). Asking again cannot help. Any other `forbidden` is a
    /// rights refusal, not an old daemon (as relay/internal/remote/bring.go
    /// tells them apart); its message is what the Mac said.
    public static func isUnsupported(_ error: any Error) -> Bool {
        guard let coded = error as? any CodedError else { return false }
        return coded.code == "unsupported"
            || coded.code == "forbidden" && String(describing: error).contains("Unknown method")
    }

    /// The wait before opening again after `error`, or nil to stop
    /// (`isUnsupported`). `previous` is the wait before this failure, nil
    /// after a stream that opened.
    public static func wait(after error: any Error, previous: Duration?) -> Duration? {
        if isUnsupported(error) { return nil }
        // A rights refusal stays one until the owner changes the rights.
        if let coded = error as? any CodedError, coded.code == "forbidden" { return nil }
        if let coded = error as? any CodedError, coded.code == "unavailable" { return missingWait }
        return next(previous)
    }

    /// The wait before reopening after the `consecutive`th overflow in a
    /// row (1-based): none for the first, a burst; then `firstWait`
    /// doubling up to `longestWait`, so a sustained burst cannot spin
    /// against the relay. The store counts from 0 again once a stream
    /// ends any other way.
    public static func overflowWait(consecutive: Int) -> Duration {
        guard consecutive > 1 else { return .zero }
        var wait = firstWait
        for _ in 2..<min(consecutive, 8) { wait = next(wait) }
        return wait
    }

    /// The wait after `previous`: `firstWait`, then doubling up to
    /// `longestWait`.
    public static func next(_ previous: Duration?) -> Duration {
        guard let previous else { return firstWait }
        return min(previous * 2, longestWait)
    }

    /// What the phone holds is from another session than `now`, so it
    /// starts over. An unknown `now` (the Mac has not said yet) keeps it.
    public static func startsOver(held: String?, now: String?) -> Bool {
        guard let now else { return false }
        return held != nil && held != now
    }
}
