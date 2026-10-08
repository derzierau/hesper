# Contributing to Hesper

Thanks for helping. Bug reports, fixes and small focused changes are welcome.
For anything larger, open an issue first so we can agree on the shape before
you write the code.

## Layout

- `relay/`: Go. `hesperd` (the per-Mac daemon), `hesperctl`, `hesper-relay`
  and the Secure Enclave helper `hesper-keys`.
- `app/`: Hesper.app, the native macOS client (SwiftPM, no Xcode project).
- `install.sh`, `scripts/`: the installer and its tests.
- `docs/`: the contracts the code is built against.

## Requirements

- macOS with Xcode 26 (the app builds with its toolchain).
- Go 1.25.
- tmux, for the relay's terminal emulator tests.

## Build and test

These are the commands CI runs (`.github/workflows/`).

Relay and daemon:

```sh
cd relay
test -z "$(gofmt -l .)"
make build test check   # go build, go test -race, go vet
```

macOS app (needs `make -C relay build` first):

```sh
make -C app test-unit test-fake   # unit tests, fake daemon vet
make -C app build                 # build/Hesper.app, ad-hoc signed
make -C app test-ui               # launches the app against the fake daemon
```

`make -C app test` runs every UI smoke test; `app/Makefile` lists the rest.
None of them touch your real hesperd, its state or your agents.

Installer:

```sh
scripts/test-install.sh   # dry runs in a temporary HOME
```

## Pull requests

- Keep each pull request to one change, with tests where the code has them.
- Run the checks above for the parts you touched.
- If behavior changes, update the matching doc in `docs/` or `relay/docs/`.
- Write commit messages in the imperative, prefixed with the area
  (for example `Wall: …`, `relay: …`, `install.sh: …`).

## Security issues

Do not open a public issue for a vulnerability. See [SECURITY.md](SECURITY.md).

## License

By contributing you agree that your contributions are licensed under the
MIT License (see [LICENSE](LICENSE)).
