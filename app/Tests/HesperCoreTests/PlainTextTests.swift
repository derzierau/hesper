import Foundation
import Testing
@testable import HesperCore

@Suite struct PlainTextTests {
    @Test func emphasisCodeLinks() {
        #expect(PlainText.plain("**Fixed** the `debounce` in [Toolbar.swift](app/Toolbar.swift)") == "Fixed the debounce in Toolbar.swift")
        #expect(PlainText.plain("an *important* and _quiet_ ~~old~~ note") == "an important and quiet old note")
        #expect(PlainText.plain("see ![diagram](x.png) here") == "see diagram here")
        #expect(PlainText.plain("```swift\nlet a = 1\n```") == "swift let a = 1")
    }

    @Test func literalsStay() {
        #expect(PlainText.plain("snake_case_name and 2 * 3 = 6") == "snake_case_name and 2 * 3 = 6")
        #expect(PlainText.plain("[not a link] (really)") == "[not a link] (really)")
    }

    @Test func bulletsHeadingsWhitespace() {
        let md = "## Summary\n\n- first point\n- second **bold**\n1. numbered\n> quoted\n  * nested"
        #expect(PlainText.plain(md) == "Summary first point second bold numbered quoted nested")
        #expect(PlainText.plain("  lots   of\n\n space\t") == "lots of space")
        #expect(PlainText.plain("---") == "---")
    }

    @Test func quotesAndCut() {
        #expect(PlainText.plain("“Done: all green”") == "Done: all green")
        #expect(PlainText.plain("abcdef ghij", max: 6) == "abcdef…")
        #expect(PlainText.plain("abc def", max: 4) == "abc…")
    }

    @Test func highlightsFollowTheStrip() {
        // A search snippet: hesperd's [marks] inside Markdown.
        let raw = "**Fixed** the [debounce] in [`Toolbar.swift`](a/Toolbar.swift)"
        let h = SessionFormat.highlights(PlainText.prepare(raw))
        let p = PlainText.plain(h.text, highlights: h.ranges)
        #expect(p.text == "Fixed the debounce in Toolbar.swift")
        #expect(p.ranges == [10..<18])
        // A match inside emphasis keeps its word.
        let h2 = SessionFormat.highlights("- **[bug]** fixed")
        let p2 = PlainText.plain(h2.text, highlights: h2.ranges)
        #expect(p2.text == "bug fixed" && p2.ranges == [0..<3])
    }

    @Test func rowTextIsPlain() {
        let s = Session(id: "s", title: "", firstPrompt: "**Make** the panel `opaque`",
                        lastAssistant: "")
        let r = SearchRowText(s, machines: [:])
        #expect(r.title == "(untitled)")
        #expect(r.snippet == "Make the panel opaque", "an untitled row shows its first prompt")
        let a = SearchRowText(Session(id: "t", title: "Fix", lastAssistant: "Done:\n- **one**\n- [two](x)"), machines: [:])
        #expect(a.snippet == "Done: one two", "no quotes, bullets or Markdown")
    }
}
