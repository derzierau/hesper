// swift-tools-version: 6.2
import PackageDescription

// The iPhone app's logic: no views, so `swift test` runs it on a Mac. The
// screens are in ../HesperUI; the app and extension targets in project.yml
// stay thin. Wire models come from the Mac app's HesperCore, so the phone
// and the Mac decode the same contract.
//
//   HesperFleet     what every Mac reports, folded into one fleet, and the
//                   app's observable model over a link
//   HesperFixtures  a fake link with sample agents, for previews and tests
//   HesperSurfaces  what the app shares with its extensions
//   HesperLink      the live link: keys, enrollment, the relay socket, the
//                   end-to-end channel, `LiveLink` and `LivePairing`

// Swift 6 with approachable concurrency: async functions run on the
// caller's actor unless marked @concurrent (SE-0461), and conformances of
// main-actor types are main-actor isolated (SE-0470).
let shared: [SwiftSetting] = [
    .enableUpcomingFeature("NonisolatedNonsendingByDefault"),
    .enableUpcomingFeature("InferIsolatedConformances"),
    .enableUpcomingFeature("ExistentialAny"),
    .enableUpcomingFeature("MemberImportVisibility"),
    // `make build` relies on this: Config/Base.xcconfig.
    .treatAllWarnings(as: .error),
]
let package = Package(
    name: "HesperKit",
    defaultLocalization: "en",
    // macOS only so `swift test` runs the pure targets on a Mac.
    platforms: [.iOS(.v26), .macOS(.v15)],
    products: [
        .library(name: "HesperFleet", targets: ["HesperFleet"]),
        .library(name: "HesperFixtures", targets: ["HesperFixtures"]),
        .library(name: "HesperSurfaces", targets: ["HesperSurfaces"]),
        .library(name: "HesperLink", targets: ["HesperLink"]),
    ],
    dependencies: [
        // HesperCore only; resolving it also fetches libghostty-spm, which
        // the agent view will use.
        .package(path: "../../../../app"),
    ],
    targets: [
        .target(
            name: "HesperFleet",
            dependencies: [.product(name: "HesperCore", package: "app")],
            swiftSettings: shared
        ),
        .target(name: "HesperFixtures", dependencies: ["HesperFleet"], swiftSettings: shared),
        .target(name: "HesperSurfaces", dependencies: ["HesperFleet"], swiftSettings: shared),
        .target(
            name: "HesperLink",
            dependencies: ["HesperFleet", .product(name: "HesperCore", package: "app")],
            swiftSettings: shared
        ),
        .testTarget(name: "HesperLinkTests", dependencies: ["HesperLink"], swiftSettings: shared),
        .testTarget(name: "HesperFleetTests", dependencies: ["HesperFleet", "HesperFixtures", "HesperSurfaces"], swiftSettings: shared),
    ],
    swiftLanguageModes: [.v6]
)
