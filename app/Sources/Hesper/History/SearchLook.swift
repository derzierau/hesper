import AppKit
import HesperCore
import SwiftUI

/// The one search surface's measures (⌘K All and ⌘Y History share them,
/// so switching scope keeps the field where it was). Spacing, type, radii
/// and colors come from DS / Theme; these are the surface's own sizes.
enum SearchLook {
    /// The search field's row.
    static let fieldHeight: CGFloat = 48
    /// ⌘K's width (the overlay layer caps it at the window).
    static let paletteWidth: CGFloat = 620
    /// ⌘K's result list.
    static let paletteListMaxHeight: CGFloat = 400
    /// History: a modal sheet, min(1100, 86% of the window) wide and
    /// min(760, 80%) tall, a little above the middle.
    static let historyMaxWidth: CGFloat = 1100
    static let historyMaxHeight: CGFloat = 760
    static let historyWidthFraction: CGFloat = 0.86
    static let historyHeightFraction: CGFloat = 0.8
    /// The share of the free height above the sheet (0.5 would center it).
    static let historyAboveFraction: CGFloat = 0.4
    /// Where both start: 14% down the window, at least 60 pt (the overlay
    /// layer places ⌘K the same way).
    static let topMin: CGFloat = 60
    static let topFraction: CGFloat = 0.14
    /// The right-hand preview pane: 42% of the content, 320…440 pt.
    static let previewFraction: CGFloat = 0.42
    static let previewMin: CGFloat = 320
    static let previewMax: CGFloat = 440
    /// The scrim behind both sheets (the wall keeps running underneath):
    /// Night at 55%, solid under Reduce Transparency.
    static let scrimAlpha: CGFloat = 0.55
    static let night = NSColor(srgbRed: 0x08 / 255.0, green: 0x09 / 255.0, blue: 0x14 / 255.0, alpha: 1)
    static func scrimColor(reduceTransparency: Bool) -> NSColor {
        night.withAlphaComponent(reduceTransparency ? 1 : scrimAlpha)
    }
    /// Open: scale 0.98 → 1 and a fade (DS.Motion.quick; none under Reduce Motion).
    static let openScale: CGFloat = 0.98
    /// The selected row: `working` at 14%.
    static let selectionAlpha: CGFloat = 0.14
    static var selection: Color { Color(nsColor: Theme.ns(.working, alpha: selectionAlpha)) }
    /// A search match: Horizon amber behind the text at 25%.
    static let highlightAlpha: CGFloat = 0.25
    /// Rows: title (Geist 600 13) and snippet (Geist 12) with `m` above and below.
    @MainActor static var rowHeight: CGFloat { HistoryCell.height }
    /// The footer (Hints).
    static let footerHeight: CGFloat = 28
    /// The preview's label column ("you asked").
    static let previewLabelWidth: CGFloat = 84

    /// A disabled row (an offline Mac).
    static let disabledOpacity: Double = 0.42
    /// ⌘K's History snippets are cut shorter (one line in 620 pt).
    static let paletteSnippetMax = 120

    static var markSide: CGFloat { CGFloat(StateMarkKind.side) }

    static func markKind(_ m: SearchRowMark) -> StateMarkKind { m == .live ? .working : .idle }

    /// History's sheet in a scrim of `size`.
    static func historyFrame(in size: CGSize) -> CGRect {
        let w = min(historyMaxWidth, (size.width * historyWidthFraction).rounded())
        let h = min(historyMaxHeight, (size.height * historyHeightFraction).rounded())
        let y = ((size.height - h) * historyAboveFraction).rounded()
        return CGRect(x: ((size.width - w) / 2).rounded(), y: max(0, y), width: max(0, w), height: max(0, h))
    }

    /// The card's text without Markdown: title, "you asked", "it answered"
    /// as plain text (from the session's raw fields, so bullets go too).
    static func plain(_ t: SessionCardText, _ s: Session) -> SessionCardText {
        var t = t
        let title = PlainText.plain(s.title, max: 160)
        t.title = title.isEmpty ? "(untitled)" : title
        t.asked = PlainText.plain(s.lastUser.isEmpty ? s.firstPrompt : s.lastUser, max: 400)
        t.answered = PlainText.plain(s.lastAssistant, max: 600)
        t.todos = t.todos.map { PlainText.plain($0, max: 120) }
        return t
    }

