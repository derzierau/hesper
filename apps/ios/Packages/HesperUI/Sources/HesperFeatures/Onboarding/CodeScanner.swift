import AVFoundation
import HesperDesign
import SwiftUI
import UIKit
import Vision
import VisionKit

/// The viewfinder for a Mac's pairing code, with the camera permission
/// asked on the tap that needs it (docs/iphone/onboarding.md,
/// "Permissions"). `found` gets each QR code's text and says whether it
/// was a Hesper code; anything else is ignored, with a hint after 5 s.
struct CodeScanner: View {
    /// The way around a declined camera, or a device that cannot scan.
    var enterAddress: (() -> Void)?
    var notice: String?
    let found: (String) -> Bool

    @Environment(\.openURL) private var openURL
    @Environment(\.scenePhase) private var scenePhase
    @State private var camera = AVCaptureDevice.authorizationStatus(for: .video)
    @State private var scanning = false
    @State private var hint = false
    @State private var matched = false

    var body: some View {
        VStack(spacing: DS.Spacing.l) {
            switch (DataScannerViewController.isSupported, camera) {
            case (false, _):
                off("This device can't scan codes.", "Enter your relay's address instead.", settings: false)
            case (true, .denied), (true, .restricted):
                off("The camera is off for Hesper.", "Turn it on to scan, or enter your relay's address.", settings: true)
            case (true, .authorized) where scanning:
                viewfinder
            default:
                VStack(spacing: DS.Spacing.l) {
                    Image(systemName: "qrcode.viewfinder")
                        .font(.system(size: 64, weight: .light))
                        .foregroundStyle(Theme.dim)
                        .frame(maxWidth: .infinity, minHeight: Self.height)
                        .background(Theme.tile, in: .rect(cornerRadius: Self.radius))
                    WideButton("Scan the code") { Task { await start() } }
                }
            }
            if let notice {
                Text(notice).font(.footnote).foregroundStyle(Theme.question).multilineTextAlignment(.center)
            } else if hint, !matched {
                Text("Point at the code in Hesper › Settings › iPhone").font(.footnote).foregroundStyle(Theme.dim)
            }
            if let enterAddress {
                if cameraOff {
                    WideButton("Enter address instead", action: enterAddress).controlSize(.large)
                } else {
                    LinkButton("Enter address instead", action: enterAddress)
                }
            }
        }
        .onChange(of: scenePhase) { _, phase in
            // Back from Settings with the camera on.
            if phase == .active { camera = AVCaptureDevice.authorizationStatus(for: .video) }
        }
        .task {
            // Allowed before: the viewfinder opens at once.
            if camera == .authorized { scanning = true }
        }
    }

    private var viewfinder: some View {
        QRScannerView { text in
            guard !matched, found(text) else { return }
            matched = true
        }
        .frame(height: Self.height)
        .clipShape(.rect(cornerRadius: Self.radius))
        .overlay {
            RoundedRectangle(cornerRadius: DS.Spacing.xl, style: .continuous)
                .strokeBorder(matched ? Theme.done : Theme.text.opacity(0.7), lineWidth: 3)
                .frame(width: 180, height: 180)
                .accessibilityHidden(true)
        }
        .sensoryFeedback(.success, trigger: matched) { _, now in now }
        .task(id: notice) {
            // A notice (expired code): look again.
            matched = false
            hint = false
            try? await Task.sleep(for: .seconds(5))
            hint = true
        }
        .accessibilityLabel("Camera viewfinder")
    }

    private func off(_ title: String, _ detail: String, settings: Bool) -> some View {
        VStack(spacing: DS.Spacing.m) {
            Image(systemName: "video.slash").font(.title).foregroundStyle(Theme.dim)
            Text(title).font(.headline)
            Text(detail).font(.subheadline).foregroundStyle(Theme.text2)
            if settings {
                Button("Open Settings") {
                    if let url = URL(string: UIApplication.openSettingsURLString) { openURL(url) }
                }
                .secondaryAction()
                .padding(.top, DS.Spacing.s)
            }
        }
        .multilineTextAlignment(.center)
        .padding(DS.Spacing.xl)
        .frame(maxWidth: .infinity, minHeight: Self.height)
        .background(Theme.tile, in: .rect(cornerRadius: Self.radius))
    }

    /// No camera to scan with: the way around becomes the main button.
    private var cameraOff: Bool {
        !DataScannerViewController.isSupported || camera == .denied || camera == .restricted
    }

    /// The tap on Scan: the system camera alert comes up here the first
    /// time; this screen is its context.
    private func start() async {
        if camera == .notDetermined {
            _ = await AVCaptureDevice.requestAccess(for: .video)
            camera = AVCaptureDevice.authorizationStatus(for: .video)
        }
        scanning = camera == .authorized
    }

    static let height: CGFloat = 300
    static let radius: CGFloat = 26
}

/// VisionKit's scanner, QR codes only. Reports each code's text once per
/// appearance in view.
private struct QRScannerView: UIViewControllerRepresentable {
    let found: (String) -> Void

    func makeUIViewController(context: Context) -> DataScannerViewController {
        let scanner = DataScannerViewController(
            recognizedDataTypes: [.barcode(symbologies: [.qr])], qualityLevel: .balanced, recognizesMultipleItems: false,
            isHighFrameRateTrackingEnabled: false, isHighlightingEnabled: false)
        scanner.delegate = context.coordinator
        try? scanner.startScanning()
        return scanner
    }

    func updateUIViewController(_ scanner: DataScannerViewController, context: Context) {
        context.coordinator.found = found
    }

    static func dismantleUIViewController(_ scanner: DataScannerViewController, coordinator: Coordinator) {
        scanner.stopScanning()
    }

    func makeCoordinator() -> Coordinator { Coordinator(found: found) }

    final class Coordinator: NSObject, DataScannerViewControllerDelegate {
        var found: (String) -> Void

        init(found: @escaping (String) -> Void) {
            self.found = found
        }

        func dataScanner(_ scanner: DataScannerViewController, didAdd items: [RecognizedItem], allItems: [RecognizedItem]) {
            for case .barcode(let code) in items {
                if let text = code.payloadStringValue { found(text) }
            }
        }
    }
}
