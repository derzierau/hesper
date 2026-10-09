import AppKit
import HesperCore
import Observation

/// Work ready for review, app-wide (one per daemon connection, like
/// HistoryHub): `review.list` (hesperd merges every Mac), refreshed on
/// `review.changed`, when an agent settles or starts again, and after a
/// connect; decoded off the main thread. The toolbar pill, the tiles'
/// footers, the menu bar and the sheet read it. Per Mac: a Mac whose
/// hesperd answers review.* with "no such method" has no entries.
@MainActor
@Observable
final class ReviewHub {
    private static var hubs: [ObjectIdentifier: ReviewHub] = [:]

    static func shared(for model: AppModel) -> ReviewHub {
        let key = ObjectIdentifier(model.client)
        if let h = hubs[key] { return h }
        let h = ReviewHub(client: model.client)
        hubs[key] = h
        return h
    }

    @ObservationIgnored let client: DaemonClient
    private(set) var support = ReviewSupport()
    /// Ranked (ReviewInbox), without the Macs that have no review.*.
    private(set) var items: [ReviewItem] = []
    @ObservationIgnored private var raw: [ReviewItem] = []
    @ObservationIgnored private var arrivals = ReviewArrivals()
    @ObservationIgnored private var refreshTask: Task<Void, Never>?
    @ObservationIgnored private var again = false
    /// Items that became ready since the last list (notifications).
    @ObservationIgnored var onArrivals: (([ReviewItem]) -> Void)?

    private init(client: DaemonClient) {
        self.client = client
        let key = ObjectIdentifier(client)
        client.onReviewNotification = { method, _ in
            // Reader thread: one hop to main; the list comes debounced.
            guard method == ReviewRPC.changed else { return }
            DispatchQueue.main.async { MainActor.assumeIsolated { ReviewHub.hubs[key]?.scheduleRefresh() } }
        }
    }

    var available: Bool { support.available }
    var count: Int { items.count }
    func item(_ id: String?) -> ReviewItem? { id.flatMap { id in items.first { $0.id == id } } }

    // MARK: Refresh

    /// A connect: a fresh baseline (no "ready" flood for what finished
    /// while away), then the list.
    func connected() {
        arrivals.reset()
        scheduleRefresh(after: 0)
    }

    /// Debounced (several agents settle at once): one review.list.
    func scheduleRefresh(after delay: TimeInterval = 0.3) {
        if refreshTask != nil { again = true; return }
        let client = client
        refreshTask = Task { [weak self] in
            if delay > 0 { try? await Task.sleep(nanoseconds: UInt64(delay * 1_000_000_000)) }
            let r: Result<[ReviewItem], any Error> = await Task.detached {
                do { return .success(try await client.reviewList()) } catch { return .failure(error) }
            }.value
            guard let self else { return }
            self.refreshTask = nil
            self.applied(r)
            if self.again {
                self.again = false
                self.scheduleRefresh()
            }
        }
    }

    private func applied(_ r: Result<[ReviewItem], any Error>) {
        switch r {
        case .success(let list):
            support.local = true
            raw = list
            publish(announce: true)
        case .failure(let e as RPCError) where ReviewRPC.missing(e):
            support.local = false // an older hesperd: no review anywhere
            raw = []
            publish(announce: false)
        case .failure:
            break // not connected, a timeout: keep what we have
        }
    }

    private func publish(announce: Bool) {
        let next = ReviewInbox.ranked(support.filter(raw))
        let new = arrivals.update(next)
        if next != items { items = next }
        ReviewPanelHost.wallsChanged()
        if announce, !new.isEmpty { onArrivals?(new) }
    }

    /// review.* on that Mac said "no such method": its entries go.
    func noteFailure(_ error: any Error, machine: String) -> Bool {
        guard support.note(error, machine: machine) else { return false }
        publish(announce: false)
        return true
    }

    /// Accepted or sent back: gone from the list now (the next list agrees).
    func dropped(_ id: String) {
        raw.removeAll { $0.id == id }
        publish(announce: false)
        scheduleRefresh(after: 1)
    }

    /// A hub on no connection (offscreen renders).
    static func offscreen() -> ReviewHub { ReviewHub(client: DaemonClient(socketPath: "")) }

    /// Fixtures (offscreen renders): the list as given.
    func showFixture(_ list: [ReviewItem]) {
        support.local = true
        raw = list
        items = ReviewInbox.ranked(list)
    }
}

// MARK: The wall model's hooks

extension AppModel {
    var reviewHub: ReviewHub { ReviewHub.shared(for: self) }
    /// The toolbar pill's count (0: hidden).
    var reviewCount: Int { lists.reviewHub.count }

    /// The review item of an agent, when it is ready to review.
    func reviewItem(_ id: String?) -> ReviewItem? { reviewHub.item(id) }

    /// ⌘R, the pill, ⏎ on a ready tile: open the sheet (on `select`, else
    /// the selected tile's item), or close it.
    func toggleReview(select: String? = nil) {
        prepareForReview()
        ReviewPanelHost.toggle(self, select: select ?? reviewItem(selectedID)?.id)
    }

    /// The sheet open on `id` (never closes it).
    func openReview(select id: String?) {
        prepareForReview()
        ReviewPanelHost.show(self, select: id)
    }

    private func prepareForReview() {
        if mode == .compose { leaveComposer() }
        closePopover()
        showPalette = false
        if HistoryPanelHost.isOpen(self) { HistoryPanelHost.panel(for: self)?.close() }
    }

    /// Every daemon event (marked integration point in `handle`): an
    /// agent that settled, started again or went away changes the list.
    func reviewSaw(_ e: DaemonEvent) {
        let hub = reviewHub
        switch e {
        case .connected: hub.connected()
        case .removed(let id, _): if hub.item(id) != nil { hub.scheduleRefresh() }
        case .changed(let a):
            if CloseRules.isFinished(a) != (hub.item(a.id) != nil) { hub.scheduleRefresh(after: 1) }
        default: break
        }
    }
}
