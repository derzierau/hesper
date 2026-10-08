import AppKit
import Carbon.HIToolbox
import HesperCore
import Observation

/// The app's own settings (⌘,): quick launch hotkey, notifications, the
/// default profile per kind. Layout settings live on AppModel (they drive
/// the wall). Automated runs keep everything in memory.
@MainActor
@Observable
final class AppSettings {
    @ObservationIgnored private let persist: Bool
    private let d = UserDefaults.standard

    var quickLaunchEnabled: Bool { didSet { save("quickLaunchEnabled", quickLaunchEnabled) } }
    var hotKey: HotKeySpec { didSet { save("quickLaunchKeyCode", Int(hotKey.keyCode)); save("quickLaunchModifiers", Int(hotKey.carbonModifiers)) } }
    var notificationsEnabled: Bool { didSet { save("notificationsEnabled", notificationsEnabled) } }
    var notificationSound: Bool { didSet { save("notificationSound", notificationSound) } }
    /// The one font of every wall tile (9–16 pt; default: the user's
    /// Ghostty font size, else 12).
    var tileFontSize: Double { didSet { save("tileFontSize", tileFontSize); if oldValue != tileFontSize { onTileFontChanged?() } } }
    @ObservationIgnored var onTileFontChanged: (() -> Void)?
    static let tileFontRange: ClosedRange<Double> = 9...16
    /// kind → profile; missing: hesperd's default for the kind.
    var defaultProfiles: [String: String] { didSet { save("defaultProfiles", defaultProfiles) } }
    /// Desks: switch desks automatically when the displays change.
    var desksAutoSwitch: Bool { didSet { save("desksAutoSwitch", desksAutoSwitch) } }
    /// Desks: agent windows when a display setup without a desk appears.
    var foldAgentWindows: FoldAgentWindows { didSet { save("foldAgentWindows", foldAgentWindows.rawValue) } }
    /// Settings › Agents: closed agents stay on their band's shelf as ghost
    /// cards for a day (off by default).
    var ghostCards: Bool { didSet { save("ghostCards", ghostCards); if oldValue != ghostCards { onGhostCardsChanged?() } } }
    @ObservationIgnored var onGhostCardsChanged: (() -> Void)?
    /// Settings › Agents: "Close finished agents after 30 min" (off).
    var autoTidy: Bool { didSet { save("autoTidy", autoTidy); if oldValue != autoTidy { onAutoTidyChanged?() } } }
    @ObservationIgnored var onAutoTidyChanged: (() -> Void)?
    /// Wall density (Comfortable default, Compact): tile header 26/22,
    /// gutter 8/6. Mirrored into `DS.density`, which posts
    /// `DS.densityDidChange` for views that lay out from it.
    var density: DS.Density { didSet { save("density", density.rawValue); DS.density = density } }
    /// Settings › Appearance: Dusk, Daylight or the system's (default).
    /// Applied as NSApp.appearance; tokens follow it.
    var appearance: AppearanceChoice { didSet { save("appearance", appearance.rawValue); if persist { appearance.apply() } } }
    /// The first run (pair, add a Mac, first agent) was shown or skipped.
    var firstRunDone: Bool { didSet { save("firstRunDone", firstRunDone) } }

    init(persist: Bool, defaultTileFont: Double = 12) {
        self.persist = persist
        let d = UserDefaults.standard
        let f = persist ? (d.object(forKey: "tileFontSize") as? Double ?? defaultTileFont) : defaultTileFont
        tileFontSize = min(max(f, Self.tileFontRange.lowerBound), Self.tileFontRange.upperBound)
        quickLaunchEnabled = persist ? (d.object(forKey: "quickLaunchEnabled") as? Bool ?? true) : true
        if persist, let k = d.object(forKey: "quickLaunchKeyCode") as? Int, let m = d.object(forKey: "quickLaunchModifiers") as? Int {
            hotKey = HotKeySpec(keyCode: UInt32(k), carbonModifiers: UInt32(m))
        } else {
            hotKey = .default
        }
        notificationsEnabled = persist ? (d.object(forKey: "notificationsEnabled") as? Bool ?? true) : true
        notificationSound = persist ? (d.object(forKey: "notificationSound") as? Bool ?? true) : true
        defaultProfiles = persist ? (d.dictionary(forKey: "defaultProfiles") as? [String: String] ?? [:]) : [:]
        desksAutoSwitch = persist ? (d.object(forKey: "desksAutoSwitch") as? Bool ?? true) : true
        foldAgentWindows = persist ? (d.string(forKey: "foldAgentWindows").flatMap(FoldAgentWindows.init(rawValue:)) ?? .tabs) : .tabs
        ghostCards = persist ? (d.object(forKey: "ghostCards") as? Bool ?? false) : false
        autoTidy = persist ? d.bool(forKey: "autoTidy") : false
        density = persist ? (d.string(forKey: "density").flatMap(DS.Density.init(rawValue:)) ?? .default) : .default
        appearance = persist ? (d.string(forKey: "appearance").flatMap(AppearanceChoice.init(rawValue:)) ?? .system) : .system
        firstRunDone = persist ? d.bool(forKey: "firstRunDone") : true
        DS.density = density
        if persist { appearance.apply() }
    }

