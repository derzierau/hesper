import Foundation
import HesperCore

/// What arrives from the paired Macs.
public enum LinkUpdate: Equatable, Sendable {
    case event(LinkEvent, machine: String)
    case reachability(Reachability, machine: String)
    /// The paired Macs (inventory), on start and when it changes.
    case macs([Machine])
    /// A Mac's drafts (`drafts.list`): replaces what that Mac had.
    case drafts([Draft], machine: String)
    /// `drafts.changed`: one draft, new or updated.
    case draftChanged(Draft, machine: String)
    /// `drafts.removed`: the draft was started or thrown away.
    case draftRemoved(id: String, machine: String)
}

/// A verb the phone sends to the agent's Mac (relay/docs/protocol.md,
/// "Methods").
public enum LinkCommand: Equatable, Sendable {
    /// `agents.answer` with a decision (Allow, Always, Deny). A deny may
    /// carry a message: hesperd types it after the deny, telling the agent
    /// what to do instead.
    case answer(agent: String, Decision, message: String? = nil)
    /// `agents.answer` with a question's choice, by its number.
    case choose(agent: String, option: Int)
    /// `agents.input`: a whole message, sent with return.
    case reply(agent: String, text: String)
    case spawn(SpawnRequest)
    case stop(agent: String)
    case resume(agent: String)
    case close(agent: String)
    /// `drafts.save` on `machine`: a task kept for later, not started.
    case saveDraft(Draft, machine: String)
    /// `drafts.remove` on `machine`.
    case removeDraft(id: String, machine: String)

    /// The agent it acts on; nil for spawn and drafts.
    public var agent: String? {
        switch self {
        case .answer(let a, _, _), .choose(let a, _), .reply(let a, _), .stop(let a), .resume(let a), .close(let a): a
        case .spawn, .saveDraft, .removeDraft: nil
        }
    }
}

/// The phone's only way to the Macs. The live client (end-to-end through
/// the owner's relay, or direct on the LAN) comes with the mobile link
/// library; the base project runs on `FixtureLink`.
public protocol LinkClient: Sendable {
    /// Updates from every paired Mac, until the stream is cancelled.
    func updates() -> AsyncStream<LinkUpdate>
    /// Sends one command. Throws when the Mac refused it or did not answer;
    /// never retried automatically.
    func perform(_ command: LinkCommand) async throws

    /// Watches an agent's terminal at `cols`×`rows`. Mirror (`owner`
    /// false): a read-only attach at the Mac's grid. Fitted (`owner`
    /// true): a read-write attach that makes the phone the size owner, so
    /// the PTY takes this grid (`AttachPlan`); it sends RESIZE, never
    /// bytes. Throws when the Mac refused it or is not linked. The stream
    /// ends without `.exit` when the link broke: show "Reconnecting…" and
    /// attach again (it waits for the relink).
    func attach(agent: String, cols: Int, rows: Int, owner: Bool) async throws -> any AttachStream

    /// What `machine` lets this phone do with a terminal's size; nil when
    /// the Mac is not known (yet).
    func sizing(on machine: String) -> HostSizing?
}

extension LinkClient {
    /// A mirror attach.
    public func attach(agent: String, cols: Int, rows: Int) async throws -> any AttachStream {
        try await attach(agent: agent, cols: cols, rows: rows, owner: false)
    }
}

/// An error with the Mac's code (`forbidden`, `unsupported`, …), so the
/// fleet can decide on it without knowing the link's error type.
public protocol CodedError: Error {
    var code: String { get }
}

/// What a watched terminal sends (relay/pkg/wire/frame.go): its bytes,
/// its grid size (first from the attach's reply, then at every resize), a
/// scroll state, and its exit, the last frame.
public enum AttachFrame: Equatable, Sendable {
    case data(Data)
    case size(cols: Int, rows: Int)
    case exit
    case scroll
}

/// An open attach. `frames` ends after `.exit`, when the Mac closed it,
/// when the link broke, or after `close()`.
public protocol AttachStream: Sendable {
    var frames: AsyncStream<AttachFrame> { get }
    /// Asks the Mac to size the terminal to `cols`×`rows` (RESIZE). Only
    /// the size owner's counts; the Mac ignores a mirror's.
    func resize(cols: Int, rows: Int)
    /// Ends the attach; the Mac stops sending. Idempotent.
    func close()
}
