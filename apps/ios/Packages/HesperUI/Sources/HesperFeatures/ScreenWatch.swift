import HesperCore
import HesperFleet
import HesperSurfaces
import Observation
import SwiftUI

/// The live section of the transcript view: the agent's screen watched as
/// a view attach (it never sizes the Mac's terminal), folded into
/// `ScreenRows`. Typing reaches the agent when the Mac allows it.
@MainActor
@Observable
public final class ScreenWatch {
    public enum Phase: Equatable {
        case loading
        case live
        case reconnecting
        /// The agent exited; the last screen stays.
        case exited
    }

    public private(set) var rows = ScreenRows(size: FleetModel.fallbackSize)
    public private(set) var phase: Phase = .loading
    /// The open attach may type: `send` reaches the agent.
    public private(set) var canType = false
    /// The agent is starting or working (its last state).
    public private(set) var working = false

    /// The terminal's active input row stays below the scroll when its
    /// typing attach is live. The other visible rows stay in the scroll.
    public func partition(pinInput: Bool) -> LiveRows.Partition {
        let texts = rows.rows.indices.map { rows.text(row: $0) }
        return LiveRows.partition(
            rowTexts: texts, cursorRow: rows.cursor?.row, working: showsWorking || shell,
            pinInput: pinInput && phase == .live && canType)
    }

    /// `working` as `visible` uses it: it turns false only after the delay.
    private var showsWorking = false
    /// A shell agent: it has no transcript above, so all its rows show.
    private var shell = false
    /// The open attach, for typing.
    @ObservationIgnored private var stream: (any AttachStream)?
    /// The wait before `showsWorking` follows `working` to false.
    @ObservationIgnored private var shrink: Task<Void, Never>?
    /// `stop()` was called: the run loop does not attach again.
    @ObservationIgnored private var stopped = false
    /// The grid the open attach asked for.
    @ObservationIgnored private var attachedSize: GridSize?
    /// The attach was closed to open again at the agent's new size.
    @ObservationIgnored private var resizing = false

    /// Wait before attaching again after the link broke or the Mac refused.
    static let backoff: Duration = .seconds(1)

    public init() {}

    /// Attaches and follows the screen until the agent exits, `stop()` or
    /// the task is cancelled, attaching again whenever the link breaks.
    public func run(_ agent: Agent, model: FleetModel) async {
        stopped = false
        resizing = false
        // A shrink left from an earlier run must not fire into this one.
        shrink?.cancel()
        shrink = nil
        defer {
            shrink?.cancel()
            shrink = nil
        }
        phase = .loading
        working = Self.isWorking(agent.state)
        showsWorking = working
        shell = agent.kind == "shell"
        // The Mac refused a typing attach: later ones do not ask again.
        var refused = false
        while !Task.isCancelled, !stopped {
            let size = model.agent(agent.id)?.size ?? agent.size ?? FleetModel.fallbackSize
            // Read at every attach: the Mac's rights may have changed.
            let typing = !refused && (model.sizing(on: agent.machine)?.canType ?? false)
            let stream: any AttachStream
            let typed: Bool
            do {
                (stream, typed) = try await open(agent, size: size, typing: typing, model: model)
                if typing, !typed { refused = true }
            } catch {
                guard !Task.isCancelled, !stopped else { return }
                if model.agent(agent.id)?.state == .exited {
                    phase = .exited
                    return
                }
                // There is no reason case to show here: the section reads
                // "Reconnecting…" and asks again.
                phase = .reconnecting
                guard (try? await Task.sleep(for: Self.backoff)) != nil else { return }
                continue
            }
            // `stop()` or a cancel while the attach was opening: drop it.
            guard !Task.isCancelled, !stopped else {
                stream.close()
                return
            }
            rows = ScreenRows(size: size)
            self.stream = stream
            attachedSize = size
            canType = typed
            phase = .live
            let exited = await withTaskCancellationHandler {
                await consume(stream)
            } onCancel: {
                stream.close()
            }
            self.stream = nil
            attachedSize = nil
            canType = false
            stream.close()
            if exited {
                phase = .exited
                return
            }
            guard !Task.isCancelled, !stopped else { return }
            if resizing {
                resizing = false
                continue
            }
            // The frames ended without `.exit`: the link broke.
            phase = .reconnecting
            guard (try? await Task.sleep(for: Self.backoff)) != nil else { return }
        }
    }

    /// The agent's state changed. Turning to work shows all rows at once;
    /// leaving it shrinks the section after `LiveRows.shrinkDelay`, unless
    /// work starts again first.
    public func stateChanged(_ state: AgentState) {
        let now = Self.isWorking(state)
        guard now != working else { return }
        working = now
        shrink?.cancel()
        shrink = nil
        if now {
            showsWorking = true
            return
        }
        shrink = Task { [weak self] in
            guard (try? await Task.sleep(for: LiveRows.shrinkDelay)) != nil else { return }
            self?.showsWorking = false
        }
    }

    /// The agent's terminal changed size (the view's `onChange(of:
    /// agent.size)`): the attach is made again at the new grid. A view
    /// never sends RESIZE; its grid is fixed when it attaches.
    public func sizeChanged(_ size: GridSize?) {
        guard let size, let stream, let attachedSize, size != attachedSize else { return }
        resizing = true
        canType = false
        stream.close()
    }

    /// Direct terminal keys from the transcript keyboard.
    public func send(_ key: HesperSurfaces.KeyPress) {
        guard canType, let stream else { return }
        stream.write(KeyEncoder.bytes(key))
    }

    public func send(text: String) {
        guard canType, let stream else { return }
        stream.write(KeyEncoder.bytes(text: text))
    }

    /// Closes the attach for good; `run` returns.
    public func stop() {
        stopped = true
        shrink?.cancel()
        shrink = nil
        stream?.close()
        stream = nil
        canType = false
    }

    /// A typing view attach the Mac refuses as `forbidden` or read only is
    /// made once more without typing, and that answer stands.
    private func open(
        _ agent: Agent, size: GridSize, typing: Bool, model: FleetModel
    ) async throws -> (any AttachStream, Bool) {
        do {
            return (try await model.attachView(agent, size: size, typing: typing), typing)
        } catch let error where typing && Self.refusesTyping(error) {
            return (try await model.attachView(agent, size: size, typing: false), false)
        }
    }

    /// Feeds the frames into `rows`; true when the agent exited. The
    /// first size is the reply's view echo, the grid the bytes are drawn
    /// at; later ones are the PTY's and do not change it.
    private func consume(_ stream: any AttachStream) async -> Bool {
        var sized = false
        for await frame in stream.frames {
            switch frame {
            case .data(let bytes): rows.apply(bytes)
            case .size(let cols, let rowCount):
                guard !sized else { break }
                sized = true
                rows.resize(GridSize(cols: cols, rows: rowCount))
            case .exit: return true
            case .scroll: break
            }
        }
        return false
    }

    private static func refusesTyping(_ error: any Error) -> Bool {
        if let coded = error as? any CodedError, coded.code == "forbidden" { return true }
        return String(describing: error).localizedCaseInsensitiveContains("read-only")
    }

    private static func isWorking(_ state: AgentState) -> Bool {
        state == .working || state == .starting
    }
}
