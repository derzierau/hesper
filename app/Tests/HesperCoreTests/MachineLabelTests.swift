import Testing
@testable import HesperCore

struct MachineLabelTests {
    @Test func shortNameWinsOverHostNames() {
        // machines.json shorts (what hesperctl machines lists), device names from the relay.
        #expect(MachineLabel.name(Machine(short: "laptop", name: "ABC123456")) == "laptop")
        #expect(MachineLabel.name(Machine(short: "mini", name: "XYZ987654 Mac mini")) == "mini")
        #expect(MachineLabel.name(Machine(short: "mbp", name: "My-MacBook-Pro.local")) == "mbp")
    }

    @Test func fallsBackWithoutARealShortName() {
        // A one-letter legacy short: the device's chosen name.
        #expect(MachineLabel.name(Machine(short: "M", name: "mini")) == "mini")
        // …unless that is a host name.
        #expect(MachineLabel.name(Machine(short: "M", name: "Oles-Mac-mini.local")) == "M")
        #expect(MachineLabel.name(short: "", name: "ABC123456") == "ABC123456")
    }

    @Test func lookupsAndTables() {
        let ms = [Machine(short: "laptop", name: "ABC123456"), Machine(short: "M", name: "mini")]
        #expect(MachineLabel.name(short: "laptop", in: ms) == "laptop")
        #expect(MachineLabel.name(short: "M", in: ms) == "mini")
        #expect(MachineLabel.name(short: "studio", in: ms) == "studio")
        #expect(MachineLabel.names(ms) == ["laptop": "laptop", "M": "mini"])
        #expect(ms[0].displayName == "laptop")
    }
}
