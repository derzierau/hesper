import Foundation

// The Loops sheet's verbs as pure changes: what D, P, X, space, A, T and
// ⌘↩ send (loops.save / loops.item), how a typed step or trigger reads,
// and the words the detail shows for steps (G2: owner, trigger, the
// roll-up line). The sheet sends them; hesperd decides who acts next.

public enum LoopActions {
    /// A park lasts until tomorrow, 09:00 local (the wrap-up's too).
    public static func parkUntil(now: Date = Date(), calendar: Calendar = .current) -> Date {
        let start = calendar.startOfDay(for: now)
        let tomorrow = calendar.date(byAdding: .day, value: 1, to: start) ?? now
        return calendar.date(bySettingHour: 9, minute: 0, second: 0, of: tomorrow) ?? tomorrow
    }

    public static func done(_ id: String) -> LoopPatch { LoopPatch(id: id, status: .done) }
    public static func drop(_ id: String) -> LoopPatch { LoopPatch(id: id, status: .dropped) }
    /// P on a parked loop takes it back (open); else parks it until tomorrow.
    public static func park(_ l: Loop, now: Date = Date(), calendar: Calendar = .current) -> LoopPatch {
        l.status == .parked ? LoopPatch(id: l.id, status: .open) : LoopPatch(id: l.id, status: .parked, parkedUntil: parkUntil(now: now, calendar: calendar))
    }

    /// Space: an open item is ticked off, a done one opens again.
    public static func check(_ item: LoopItem) -> ItemPatch {
        ItemPatch(id: item.id, state: item.state == .open ? .done : .open)
    }

    /// A typed step: "@agent …" or "@others …" names its owner, else it is
    /// the person's. nil: nothing but an owner.
    public static func step(_ text: String, order: Double) -> ItemPatch? {
        var t = text.trimmingCharacters(in: .whitespacesAndNewlines)
        var owner = ItemOwner.you
        for (word, o) in [("@agent", ItemOwner.agent), ("@agents", .agent), ("@others", .others), ("@you", .you), ("@me", .you)] {
            if t.lowercased() == word { return nil }
            if t.lowercased().hasPrefix(word + " ") {
                owner = o
                t = String(t.dropFirst(word.count)).trimmingCharacters(in: .whitespaces)
                break
            }
        }
        guard !t.isEmpty else { return nil }
        return ItemPatch(text: t, owner: owner, kind: .step, order: order)
    }

    /// The order after the loop's last item.
    public static func nextOrder(_ l: Loop) -> Double { (l.items.map(\.order).max() ?? 0) + 1 }

    /// A typed trigger for `item`: "after 2" (the step numbered 2 in the
    /// detail; "after 2 3" several), "pr <url>", "url <url>", "tag <name>",
    /// "review <id>", "loop <id>", or a bare URL (a PR when it has /pull/).
    /// "none" or "" clears what it waits on. nil: not a trigger.
    public static func trigger(_ text: String, item: LoopItem, steps: [LoopItem]) -> ItemPatch? {
        let t = text.trimmingCharacters(in: .whitespacesAndNewlines)
        let words = t.split(whereSeparator: \.isWhitespace).map(String.init)
        guard let first = words.first?.lowercased() else { return ItemPatch(id: item.id, after: []) }
        if first == "none" || first == "clear" { return ItemPatch(id: item.id, after: []) }
        if first == "after" {
            let ids = words.dropFirst().compactMap { w -> String? in
                guard let n = Int(w.trimmingCharacters(in: CharacterSet(charactersIn: "#,"))), n >= 1, n <= steps.count else { return nil }
                let id = steps[n - 1].id
                return id == item.id ? nil : id
            }
            return ids.isEmpty ? nil : ItemPatch(id: item.id, after: ids)
        }
        if let kind = ItemCheckKind(rawValue: first), words.count >= 2 {
            return ItemPatch(id: item.id, check: ItemCheck(kind: kind.rawValue, ref: words.dropFirst().joined(separator: " ")))
        }
        if words.count == 1, let u = URL(string: t), let scheme = u.scheme, scheme.hasPrefix("http") {
            return ItemPatch(id: item.id, check: ItemCheck(kind: t.contains("/pull/") ? ItemCheckKind.pr.rawValue : ItemCheckKind.url.rawValue, ref: t))
        }
        return nil
    }

    /// The trigger column: "after 1, 3", "PR merged", "URL answers", …;
    /// nil: ready now (nothing to wait on).
    public static func triggerText(_ item: LoopItem, steps: [LoopItem]) -> String? {
        if !item.after.isEmpty {
            let nums = item.after.compactMap { id in steps.firstIndex { $0.id == id }.map { String($0 + 1) } }
            return nums.isEmpty ? "after a removed step" : "after " + nums.joined(separator: ", ")
        }
        guard let c = item.check else { return nil }
        switch c.knownKind {
        case .pr: return "PR merged"
        case .tag: return "tag \(c.ref)"
        case .url: return "URL answers"
        case .review: return "review accepted"
        case .loop: return "loop done"
        case nil: return c.kind
        }
    }

    /// What the trigger field starts with for `item` (its trigger as typed).
    public static func triggerField(_ item: LoopItem, steps: [LoopItem]) -> String {
        if !item.after.isEmpty {
            return "after " + item.after.compactMap { id in steps.firstIndex { $0.id == id }.map { String($0 + 1) } }.joined(separator: " ")
        }
        if let c = item.check { return "\(c.kind) \(c.ref)" }
        return ""
    }

    /// The steps the detail lists and numbers: every item but offers and
    /// dropped ones, in order.
    public static func steps(_ l: Loop) -> [LoopItem] { l.items.filter { $0.kind != .offer && $0.state != .dropped } }

    /// The roll-up under the steps: "2 of 5 done · 2 yours · next: Pick the
    /// provider"; nil without steps.
    public static func rollUp(_ l: Loop) -> String? {
        let s = steps(l)
        guard !s.isEmpty else { return nil }
        let done = s.filter { $0.state == .done }.count
        var parts = ["\(done) of \(s.count) done"]
        let yours = s.filter(\.isYourMove).count
        if yours > 0 { parts.append(yours == 1 ? "1 yours" : "\(yours) yours") }
        let waiting = s.filter { $0.isOpen && !$0.ready }.count
        if waiting > 0 { parts.append("\(waiting) waiting") }
        if let next = s.first(where: { $0.isOpen && $0.ready }) { parts.append("next: \(next.text)") }
        return parts.joined(separator: " · ")
    }

    /// The drafted next agent: the first open, ready item with a draft.
    public static func draftedNext(_ l: Loop) -> LoopItem? { l.items.first { $0.isOpen && $0.ready && $0.draft != nil } }

    /// Loops `l` can merge into: the other active loops, a query matching
    /// their title (case-insensitive words, all of them), most recent first.
    public static func mergeTargets(for l: Loop, in loops: some Sequence<Loop>, query: String) -> [Loop] {
        let words = query.lowercased().split(whereSeparator: \.isWhitespace)
        return loops.filter { o in
            guard o.id != l.id, o.isActive else { return false }
            let t = o.displayTitle.lowercased()
            return words.allSatisfy { t.contains($0) }
        }.sorted { a, b in a.lastChange != b.lastChange ? a.lastChange > b.lastChange : a.id < b.id }
    }
}
