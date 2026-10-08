import AppKit
import HesperCore
import SwiftUI

/// ⌘,: one sidebar window. General (the wall, quick launch,
/// notifications), Machines & pairing, Agents & profiles, Appearance
/// (Dusk / Daylight / System, density), Shortcuts (read only) and Advanced
/// (desks, the first run, hesperd). The sidebar is Rows, choices are
/// segmented Pills; ⌘1…⌘6 switch panes.
struct SettingsView: View {
    @Bindable var model: AppModel
    var onHotKeyChanged: () -> Void = {}
    /// Desks (the window layer's DeskController).
    var desks: DeskController? = nil
    /// The wall in front (window layer): Wall › Layout and the minimum
    /// width are that wall's own (nil: `model`'s).
    var front: FrontWall? = nil
    @State private var pane = Pane.general

    /// The wall whose layout the Wall section edits.
    private var wall: AppModel { front?.model ?? model }
    @State private var deskRefresh = 0

    enum Pane: String, CaseIterable, Identifiable {
        case general, machines, agents, appearance, shortcuts, advanced
        var id: String { rawValue }
        var title: String {
            switch self {
            case .general: return "General"
            case .machines: return "Machines & pairing"
            case .agents: return "Agents & profiles"
            case .appearance: return "Appearance"
            case .shortcuts: return "Shortcuts"
            case .advanced: return "Advanced"
            }
        }
    }

    static let size = NSSize(width: 720, height: 540)
    static let sidebarWidth: CGFloat = 196
    static let labelWidth: CGFloat = 168
    static let sliderWidth: CGFloat = 180

    var body: some View {
        HStack(spacing: 0) {
            sidebar
            Rectangle().fill(Theme.stroke).frame(width: 1)
            ScrollView {
                VStack(alignment: .leading, spacing: DS.Spacing.xl) {
                    Text(pane.title).font(DS.font(.sheetTitle, .semibold)).foregroundStyle(Theme.fg)
                    content
                }
                .padding(.horizontal, DS.Spacing.xxl)
                .padding(.top, DS.chromeMaxHeight + DS.Spacing.l)
                .padding(.bottom, DS.Spacing.xxl)
                .frame(maxWidth: .infinity, alignment: .leading)
            }
            .accessibilityIdentifier("settings.\(pane.rawValue)")
        }
        .frame(width: Self.size.width, height: Self.size.height)
        .background(Theme.color(.background))
        .font(DS.font(.body))
        .foregroundStyle(Theme.fg)
        .background(SettingsWindowStyle())
        .ignoresSafeArea()
    }

    private var sidebar: some View {
        VStack(alignment: .leading, spacing: DS.Spacing.xxs) {
            ForEach(Array(Pane.allCases.enumerated()), id: \.element) { i, p in
                Button { pane = p } label: { Row(title: p.title, kbd: "⌘\(i + 1)", selected: p == pane) }
                    .buttonStyle(.plain)
                    .keyboardShortcut(KeyEquivalent(Character("\(i + 1)")), modifiers: .command)
                    .accessibilityAddTraits(p == pane ? .isSelected : [])
                    .accessibilityIdentifier("settings.pane.\(p.rawValue)")
            }
            Spacer()
        }
        .padding(.horizontal, DS.Spacing.m)
        .padding(.top, DS.chromeMaxHeight + DS.Spacing.l)
        .frame(width: Self.sidebarWidth)
        .frame(maxHeight: .infinity)
        .background(Theme.surface)
    }

    @ViewBuilder private var content: some View {
        switch pane {
        case .general: general
        case .machines: machines
        case .agents: profiles
        case .appearance: appearance
        case .shortcuts: shortcuts
        case .advanced: advanced
        }
    }

    // MARK: General

