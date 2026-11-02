# Hesper for iPhone: onboarding

Status: concept, 2 Nov 2026. Wireframes in
[`wireframes/round-6.html`](wireframes/round-6.html). Part of the
[iPhone concept](concept.md); the pairing it builds on is in
[`relay/docs/protocol.md`](../../relay/docs/protocol.md) (GitHub enrollment,
device keys, `devices.request`, end-to-end channel).

## The idea

Onboarding has one job: **get the first Mac on the phone, with approvals
able to reach the lock screen.** Everything else waits until the owner
uses the feature that needs it. Target: useful within a minute, four
screens of Hesper's own plus two system sheets.

Three rules:

1. **Connect first, explain never.** The app is understood by using it:
   the Inbox with real agents teaches more than any slide. No tour, no
   feature carousel. Apple: "Ideally, people can understand your app …
   simply by experiencing it, but if onboarding is necessary, design a
   flow that's fast, fun, and optional"
   ([HIG onboarding](https://developer.apple.com/design/human-interface-guidelines/onboarding)).
2. **The Mac vouches for the phone.** The owner is at a Mac when they set
   up the phone, so the Mac shows a code and the phone scans it. Scanning
   proves the owner is in front of that Mac, which replaces typing a relay
   address and comparing `ABC-DEF` codes on two screens.
3. **Each permission at the moment it pays off.** Notifications right
   after the first Mac connects (its agents are what the notifications are
   about). Camera when the owner taps Scan. Microphone the first time they
   hold the mic circle. Face ID the first time something needs the strong
   key. "Avoid requesting permission at launch unless the data or resource
   is required for your app to function"
   ([HIG privacy](https://developer.apple.com/design/human-interface-guidelines/privacy#Requesting-permission));
   ask for notifications "in a context that helps people understand why"
   ([UserNotifications](https://developer.apple.com/documentation/usernotifications/asking-permission-to-use-notifications)).

The look follows the app icon: the "h." whose full stop is a state mark.
On the first screen the square is the accent (working) and turns to the
signal colour with the needs-you pulse: the one thing Hesper does, told in
the logo. Under Reduce Motion it is simply signal.

## The flow

```
Hello ──Connect a Mac──▶ Scan (camera) ──▶ Sign in (GitHub sheet)
  │                         │ Enter address instead      │
  │                         ▼                            ▼
  │                    Relay address ──▶ Sign in ──▶ Pick a Mac ──▶ Approve on Mac (code)
  │                                                                     │
  │                                    ┌────────────────────────────────┘
  │                                    ▼
  │                         Connected · notifications (system alert)
  │                                    ▼
  │                         Your language (hold to try: microphone alert)
  │                                    ▼
  └──Look around──▶ Inbox with sample Macs          Inbox (live), one tip on the mic circle
```

| # | Screen | Its one job | Required? |
|---|---|---|---|
| 1 | Hello | Say what Hesper is in one line; start | Yes (one tap) |
| 2 | Scan your Mac | Read the Mac's pairing code | Yes, or the manual path |
| 3 | Sign in | Enroll the phone on the owner's relay with GitHub | Yes |
| 4 | Connected | Show it worked; ask for notifications | The screen yes, permission no |
| 5 | Your language | Pick the dictation language; optionally try it | Pre-selected; one tap |
| 6 | Inbox | Real work; one tip on the mic circle | — |

About 40 seconds when GitHub is already signed in in Safari: 1 tap,
point the camera, 2 taps for GitHub, 1 tap plus the system alert, 1 tap.

### 1. Hello

The mark large, the square pulsing, then one line and one button.

- Title **Hesper**. Line: "Your agents keep working on your Macs. Hesper
  tells you when they need you."
- **Connect a Mac** (primary, accent).
- **Look around with sample Macs** (plain text button): the app as it runs
  today on `FixtureLink`, every screen working, a pill "Sample Macs" in
  the Macs tab with **Connect yours**. No permissions are asked in this
  mode. This is also the demo mode App Review needs: an app with
  account-based features must offer "either an active demo account or
  fully-featured demo mode"
  ([App Review 2.1](https://developer.apple.com/app-store/review/guidelines/#app-completeness)),
  and a self-hosted relay cannot give Apple an account.
- No sign-in, terms or licence text here
  ([HIG](https://developer.apple.com/design/human-interface-guidelines/onboarding):
  "Avoid displaying licensing details within your onboarding flow").

### 2. Scan your Mac

What to do on the Mac, then the camera.

- "On your Mac, open **Hesper › Settings › iPhone**. Or run
  `hesperctl phone`." The Mac shows a code (QR) that renews itself every
  two minutes while the pane is open; the terminal draws it with block
  characters, as Claude Code's Remote Control does
  ([Remote Control](https://code.claude.com/docs/en/remote-control)).
- **Scan the code** opens the camera. The first time, the system camera
  alert comes up on that tap; the screen above it is its context, so there
  is no separate pre-alert. Purpose string: "Hesper scans the code your
  Mac shows, to connect to it."
- The code holds the relay's address, the Mac's id and name, the Mac's
  end-to-end key (pinned as in `trusted-hosts.json`), the rights the
  owner allows (set in the Mac pane; default observe, answer, type,
  start & stop; shells off) and a one-time pairing secret. It is not a
  URL: the system Camera app shows it as text, so the secret never travels
  as a link ("Credentials and pairing secrets never belong in URLs",
  protocol).
- **Enter address instead** (small, under the viewfinder) for when the
  camera is off or the Mac is not at hand. It is the required alternative
  to a declined permission
  ([App Review 5.1.1(iv)](https://developer.apple.com/app-store/review/guidelines/#data-collection-and-storage):
  "Where possible, provide alternative solutions for users who don't grant
  consent").

### 3. Sign in

The relay only lets in its owner, so the phone enrolls as a controller
with GitHub, the way `hesperctl login` does.

- Card: the Mac's name and the relay's host ("studio · relay.example.com"),
  so the owner sees where they are signing in. **Continue with GitHub**.
- The system asks "Hesper wants to use example.com to sign in", then
  `ASWebAuthenticationSession` shows the relay's approval page. The page
  shows the device name ("iPhone") and the enrollment code; the app shows
  the same code under the sheet, so it can be compared. If GitHub is
  already signed in in Safari, this is two taps.
- The relay sends the session back to a registered app callback (the
  `callback` field of `/v1/auth/enroll`); the app claims the credentials
  with its verifier and stores them in the Keychain.
- The phone creates its device key and strong key in the Secure Enclave
  (the strong key behind biometry) and its end-to-end key. Nothing asks
  for Face ID yet: creating a key does not use it.

### 4. Connected

The pay-off and the one permission that belongs here.

- While it works, three quiet steps tick: Signed in · studio approved ·
  Secure channel. With the scanned secret, studio approves the phone at
  once (see [Core work](#core-work)); the Mac shows a notification
  "iPhone added · Remove".
- Then the screen settles: **studio is connected**, its live summary
  ("4 agents · 1 needs you"), and what the phone may do there ("Answer,
  type and start agents · no shells"), set on the Mac.
- Below, the notification context: a preview of a real Hesper
  notification ("relay-tests on studio wants to edit hub.go · Allow ·
  Deny"), with "When an agent needs you, it shows up on your lock screen.
  You can answer from there."
- One button, **Continue**, which opens the system notification alert.
  Apple's rule for a screen before a system alert: "Include only one
  button and make it clear that it opens the system alert", titled
  "Continue" or "Next", never "Allow", and no Close or Not now
  ([HIG privacy, pre-alert screens](https://developer.apple.com/design/human-interface-guidelines/privacy#Pre-alert-screens-windows-or-views)).
  Declining happens in the system alert, and the flow goes on either way.
- Explicit authorization, not provisional: provisional notifications are
  delivered quietly and "don't … appear on the lock screen"
  ([UserNotifications](https://developer.apple.com/documentation/usernotifications/asking-permission-to-use-notifications)),
  which defeats the point. Options: alert, sound, badge; approvals and
  questions are sent `time-sensitive` (entitlement), and the system
  explains Time Sensitive itself the first time one arrives
  ([HIG notifications](https://developer.apple.com/design/human-interface-guidelines/managing-notifications)).
- After the answer, the APNs token is registered with the relay (core
  work 2 in the concept). The Live Activity needs no permission: it is on
  by default and the owner can turn it off in Settings.

### 5. Your language

The decided place to choose the dictation language (concept, flow 3).

- "Which language do you speak to Hesper in?" A short list from
  `SpeechLanguages.candidates`: the keyboard's dictation languages, then
  the preferred languages, matched to the locales `SpeechTranscriber`
  supports, never the region. Each row in its own name ("Deutsch",
  "English (US)"). The first is selected.
- **Hold to try** with a mic circle like the one in the app: holding it
  asks for the microphone the first time (this is the first use, so the
  system alert comes in context) and shows the words live in the picked
  language. If the words come out wrong, the owner sees at once that the
  language is wrong. Trying is optional.
- **Done** saves `dictationLanguage` and starts the model download for
  that locale in the background (`AssetInventory`), so the first real
  capture does not wait. "Don't let large downloads hinder onboarding"
  ([HIG](https://developer.apple.com/design/human-interface-guidelines/onboarding)):
  nothing waits for it.
- Footer: "Hesper listens on this iPhone. Nothing you say leaves it until
  you send it." Changeable later in Settings › Voice.
- One candidate: the screen still shows, with that one row and Hold to
  try; it is where the owner learns the mic exists. No candidate (no
  supported language): the screen is skipped, and capture opens with the
  keyboard.

### 6. Inbox

The app, live. One TipKit tip on the mic circle, shown once: "Hold to say
a task. Let go to start it or keep it for later." Apple prefers "a
collection of context-specific tips instead of a single onboarding flow"
([HIG](https://developer.apple.com/design/human-interface-guidelines/onboarding),
[TipKit](https://developer.apple.com/documentation/tipkit)). Later tips,
each once and only when the situation arises: swipe on a row (first
exited agent), Fit to iPhone (first terminal wider than the phone).

When the Mac has no agents, the Inbox says "Nothing needs you" and
offers the mic circle: "Say what studio should do."

## Permissions

| Permission | When | Without it |
|---|---|---|
| Camera | Tap Scan the code | Enter address instead |
| Notifications | Connected, after one Continue | Inbox only; a quiet line in Settings and one card (below) |
| Microphone | First hold of a mic circle (Hold to try, or the app's) | Capture opens with the keyboard: "Microphone is off · Settings" |
| Face ID | First use of the strong key (typing into a shell; approvals if the open decision says so) | Shells stay locked; everything else works |
| Local network | Not in v1 onboarding; the direct path asks when it first finds a Mac on the LAN | Relay only |
| Live Activity | Never asked; on by default | — |

Speech recognition permission is not needed: on-device `SpeechTranscriber`
needs only the microphone.

Declined notifications are not nagged about. The one reminder: the first
time something needed the owner while the app was in the background, the
Inbox shows a dismissable card above it: "relay-tests waited 12 min.
Turn on notifications to get this on your lock screen." with **Settings**.
Dismissed once, it never comes back; Settings › Notifications keeps a
plain status line.

## Adding more Macs

From the Macs tab, never in onboarding. After the first Mac, the relay
already knows the owner's other Macs, so **Add Mac** lists them:

- **On your relay**: each Mac the relay has seen in the owner's namespace
  but not yet approving this phone, with **Pair**. Pair sends
  `devices.request` and shows the code (`ABC-DEF`), big, with "Approve on
  mini: click the notification there, or run `hesperctl approve ABC-DEF`".
  The row waits ("Waiting for mini") and turns into a normal Mac row when
  approved. The owner does not have to be at that Mac right now; the
  request stays pending for 10 minutes and can be asked again.
- **Scan a code**: the same scanner as onboarding, instant approval.
- A Mac without Hesper yet: "Install Hesper on your Mac" with the
  install command to share (AirDrop, Messages), no store link.

## Empty and failure states

| Where | What happened | The screen says | Way out |
|---|---|---|---|
| Scan | Camera declined | "The camera is off for Hesper." | Open Settings · Enter address instead |
| Scan | Not a Hesper code | Nothing; the viewfinder keeps looking, a hint after 5 s: "Point at the code in Hesper › Settings › iPhone" | — |
| Scan | Code expired or used | "This code has expired. Your Mac shows a new one." | Keeps scanning |
| Sign in | Sheet cancelled | Back on Sign in, nothing lost | Continue with GitHub |
| Sign in | GitHub account not on the relay's allowlist | "relay.example.com doesn't let *jdoe* in. Ask its owner to add your GitHub account." | Use another account |
| Sign in | Relay unreachable | "Can't reach relay.example.com." If the host is private: "Is your VPN on?" | Try again |
| Connected | Mac asleep or offline after the scan | "studio is asleep. It approves the iPhone when it wakes." Steps stay pending | Wait, or Continue and finish later from Macs |
| Connected | Mac's hesperd too old for scanned approval | Falls back to the code: "Approve on studio: ABC-DEF" | Approve on the Mac |
| Connected | Denied on the Mac | "studio said no to this iPhone." | Scan again |
| Connected | Paired, but the Mac has no agents | "studio is connected · nothing running" | Continue |
| Language | Model download fails or no network | Nothing here; capture says "Getting Deutsch ready" and offers the keyboard | Retries in the background |
| Any | App killed mid-way | Resumes at the first step not done (credentials, approval, asked notifications, language are each remembered) | — |
| Later | Relay session expired (30 days) | A banner on every tab: "Sign in again to reach your Macs." Lists show "as of" data | Sign in (GitHub sheet), approvals stay |
| Later | Last Mac removed this phone | Macs tab: "No Mac lets this iPhone in." | Add Mac |

Every failure keeps what was already done. Nothing restarts from Hello
except **Settings › Reset this iPhone**, which deletes the keys and
credentials and tells the owner to remove the iPhone on their Macs.

## iPad

The same screens as a centred card (form-sheet width) over the dusk
background. The scanner uses the front or back camera, whichever faces the
Mac. Pairing an iPad is pairing another device: its own keys, its own
approval.

## What onboarding leaves out

| Left out | Why |
|---|---|
| A feature tour or carousel | The Inbox and tips teach in context |
| Face ID setup | Only matters at the first strong-key use; asked there |
| Choosing rights on the phone | Rights are the Mac's decision, set in its pane before the code is scanned |
| Pairing several Macs | One is enough to be useful; the rest come from the Macs tab with one tap each |
| Live Activity, widgets, controls | No permission needed; they appear when there is something to show |
| Voice model or language switch in capture | Decided: one language, chosen here |
| Account creation | The owner already has a relay and a GitHub account; the relay is self-hosted |

## Core work

What core Hesper needs for this onboarding, adding to the concept's list:

1. **A pairing offer on the Mac.** `hesperctl phone` and a new
   **Settings › iPhone** pane in Hesper.app show a code with the relay
   origin, machine id and name, host end-to-end key, offered rights and a
   one-time secret (128 bits, 5 minutes, single use, renewed while shown).
   hesperd keeps the open offers.
2. **Approve by proof of the offer.** `devices.request` gains an optional
   `offerProof`: HMAC-SHA256 with the secret over the request's `name`,
   `key`, `strongKey` and `e2eKey`. The secret itself never leaves the
   phone, so the relay (which sees `devices.request`) cannot reuse it for
   its own keys. A valid proof approves the device at once with the
   offer's rights and posts "iPhone added · Remove" on the Mac. Additive
   and backward compatible: without it, the code flow as today. Update
   `relay/docs/protocol.md`.
3. **An app callback for GitHub enrollment.** The relay registers the iOS
   app's callback so `ASWebAuthenticationSession` returns to the app; the
   enrollment page names the device.
4. **Approvals that survive signing in again.** Hosts keep approvals by
   relay device id (`controllers.json`). When the 30-day refresh family
   ends and the phone enrolls again, it must not lose every Mac: either
   re-enrollment keeps the device id for a device that proves its device
   key, or hosts match a known key under a new id.
5. **The owner's unpaired Macs** for Add Mac: names and online state of
   every host in the namespace, from the inventory the relay already sends
   (check that names are included; snapshots carry none).
6. **The push token** registered with the relay after notification
   permission (concept, core work 2).

## Interfaces

The contracts the parts are built against. `relay/docs/protocol.md` holds
the wire parts once built; the Swift side is
`HesperKit/Sources/HesperFleet/Pairing.swift`.

**Pairing code.** The scanned text is `HESPER1:` followed by base64url
(no padding) of compact JSON:

```json
{"v":1,"relay":"https://relay.example.com","machine":"MACHINE_ID","name":"studio",
 "hostKey":"BASE64_X25519","rights":["observe","answer","type","transfer"],
 "offer":"BASE64URL_16_BYTES","expires":1793650956}
```

`expires` is Unix seconds. Anything else, or `v` other than 1, is not a
Hesper code.

**Offer proof.** `devices.request` takes an optional `offerProof`:
standard base64 of HMAC-SHA256, keyed with the 16 offer bytes, over these
lines joined by `\n` (no trailing newline):

```text
hesper-offer-v1
<host machine ID>
<name>
<key>
<strongKey>
<e2eKey, or empty>
```

`key`, `strongKey` and `e2eKey` exactly as sent in the request. The host
tries each open offer; a match approves the device at once with the
offer's rights (minus `shell` for software keys, as today), uses the offer
up and answers `{"status":"approved",…}`. No match: `forbidden`, and the
request is not kept pending. An older host ignores the field and answers
`pending`; the phone then shows the code flow.

**Offers on the Mac.** hesperd keeps at most 3 open offers in memory, 5
minutes each, single use. Control socket (local only):

- `devices.offer` `{rights?: [String]}` → `{payload, expires, id}`; rights
  default to `observe, answer, type, transfer`.
- `devices.offerCancel` `{id}`.
- Event `devices.paired` `{offer, device, name, rights}` when an offer is
  used, for the Mac app's pane and its "iPhone added" notification.

`hesperctl phone [--rights R,…] [--json]` draws the code in the terminal
(half-block characters), renews it before it expires, and exits when a
device pairs (with `--json`: one line per offer, then the pairing).

**Relay.** The iOS app's sign-in callback `hesper-ios://auth` is a
registered `callback` for `/v1/auth/enroll`. `POST /v1/devices/push`
`{token, environment}` (bearer) stores the device's APNs token; sending
pushes stays concept core work 2. An enrollment that presents a proof of
an earlier device key (`previousDevice`, signed) keeps that device id, so
approvals on the Macs survive signing in again.

## Open decisions

Taken for the first build, open to change:

- Scanned approval is immediate (no click on the Mac).
- Hold to try stays on the language screen.
- Only the candidate languages are offered.
- The command is `hesperctl phone`.

Still open:

- Should Settings › iPhone on the Mac also list the phones it approved,
  with Remove, or does that stay in Machines & pairing?
