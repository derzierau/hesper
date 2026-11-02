import Foundation
import Testing
@testable import HesperCore

struct LoopsKeysTests {
    func k(_ c: Character, command: Bool = false, shift: Bool = false) -> KeyChord { KeyChord(.char(c), command: command, shift: shift) }

    @Test func verbsPerSurface() {
        #expect(LoopsKeys.route(k("d"), in: .loops) == .done)
        #expect(LoopsKeys.route(k("d"), in: .wrapUp) == .done)
        #expect(LoopsKeys.route(k("d"), in: .product) == .pass)
        #expect(LoopsKeys.route(k("p"), in: .loops) == .park)
        #expect(LoopsKeys.route(k("p"), in: .product) == .plan)
        #expect(LoopsKeys.route(k("x"), in: .product) == .dropProposal)
        #expect(LoopsKeys.route(k("n"), in: .loops) == .nextAgent)
        #expect(LoopsKeys.route(k("n"), in: .wrapUp) == .nextStep)
        #expect(LoopsKeys.route(k(" "), in: .loops) == .check)
        #expect(LoopsKeys.route(KeyChord(.enter), in: .loops) == .open)
        #expect(LoopsKeys.route(KeyChord(.enter), in: .wrapUp) == .accept)
        #expect(LoopsKeys.route(KeyChord(.enter, command: true), in: .loops) == .startDraft)
        #expect(LoopsKeys.route(KeyChord(.enter, command: true), in: .wrapUp) == .apply)
        #expect(LoopsKeys.route(KeyChord(.enter, command: true), in: .product) == .savePlan)
        #expect(LoopsKeys.route(KeyChord(.left), in: .loops) == .left)
        #expect(LoopsKeys.route(KeyChord(.escape), in: .product) == .close)
        #expect(LoopsKeys.route(k("D", shift: true), in: .loops) == .done) // ⇧ is only the letter's case
    }

    @Test func ownChordClosesOthersPassThrough() {
        #expect(LoopsKeys.route(k("l", command: true), in: .loops) == .close)
        #expect(LoopsKeys.route(k("l", command: true, shift: true), in: .product) == .close)
        #expect(LoopsKeys.route(k("e", command: true, shift: true), in: .wrapUp) == .close)
        #expect(LoopsKeys.passesThrough(k("l", command: true, shift: true), in: .loops))
        #expect(!LoopsKeys.passesThrough(k("l", command: true), in: .loops))
        #expect(LoopsKeys.passesThrough(k("k", command: true), in: .wrapUp))
        #expect(LoopsKeys.passesThrough(k("r", command: true), in: .product))
    }

    @Test func aFieldKeepsItsLetters() {
        #expect(LoopsKeys.route(k("d"), in: .loops, editing: true) == .pass)
        #expect(LoopsKeys.route(k(" "), in: .loops, editing: true) == .pass)
        #expect(LoopsKeys.route(KeyChord(.enter), in: .loops, editing: true) == .commitField)
        #expect(LoopsKeys.route(KeyChord(.escape), in: .loops, editing: true) == .cancelField)
        #expect(LoopsKeys.route(KeyChord(.down), in: .product, editing: true) == .down)
    }

    @Test func sheetsHideWithoutLoops() {
        for id in [CommandID.loops, .product, .wrapUp] {
            #expect(CommandRegistry.command(id)!.availability(CommandContext(loopsSupported: false)) == .hidden)
            #expect(CommandRegistry.command(id)!.availability(.none) == .enabled)
        }
        guard case .disabled = CommandRegistry.command(.closeAgent)!.availability(CommandContext(selectedState: .working, keyWindow: .wall, loopsOpen: true)) else {
            Issue.record("Close enabled under the Loops sheet"); return
        }
    }
}
