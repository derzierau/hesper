import TipKit

/// The one tip after onboarding, on the mic circle, shown once.
struct MicTip: Tip {
    var title: Text { Text("Say a task") }
    var message: Text? { Text("Hold, speak, let go. Then start it on a Mac or keep it for later.") }
    var options: [any TipOption] { [MaxDisplayCount(1)] }
}
