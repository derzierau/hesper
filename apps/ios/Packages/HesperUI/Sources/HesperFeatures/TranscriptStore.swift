import Foundation
import HesperCore
import HesperFleet
import Observation

/// An agent's history for the transcript view: the transcript stream
/// folded into a `TranscriptLog`, older pages on request. A shell agent
/// has no transcript; its scrollback is read once instead (`shellText`).
@MainActor
@Observable
public final class TranscriptStore {
    public enum Phase: Equatable {
        case loading
        case live
        /// The Mac's daemon has no transcripts: "History needs a newer
        /// Hesper on <machine>".
        case unsupported(machine: String)
        case unavailable(String)
        case reconnecting
    }

    public private(set) var log = TranscriptLog()
    public private(set) var phase: Phase = .loading
    /// Items appended while the person was scrolled up.
    public private(set) var unseen = 0
    /// The newest item is on screen; scrolling back to it clears `unseen`.
    public var atBottom = true {
        didSet {
            if atBottom { unseen = 0 }
            // While the person reads older history, keep what they paged in.
            log.keepsOlder = !atBottom
        }
    }
    /// The oldest item is on screen: a busy page is asked for again.
    @ObservationIgnored public var atTop = false
    /// A shell agent's scrollback, one string per line, read at open.
    public private(set) var shellText: [String]?
    /// Tool calls whose result is unfolded, by seq.
    public private(set) var open: Set<Int64> = []

    /// A page of older items is being fetched.
    @ObservationIgnored private var loadingOlder = false
    /// Counts resets, so a page asked for before one is dropped.
    @ObservationIgnored private var resets = 0
    /// The session the log holds, as the agent said when the stream opened.
    @ObservationIgnored private var session: String?
    /// The open stream, closed when the session changes.
    @ObservationIgnored private var stream: (any TranscriptStream)?
    /// The stream was closed for a new session: open again at once.
    @ObservationIgnored private var reopenNow = false
    /// Rows' text, parsed once per item (`text(_:)`); cleared on reset.
    @ObservationIgnored private var rendered: [Int64: AttributedString] = [:]

    /// The last transcript line held; a marker (negative seq) is not one.
    /// The stream reopens after it.
    private var lastLineSeq: Int64? {
        guard let last = log.lastSeq, last >= 0 else { return nil }
        return last
    }

    /// Items asked for per stream open and per older page.
    static let pageSize = 200
    /// Lines of a shell agent's scrollback read at open.
    static let shellScrollback = 2000
    /// Wait before asking a busy Mac for an older page again.
    static let busyWait: Duration = .seconds(1)

    public init() {}

    /// Follows `agent`'s transcript until the task is cancelled. It opens
    /// again after the last item held when the link breaks or an open
    /// fails (`TranscriptFollow`), and from the start when the Mac closed
    /// the stream or the session changed. Only a Mac without transcripts
    /// stops it. A shell agent's scrollback is read once.
    public func run(_ agent: Agent, model: FleetModel) async {
        if agent.kind == "shell" {
            await readShell(agent, model: model)
            return
        }
        reopenNow = false
        if log.items.isEmpty { phase = .loading }
        var wait: Duration?
        // Streams in a row that ended on a full queue; any other end of a
        // stream starts the count again.
        var overflows = 0
        while !Task.isCancelled {
            let now = model.agent(agent.id)?.sessionId ?? agent.sessionId
            if TranscriptFollow.startsOver(held: session, now: now) { startOver() }
            if now != nil { session = now }
            let stream: any TranscriptStream
            // A session change during the open has no stream to close
            // yet; its reset shows in `resets`, and the stream opened
            // after the old session's last line is dropped below.
            let resetsBefore = resets
            do {
                stream = try await model.transcriptStream(agent, after: lastLineSeq, limit: Self.pageSize)
            } catch {
                guard !Task.isCancelled else { return }
                guard let next = TranscriptFollow.wait(after: error, previous: wait) else {
                    fail(error, agent)
                    return
                }
                wait = next
                // The daemon's `unavailable` has its message; the rest is
                // the link coming back.
                if let coded = error as? any CodedError, coded.code == "unavailable" {
                    phase = .unavailable(SafeLinks.visible(String(describing: error)))
                } else {
                    phase = .reconnecting
                }
                guard (try? await Task.sleep(for: next)) != nil else { return }
                continue
            }
            wait = nil
            guard resets == resetsBefore else {
                stream.close()
                continue
            }
            // The first open fills the newest page; whether the Mac holds
            // more is only known once `loadOlder` asks.
            if log.items.isEmpty { log.hasOlder = true }
            phase = .live
            self.stream = stream
            await withTaskCancellationHandler {
                await consume(stream)
            } onCancel: {
                stream.close()
            }
            self.stream = nil
            stream.close()
            guard !Task.isCancelled else { return }
            // A full queue is a burst, not a broken link: reopen after
            // the last item held without a status change, at once the
            // first time and backing off on a run of them.
            if stream.ended == .overflow {
                // The loop reopens anyway; a session change is handled at
                // its top.
                reopenNow = false
                overflows += 1
                let pause = TranscriptFollow.overflowWait(consecutive: overflows)
                if pause > .zero { guard (try? await Task.sleep(for: pause)) != nil else { return } }
                continue
            }
            overflows = 0
            if reopenNow {
                reopenNow = false
                continue
            }
            // The Mac closed it: its transcript changed under the stream.
            if stream.ended == .closedByMac { startOver() }
            if stream.ended == .oversized {
                phase = .unavailable("This transcript item needs a newer Hesper on \(agent.machine).")
                return
            }
            phase = .reconnecting
            guard (try? await Task.sleep(for: TranscriptFollow.firstWait)) != nil else { return }
        }
    }

