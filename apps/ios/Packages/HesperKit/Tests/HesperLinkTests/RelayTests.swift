import Foundation
import Testing

@testable import HesperLink

@Suite struct RelayTests {
    @Test func requestEnvelopeCarriesParamsAsSent() throws {
        let params = Data(#"{"text":"<"}"#.utf8)
        let auth = RequestAuth(device: "d", ts: 1, nonce: "n", sig: "s")
        let frame = Envelope.request(id: "p1", machine: "m", method: "agents.input", params: params, auth: auth)
        let text = String(decoding: frame, as: UTF8.self)
        #expect(text.contains(#""params":{"text":"<"}"#))
        let o = jsonObject(frame)
        #expect(o["v"] as? Int == 2 && o["type"] as? String == "request" && o["machineId"] as? String == "m")
        #expect((o["auth"] as? [String: Any])?["device"] as? String == "d")
    }

    @Test func envelopeParsesResultsErrorsAndInventory() throws {
        let r = try Envelope(parsing: Data(#"{"v":2,"type":"result","id":"p1","result":{"a":[1]}}"#.utf8))
        #expect(r.id == "p1" && jsonObject(r.result!)["a"] as? [Int] == [1])
        let e = try Envelope(parsing: Data(#"{"v":2,"type":"result","id":"p2","error":{"code":"forbidden","message":"no"}}"#.utf8))
        #expect(e.error == RelayError(code: "forbidden", message: "no"))
        let m = try Envelope(
            parsing: Data(
                #"{"v":2,"type":"machines","machines":[{"id":"X","name":"Mac","online":true,"snapshot":{"short":"M","e2eKey":"AAAA","capabilities":{"e2e":true}}}]}"#
                    .utf8))
        #expect(
            m.machines == [
                RelayMachine(id: "X", name: "Mac", online: true, short: "M", e2eKey: Data(count: 3), capabilities: ["e2e": true])
            ])
        #expect(throws: RelayError.self) { try Envelope(parsing: Data(#"{"v":1,"type":"result"}"#.utf8)) }
    }

    @Test func linkFramesReassemble() throws {
        let a = LinkFrames.encode(kind: UInt8(ascii: "E"), channel: 0, payload: Data("{}".utf8))
        let b = LinkFrames.encode(kind: UInt8(ascii: "D"), channel: 7, payload: Data([1, 2, 3]))
        var frames = LinkFrames()
        let bytes = a + b
        #expect(try frames.append(bytes.prefix(4)).isEmpty)
        let got = try frames.append(bytes.dropFirst(4))
        #expect(got.map(\.kind) == [UInt8(ascii: "E"), UInt8(ascii: "D")])
        #expect(got[1].channel == 7 && got[1].payload == Data([1, 2, 3]))
        var bad = LinkFrames()
        #expect(throws: RelayError.self) { try bad.append(Data([0x45, 0, 0, 0, 0, 0x7f, 0xff, 0xff, 0xff])) }
    }
}
