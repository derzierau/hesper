// swift-tools-version: 6.2
import PackageDescription

// The iPhone app's screens (iOS only; built and previewed in Xcode). The
// logic they show is in ../HesperKit.
//
//   HesperDesign    the Neon console's colours, type, state marks and
//                   controls (docs/iphone/neon.md)
//   HesperFeatures  the tabs, the agent view, New agent and the root view

// UI code is main-actor by default (SE-0466), with the same upcoming
// features as HesperKit.
let settings: [SwiftSetting] = [
    .defaultIsolation(MainActor.self),
    .enableUpcomingFeature("NonisolatedNonsendingByDefault"),
    .enableUpcomingFeature("InferIsolatedConformances"),
    .enableUpcomingFeature("ExistentialAny"),
    .enableUpcomingFeature("MemberImportVisibility"),
]

let package = Package(
    name: "HesperUI",
    defaultLocalization: "en",
    platforms: [.iOS(.v26)],
    products: [
        .library(name: "HesperDesign", targets: ["HesperDesign"]),
        .library(name: "HesperFeatures", targets: ["HesperFeatures"]),
    ],
    dependencies: [
        .package(path: "../HesperKit"),
        .package(path: "../../../../app"),
    ],
    targets: [
        .target(
            name: "HesperDesign",
            dependencies: [
                .product(name: "HesperCore", package: "app"),
                .product(name: "HesperSurfaces", package: "HesperKit"),
            ],
            swiftSettings: settings
        ),
        .target(
            name: "HesperFeatures",
            dependencies: [
                "HesperDesign",
                .product(name: "HesperCore", package: "app"),
                .product(name: "HesperFleet", package: "HesperKit"),
                .product(name: "HesperFixtures", package: "HesperKit"),
                .product(name: "HesperSurfaces", package: "HesperKit"),
            ],
            swiftSettings: settings
        ),
    ],
    swiftLanguageModes: [.v6]
)
