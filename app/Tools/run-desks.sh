#!/bin/bash
# Desks UI test against the TEST-ONLY fake daemon (--projects demo) in a
# fresh temp dir, with injected display sets (--fake-screens; never the
# user's daemon, state, desks or window-state file). Phase 1 starts from a
# windows.json (version 1) to migrate; phase 2 relaunches on the "office"
# displays with the desks.json phase 1 wrote. SHOTS=dir: screenshots.
set -euo pipefail
cd "$(dirname "$0")/.."
APP=build/Hesper.app/Contents/MacOS/Hesper
FAKE=$PWD/.build/fake-hesperd

dir=$(mktemp -d -t hesperd)
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

# The file before desks: one main wall (columns, sidebar on).
state=$dir/desks.json
cat >"$state" <<'JSON'
{"version":1,"walls":[{"id":"main","isMain":true,"scope":{"all":{}},"arrangement":"columns","minChars":80,
"frame":{"x":40,"y":80,"width":1400,"height":850},"screen":"1","fullScreen":false,"sidebar":true}],"agentWindows":[]}
JSON

shots=()
if [[ -n ${SHOTS:-} ]]; then mkdir -p "$SHOTS"; shots=(--desks-shots "$(cd "$SHOTS" && pwd)"); fi
common=(--socket "$HESPER_SOCKET" --hesperd "$FAKE" --no-notifications --no-status-item --no-hotkey --ephemeral --window-state "$state" --min-chars 80)
status=0
"$APP" "${common[@]}" --fake-screens laptop --desks-test-out "$dir/p1.json" --desks-phase 1 ${shots[@]+"${shots[@]}"} || status=$?
[[ -f $dir/p1.json ]] && cat "$dir/p1.json"
if [[ $status == 0 ]]; then
  "$APP" "${common[@]}" --fake-screens office --desks-test-out "$dir/p2.json" --desks-phase 2 || status=$?
  [[ -f $dir/p2.json ]] && cat "$dir/p2.json"
fi
[[ $status != 0 ]] && tail -20 "$dir/daemon.log" >&2
exit $status