    /// The agent's session changed while the view is open (the view's
    /// `onChange(of: agent.sessionId)`): drop what is held and open the
    /// stream again from the newest page.
    public func sessionChanged(_ id: String?) {
        guard TranscriptFollow.startsOver(held: session, now: id) else { return }
        session = id
        startOver()
        guard let stream else { return }
        reopenNow = true
        stream.close()
    }

    /// Prepends the page before the first line held. One at a time; a
    /// failed page leaves `hasOlder` as it was, so the next scroll to the
    /// top asks again. A busy Mac is asked again while the top is shown.
    public func loadOlder(_ agent: Agent, model: FleetModel) async {
        guard !loadingOlder, log.hasOlder, !log.items.isEmpty else { return }
        loadingOlder = true
        defer { loadingOlder = false }
        let resetsBefore = resets
        while !Task.isCancelled {
            do {
                // Only a marker held: the newest page of the new transcript.
                let page = try await model.transcript(agent, before: log.firstLineSeq, limit: Self.pageSize)
                guard resets == resetsBefore else { return }
                log.insert(page.items)
                log.hasOlder = page.more
                return
            } catch let e as any CodedError where e.code == "busy" {
                guard (try? await Task.sleep(for: Self.busyWait)) != nil, atTop, resets == resetsBefore else { return }
            } catch {
                return
            }
        }
    }

    /// Unfolds or folds a tool call's result.
    public func toggle(_ seq: Int64) {
        if open.contains(seq) { open.remove(seq) } else { open.insert(seq) }
    }

    /// `item`'s text as a row shows it, parsed by `parse` the first time.
    func text(_ item: TranscriptItem, _ parse: (String) -> AttributedString) -> AttributedString {
        if let done = rendered[item.seq] { return done }
        let text = parse(item.text ?? "")
        // Bounded like the log: past it, rows parse again as they show.
        if rendered.count >= TranscriptLog.maxItems { rendered.removeAll(keepingCapacity: true) }
        rendered[item.seq] = text
        return text
    }

    private func consume(_ stream: any TranscriptStream) async {
        var generation = log.generation
        for await item in stream.items {
            // A session change can finish a stream with old items still in
            // its buffer. Leave them behind with the old generation.
            guard generation == log.generation else { return }
            // A marker restarts the log (`TranscriptLog.follow`); what the
            // view made from the old items goes with it.
            let (added, restarted) = log.follow(item)
            if restarted {
                startOver(logRestarted: true)
                generation = log.generation
                continue
            }
            if !atBottom { unseen += added }
        }
    }

    /// Drops everything held (`TranscriptLog.restart`) and what was made
    /// from it. `logRestarted`: the log already did (`TranscriptLog.follow`).
    private func startOver(marker: TranscriptItem? = nil, logRestarted: Bool = false) {
        if !logRestarted { log.restart(marker: marker) }
        resets += 1
        unseen = 0
        open = []
        rendered = [:]
    }

    private func readShell(_ agent: Agent, model: FleetModel) async {
        phase = .loading
        do {
            let screen = try await model.screen(agent, rows: 0, scrollback: Self.shellScrollback)
            var lines = screen.text.split(separator: "\n", omittingEmptySubsequences: false).map(String.init)
            while lines.last?.allSatisfy(\.isWhitespace) == true { lines.removeLast() }
            shellText = lines
            phase = .live
        } catch {
            if !Task.isCancelled { fail(error, agent) }
        }
    }

    private func fail(_ error: any Error, _ agent: Agent) {
        if TranscriptFollow.isUnsupported(error) {
            phase = .unsupported(machine: agent.machine)
        } else {
            phase = .unavailable(SafeLinks.visible(String(describing: error)))
        }
    }
}
