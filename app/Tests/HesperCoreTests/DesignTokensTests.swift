import Foundation
import Testing
@testable import HesperCore

@Suite struct DesignTokensTests {
    @Test func scales() {
        #expect(DesignTokens.Spacing.all == [2, 4, 6, 8, 12, 16, 24])
        #expect([DesignTokens.Radius.kbd, DesignTokens.Radius.control, DesignTokens.Radius.tile, DesignTokens.Radius.panel] == [4, 7, 10, 14])
        #expect(DesignTokens.TextStyle.allCases.map(\.size) == [11, 12, 13, 15, 20, 28])
        #expect(DesignTokens.TextStyle.allCases.filter(\.isMono) == [.meta])
        #expect(DesignTokens.Weight.allCases.map(\.rawValue) == [400, 500, 600])
        #expect(DesignTokens.Chrome.maxHeight == 28)
    }

    @Test func density() {
        #expect(DesignTokens.Density.default == .comfortable)
        #expect(DesignTokens.Density.comfortable.tileHeader == 26 && DesignTokens.Density.compact.tileHeader == 22)
        #expect(DesignTokens.Density.comfortable.tileGutter == 8 && DesignTokens.Density.compact.tileGutter == 6)
        #expect(DesignTokens.Density.allCases.allSatisfy { $0.bandGap == 16 && $0.tileHeader <= DesignTokens.Chrome.maxHeight })
        // Persisted by raw value.
        #expect(DesignTokens.Density(rawValue: "compact") == .compact)
        #expect(DesignTokens.Density(rawValue: "bogus") == nil)
    }

    @Test func motionRespectsReduceMotion() {
        typealias M = DesignTokens.Motion
        #expect(M.allCases.map(\.duration) == [0.12, 0.18, 0.22])
        for m in M.allCases {
            #expect(m.duration(reduceMotion: false) == m.duration)
            #expect(m.duration(reduceMotion: true) == 0)
        }
        #expect(M.animates(reduceMotion: false) && !M.animates(reduceMotion: true))
        #expect(M.curve == (0.2, 0, 0, 1))
    }

    @Test func everyAgentStateHasAMark() {
        let expected: [AgentState: StateMarkKind] = [
            .starting: .starting, .working: .working, .approval: .needsYou, .question: .question,
            .done: .done, .idle: .idle, .error: .error, .exited: .exited, .unknown: .idle,
        ]
        for s in AgentState.allCases { #expect(StateMarkKind(s) == expected[s], "\(s)") }
    }

    @Test func markShapesAndTones() {
        #expect(StateMarkKind.allCases.filter { $0.fill == .filled } == [.working, .needsYou, .question, .done, .error])
        #expect(StateMarkKind.idle.fill == .outlined && StateMarkKind.starting.fill == .outlined && StateMarkKind.exited.fill == .slashed)
        #expect(StateMarkKind.starting.tone == .working && StateMarkKind.idle.tone == .dim && StateMarkKind.exited.tone == .dim)
        // One light: only "needs you" is Signal, and only it pulses.
        #expect(StateMarkKind.allCases.filter { $0.tone == .signal } == [.needsYou])
        #expect(StateMarkKind.allCases.filter(\.pulses) == [.needsYou])
        #expect(StateMarkKind.needsYou.pulses(reduceMotion: false) && !StateMarkKind.needsYou.pulses(reduceMotion: true))
        #expect(StateMarkKind.side == 7 && StateMarkKind.stroke == 1.5)
    }
}
