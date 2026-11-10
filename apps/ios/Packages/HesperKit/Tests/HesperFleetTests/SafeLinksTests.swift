import Foundation
import HesperFleet
import Testing

@Suite struct SafeLinksTests {
    private func md(_ s: String) throws -> AttributedString {
        try AttributedString(markdown: s, options: .init(interpretedSyntax: .inlineOnlyPreservingWhitespace))
    }

    private func links(_ a: AttributedString) -> [String] { a.runs.compactMap { $0.link?.absoluteString } }

    @Test func testOtherSchemesLoseTheirLink() throws {
        let out = SafeLinks.sanitized(try md("[call](tel:123) and [open](file:///etc/passwd)"))
        #expect(String(out.characters) == "call and open")
        #expect(links(out).isEmpty)
    }

    /// The text cannot hide where a link goes.
    @Test func testMisleadingTextShowsItsTarget() throws {
        let out = SafeLinks.sanitized(try md("see [https://example.org](https://example.net/x)"))
        #expect(String(out.characters) == "see https://example.org (https://example.net/x)")
        #expect(Set(links(out)) == ["https://example.net/x"])
        let styled = SafeLinks.sanitized(try md("[**a** b](https://example.net)"))
        #expect(String(styled.characters) == "a b (https://example.net)")
    }

    /// Bidi controls cannot reorder a label; the shown target is exactly
    /// what a tap opens.
    @Test func testBidiControlsAreStripped() throws {
        let out = SafeLinks.sanitized(try md("[\u{202E}gro.elpmaxe](https://example.net/x)"))
        #expect(String(out.characters) == "gro.elpmaxe (https://example.net/x)")
        for (link, range) in out.runs[\.link] {
            guard let link else { continue }
            #expect(link.absoluteString == "https://example.net/x")
            #expect(String(out[range].characters).contains(link.absoluteString))
        }
        #expect(SafeLinks.visible("a\u{202E}b\u{2066}c\u{2069}d\u{202A}") == "abcd")
    }

    /// A target hiding a bidi control (raw or percent-encoded) is not
    /// tappable: shown as plain text, once.
    @Test func testTargetWithBidiControlIsNotALink() throws {
        var a = AttributedString("open")
        a.link = URL(string: "https://example.net/%E2%81%A6x")
        let out = SafeLinks.sanitized(a)
        #expect(links(out).isEmpty)
        #expect(String(out.characters) == "open (https://example.net/%E2%81%A6x)")
    }

    /// Marks that place text, not reorder it, stay: RLM in Arabic, a
    /// zero-width space between Thai words, the emoji joiners.
    @Test func testLegitimateFormatCharactersStay() {
        let arabic = "مرحبا\u{200F}!"
        let thai = "สวัสดี\u{200B}ครับ"
        #expect(SafeLinks.visible(arabic) == arabic)
        #expect(SafeLinks.visible(thai) == thai)
        #expect(SafeLinks.visible("👨\u{200D}👩") == "👨\u{200D}👩")
        #expect(SafeLinks.visible("\u{0600}١٢") == "\u{0600}١٢")
    }

    @Test func testBareWebLinkIsKept() throws {
        var a = AttributedString("go https://example.org now")
        let r = try #require(a.range(of: "https://example.org"))
        a[r].link = URL(string: "https://example.org")
        let out = SafeLinks.sanitized(a)
        #expect(String(out.characters) == "go https://example.org now")
        #expect(links(out) == ["https://example.org"])
    }
}