    @ViewBuilder private var general: some View {
        SettingsSection("Wall", footer: "Each wall keeps its own layout; this is the frontmost wall's (also ⌥⌘L for the next layout, and the layout button in the toolbar). Every tile uses the tile font and shows as much of its agent as fits; cards never get narrower than the minimum width, the wall adds rows or scrolls instead.") {
            SettingsLine("Layout") {
                Segments(LayoutSwitcher(wall.arrangement).rows.map { r in (r.arrangement, r.title, r.shortcut) }, selection: Bindable(wall).arrangement)
                    .accessibilityIdentifier("settings.layout")
            }
            SettingsLine("Minimum card width") {
                Segments([(60, "60 · dense", nil), (80, "80", nil), (100, "100 characters", nil)], selection: Bindable(wall).minChars)
                    .accessibilityIdentifier("settings.minChars")
            }
            SettingsLine("Tile font size") {
                HStack(spacing: DS.Spacing.m) {
                    Slider(value: Bindable(model.settings).tileFontSize, in: AppSettings.tileFontRange, step: 0.5)
                        .frame(width: Self.sliderWidth)
                        .accessibilityIdentifier("settings.tileFont")
                    Text(String(format: "%.1f pt", model.settings.tileFontSize)).font(DS.font(.meta)).foregroundStyle(Theme.dim).monospacedDigit()
                }
            }
        }
        SettingsSection("Quick launch", footer: "Opens the composer as a small floating panel over any app; ⌘↩ starts the agent and a notification takes you to it. Also in the menu bar item.") {
            SettingsLine("From anywhere") {
                Toggle("Quick launch with a global hotkey", isOn: Binding(get: { model.settings.quickLaunchEnabled },
                                                                          set: { model.settings.quickLaunchEnabled = $0; onHotKeyChanged() }))
            }
            SettingsLine("Hotkey") {
                HStack(spacing: DS.Spacing.m) {
                    HotKeyRecorder(spec: Binding(get: { model.settings.hotKey }, set: { model.settings.hotKey = $0; onHotKeyChanged() }))
                    Button("Reset to ⌃⌥Space") { model.settings.hotKey = .default; onHotKeyChanged() }
                        .disabled(model.settings.hotKey == .default)
                }
                .disabled(!model.settings.quickLaunchEnabled)
            }
        }
        SettingsSection("Notifications", footer: "Approvals, questions and errors, unless you are looking at that agent: \"<agent> on <machine> needs you\" with the exact question, grouped per machine. Approvals can be allowed or denied from the notification. Starting from quick launch always confirms with a notification you can click.") {
            SettingsLine("Needs you") {
                Toggle("Notify when an agent needs you", isOn: Bindable(model.settings).notificationsEnabled)
            }
            SettingsLine("Sound") {
                Toggle("Play a sound for approvals", isOn: Bindable(model.settings).notificationSound)
                    .disabled(!model.settings.notificationsEnabled)
            }
        }
    }

    // MARK: Machines & pairing

    @ViewBuilder private var machines: some View {
        let local = model.localMachine
        SettingsSection("Machines") {
            if model.machines.isEmpty {
                Text(model.isConnected ? "Only this Mac." : (model.connectionMessage ?? "Not connected to hesperd."))
                    .font(DS.font(.chrome)).foregroundStyle(Theme.dim)
            }
            ForEach(model.machines, id: \.short) { m in
                let n = model.registry.agents.values.filter { $0.machine == m.short }.count
                Row(title: m.short == local ? "\(m.displayName) (this Mac)" : m.displayName,
                    subtitle: Self.route(m),
                    meta: "\(m.short) · \(n) agent\(n == 1 ? "" : "s")",
                    mark: m.online ? .done : .idle)
            }
        }
        SettingsSection("Pair another Mac", footer: "Pairing runs in hesperctl on both Macs; Hesper never asks for your relay credentials. The new Mac shows up here once it's approved.") {
            Text("On the other Mac, run:").font(DS.font(.chrome)).foregroundStyle(Theme.fg2)
            CommandText("hesperctl pair-host --machine \(local)")
            Text("then approve its code here:").font(DS.font(.chrome)).foregroundStyle(Theme.fg2)
            CommandText("hesperctl approve CODE")
        }
    }

    static func route(_ m: Machine) -> String {
        if m.route == "local" { return "local" }
        guard m.online else { return "offline" }
        return [m.route ?? "", m.rttMs.map { "\(Int($0)) ms" } ?? ""].filter { !$0.isEmpty }.joined(separator: " · ")
    }

    // MARK: Agents & profiles

