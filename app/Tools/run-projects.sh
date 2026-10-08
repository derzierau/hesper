#!/bin/bash
# Projects (views) UI test against the TEST-ONLY fake daemon seeded with
# projects and groups (--projects demo), in a fresh temp dir (never the
# user's daemon, state or window-state file). SHOTS=dir: screenshots.
set -euo pipefail
cd "$(dirname "$0")/.."
APP=build/Hesper.app/Contents/MacOS/Hesper
FAKE=$PWD/.build/fake-hesperd

dir=$(mktemp -d -t hesperp)
export HESPER_STATE_DIR=$dir
export HESPER_SOCKET=$dir/hesperd.sock
cleanup() {
  [[ -n "${daemon:-}" ]] && kill "$daemon" 2>/dev/null || true
  wait 2>/dev/null || true
  rm -rf "$dir"
}
trap cleanup EXIT

"$FAKE" serve --agents "${AGENTS:-10}" --tui agent --projects demo >"$dir/daemon.log" 2>&1 &
daemon=$!
for _ in $(seq 100); do [[ -S $HESPER_SOCKET ]] && break; sleep 0.05; done

shots=()
if [[ -n ${SHOTS:-} ]]; then mkdir -p "$SHOTS"; shots=(--projects-shots "$(cd "$SHOTS" && pwd)"); fi
status=0
"$APP" --socket "$HESPER_SOCKET" --hesperd "$FAKE" --no-notifications --no-status-item --no-hotkey --ephemeral \
  --window-state "$dir/windows.json" --projects-test-out "$dir/out.json" --window-size 1500x950 --min-chars 80 \
  ${shots[@]+"${shots[@]}"} || status=$?
[[ -f $dir/out.json ]] && cat "$dir/out.json"
[[ $status != 0 ]] && tail -20 "$dir/daemon.log" >&2
exit $status
