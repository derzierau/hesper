import Foundation
import HesperCore

/// An agent's transcript as the phone holds it: items sorted by `seq`,
/// each once. Pages going back and the stream going forward overlap after
/// a reconnect; inserting them here keeps one copy of every line.
public struct TranscriptLog: Equatable, Sendable {
    public private(set) var items: [TranscriptItem]
    /// Incremented whenever a different transcript replaces these items.
    public private(set) var generation = 0
    /// Older items are left on the Mac: the last page's `more`, set by the
    /// store.
    public var hasOlder: Bool

    public var firstSeq: Int64? { items.first?.seq }
    public var lastSeq: Int64? { items.last?.seq }
    /// The first transcript line held; markers (negative seq) sort before
    /// the lines and are not one. Older pages are asked for before it.
    public var firstLineSeq: Int64? { items.first { $0.seq >= 0 }?.seq }

    public init(_ items: [TranscriptItem] = [], hasOlder: Bool = false) {
        self.items = []
        self.hasOlder = hasOlder
        insert(items)
    }

    /// Adds the items not in the log yet, in order; returns how many were
    /// new. A seq already here keeps its item: seq is the line's offset in
    /// the file, so the same seq is the same line.
    @discardableResult
    public mutating func insert(_ new: [TranscriptItem]) -> Int {
        var added = 0
        var appended = false
        for item in new {
            // The stream appends at the end; check that before searching.
            if let last = items.last, last.seq >= item.seq {
                let i = index(of: item.seq)
                guard i == items.endIndex || items[i].seq != item.seq else { continue }
                items.insert(item, at: i)
            } else {
                items.append(item)
                appended = true
            }
            textBytes += Self.size(item)
            added += 1
        }
        // Only growth at the end trims. While the person reads older
        // history (`keepsOlder`) the pages they loaded stay, up to a hard
        // bound.
        let bound = keepsOlder ? Self.maxItemsReading : Self.maxItems
        let byteBound = keepsOlder ? Self.maxTextBytesReading : Self.maxTextBytes
        if appended, items.count > bound || textBytes > byteBound {
            var drop = 0
            while items.count - drop > 1, items.count - drop > bound || textBytes > byteBound {
                textBytes -= Self.size(items[drop])
                drop += 1
            }
            items.removeFirst(drop)
            hasOlder = true
        }
        return added
    }

    /// The text the items hold, in UTF-8 bytes (`size`).
    public private(set) var textBytes = 0
    /// At most this much text after the stream adds some (an item may be
    /// up to the 1 MiB frame limit); twice as much while reading.
    public static let maxTextBytes = 16 << 20
    public static let maxTextBytesReading = 32 << 20

    private static func size(_ item: TranscriptItem) -> Int {
        (item.text?.utf8.count ?? 0) + (item.summary?.utf8.count ?? 0)
    }

    /// Items held at most after the stream adds some: 25 of the daemon's
    /// default pages (200). The oldest go first and `hasOlder` turns on,
    /// so scrolling up pages them back from the Mac.
    public static let maxItems = 5000
    /// The bound while the person reads older history.
    public static let maxItemsReading = 10_000
    /// The person is scrolled up in older history; set by the store.
    public var keepsOlder = false

    /// Stream items queued before a restart belong to the old transcript.
    @discardableResult
    public mutating func insert(_ new: [TranscriptItem], from generation: Int) -> Int {
        guard generation == self.generation else { return 0 }
        return insert(new)
    }

    /// One stream item. A marker (negative seq, "Transcript restarted")
    /// means hesperd could not continue after what the phone holds (the
    /// file was rewritten, or the phone is too far behind): everything
    /// held is dropped and the marker stays, then the newest items follow.
    /// A line is inserted. True when it restarted.
    @discardableResult
    public mutating func follow(_ item: TranscriptItem) -> (added: Int, restarted: Bool) {
        guard item.seq >= 0 else {
            restart(marker: item)
            return (0, true)
        }
        return (insert([item]), false)
    }

    /// Drops everything held: the transcript was rewound, belongs to a new
    /// session, or the Mac closed the stream. `marker` ("Transcript
    /// restarted") stays as the only item. The Mac may hold older lines of
    /// the new transcript, so paging is on again.
    public mutating func restart(marker: TranscriptItem? = nil) {
        generation += 1
        items = marker.map { [$0] } ?? []
        textBytes = items.reduce(0) { $0 + Self.size($1) }
        hasOlder = true
    }

    /// The first index whose seq is not below `seq`.
    private func index(of seq: Int64) -> Int {
        var lo = items.startIndex
        var hi = items.endIndex
        while lo < hi {
            let mid = (lo + hi) / 2
            if items[mid].seq < seq { lo = mid + 1 } else { hi = mid }
        }
        return lo
    }
}
