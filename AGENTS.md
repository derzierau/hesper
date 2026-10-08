# Hesper

Hesper runs Claude Code, Codex and shell agents on the owner's Macs.
- `relay/`: Go 1.25. `hesperd` (per-Mac daemon), `hesperctl` (CLI), `hesper-relay` (network relay).
- `app/`: Swift 6.2, SwiftPM only, macOS 14+. Hesper.app. `HesperCore` is pure, tested logic; `Hesper` is AppKit/SwiftUI on libghostty.
- The app is only a view of hesperd. It never talks to the network.

## Commands
- Relay, full check: `cd relay && test -z "$(gofmt -l .)" && make build test check`
- One Go test: `cd relay && go test -race ./internal/<pkg>/ -run <Name>`
- App, headless: `make -C relay build && make -C app test-unit test-fake`
- App bundle: `make -C app build` (output `app/build/Hesper.app`)
- Installer: `scripts/test-install.sh`
- `relay/internal/vt` tests need `tmux` 3.7 or newer on PATH.

## Never run without asking
- `make -C app test`, `test-ui*`, `run`, `layout*`, `perf*` and `app/Tools/run-*.sh`: they open windows and steal focus.
- Any `just` recipe in `relay/` except `test-remote`, and `relay/scripts/deploy.py`: they act on the deployed relay.

## Code quality
- Match the surrounding file: naming, comment density, error style. Read a neighbouring file before adding one.
- Make the smallest change that solves the task. No drive-by refactors, renames or reformatting.
- No new dependency (Go module or Swift package) without asking.
- Testable logic goes in `HesperCore` or `relay/internal/`, with a unit test. Keep the `Hesper` target thin.
- Bug fix: write the failing test first, then fix it.
- Go: wrap errors with `%w` and context; no `panic` outside `main`; blocking calls take a `context.Context` first.
- Swift: Swift 6 strict concurrency. UI code is `@MainActor`. Each `nonisolated(unsafe)` or `@unchecked Sendable` needs a comment saying why it is safe.
- Colors and spacing come from `Theme` and the `DS` namespace (`docs/design-system.md`). No literal colors.
- Wire protocol changes stay backward compatible and update `relay/docs/protocol.md`.
- A behaviour change updates `docs/` or `relay/docs/` in the same commit.
- Hot paths have their own rules: `relay/internal/{vt,ptyhost,transport}/AGENTS.md`, `app/AGENTS.md`.

## Done means
- The check for the area you touched passes. Show the command and its result.
- The diff holds only the task. No debug prints, commented-out code or TODOs without an issue.

## Commits
- One logical change per commit. Each commit builds and passes its tests on its own.
- Keep formatting, renames and refactors in their own commits, apart from behaviour changes.
- Subject: `<area>: <what changes, imperative>`, at most 72 characters, no trailing period.
  Areas: `relay`, `hesperd`, `hesperctl`, `app` or an app feature (`Wall`, `History`, `Desks`), `install.sh`, `ci`, `release`, `brew`, `docs`, `AGENTS.md`.
- Body, wrapped at 72: why the change is needed and anything a reviewer cannot see in the diff.
- Work on a branch; never commit to or force-push `main`.
- Commits written by an agent end with its `Co-Authored-By:` trailer.

## Open source
This repository is public under the MIT License. Everything committed, including commit messages, is published.
- Never commit credentials, tokens, private keys, personal paths (`/Users/<name>`), private hostnames, or data from real sessions. Use fixtures.
- Security fixes: describe the vulnerability only in the private advisory (`SECURITY.md`), never in a public issue, commit message or PR before it is released.
- Dependencies must be MIT, BSD, ISC, Apache-2.0 or MPL-2.0. Anything shipped in a binary is added to `THIRD_PARTY_NOTICES.md`.
- Do not paste code from other projects or answers unless its license allows it; keep its notice and list it in `THIRD_PARTY_NOTICES.md`.
- GitHub Actions stay pinned to a commit SHA with a version comment. Workflows keep `permissions: contents: read` and use no secrets on `pull_request`.
- When a command or check changes, update `CONTRIBUTING.md` and `.github/PULL_REQUEST_TEMPLATE.md` with it.

## Searching
`.claude/worktrees/` holds full copies of the repo made by agents. Exclude it from searches.

## Read before changing
- Architecture: `relay/docs/architecture.md`, `docs/rebuild-contract.md`
- Wire format: `relay/docs/protocol.md`; terminal streaming: `relay/docs/terminal-streaming.md`
