import Foundation

// Desks (docs/rebuild-contract.md "As built — desks"; workspace model step
// 3): a desk is the whole arrangement of windows — walls with their views
// and agent windows with their tab groups — placed on displays, saved per
// display setup. Pure, so it is unit-tested; the AppKit side is
// app/Sources/Hesper/Windows/DeskController.swift.

// MARK: Display setups

/// One attached display as the app reads it (NSScreen + CGDisplay*).
public struct DisplayInfo: Equatable, Sendable {
    /// CGDisplayVendorNumber / ModelNumber / SerialNumber (0: unknown).
    public var vendor: UInt32
    public var model: UInt32
    public var serial: UInt32
    /// NSScreen.localizedName ("Built-in Retina Display", "Studio Display").
    public var name: String
    public var builtin: Bool
    /// Physical size in millimetres (CGDisplayScreenSize; 0 when unknown).
    public var sizeMM: (width: Double, height: Double)
    /// AppKit global frame and visible frame (without menu bar and Dock).
    public var frame: Rect
    public var visible: Rect
    /// CGDirectDisplayID as text: changes across reconnects, only used to
    /// map old window files' `screen` values.
    public var number: String?

    public init(vendor: UInt32, model: UInt32, serial: UInt32, name: String, builtin: Bool,
                sizeMM: (width: Double, height: Double) = (0, 0), frame: Rect, visible: Rect? = nil, number: String? = nil) {
        self.vendor = vendor; self.model = model; self.serial = serial; self.name = name; self.builtin = builtin
        self.sizeMM = sizeMM; self.frame = frame; self.visible = visible ?? frame; self.number = number
    }

    public static func == (l: DisplayInfo, r: DisplayInfo) -> Bool {
        l.vendor == r.vendor && l.model == r.model && l.serial == r.serial && l.name == r.name && l.builtin == r.builtin
            && l.sizeMM.width == r.sizeMM.width && l.sizeMM.height == r.sizeMM.height && l.frame == r.frame && l.visible == r.visible && l.number == r.number
    }

    /// The identity that survives reconnects: vendor/model/serial when the
    /// display reports a serial, else vendor/model + name + physical size.
    public var identity: String {
        if serial != 0 && (vendor != 0 || model != 0) { return "v\(vendor)-m\(model)-s\(serial)" }
        let mm = "\(Int(sizeMM.width.rounded()))x\(Int(sizeMM.height.rounded()))mm"
        return "v\(vendor)-m\(model)-\(name.lowercased().replacingOccurrences(of: " ", with: "_"))-\(mm)"
    }
}

/// A display as a setup records it (its stable key and how to call it).
public struct SetupScreen: Codable, Equatable, Sendable {
    public var key: String
    public var name: String
    public var builtin: Bool
    public init(key: String, name: String, builtin: Bool) { self.key = key; self.name = name; self.builtin = builtin }
}

/// The set of attached displays and how they are arranged. Its `id`
/// ignores the order the system lists them in, CGDirectDisplayIDs, global
/// coordinates (which change with the main display) and resolutions; it
/// changes when a display is plugged in or out, or moved to another side
/// of another one in System Settings ▸ Displays ▸ Arrange.
public struct DisplaySetup: Equatable, Sendable {
    /// "ds-" + 16 hex (FNV-1a 64 of `signature`).
    public var id: String
    /// Human-readable canonical form: the sorted keys, then the relations.
    public var signature: String
    /// Sorted by key.
    public var screens: [SetupScreen]
    /// Display key per input display (same order as the input).
    public var keys: [String]
    /// "Laptop only", "Laptop + Studio Display", "Studio Display".
    public var defaultName: String

    /// Stable keys: `identity`, with "#2", "#3" … for identical displays
    /// (no serial), numbered left to right, then bottom to top.
    public static func keys(_ displays: [DisplayInfo]) -> [String] {
        var keys = displays.map(\.identity)
        let groups = Dictionary(grouping: displays.indices, by: { keys[$0] })
        for (_, idx) in groups where idx.count > 1 {
            let sorted = idx.sorted { a, b in
                let fa = displays[a].frame, fb = displays[b].frame
                return fa.x != fb.x ? fa.x < fb.x : fa.y < fb.y
            }
            for (n, i) in sorted.enumerated() where n > 0 { keys[i] += "#\(n + 1)" }
        }
        return keys
    }