    @ViewBuilder private var profiles: some View {
        let all = model.lists.profiles?.profiles ?? [:]
        let kinds = Array(Set(all.values.map(\.kind))).sorted()
        SettingsSection("Closing agents", footer: "⌘W closes an agent: it leaves the wall at once and its conversation stays in History (⌘Z or ⌘⇧T resumes it). ⌥⌘W keeps it running in the background, ⌃⌘W kills it and leaves the pane, ⇧⌘W closes every finished agent in the band or wall.") {
            SettingsLine("Ghost cards") {
                Toggle("Show closed agents as ghost cards", isOn: Bindable(model.settings).ghostCards)
                    .accessibilityIdentifier("settings.ghostCards")
            }
            SettingsLine("Auto-tidy") {
                Toggle("Close finished agents after 30 min", isOn: Bindable(model.settings).autoTidy)
                    .accessibilityIdentifier("settings.autoTidy")
            }
        }
        SettingsSection("Default profile per tool", footer: "A draft uses its project's last profile, else this default for the tool; type /profile in the task to choose one.") {
            ForEach(kinds.isEmpty ? ["claude", "codex", "shell"] : kinds, id: \.self) { kind in
                let names = all.filter { $0.value.kind == kind }.keys.sorted()
                SettingsLine(Theme.kindLabel(kind)) {
                    Picker(Theme.kindLabel(kind), selection: Binding(get: { model.settings.defaultProfiles[kind] ?? "" },
                                                                     set: { model.settings.defaultProfiles[kind] = $0.isEmpty ? nil : $0 })) {
                        Text("hesperd's default (\(model.lists.profiles?.defaults?.kinds?[kind] ?? "–"))").tag("")
                        ForEach(names, id: \.self) { Text($0).tag($0) }
                    }
                    .labelsHidden()
                    .fixedSize()
                }
            }
            SettingsLine("New agents run") {
                Pill(Theme.kindLabel(model.lists.profiles?.defaults?.kind ?? "claude"))
            }
        }
        if !all.isEmpty {
            SettingsSection("Profiles") {
                ForEach(all.keys.sorted(), id: \.self) { name in
                    Row(title: name, meta: all[name].map { Theme.kindLabel($0.kind) })
                }
            }
        }
    }

    // MARK: Appearance

    @ViewBuilder private var appearance: some View {
        SettingsSection("Scheme", footer: "Dusk is dark, Daylight is light; System follows macOS. Terminals stay dark in every scheme.") {
            SettingsLine("Appearance") {
                Segments(AppearanceChoice.allCases.map { ($0, $0.title, nil) }, selection: Bindable(model.settings).appearance)
                    .accessibilityIdentifier("settings.appearance")
            }
        }
        SettingsSection("Density", footer: "Compact uses 22 pt tile headers and 6 pt gutters, for a 16-agent wall on a laptop screen.") {
            SettingsLine("Wall") {
                Segments(DS.Density.allCases.map { ($0, $0.label, nil) }, selection: Bindable(model.settings).density)
                    .accessibilityIdentifier("settings.density")
            }
        }
    }

    // MARK: Shortcuts

    @ViewBuilder private var shortcuts: some View {
        ForEach(ShortcutList.groups(hotKey: model.settings.quickLaunchEnabled ? model.settings.hotKey.label : nil), id: \.title) { g in
            SettingsSection(g.title) {
                VStack(spacing: 0) {
                    ForEach(g.items, id: \.title) { s in Row(title: s.title, kbd: s.keys) }
                }
            }
        }
    }

    // MARK: Advanced

    @ViewBuilder private var advanced: some View {
        let _ = deskRefresh
        let list = desks?.desksForSetup ?? []
        SettingsSection("Desks", footer: "A desk is every wall (scope, grouping, layout, collapsed bands, sidebar) and agent window (tabs, full screen) on your displays. Each display setup keeps its own; a new setup starts as one wall on the main display. Spaces can't be restored.") {
            SettingsLine("Displays change") {
                Toggle("Switch desks automatically", isOn: Bindable(model.settings).desksAutoSwitch)
                    .accessibilityIdentifier("settings.desksAutoSwitch")
            }
            SettingsLine("New display setup") {
                Segments([(FoldAgentWindows.tabs, "Agent windows as tabs", nil), (FoldAgentWindows.close, "Close them", nil)],
                         selection: Bindable(model.settings).foldAgentWindows)
            }
            if list.isEmpty {
                Text("Desks for these displays are saved when you arrange windows.").font(DS.font(.chrome)).foregroundStyle(Theme.dim)
            }
            ForEach(list, id: \.id) { d in
                HStack(spacing: DS.Spacing.m) {
                    StateMark(d.id == desks?.currentDesk?.id ? .working : .idle)
                    TextField("Name", text: Binding(get: { d.name }, set: { desks?.rename(d.id, to: $0); deskRefresh += 1 }))
                        .textFieldStyle(.roundedBorder)
                    if d.id == desks?.currentDesk?.id { Pill("current", variant: .count) }
                    if !d.automatic {
                        Button("Remove") { desks?.remove(d.id); deskRefresh += 1 }
                    }
                }
            }
        }
        .onAppear { deskRefresh += 1 }
        SettingsSection("First run", footer: "The three steps: pair this Mac, add another, start your first agent.") {
            SettingsLine("Welcome") {
                Button("Show the first run again") { FirstRun.show(model: model) }
                    .accessibilityIdentifier("settings.firstRun")
            }
        }
        SettingsSection("hesperd") {
            if let h = model.registry.hello {
                Row(title: "This Mac", meta: h.machine)
                Row(title: "Version", meta: h.version)
            } else {
                Text(model.connectionMessage ?? "Not connected.").font(DS.font(.chrome)).foregroundStyle(Theme.dim)
            }
        }
    }
}

