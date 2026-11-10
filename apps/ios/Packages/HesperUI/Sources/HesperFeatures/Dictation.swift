import AVFoundation
import HesperFleet
import Observation
import Speech
import Synchronization
import UIKit

/// On-device dictation for capture (concept, flow 3). `settled` is text
/// that will not change; `guess` is the model's current guess, shown
/// dimmed. Both stream while the owner speaks.
///
/// Kept fast in three ways: the model reports fast, early guesses; the next
/// capture's model is loaded (and downloaded, the first time) before the
/// mic is touched; and the microphone runs before the model is ready, its
/// audio waiting in the analyzer's input, so the first word is not lost.
@Observable
final class Dictation {
    enum Phase: Equatable {
        case idle
        case starting
        case listening(since: Date)
        /// No microphone, no model for this language, or a failure; the
        /// capture falls back to typing.
        case unavailable(String)
    }

    private(set) var phase = Phase.idle
    private(set) var settled = ""
    private(set) var guess = ""
    /// The language it listens in (BCP 47): the one chosen in onboarding
    /// (`languageKey`), else the owner's first language the model knows.
    private(set) var language: String?
    /// Words to expect (Mac, project and agent names), for the model.
    var hints: [String] = []
    /// The language picked on onboarding's language screen, before Done
    /// saves it; tried first.
    var chosen: String?

    /// Onboarding stores the owner's dictation language here.
    static let languageKey = "dictationLanguage"

    private var starting: Task<Void, Never>?
    private var engine: AVAudioEngine?
    private var input: AsyncStream<AnalyzerInput>.Continuation?
    private var analyzer: SpeechAnalyzer?
    private var results: Task<Void, Never>?
    /// The next capture's model, loaded and waiting.
    private var prepared: Prepared?
    private var preparing: Task<Void, Never>?

    var isListening: Bool {
        switch phase {
        case .starting, .listening: true
        case .idle, .unavailable: false
        }
    }

    /// Finds the language and loads its model for the next capture,
    /// downloading it the first time. Cheap when it is already loaded;
    /// failures stay quiet (the capture reports them).
    func prepare() {
        guard preparing == nil else { return }
        preparing = Task {
            defer { preparing = nil }
            await findLanguage()
            guard let language, prepared?.language != language else { return }
            prepared = try? await Prepared.load(language: language)
            if prepared != nil { _ = try? await AssetInventory.reserve(locale: Locale(identifier: language)) }
        }
    }

    /// The onboarding choice, else keyboard dictation languages first,
    /// then preferred languages, matched to what the model supports.
    private func findLanguage() async {
        guard SpeechTranscriber.isAvailable else { return }
        let supported = await SpeechTranscriber.supportedLocales.map { $0.identifier(.bcp47) }
        let chosen = [self.chosen, UserDefaults.standard.string(forKey: Self.languageKey)].compactMap(\.self)
        let keyboards = UITextInputMode.activeInputModes.compactMap(\.primaryLanguage)
        language =
            SpeechLanguages.candidates(preferred: chosen + keyboards + Locale.preferredLanguages, supported: supported, limit: 1).first
    }

    /// Downloads the voice model for `language` in the background, so the
    /// first capture does not wait; failures stay quiet (capture retries).
    static func download(_ language: String) {
        Task {
            guard let locale = await SpeechTranscriber.supportedLocale(equivalentTo: Locale(identifier: language)) else { return }
            let module = SpeechTranscriber(locale: locale, preset: .transcription)
            try? await AssetInventory.assetInstallationRequest(supporting: [module])?.downloadAndInstall()
        }
    }

    /// Starts listening; returns at once. `stop()` waits for the start.
    func start() {
        guard !isListening else { return }
        settled = ""
        guess = ""
        phase = .starting
        starting = Task { await setUp() }
    }

    /// Stops listening, waits for the last words and returns everything
    /// said.
    func stop() async -> String {
        await starting?.value
        guard case .listening = phase else { return "" }
        stopAudio()
        try? await analyzer?.finalizeAndFinishThroughEndOfInput()
        await results?.value
        let text = (settled + guess).trimmingCharacters(in: .whitespacesAndNewlines)
        tearDown()
        phase = .idle
        prepare()
        return text
    }

    #if DEBUG
        /// Listening without the microphone, with `said` heard so far: the
        /// `HESPER_CAPTURE=listening` screenshot.
        func pretend(_ said: String) {
            settled = said
            guess = ""
            phase = .listening(since: .now)
        }
    #endif

    /// Stops without waiting; what was said is dropped.
    func cancel() {
        starting?.cancel()
        stopAudio()
        results?.cancel()
        tearDown()
        if isListening {
            phase = .idle
            prepare()
        }
    }

