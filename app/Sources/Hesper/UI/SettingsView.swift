import AppKit
import HesperCore
import SwiftUI

/// ⌘,: one sidebar window. General (the wall, quick launch,
/// notifications), Machines & pairing, Agents & profiles, Appearance
/// (Dusk / Daylight / System, density), Shortcuts (rebinding) and Advanced
/// (desks, the first run, hesperd), Steward (the loops' background
/// agent). The sidebar is Rows, choices are segmented Pills; ⌘1…⌘7
/// switch panes.
struct SettingsView: View {
    @Bindable var model: AppModel
    var onHotKeyChanged: () -> Void = {}
    /// Desks (the window layer's DeskController).
    var desks: DeskController? = nil
    /// The wall in front (window layer): Wall › Layout and the minimum
    /// width are that wall's own (nil: `model`'s).
    var front: FrontWall? = nil
    @State private var pane = Pane.general
    @State private var symbolicHotKeys: [String: Any]?
    /// Shortcuts: why a recorded chord was refused, per row, until the
    /// next recording or reset.
    @State private var refusals: [CommandID: String] = [:]

    /// The wall whose layout the Wall section edits.
    private var wall: AppModel { front?.model ?? model }
    @State private var deskRefresh = 0
    /// Steward: the last steward.set's error, and one in flight.
    @State private var stewardError: String?
    @State private var stewardBusy = false

    enum Pane: String, CaseIterable, Identifiable {
        case general, machines, agents, appearance, shortcuts, advanced, steward
        var id: String { rawValue }
        var title: String {
            switch self {
            case .general: return "General"
            case .machines: return "Machines & pairing"
            case .agents: return "Agents & profiles"
            case .appearance: return "Appearance"
            case .shortcuts: return "Shortcuts"
            case .advanced: return "Advanced"
            case .steward: return "Steward"
            }
        }
    }

    static let size = NSSize(width: 960, height: 640)
    /// Posted to open a pane (userInfo "pane"): the shortcuts sheet's footer.
    static let showPane = Notification.Name("hesper.settings.showPane")
    static let sidebarWidth: CGFloat = 224
    static let labelWidth: CGFloat = 168
    static let sliderWidth: CGFloat = 180

    var body: some View {
        HStack(spacing: 0) {
            sidebar
            Rectangle().fill(Theme.stroke).frame(width: 1)
            GeometryReader { area in
                ScrollView {
                    VStack(alignment: .leading, spacing: DS.Spacing.xl) {
                        Text(pane.title).font(DS.font(.sheetTitle, .semibold)).foregroundStyle(Theme.fg)
                        content
                    }
                    .padding(.horizontal, DS.Spacing.xxl)
                    .padding(.top, DS.chromeMaxHeight + DS.Spacing.l)
                    .padding(.bottom, DS.Spacing.xxl)
                    .frame(width: area.size.width, alignment: .leading)
                }
            }
            .frame(minWidth: 0, maxWidth: .infinity)
            .accessibilityIdentifier("settings.\(pane.rawValue)")
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background(Theme.color(.background))
        .font(DS.font(.body))
        .foregroundStyle(Theme.fg)
        .ignoresSafeArea()
        .onAppear { symbolicHotKeys = Self.readSymbolicHotKeys() }
        .onReceive(NotificationCenter.default.publisher(for: Self.showPane)) { n in
            if let p = n.userInfo?["pane"] as? Pane { pane = p }
        }
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
        case .steward: steward
        }
    }

    // MARK: General

