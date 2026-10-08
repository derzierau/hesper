import Foundation

/// Markdown → plain text for one-line snippets and preview lines (History
/// rows, ⌘K's History section, the preview's "you asked" / "it answered").
/// Pure; tested in PlainTextTests.
///
/// Removes emphasis markers (`**` `__` `*` `_` `~~`), keeps a link's text
/// (`[text](url)` → `text`, images too), drops inline-code ticks and code
/// fences, list bullets ("- ", "* ", "1. "), heading and quote markers at
/// line starts, a wrapping pair of quotes, and collapses all whitespace
/// (newlines too) to single spaces. snake_case and "2 * 3" stay as they are.
public enum PlainText {
    /// Plain text of `s`, cut at `max` characters ("…" appended).
    public static func plain(_ s: String, max: Int = Int.max) -> String {
        plain(prepare(s), highlights: [], max: max).text
    }

    /// The block-level pass on raw text (before search marks are read: a
    /// link's brackets would otherwise look like a match): links and images
    /// keep their text; bullets, numbers, headings and quote markers at line
    /// starts go. Newlines stay (the inline pass collapses them).
    public static func prepare(_ s: String) -> String {
        var items = Array(s).enumerated().map { Item(c: $0.element, src: $0.offset) }
        items = lineMarkers(items)
        items = links(items)
        return String(items.map(\.c))
    }

    /// The inline pass on text whose `highlights` (character offsets) must
    /// follow it: emphasis, code ticks, wrapping quotes, whitespace. The
    /// ranges come back in the new text's offsets (empty ones dropped).
    public static func plain(_ s: String, highlights: [Range<Int>], max: Int = Int.max) -> (text: String, ranges: [Range<Int>]) {
        let n = s.count
        var items = Array(s).enumerated().map { Item(c: $0.element, src: $0.offset) }
        items = lineMarkers(items) // a text that starts with a bullet
        items = codeTicks(items)
        items = emphasis(items)
        items = whitespace(items)
        items = wrappingQuotes(items)
        var cut = false
        if items.count > max {
            items = Array(items.prefix(Swift.max(0, max)))
            while items.last?.c.isWhitespace == true { items.removeLast() }
            cut = true
        }
        // Source offset → new offset: how many kept characters lie before it.
        var before = [Int](repeating: 0, count: n + 1)
        var kept = [Bool](repeating: false, count: n)
        for it in items where it.src < n { kept[it.src] = true }
        for i in 0..<n { before[i + 1] = before[i] + (kept[i] ? 1 : 0) }
        var ranges: [Range<Int>] = []
        for r in highlights where r.lowerBound >= 0 && r.upperBound <= n && !r.isEmpty {
            let lo = before[r.lowerBound], hi = before[r.upperBound]
            if hi > lo { ranges.append(lo..<hi) }
        }
        var text = String(items.map(\.c))
        if cut { text.append("…") }
        return (text, ranges)
    }

    // MARK: Passes (each keeps the source offset of every character)

    struct Item { var c: Character; var src: Int }

    private static func isSpace(_ c: Character) -> Bool { c == " " || c == "\t" }

    /// "- ", "* ", "+ ", "• ", "1. ", "1) ", "# ", "> " at line starts.
    private static func lineMarkers(_ a: [Item]) -> [Item] {
        var out: [Item] = []
        out.reserveCapacity(a.count)
        var i = 0
        var lineStart = true
        while i < a.count {
            if lineStart {
                // Leading indentation stays (collapsed later); look past it.
                var j = i
                while j < a.count && isSpace(a[j].c) { j += 1 }
                if let end = markerEnd(a, j) {
                    out.append(contentsOf: a[i..<j])
                    i = end
                    lineStart = true // "> - item": markers nest
                    continue
                }
                lineStart = false
            }
            let c = a[i].c
            out.append(a[i])
            if c.isNewline { lineStart = true }
            i += 1
        }
        return out
    }

    /// Where the text after a line-start marker at `j` begins (nil: none).
    private static func markerEnd(_ a: [Item], _ j: Int) -> Int? {
        guard j < a.count else { return nil }
        func spaceAfter(_ k: Int) -> Int? {
            guard k < a.count, isSpace(a[k].c) else { return nil }
            var e = k
            while e < a.count && isSpace(a[e].c) { e += 1 }
            return e
        }
        let c = a[j].c
        if "-*+•".contains(c) {
            // "---" (a rule) or "**bold" are not bullets.
            return spaceAfter(j + 1)
        }
        if c == ">" { return spaceAfter(j + 1) ?? (j + 1 < a.count ? j + 1 : nil) }
        if c == "#" {
            var k = j
            while k < a.count && a[k].c == "#" && k - j < 6 { k += 1 }
            return spaceAfter(k)
        }
        if c.isASCII && c.isNumber {
            var k = j
            while k < a.count && a[k].c.isASCII && a[k].c.isNumber && k - j < 3 { k += 1 }
            if k < a.count && (a[k].c == "." || a[k].c == ")") { return spaceAfter(k + 1) }
        }
        return nil
    }