    public init(_ displays: [DisplayInfo]) {
        let keys = Self.keys(displays)
        self.keys = keys
        let byKey = Dictionary(zip(keys, displays), uniquingKeysWith: { a, _ in a })
        let sortedKeys = keys.sorted()
        screens = sortedKeys.map { k in SetupScreen(key: k, name: byKey[k]!.name, builtin: byKey[k]!.builtin) }
        // Arrangement: where every other display sits relative to the first
        // (by key): right / left / above / below, by the larger offset of
        // their centres.
        var rel: [String] = []
        if let ref = sortedKeys.first.flatMap({ byKey[$0] }) {
            for k in sortedKeys.dropFirst() {
                let d = byKey[k]!
                let dx = d.frame.midX - ref.frame.midX, dy = d.frame.midY - ref.frame.midY
                let side = abs(dx) >= abs(dy) ? (dx >= 0 ? "right" : "left") : (dy >= 0 ? "above" : "below")
                rel.append("\(k)@\(side)")
            }
        }
        signature = sortedKeys.joined(separator: "+") + (rel.isEmpty ? "" : ";" + rel.joined(separator: ","))
        id = "ds-" + Self.fnv64hex(signature)
        defaultName = Self.name(displays)
    }

    /// "Laptop only"; else the displays left to right ("Laptop + Studio
    /// Display"); a lid-closed Mac on one display: that display's name.
    public static func name(_ displays: [DisplayInfo]) -> String {
        if displays.isEmpty { return "No display" }
        if displays.count == 1 && displays[0].builtin { return "Laptop only" }
        let ordered = displays.sorted { $0.frame.x != $1.frame.x ? $0.frame.x < $1.frame.x : $0.frame.y < $1.frame.y }
        return ordered.map { $0.builtin ? "Laptop" : $0.name }.joined(separator: " + ")
    }

    static func fnv64hex(_ s: String) -> String {
        var h: UInt64 = 0xcbf2_9ce4_8422_2325
        for b in s.utf8 { h ^= UInt64(b); h = h &* 0x0000_0100_0000_01b3 }
        return String(format: "%016llx", h)
    }

    /// The displays as frame-clamping targets, keyed by their stable key.
    public static func areas(_ displays: [DisplayInfo]) -> [ScreenArea] {
        zip(keys(displays), displays).map { ScreenArea(id: $0, visible: $1.visible) }
    }
}

// MARK: Desks

/// What happens to agent windows when a display setup without a desk
/// appears (everything folds into one wall on the main display).
public enum FoldAgentWindows: String, Codable, CaseIterable, Sendable {
    /// Kept, as tabs of one window on the main display.
    case tabs
    /// Closed (the agents keep running).
    case close
}

/// A saved arrangement of windows for one display setup.
public struct Desk: Codable, Equatable, Sendable {
    public var id: String
    public var name: String
    /// The display setup it belongs to (`DisplaySetup.id`).
    public var setup: String
    /// The setup's own desk (one per setup, made and kept up to date
    /// automatically); false: a desk saved by name (Save Desk As…).
    public var automatic: Bool
    /// Walls (home first) and agent windows.
    public var windows: SavedWindows
    /// Seconds since 1970 of the last save.
    public var updated: Double

    public init(id: String, name: String, setup: String, automatic: Bool, windows: SavedWindows, updated: Double) {
        self.id = id; self.name = name; self.setup = setup; self.automatic = automatic; self.windows = windows; self.updated = updated
    }

    /// The home wall: the first wall of the desk.
    public var home: String? { windows.walls.first?.id }
}

/// A display setup the app has seen: its displays, name and current desk.
public struct SetupRecord: Codable, Equatable, Sendable {
    public var id: String
    public var signature: String
    public var screens: [SetupScreen]
    /// The desk in use for this setup (restored when the setup comes back).
    public var current: String?
    public var lastUsed: Double
    public init(id: String, signature: String, screens: [SetupScreen], current: String?, lastUsed: Double) {
        self.id = id; self.signature = signature; self.screens = screens; self.current = current; self.lastUsed = lastUsed
    }
}

/// desks.json: every desk and every setup seen.
public struct DeskBook: Codable, Equatable, Sendable {
    public var version = 2
    public var setups: [SetupRecord] = []
    public var desks: [Desk] = []
    public init() {}

    // MARK: File

    public func encoded() throws -> Data {
        let e = JSONEncoder()
        e.outputFormatting = [.prettyPrinted, .sortedKeys]
        return try e.encode(self)
    }

