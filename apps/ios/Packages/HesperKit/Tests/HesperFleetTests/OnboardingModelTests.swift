import Foundation
import HesperCore
import HesperFixtures
import HesperFleet
import Testing

@MainActor
@Suite struct OnboardingModelTests {
    let defaults = UserDefaults(suiteName: "OnboardingModelTests.\(UUID())")!
    let relay = URL(string: "https://relay.example.com")!

    func model(_ script: FixturePairing.Script = .init(), pairing: FixturePairing? = nil) -> OnboardingModel {
        OnboardingModel(pairing: pairing ?? FixturePairing(script), defaults: defaults, poll: .milliseconds(1))
    }

    func script(_ edit: (inout FixturePairing.Script) -> Void) -> FixturePairing.Script {
        var s = FixturePairing.Script()
        edit(&s)
        return s
    }

    /// Never shown: the fixture does not present a page.
    let present: SignInPresenter = { page, _, _ in page }

    @Test func aFreshInstallStartsAtHello() async {
        let m = model()
        await m.resume()
        #expect(m.step == .hello)
        #expect(m.mode == .live)
    }

    @Test func theScannedPathEndsInTheApp() async {
        let m = model()
        await m.resume()
        m.connectMac()
        #expect(m.step == .scan)
        #expect(m.scanned("not a code") == .ignored)
        #expect(m.step == .scan)
        guard case .found = m.scanned(FixturePairing.sampleCode()) else {
            Issue.record("the sample code was not found")
            return
        }
        #expect(m.step == .signIn)
        #expect(m.relay == relay)
        m.signIn(present: present)
        await m.settled()
        #expect(m.account?.login == "jdoe")
        #expect(m.step == .connected)
        #expect(m.approval == .approved(rights: ["observe", "answer", "type", "transfer"]))
        #expect(m.mac?.name == "studio")
        m.notificationsAnswered()
        #expect(m.step == .language)
        m.offerLanguages(["de-DE", "en-US"])
        #expect(m.language == "de-DE")
        m.language = "en-US"
        m.finish()
        #expect(m.isFinished)
        #expect(defaults.string(forKey: OnboardingModel.languageKey) == "en-US")
    }

    @Test func anExpiredCodeSaysSoAndKeepsScanning() async {
        let m = model()
        await m.resume()
        m.connectMac()
        let old = FixturePairing.sampleCode(now: .now.addingTimeInterval(-600))
        #expect(m.scanned(old) == .expired)
        #expect(m.step == .scan)
        #expect(m.scanNotice == "This code has expired. Your Mac shows a new one.")
    }

    @Test func theAddressPathPicksAMacAndWaitsForItsApproval() async {
        let pairing = FixturePairing(script { $0.pairMachine = .pending(approvedAfter: 2) })
        let m = model(pairing: pairing)
        await m.resume()
        m.connectMac()
        m.enterAddress()
        #expect(!m.submitAddress("not an address"))
        #expect(m.submitAddress("relay.example.com"))
        #expect(m.relay == relay)
        m.signIn(present: present)
        await m.settled()
        #expect(m.step == .pickMac)
        #expect(m.unpaired.map(\.short) == ["mini", "air"])
        m.choose(m.unpaired[0])
        await m.settled()
        #expect(m.step == .connected)
        #expect(m.mac == PairedMac(machine: "mini", name: "mini", rights: ["observe", "answer", "type", "transfer"]))
        #expect(m.pending == nil)
    }

    @Test(arguments: [
        (FixturePairing.SignIn.notAllowed, PairingError.notAllowed(login: "jdoe")),
        (.unreachable, .relayUnreachable(host: "relay.example.com")),
    ])
    func aFailedSignInStaysOnSignIn(_ outcome: FixturePairing.SignIn, _ error: PairingError) async {
        let m = model(script { $0.signIn = outcome })
        await m.resume()
        m.scanned(FixturePairing.sampleCode())
        m.signIn(present: present)
        await m.settled()
        #expect(m.step == .signIn)
        #expect(m.failure == error)
        // The scanned code is kept: nothing starts over.
        #expect(m.offer?.name == "studio")
    }

    @Test func aCancelledSheetSaysNothing() async {
        let m = model(script { $0.signIn = .cancelled })
        await m.resume()
        m.scanned(FixturePairing.sampleCode())
        m.signIn(present: present)
        await m.settled()
        #expect(m.step == .signIn)
        #expect(m.failure == nil)
    }

    @Test(arguments: [
        (FixturePairing.Pair.denied, PairingError.denied(machine: "studio")),
        (.keyChanged, .keyChanged(machine: "studio")),
    ])
    func aMacThatSaysNoIsShownOnConnected(_ outcome: FixturePairing.Pair, _ error: PairingError) async {
        let m = model(script { $0.pairOffer = outcome })
        await m.resume()
        m.scanned(FixturePairing.sampleCode())
        m.signIn(present: present)
        await m.settled()
        #expect(m.step == .connected)
        #expect(m.approval == .failed(error))
        m.scanAgain()
        #expect(m.step == .scan)
        #expect(m.offer == nil)
    }