    /// `[text](url)` and `![alt](url)` → their text (nested brackets in
    /// the text, like search marks, stay).
    private static func links(_ a: [Item]) -> [Item] {
        var drop = [Bool](repeating: false, count: a.count)
        var i = 0
        while i < a.count {
            guard a[i].c == "[" else { i += 1; continue }
            var depth = 0, close: Int?
            var k = i
            while k < a.count {
                if a[k].c == "[" { depth += 1 } else if a[k].c == "]" { depth -= 1; if depth == 0 { close = k; break } }
                if a[k].c.isNewline { break }
                k += 1
            }
            guard let cl = close, cl + 1 < a.count, a[cl + 1].c == "(" else { i += 1; continue }
            var e = cl + 2
            var paren = 1
            while e < a.count {
                if a[e].c == "(" { paren += 1 } else if a[e].c == ")" { paren -= 1; if paren == 0 { break } }
                if a[e].c.isWhitespace { break }
                e += 1
            }
            guard e < a.count, a[e].c == ")" else { i += 1; continue }
            drop[i] = true
            if i > 0 && a[i - 1].c == "!" { drop[i - 1] = true }
            for x in cl...e { drop[x] = true }
            i += 1 // the text may hold another link
        }
        return a.enumerated().filter { !drop[$0.offset] }.map(\.element)
    }

    /// Every backtick (inline code and fences).
    private static func codeTicks(_ a: [Item]) -> [Item] { a.filter { $0.c != "`" } }

    /// `**` `__` `~~` always; a single `*` / `_` when it opens (after a
    /// space or the start, before a non-space) or closes (after a non-space,
    /// before a space, punctuation or the end).
    private static func emphasis(_ a: [Item]) -> [Item] {
        var drop = [Bool](repeating: false, count: a.count)
        var i = 0
        while i < a.count {
            let c = a[i].c
            if (c == "*" || c == "_" || c == "~"), i + 1 < a.count, a[i + 1].c == c {
                var k = i
                while k < a.count && a[k].c == c { k += 1 }
                if c != "~" || k - i == 2 { for x in i..<k { drop[x] = true } }
                i = k
                continue
            }
            if c == "*" || c == "_" {
                let prev: Character? = i > 0 ? a[i - 1].c : nil
                let next: Character? = i + 1 < a.count ? a[i + 1].c : nil
                let opens = (prev == nil || prev!.isWhitespace || isOpenPunct(prev!)) && next != nil && !next!.isWhitespace
                let closes = prev != nil && !prev!.isWhitespace && (next == nil || next!.isWhitespace || isPunct(next!))
                if opens != closes { drop[i] = true } // both or neither: literal ("a_b", "2 * 3")
            }
            i += 1
        }
        return a.enumerated().filter { !drop[$0.offset] }.map(\.element)
    }

    private static func isPunct(_ c: Character) -> Bool { ".,;:!?)]}'\"”’…".contains(c) }
    private static func isOpenPunct(_ c: Character) -> Bool { "([{'\"“‘".contains(c) }

    /// Runs of whitespace (newlines too) → one space; none at the ends.
    private static func whitespace(_ a: [Item]) -> [Item] {
        var out: [Item] = []
        out.reserveCapacity(a.count)
        var pending: Item?
        for it in a {
            if it.c.isWhitespace || it.c.isNewline {
                if !out.isEmpty && pending == nil { pending = Item(c: " ", src: it.src) }
                continue
            }
            if let p = pending { out.append(p); pending = nil }
            out.append(it)
        }
        return out
    }

    /// A text wrapped in quotes (“…”, "…") loses them; a lone leading “ too.
    private static func wrappingQuotes(_ a: [Item]) -> [Item] {
        guard let f = a.first?.c, "“\"„«".contains(f) else { return a }
        var b = Array(a.dropFirst())
        if let l = b.last?.c, "”\"“»".contains(l) { b.removeLast() }
        return b
    }
}