    /// A desks.json (version 2), or a windows.json (version 1) migrated
    /// into the given setup's automatic desk. Anything else: nil.
    public static func decode(_ d: Data, setup: DisplaySetup, displays: [DisplayInfo] = [], now: Double = Date().timeIntervalSince1970) -> DeskBook? {
        if let b = try? JSONDecoder().decode(DeskBook.self, from: d), b.version == 2 { return b }
        if let old = SavedWindows.decode(d) { return migrate(old, setup: setup, displays: displays, now: now) }
        return nil
    }

    /// windows.json → the current setup's automatic desk. Its `screen`
    /// values (CGDirectDisplayIDs) become the stable keys of the displays
    /// attached now; unknown ones stay (clamping falls back to overlap).
    public static func migrate(_ old: SavedWindows, setup: DisplaySetup, displays: [DisplayInfo], now: Double) -> DeskBook {
        var map: [String: String] = [:]
        for (k, d) in zip(DisplaySetup.keys(displays), displays) { if let n = d.number { map[n] = k } }
        var w = old
        for i in w.walls.indices { if let s = w.walls[i].screen, let k = map[s] { w.walls[i].screen = k } }
        for i in w.agentWindows.indices { if let s = w.agentWindows[i].screen, let k = map[s] { w.agentWindows[i].screen = k } }
        var b = DeskBook()
        b.record(w, setup: setup, now: now)
        return b
    }

    // MARK: Lookup

    public func setup(_ id: String) -> SetupRecord? { setups.first { $0.id == id } }
    public func desk(_ id: String?) -> Desk? { id.flatMap { i in desks.first { $0.id == i } } }
    public func desks(for setup: String) -> [Desk] {
        desks.filter { $0.setup == setup }.sorted { $0.automatic != $1.automatic ? $0.automatic : $0.name.lowercased() < $1.name.lowercased() }
    }
    public func automaticDesk(for setup: String) -> Desk? { desks.first { $0.setup == setup && $0.automatic } }

    /// The desk restored when this setup appears: its current one, else its
    /// automatic one.
    public func currentDesk(for setup: String) -> Desk? {
        desk(self.setup(setup)?.current) ?? automaticDesk(for: setup)
    }

    /// The most recently saved desk of another setup (what folds into a
    /// setup seen for the first time).
    public func lastUsed(excluding setup: String) -> Desk? {
        desks.filter { $0.setup != setup }.max { $0.updated < $1.updated }
    }

    // MARK: Changes

    private mutating func touchSetup(_ s: DisplaySetup, current: String?, now: Double) {
        if let i = setups.firstIndex(where: { $0.id == s.id }) {
            if let current { setups[i].current = current }
            setups[i].lastUsed = now
            setups[i].screens = s.screens
        } else {
            setups.append(SetupRecord(id: s.id, signature: s.signature, screens: s.screens, current: current, lastUsed: now))
        }
    }

    /// The windows as they are now go into the setup's current desk (its
    /// automatic desk, made on first use with the setup's default name).
    @discardableResult
    public mutating func record(_ w: SavedWindows, setup s: DisplaySetup, now: Double) -> Desk {
        if let cur = currentDesk(for: s.id), let i = desks.firstIndex(where: { $0.id == cur.id }) {
            desks[i].windows = w
            desks[i].updated = now
            touchSetup(s, current: cur.id, now: now)
            return desks[i]
        }
        let d = Desk(id: Self.newID(s.id, automatic: true, existing: desks.map(\.id)), name: s.defaultName, setup: s.id, automatic: true, windows: w, updated: now)
        desks.append(d)
        touchSetup(s, current: d.id, now: now)
        return d
    }

    /// Save Desk As…: a named desk for this setup with these windows; it
    /// becomes the setup's current desk. A desk of this setup with the
    /// same name is replaced.
    @discardableResult
    public mutating func saveAs(_ name: String, windows w: SavedWindows, setup s: DisplaySetup, now: Double) -> Desk {
        let name = name.trimmingCharacters(in: .whitespacesAndNewlines)
        if let i = desks.firstIndex(where: { $0.setup == s.id && $0.name.lowercased() == name.lowercased() }) {
            desks[i].windows = w
            desks[i].updated = now
            touchSetup(s, current: desks[i].id, now: now)
            return desks[i]
        }
        if automaticDesk(for: s.id) == nil { record(w, setup: s, now: now) }
        let d = Desk(id: Self.newID(s.id, automatic: false, existing: desks.map(\.id)), name: name, setup: s.id, automatic: false, windows: w, updated: now)
        desks.append(d)
        touchSetup(s, current: d.id, now: now)
        return d
    }

