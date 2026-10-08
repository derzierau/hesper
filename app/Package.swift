// swift-tools-version: 6.2
import PackageDescription

// libghostty. By default the prebuilt XCFramework from libghostty-spm (no Zig
// needed), pinned exactly: 2.2.2026100501 = Ghostty 35a81a980bb9fce09a1ea762a68b55f8eb3477ed.
// It statically includes GNU libintl (LGPL-2.1). HESPER_GHOSTTYKIT names an
// XCFramework relative to this directory to link instead: the Makefile sets
// it to Vendor/GhosttyKit.xcframework when that exists
// (scripts/build-ghosttykit.sh: the same pin built from source with
// -Di18n=false and without libintl), as in release builds (docs/licensing.md).
// An environment variable, not a file check: SwiftPM caches the evaluated
// manifest, keyed on its text and the environment.
let vendoredGhosttyKit = Context.environment["HESPER_GHOSTTYKIT"].flatMap { $0.isEmpty ? nil : $0 }

// Hesper imports the module libghostty (both frameworks' modulemap name), so
// it needs no GhosttyKit wrapper target of its own.
let libghostty: Target.Dependency = vendoredGhosttyKit != nil
    ? "libghosttySource"
    : .product(name: "GhosttyKit", package: "libghostty-spm")

let libghosttyTargets: [Target] = vendoredGhosttyKit.map { [.binaryTarget(name: "libghosttySource", path: $0)] } ?? []

let package = Package(
    name: "Hesper",
    platforms: [.macOS(.v14)],
    dependencies: [
        .package(url: "https://github.com/Lakr233/libghostty-spm.git", exact: "2.2.2026100501"),
    ],
    targets: libghosttyTargets + [
        .target(name: "HesperCore", path: "Sources/HesperCore"),
        .executableTarget(
            name: "Hesper",
            dependencies: ["HesperCore", libghostty],
            path: "Sources/Hesper",
            // What libghostty needs (libghostty-spm's GhosttyKit target links the same).
            linkerSettings: [.linkedLibrary("c++"), .linkedFramework("Carbon")]
        ),
        .testTarget(name: "HesperCoreTests", dependencies: ["HesperCore"], path: "Tests/HesperCoreTests"),
    ],
    swiftLanguageModes: [.v6]
)
