@AGENTS.md

## Claude Code
- Use plan mode for changes under `relay/pkg/protocol`, `relay/pkg/e2e` or `relay/internal/transport`.
- Send research across many files to a subagent; keep the main context for the change.
- When compacting, keep the list of modified files and the test commands already run.
