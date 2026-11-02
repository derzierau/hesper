import CryptoKit
import Foundation
import Testing

@testable import HesperLink

// Golden vectors from relay/pkg/e2e and github.com/flynn/noise v1.1.0:
// fixed static and ephemeral keys (bytes counting up from 0x01, 0x41, 0x81
// and 0xc1), message 1 and 2 of the handshake, then frames each way.
private enum Vector {
    static let controller = key("0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20")
    static let host = key("4142434445464748494a4b4c4d4e4f505152535455565758595a5b5c5d5e5f60")
    static let controllerEphemeral = key("8182838485868788898a8b8c8d8e8f909192939495969798999a9b9c9d9e9fa0")
    static let hostEphemeral = key("c1c2c3c4c5c6c7c8c9cacbcccdcecfd0d1d2d3d4d5d6d7d8d9dadbdcdddedfe0")
    static let controllerPublic = Data(hex: "07a37cbc142093c8b755dc1b10e86cb426374ad16aa853ed0bdfc0b2b86d1c7c")!
    static let hostPublic = Data(hex: "64b101b1d0be5a8704bd078f9895001fc03e8e9f9522f188dd128d9846d48466")!
    static let prologue = E2E.prologue(machine: "machine-1", controller: "controller-1")
    static let payload1 = Data(#"{"v":1,"ts":1700000000000}"#.utf8)
    static let payload2 = Data(#"{"v":1,"session":"00112233445566778899aabbccddeeff"}"#.utf8)
    static let session = "00112233445566778899aabbccddeeff"
    static let message1 = Data(
        hex: "883186b800b41d5cf0429695da9b3cc4f328ebcd184a6e482fa578c103f06c777e515e2649e7bc3053b2ad63f8f50b"
            + "185b3f445b2075edc5928e529b876fcf976004963116e80d00c0080eb08df6d901ea197300474078f3fa124cbedaa"
            + "a08aafe938cbcb35103b91bf27856995bd464fefb4bf5292d44f56f11")!
    static let message2 = Data(
        hex: "3a553d74792d727efa9b9a4cde3da1ad93f1a2d0c09cb639b1a3c0fda14cbe2401ea9c40eb1295866b306c58c3a95dfe"
            + "103edbff64413a1e207cded110bbc550a7d0133a464b29dd00ecbde45be3837e7eeac79ff9fbf66b5f62bf4473202b9bae8e30d3")!
    /// Controller to host: counter, plaintext, ciphertext.
    static let toHost: [(UInt64, String, String)] = [
        (0, #"{"id":"r0","method":"ping"}"#, "a94ce0b3206772a8498c34894f49e308a7a29c216c346b82edf731b37c8773cecd25cba20ce05873ff2a52"),
        (1, #"{"id":"r1","method":"ping"}"#, "6cc19775e69f731b45a672f3b0968f938b93bf3c1b1df262d5783b08213b7328e0a925c8a428851e914196"),
        (7, #"{"id":"r7","method":"ping"}"#, "80494baf5293939f6ee77bd54b0da37b64b2b42e02e9e7cf3ddd7284bfd3074ea65d00fa380516e726de5a"),
    ]
    /// Host to controller.
    static let toController: [(UInt64, String, String)] = [
        (0, #"{"id":"r0","result":{}}"#, "2bcbd8d163780f4a997f7800ee2da9baa0df728d9b9d5cbf5a4ffe7474b4c6c7aab5d1c6f927b3"),
        (3, #"{"id":"r3","result":{}}"#, "2647871efc522c9120b302c4d7e4cdf6594221bf3fd27bdf7aefaf0f2b22638bf3dc5ca7078cae"),
    ]

    static func key(_ hex: String) -> Curve25519.KeyAgreement.PrivateKey {
        try! .init(rawRepresentation: Data(hex: hex)!)
    }
}

@Suite struct BLAKE2sTests {
    @Test func rfc7693() {
        #expect(BLAKE2s.hash(Data("abc".utf8)).hex == "508c5e8c327c14e2e1a72ba34eeb452f37458b209ed63a294d999b4c86675982")
        #expect(BLAKE2s.hash(Data()).hex == "69217a3079908094e11121d042354a7c1f55b6482ca1a51e1b250dfd1ed0eef9")
    }

    @Test func keyedKAT() {
        // blake2s-kat.txt: key 00…1f, input 00…(n-1).
        let key = Data(0..<32)
        let input = Data(0..<255)
        #expect(BLAKE2s.hash(Data(), key: key).hex == "48a8997da407876b3d79c0d92325ad3b89cbb754d86ab71aee047ad345fd2c49")
        #expect(BLAKE2s.hash(input.prefix(1), key: key).hex == "40d15fee7c328830166ac3f918650f807e7e01e177258cdc0a39b11f598066f1")
        #expect(BLAKE2s.hash(input.prefix(64), key: key).hex == "8975b0577fd35566d750b362b0897a26c399136df07bababbde6203ff2954ed4")
        #expect(BLAKE2s.hash(input.prefix(65), key: key).hex == "21fe0ceb0052be7fb0f004187cacd7de67fa6eb0938d927677f2398c132317a8")
        #expect(BLAKE2s.hash(input, key: key).hex == "3fb735061abc519dfe979e54c1ee5bfad0a9d858b3315bad34bde999efd724dd")
    }

    @Test func multiBlockAndLength() {
        #expect(BLAKE2s.hash(Data(0..<128)).hex == "1fa877de67259d19863a2a34bcc6962a2b25fcbf5cbecd7ede8f1fa36688a796")
        #expect(BLAKE2s.hash(Data("abc".utf8), length: 16).count == 16)
    }
}

@Suite struct NoiseTests {
    @Test func publicKeysMatchGo() {
        #expect(Vector.controller.publicKey.rawRepresentation == Vector.controllerPublic)
        #expect(Vector.host.publicKey.rawRepresentation == Vector.hostPublic)
    }

    @Test func initiatorMatchesGo() throws {
        var initiator = try NoiseInitiator(
            static: Vector.controller, remoteStatic: Vector.hostPublic, prologue: Vector.prologue, ephemeral: Vector.controllerEphemeral)
        #expect(try initiator.writeMessage(payload: Vector.payload1) == Vector.message1)
        let (payload, send, receive) = try initiator.readMessage(Vector.message2)
        #expect(payload == Vector.payload2)

        let session = E2ESession(id: Vector.session, send: send, receive: receive, peer: Vector.hostPublic)
        for (n, plain, cipher) in Vector.toHost {
            #expect(send.seal(Data(plain.utf8), nonce: n, ad: Data("ghosty-e2e-v1\0\(Vector.session)".utf8)).hex == cipher)
        }
        let (n0, c0) = session.seal(Data(Vector.toHost[0].1.utf8))
        #expect(n0 == 0 && c0.hex == Vector.toHost[0].2)
        #expect(session.sent == 1)
        for (n, plain, cipher) in Vector.toController {
            #expect(try session.open(counter: n, ciphertext: Data(hex: cipher)!) == Data(plain.utf8))
        }
    }

    @Test func responderMatchesGo() throws {
        var responder = NoiseResponder(static: Vector.host, prologue: Vector.prologue, ephemeral: Vector.hostEphemeral)
        #expect(try responder.readMessage(Vector.message1) == Vector.payload1)
        #expect(responder.remoteStatic == Vector.controllerPublic)
        let (message, send, receive) = try responder.writeMessage(payload: Vector.payload2)
        #expect(message == Vector.message2)

        let session = E2ESession(id: Vector.session, send: send, receive: receive, peer: Vector.controllerPublic)
        for (n, plain, cipher) in Vector.toHost {
            #expect(try session.open(counter: n, ciphertext: Data(hex: cipher)!) == Data(plain.utf8))
        }
        let (n0, c0) = session.seal(Data(Vector.toController[0].1.utf8))
        #expect(n0 == 0 && c0.hex == Vector.toController[0].2)
    }

    @Test func roundTrip() throws {
        let host = Curve25519.KeyAgreement.PrivateKey()
        let controller = Curve25519.KeyAgreement.PrivateKey()
        let prologue = E2E.prologue(machine: "m", controller: "c")
        var initiator = try NoiseInitiator(static: controller, remoteStatic: host.publicKey.rawRepresentation, prologue: prologue)
        var responder = NoiseResponder(static: host, prologue: prologue)
        #expect(try responder.readMessage(initiator.writeMessage(payload: Data("hi".utf8))) == Data("hi".utf8))
        #expect(responder.remoteStatic == controller.publicKey.rawRepresentation)
        let (message, hostSend, hostReceive) = try responder.writeMessage(payload: Data("ok".utf8))
        let (payload, send, receive) = try initiator.readMessage(message)
        #expect(payload == Data("ok".utf8))

        let app = E2ESession(id: "s", send: send, receive: receive, peer: host.publicKey.rawRepresentation)
        let mac = E2ESession(id: "s", send: hostSend, receive: hostReceive, peer: controller.publicKey.rawRepresentation)
        let (n, c) = app.seal(Data("ping".utf8))
        #expect(try mac.open(counter: n, ciphertext: c) == Data("ping".utf8))
        let (m, d) = mac.seal(Data("pong".utf8))
        #expect(try app.open(counter: m, ciphertext: d) == Data("pong".utf8))
        #expect(throws: E2EError.authentication) { try app.open(counter: 1, ciphertext: d) }
        #expect(throws: NoiseError.outOfOrder) { try initiator.readMessage(message) }
    }

    @Test func wrongPinnedHostKeyFails() throws {
        let host = Curve25519.KeyAgreement.PrivateKey()
        let impostor = Curve25519.KeyAgreement.PrivateKey()
        let prologue = E2E.prologue(machine: "m", controller: "c")
        var initiator = try NoiseInitiator(
            static: .init(), remoteStatic: host.publicKey.rawRepresentation, prologue: prologue)
        let first = try initiator.writeMessage(payload: Data())
        // The impostor cannot read the first message, and its answer to a
        // message it made up does not verify against the pinned key.
        var responder = NoiseResponder(static: impostor, prologue: prologue)
        #expect(throws: NoiseError.decryptFailed) { try responder.readMessage(first) }
        var other = try NoiseInitiator(static: .init(), remoteStatic: impostor.publicKey.rawRepresentation, prologue: prologue)
        _ = try responder.readMessage(other.writeMessage(payload: Data()))
        let (answer, _, _) = try responder.writeMessage(payload: Data())
        #expect(throws: NoiseError.decryptFailed) { try initiator.readMessage(answer) }
        #expect(throws: NoiseError.invalidKey) {
            try NoiseInitiator(static: .init(), remoteStatic: Data(count: 31), prologue: prologue)
        }
    }
}

@Suite struct E2ESessionTests {
    private func pair() -> (E2ESession, E2ESession) {
        let a = NoiseCipher(key: SymmetricKey(size: .bits256))
        let b = NoiseCipher(key: SymmetricKey(size: .bits256))
        return (E2ESession(id: "s", send: a, receive: b, peer: Data()), E2ESession(id: "s", send: b, receive: a, peer: Data()))
    }

    @Test func replayWindow() throws {
        let (sender, receiver) = pair()
        let frames = (0..<1100).map { _ in sender.seal(Data("x".utf8)) }
        // Out of order within the window: each accepted once.
        _ = try receiver.open(counter: frames[5].counter, ciphertext: frames[5].ciphertext)
        _ = try receiver.open(counter: frames[2].counter, ciphertext: frames[2].ciphertext)
        #expect(throws: E2EError.replay) { try receiver.open(counter: frames[2].counter, ciphertext: frames[2].ciphertext) }
        #expect(throws: E2EError.replay) { try receiver.open(counter: frames[5].counter, ciphertext: frames[5].ciphertext) }
        _ = try receiver.open(counter: frames[3].counter, ciphertext: frames[3].ciphertext)
        // Jump ahead: 1029 − 1024 = 5, so 4 is out of the window, 6 still in.
        _ = try receiver.open(counter: frames[1029].counter, ciphertext: frames[1029].ciphertext)
        #expect(throws: E2EError.replay) { try receiver.open(counter: frames[4].counter, ciphertext: frames[4].ciphertext) }
        _ = try receiver.open(counter: frames[6].counter, ciphertext: frames[6].ciphertext)
        #expect(throws: E2EError.replay) { try receiver.open(counter: frames[6].counter, ciphertext: frames[6].ciphertext) }
        // A frame that fails authentication does not burn its counter.
        #expect(throws: E2EError.authentication) { try receiver.open(counter: 7, ciphertext: frames[8].ciphertext) }
        _ = try receiver.open(counter: frames[7].counter, ciphertext: frames[7].ciphertext)
        #expect(throws: E2EError.replay) { try receiver.open(counter: .max, ciphertext: Data()) }
    }

    @Test func bindingAndPrologue() {
        #expect(E2E.prologue(machine: "m", controller: "c") == Data("ghosty-e2e-v1\u{0}m\u{0}c".utf8))
        let digest = E2E.bindingDigest(static: Data(count: 32))
        let expected = SHA256.hash(data: Data("ghosty-e2e-bind-v1\nAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=".utf8))
        #expect(digest == Data(expected))
    }
}

@Suite struct StreamCipherTests {
    // e2e.NewStream with this secret and stream ID; frames as Go sealed them.
    private let secret = Data(hex: "2122232425262728292a2b2c2d2e2f303132333435363738393a3b3c3d3e3f40")!

    @Test func controllerMatchesGo() throws {
        let stream = try StreamCipher(secret: secret, streamID: "stream-1")
        #expect(stream.seal(kind: StreamCipher.data, payload: Data("ls\r".utf8)).hex == "3da064ba7c4bbcacadc4d3c0579d2931f61d3704")
        #expect(
            stream.seal(kind: StreamCipher.resize, payload: StreamCipher.resizePayload(columns: 120, rows: 40)).hex
                == "c7ce8f3bbeaf73325a9e7d5a98f2cd3fc3ca9ee2f6")
        let first = try stream.open(Data(hex: "5effbe184d29e6183a12d59a26898030d08ec5f11836c27c")!)
        #expect(first.kind == StreamCipher.data && first.payload == Data("hello\r\n".utf8))
        let second = try stream.open(Data(hex: "c73f89aafe9455ad46df668f3cc75b62ca262e")!)
        #expect(second.kind == StreamCipher.data && second.payload == Data("$ ".utf8))
    }

    @Test func hostMatchesGo() throws {
        let stream = try StreamCipher(secret: secret, streamID: "stream-1", host: true)
        let frame = stream.seal(kind: StreamCipher.data, payload: Data("hello\r\n".utf8))
        #expect(frame.hex == "5effbe184d29e6183a12d59a26898030d08ec5f11836c27c")
        let typed = try stream.open(Data(hex: "3da064ba7c4bbcacadc4d3c0579d2931f61d3704")!)
        #expect(typed.kind == StreamCipher.data && typed.payload == Data("ls\r".utf8))
        // A repeated frame fails: the counter is implicit.
        #expect(throws: E2EError.authentication) { try stream.open(Data(hex: "3da064ba7c4bbcacadc4d3c0579d2931f61d3704")!) }
    }

    @Test func refusesBadSecret() {
        #expect(throws: NoiseError.invalidKey) { try StreamCipher(secret: Data(count: 16), streamID: "s") }
        #expect(StreamCipher.resizePayload(columns: 0x0102, rows: 0x0304) == Data([1, 2, 3, 4]))
    }
}
