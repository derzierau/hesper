import HesperCore
import HesperDesign
import HesperFleet
import SwiftUI

/// Add Mac, in the Macs tab (docs/iphone/onboarding.md, "Adding more
/// Macs"; wireframes round 6, 15): the owner's other Macs on the relay
/// with Pair, Scan a code, and how to install Hesper on another Mac.
struct AddMacSection: View {
    @Environment(AddMacModel.self) private var model
    @State private var scanning = false

    /// Shared as text (AirDrop, Messages); no store link.
    static let install = "brew install --cask derzierau/hesper/hesper"

    var body: some View {
        Section {
            ForEach(model.unpaired, id: \.short) { mac in
                row(mac).themedRow()
            }
            if model.loading && model.unpaired.isEmpty {
                ProgressView().frame(maxWidth: .infinity).themedRow()
            }
            if let message = model.failure?.message() {
                Text(message).font(.footnote).foregroundStyle(Theme.dim).themedRow()
            }
        } header: {
            if !model.unpaired.isEmpty { Text("On your relay · not paired").listHeading() }
        }
        Section {
            Button("Scan a code", systemImage: "qrcode.viewfinder") { scanning = true }.themedRow()
            ShareLink(item: Self.install, subject: Text("Install Hesper"), message: Text("Install Hesper on your Mac:")) {
                Label("Install Hesper on another Mac", systemImage: "square.and.arrow.up")
            }
            .themedRow()
        }
        .task { await model.load() }
        .sheet(isPresented: $scanning) { ScanCodeSheet() }
    }

    @ViewBuilder private func row(_ mac: Machine) -> some View {
        let state = model.row(mac.short)
        HStack(spacing: DS.Spacing.m) {
            StateMark(mac.online ? StateMarkKind.done : StateMarkKind.idle)
            VStack(alignment: .leading, spacing: DS.Spacing.xxs) {
                Text(mac.displayName).font(.body.weight(.semibold))
                Group {
                    switch state {
                    case .idle, .asking: Text(mac.online ? "Online" : "Offline")
                    case .waiting(let code, _): Text("Waiting for \(mac.displayName) · \(Text(code).monospaced())")
                    case .approved: Text("Approved")
                    case .failed(let error): Text(error.message(macName: mac.displayName) ?? "")
                    }
                }
                .font(.subheadline)
                .foregroundStyle(Theme.text2)
                if case .waiting(let code, _) = state {
                    Text("Approve on \(mac.displayName): click the notification there, or run `hesperctl approve \(code)`")
                        .font(.footnote)
                        .foregroundStyle(Theme.dim)
                }
            }
            Spacer()
            switch state {
            case .idle, .failed:
                Button("Pair") { Task { await model.pair(mac.short) } }.secondaryAction().controlSize(.small)
            case .asking, .waiting:
                ProgressView().controlSize(.small)
            case .approved:
                Image(systemName: "checkmark.circle.fill").foregroundStyle(Theme.done)
            }
        }
        .accessibilityElement(children: .combine)
    }
}

/// Scan a code from the Macs tab: the same scanner as onboarding; the Mac
/// approves at once.
private struct ScanCodeSheet: View {
    @Environment(AddMacModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    @State private var notice: String?

    var body: some View {
        VStack(alignment: .leading, spacing: DS.Spacing.l) {
            // A square Neon Close in the body, like the capture sheet,
            // not the toolbar's glass circle.
            HStack(spacing: DS.Spacing.m) {
                Text("Scan a code")
                    .font(Typeface.heading())
                    .foregroundStyle(Theme.text)
                    .accessibilityAddTraits(.isHeader)
                Spacer(minLength: 0)
                Button("Close", systemImage: "xmark", role: .cancel) { dismiss() }
                    .labelStyle(.iconOnly)
                    .font(.body.weight(.semibold))
                    .secondaryAction(square: true)
            }
            Text("On the Mac, open **Hesper › Settings › iPhone**, or run `hesperctl phone`.")
                .foregroundStyle(Theme.text2)
            CodeScanner(notice: notice) { text in
                guard text.hasPrefix(PairingPayload.prefix) else { return false }
                Task { await scanned(text) }
                return true
            }
            Spacer()
        }
        .padding(DS.Spacing.xl)
        .background(Theme.background)
    }

    private func scanned(_ text: String) async {
        switch await model.scanned(text) {
        case .success(.approved)?: dismiss()
        case .success(.pending(let machine, let code, _))?: notice = "Approve on \(machine): \(code)"
        case .failure(let error)?: notice = error.message()
        case nil: break
        }
    }
}
