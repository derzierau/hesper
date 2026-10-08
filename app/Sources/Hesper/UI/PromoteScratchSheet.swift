import AppKit
import HesperCore
import SwiftUI

/// "Promote to project…": a scratch becomes a project (`projects.promote`):
/// its folder moves to ~/projects/<name>, keeping its git history and its
/// id, optionally as a new private GitHub repository. A sheet on the
/// window: the name, the repo toggle, Promote (⏎) and Cancel (esc).
struct PromoteScratchSheet: View {
    var scratchName: String
    var folder: String?
    @State var name: String
    @State var createRepo = false
    var onPromote: (String, Bool) -> Void
    var onCancel: () -> Void

    static let width: CGFloat = 420

    init(scratchName: String, folder: String?, suggested: String, createRepo: Bool = false,
         onPromote: @escaping (String, Bool) -> Void, onCancel: @escaping () -> Void) {
        self.scratchName = scratchName; self.folder = folder
        _name = State(initialValue: suggested)
        _createRepo = State(initialValue: createRepo)
        self.onPromote = onPromote; self.onCancel = onCancel
    }

    private var trimmed: String { name.trimmingCharacters(in: .whitespacesAndNewlines) }

    var body: some View {
        VStack(alignment: .leading, spacing: DS.Spacing.l) {
            VStack(alignment: .leading, spacing: DS.Spacing.xs) {
                Text("Promote to project").font(DS.font(.panelTitle, .semibold)).foregroundStyle(Theme.fg)
                    .accessibilityAddTraits(.isHeader)
                Text("“\(scratchName)” moves to ~/projects/\(PromoteScratchSheet.folderName(trimmed)) with its git history; its agents and sessions stay linked.")
                    .font(DS.font(.chrome)).foregroundStyle(Theme.dim)
                    .fixedSize(horizontal: false, vertical: true)
                if let folder {
                    Text((folder as NSString).abbreviatingWithTildeInPath).font(DS.font(.meta)).foregroundStyle(Theme.dim)
                        .lineLimit(1).truncationMode(.middle)
                }
            }
            VStack(alignment: .leading, spacing: DS.Spacing.s) {
                Text("Name").font(DS.font(.chrome, .medium)).foregroundStyle(Theme.fg2)
                TextField("Project name", text: $name)
                    .textFieldStyle(.plain)
                    .font(DS.font(.body))
                    .padding(.horizontal, DS.Spacing.m)
                    .frame(height: DS.chromeMaxHeight)
                    .background(DS.Radius.shape(DS.Radius.control).fill(Theme.color(.background)))
                    .overlay(DS.Radius.shape(DS.Radius.control).strokeBorder(Theme.stroke, lineWidth: 1))
                    .onSubmit { if !trimmed.isEmpty { onPromote(trimmed, createRepo) } }
                    .accessibilityLabel("Project name")
                    .accessibilityIdentifier("promote.name")
                Toggle(isOn: $createRepo) {
                    VStack(alignment: .leading, spacing: DS.Spacing.xxs) {
                        Text("Create GitHub repo").font(DS.font(.body)).foregroundStyle(Theme.fg)
                        Text("Private, pushed with gh").font(DS.font(.chrome)).foregroundStyle(Theme.dim)
                    }
                }
                .toggleStyle(.checkbox)
                .tint(Theme.accent)
                .accessibilityIdentifier("promote.createRepo")
            }
            HStack(spacing: DS.Spacing.m) {
                Spacer()
                Button(action: onCancel) { Pill("Cancel", variant: .segment(selected: false), kbd: "esc") }
                    .buttonStyle(.plain)
                    .keyboardShortcut(.cancelAction)
                    .accessibilityIdentifier("promote.cancel")
                Button { onPromote(trimmed, createRepo) } label: { Pill("Promote", variant: .segment(selected: true), kbd: "⏎") }
                    .buttonStyle(.plain)
                    .disabled(trimmed.isEmpty)
                    .opacity(trimmed.isEmpty ? OverlayLook.disabledOpacity : 1)
                    .keyboardShortcut(.defaultAction)
                    .accessibilityIdentifier("promote.go")
            }
        }
        .padding(DS.Spacing.xl)
        .frame(width: Self.width, alignment: .leading)
        .background(Theme.color(.surface))
    }

    /// The folder name the project gets (hesperd's slug of the name).
    static func folderName(_ name: String) -> String {
        ScratchName.slug(name, limit: 64)
    }
}

/// Runs the sheet on `window` (or as its own window without one).
@MainActor
enum PromoteScratchPresenter {
    static func present(on window: NSWindow?, scratchName: String, folder: String?, suggested: String,
                        promote: @escaping (String, Bool) -> Void) {
        let sheet = NSWindow(contentRect: NSRect(x: 0, y: 0, width: PromoteScratchSheet.width, height: 10),
                             styleMask: [.titled], backing: .buffered, defer: true)
        func close() {
            if let parent = sheet.sheetParent { parent.endSheet(sheet) } else { sheet.close() }
        }
        let view = PromoteScratchSheet(scratchName: scratchName, folder: folder, suggested: suggested,
                                       onPromote: { name, repo in close(); promote(name, repo) },
                                       onCancel: { close() })
        let host = NSHostingView(rootView: view)
        sheet.contentView = host
        sheet.setContentSize(host.fittingSize)
        sheet.setAccessibilityIdentifier("promote.sheet")
        if let window { window.beginSheet(sheet) } else { sheet.center(); sheet.makeKeyAndOrderFront(nil) }
    }
}
