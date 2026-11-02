# apps/ios: the iPhone and iPad app

- Read `docs/iphone/concept.md` before changing behaviour; the wireframes
  are in `docs/iphone/wireframes/`.
- `project.yml` is the project. Never commit `Hesper.xcodeproj` or edit
  it by hand; run `make project`.
- Logic goes in `Packages/HesperKit` with a Swift Testing test; views in
  `Packages/HesperUI`. App and extension targets stay thin.
- Wire models and design tokens come from `HesperCore` (`app/`). Add a
  field there, not a copy here. A change to `HesperCore` runs the app's
  checks too (`make -C app test-unit`).
- The app reaches the Macs only through a `LinkClient`. Nothing else on
  the network.
- Colours come from `Theme`, spacing from `DS` (`HesperDesign`). Signal red
  only on needs-you marks and the badge; the primary action uses the
  accent.
- UI code is main-actor by default. Do no blocking work there; decode and
  fold off the main actor when a live link arrives.
- Nothing in the Makefile opens a window. A headless simulator (`xcrun
  simctl boot`, `install`, `io screenshot`) is fine to check a screen;
  opening Simulator.app or running UI tests needs the user's go-ahead.
- Check: `make -C apps/ios check` (lint, logic tests, unsigned build).
