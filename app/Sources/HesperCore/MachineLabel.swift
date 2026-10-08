import Foundation

/// The one name the app shows for a machine: its short name from
/// machines.json ("laptop", "mini"), not the relay device name (a host
/// name such as "ABC123456" or "XYZ987654 Mac mini"). Falls back to the
/// device name only when there is no real short name (a one-letter legacy
/// short such as "M"), and to the short name when the device name is a
/// host name ("My-MacBook-Pro.local").
public enum MachineLabel {
    /// A device name that reads as a host name, not a chosen name.
    public static func isHostName(_ name: String) -> Bool {
        name.isEmpty || name.contains(".") || name.count > 20
    }

    public static func name(short: String, name: String) -> String {
        if isHostName(name) { return short.isEmpty ? name : short }
        if short.count > 1 { return short }
        return name.isEmpty ? short : name
    }

    public static func name(_ m: Machine) -> String { name(short: m.short, name: m.name) }

    /// The label of the machine with this short id; the id itself when the
    /// machine is unknown.
    public static func name(short: String, in machines: [Machine]) -> String {
        machines.first { $0.short == short }.map(name) ?? short
    }

    /// short → label, for formatters that take a name table (history rows,
    /// the palette, session cards).
    public static func names(_ machines: [Machine]) -> [String: String] {
        Dictionary(machines.map { ($0.short, name($0)) }, uniquingKeysWith: { a, _ in a })
    }
}
