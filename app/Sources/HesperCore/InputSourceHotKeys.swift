import Foundation

/// A global hotkey as Carbon registers it: a virtual key code and the
/// Carbon modifier bits (what the app's `HotKeySpec` holds).
public struct SystemHotKey: Hashable, Sendable {
    public let keyCode: UInt32
    public let carbonModifiers: UInt32

    public init(keyCode: UInt32, carbonModifiers: UInt32) { self.keyCode = keyCode; self.carbonModifiers = carbonModifiers }

    /// Carbon's modifier bits (Events.h: cmdKey, shiftKey, optionKey,
    /// controlKey).
    public static let command: UInt32 = 0x0100
    public static let shift: UInt32 = 0x0200
    public static let option: UInt32 = 0x0800
    public static let control: UInt32 = 0x1000
    /// Only the four modifiers count: Carbon ignores the rest.
    public static let modifierMask: UInt32 = command | shift | option | control

    /// Space, key code 49.
    public static let space: UInt32 = 49
    /// ⌃Space and ⌃⌥Space: what macOS binds "Select the previous / next
    /// input source" to by default.
    public static let inputSourceDefaults: [SystemHotKey] = [
        SystemHotKey(keyCode: space, carbonModifiers: control),
        SystemHotKey(keyCode: space, carbonModifiers: control | option),
    ]
}

/// macOS's "Select the previous / next input source" chords (System
/// Settings › Keyboard › Keyboard Shortcuts › Input Sources), read from
/// `com.apple.symbolichotkeys` (`UserDefaults(suiteName:)`, key
/// `AppleSymbolicHotKeys`): a dictionary keyed by the hotkey's id, each
/// `{enabled: Bool, value: {parameters: [char, keyCode, modifierFlags],
/// type: "standard"}}` with NSEvent modifier flags. Ids 60 and 61 are the
/// input source ones. Settings warns when the quick launch hotkey is one
/// of them; a plist without them means macOS's defaults, which the system
/// writes only once changed.
public enum InputSourceHotKeys {
    /// The AppleSymbolicHotKeys ids: previous (60) and next (61) input source.
    public static let ids = ["60", "61"]

    // NSEvent.ModifierFlags as the plist stores them.
    static let eventShift = 0x20000, eventControl = 0x40000, eventOption = 0x80000, eventCommand = 0x100000

    /// The enabled input source chords in a decoded AppleSymbolicHotKeys
    /// dictionary; nil when neither entry has the shape macOS writes
    /// (the defaults then stand, or the plist is unreadable).
    public static func chords(in symbolicHotKeys: [String: Any]) -> [SystemHotKey]? {
        var found = false
        var out: [SystemHotKey] = []
        for id in ids {
            guard let entry = symbolicHotKeys[id] as? [String: Any] else { continue }
            let enabled = entry["enabled"].flatMap(bool) ?? true
            guard let value = entry["value"] as? [String: Any], let params = value["parameters"] as? [Any], params.count == 3,
                  let keyCode = int(params[1]), let flags = int(params[2]), keyCode >= 0 else { continue }
            found = true
            if enabled { out.append(SystemHotKey(keyCode: UInt32(keyCode), carbonModifiers: carbon(flags))) }
        }
        return found ? out : nil
    }

    /// What macOS does with `hotKey`.
    public enum Verdict: Equatable, Sendable {
        /// Free (or the plist says input source switching uses another chord).
        case clear
        /// macOS switches input sources with it.
        case bound
        /// The plist could not be read and the chord is one of macOS's
        /// defaults: it is probably bound.
        case assumed

        /// The Settings warning next to the recorder; nil when clear.
        public var warning: String? {
            switch self {
            case .clear: return nil
            case .bound:
                return "macOS uses this shortcut to switch input sources. Change one of them, or the hotkey will not reach Hesper (System Settings › Keyboard › Keyboard Shortcuts › Input Sources)."
            case .assumed:
                return "macOS may use this shortcut to switch input sources when more than one is enabled (System Settings › Keyboard › Keyboard Shortcuts › Input Sources)."
            }
        }
        /// The Quick Launch row's "Conflicts with …" text; nil when clear.
        public var conflict: String? { self == .clear ? nil : "macOS input source switching" }
    }

    /// - Parameter symbolicHotKeys: the decoded AppleSymbolicHotKeys
    ///   dictionary, nil when unreadable.
    public static func check(_ hotKey: SystemHotKey, in symbolicHotKeys: [String: Any]?) -> Verdict {
        let key = SystemHotKey(keyCode: hotKey.keyCode, carbonModifiers: hotKey.carbonModifiers & SystemHotKey.modifierMask)
        guard let bound = symbolicHotKeys.flatMap(chords(in:)) else {
            return SystemHotKey.inputSourceDefaults.contains(key) ? .assumed : .clear
        }
        return bound.contains(key) ? .bound : .clear
    }

    static func carbon(_ eventFlags: Int) -> UInt32 {
        var m: UInt32 = 0
        if eventFlags & eventShift != 0 { m |= SystemHotKey.shift }
        if eventFlags & eventControl != 0 { m |= SystemHotKey.control }
        if eventFlags & eventOption != 0 { m |= SystemHotKey.option }
        if eventFlags & eventCommand != 0 { m |= SystemHotKey.command }
        return m
    }

    // A plist's numbers arrive as NSNumber, Int or Bool depending on the decoder.
    private static func int(_ v: Any) -> Int? {
        if let i = v as? Int { return i }
        if let n = v as? NSNumber { return n.intValue }
        if let d = v as? Double { return Int(d) }
        return nil
    }

    private static func bool(_ v: Any) -> Bool? {
        if let b = v as? Bool { return b }
        return int(v).map { $0 != 0 }
    }
}