    private func save(_ key: String, _ value: Any) {
        guard persist else { return }
        d.set(value, forKey: key)
    }
}

/// The app's color scheme: Dusk (dark), Daylight (light) or the system's.
/// Terminals stay dark either way.
enum AppearanceChoice: String, CaseIterable, Sendable {
    case dusk, daylight, system

    var title: String {
        switch self {
        case .dusk: return "Dusk"
        case .daylight: return "Daylight"
        case .system: return "System"
        }
    }

    var nsAppearance: NSAppearance? {
        switch self {
        case .dusk: return NSAppearance(named: .darkAqua)
        case .daylight: return NSAppearance(named: .aqua)
        case .system: return nil
        }
    }

    @MainActor func apply() { NSApp?.appearance = nsAppearance }
}

/// A global hotkey: a virtual key code and Carbon modifiers.
struct HotKeySpec: Equatable, Sendable {
    var keyCode: UInt32
    var carbonModifiers: UInt32

    /// ⌃⌥Space.
    static let `default` = HotKeySpec(keyCode: UInt32(kVK_Space), carbonModifiers: UInt32(controlKey | optionKey))

    init(keyCode: UInt32, carbonModifiers: UInt32) { self.keyCode = keyCode; self.carbonModifiers = carbonModifiers }

    init?(event: NSEvent) {
        let f = event.modifierFlags.intersection(.deviceIndependentFlagsMask)
        var m: UInt32 = 0
        if f.contains(.command) { m |= UInt32(cmdKey) }
        if f.contains(.option) { m |= UInt32(optionKey) }
        if f.contains(.control) { m |= UInt32(controlKey) }
        if f.contains(.shift) { m |= UInt32(shiftKey) }
        // A global hotkey needs ⌘, ⌥ or ⌃ (⇧ alone would eat typing).
        guard m & UInt32(cmdKey | optionKey | controlKey) != 0 else { return nil }
        self.init(keyCode: UInt32(event.keyCode), carbonModifiers: m)
    }

    var label: String {
        var s = ""
        if carbonModifiers & UInt32(controlKey) != 0 { s += "⌃" }
        if carbonModifiers & UInt32(optionKey) != 0 { s += "⌥" }
        if carbonModifiers & UInt32(shiftKey) != 0 { s += "⇧" }
        if carbonModifiers & UInt32(cmdKey) != 0 { s += "⌘" }
        return s + Self.keyName(keyCode)
    }

    static func keyName(_ code: UInt32) -> String {
        switch Int(code) {
        case kVK_Space: return "Space"
        case kVK_Return: return "↩"
        case kVK_Tab: return "⇥"
        case kVK_Escape: return "esc"
        case kVK_Delete: return "⌫"
        case kVK_UpArrow: return "↑"
        case kVK_DownArrow: return "↓"
        case kVK_LeftArrow: return "←"
        case kVK_RightArrow: return "→"
        default:
            break
        }
        // The key's character on the current layout.
        guard let src = TISCopyCurrentKeyboardLayoutInputSource()?.takeRetainedValue(),
              let ptr = TISGetInputSourceProperty(src, kTISPropertyUnicodeKeyLayoutData) else { return "#\(code)" }
        let data = Unmanaged<CFData>.fromOpaque(ptr).takeUnretainedValue() as Data
        var dead: UInt32 = 0
        var chars = [UniChar](repeating: 0, count: 4)
        var len = 0
        let status = data.withUnsafeBytes { raw -> OSStatus in
            guard let layout = raw.baseAddress?.assumingMemoryBound(to: UCKeyboardLayout.self) else { return -1 }
            return UCKeyTranslate(layout, UInt16(code), UInt16(kUCKeyActionDisplay), 0, UInt32(LMGetKbdType()),
                                  OptionBits(kUCKeyTranslateNoDeadKeysBit), &dead, 4, &len, &chars)
        }
        guard status == noErr, len > 0 else { return "#\(code)" }
        return String(utf16CodeUnits: chars, count: len).uppercased()
    }
}

/// The one global hotkey (Carbon RegisterEventHotKey: no accessibility
/// permission needed).
@MainActor
final class GlobalHotKey {
    private var ref: EventHotKeyRef?
    private var handlerInstalled = false
    nonisolated(unsafe) static var fire: (@MainActor () -> Void)?

    func register(_ spec: HotKeySpec?, action: @escaping @MainActor () -> Void) {
        unregister()
        Self.fire = action
        guard let spec else { return }
        if !handlerInstalled {
            var type = EventTypeSpec(eventClass: OSType(kEventClassKeyboard), eventKind: UInt32(kEventHotKeyPressed))
            InstallEventHandler(GetApplicationEventTarget(), { _, _, _ in
                DispatchQueue.main.async { MainActor.assumeIsolated { GlobalHotKey.fire?() } }
                return noErr
            }, 1, &type, nil, nil)
            handlerInstalled = true
        }
        let id = EventHotKeyID(signature: OSType(0x4748_5354), id: 1) // "GHST"
        RegisterEventHotKey(spec.keyCode, spec.carbonModifiers, id, GetApplicationEventTarget(), 0, &ref)
    }

    func unregister() {
        if let ref { UnregisterEventHotKey(ref) }
        ref = nil
    }
}