    @ViewBuilder private var general: some View {
        SettingsSection("Wall", footer: "Each wall keeps its own layout; this is the frontmost wall's (also ⌥⌘L for the next layout, and the layout button in the toolbar). Every tile uses this font size and shows as much of its agent as fits (the focused agent and agent windows keep your Ghostty font size); cards never get narrower than the minimum width, the wall adds rows or scrolls instead.") {
            SettingsLine("Layout") {
                Segments(LayoutSwitcher(wall.arrangement).rows.map { r in (r.arrangement, r.title, r.shortcut) }, selection: Bindable(wall).arrangement)
                    .accessibilityIdentifier("settings.layout")
            }
            SettingsLine("Minimum card width") {
                Segments([(60, "60 · dense", nil), (80, "80", nil), (100, "100 characters", nil)], selection: Bindable(wall).minChars)
                    .accessibilityIdentifier("settings.minChars")
            }
            SettingsLine("Font size") {
                HStack(spacing: DS.Spacing.m) {
                    Slider(value: Bindable(model.settings).fontSize, in: TerminalFont.range, step: TerminalFont.step)
                        .frame(width: Self.sliderWidth)
                        .accessibilityIdentifier("settings.tileFont")
                    Text(String(format: "%.1f pt", model.settings.fontSize)).font(DS.font(.meta)).foregroundStyle(Theme.dim).monospacedDigit()
                }
            }
        }
        SettingsSection("Quick launch", footer: "Opens the composer as a small floating panel over any app; ⌘↩ starts the agent and a notification takes you to it. Also in the menu bar item.") {
            SettingsLine("From anywhere") {
                Toggle("Quick launch with a global hotkey", isOn: Binding(get: { model.settings.quickLaunchEnabled },
                                                                          set: { model.settings.quickLaunchEnabled = $0; onHotKeyChanged() }))
            }
            SettingsLine("Hotkey") {
                VStack(alignment: .leading, spacing: DS.Spacing.s) {
                    HStack(spacing: DS.Spacing.m) {
                        HotKeyRecorder(spec: Binding(get: { model.settings.hotKey }, set: { model.settings.hotKey = $0; onHotKeyChanged() }))
                        Button("Reset to ⌃⌥Space") { model.settings.hotKey = .default; onHotKeyChanged() }
                            .disabled(model.settings.hotKey == .default)
                    }
                    .disabled(!model.settings.quickLaunchEnabled)
                    if model.settings.quickLaunchEnabled, let warning = hotKeyVerdict.warning {
                        HStack(alignment: .firstTextBaseline, spacing: DS.Spacing.s) {
                            StateMark(.error)
                            Text(warning).font(DS.font(.chrome)).foregroundStyle(Theme.dim)
                                .fixedSize(horizontal: false, vertical: true)
                        }
                        .accessibilityIdentifier("settings.hotkey.warning")
                    }
                }
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
        // hesperd's scratch settings; hidden when it can't say them.
        if let s = model.lists.scratchBook.settings {
            SettingsSection("Scratch projects", footer: "⌘N without a folder starts the agent in a new scratch: ~/scratch/<date>-<task> on that Mac, a git repo of its own. Kept scratches never archive; archived ones move to ~/scratch/.archive, and their sessions stay in History after the folder is deleted.") {
                SettingsLine("Archive scratches after") {
                    Stepper(value: Binding(get: { s.archiveAfterDays }, set: { model.setScratchSetting(ScratchSettings.archiveKey, days: $0) }),
                            in: ScratchSettings.range) {
                        Text("\(s.archiveAfterDays) days").font(DS.font(.body)).monospacedDigit()
                    }
                    .accessibilityLabel("Archive scratches after \(s.archiveAfterDays) quiet days")
                    .accessibilityIdentifier("settings.scratchArchiveDays")
                }
                SettingsLine("Delete archived after") {
                    Stepper(value: Binding(get: { s.deleteAfterDays }, set: { model.setScratchSetting(ScratchSettings.deleteKey, days: $0) }),
                            in: ScratchSettings.range) {
                        Text("\(s.deleteAfterDays) days").font(DS.font(.body)).monospacedDigit()
                    }
                    .accessibilityLabel("Delete archived scratches after \(s.deleteAfterDays) days")
                    .accessibilityIdentifier("settings.scratchDeleteDays")
                }
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

    /// What macOS does with the quick launch hotkey (Decision 4: the
    /// input source chords from com.apple.symbolichotkeys; unreadable:
    /// the static warning on macOS's defaults).
    private var hotKeyVerdict: InputSourceHotKeys.Verdict {
        let k = model.settings.hotKey
        return InputSourceHotKeys.check(SystemHotKey(keyCode: k.keyCode, carbonModifiers: k.carbonModifiers), in: symbolicHotKeys)
    }

    /// The system's AppleSymbolicHotKeys, read once when the window shows.
    static func readSymbolicHotKeys() -> [String: Any]? {
        UserDefaults(suiteName: "com.apple.symbolichotkeys")?.dictionary(forKey: "AppleSymbolicHotKeys")
    }

    /// The sheet's sections (HesperCore `ShortcutsSheet`, every section
    /// open, the stored overrides in); the quick launch row shows the
    /// configured hotkey and, like any row whose chord is bound twice in
    /// one surface, a "Conflicts with …" pill. A rebindable row's chord
    /// is a recorder (click, press the new chord; esc cancels, ⌫
    /// unbinds), with Reset once it is overridden; a refused chord says
    /// why under the row. Reset All sits above the sections.
    @ViewBuilder private var shortcuts: some View {
        let overrides = model.settings.shortcutOverrides
        let sections = ShortcutsSheet.sections(current: .everywhere, hotKey: model.settings.quickLaunchEnabled ? model.settings.hotKey.label : nil,
                                               hotKeyConflict: hotKeyVerdict.conflict, overrides: overrides, scanConflicts: !overrides.isEmpty)
        HStack(spacing: DS.Spacing.m) {
            Text("Click a shortcut to record a new one; esc cancels, ⌫ removes it.").font(DS.font(.chrome)).foregroundStyle(Theme.dim)
            Spacer(minLength: DS.Spacing.m)
            Button("Reset All") { refusals = [:]; model.settings.shortcutOverrides.resetAll() }
                .disabled(overrides.isEmpty)
                .accessibilityIdentifier("settings.shortcuts.resetAll")
        }
        ForEach(sections, id: \.surface) { g in
            SettingsSection(g.title) {
                VStack(spacing: 0) {
                    ForEach(g.rows, id: \.id) { r in
                        VStack(alignment: .leading, spacing: DS.Spacing.xxs) {
                            HStack(spacing: DS.Spacing.s) {
                                Row(title: r.title, kbd: r.rebindable ? (r.aliases.isEmpty ? nil : r.aliases.joined(separator: " ")) : r.chord)
                                if r.rebindable {
                                    ShortcutRecorder(title: r.title, chord: r.primary, identifier: "settings.shortcut.record.\(r.id.key)") { binding in
                                        rebind(r.id, to: binding)
                                    }
                                    if r.overridden {
                                        IconButton("arrow.uturn.backward", help: Self.resetHelp(r.id)) { refusals[r.id] = nil; model.settings.shortcutOverrides.reset(r.id) }
                                            .accessibilityIdentifier("settings.shortcut.reset.\(r.id.key)")
                                    }
                                }
                                if !r.conflicts.isEmpty {
                                    Pill("Conflicts with \(r.conflicts.joined(separator: ", "))", mark: .error)
                                        .accessibilityIdentifier("settings.shortcut.conflict.\(r.id.key)")
                                }
                            }
                            if let reason = refusals[r.id] {
                                Text(reason).font(DS.font(.chrome)).foregroundStyle(Theme.color(.error))
                                    .padding(.leading, DS.Spacing.m)
                                    .accessibilityIdentifier("settings.shortcut.reason.\(r.id.key)")
                            }
                        }
                        .accessibilityIdentifier("settings.shortcut.\(r.id.key)")
                    }
                }
            }
        }
    }

    /// Stores the binding, or shows the registry's reason under the row;
    /// says whether it was stored.
    private func rebind(_ id: CommandID, to binding: ShortcutOverrides.Binding) -> Bool {
        switch model.settings.shortcutOverrides.rebinding(id, to: binding) {
        case .applied(let o): refusals[id] = nil; model.settings.shortcutOverrides = o; return true
        case .refused(let reason): refusals[id] = reason; return false
        }
    }

    static func resetHelp(_ id: CommandID) -> String {
        CommandRegistry.defaults.command(id)?.chord.map { "Reset to \($0.label)" } ?? "Reset (no shortcut)"
    }

    // MARK: Steward

    /// What each level lets the Steward do, from the least (the trust
    /// ladder: watch, suggest, act).
    static let stewardLadder: [StewardLevel: String] = [
        .watch: "Reads what agents did and keeps each loop's next step, loose ends and links current. It changes nothing else.",
        .suggest: "Also drafts the next agent for a loop (you start it with ⌘↩) and proposes milestones. The default.",
        .act: "Also starts drafts that are ready and merges or splits loops. Each change is audited.",
    ]

    /// What it never does, at any level: the person-only calls.
    static let stewardNever = [
        "mark a loop or a milestone done, or drop one",
        "accept a plan (⌘↩ in the planning view is yours) or remove a goal",
        "change its own settings or the loop settings",
        "answer an agent's approval or question for you",
        "push, open a pull request or skip permissions",
    ]

    @ViewBuilder private var steward: some View {
        let hub = model.loopHub
        let st = StewardSettings(status: hub.steward, machines: model.machines, local: model.localMachine)
        if !hub.available {
            SettingsSection("Steward") {
                Text("This hesperd has no loops yet: update it on this Mac to use the Steward.").font(DS.font(.chrome)).foregroundStyle(Theme.dim)
            }
        } else {
            SettingsSection("Steward", footer: "A background Claude agent that keeps your loops up to date: it wakes when an agent settles or closes, at most once a minute and \(hub.settings.maxWakesPerDay) times a day, and costs tokens while it runs. One Mac runs it for all of them.") {
                SettingsLine("Steward") {
                    VStack(alignment: .leading, spacing: DS.Spacing.xs) {
                        Toggle("Keep my loops up to date", isOn: Binding(get: { st.enabled }, set: { on in setSteward(st.setEnabled(on)) }))
                            .disabled(stewardBusy)
                            .accessibilityIdentifier("settings.steward.enabled")
                        Text(st.summary).font(DS.font(.chrome)).foregroundStyle(Theme.dim)
                            .accessibilityIdentifier("settings.steward.summary")
                    }
                }
                SettingsLine("Level") {
                    VStack(alignment: .leading, spacing: DS.Spacing.s) {
                        Segments(StewardSettings.levels.map { ($0.level, $0.title, nil as String?) },
                                 selection: Binding(get: { st.level }, set: { l in setSteward(st.setLevel(l)) }))
                            .accessibilityIdentifier("settings.steward.level")
                        Text(Self.stewardLadder[st.level] ?? "").font(DS.font(.chrome)).foregroundStyle(Theme.dim)
                            .fixedSize(horizontal: false, vertical: true)
                    }
                }
                SettingsLine("Runs on") {
                    Segments(st.machines.map { ($0.machine, $0.online ? $0.title : "\($0.title) (offline)", nil as String?) },
                             selection: Binding(get: { st.machine }, set: { m in setSteward(st.setMachine(m)) }))
                        .accessibilityIdentifier("settings.steward.machine")
                }
                if let e = stewardError {
                    HStack(alignment: .firstTextBaseline, spacing: DS.Spacing.s) {
                        StateMark(.error)
                        Text(e).font(DS.font(.chrome)).foregroundStyle(Theme.dim).fixedSize(horizontal: false, vertical: true)
                    }
                    .accessibilityIdentifier("settings.steward.error")
                }
            }
            SettingsSection("It never", footer: "These stay yours at every level; hesperd refuses them from any agent.") {
                VStack(alignment: .leading, spacing: DS.Spacing.xs) {
                    ForEach(Self.stewardNever, id: \.self) { t in
                        Text("· \(t)").font(DS.font(.chrome)).foregroundStyle(Theme.fg2)
                    }
                }
                .accessibilityIdentifier("settings.steward.never")
            }
            SettingsSection("From a terminal") {
                CommandText("hesperctl steward on")
                CommandText("hesperctl steward level suggest")
            }
        }
    }

    private func setSteward(_ patch: StewardPatch?) {
        guard let patch else { return }
        stewardBusy = true
        stewardError = nil
        let hub = model.loopHub
        Task {
            stewardError = await hub.stewardSet(patch)
            stewardBusy = false
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
        ViewThatFits(in: .horizontal) {
            HStack(alignment: .firstTextBaseline, spacing: DS.Spacing.l) {
                labelView.frame(width: SettingsView.labelWidth, alignment: .leading)
                control.frame(maxWidth: .infinity, alignment: .leading)
            }
            VStack(alignment: .leading, spacing: DS.Spacing.s) {
                labelView
                control.frame(maxWidth: .infinity, alignment: .leading)
            }
        }
    }

    private var labelView: some View {
        Text(label).font(DS.font(.body)).foregroundStyle(Theme.fg2)
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
        ViewThatFits(in: .horizontal) {
            HStack(spacing: DS.Spacing.xxs) { choices }
            VStack(alignment: .leading, spacing: DS.Spacing.xxs) { choices }
        }
        .padding(DS.Spacing.xxs)
        .overlay(DS.Radius.shape(DS.Radius.control + DS.Spacing.xxs).strokeBorder(Theme.stroke))
    }

    private var choices: some View {
        ForEach(Array(options.enumerated()), id: \.offset) { _, o in
            Button { selection = o.value } label: { Pill(o.title, variant: .segment(selected: o.value == selection), kbd: o.kbd) }
                .buttonStyle(.plain)
                .accessibilityLabel(o.title)
                .accessibilityAddTraits(o.value == selection ? .isSelected : [])
        }
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

/// Click, then press the new shortcut (esc cancels): the recorder behind
/// the quick launch hotkey and the per-command shortcuts. `take` sees a
/// key event while recording and says whether it took it: recording
/// stops on a chord it took, and keeps listening after one it refused
/// (the caller shows why). The key monitor exists only while recording and
/// lets every event through once it stopped, so ⌘Q and ⌘W reach the
/// menu bar at every other time.
struct KeyRecorder: View {
    var label: String
    var accessibilityLabel: String
    var identifier: String
    var take: (NSEvent) -> Bool
    @State private var recording = false
    @State private var monitor: Any?
    static let minWidth: CGFloat = 140
    /// Stops the recorder that is recording: one at a time, or two
    /// monitors would race for the next key.
    @MainActor private static var stopActive: (() -> Void)?

    var body: some View {
        Button {
            recording ? stop() : start()
        } label: {
            Text(recording ? "Press a shortcut…" : label)
                .font(DS.monoFont(.chrome, .medium))
                .frame(minWidth: Self.minWidth)
        }
        .accessibilityLabel(recording ? "Press a shortcut" : "\(accessibilityLabel): \(label)")
        .accessibilityIdentifier(identifier)
        .onDisappear { stop() }
    }

    private func start() {
        Self.stopActive?()
        Self.stopActive = { stop() }
        recording = true
        monitor = NSEvent.addLocalMonitorForEvents(matching: .keyDown) { e in
            guard recording else { return e }
            if e.keyCode == 53 { stop(); return nil }
            if take(e) { stop() }
            return nil
        }
    }

    private func stop() {
        Self.stopActive = nil
        recording = false
        if let m = monitor { NSEvent.removeMonitor(m) }
        monitor = nil
    }
}

/// The quick launch hotkey (a global hotkey needs ⌘, ⌥ or ⌃): a key
/// without one says so under the recorder, which keeps listening.
struct HotKeyRecorder: View {
    @Binding var spec: HotKeySpec
    @State private var refusal: String?
    static let needsModifier = "A hotkey needs ⌘, ⌥ or ⌃"

    var body: some View {
        VStack(alignment: .leading, spacing: DS.Spacing.xxs) {
            KeyRecorder(label: spec.label, accessibilityLabel: "Quick launch hotkey", identifier: "settings.hotkey") { e in
                guard let s = HotKeySpec(event: e) else { refusal = Self.needsModifier; return false }
                refusal = nil
                spec = s
                return true
            }
            if let refusal {
                Text(refusal).font(DS.font(.chrome)).foregroundStyle(Theme.color(.error))
                    .accessibilityIdentifier("settings.hotkey.reason")
            }
        }
    }
}

/// A command's shortcut in Settings › Shortcuts: ⌫ unbinds, any other
/// key is the new chord (the registry says whether it may be; a refused
/// one keeps the recorder listening).
struct ShortcutRecorder: View {
    var title: String
    /// The current chord's label; nil: none.
    var chord: String?
    var identifier: String
    /// Whether the binding was stored.
    var onRecord: (ShortcutOverrides.Binding) -> Bool

    var body: some View {
        KeyRecorder(label: chord ?? "None", accessibilityLabel: "Shortcut for \(title)", identifier: identifier) { e in
            let k = KeyChord(event: e)
            return onRecord(k.key == .delete && k.plain && !k.shift ? .unbound : .chord(k))
        }
    }
}