    @Test func anOlderMacFallsBackToTheCode() async {
        let m = model(script { $0.pairOffer = .unsupported })
        await m.resume()
        m.scanned(FixturePairing.sampleCode())
        m.signIn(present: present)
        await m.settled()
        // Pending once, then approved on the next ask.
        #expect(m.step == .connected)
        #expect(m.mac?.machine == "studio")
    }

    @Test func aSleepingMacCanBeLeftForLater() async {
        let m = OnboardingModel(
            pairing: FixturePairing(script { $0.pairOffer = .offline }), defaults: defaults, poll: .seconds(60))
        await m.resume()
        m.scanned(FixturePairing.sampleCode())
        m.signIn(present: present)
        for _ in 0..<100 where m.approval != .failed(.macOffline(machine: "studio")) {
            try? await Task.sleep(for: .milliseconds(5))
        }
        #expect(m.approval == .failed(.macOffline(machine: "studio")))
        m.notificationsAnswered()
        #expect(m.step == .language)
        #expect(m.mac == nil)
    }

    @Test func aRelaunchResumesAtTheFirstStepNotDone() async {
        let pairing = FixturePairing()
        let first = model(pairing: pairing)
        await first.resume()
        first.scanned(FixturePairing.sampleCode())
        first.signIn(present: present)
        await first.settled()
        #expect(first.step == .connected)

        // Killed on Connected: the approval is remembered.
        let second = model(pairing: pairing)
        await second.resume()
        #expect(second.step == .connected)
        #expect(second.approval == .approved(rights: ["observe", "answer", "type", "transfer"]))
        second.notificationsAnswered()

        let third = model(pairing: pairing)
        await third.resume()
        #expect(third.step == .language)
        third.offerLanguages([])
        #expect(third.isFinished)

        let fourth = model(pairing: pairing)
        await fourth.resume()
        #expect(fourth.isFinished)
    }

    @Test func signedInButNotPairedResumesAtPickAMac() async {
        let pairing = FixturePairing(account: RelayAccount(relay: relay, deviceID: "DEVICE_ID", login: "jdoe"))
        let m = model(pairing: pairing)
        await m.resume()
        await m.settled()
        #expect(m.step == .pickMac)
        #expect(m.unpaired.count == 2)
    }

    @Test func aScanForTheSignedInRelayGoesStraightToTheMac() async {
        let pairing = FixturePairing(account: RelayAccount(relay: relay, deviceID: "DEVICE_ID", login: "jdoe"))
        let m = model(pairing: pairing)
        await m.resume()
        m.scanAgain()
        m.scanned(FixturePairing.sampleCode())
        await m.settled()
        #expect(m.step == .connected)
        #expect(m.mac?.machine == "studio")
    }

    @Test func sampleMacsSkipEverythingAndStay() async {
        let m = model()
        await m.resume()
        m.lookAround()
        #expect(m.isFinished)
        #expect(m.mode == .samples)

        let again = model()
        await again.resume()
        #expect(again.mode == .samples)
        await again.connectYours()
        #expect(again.mode == .live)
        #expect(again.step == .scan)
    }

    @Test func resetStartsOverAtHello() async {
        let pairing = FixturePairing()
        let m = model(pairing: pairing)
        await m.resume()
        m.scanned(FixturePairing.sampleCode())
        m.signIn(present: present)
        await m.settled()
        m.notificationsAnswered()
        m.offerLanguages(["de-DE"])
        m.finish()
        await m.reset()
        #expect(m.step == .hello)
        #expect(pairing.resets == 1)
        #expect(defaults.string(forKey: OnboardingModel.languageKey) == nil)
        let fresh = model(pairing: pairing)
        await fresh.resume()
        #expect(fresh.step == .hello)
    }

    @Test func thePushTokenGoesToTheRelayOnlyWhenLive() async {
        let pairing = FixturePairing()
        let m = model(pairing: pairing)
        await m.resume()
        m.lookAround()
        await m.registerPushToken(Data([1]))
        #expect(pairing.pushToken == nil)
        await m.connectYours()
        await m.registerPushToken(Data([2]))
        #expect(pairing.pushToken == Data([2]))
    }
}

@MainActor
@Suite struct AddMacModelTests {
    let relay = URL(string: "https://relay.example.com")!

    func pairing(_ edit: (inout FixturePairing.Script) -> Void = { _ in }) -> FixturePairing {
        var s = FixturePairing.Script()
        edit(&s)
        return FixturePairing(s, account: RelayAccount(relay: relay, deviceID: "DEVICE_ID", login: "jdoe"))
    }

