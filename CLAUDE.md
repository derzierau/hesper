@AGENTS.md

## Claude Code
- Use plan mode for changes under `relay/pkg/protocol`, `relay/pkg/e2e` or `relay/internal/transport`.
- Send research across many files to a subagent; keep the main context for the change.
- When compacting, keep the list of modified files and the test commands already run.
- Hooks in `.claude/settings.json` run gofmt after edits, refuse the commands under "Never run without asking", and run the headless checks for changed code before you stop (`HESPER_SKIP_STOP_CHECK=1` turns that off).
