import HesperDesign
import HesperSurfaces
import SwiftUI

/// The supplied rows of the agent's live screen, as styled text. Rows never wrap; a
/// grid wider than the phone scrolls sideways as a block, from the left
/// edge, so the prompt's start is always in view.
struct LiveScreenView: View {
    let screen: ScreenWatch
    let indices: [Int]

    var body: some View {
        let rows = screen.rows
        let visible = indices.filter { rows.rows.indices.contains($0) }
        VStack(alignment: .leading, spacing: DS.Spacing.xs) {
            if screen.phase == .reconnecting {
                Text("Reconnecting…").font(Typeface.mono(11, relativeTo: .caption2)).foregroundStyle(Theme.dim)
            }
            ScrollView(.horizontal, showsIndicators: false) {
                VStack(alignment: .leading, spacing: 0) {
                    ForEach(visible, id: \.self) { i in
                        Text(Self.line(rows.rows[i], cursor: rows.cursor?.row == i ? rows.cursor?.col : nil))
                            .lineLimit(1)
                            .fixedSize()
                    }
                }
                .font(TranscriptRow.font)
            }
        }
    }

    /// One row; `cursor` is the cursor's column on it, drawn as a block.
    static func line(_ runs: [StyledRun], cursor: Int?) -> AttributedString {
        var out = AttributedString()
        // `cursor` counts cells, not characters (`ScreenRows.cursorPosition`).
        let at = cursor.flatMap { ScreenRows.cursorPosition(in: runs, col: $0) }
        for (r, run) in runs.enumerated() {
            if let at, at.run == r {
                let chars = Array(run.text)
                out += piece(String(chars[..<at.offset]), run.style)
                out += cursorCell(String(chars[at.offset]))
                out += piece(String(chars[(at.offset + 1)...]), run.style)
            } else {
                out += piece(run.text, run.style)
            }
        }
        if let c = cursor, at == nil {
            let used = runs.reduce(0) { $0 + $1.text.reduce(0) { $0 + ScreenRows.cells($1) } }
            out += AttributedString(String(repeating: " ", count: max(c - used, 0)))
            out += cursorCell(" ")
        }
        // An empty row keeps its height.
        return out.characters.isEmpty ? AttributedString(" ") : out
    }

    private static func piece(_ text: String, _ style: RunStyle) -> AttributedString {
        var a = AttributedString(text)
        guard !text.isEmpty else { return a }
        var fg = Theme.color(style.fg, default: Theme.text)
        var bg = style.bg.map { Theme.color($0, default: Theme.background) }
        if style.reverse { (fg, bg) = (bg ?? Theme.background, fg) }
        a.foregroundColor = style.dim ? fg.opacity(Theme.faint) : fg
        if let bg { a.backgroundColor = bg }
        var intent: InlinePresentationIntent = []
        if style.bold { intent.insert(.stronglyEmphasized) }
        if style.italic { intent.insert(.emphasized) }
        if !intent.isEmpty { a.inlinePresentationIntent = intent }
        if style.underline { a.underlineStyle = .single }
        if style.strike { a.strikethroughStyle = .single }
        return a
    }

    private static func cursorCell(_ char: String) -> AttributedString {
        var a = AttributedString(char)
        a.foregroundColor = Theme.onAccent
        a.backgroundColor = Theme.accent
        return a
    }
}
