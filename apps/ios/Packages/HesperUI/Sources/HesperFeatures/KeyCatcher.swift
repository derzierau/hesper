import HesperDesign
import HesperSurfaces
import SwiftUI
import UIKit

/// The live section's keyboard: an invisible first responder that hands
/// typed text and keys to the agent, with a key bar above the keyboard
/// (Esc, Tab, a sticky Ctrl, the arrows) in the raw terminal's colours.
struct KeyCatcher: UIViewRepresentable {
    /// The keyboard is up; a dismissal by the system turns it false.
    @Binding var active: Bool
    /// One `insertText` at a time: `ScreenWatch.send(text:)` sends a
    /// single character as is and more at once as a bracketed paste.
    let text: (String) -> Void
    let key: (HesperSurfaces.KeyPress) -> Void

    func makeUIView(context: Context) -> KeyInputView { KeyInputView() }

    func updateUIView(_ view: KeyInputView, context: Context) {
        view.onText = text
        view.onKey = key
        let active = $active
        view.onResign = { if active.wrappedValue { active.wrappedValue = false } }
        if self.active, !view.isFirstResponder {
            // Refused (no window yet, or another responder holds on): the
            // binding must not claim a keyboard that is not up. Changed
            // after this update, as SwiftUI asks.
            if !view.becomeFirstResponder() {
                Task { @MainActor in active.wrappedValue = false }
            }
        } else if !self.active, view.isFirstResponder {
            view.resignFirstResponder()
        }
    }
}

final class KeyInputView: UIView, UIKeyInput {
    var onText: (String) -> Void = { _ in }
    var onKey: (HesperSurfaces.KeyPress) -> Void = { _ in }
    var onResign: () -> Void = {}

    /// The key bar's height.
    /// A touch target's height, as every control on the phone.
    private static let barHeight = DS.Size.control
    private lazy var bar: UIView = makeBar()
    private var ctrlButton: UIButton?
    /// Ctrl is held for the next key.
    private var ctrl = false {
        didSet { paintCtrl() }
    }

    override var canBecomeFirstResponder: Bool { true }
    override var inputAccessoryView: UIView? { bar }

    // A terminal wants the keys as typed.
    var autocorrectionType: UITextAutocorrectionType = .no
    var autocapitalizationType: UITextAutocapitalizationType = .none
    var spellCheckingType: UITextSpellCheckingType = .no
    var smartQuotesType: UITextSmartQuotesType = .no
    var smartDashesType: UITextSmartDashesType = .no
    var smartInsertDeleteType: UITextSmartInsertDeleteType = .no
    var inlinePredictionType: UITextInlinePredictionType = .no
    var keyboardAppearance: UIKeyboardAppearance = .dark

    /// Always true, so Backspace reaches the agent on an empty field.
    var hasText: Bool { true }

    func insertText(_ text: String) {
        if text == "\n" {
            onKey(.enter)
        } else if ctrl, text.count == 1, let c = text.first {
            ctrl = false
            onKey(.ctrl(c))
        } else {
            onText(text)
        }
    }

    func deleteBackward() { onKey(.backspace) }

    @discardableResult override func resignFirstResponder() -> Bool {
        let done = super.resignFirstResponder()
        if done { onResign() }
        return done
    }

    private func makeBar() -> UIView {
        let bar = UIView(frame: CGRect(x: 0, y: 0, width: 0, height: Self.barHeight))
        bar.autoresizingMask = .flexibleWidth
        bar.backgroundColor = Theme.uiColor(.background)
        let stack = UIStackView()
        stack.axis = .horizontal
        stack.distribution = .fillEqually
        stack.spacing = DS.Spacing.xs
        stack.translatesAutoresizingMaskIntoConstraints = false
        let keys: [(String, HesperSurfaces.KeyPress?)] = [
            ("Esc", .escape), ("Tab", .tab), ("Ctrl", nil), ("↑", .up), ("↓", .down), ("←", .left), ("→", .right),
        ]
        for (title, key) in keys {
            var config = UIButton.Configuration.filled()
            config.title = title
            config.baseBackgroundColor = Theme.uiColor(.surface)
            config.baseForegroundColor = Theme.uiColor(.text)
            config.background.cornerRadius = DS.Radius.console
            let button = UIButton(
                configuration: config,
                primaryAction: UIAction { [weak self] _ in
                    guard let self else { return }
                    if let key {
                        self.ctrl = false
                        self.onKey(key)
                    } else {
                        self.ctrl.toggle()
                    }
                })
            button.accessibilityLabel = Self.spoken[title] ?? title
            if key == nil { ctrlButton = button }
            stack.addArrangedSubview(button)
        }
        bar.addSubview(stack)
        NSLayoutConstraint.activate([
            stack.leadingAnchor.constraint(equalTo: bar.leadingAnchor, constant: DS.Spacing.s),
            stack.trailingAnchor.constraint(equalTo: bar.trailingAnchor, constant: -DS.Spacing.s),
            stack.topAnchor.constraint(equalTo: bar.topAnchor, constant: DS.Spacing.xs),
            stack.bottomAnchor.constraint(equalTo: bar.bottomAnchor, constant: -DS.Spacing.xs),
        ])
        return bar
    }

    /// The arrows' names for VoiceOver.
    private static let spoken = ["↑": "Up", "↓": "Down", "←": "Left", "→": "Right"]

    private func paintCtrl() {
        guard let ctrlButton, var config = ctrlButton.configuration else { return }
        config.baseBackgroundColor = Theme.uiColor(ctrl ? .accent : .surface)
        config.baseForegroundColor = Theme.uiColor(ctrl ? .onAccent : .text)
        ctrlButton.configuration = config
    }
}