    private func setUp() async {
        do {
            guard await AVAudioApplication.requestRecordPermission() else {
                throw Failure("Hesper can't use the microphone. Allow it in Settings, or type.")
            }
            // A new pick on the language screen: find its model first.
            if let chosen, chosen != language {
                await preparing?.value
                prepare()
            }
            if language == nil { prepare() }
            await preparing?.value
            guard let language else { throw Failure("Dictation isn't available in your language. Type instead.") }
            let ready: Prepared
            if let prepared, prepared.language == language {
                ready = prepared
            } else {
                ready = try await Prepared.load(language: language)
            }
            prepared = nil
            let context = AnalysisContext()
            context.contextualStrings[.general] = hints
            try? await ready.analyzer.setContext(context)
            try Task.checkCancellation()

            let session = AVAudioSession.sharedInstance()
            try session.setCategory(.record, mode: .measurement)
            try session.setActive(true, options: .notifyOthersOnDeactivation)
            let (stream, input) = AsyncStream<AnalyzerInput>.makeStream()
            let engine = AVAudioEngine()
            try AudioTap.install(on: engine, converting: ready.format, into: input)
            engine.prepare()
            try engine.start()
            self.engine = engine
            self.input = input

            try await ready.analyzer.start(inputSequence: stream)
            analyzer = ready.analyzer
            try Task.checkCancellation()
            let transcriber = ready.module
            results = Task { [weak self] in
                do {
                    for try await result in transcriber.results {
                        let text = String(result.text.characters)
                        if result.isFinal {
                            self?.settled += text
                            self?.guess = ""
                        } else {
                            self?.guess = text
                        }
                    }
                } catch {
                    // The analyzer ended; what settled so far is kept.
                }
            }
            phase = .listening(since: .now)
        } catch is CancellationError {
            stopAudio()
            await analyzer?.cancelAndFinishNow()
            tearDown()
        } catch {
            stopAudio()
            tearDown()
            phase = .unavailable((error as? Failure)?.message ?? "Dictation stopped: \(error.localizedDescription) Type instead.")
        }
    }

    /// The microphone off and the analyzer's input closed.
    private func stopAudio() {
        engine?.stop()
        engine?.inputNode.removeTap(onBus: 0)
        input?.finish()
    }

    private func tearDown() {
        starting = nil
        engine = nil
        input = nil
        analyzer = nil
        results = nil
        try? AVAudioSession.sharedInstance().setActive(false, options: .notifyOthersOnDeactivation)
    }

    struct Failure: Error {
        var message: String
        init(_ message: String) { self.message = message }
    }
}

/// A model with its analyzer, loaded and not started: one capture's worth.
private struct Prepared {
    var language: String
    var module: SpeechTranscriber
    var analyzer: SpeechAnalyzer
    var format: AVAudioFormat

    /// Keeps the model in memory between captures; the user is waiting.
    static let options = SpeechAnalyzer.Options(priority: .userInitiated, modelRetention: .processLifetime)

    static func load(language: String) async throws -> Prepared {
        guard SpeechTranscriber.isAvailable,
            let locale = await SpeechTranscriber.supportedLocale(equivalentTo: Locale(identifier: language))
        else {
            throw Dictation.Failure("Dictation isn't available in this language. Type instead.")
        }
        // Fast, early guesses: the words show while they are said.
        let module = SpeechTranscriber(
            locale: locale, transcriptionOptions: [], reportingOptions: [.volatileResults, .fastResults], attributeOptions: [])
        guard let format = await SpeechAnalyzer.bestAvailableAudioFormat(compatibleWith: [module]) else {
            throw Dictation.Failure("Dictation isn't available on this device. Type instead.")
        }
        if let install = try await AssetInventory.assetInstallationRequest(supporting: [module]) {
            try await install.downloadAndInstall()
        }
        let analyzer = SpeechAnalyzer(modules: [module], options: options)
        try await analyzer.prepareToAnalyze(in: format)
        return Prepared(language: language, module: module, analyzer: analyzer, format: format)
    }
}

/// The microphone tap. Core Audio calls it on its own thread, so nothing
/// here is main-actor.
nonisolated enum AudioTap {
    static func install(
        on engine: AVAudioEngine, converting format: AVAudioFormat, into input: AsyncStream<AnalyzerInput>.Continuation
    ) throws {
        let node = engine.inputNode
        let source = node.outputFormat(forBus: 0)
        guard let converter = Converter(from: source, to: format) else {
            throw Dictation.Failure("Dictation can't read this microphone. Type instead.")
        }
        node.installTap(onBus: 0, bufferSize: 4096, format: source) { buffer, _ in
            if let converted = converter.convert(buffer) { input.yield(AnalyzerInput(buffer: converted)) }
        }
    }
}

/// Resamples the microphone's buffers to the analyzer's format.
// @unchecked Sendable: only the tap's thread uses it, one buffer at a time.
nonisolated final class Converter: @unchecked Sendable {
    private let converter: AVAudioConverter
    private let format: AVAudioFormat

    init?(from source: AVAudioFormat, to format: AVAudioFormat) {
        guard let converter = AVAudioConverter(from: source, to: format) else { return nil }
        self.converter = converter
        self.format = format
    }

    func convert(_ buffer: AVAudioPCMBuffer) -> AVAudioPCMBuffer? {
        if buffer.format == format { return buffer }
        let ratio = format.sampleRate / buffer.format.sampleRate
        let capacity = AVAudioFrameCount((Double(buffer.frameLength) * ratio).rounded(.up))
        guard let out = AVAudioPCMBuffer(pcmFormat: format, frameCapacity: capacity) else { return nil }
        let input = InputOnce(buffer)
        var error: NSError?
        converter.convert(to: out, error: &error) { _, status in
            guard let buffer = input.take() else {
                status.pointee = .noDataNow
                return nil
            }
            status.pointee = .haveData
            return buffer
        }
        return error == nil ? out : nil
    }
}

/// One buffer supplied to one synchronous converter call.
// @unchecked Sendable: the mutex makes taking the buffer atomic; the
// converter reads the returned buffer before convert(to:) returns.
nonisolated private final class InputOnce: @unchecked Sendable {
    private let buffer: AVAudioPCMBuffer
    private let taken = Mutex(false)

    init(_ buffer: AVAudioPCMBuffer) { self.buffer = buffer }

    func take() -> AVAudioPCMBuffer? {
        taken.withLock { taken in
            guard !taken else { return nil }
            taken = true
            return buffer
        }
    }
}