    /// Makes a desk the current one of a setup (the picker). A desk of
    /// another setup is copied into this setup as a named desk first.
    @discardableResult
    public mutating func select(_ id: String, setup s: DisplaySetup, now: Double) -> Desk? {
        guard let d = desk(id) else { return nil }
        if d.setup == s.id {
            touchSetup(s, current: d.id, now: now)
            return d
        }
        return saveAs(d.name, windows: d.windows, setup: s, now: now)
    }

    public mutating func rename(_ id: String, to name: String) {
        let n = name.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !n.isEmpty, let i = desks.firstIndex(where: { $0.id == id }) else { return }
        desks[i].name = n
    }

    /// Removes a named desk (an automatic desk stays: it is the setup's).
    public mutating func remove(_ id: String) {
        guard let d = desk(id), !d.automatic else { return }
        desks.removeAll { $0.id == id }
        for i in setups.indices where setups[i].current == id { setups[i].current = automaticDesk(for: setups[i].id)?.id }
    }

    /// Make Home Wall: the wall moves to the front of the desk.
    public mutating func setHome(_ wall: String, desk id: String) {
        guard let i = desks.firstIndex(where: { $0.id == id }) else { return }
        desks[i].windows = HomeRule.makeHome(wall, in: desks[i].windows)
    }

    static func newID(_ setup: String, automatic: Bool, existing: [String]) -> String {
        let base = "desk-" + setup.dropFirst(3).prefix(8)
        if automatic && !existing.contains(base) { return String(base) }
        var n = 2
        while existing.contains("\(base)-\(n)") { n += 1 }
        return "\(base)-\(n)"
    }
}

// MARK: Home wall

/// The first wall of a desk is home (the needs-you strip, the head of the
/// overflow chain, drafts). Make Home Wall moves a wall to the front.
public enum HomeRule {
    public static func makeHome(_ wall: String, in w: SavedWindows) -> SavedWindows {
        guard let i = w.walls.firstIndex(where: { $0.id == wall }), i > 0 else { return w }
        var s = w
        let h = s.walls.remove(at: i)
        s.walls.insert(h, at: 0)
        return s
    }

    /// Wall ids in desk order with `home` first.
    public static func order(_ ids: [String], home: String?) -> [String] {
        guard let home, let i = ids.firstIndex(of: home), i > 0 else { return ids }
        var o = ids
        o.remove(at: i)
        o.insert(home, at: 0)
        return o
    }
}

// MARK: Fold to defaults

public enum DeskFold {
    /// A display setup without a desk: one wall on the main display
    /// showing everyone (the app's main wall with its layout and card
    /// width; scope All, nothing collapsed, the sidebar as it was), agent
    /// windows as tabs of one window on that display (the selected tab
    /// kept) or closed.
    public static func fold(_ s: SavedWindows, main: ScreenArea, agentWindows: FoldAgentWindows) -> SavedWindows {
        let v = main.visible
        let src = s.walls.first { $0.isMain } ?? s.walls.first
        let wall = SavedWall(id: src?.isMain == true ? src!.id : "main", isMain: true, scope: .all,
                             arrangement: src?.arrangement ?? .shelf, minChars: src?.minChars ?? 80,
                             frame: SavedFrame(x: v.x, y: v.y, width: v.width, height: v.height), screen: main.id, fullScreen: false,
                             grouping: src?.grouping, collapsed: nil, bandOrder: src?.bandOrder, sidebar: src?.sidebar)
        var out = SavedWindows(walls: [wall], agentWindows: [])
        guard agentWindows == .tabs, !s.agentWindows.isEmpty else { return out }
        let w = min(980, v.width), h = min(680, v.height)
        let f = SavedFrame(x: v.x + (v.width - w) / 2, y: v.y + (v.height - h) / 2, width: w, height: h)
        let selected = s.agentWindows.firstIndex { $0.selectedTab } ?? 0
        let many = s.agentWindows.count > 1
        out.agentWindows = s.agentWindows.enumerated().map { i, a in
            SavedAgentWindow(agent: a.agent, frame: f, screen: main.id, tabGroup: many ? 0 : nil, selectedTab: i == selected, fullScreen: false)
        }
        return out
    }
}
