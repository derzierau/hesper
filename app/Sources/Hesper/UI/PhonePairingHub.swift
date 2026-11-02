import AppKit
import CoreImage
import HesperCore
import Observation

/// Settings › iPhone's driver (one per daemon connection, like ReviewHub):
/// asks hesperd for a code while the pane shows, renews it, cancels the
/// open ones when the pane goes, and listens for `devices.paired`. The
/// decisions are HesperCore `PhonePairing`'s.
@MainActor
@Observable
final class PhonePairingHub {
    private static var hubs: [ObjectIdentifier: PhonePairingHub] = [:]

    static func shared(for model: AppModel) -> PhonePairingHub {
        let key = ObjectIdentifier(model.client)
        if let h = hubs[key] { return h }
        let h = PhonePairingHub(client: model.client)
        hubs[key] = h
        return h
    }

    @ObservationIgnored let client: DaemonClient
    private(set) var pairing = PhonePairing()
    /// The shown code's QR image (modules opaque, the rest clear).
    private(set) var code: CGImage?
    @ObservationIgnored private var loop: Task<Void, Never>?

    private init(client: DaemonClient) {
        self.client = client
        let key = ObjectIdentifier(client)
        client.onDeviceNotification = { method, params in
            // Reader thread: one hop to main.
            guard method == PhonePairingRPC.paired, let e = PhonePaired(params: params) else { return }
            DispatchQueue.main.async { MainActor.assumeIsolated { PhonePairingHub.hubs[key]?.paired(e) } }
        }
    }

    /// The pane shows (again): a code, unless one is up or a phone just paired.
    func open() {
        switch pairing.state {
        case .paired: return
        case .starting, .waiting, .failed: if loop != nil { return }
        case .unsupported: break // hesperd may have been updated since
        }
        restart()
    }

    /// The pane went away: no code stays open.
    func close() {
        loop?.cancel()
        loop = nil
        cancel(pairing.close(at: Date()))
        code = nil
    }

    func setRights(_ r: PhoneRights) {
        let ids = pairing.setRights(r, at: Date())
        guard !ids.isEmpty || loop != nil else { return }
        cancel(ids)
        code = nil
        restart()
    }

    /// After a pairing: "Show another code".
    func showAnother() { restart() }

    private func paired(_ e: PhonePaired) {
        guard let others = pairing.paired(e, at: Date()) else { return }
        loop?.cancel()
        loop = nil
        code = nil
        cancel(others)
    }

    private func restart() {
        loop?.cancel()
        loop = Task { [weak self] in await self?.run() }
    }

    private func run() async {
        while !Task.isCancelled {
            let request = pairing.request()
            do {
                let o = try await client.phoneOffer(pairing.rights)
                let stale = pairing.offered(o, request: request, at: Date())
                cancel(stale)
                if stale.isEmpty { code = QRCode.image(o.payload) }
            } catch {
                pairing.failed(error, request: request)
            }
            guard !Task.isCancelled, let wait = pairing.renewDelay(now: Date()) ?? pairing.retryDelay else { break }
            try? await Task.sleep(nanoseconds: UInt64(wait * 1_000_000_000))
        }
    }

    private func cancel(_ ids: [String]) {
        guard !ids.isEmpty else { return }
        let client = client
        Task.detached { for id in ids { try? await client.cancelPhoneOffer(id) } }
    }
}

/// Core Image's QR generator, as a mask: one pixel per module, so it is
/// drawn scaled with `.interpolation(.none)`.
enum QRCode {
    static func image(_ text: String) -> CGImage? {
        guard let f = CIFilter(name: "CIQRCodeGenerator") else { return nil }
        f.setValue(Data(text.utf8), forKey: "inputMessage")
        f.setValue("M", forKey: "inputCorrectionLevel")
        guard let out = f.outputImage else { return nil }
        let mask = out.applyingFilter("CIColorInvert").applyingFilter("CIMaskToAlpha")
        return CIContext().createCGImage(mask, from: mask.extent)
    }
}
