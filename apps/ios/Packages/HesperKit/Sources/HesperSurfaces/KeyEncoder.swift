import Foundation

/// A key the phone's key bar sends. Named KeyPress because libghostty
/// already exports a TerminalKey.
public enum KeyPress: Equatable, Sendable {
    case enter, escape, tab, backspace, up, down, left, right
    case ctrl(Character)
}

/// Taps and typed text as the legacy bytes any terminal program reads:
/// no keyboard protocol to negotiate with the agent.
public enum KeyEncoder {
    public static func bytes(_ key: KeyPress) -> Data {
        switch key {
        case .enter: return Data("\r".utf8)
        case .escape: return Data([0x1B])
        case .tab: return Data("\t".utf8)
        case .backspace: return Data([0x7F])
        case .up: return Data("\u{1B}[A".utf8)
        case .down: return Data("\u{1B}[B".utf8)
        case .right: return Data("\u{1B}[C".utf8)
        case .left: return Data("\u{1B}[D".utf8)
        case .ctrl(let c):
            // As xterm: letters, space and @ [ \ ] ^ _ have a control code,
            // ? is DEL; anything else (a digit, an emoji) sends nothing.
            guard let a = c.asciiValue else { return Data() }
            if c == "?" { return Data([0x7F]) }
            guard c.isLetter || c == " " || (0x40...0x5F).contains(a) else { return Data() }
            return Data([a & 0x1F])
        }
    }

    /// Typed text. One character → its key: a printable one as is, Tab
    /// and Delete as themselves, a newline as Enter (CR), ESC dropped, any
    /// other control in a paste. More (a paste, dictation) → one bracketed
    /// paste with the newlines kept, so a newline in it never acts as
    /// Enter. ESC is removed from text, as terminals do, so a paste cannot
    /// end early and run the rest as typed input. Empty → nothing.
    public static func bytes(text: String) -> Data {
        guard !text.isEmpty else { return Data() }
        if text.count == 1, let c = text.first {
            // A hardware keyboard's Tab, Return and Delete arrive as text.
            if c == "\t" || c == "\u{7F}" { return Data(text.utf8) }
            if c.isNewline { return Data("\r".utf8) }
            if !c.unicodeScalars.contains(where: { $0.properties.generalCategory == .control }) {
                return Data(text.utf8)
            }
        }
        let safe = String(String.UnicodeScalarView(text.unicodeScalars.filter { $0 != "\u{1B}" }))
        guard !safe.isEmpty else { return Data() }
        return Data("\u{1B}[200~\(safe)\u{1B}[201~".utf8)
    }

}