// MARK: Parts

/// A titled group: a quiet heading, the controls on `surface`, a footnote.
private struct SettingsSection<Content: View>: View {
    var title: String
    var footer: String?
    @ViewBuilder var content: Content

    init(_ title: String, footer: String? = nil, @ViewBuilder content: () -> Content) {
        self.title = title; self.footer = footer; self.content = content()
    }

    var body: some View {
        VStack(alignment: .leading, spacing: DS.Spacing.s) {
            Text(title).font(DS.font(.chrome, .semibold)).foregroundStyle(Theme.dim)
                .accessibilityAddTraits(.isHeader)
            VStack(alignment: .leading, spacing: DS.Spacing.m) { content }
                .padding(DS.Spacing.l)
                .frame(maxWidth: .infinity, alignment: .leading)
                .background(DS.Radius.shape(DS.Radius.tile).fill(Theme.surface))
                .overlay(DS.Radius.shape(DS.Radius.tile).strokeBorder(Theme.stroke))
            if let footer {
                Text(footer).font(DS.font(.chrome)).foregroundStyle(Theme.dim)
                    .fixedSize(horizontal: false, vertical: true)
            }
        }
    }
}

/// A label on the left, its control on the right.
private struct SettingsLine<Control: View>: View {
    var label: String
    @ViewBuilder var control: Control

    init(_ label: String, @ViewBuilder control: () -> Control) { self.label = label; self.control = control() }

    var body: some View {
        HStack(alignment: .firstTextBaseline, spacing: DS.Spacing.l) {
            Text(label).font(DS.font(.body)).foregroundStyle(Theme.fg2)
                .frame(width: SettingsView.labelWidth, alignment: .leading)
            control.frame(maxWidth: .infinity, alignment: .leading)
        }
    }
}

/// A segmented choice made of Pills (one selected), each with an optional
/// Kbd.
private struct Segments<T: Hashable>: View {
    var options: [(value: T, title: String, kbd: String?)]
    @Binding var selection: T

    init(_ options: [(T, String, String?)], selection: Binding<T>) {
        self.options = options.map { (value: $0.0, title: $0.1, kbd: $0.2) }
        _selection = selection
    }

    var body: some View {
        HStack(spacing: DS.Spacing.xxs) {
            ForEach(Array(options.enumerated()), id: \.offset) { _, o in
                Button { selection = o.value } label: { Pill(o.title, variant: .segment(selected: o.value == selection), kbd: o.kbd) }
                    .buttonStyle(.plain)
                    .accessibilityLabel(o.title)
                    .accessibilityAddTraits(o.value == selection ? .isSelected : [])
            }
        }
        .padding(DS.Spacing.xxs)
        .overlay(DS.Radius.shape(DS.Radius.control + DS.Spacing.xxs).strokeBorder(Theme.stroke))
    }
}

/// A shell command in Geist Mono with a copy button (Settings, first run).
struct CommandText: View {
    var command: String
    init(_ command: String) { self.command = command }

    var body: some View {
        HStack(spacing: DS.Spacing.m) {
            Text(command).font(DS.font(.meta)).foregroundStyle(Theme.fg).textSelection(.enabled)
            Spacer(minLength: DS.Spacing.m)
            IconButton("doc.on.doc", help: "Copy") {
                NSPasteboard.general.clearContents()
                NSPasteboard.general.setString(command, forType: .string)
            }
        }
        .padding(.leading, DS.Spacing.m)
        .padding(.vertical, DS.Spacing.xxs)
        .background(DS.Radius.shape(DS.Radius.control).fill(Theme.chipBG))
    }
}

