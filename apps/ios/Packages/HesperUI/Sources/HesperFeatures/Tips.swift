import TipKit

/// The one tip after onboarding, on Speak, shown once.
struct MicTip: Tip {
    var title: Text { Text("Say a task") }
    var message: Text? { Text("Tap Speak or hold it to talk. Then start it on a Mac or keep it for later.") }
    var options: [any TipOption] { [MaxDisplayCount(1)] }
}
