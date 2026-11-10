import Foundation
import HesperSurfaces
import Testing

@Suite struct KeyEncoderTests {
    @Test func testKeys() {
        let table: [(KeyPress, String)] = [
            (.enter, "\r"), (.escape, "\u{1B}"), (.tab, "\t"), (.backspace, "\u{7F}"),
            (.up, "\u{1B}[A"), (.down, "\u{1B}[B"), (.right, "\u{1B}[C"), (.left, "\u{1B}[D"),
        ]
        for (key, bytes) in table {
            #expect(KeyEncoder.bytes(key) == Data(bytes.utf8))
        }
    }

    @Test func testSingleCharPlain() {
        #expect(KeyEncoder.bytes(text: "a") == Data([0x61]))
        #expect(KeyEncoder.bytes(text: "é") == Data("é".utf8))
        #expect(KeyEncoder.bytes(text: "") == Data())
    }

    @Test func testPasteBracketed() {
        #expect(KeyEncoder.bytes(text: "ab\ncd") == Data("\u{1B}[200~ab\ncd\u{1B}[201~".utf8))
        #expect(KeyEncoder.bytes(text: "hi") == Data("\u{1B}[200~hi\u{1B}[201~".utf8))
    }

    /// A hardware keyboard's Tab, Return and Delete arrive as one
    /// character of text; they keep their key meaning.
    @Test func testLoneKeysKeepTheirMeaning() {
        #expect(KeyEncoder.bytes(text: "\t") == Data("\t".utf8))
        #expect(KeyEncoder.bytes(text: "\u{7F}") == Data([0x7F]))
        #expect(KeyEncoder.bytes(text: "\n") == Data("\r".utf8))
        #expect(KeyEncoder.bytes(text: "\r") == Data("\r".utf8))
        #expect(KeyEncoder.bytes(text: "\u{1B}") == Data())
    }

    /// A paste cannot end itself early and run the rest as typed input.
    @Test func testPasteDropsEscape() {
        let evil = "a\u{1B}[201~rm -rf x\r"
        #expect(KeyEncoder.bytes(text: evil) == Data("\u{1B}[200~a[201~rm -rf x\r\u{1B}[201~".utf8))
        #expect(KeyEncoder.bytes(text: "\u{1B}\u{1B}") == Data())
        // One pasted control character takes the same path: no raw ESC.
        #expect(KeyEncoder.bytes(text: "\u{1B}") == Data())
        #expect(KeyEncoder.bytes(text: "\u{3}") == Data("\u{1B}[200~\u{3}\u{1B}[201~".utf8))
    }

    @Test func testCtrlC() {
        #expect(KeyEncoder.bytes(.ctrl("c")) == Data([0x03]))
        #expect(KeyEncoder.bytes(.ctrl("C")) == Data([0x03]))
        #expect(KeyEncoder.bytes(.ctrl("d")) == Data([0x04]))
        #expect(KeyEncoder.bytes(.ctrl("ü")) == Data())
        // Only letters and the classic @ [ \ ] ^ _ ? have control codes.
        #expect(KeyEncoder.bytes(.ctrl("1")) == Data())
        #expect(KeyEncoder.bytes(.ctrl(" ")) == Data([0x00]))
        #expect(KeyEncoder.bytes(.ctrl("[")) == Data([0x1B]))
        #expect(KeyEncoder.bytes(.ctrl("?")) == Data([0x7F]))
        #expect(KeyEncoder.bytes(.ctrl("@")) == Data([0x00]))
    }
}
