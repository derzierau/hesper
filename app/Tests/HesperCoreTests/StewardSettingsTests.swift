import Foundation
import Testing
@testable import HesperCore

struct StewardSettingsTests {
    let machines = [Machine(short: "studio", name: "studio", online: false), Machine(short: "L", name: "L"), Machine(short: "mini", name: "mini")]

    @Test func offByDefault() {
        let s = StewardSettings(status: StewardStatus(), machines: machines, local: "L")
        #expect(!s.enabled && s.level == .suggest && s.summary == "Off" && s.runsHere && s.machine == "L")
        // This Mac first, then by name; offline ones stay choosable.
        #expect(s.machines.map(\.machine) == ["L", "mini", "studio"] && s.machines.last?.online == false)
        #expect(StewardSettings.levels.map(\.level) == [.watch, .suggest, .act])
        // Turning it on pins it to this Mac.
        #expect(s.setEnabled(true) == StewardPatch(enabled: true, machine: "L"))
        #expect(s.setEnabled(false) == nil && s.setLevel(.suggest) == nil && s.setLevel(.act) == StewardPatch(level: .act))
        #expect(s.setMachine("mini") == StewardPatch(machine: "mini") && s.setMachine("L") == nil && s.setMachine("nowhere") == nil)
    }

    @Test func summaries() {
        let here = StewardSettings(status: StewardStatus(enabled: true, machine: "L", agent: "L/st", queued: 2, wakes: 12), machines: machines, local: "L")
        #expect(here.summary == "Runs on this Mac · 12 wakes today · 2 waiting")
        let stopped = StewardSettings(status: StewardStatus(enabled: true, machine: "L", wakes: 1), machines: machines, local: "L")
        #expect(stopped.summary == "Runs on this Mac · 1 wake today · not running")
        let there = StewardSettings(status: StewardStatus(enabled: true, machine: "mini"), machines: machines, local: "L")
        #expect(there.summary == "Runs on mini" && !there.runsHere)
        #expect(there.setEnabled(true) == nil && there.setEnabled(false) == StewardPatch(enabled: false))
        // Stopped after failing: why, and that off and on again retries.
        var failing = StewardStatus(enabled: true, machine: "L", wakes: 3, dropped: 2, stopped: "it failed 5 times in 10 minutes")
        let broken = StewardSettings(status: failing, machines: machines, local: "L")
        #expect(broken.summary == "Runs on this Mac · 3 wakes today · stopped · 2 events dropped")
        #expect(broken.stoppedNote == "Stopped: it failed 5 times in 10 minutes. Turn it off and on again to retry.")
        failing.enabled = false
        #expect(StewardSettings(status: failing, machines: machines, local: "L").stoppedNote == nil)
        #expect(here.stoppedNote == nil)
        // A Mac the list doesn't know yet is still shown.
        let gone = StewardSettings(status: StewardStatus(enabled: true, machine: "old"), machines: [], local: "L")
        #expect(gone.machines.map(\.machine) == ["L", "old"])
    }
}
