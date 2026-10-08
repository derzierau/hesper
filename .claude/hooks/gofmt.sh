#!/bin/bash
# PostToolUse(Edit|Write|MultiEdit): gofmt the Go file Claude just wrote.
command -v jq >/dev/null || exit 0
file=$(jq -r '.tool_input.file_path // empty')
[[ "$file" == *.go && -f "$file" ]] || exit 0
gofmt -w "$file"
