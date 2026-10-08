import Foundation

// Drag & drop onto terminals (docs: "As built — drop to attach"). The
// pure rules: what a drop carries, how a path is written into an agent's
// input, where non-file payloads are saved, and the limits of an upload
// to another machine. The AppKit side (UI/TerminalDrop.swift) reads the
// pasteboard into `DropPayload`s and delivers the text as a bracketed
// paste (agents.input paste:true, submit:false): the agent's own input
// turns pasted image and file paths into attachments.

/// One thing in a drop.
public enum DropPayload: Equatable, Sendable {
    /// A file or folder that exists on this Mac (Finder, file promises
    /// once received).
    case file(path: String, isDirectory: Bool)
    /// Image bytes without a file (a browser image, a screenshot
    /// thumbnail, Preview): saved as PNG first.
    case image(data: Data, name: String)
    /// Text or a web URL: pasted as it is.
    case text(String)

    public var isText: Bool { if case .text = self { return true } else { return false } }
}

/// What the pasteboard offers, by type identifier (UTIs), in the order a
/// drop is tried. Pure: the app maps NSPasteboard types to these.
public enum DropKind: Equatable, Sendable {
    case files, filePromise, imageData, url, text, unsupported
}

public enum DropClassifier {
    public static let fileURL = "public.file-url"
    public static let promiseTypes = ["com.apple.NSFilePromiseItemMetaData", "com.apple.pasteboard.promised-file-url",
                                      "Apple files promise pasteboard type", "com.apple.pasteboard.promised-file-content-type"]
    public static let imageTypes = ["public.png", "public.tiff", "public.heic", "public.heif", "public.jpeg",
                                    "com.compuserve.gif", "org.webmproject.webp", "NeXT TIFF v4.0 pasteboard type", "Apple PNG pasteboard type"]
    public static let urlTypes = ["public.url", "NSURLPboardType"]
    public static let textTypes = ["public.utf8-plain-text", "public.plain-text", "NSStringPboardType", "public.utf16-plain-text"]

    /// The best way to read a drop that offers `types`: real files first
    /// (Finder, several files, folders), then file promises (Photos,
    /// screenshot thumbnails, Mail), then image bytes (browsers, Preview),
    /// then a URL or text. A browser image offers its bytes and its web
    /// URL: the bytes win.
    public static func classify(_ types: [String]) -> DropKind {
        let set = Set(types)
        if set.contains(fileURL) { return .files }
        if !set.isDisjoint(with: promiseTypes) { return .filePromise }
        if !set.isDisjoint(with: imageTypes) { return .imageData }
        if !set.isDisjoint(with: urlTypes) { return .url }
        if !set.isDisjoint(with: textTypes) { return .text }
        return .unsupported
    }

    /// Every type a terminal registers for.
    public static var registeredTypes: [String] { [fileURL] + promiseTypes + imageTypes + urlTypes + textTypes }
}

/// Paths written into an agent's input the way a terminal pastes a
/// dropped file (Ghostty, iTerm: backslash escapes), so Claude Code and
/// Codex recognize them.
public enum ShellEscape {
    /// Characters a shell would split or interpret; each gets a backslash
    /// (Ghostty's set for dropped files).
    static let special: Set<Character> = [" ", "\t", "\\", "'", "\"", "`", "$", "!", "#", "&", ";", "|", "*", "?",
                                          "(", ")", "[", "]", "{", "}", "<", ">"]

    /// `/Users/me/My File.png` → `/Users/me/My\ File.png`. Unicode stays as
    /// it is. A path with a newline or another control character cannot be
    /// backslash-escaped (backslash-newline is a line continuation): it is
    /// written ANSI-C quoted, `$'a\nb'`.
    public static func quote(_ path: String) -> String {
        if path.unicodeScalars.contains(where: { $0.value < 0x20 || $0.value == 0x7f }) {
            var out = "$'"
            for s in path.unicodeScalars {
                switch s {
                case "\n": out += "\\n"
                case "\t": out += "\\t"
                case "\r": out += "\\r"
                case "\\": out += "\\\\"
                case "'": out += "\\'"
                default:
                    if s.value < 0x20 || s.value == 0x7f { out += String(format: "\\x%02x", s.value) } else { out.unicodeScalars.append(s) }
                }
            }
            return out + "'"
        }
        var out = ""
        for c in path {
            if special.contains(c) { out.append("\\") }
            out.append(c)
        }
        return out
    }

    /// Paths escaped and space-separated (a draft's task, a single paste).
    public static func joined(_ paths: [String]) -> String {
        paths.map(quote).joined(separator: " ")
    }
}

