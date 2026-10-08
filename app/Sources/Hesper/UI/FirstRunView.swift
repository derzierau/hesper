import AppKit
import HesperCore
import SwiftUI

/// The first run: shown once, for a fresh setup (no agents, no other Mac
/// paired; `FirstRunSteps.shouldShow`), in a small window of its own. Three
/// steps, each done by what the app already knows: pair this Mac (hesperd
/// is running and signed in), add another Mac (hesperctl pair-host /
/// approve; optional), start your first agent (the ⌘N composer). Hesper
/// never asks for credentials here: pairing stays in hesperctl.
/// `AppSettings.firstRunDone` records that it was shown (or skipped).
@MainActor
enum FirstRun {
    private static var window: NSWindow?
    private static var watching = false
    private static var closeObserver: NSObjectProtocol?
    /// How long after connecting the decision waits, so the first agent
    /// list has arrived (an existing setup must never see the first run).
    static let settle: TimeInterval = 2
    static let size = NSSize(width: 560, height: 470)

    /// At launch: once hesperd answers, shows the first run if this is a
    /// fresh setup; otherwise records that it's not needed.
    static func watch(model: AppModel) {
        guard !model.settings.firstRunDone, !watching else { return }
        watching = true
        track(model)
    }

    private static func track(_ model: AppModel) {
        let connected = withObservationTracking {
            model.isConnected && model.registry.hello != nil
        } onChange: {
            DispatchQueue.main.async { MainActor.assumeIsolated { if watching { track(model) } } }
        }
        guard connected, watching else { return }
        watching = false
        DispatchQueue.main.asyncAfter(deadline: .now() + settle) {
            MainActor.assumeIsolated { decide(model) }
        }
    }

    private static func decide(_ model: AppModel) {
        guard !model.settings.firstRunDone else { return }
        let others = model.machines.filter { $0.short != model.localMachine }.count
        if FirstRunSteps.shouldShow(alreadyShown: false, otherMachines: others, agents: model.registry.agents.count) {
            show(model: model)
        } else {
            model.settings.firstRunDone = true
        }
    }

    /// Opens (or brings forward) the first-run window; also Settings ›
    /// Advanced.
    static func show(model: AppModel) {
        if let window { window.makeKeyAndOrderFront(nil); return }
        let w = NSWindow(contentViewController: NSHostingController(rootView: FirstRunView(model: model, close: { close(model) })))
        w.styleMask = [.titled, .closable, .fullSizeContentView]
        w.titlebarAppearsTransparent = true
        w.titleVisibility = .hidden
        w.title = "Welcome to Hesper"
        w.isReleasedWhenClosed = false
        w.setContentSize(size)
        w.setAccessibilityIdentifier("firstRun")
        w.center()
        window = w
        closeObserver = NotificationCenter.default.addObserver(forName: NSWindow.willCloseNotification, object: w, queue: .main) { _ in
            MainActor.assumeIsolated { finished(model) }
        }
        w.makeKeyAndOrderFront(nil)
        NSApp.activate(ignoringOtherApps: true)
    }

    private static func close(_ model: AppModel) {
        window?.close() // → finished
    }

    private static func finished(_ model: AppModel) {
        model.settings.firstRunDone = true
        if let closeObserver { NotificationCenter.default.removeObserver(closeObserver) }
        closeObserver = nil
        window = nil
    }

    /// Step 3: the app's own ⌘N, on the wall.
    static func newAgent(_ model: AppModel) {
        (NSApp.delegate as? AppDelegate)?.showWindow()
        model.perform(.newAgent)
    }
}

struct FirstRunView: View {
    var model: AppModel
    var close: () -> Void

    private var steps: FirstRunSteps {
        FirstRunSteps(connected: model.isConnected && model.registry.hello != nil,
                      otherMachines: model.machines.filter { $0.short != model.localMachine }.count,
                      agents: model.registry.agents.count)
    }