    /// The preview's meta line: "Claude · ~/projects/hesper · mini".
    static func previewMeta(_ s: Session, machines: [String: String]) -> String {
        var parts = [SessionFormat.kindLabel(s.kind)]
        if let o = SessionFormat.originLabel(s.origin) { parts[0] += " (\(o))" }
        if !s.cwd.isEmpty { parts.append(SessionFormat.abbreviate(s.cwd)) }
        if !s.machine.isEmpty { parts.append(SessionFormat.machineName(s.machine, machines)) }
        return parts.joined(separator: " · ")
    }

    /// Top of the surface in a window of `height` (both scopes).
    static func top(windowHeight: CGFloat) -> CGFloat { max(topMin, (windowHeight * topFraction).rounded()) }

    /// The snippet with its matches on Horizon amber (SwiftUI).
    static func snippet(_ r: SearchRowText) -> AttributedString {
        var a = AttributedString(r.snippet)
        a.foregroundColor = Theme.fg2
        for range in r.highlights where range.upperBound <= r.snippet.count && !range.isEmpty {
            let lo = a.index(a.startIndex, offsetByCharacters: range.lowerBound)
            let hi = a.index(a.startIndex, offsetByCharacters: range.upperBound)
            a[lo..<hi].backgroundColor = Color(nsColor: Theme.ns(.horizon, alpha: highlightAlpha))
            a[lo..<hi].foregroundColor = Theme.fg
        }
        return a
    }

    /// The snippet with its matches on Horizon amber (AppKit cells).
    static func snippet(_ r: SearchRowText, font: NSFont, matchFont: NSFont, paragraph: NSParagraphStyle) -> NSAttributedString {
        let a = NSMutableAttributedString(string: r.snippet, attributes: [.font: font, .foregroundColor: Theme.ns(.text2), .paragraphStyle: paragraph])
        for range in r.utf16Highlights where range.location + range.length <= a.length {
            a.addAttributes([.backgroundColor: Theme.ns(.horizon, alpha: highlightAlpha), .foregroundColor: Theme.ns(.text), .font: matchFont], range: range)
        }
        return a
    }
}

/// A search result row with a snippet: the Row primitive's look (mark,
/// title, Mono meta, selection, radius) with the title at 600 and the
/// snippet's matches highlighted. ⌘K's History section uses it; the ⌘Y
/// list draws the same thing in `HistoryCell` (no SwiftUI per row there).
struct SearchResultRow: View {
    var row: SearchRowText
    var selected: Bool
    @State private var hover = false

    var body: some View {
        HStack(spacing: DS.Spacing.m) {
            StateMark(SearchLook.markKind(row.mark))
            VStack(alignment: .leading, spacing: DS.Spacing.xxs) {
                HStack(spacing: DS.Spacing.m) {
                    Text(row.title).font(DS.font(.body, .semibold)).foregroundStyle(row.title == "(untitled)" ? Theme.dim : Theme.fg)
                        .lineLimit(1).truncationMode(.tail)
                    Spacer(minLength: DS.Spacing.m)
                    Text(row.meta).font(DS.font(.meta)).foregroundStyle(Theme.dim).lineLimit(1).fixedSize()
                }
                if !row.snippet.isEmpty {
                    Text(SearchLook.snippet(row)).font(DS.font(.chrome)).lineLimit(1).truncationMode(.tail)
                }
            }
        }
        .padding(.horizontal, DS.Spacing.m)
        .frame(height: SearchLook.rowHeight)
        .background(DS.Radius.shape(DS.Radius.control).fill(selected ? SearchLook.selection : hover ? Theme.chipBG.opacity(0.6) : .clear))
        .contentShape(Rectangle())
        .onHover { hover = $0 }
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(row.accessibilityLabel)
        .accessibilityAddTraits(selected ? .isSelected : [])
    }
}
