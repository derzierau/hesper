import Foundation
import Testing
@testable import HesperCore

// The global hotkey against macOS's input source switching: the
// com.apple.symbolichotkeys shape decoded, the modifier mapping, and the
// verdicts when the plist is there, says otherwise, or is unreadable.

@Suite struct InputSourceHotKeysTests {
    /// AppleSymbolicHotKeys as `defaults export` writes it: 60 ⌃Space,
    /// 61 ⌃⌥Space (NSEvent flags), plus an unrelated entry.
    static func fixture(nextEnabled: Bool = true) throws -> [String: Any] {
        let xml = """
        <?xml version="1.0" encoding="UTF-8"?>
        <!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
        <plist version="1.0">
        <dict>
            <key>60</key>
            <dict>
                <key>enabled</key><true/>
                <key>value</key>
                <dict>
                    <key>parameters</key>
                    <array><integer>32</integer><integer>49</integer><integer>262144</integer></array>
                    <key>type</key><string>standard</string>
                </dict>
            </dict>
            <key>61</key>
            <dict>
                <key>enabled</key><\(nextEnabled ? "true" : "false")/>
                <key>value</key>
                <dict>
                    <key>parameters</key>
                    <array><integer>32</integer><integer>49</integer><integer>786432</integer></array>
                    <key>type</key><string>standard</string>
                </dict>
            </dict>
            <key>64</key>
            <dict>
                <key>enabled</key><true/>
                <key>value</key>
                <dict>
                    <key>parameters</key>
                    <array><integer>32</integer><integer>49</integer><integer>1048576</integer></array>
                    <key>type</key><string>standard</string>
                </dict>
            </dict>
        </dict>
        </plist>
        """
        let any = try PropertyListSerialization.propertyList(from: Data(xml.utf8), format: nil)
        return try #require(any as? [String: Any])
    }

    static let ctrlSpace = SystemHotKey(keyCode: SystemHotKey.space, carbonModifiers: SystemHotKey.control)
    static let ctrlOptSpace = SystemHotKey(keyCode: SystemHotKey.space, carbonModifiers: SystemHotKey.control | SystemHotKey.option)
    static let cmdOptSpace = SystemHotKey(keyCode: SystemHotKey.space, carbonModifiers: SystemHotKey.command | SystemHotKey.option)

    @Test func decodesTheInputSourceEntries() throws {
        let chords = try #require(InputSourceHotKeys.chords(in: try Self.fixture()))
        #expect(chords == [Self.ctrlSpace, Self.ctrlOptSpace])
        // 64 (Spotlight) is not an input source entry.
        #expect(!chords.contains { $0.carbonModifiers == SystemHotKey.command })
    }

    @Test func theDefaultHotKeyIsBoundWhenMacOSSwitchesInputSourcesWithIt() throws {
        let dict = try Self.fixture()
        #expect(InputSourceHotKeys.check(Self.ctrlOptSpace, in: dict) == .bound)
        #expect(InputSourceHotKeys.check(Self.ctrlSpace, in: dict) == .bound)
        #expect(InputSourceHotKeys.check(Self.cmdOptSpace, in: dict) == .clear)
        #expect(InputSourceHotKeys.Verdict.bound.warning?.contains("Input Sources") == true)
        #expect(InputSourceHotKeys.Verdict.bound.conflict == "macOS input source switching")
        #expect(InputSourceHotKeys.Verdict.clear.warning == nil)
        #expect(InputSourceHotKeys.Verdict.clear.conflict == nil)
    }

    @Test func aDisabledEntryFreesTheChord() throws {
        let dict = try Self.fixture(nextEnabled: false)
        #expect(InputSourceHotKeys.chords(in: dict) == [Self.ctrlSpace])
        #expect(InputSourceHotKeys.check(Self.ctrlOptSpace, in: dict) == .clear)
    }

    @Test func anUnreadablePlistAssumesMacOSDefaults() {
        #expect(InputSourceHotKeys.check(Self.ctrlOptSpace, in: nil) == .assumed)
        #expect(InputSourceHotKeys.check(Self.ctrlSpace, in: nil) == .assumed)
        #expect(InputSourceHotKeys.check(Self.cmdOptSpace, in: nil) == .clear)
        // A plist without the two entries (never changed) is the defaults too.
        #expect(InputSourceHotKeys.chords(in: ["64": ["enabled": true]]) == nil)
        #expect(InputSourceHotKeys.check(Self.ctrlOptSpace, in: ["64": ["enabled": true]]) == .assumed)
        #expect(InputSourceHotKeys.Verdict.assumed.warning?.contains("may use") == true)
    }

    @Test func eventFlagsMapToCarbonModifiers() {
        #expect(InputSourceHotKeys.carbon(0x20000) == SystemHotKey.shift)
        #expect(InputSourceHotKeys.carbon(0x40000) == SystemHotKey.control)
        #expect(InputSourceHotKeys.carbon(0x80000) == SystemHotKey.option)
        #expect(InputSourceHotKeys.carbon(0x100000) == SystemHotKey.command)
        // The function key bit and friends are not Carbon modifiers.
        #expect(InputSourceHotKeys.carbon(0x800000 | 0x40000 | 0x80000) == SystemHotKey.control | SystemHotKey.option)
        // A HotKeySpec with stray bits compares on the four modifiers only.
        let stray = SystemHotKey(keyCode: SystemHotKey.space, carbonModifiers: SystemHotKey.control | SystemHotKey.option | 0x4000)
        #expect(InputSourceHotKeys.check(stray, in: nil) == .assumed)
    }
}
