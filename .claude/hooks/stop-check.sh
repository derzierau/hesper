#!/bin/bash
# Stop: before Claude ends a turn with uncommitted changes, run the headless
# checks for the parts it touched (AGENTS.md, "Done means"). A failure blocks
# the stop and hands Claude the output. A tree that already passed, or that
# has not changed since the last failure, is not checked again.
# HESPER_SKIP_STOP_CHECK=1 turns it off.
[[ -n "$HESPER_SKIP_STOP_CHECK" ]] && exit 0
command -v jq >/dev/null || exit 0
input=$(cat)
active=$(jq -r '.stop_hook_active // false' <<<"$input")
cd "$(jq -r '.cwd // "."' <<<"$input")" 2>/dev/null || exit 0
root=$(git rev-parse --show-toplevel 2>/dev/null) || exit 0
cd "$root" || exit 0
state="$(git rev-parse --git-dir)/claude-stop-check"

changed=$(git status --porcelain --untracked-files=all | sed -E 's/^.. //; s/.* -> //')
[[ -n "$changed" ]] || exit 0
print=$({ git diff HEAD; git ls-files -o --exclude-standard -z | xargs -0 shasum 2>/dev/null; } | shasum | cut -c1-40)
last=$(cat "$state" 2>/dev/null)
[[ "$last" == "ok $print" ]] && exit 0
[[ "$active" == true && "$last" == "fail $print" ]] && exit 0

go_files=$(grep -E '^relay/.*\.go$' <<<"$changed")
pkgs=$(for f in $go_files; do d=$(dirname "${f#relay/}"); [[ -d "relay/$d" ]] && echo "./$d"; done | sort -u)
swift=$(grep -E '^app/(Sources|Tests)/|^app/Package\.(swift|resolved)$' <<<"$changed")
fake=$(grep -E '^app/Tools/fake-hesperd/' <<<"$changed")
[[ -n "$pkgs$swift$fake" ]] || exit 0

out=$(mktemp)
trap 'rm -f "$out"' EXIT
run() { echo "\$ $*" >>"$out"; "$@" >>"$out" 2>&1; }
ok=true
if [[ -n "$pkgs" ]]; then
  existing=$(for f in $go_files; do [[ -f "$f" ]] && echo "${f#relay/}"; done)
  unformatted=$(cd relay && [[ -n "$existing" ]] && gofmt -l $existing)
  [[ -z "$unformatted" ]] || { echo "gofmt needed: $unformatted" >>"$out"; ok=false; }
  (cd relay && run go vet $pkgs && run go test -race $pkgs) || ok=false
fi
[[ -n "$swift" ]] && { run make -C app test-unit || ok=false; }
[[ -n "$fake" ]] && { run make -C app test-fake || ok=false; }

if $ok; then
  echo "ok $print" >"$state"
  exit 0
fi
echo "fail $print" >"$state"
jq -n --arg r "Headless checks failed for the files you changed. Fix them, or tell the user why they fail:
$(tail -c 6000 "$out")" '{decision: "block", reason: $r}'