/// How a drop reaches the agent: a sequence of bracketed pastes. Found
/// with the real CLIs (Claude Code 2.1.292, Codex 0.160.1; contract "As
/// built — drop to attach"): both turn a pasted PNG/JPEG path (escaped or
/// quoted) into `[Image #N]`, Claude only on a bracketed paste; Codex only
/// when the paste holds exactly one path; HEIC is not recognized by
/// either (the app converts it to PNG first); other files and folders stay
/// plain paths (`@path` adds nothing in either); both add the space after
/// `[Image #N]` themselves. So: one paste per image, images first, then
/// the other paths in one paste, then dropped text; every paste after the
/// first starts with a space, none ends with one.
public enum DropPlan {
    /// Image files the agents attach as they are.
    public static let attachableImageExtensions: Set<String> = ["png", "jpg", "jpeg"]
    /// Image files the app converts to PNG before pasting.
    public static let convertedImageExtensions: Set<String> = ["heic", "heif", "tiff", "tif", "gif", "webp", "bmp"]

    public static func isAttachableImage(_ path: String) -> Bool {
        attachableImageExtensions.contains((path as NSString).pathExtension.lowercased())
    }

    public static func needsConversion(_ path: String) -> Bool {
        convertedImageExtensions.contains((path as NSString).pathExtension.lowercased())
    }

    /// The pastes for `paths` (after conversion) and dropped `texts`.
    public static func pastes(paths: [String], texts: [String] = []) -> [String] {
        let images = paths.filter(isAttachableImage)
        let others = paths.filter { !isAttachableImage($0) }
        var out = images.map(ShellEscape.quote)
        if !others.isEmpty { out.append(ShellEscape.joined(others)) }
        out += texts.filter { !$0.isEmpty }
        return out.enumerated().map { $0.offset == 0 ? $0.element : " " + $0.element }
    }
}

/// Names and places of saved payloads.
public enum AttachmentNaming {
    /// The folder of one agent's (or draft's) saved payloads below the
    /// attachments root: `L/a7f3k2` → `L-a7f3k2`.
    public static func folder(_ id: String) -> String {
        let s = id.map { $0.isLetter || $0.isNumber || $0 == "-" || $0 == "_" ? $0 : "-" }
        let out = String(s)
        return out.isEmpty ? "unknown" : out
    }

    /// `<yyyyMMdd-HHmmss>-<name>.<ext>`: `name` without its extension,
    /// sanitized (no separators, no leading dot, ≤ 60 characters).
    public static func fileName(_ name: String, ext: String, at date: Date) -> String {
        let f = DateFormatter()
        f.locale = Locale(identifier: "en_US_POSIX")
        f.timeZone = TimeZone.current
        f.dateFormat = "yyyyMMdd-HHmmss"
        var stem = (name as NSString).deletingPathExtension
        stem = String(stem.unicodeScalars.filter { $0.value >= 0x20 && $0 != "/" && $0 != ":" && $0.value != 0x7f }.map(Character.init))
            .trimmingCharacters(in: .whitespaces)
        while stem.hasPrefix(".") { stem.removeFirst() }
        if stem.count > 60 { stem = String(stem.prefix(60)) }
        if stem.isEmpty { stem = "image" }
        return "\(f.string(from: date))-\(stem).\(ext)"
    }

    /// A name not taken in `existing`: `x.png`, `x-2.png`, `x-3.png` …
    public static func unique(_ name: String, existing: (String) -> Bool) -> String {
        guard existing(name) else { return name }
        let ns = name as NSString
        let stem = ns.deletingPathExtension, ext = ns.pathExtension
        var n = 2
        while true {
            let c = ext.isEmpty ? "\(stem)-\(n)" : "\(stem)-\(n).\(ext)"
            if !existing(c) { return c }
            n += 1
        }
    }
}

/// Uploading a file to another machine's agent (files.put / files.chunk).
public enum AttachmentUpload {
    /// hesperd's cap per file.
    public static let maxBytes: Int64 = 50 * 1024 * 1024
    /// hesperd's chunk size (the last chunk may be shorter).
    public static let chunkSize = 512 * 1024
    /// Uploads at least this large show their progress on the tile.
    public static let progressThreshold: Int64 = 1024 * 1024

    /// Chunk ranges of a file of `size` bytes (an empty file: one empty,
    /// last chunk).
    public static func chunks(size: Int64, chunk: Int = chunkSize) -> [(offset: Int64, length: Int, last: Bool)] {
        if size <= 0 { return [(0, 0, true)] }
        var out: [(Int64, Int, Bool)] = []
        var off: Int64 = 0
        while off < size {
            let n = Int(min(Int64(chunk), size - off))
            out.append((off, n, off + Int64(n) >= size))
            off += Int64(n)
        }
        return out
    }

    /// Why a payload cannot go to another machine, or nil.
    public static func refusal(name: String, size: Int64, isDirectory: Bool) -> String? {
        if isDirectory { return "Folders can't be sent to another machine (\(name))" }
        if size > maxBytes { return "\(name) is larger than 50 MB" }
        return nil
    }

    /// "1.4 MB" style sizes for the progress label.
    public static func megabytes(_ n: Int64) -> String {
        String(format: "%.1f MB", Double(n) / 1_048_576)
    }
}
