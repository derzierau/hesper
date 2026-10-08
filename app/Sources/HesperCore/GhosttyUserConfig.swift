import Foundation

/// Reads font settings from the user's Ghostty config, read-only. Follows
/// `config-file` includes (optional ones prefixed with `?`) like Ghostty does.
public struct GhosttyUserConfig: Equatable, Sendable {
    public var fontFamilies: [String] = []
    public var fontSize: Double?
    public var fontThicken: Bool?
    public var fontFeatures: [String] = []

    public init() {}

    /// Default locations, in Ghostty's load order.
    public static func defaultPaths(home: String = NSHomeDirectory(), xdg: String? = ProcessInfo.processInfo.environment["XDG_CONFIG_HOME"]) -> [String] {
        let base = xdg ?? (home + "/.config")
        return [base + "/ghostty/config", base + "/ghostty/config.ghostty",
                home + "/Library/Application Support/com.mitchellh.ghostty/config",
                home + "/Library/Application Support/com.mitchellh.ghostty/config.ghostty"]
    }

    public static func load(paths: [String] = defaultPaths(), read: (String) -> String? = { try? String(contentsOfFile: $0, encoding: .utf8) }) -> GhosttyUserConfig {
        var cfg = GhosttyUserConfig()
        var visited = Set<String>()
        for p in paths { cfg.include(path: p, read: read, visited: &visited, depth: 0) }
        return cfg
    }

    public mutating func include(path: String, read: (String) -> String?, visited: inout Set<String>, depth: Int) {
        let p = (path as NSString).expandingTildeInPath
        guard depth < 10, !visited.contains(p), let text = read(p) else { return }
        visited.insert(p)
        let dir = (p as NSString).deletingLastPathComponent
        var includes: [String] = []
        for raw in text.split(whereSeparator: \.isNewline) {
            let line = raw.trimmingCharacters(in: .whitespaces)
            guard !line.isEmpty, !line.hasPrefix("#"), let eq = line.firstIndex(of: "=") else { continue }
            let key = line[..<eq].trimmingCharacters(in: .whitespaces)
            var value = line[line.index(after: eq)...].trimmingCharacters(in: .whitespaces)
            if value.count >= 2, value.hasPrefix("\""), value.hasSuffix("\"") { value = String(value.dropFirst().dropLast()) }
            switch key {
            case "font-family":
                // An empty value resets the list, as in Ghostty.
                if value.isEmpty { fontFamilies = [] } else { fontFamilies.append(value) }
            case "font-size":
                if let d = Double(value) { fontSize = d }
            case "font-thicken":
                fontThicken = value == "true"
            case "font-feature":
                if value.isEmpty { fontFeatures = [] } else { fontFeatures.append(value) }
            case "config-file":
                includes.append(value)
            default:
                break
            }
        }
        // Ghostty loads config-file includes after the file that names them.
        for var inc in includes {
            if inc.hasPrefix("?") { inc.removeFirst() }
            if inc.hasPrefix("\""), inc.hasSuffix("\""), inc.count >= 2 { inc = String(inc.dropFirst().dropLast()) }
            let expanded = (inc as NSString).expandingTildeInPath
            let full = expanded.hasPrefix("/") ? expanded : dir + "/" + expanded
            include(path: full, read: read, visited: &visited, depth: depth + 1)
        }
    }
}

/// The libghostty config the app runs with: Tokyo Night, the user's font,
/// no padding (the app frames terminals itself), no shell integration (the
/// command is `hesperd attach`, not a shell).
public enum TerminalConfigText {
    public static func make(user: GhosttyUserConfig, scrollbackBytes: Int = 2_000_000) -> String {
        var lines: [String] = []
        for f in user.fontFamilies { lines.append("font-family = \(f)") }
        lines.append("font-size = \(format(user.fontSize ?? 13))")
        if let t = user.fontThicken { lines.append("font-thicken = \(t)") }
        for f in user.fontFeatures { lines.append("font-feature = \(f)") }
        lines += [
            "background = \(TokyoNight.background)",
            "foreground = \(TokyoNight.foreground)",
            "cursor-color = \(TokyoNight.foreground)",
            "selection-background = #283457",
            "selection-foreground = \(TokyoNight.foreground)",
        ]
        for (i, c) in TokyoNight.palette.enumerated() { lines.append("palette = \(i)=\(c)") }
        lines += [
            "faint-opacity = 0.6",
            "window-padding-x = 0",
            "window-padding-y = 0",
            "window-padding-balance = false",
            "window-padding-color = background",
            "shell-integration = none",
            "confirm-close-surface = false",
            "scrollback-limit = \(scrollbackBytes)",
            "macos-option-as-alt = true",
            "mouse-hide-while-typing = true",
            "clipboard-read = deny",
            "clipboard-write = allow",
            "copy-on-select = false",
            "cursor-style-blink = false",
            "resize-overlay = never",
            "link-url = true",
            "bell-features = no-system,no-audio",
            "keybind = clear",
            "keybind = super+c=copy_to_clipboard",
            "keybind = super+v=paste_from_clipboard",
        ]
        return lines.joined(separator: "\n") + "\n"
    }

    static func format(_ d: Double) -> String {
        d == d.rounded() ? String(Int(d)) : String(d)
    }
}

public enum TokyoNight {
    public static let background = "#1a1b26"
    public static let backgroundDark = "#16161e"
    public static let surface = "#1f2335"
    public static let border = "#292e42"
    public static let foreground = "#c0caf5"
    public static let dim = "#565f89"
    public static let comment = "#737aa2"
    public static let blue = "#7aa2f7"
    public static let green = "#9ece6a"
    public static let yellow = "#e0af68"
    public static let red = "#f7768e"
    public static let magenta = "#bb9af7"
    public static let cyan = "#7dcfff"
    public static let palette = [
        "#15161e", "#f7768e", "#9ece6a", "#e0af68", "#7aa2f7", "#bb9af7", "#7dcfff", "#a9b1d6",
        "#414868", "#f7768e", "#9ece6a", "#e0af68", "#7aa2f7", "#bb9af7", "#7dcfff", "#c0caf5",
    ]
}
