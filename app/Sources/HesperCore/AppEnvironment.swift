import Foundation

/// Where the app finds the daemon. Overrides (for tests and development):
///
///   --socket PATH      or HESPER_SOCKET
///   --state-dir DIR    or HESPER_STATE_DIR      (socket = DIR/hesperd.sock)
///   --hesperd PATH     or HESPERD_BIN           (the binary run as `attach`)
///
/// Default: ~/.local/state/hesper/hesperd.sock and `hesperd` next to the app
/// executable, in ~/.local/bin, /opt/homebrew/bin, /usr/local/bin, then PATH.
public struct AppEnvironment: Equatable, Sendable {
    public var socketPath: String
    public var stateDir: String?
    public var hesperdPath: String
    public var options: Set<String>
    public var values: [String: String]

    public static func resolve(arguments: [String] = CommandLine.arguments,
                               environment: [String: String] = ProcessInfo.processInfo.environment,
                               home: String = NSHomeDirectory(),
                               executableDir: String? = Bundle.main.executableURL?.deletingLastPathComponent().path,
                               fileExists: (String) -> Bool = { FileManager.default.isExecutableFile(atPath: $0) }) -> AppEnvironment {
        var values: [String: String] = [:]
        var options = Set<String>()
        var i = 1
        while i < arguments.count {
            let a = arguments[i]
            if a.hasPrefix("--") {
                let name = String(a.dropFirst(2))
                if let eq = name.firstIndex(of: "=") {
                    values[String(name[..<eq])] = String(name[name.index(after: eq)...])
                } else if i + 1 < arguments.count, !arguments[i + 1].hasPrefix("--"), Self.valued.contains(name) {
                    values[name] = arguments[i + 1]
                    i += 1
                } else {
                    options.insert(name)
                }
            }
            i += 1
        }
        let stateDir = values["state-dir"] ?? environment["HESPER_STATE_DIR"]
        let socket = values["socket"] ?? environment["HESPER_SOCKET"]
            ?? (stateDir.map { $0 + "/hesperd.sock" } ?? home + "/.local/state/hesper/hesperd.sock")
        var bin = values["hesperd"] ?? environment["HESPERD_BIN"]
        if bin == nil {
            var candidates: [String] = []
            if let executableDir { candidates.append(executableDir + "/hesperd") }
            candidates += [home + "/.local/bin/hesperd", "/opt/homebrew/bin/hesperd", "/usr/local/bin/hesperd"]
            for dir in (environment["PATH"] ?? "").split(separator: ":") { candidates.append(dir + "/hesperd") }
            bin = candidates.first(where: fileExists)
        }
        return AppEnvironment(socketPath: socket, stateDir: stateDir, hesperdPath: bin ?? "hesperd", options: options, values: values)
    }

    static let valued: Set<String> = ["socket", "state-dir", "hesperd", "perf-seconds", "perf-out", "selftest-out", "perf-agents", "layout-out", "layout-hold", "layout-show", "window-size", "arrangement", "min-chars", "selftest-phase", "layout-type", "tile-font",
                                        // window layer
                                        "window-state", "windows-test-out", "windows-phase", "windows-shots", "walls-perf-out", "perf-walls",
                                        "selftest-shots", "projects-test-out", "projects-shots",
                                        // desks
                                        "fake-screens", "desks-test-out", "desks-shots", "desks-phase",
                                        // shared history
                                        "history-test-out", "history-shots", "history-phase", "history-perf-out", "perf-history"]

    /// The command a surface runs to show an agent (contract: `hesperd attach`).
    /// `view`: a read-only window onto the agent's last rows that fit the
    /// terminal (follows its size), as wall tiles use.
    public func attachArgv(id: String, readOnly: Bool, owner: Bool, view: Bool = false, fit: Bool = false) -> [String] {
        var argv = [hesperdPath, "attach", id]
        // --fit implies --view and asks the PTY (while no owner is
        // attached) for at least this tile's size.
        if fit { return argv + ["--fit"] }
        if view { return argv + ["--view"] }
        if readOnly { argv.append("--ro") }
        if owner && !readOnly { argv.append("--owner") }
        return argv
    }

    /// Environment for attach processes so `hesperd attach` reaches the same
    /// daemon the app talks to.
    public var attachEnvironment: [String: String] {
        var env = ["HESPER_SOCKET": socketPath]
        if let stateDir { env["HESPER_STATE_DIR"] = stateDir }
        return env
    }
}
