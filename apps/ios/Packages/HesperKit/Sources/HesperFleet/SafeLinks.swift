import Foundation

/// Links in an agent's output, made safe to tap: an agent's text is not
/// trusted, so a link opens only a web page, and never one other than
/// the text shows.
public enum SafeLinks {
    /// Keeps http(s) links and removes any other scheme (`tel:`, `file:`,
    /// an app's own). A markdown link whose text is not its target keeps
    /// the text and shows the target after it, ` (https://…)`.
    public static func sanitized(_ text: AttributedString) -> AttributedString {
        var out = AttributedString()
        // By link, so a link whose text mixes styles is one piece.
        for (link, range) in text.runs[\.link] {
            var piece = AttributedString(text[range])
            guard let url = link else {
                out += piece
                continue
            }
            // A label cannot be reordered or hidden by format controls.
            let label = visible(String(piece.characters))
            if label != String(piece.characters) {
                let attributes = piece.runs.first?.attributes ?? AttributeContainer()
                piece = AttributedString(label, attributes: attributes)
            }
            let scheme = url.scheme?.lowercased()
            guard scheme == "http" || scheme == "https" else {
                piece.link = nil
                out += piece
                continue
            }
            // A target hiding a bidi control (raw or percent-encoded) could
            // read as another address: it is shown, never opened.
            let target = url.absoluteString
            let hides = [target, target.removingPercentEncoding ?? target].contains { visible($0) != $0 }
            if hides { piece.link = nil }
            out += piece
            // The shown target is exactly the string that opens.
            if String(piece.characters) != target {
                var shown = AttributedString(" (\(target))")
                if !hides { shown.link = url }
                out += shown
            }
        }
        return out
    }

    /// `text` without the bidi controls that reorder it (`strips`), for
    /// text from the Mac or an agent shown as a label or message.
    public static func visible(_ text: String) -> String {
        guard text.unicodeScalars.contains(where: strips) else { return text }
        return String(String.UnicodeScalarView(text.unicodeScalars.filter { !strips($0) }))
    }

    /// Only the controls that reorder text: the bidi embeddings and
    /// overrides (U+202A–U+202E) and isolates (U+2066–U+2069). Marks that
    /// place punctuation (LRM, RLM, ALM), the zero-width space that breaks
    /// Thai and Khmer words, the Arabic number signs, the joiners and tag
    /// characters are text and stay.
    private static func strips(_ s: Unicode.Scalar) -> Bool {
        switch s.value {
        case 0x202A...0x202E, 0x2066...0x2069: return true
        default: return false
        }
    }
}
