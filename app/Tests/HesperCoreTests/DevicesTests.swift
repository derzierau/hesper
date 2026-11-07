import Foundation
import Testing
@testable import HesperCore

/// A devices.list answer as hesperd sends it (fixture ids and names only).
let devicesListJSON: JSONValue = ["devices": [
    ["id": "host-self", "name": "Laptop", "short": "L", "role": "host", "kind": "mac", "online": true, "self": true, "approvedHere": false, "trustedHere": false, "relay": true],
    ["id": "ctl-self", "name": "Laptop", "role": "controller", "kind": "mac", "online": true, "self": true, "approvedHere": false, "trustedHere": false, "relay": true],
    ["id": "host-b", "name": "Studio", "short": "S", "role": "host", "kind": "mac", "online": false, "self": false, "approvedHere": false, "trustedHere": true, "relay": true],
    ["id": "ctl-old", "name": "Old laptop", "role": "controller", "kind": "mac", "online": false, "self": false, "approvedHere": true, "trustedHere": false, "relay": true],
    ["id": "phone-a", "name": "Phone", "role": "controller", "kind": "iphone", "online": true, "self": false, "approvedHere": true, "trustedHere": false, "relay": true],
]]

@Suite struct Devices {
    @Test func decodesAList() throws {
        let list = try devicesListJSON.decode(DevicesList.self).devices
        #expect(list.count == 5)
        #expect(list[2] == Device(id: "host-b", name: "Studio", short: "S", role: Device.host, kind: Device.mac, online: false,
                                  isSelf: false, approvedHere: false, trustedHere: true, relay: true))
        #expect(list[1].short == nil && list[1].isSelf && list[1].role == Device.controller)
        #expect(list[4].isIPhone && !list[4].isHost)
    }

    @Test func groupsMacsSignInsAndPhones() throws {
        let list = try devicesListJSON.decode(DevicesList.self).devices
        // This Mac comes first even when it is listed later.
        let g = DeviceGroups(Array(list.reversed()))
        #expect(g.macs.map(\.id) == ["host-self", "host-b"])
        #expect(g.otherSignIns.map(\.id) == ["ctl-old"])
        #expect(g.iPhones.map(\.id) == ["phone-a"])
        #expect(!g.macs[0].removable)
        #expect(g.macs[1].removable)
    }

    @Test func outcomeAllDone() {
        let o = RemoveOutcome(relay: "done", approval: "skipped", trust: "done", names: "skipped")
        #expect(o.summary == "Removed")
    }

    @Test func outcomeFailedStep() {
        let r = RemoveOutcome(relay: "relay: not_found", approval: "done", trust: "write trusted-hosts.json: denied", names: "done")
        #expect(r.summary == "Not removed from the relay: relay: not_found")
        let t = RemoveOutcome(relay: "done", approval: "done", trust: "write trusted-hosts.json: denied", names: "done")
        #expect(t.summary == "Not removed from this Mac's trusted hosts: write trusted-hosts.json: denied")
    }

    @Test func outcomeSignedOut() throws {
        let o = try JSONValue.object(["relay": "done", "approval": "done", "trust": "done", "names": "done", "signedOut": ["ctl-old"]]).decode(RemoveOutcome.self)
        #expect(o.signedOut == ["ctl-old"])
        #expect(o.summary == "Removed, also signed out its controller record")
        let none = try JSONValue.object(["relay": "done", "approval": "done", "trust": "done", "names": "done"]).decode(RemoveOutcome.self)
        #expect(none.signedOut.isEmpty)
    }

    @Test func removeParams() {
        #expect(DevicesRPC.removeParams("phone-a", localOnly: false) == ["deviceId": "phone-a"])
        #expect(DevicesRPC.removeParams("phone-a", localOnly: true) == ["deviceId": "phone-a", "localOnly": true])
    }

    @Test func mapsErrors() {
        func rpc(_ code: Int, _ data: String) -> RPCError {
            RPCError(code: code, message: "m \(data)", kind: DaemonErrorCode(rawValue: data) ?? .unknown, data: ["code": .string(data)])
        }
        #expect(DevicesFailure(rpc(-32601, "not_found")) == .unsupportedDaemon)
        #expect(DevicesFailure(rpc(-32000, "unavailable")) == .relayUnreachable)
        #expect(DevicesFailure(rpc(-32000, "unsupported")) == .relayTooOld)
        #expect(DevicesFailure(rpc(-32000, "forbidden")) == .failed("m forbidden"))
        #expect(DevicesFailure(ConnectionError.notConnected) == .failed(ConnectionError.notConnected.description))
        #expect(DevicesFailure.relayUnreachable.text == "Not removed: the relay is unreachable. Try again later.")
    }
}