    var body: some View {
        let s = steps
        VStack(alignment: .leading, spacing: DS.Spacing.xl) {
            VStack(alignment: .leading, spacing: DS.Spacing.s) {
                Wordmark(needsYou: false, size: CGFloat(DS.TextStyle.display.size))
                Text("Agents on every Mac, on one wall. Three steps to start.")
                    .font(DS.font(.body)).foregroundStyle(Theme.fg2)
            }
            VStack(alignment: .leading, spacing: DS.Spacing.l) {
                ForEach(FirstRunSteps.Step.allCases, id: \.rawValue) { step in
                    stepRow(step, done: s.isDone(step), current: s.current == step)
                }
            }
            Spacer(minLength: 0)
            HStack(spacing: DS.Spacing.m) {
                Text(s.current == nil ? "You're set." : "You can do this later from Settings › Advanced.")
                    .font(DS.font(.chrome)).foregroundStyle(Theme.dim)
                Spacer()
                Button(s.current == nil ? "Done" : "Skip for now", action: close)
                    .keyboardShortcut(s.current == nil ? .defaultAction : .cancelAction)
                    .accessibilityIdentifier("firstRun.close")
            }
        }
        .padding(.horizontal, DS.Spacing.xxl)
        .padding(.top, DS.chromeMaxHeight + DS.Spacing.l)
        .padding(.bottom, DS.Spacing.xl)
        .frame(width: FirstRun.size.width, height: FirstRun.size.height, alignment: .topLeading)
        .background(Theme.color(.background))
        .ignoresSafeArea()
    }

    private func stepRow(_ step: FirstRunSteps.Step, done: Bool, current: Bool) -> some View {
        HStack(alignment: .firstTextBaseline, spacing: DS.Spacing.m) {
            StateMark(done ? .done : current ? .working : .idle)
                .alignmentGuide(.firstTextBaseline) { $0[.bottom] }
            VStack(alignment: .leading, spacing: DS.Spacing.s) {
                HStack(spacing: DS.Spacing.m) {
                    Text("\(step.rawValue + 1). \(step.title)")
                        .font(DS.font(.panelTitle, .semibold))
                        .foregroundStyle(done || current ? Theme.fg : Theme.dim)
                    if done { Pill("done", variant: .status, mark: .done) }
                    if step == .addAnotherMac && !done { Pill("optional", variant: .count) }
                }
                detail(step, done: done)
            }
        }
        .accessibilityElement(children: .contain)
        .accessibilityLabel("Step \(step.rawValue + 1), \(step.title), \(done ? "done" : current ? "next" : "to do")")
        .accessibilityIdentifier("firstRun.step.\(step.rawValue)")
    }

    @ViewBuilder private func detail(_ step: FirstRunSteps.Step, done: Bool) -> some View {
        switch step {
        case .pairThisMac:
            if done {
                note("This Mac is \(model.machineName(model.localMachine)) (\(model.localMachine)); hesperd \(model.registry.hello?.version ?? "") is running.")
            } else {
                note("Run install.sh from the Hesper checkout: it starts hesperd and, once you have a relay (install.sh --relay URL), prints the hesperctl login command for your other Macs.")
                if let m = model.connectionMessage { note(m) }
            }
        case .addAnotherMac:
            if done {
                note("Paired: \(model.machines.filter { $0.short != model.localMachine }.map(\.displayName).joined(separator: ", ")).")
            } else {
                note("On the other Mac run this, then approve its code here with hesperctl approve CODE:")
                CommandText("hesperctl pair-host --machine \(model.localMachine)")
            }
        case .startFirstAgent:
            if done {
                note("It's on the wall.")
            } else {
                HStack(spacing: DS.Spacing.m) {
                    Button { FirstRun.newAgent(model) } label: { Pill("New agent", variant: .segment(selected: true), kbd: "⌘N") }
                        .buttonStyle(.plain)
                        .disabled(!model.isConnected)
                        .accessibilityLabel("New agent")
                        .accessibilityIdentifier("firstRun.newAgent")
                    note("Type the task; @machine, ~/folder and /tool pick where it runs.")
                }
            }
        }
    }

    private func note(_ s: String) -> some View {
        Text(s).font(DS.font(.chrome)).foregroundStyle(Theme.dim).fixedSize(horizontal: false, vertical: true)
    }
}
