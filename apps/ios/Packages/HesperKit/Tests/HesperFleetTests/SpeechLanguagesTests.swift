import HesperFixtures
import HesperFleet
import Testing

@Suite struct SpeechLanguagesTests {
    let supported = ["de-AT", "de-CH", "de-DE", "en-AU", "en-GB", "en-US", "fr-FR", "pt-BR", "pt-PT"]

    @Test func exactLocalesWin() {
        #expect(SpeechLanguages.candidates(preferred: ["de-CH", "en-GB"], supported: supported, limit: 5) == ["de-CH", "en-GB"])
    }

    @Test func aRegionIsNotALanguage() {
        // English on a phone in Germany is English, in its main region; a
        // bare "de" is German German, not the first listed (de-AT).
        #expect(SpeechLanguages.candidates(preferred: ["en-DE", "de"], supported: supported, limit: 5) == ["en-US", "de-DE"])
        #expect(SpeechLanguages.candidates(preferred: ["de-LU"], supported: supported, limit: 5) == ["de-DE"])
        #expect(SpeechLanguages.candidates(preferred: ["pt"], supported: supported, limit: 5) == ["pt-BR"])
    }

    @Test func keyboardModesAndUnsupportedLanguagesAreSkipped() {
        #expect(SpeechLanguages.candidates(preferred: ["emoji", "nl-NL", "fr_FR"], supported: supported, limit: 5) == ["fr-FR"])
    }

    @Test func noRepeatsAndAtMostTheLimit() {
        let got = SpeechLanguages.candidates(preferred: ["de-DE", "de", "en-US", "fr-FR"], supported: supported, limit: 2)
        #expect(got == ["de-DE", "en-US"])
    }

    @Test func eachLanguageInItsOwnName() {
        #expect(SpeechLanguages.displayName("de-DE") == "Deutsch")
        #expect(SpeechLanguages.displayName("en-US") == "English (US)")
        #expect(SpeechLanguages.displayName("de-CH") == "Deutsch (CH)")
        #expect(SpeechLanguages.displayName("fr_FR") == "Français")
    }

    @Test func fleetWordsAreHints() {
        let hints = Samples.fleet.speechHints
        #expect(hints.prefix(3) == ["air", "mini", "studio"])
        #expect(hints.contains("hesper"))
        #expect(hints.contains("relay-tests"))
        #expect(Set(hints).count == hints.count)
    }
}
