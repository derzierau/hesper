#!/bin/bash
# Loops UI test (the Loops board, map, wrap-up, product page and Settings ›
# Steward) against the TEST-ONLY fake daemon seeded with --projects demo
# and --loops demo, in a fresh temp dir (never the user's daemon, state or
# window-state file). Opens windows: run it only with the owner's consent.
# SHOTS=dir: screenshots.
set -euo pipefail
cd "$(dirname "$0")/.."
APP=build/Hesper.app/Contents/MacOS/Hesper
FAKE=$PWD/.build/fake-hesperd

dir=$(mktemp -d -t hesperl)
export HESPER_STATE_DIR=$dir
export HESPER_SOCKET=$dir/hesperd.sock
cleanup() {
  [[ -n "${daemon:-}" ]] && kill "$daemon" 2>/dev/null || true
  wait 2>/dev/null || true
  rm -rf "$dir"
}
trap cleanup EXIT

"$FAKE" serve --agents "${AGENTS:-6}" --tui agent --projects demo --loops demo >"$dir/daemon.log" 2>&1 &
daemon=$!
for _ in $(seq 100); do [[ -S $HESPER_SOCKET ]] && break; sleep 0.05; done

shots=()
if [[ -n ${SHOTS:-} ]]; then mkdir -p "$SHOTS"; shots=(--loops-shots "$(cd "$SHOTS" && pwd)"); fi
status=0
"$APP" --socket "$HESPER_SOCKET" --hesperd "$FAKE" --no-notifications --no-status-item --no-hotkey --ephemeral \
  --window-state "$dir/windows.json" --loops-test-out "$dir/out.json" --window-size 1500x950 \
  ${shots[@]+"${shots[@]}"} || status=$?
[[ -f $dir/out.json ]] && cat "$dir/out.json"
[[ $status != 0 ]] && tail -20 "$dir/daemon.log" >&2
exit $status
