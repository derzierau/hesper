import HesperDesign
import HesperFleet
import Speech
import SwiftUI
import UIKit

/// 5 · Your language: the dictation languages this phone can offer, each
/// in its own name, and Hold to try with the app's own dictation (the
/// microphone alert comes on the first hold). Done saves it and starts the
/// voice model's download; nothing waits for it.
struct LanguageScreen: View {
    @Environment(OnboardingModel.self) private var model
    @State private var dictation = Dictation()
    @State private var holding = false

    var body: some View {
        @Bindable var model = model
        OnboardingPage {
            OnboardingTitle(title: "Which language do you speak to Hesper in?")
            VStack(spacing: 0) {
                ForEach(model.languages, id: \.self) { id in
                    Button {
                        model.language = id
                    } label: {
                        HStack(spacing: DS.Spacing.l) {
                            Image(systemName: model.language == id ? "largecircle.fill.circle" : "circle")
                                .font(.title3)
                                .foregroundStyle(model.language == id ? Theme.accent : Theme.dim)
                            Text(SpeechLanguages.displayName(id)).font(.body)
                            Spacer()
                        }
                        .padding(DS.Spacing.l)
                        .contentShape(.rect)
                    }
                    .buttonStyle(.plain)
                    .accessibilityAddTraits(model.language == id ? .isSelected : [])
                    if id != model.languages.last { Divider().overlay(Theme.line) }
                }
            }
            .background(Theme.tile, in: .rect(cornerRadius: DS.Spacing.xl, style: .continuous))
            tryIt
        } footer: {
            WideButton("Done") {
                dictation.cancel()
                if let language = model.language { Dictation.download(language) }
                model.finish()
            }
            Text("Hesper listens on this iPhone. Nothing you say leaves it until you send it.")
                .font(.footnote)
                .foregroundStyle(Theme.dim)
                .multilineTextAlignment(.center)
        }
        .task { model.offerLanguages(await Self.candidates()) }
        .onChange(of: model.language, initial: true) { _, language in
            dictation.chosen = language
            dictation.prepare()
        }
    }

    /// Hold to try: the words show live in the picked language.
    private var tryIt: some View {
        VStack(spacing: DS.Spacing.l) {
            Group {
                if case .unavailable(let why) = dictation.phase {
                    Text(why).foregroundStyle(Theme.dim)
                } else if dictation.settled.isEmpty && dictation.guess.isEmpty {
                    Text(holding ? "Listening…" : " ").foregroundStyle(Theme.dim)
                } else {
                    Text("\(dictation.settled)\(Text(dictation.guess).foregroundStyle(Theme.dim))")
                }
            }
            .font(.body)
            .multilineTextAlignment(.center)
            .frame(maxWidth: .infinity, minHeight: 48)
            Image(systemName: "mic.fill")
                .font(.title)
                .foregroundStyle(Theme.background)
                .frame(width: 64, height: 64)
                .glassEffect(.regular.tint(Theme.accent).interactive(), in: .circle)
                .scaleEffect(holding ? 1.12 : 1)
                .animation(.snappy, value: holding)
                .gesture(
                    DragGesture(minimumDistance: 0)
                        .onChanged { _ in
                            guard !holding else { return }
                            holding = true
                            dictation.start()
                        }
                        .onEnded { _ in
                            holding = false
                            Task { _ = await dictation.stop() }
                        }
                )
                .sensoryFeedback(.start, trigger: holding) { _, down in down }
                .accessibilityLabel("Hold to try")
                .accessibilityAddTraits(.isButton)
            Text("Hold to try").font(.footnote.weight(.medium)).foregroundStyle(Theme.text2)
        }
        .padding(.top, DS.Spacing.l)
    }

    /// The keyboard's dictation languages, then the preferred languages,
    /// matched to what the voice model supports.
    static func candidates() async -> [String] {
        guard SpeechTranscriber.isAvailable else { return [] }
        let supported = await SpeechTranscriber.supportedLocales.map { $0.identifier(.bcp47) }
        let keyboards = UITextInputMode.activeInputModes.compactMap(\.primaryLanguage)
        return SpeechLanguages.candidates(preferred: keyboards + Locale.preferredLanguages, supported: supported, limit: 4)
    }
}

#Preview {
    LanguageScreen().onboardingPreview(.language)
}
