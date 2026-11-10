// swift-tools-version: 6.2
import PackageDescription

// The agent view's terminal: a Mac's PTY bytes drawn by libghostty.
//
//   TerminalModel   how the grid is scaled and panned on the phone, when
//                   the view follows the cursor, and how output bursts are
//                   coalesced. No UIKit, so `swift test` runs it on a Mac.
//   HesperTerminal  the libghostty surface (iOS only; GhosttyTerminal is
//                   linked for iOS alone, so a Mac never builds it)

// Same upcoming features as HesperKit. The surface is UI code, so it is
// main-actor by default like HesperUI (SE-0466).
let shared: [SwiftSetting] = [
    .enableUpcomingFeature("NonisolatedNonsendingByDefault"),
    .enableUpcomingFeature("InferIsolatedConformances"),
    .enableUpcomingFeature("ExistentialAny"),
    .enableUpcomingFeature("MemberImportVisibility"),
    // `make build` relies on this: Config/Base.xcconfig.
    .treatAllWarnings(as: .error),
]

let package = Package(
    name: "HesperTerminal",
    // macOS only so `swift test` runs TerminalModel on a Mac.
    platforms: [.iOS(.v26), .macOS(.v15)],
    products: [
        .library(name: "TerminalModel", targets: ["TerminalModel"]),
        .library(name: "HesperTerminal", targets: ["HesperTerminal"]),
    ],
    dependencies: [
        // The Mac app pins the same version (app/Package.swift).
        .package(url: "https://github.com/Lakr233/libghostty-spm.git", exact: "2.2.2026100501")
    ],
    targets: [
        .target(name: "TerminalModel", swiftSettings: shared),
        .target(
            name: "HesperTerminal",
            dependencies: [
                "TerminalModel",
                .product(
                    name: "GhosttyTerminal",
                    package: "libghostty-spm",
                    condition: .when(platforms: [.iOS])
                ),
            ],
            swiftSettings: shared + [.defaultIsolation(MainActor.self)]
        ),
        .testTarget(name: "TerminalModelTests", dependencies: ["TerminalModel"], swiftSettings: shared),
    ],
    swiftLanguageModes: [.v6]
)