    @Test func listsTheUnpairedMacsAndWaitsForTheApproval() async {
        let m = AddMacModel(pairing: pairing { $0.pairMachine = .pending(approvedAfter: 2) }, poll: .milliseconds(1))
        await m.load()
        #expect(m.unpaired.map(\.short) == ["mini", "air"])
        #expect(m.row("air") == .idle)
        await m.pair("air")
        #expect(m.row("air") == .approved(rights: ["observe", "answer", "type", "transfer"]))
        await m.load()
        #expect(m.unpaired.map(\.short) == ["mini"])
    }

    @Test func showsTheCodeWhileItWaits() async {
        let m = AddMacModel(pairing: pairing { $0.pairMachine = .pending(approvedAfter: 1000) }, poll: .seconds(60))
        let task = Task { await m.pair("air") }
        for _ in 0..<100 where m.row("air") == .idle || m.row("air") == .asking {
            try? await Task.sleep(for: .milliseconds(5))
        }
        guard case .waiting(let code, _) = m.row("air") else {
            Issue.record("not waiting: \(m.row("air"))")
            return
        }
        #expect(code == "ABC-DEF")
        m.stop()
        await task.value
    }

    @Test func anExpiredRequestSaysSo() async {
        let m = AddMacModel(
            pairing: pairing { $0.pairMachine = .pending(approvedAfter: 1000) }, poll: .milliseconds(1),
            now: { .now.addingTimeInterval(3600) })
        await m.pair("mini")
        #expect(m.row("mini") == .failed(.offerExpired))
    }

    @Test func aScannedCodeApprovesAtOnce() async throws {
        let m = AddMacModel(pairing: pairing())
        #expect(await m.scanned("hello") == nil)
        let result = try #require(await m.scanned(FixturePairing.sampleCode()))
        #expect(result == .success(.approved(machine: "studio", rights: ["observe", "answer", "type", "transfer"])))
        #expect(m.row("studio") == .approved(rights: ["observe", "answer", "type", "transfer"]))
    }
}

@Suite struct OnboardingCopyTests {
    @Test func addresses() {
        #expect(RelayAddress.parse("relay.example.com")?.absoluteString == "https://relay.example.com")
        #expect(RelayAddress.parse(" https://relay.example.com:8443/v1/x ")?.absoluteString == "https://relay.example.com:8443")
        #expect(RelayAddress.parse("hesper relay") == nil)
        #expect(RelayAddress.parse("ftp://relay.example.com") == nil)
        #expect(RelayAddress.parse("relay") == nil)
    }

    @Test func privateHostsAskAboutTheVPN() {
        #expect(PairingError.relayUnreachable(host: "relay.example.com").message() == "Can't reach relay.example.com.")
        #expect(
            PairingError.relayUnreachable(host: "relay.tail1234.ts.net").message() == "Can't reach relay.tail1234.ts.net. Is your VPN on?")
        #expect(RelayAddress.isPrivate("192.168.1.20"))
        #expect(RelayAddress.isPrivate("100.101.1.2"))
        #expect(!RelayAddress.isPrivate("8.8.8.8"))
        #expect(PairingError.cancelled.message() == nil)
    }

    @Test func rightsInWords() {
        #expect(RightsCopy.summary(["observe", "answer", "type", "transfer"]) == "Can answer, type and start agents · no shells")
        #expect(RightsCopy.summary(["observe", "answer", "type", "transfer", "shell"]) == "Can answer, type and start agents and shells")
        #expect(RightsCopy.summary(["observe", "answer"]) == "Can answer agents · no shells")
        #expect(RightsCopy.summary(["observe"]) == "Can watch agents · no shells")
    }

    @Test func theNudgeNamesTheAgentThatWaitedLongest() {
        let away = Date(timeIntervalSince1970: 1000)
        let now = away.addingTimeInterval(20 * 60)
        let before = Agent(id: "a", machine: "mini", name: "old", state: .approval, stateSince: away.addingTimeInterval(-60))
        let first = Agent(id: "b", machine: "studio", name: "relay-tests", state: .approval, stateSince: away.addingTimeInterval(8 * 60))
        let later = Agent(id: "c", machine: "studio", name: "api-docs", state: .question, stateSince: away.addingTimeInterval(15 * 60))
        let got = NotificationsNudge.waited(needsYou: [before, later, first], awaySince: away, now: now)
        #expect(got?.agent.name == "relay-tests")
        #expect(got?.minutes == 12)
        #expect(NotificationsNudge.waited(needsYou: [before], awaySince: away, now: now) == nil)
        #expect(NotificationsNudge.waited(needsYou: [first], awaySince: nil, now: now) == nil)
    }
}