/// The settings window: content under a transparent title bar, so the
/// sidebar runs to the top.
private struct SettingsWindowStyle: NSViewRepresentable {
    func makeNSView(context: Context) -> NSView { NSView() }
    func updateNSView(_ v: NSView, context: Context) {
        DispatchQueue.main.async {
            guard let w = v.window, !w.styleMask.contains(.fullSizeContentView) else { return }
            w.styleMask.insert(.fullSizeContentView)
            w.titlebarAppearsTransparent = true
            w.titleVisibility = .hidden
        }
    }
}

/// Settings › Shortcuts: the app's keys, read only (they mirror KeyRouter
/// and the menus).
enum ShortcutList {
    struct Item { var title: String; var keys: String }
    struct Group { var title: String; var items: [Item] }

    static func groups(hotKey: String?) -> [Group] {
        var general = [Item(title: "New agent", keys: "⌘N"), Item(title: "Next agent needing you", keys: "⌘J"),
                       Item(title: "Search and commands", keys: "⌘K"), Item(title: "History", keys: "⌘Y"),
                       Item(title: "Settings", keys: "⌘,")]
        if let hotKey { general.append(Item(title: "Quick launch, from any app", keys: hotKey)) }
        return [
            Group(title: "General", items: general),
            Group(title: "Agents", items: [
                Item(title: "Open full size / back to the wall", keys: "⌘↩"),
                Item(title: "Back to the wall", keys: "⌘esc"),
                Item(title: "Previous / next agent", keys: "⌥⌘← →"),
                Item(title: "Previous / next agent (also)", keys: "⌘[ ⌘]"),
                Item(title: "Open in a new window", keys: "⌥⌘↩"),
                Item(title: "Close (resumable from History)", keys: "⌘W"),
                Item(title: "Send to the background", keys: "⌥⌘W"),
                Item(title: "Kill now (the pane stays)", keys: "⌃⌘W"),
                Item(title: "Tidy up finished agents", keys: "⇧⌘W"),
                Item(title: "Reopen the last closed agent", keys: "⇧⌘T"),
                Item(title: "Move to another Mac", keys: "⇧⌘M"),
                Item(title: "Undo close or move", keys: "⌘Z"),
            ]),
            Group(title: "On a selected tile", items: [
                Item(title: "Allow, or type into it", keys: "⏎"),
                Item(title: "Always allow", keys: "A"),
                Item(title: "Deny with a message", keys: "N"),
                Item(title: "Move the selection", keys: "← → ↑ ↓"),
                Item(title: "Next / previous tile", keys: "⇥ ⇧⇥"),
                Item(title: "Previous / next band", keys: "⌥↑ ⌥↓"),
            ]),
            Group(title: "Walls and windows", items: [
                Item(title: "Layouts", keys: WallArrangement.shortcutRange),
                Item(title: "Next layout", keys: "⌥⌘L"),
                Item(title: "Project sidebar", keys: "⌘0"),
                Item(title: "Show the wall", keys: "⇧⌘0"),
                Item(title: "New wall window", keys: "⌥⌘N"),
                Item(title: "Wall scope", keys: "⌃⌘S"),
            ]),
            Group(title: "New agent", items: [
                Item(title: "Start", keys: "⌘↩"),
                Item(title: "Start and write the next one", keys: "⌥↩"),
                Item(title: "Worktree on / off", keys: "⇧⌘W"),
                Item(title: "Leave the draft", keys: "esc"),
            ]),
        ]
    }
}

/// Click, then press the new shortcut (esc cancels).
struct HotKeyRecorder: View {
    @Binding var spec: HotKeySpec
    @State private var recording = false
    @State private var monitor: Any?
    static let minWidth: CGFloat = 140

    var body: some View {
        Button {
            recording ? stop() : start()
        } label: {
            Text(recording ? "Press a shortcut…" : spec.label)
                .font(DS.monoFont(.chrome, .medium))
                .frame(minWidth: Self.minWidth)
        }
        .accessibilityIdentifier("settings.hotkey")
        .onDisappear { stop() }
    }

    private func start() {
        recording = true
        monitor = NSEvent.addLocalMonitorForEvents(matching: .keyDown) { e in
            if e.keyCode == 53 { stop(); return nil }
            if let s = HotKeySpec(event: e) { spec = s; stop(); return nil }
            return nil
        }
    }

    private func stop() {
        recording = false
        if let m = monitor { NSEvent.removeMonitor(m) }
        monitor = nil
    }
}
