import Foundation
import HesperCore

/// Which languages capture listens in: the owner's own languages (keyboard
/// dictation languages, then preferred languages) matched to the locales a
/// voice model supports. The phone's region is not a language: English
/// with region Germany is still English.
public enum SpeechLanguages {
    /// Each preference matched to a supported locale, best first, without
    /// repeats, at most `limit`. Identifiers are BCP 47 ("de-DE").
    ///
    /// A preference matches its exact locale when it is supported;
    /// otherwise its language in the language's main region ("de" and
    /// "de-LU" → "de-DE", "en-DE" → "en-US"); otherwise the first supported
    /// locale of that language.
    public static func candidates(preferred: [String], supported: [String], limit: Int) -> [String] {
        let supported = supported.map(normalized)
        var out: [String] = []
        for p in preferred.map(normalized) where out.count < limit {
            guard let match = match(p, in: supported), !out.contains(match) else { continue }
            out.append(match)
        }
        return out
    }

    /// The language in its own name, for the language screen: "Deutsch",
    /// "English (US)". The region shows only where it is not the
    /// language's own (de-DE is just German, de-CH is not).
    public static func displayName(_ id: String) -> String {
        let id = normalized(id)
        let locale = Locale(identifier: id)
        let language = language(id)
        guard let name = locale.localizedString(forLanguageCode: language)?.capitalized(with: locale) else { return id }
        let parts = id.split(separator: "-")
        guard parts.count > 1, let region = parts.last.map(String.init), region.count == 2, region != language.uppercased() else {
            return name
        }
        return "\(name) (\(region))"
    }

    static func match(_ id: String, in supported: [String]) -> String? {
        if supported.contains(id) { return id }
        let language = language(id)
        guard !language.isEmpty else { return nil }
        let main = "\(language)-\(mainRegion[language] ?? language.uppercased())"
        if supported.contains(main) { return main }
        return supported.first { Self.language($0) == language }
    }

    /// "de_DE" → "de-DE"; keyboard modes such as "emoji" pass through and
    /// match nothing.
    static func normalized(_ id: String) -> String {
        id.replacingOccurrences(of: "_", with: "-")
    }

    static func language(_ id: String) -> String {
        String(normalized(id).split(separator: "-").first ?? "").lowercased()
    }

    /// Where a language's main region is not its own code in capitals.
    static let mainRegion: [String: String] = [
        "en": "US", "ja": "JP", "ko": "KR", "zh": "CN", "yue": "CN", "sv": "SE", "da": "DK", "cs": "CZ",
        "el": "GR", "he": "IL", "uk": "UA", "vi": "VN", "nb": "NO", "hi": "IN", "ar": "SA", "ca": "ES",
        "ms": "MY", "pt": "BR",
    ]
}

extension Fleet {
    /// Words the voice model should expect: the Macs', projects' and
    /// agents' names, which are rarely dictionary words.
    public var speechHints: [String] {
        var words: [String] = []
        for m in macList { words.append(MachineLabel.name(m.machine)) }
        for g in groups {
            if let p = g.project { words.append(p) }
            words += g.agents.map(\.name)
        }
        var seen = Set<String>()
        return words.filter { !$0.isEmpty && seen.insert($0).inserted }
    }
}
