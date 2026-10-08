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

## Commits

- One logical change per commit. Each commit builds and passes its tests on
  its own, so `git bisect` works.
- Keep formatting, renames and refactors in their own commits, apart from
  behavior changes.
- Subject: `<area>: <what changes>`, imperative, at most 72 characters, no
  trailing period. Areas: `relay`, `hesperd`, `hesperctl`, `app` or an app
  feature (`Wall`, `History`, `Desks`), `install.sh`, `ci`, `release`,
  `brew`, `docs`, `AGENTS.md`. For example `relay: drop a viewer that falls 4 MiB behind`.
- Body, wrapped at 72 columns: why the change is needed and what a reviewer
  cannot see in the diff.

## Pull requests

- Keep each pull request to one change, with tests where the code has them.
- Run the checks above for the parts you touched.
- If behavior changes, update the matching doc in `docs/` or `relay/docs/`.
- Work on a branch. `main` is never force-pushed.

## Third-party code and secrets

Everything you commit, commit messages included, is public.

- New dependencies need a license compatible with MIT (MIT, BSD, ISC,
  Apache-2.0 or MPL-2.0). Say why in the pull request. Anything that ships
  in a binary goes into [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).
- Do not copy code from other projects unless its license allows it; keep
  its notice and list it in `THIRD_PARTY_NOTICES.md`.
- Never commit credentials, keys, personal paths or data from real
  sessions. Tests use fixtures.

## Coding agents

[AGENTS.md](AGENTS.md) holds the same rules for coding agents (Codex,
Cursor, Claude Code and others), plus the commands they must not run.
Directories with their own rules have their own `AGENTS.md`. Claude Code
reads them through `CLAUDE.md`, and `.claude/settings.json` adds hooks that
format Go, refuse the window-opening targets and run the headless checks.

## Security issues

Do not open a public issue for a vulnerability. See [SECURITY.md](SECURITY.md).

## License

By contributing you agree that your contributions are licensed under the
MIT License (see [LICENSE](LICENSE)).
