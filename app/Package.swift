// swift-tools-version: 6.2
import PackageDescription

let package = Package(
    name: "Hesper",
    platforms: [.macOS(.v14)],
    dependencies: [
        // libghostty as a prebuilt XCFramework (no Zig needed). Pinned exactly:
        // 2.2.2026100501 = Ghostty 35a81a980bb9fce09a1ea762a68b55f8eb3477ed.
        .package(url: "https://github.com/Lakr233/libghostty-spm.git", exact: "2.2.2026100501"),
    ],
    targets: [
        .target(name: "HesperCore", path: "Sources/HesperCore"),
        .executableTarget(
            name: "Hesper",
            dependencies: [
                "HesperCore",
                .product(name: "GhosttyKit", package: "libghostty-spm"),
            ],
            path: "Sources/Hesper"
        ),
        .testTarget(name: "HesperCoreTests", dependencies: ["HesperCore"], path: "Tests/HesperCoreTests"),
    ],
    swiftLanguageModes: [.v6]
)
