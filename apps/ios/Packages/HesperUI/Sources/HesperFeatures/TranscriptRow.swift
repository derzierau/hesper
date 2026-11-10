import Foundation
import HesperCore
import HesperDesign
import HesperFleet
import SwiftUI

/// One history row in Claude Code's look (spec "Terminal look"): `›` and
/// what the person typed, the agent's reply with inline markdown, a tool
/// call as one `⏺` line that opens to its result, a note centered.
struct TranscriptRow: View {
    let line: TranscriptLine
    /// The tool call's result is unfolded.
    var open = false
    /// Parses each item's text once and folds tool calls; a reference, so
    /// SwiftUI still skips rows whose line and `open` did not change.
    var store: TranscriptStore?
    var onShowFullText: ((TranscriptItem) -> Void)?

    /// The size of the raw terminal's text.
    static let font = Typeface.mono(13.5, relativeTo: .footnote)

    var body: some View {
        content
            .font(Self.font)
            .textSelection(.enabled)
            .frame(maxWidth: .infinity, alignment: line.item.kind == .system ? .center : .leading)
    }

    @ViewBuilder private var content: some View {
        let item = line.item
        switch item.kind {
        case .user:
            VStack(alignment: .leading, spacing: DS.Spacing.xxs) {
                HStack(alignment: .firstTextBaseline, spacing: 0) {
                    Text("› ").foregroundStyle(Theme.accent)
                    Text(text(item, Self.plain)).foregroundStyle(Theme.text)
                }
                fullTextAction(item)
            }
        case .assistant:
            VStack(alignment: .leading, spacing: DS.Spacing.xxs) {
                Text(text(item, Self.markdown)).foregroundStyle(Theme.text)
                fullTextAction(item)
            }
        case .tool:
            VStack(alignment: .leading, spacing: DS.Spacing.xxs) {
                HStack(alignment: .firstTextBaseline, spacing: 0) {
                    Text("⏺ ").foregroundStyle(line.result?.ok == false ? Theme.error : Theme.muted)
                    Text([item.tool, item.summary].compactMap(\.self).joined(separator: " "))
                        .foregroundStyle(Theme.muted)
                }
                .contentShape(Rectangle())
                .onTapGesture { store?.toggle(line.id) }
                .accessibilityAddTraits(.isButton)
                if open, let result = line.result {
                    folded(result)
                }
            }
        case .result:
            // Its call is on a page not loaded yet: the result stands alone.
            folded(item)
        case .system:
            Text(item.text ?? "").foregroundStyle(Theme.dim).multilineTextAlignment(.center)
        }
    }

    @ViewBuilder private func fullTextAction(_ item: TranscriptItem) -> some View {
        if item.truncated {
            Button("Show full text") { onShowFullText?(item) }
                .foregroundStyle(Theme.accent)
        }
    }

    /// A result under its call: `⎿` and the text, `…` when the Mac clipped it.
    private func folded(_ result: TranscriptItem) -> some View {
        HStack(alignment: .firstTextBaseline, spacing: 0) {
            Text("  ⎿  ")
            VStack(alignment: .leading, spacing: 0) {
                Text(text(result, Self.plain))
                if result.truncated { Text("…") }
            }
        }
        .foregroundStyle(Theme.muted)
    }

    /// `item`'s text parsed by `parse`, once per item when there is a store.
    private func text(_ item: TranscriptItem, _ parse: (String) -> AttributedString) -> AttributedString {
        store?.text(item, parse) ?? parse(item.text ?? "")
    }

    /// Plain text with its links.
    static func plain(_ text: String) -> AttributedString { linked(AttributedString(text)) }

    /// Inline markdown only (`**bold**`, `*italic*`, `` `code` ``). Fenced
    /// blocks and table lines (`|`) stay the raw lines they are: the inline
    /// parser would read a fence as one code span and drop the backticks,
    /// and would mangle a table's cells.
    static func markdown(_ text: String) -> AttributedString {
        var segments: [(code: Bool, lines: [Substring])] = []
        var inFence = false
        for line in text.split(separator: "\n", omittingEmptySubsequences: false) {
            let trimmed = line.trimmingCharacters(in: .whitespaces)
            let fence = trimmed.hasPrefix("```")
            let code = inFence || fence || trimmed.hasPrefix("|")
            if fence { inFence.toggle() }
            if segments.last?.code == code {
                segments[segments.count - 1].lines.append(line)
            } else {
                segments.append((code, [line]))
            }
        }
        var out = AttributedString()
        for (i, segment) in segments.enumerated() {
            if i > 0 { out += AttributedString("\n") }
            let raw = segment.lines.joined(separator: "\n")
            if !segment.code,
                let parsed = try? AttributedString(
                    markdown: raw, options: .init(interpretedSyntax: .inlineOnlyPreservingWhitespace))
            {
                out += parsed
            } else {
                out += AttributedString(raw)
            }
        }
        return linked(out)
    }

    /// Made once: building a detector compiles its patterns.
    private static let detector = try? NSDataDetector(types: NSTextCheckingResult.CheckingType.link.rawValue)

    /// Bare URLs as links, which `Text` opens in the browser; only web
    /// links, each showing its target (`SafeLinks`).
    static func linked(_ text: AttributedString) -> AttributedString {
        guard let detector else { return SafeLinks.sanitized(text) }
        var text = SafeLinks.sanitized(text)
        let plain = String(text.characters)
        for match in detector.matches(in: plain, range: NSRange(plain.startIndex..., in: plain)) {
            guard let url = match.url, ["http", "https"].contains(url.scheme?.lowercased() ?? ""),
                let range = Range(match.range, in: text), text[range].link == nil
            else { continue }
            text[range].link = url
        }
        return text
    }
}
