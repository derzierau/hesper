import Foundation
import Testing
@testable import HesperCore

@Suite struct AttentionInboxTests {
    typealias A = InboxAnswer

    @Test func approvalOffersAlwaysOnlyWhenOffered() {
        let full = Attention(kind: "approval", title: "Bash", detail: "git push", options: ["allow", "always", "deny"])
        #expect(AttentionInbox.answers(state: .approval, attention: full) == [
            A(title: "Allow", action: .decision(.allow), key: 1),
            A(title: "Always", action: .decision(.always), key: 2),
            A(title: "Deny", action: .decision(.deny), key: 3),
        ])
        let noAlways = Attention(kind: "approval", title: "Bash", options: ["allow", "deny"])
        #expect(AttentionInbox.answers(state: .approval, attention: noAlways).map(\.title) == ["Allow", "Deny"])
        #expect(AttentionInbox.answers(state: .approval, attention: noAlways).map(\.key) == [1, 2])
        // An older daemon (no options): Allow and Deny.
        #expect(AttentionInbox.approvalDecisions(Attention(kind: "approval")) == [.allow, .deny])
        #expect(AttentionInbox.approvalDecisions(nil) == [.allow, .deny])
        // Order is always allow, always, deny.
        #expect(AttentionInbox.approvalDecisions(Attention(kind: "approval", options: ["deny", "always", "allow"])) == [.allow, .always, .deny])
    }

    @Test func questionsHesperdAnswers() {
        let trust = Attention(kind: "question", title: "Trust this folder?", detail: "~/x", options: ["trust", "exit"])
        #expect(AttentionInbox.answers(state: .question, attention: trust) == [
            A(title: "Trust", action: .decision(.trust), key: 1),
            A(title: "Exit", action: .decision(.exit), key: 2),
        ])
        let update = Attention(kind: "question", title: "Update", options: ["skip", "update"])
        #expect(AttentionInbox.answers(state: .question, attention: update).map(\.action) == [.decision(.skip), .decision(.update)])
    }

    @Test func numberedQuestionsAnswerWithTheirNumber() {
        let q = Attention(kind: "question", title: "Question", detail: "Which tone should the README use? 1. Formal 2. Casual 3. Playful")
        let a = AttentionInbox.answers(state: .question, attention: q)
        #expect(a == [
            A(title: "Formal", action: .keys("1"), key: 1),
            A(title: "Casual", action: .keys("2"), key: 2),
            A(title: "Playful", action: .keys("3"), key: 3),
        ])
        #expect(AttentionInbox.question(state: .question, attention: q) == "Which tone should the README use?")
    }

    @Test func freeTextFallsBackToOpen() {
        let q = Attention(kind: "question", title: "Question", detail: "Home or settings?")
        #expect(AttentionInbox.answers(state: .question, attention: q) == [A(title: "Open", action: .open, key: nil)])
        #expect(AttentionInbox.answers(state: .question, attention: nil) == [A(title: "Open", action: .open, key: nil)])
        #expect(AttentionInbox.answers(state: .error, attention: Attention(kind: "error", title: "Agent error", detail: "boom")) == [A(title: "Open", action: .open, key: nil)])
        #expect(AttentionInbox.question(state: .question, attention: q) == "Home or settings?")
    }

    @Test func questionText() {
        #expect(AttentionInbox.question(state: .approval, attention: Attention(kind: "approval", title: "Edit", detail: "0042_users.sql")) == "Edit: 0042_users.sql")
        #expect(AttentionInbox.question(state: .approval, attention: Attention(kind: "approval", title: "Permission")) == "Permission")
        #expect(AttentionInbox.question(state: .approval, attention: nil) == "Approval")
        #expect(AttentionInbox.question(state: .error, attention: Attention(kind: "error", title: "Agent error", detail: "rate limited")) == "rate limited")
        #expect(AttentionInbox.question(state: .error, attention: Attention(kind: "error", title: "Not resumed", detail: "gone")) == "Not resumed: gone")
        #expect(AttentionInbox.question(state: .question, attention: Attention(kind: "question", title: "Log in")) == "Log in")
    }

    @Test func numberedOptionParsing() {
        #expect(AttentionInbox.numberedOptions("Pick one:\n1. Keep it\n2. Drop it") == NumberedQuestion(question: "Pick one:", options: ["Keep it", "Drop it"]))
        #expect(AttentionInbox.numberedOptions("Which? 1) red, 2) green, or 3) blue") == NumberedQuestion(question: "Which?", options: ["red", "green", "blue"]))
        #expect(AttentionInbox.numberedOptions("[1] yes [2] no")?.options == ["yes", "no"])
        // Prose numbers and single items are not options.
        #expect(AttentionInbox.numberedOptions("Do it in 2 steps?") == nil)
        #expect(AttentionInbox.numberedOptions("Bump to version 1.2 now?") == nil)
        #expect(AttentionInbox.numberedOptions("Only 1. one option") == nil)
        #expect(AttentionInbox.numberedOptions("") == nil)
        // Must start at 1 and count up.
        #expect(AttentionInbox.numberedOptions("2. a 3. b") == nil)
    }

    @Test func whereabouts() {
        #expect(AttentionInbox.whereabouts(machine: "L", local: "L", onThisWall: true) == [])
        #expect(AttentionInbox.whereabouts(machine: "L", local: "L", onThisWall: false) == ["other wall"])
        #expect(AttentionInbox.whereabouts(machine: "M", local: "L", onThisWall: true) == ["other Mac"])
        #expect(AttentionInbox.whereabouts(machine: "M", local: "L", onThisWall: false) == ["other wall", "other Mac"])
    }

    @Test func keys() {
        #expect(InboxKeys.route(.type("j")) == .move(1))
        #expect(InboxKeys.route(.type("K")) == .move(-1))
        #expect(InboxKeys.route(.down) == .move(1))
        #expect(InboxKeys.route(.up) == .move(-1))
        #expect(InboxKeys.route(.type("1")) == .answer(0))
        #expect(InboxKeys.route(.type("9")) == .answer(8))
        #expect(InboxKeys.route(.type("0")) == .none)
        #expect(InboxKeys.route(.type("x")) == .none)
        #expect(InboxKeys.route(.type("12")) == .none)
        #expect(InboxKeys.route(.activate) == .primary)
        #expect(InboxKeys.route(.alternate) == .open)
        #expect(InboxKeys.route(.close) == .close)
        #expect(InboxKeys.route(.pass) == .none)
        #expect(InboxKeys.route(.actOn) == .none)
        // Through the overlay router: plain j/k and digits arrive as typing.
        #expect(InboxKeys.route(OverlayKeys.route(KeyChord(.char("j")), characters: "j")) == .move(1))
        #expect(InboxKeys.route(OverlayKeys.route(KeyChord(.char("2")), characters: "2")) == .answer(1))
        #expect(InboxKeys.route(OverlayKeys.route(KeyChord(.enter))) == .primary)
        #expect(InboxKeys.route(OverlayKeys.route(KeyChord(.escape))) == .close)
    }

    @Test func numberKeysPickAnswers() {
        let a = AttentionInbox.answers(state: .approval, attention: Attention(kind: "approval", options: ["allow", "always", "deny"]))
        #expect(InboxKeys.answer(2, in: a)?.action == .decision(.deny))
        #expect(InboxKeys.answer(3, in: a) == nil)
        #expect(InboxKeys.primary(a)?.action == .decision(.allow))
        let open = AttentionInbox.answers(state: .question, attention: nil)
        #expect(InboxKeys.answer(0, in: open) == nil) // Open has no number: ⏎ or ⌘O
        #expect(InboxKeys.primary(open)?.action == .open)
    }
}
