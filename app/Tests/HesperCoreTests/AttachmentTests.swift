import Foundation
import Testing
@testable import HesperCore

@Suite struct PathEscaping {
    @Test func plainPathsStayAsTheyAre() {
        #expect(ShellEscape.quote("/Users/me/shot.png") == "/Users/me/shot.png")
        #expect(ShellEscape.quote("/tmp/a-b_c.d/e,f+g@h%i:j") == "/tmp/a-b_c.d/e,f+g@h%i:j")
    }

    @Test func spacesAndShellCharactersGetBackslashes() {
        #expect(ShellEscape.quote("/Users/me/My File.png") == "/Users/me/My\\ File.png")
        #expect(ShellEscape.quote("/x/drop me (1).txt") == "/x/drop\\ me\\ \\(1\\).txt")
        #expect(ShellEscape.quote("/x/a&b;c|d*e?f") == "/x/a\\&b\\;c\\|d\\*e\\?f")
        #expect(ShellEscape.quote("/x/$HOME!#`") == "/x/\\$HOME\\!\\#\\`")
        #expect(ShellEscape.quote("/x/[a]{b}<c>") == "/x/\\[a\\]\\{b\\}\\<c\\>")
        #expect(ShellEscape.quote("/x/back\\slash") == "/x/back\\\\slash")
    }

    @Test func quotes() {
        #expect(ShellEscape.quote("/x/it's \"quoted\".png") == "/x/it\\'s\\ \\\"quoted\\\".png")
    }

    @Test func unicodeIsKept() {
        #expect(ShellEscape.quote("/x/Bildschirmfoto 2026-10-07 um 10.00.00 ä.png") == "/x/Bildschirmfoto\\ 2026-10-07\\ um\\ 10.00.00\\ ä.png")
        #expect(ShellEscape.quote("/x/日本語.png") == "/x/日本語.png")
        #expect(ShellEscape.quote("/x/👍.png") == "/x/👍.png")
    }

    @Test func newlinesAndControlCharactersAreAnsiCQuoted() {
        #expect(ShellEscape.quote("/x/a\nb.png") == "$'/x/a\\nb.png'")
        #expect(ShellEscape.quote("/x/it's\tok") == "$'/x/it\\'s\\tok'")
        #expect(ShellEscape.quote("/x/\u{1}") == "$'/x/\\x01'")
        #expect(ShellEscape.quote("/x/a b\nc") == "$'/x/a b\\nc'")
    }

    @Test func joined() {
        #expect(ShellEscape.joined(["/a b", "/c"]) == "/a\\ b /c")
    }
}

@Suite struct DropPlanning {
    @Test func oneImagePerPasteImagesFirstThenOtherPathsThenText() {
        let p = DropPlan.pastes(paths: ["/x/notes with space.txt", "/x/a.png", "/x/b.JPG", "/x/dir"], texts: ["hello"])
        #expect(p == ["/x/a.png", " /x/b.JPG", " /x/notes\\ with\\ space.txt /x/dir", " hello"])
    }

    @Test func singleFileHasNoLeadingOrTrailingSpace() {
        #expect(DropPlan.pastes(paths: ["/x/a b.png"]) == ["/x/a\\ b.png"])
        #expect(DropPlan.pastes(paths: [], texts: ["https://example.com"]) == ["https://example.com"])
        #expect(DropPlan.pastes(paths: []).isEmpty)
    }

    @Test func formatsTheAgentsDoNotReadAreConverted() {
        #expect(DropPlan.needsConversion("/x/a.HEIC"))
        #expect(DropPlan.needsConversion("/x/a.tiff"))
        #expect(!DropPlan.needsConversion("/x/a.png"))
        #expect(!DropPlan.needsConversion("/x/a.txt"))
        #expect(DropPlan.isAttachableImage("/x/a.jpeg"))
        #expect(!DropPlan.isAttachableImage("/x/a.heic"))
    }
}

@Suite struct DropClassification {
    @Test func filesWinOverEverything() {
        #expect(DropClassifier.classify(["public.file-url", "public.png", "public.utf8-plain-text"]) == .files)
    }

    @Test func promisesBeforeImageBytes() {
        #expect(DropClassifier.classify(["com.apple.NSFilePromiseItemMetaData", "public.png"]) == .filePromise)
        #expect(DropClassifier.classify(["com.apple.pasteboard.promised-file-url"]) == .filePromise)
    }

    @Test func browserImageBytesBeatItsURL() {
        #expect(DropClassifier.classify(["public.url", "public.tiff", "public.utf8-plain-text"]) == .imageData)
        #expect(DropClassifier.classify(["public.heic"]) == .imageData)
        #expect(DropClassifier.classify(["public.jpeg"]) == .imageData)
    }

    @Test func urlsAndText() {
        #expect(DropClassifier.classify(["public.url", "public.utf8-plain-text"]) == .url)
        #expect(DropClassifier.classify(["public.utf8-plain-text"]) == .text)
        #expect(DropClassifier.classify(["NSStringPboardType"]) == .text)
    }

    @Test func unknownIsUnsupported() {
        #expect(DropClassifier.classify(["com.example.unknown"]) == .unsupported)
        #expect(DropClassifier.classify([]) == .unsupported)
    }
}

@Suite struct AttachmentNames {
    @Test func timestampedNames() {
        let d = Date(timeIntervalSince1970: 0)
        let name = AttachmentNaming.fileName("Bildschirmfoto ä.heic", ext: "png", at: d)
        #expect(name.hasSuffix("-Bildschirmfoto ä.png"))
        #expect(name.count == "19700101-000000-Bildschirmfoto ä.png".count)
        #expect(AttachmentNaming.fileName("..hidden/x:y.txt", ext: "png", at: d).hasSuffix("-hiddenxy.png"))
        #expect(!AttachmentNaming.fileName("..hidden", ext: "png", at: d).contains("-.."))
        #expect(AttachmentNaming.fileName("", ext: "png", at: d).hasSuffix("-image.png"))
        #expect(!AttachmentNaming.fileName("a\nb/c", ext: "png", at: d).contains("\n"))
    }

    @Test func folders() {
        #expect(AttachmentNaming.folder("L/a7f3k2") == "L-a7f3k2")
        #expect(AttachmentNaming.folder("d-abc") == "d-abc")
    }

    @Test func uniqueNames() {
        let taken: Set = ["x.png", "x-2.png"]
        #expect(AttachmentNaming.unique("x.png") { taken.contains($0) } == "x-3.png")
        #expect(AttachmentNaming.unique("y.png") { taken.contains($0) } == "y.png")
    }

    @Test func uploadChunksAndLimits() {
        let c = AttachmentUpload.chunks(size: 1_300_000)
        #expect(c.count == 3)
        #expect(c.map(\.offset) == [0, 524_288, 1_048_576])
        #expect(c.last?.length == 1_300_000 - 1_048_576 && c.last?.last == true && c[0].last == false)
        #expect(AttachmentUpload.chunks(size: 0).count == 1)
        #expect(AttachmentUpload.chunks(size: 524_288).count == 1)
        #expect(AttachmentUpload.refusal(name: "big.mov", size: 60 << 20, isDirectory: false) == "big.mov is larger than 50 MB")
        #expect(AttachmentUpload.refusal(name: "dir", size: 0, isDirectory: true) != nil)
        #expect(AttachmentUpload.refusal(name: "a.png", size: 1000, isDirectory: false) == nil)
    }
}
