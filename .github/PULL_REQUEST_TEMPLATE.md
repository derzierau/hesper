## What and why

<!-- One change per pull request. Link the issue if there is one. -->

## How it was tested

<!-- The commands you ran (see CONTRIBUTING.md) and what you checked by hand. -->

## Checklist

- [ ] `gofmt` and `make -C relay build test check` pass (if `relay/` changed)
- [ ] `make -C app test-unit test-fake build` pass (if `app/` changed)
- [ ] `scripts/test-install.sh` passes (if `install.sh` changed)
- [ ] Docs updated where behavior changed
- [ ] Each commit is one change with an `<area>: …` subject (CONTRIBUTING.md)
- [ ] No secrets or personal data; new dependencies are MIT-compatible and listed in THIRD_PARTY_NOTICES.md
