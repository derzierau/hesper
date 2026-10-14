import Foundation
import Testing

/// Agents are children of hesperd (Hesper.app/Contents/MacOS/hesperd, run by
/// launchd), so hesperd is their responsible process: macOS checks Hesper.app's
/// usage descriptions and hesperd's entitlements when an agent asks for a
/// protected resource. Without them `claude /voice` is killed by TCC.
@Suite struct BundlePrivacyTests {
    static let repo = URL(fileURLWithPath: #filePath)
        .deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()

    static func plist(_ path: String) throws -> [String: Any] {
        let data = try Data(contentsOf: repo.appendingPathComponent(path))
        return try #require(PropertyListSerialization.propertyList(from: data, format: nil) as? [String: Any])
    }

    @Test func appDescribesMicrophoneUse() throws {
        let info = try Self.plist("app/Resources/Info.plist")
        let text = try #require(info["NSMicrophoneUsageDescription"] as? String)
        #expect(!text.isEmpty)
    }

    @Test func hesperdMayUseMicrophone() throws {
        let entitlements = try Self.plist("packaging/hesperd.entitlements")
        #expect(entitlements["com.apple.security.device.audio-input"] as? Bool == true)
        #expect(entitlements["com.apple.security.automation.apple-events"] as? Bool == true)
    }
}
