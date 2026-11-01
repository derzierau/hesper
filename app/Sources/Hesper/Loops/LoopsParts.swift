import AppKit
import HesperCore
import SwiftUI

/// The sheets' inline error: one line under the chrome, `error` toned.
struct LoopsErrorLine: View {
    var text: String

    var body: some View {
        HStack(spacing: DS.Spacing.s) {
            StateMark(.error)
            Text(text).font(DS.font(.chrome)).foregroundStyle(Theme.fg).lineLimit(2)
            Spacer(minLength: 0)
        }
        .padding(.horizontal, DS.Spacing.l)
        .padding(.vertical, DS.Spacing.s)
        .background(Color(nsColor: Theme.ns(.error, alpha: DS.Alpha.banner)))
        .accessibilityIdentifier("loops.error")
    }
}

/// An empty state: what will appear here and how it gets there.
struct LoopsEmpty: View {
    var symbol: String
    var title: String
    var detail: String

    var body: some View {
        VStack(spacing: DS.Spacing.s) {
            Image(systemName: symbol).font(.system(size: IconButton.side)).foregroundStyle(Theme.dim)
            Text(title).font(DS.font(.panelTitle, .medium)).foregroundStyle(Theme.fg2)
            Text(detail).font(DS.font(.chrome)).foregroundStyle(Theme.dim).multilineTextAlignment(.center)
                .frame(maxWidth: LoopsLook.pickerWidth)
        }
        .padding(DS.Spacing.xxl)
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("loops.empty")
    }
}

/// The Steward's line under the board: off (and how to turn it on), or
/// its level, where it runs and how busy it was today.
struct StewardLine: View {
    var steward: StewardStatus

    var body: some View {
        VStack(spacing: 0) {
            Rectangle().fill(Theme.stroke).frame(height: 1)
            HStack(spacing: DS.Spacing.s) {
                StateMark(steward.enabled ? (steward.agent != nil ? .working : .idle) : .idle)
                Text(LoopSheetWords.steward(steward)).font(DS.font(.chrome)).foregroundStyle(Theme.dim).lineLimit(1)
                Spacer(minLength: 0)
            }
            .padding(.horizontal, DS.Spacing.l)
            .frame(height: DS.chromeMaxHeight)
        }
        .accessibilityIdentifier("loops.steward")
    }
}

/// The text field the sheets type into (a step, a trigger, a filter):
/// focused when it appears; ⏎ and esc stay with the sheet's keys.
struct LoopsField: View {
    var prompt: String
    @Binding var text: String
    @FocusState private var focused: Bool

    var body: some View {
        TextField(prompt, text: $text)
            .textFieldStyle(.plain)
            .font(DS.font(.body))
            .focused($focused)
            .padding(.horizontal, DS.Spacing.m)
            .frame(height: IconButton.side)
            .background(DS.Radius.shape(DS.Radius.control).fill(Theme.color(.background)))
            .overlay(DS.Radius.shape(DS.Radius.control).strokeBorder(Theme.accent, lineWidth: DS.hairline))
            .onAppear { focused = true }
            .accessibilityIdentifier("loops.field")
    }
}

/// Words that need the app (machine names) or are only the sheets'.
enum LoopSheetWords {
    static func steward(_ s: StewardStatus, now: Date = Date()) -> String {
        guard s.enabled else { return "Steward is off · Settings › Steward turns it on to keep these loops up to date" }
        let level = StewardSettings.levels.first { $0.level == s.level }?.title ?? s.level.rawValue.capitalized
        var parts = ["Steward · \(level)"]
        if let m = s.machine { parts.append("on \(m)") }
        parts.append(s.wakes == 1 ? "1 wake today" : "\(s.wakes) wakes today")
        if let w = s.lastWake { parts.append("last \(LoopWords.short(now.timeIntervalSince(w))) ago") }
        if s.queued > 0 { parts.append("\(s.queued) waiting") }
        if s.agent == nil { parts.append("not running") }
        return parts.joined(separator: " · ")
    }
}
