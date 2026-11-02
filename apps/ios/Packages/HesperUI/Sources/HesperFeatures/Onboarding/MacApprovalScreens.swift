import HesperCore
import HesperDesign
import HesperFleet
import SwiftUI

/// Signed in without a scan: which of the owner's Macs to ask.
struct PickMacScreen: View {
    @Environment(OnboardingModel.self) private var model

    var body: some View {
        OnboardingPage {
            OnboardingTitle(
                title: "Pick a Mac",
                lead: Text("The Macs on \(model.relay?.host() ?? "your relay"). It shows a code to approve there."))
            if model.busy && model.unpaired.isEmpty {
                ProgressView().frame(maxWidth: .infinity)
            }
            VStack(spacing: 0) {
                ForEach(model.unpaired, id: \.short) { mac in
                    Button {
                        model.choose(mac)
                    } label: {
                        HStack(spacing: DS.Spacing.m) {
                            StateMark(mac.online ? StateMarkKind.done : StateMarkKind.idle)
                            Text(mac.displayName).font(.body.weight(.semibold))
                            Spacer()
                            Text(mac.online ? "Online" : "Offline").font(.subheadline).foregroundStyle(Theme.dim)
                            Image(systemName: "chevron.right").font(.footnote).foregroundStyle(Theme.dim)
                        }
                        .padding(DS.Spacing.l)
                        .contentShape(.rect)
                    }
                    .buttonStyle(.plain)
                    .disabled(model.busy)
                    if mac.short != model.unpaired.last?.short { Divider().overlay(Theme.line) }
                }
            }
            .background(Theme.tile, in: .rect(cornerRadius: CGFloat(DesignTokens.Radius.panel), style: .continuous))
            if !model.busy && model.unpaired.isEmpty && model.failure == nil {
                Text("No Mac on this relay is waiting for this iPhone. Install Hesper on your Mac and sign it in to the same relay.")
                    .font(.subheadline)
                    .foregroundStyle(Theme.text2)
            }
            if let message = model.failure?.message() {
                Text(message).font(.subheadline).foregroundStyle(Theme.error)
            }
        } footer: {
            if model.failure != nil || model.unpaired.isEmpty {
                WideButton("Try again", primary: false) { model.reloadMacs() }.disabled(model.busy)
            }
            LinkButton("Scan a code instead") { model.scanAgain() }
        }
    }
}

/// Approve on the Mac: the code to compare, big, while the Mac waits for
/// its owner. Also the fallback for a Mac too old to approve by scan.
struct ApproveOnMacScreen: View {
    @Environment(OnboardingModel.self) private var model

    var body: some View {
        if let p = model.pending {
            OnboardingPage(back: model.back) {
                OnboardingTitle(
                    title: "Approve on \(p.name)",
                    lead: Text("Check that \(p.name) shows the same code, then approve it there."))
                Text(p.code)
                    .font(.system(.largeTitle, design: .monospaced).weight(.semibold))
                    .tracking(3)
                    .frame(maxWidth: .infinity)
                    .padding(.vertical, DS.Spacing.xl)
                    .background(Theme.tile, in: .rect(cornerRadius: CGFloat(DesignTokens.Radius.panel), style: .continuous))
                    .accessibilityLabel(p.code.map { String($0) }.joined(separator: " "))
                VStack(alignment: .leading, spacing: DS.Spacing.s) {
                    Text("Click the notification on \(p.name), or run").font(.subheadline).foregroundStyle(Theme.text2)
                    Text("hesperctl approve \(p.code)")
                        .font(.subheadline.monospaced())
                        .textSelection(.enabled)
                        .padding(DS.Spacing.m)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .background(Theme.surface, in: .rect(cornerRadius: CGFloat(DesignTokens.Radius.control)))
                }
                TimelineView(.periodic(from: .now, by: 15)) { context in
                    let left = Int(p.expires.timeIntervalSince(context.date) / 60) + 1
                    if let message = model.failure?.message(macName: p.name) {
                        Text(message).font(.subheadline).foregroundStyle(Theme.error)
                    } else if p.expires > context.date {
                        HStack(spacing: DS.Spacing.m) {
                            ProgressView().controlSize(.small)
                            Text("Waiting for \(p.name) · \(left) min left").font(.subheadline).foregroundStyle(Theme.text2)
                        }
                    } else {
                        Text("The request expired before \(p.name) approved it.").font(.subheadline).foregroundStyle(Theme.text2)
                        WideButton("Ask again") { model.askAgain() }.controlSize(.large)
                    }
                }
            } footer: {
                if model.failure != nil {
                    WideButton("Ask again") { model.askAgain() }
                }
                LinkButton("Scan a code instead") { model.scanAgain() }
            }
        }
    }
}

#Preview("Pick a Mac") {
    PickMacScreen().onboardingPreview(.pickMac)
}

#Preview("Approve on Mac") {
    ApproveOnMacScreen().onboardingPreview(.approveOnMac)
}
