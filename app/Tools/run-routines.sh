#!/bin/bash
# Routines (the ⌘⇧R panel, its actions, ⌘K, app.open) against the
# TEST-ONLY fake daemon in a fresh temp dir (never the user's daemon,
# state or window file).
#   run-routines.sh test   UI test against `--routines 5` (SHOTS=dir:
#                          screenshots), then the same app against a daemon
#                          without routines.* (every entry point hidden)
set -euo pipefail
cd "$(dirname "$0")/.."
mode=${1:-test}
APP=build/Hesper.app/Contents/MacOS/Hesper
FAKE=$PWD/.build/fake-hesperd

dir=$(mktemp -d -t hesperr)
export HESPER_STATE_DIR=$dir
export HESPER_SOCKET=$dir/hesperd.sock
cleanup() {
  [[ -n "${daemon:-}" ]] && kill "$daemon" 2>/dev/null || true
  wait 2>/dev/null || true
  rm -rf "$dir"
}
trap cleanup EXIT

start_daemon() {
  rm -f "$HESPER_SOCKET"
  "$FAKE" serve --tui agent --projects demo "$@" >>"$dir/daemon.log" 2>&1 &
  daemon=$!
  for _ in $(seq 100); do [[ -S $HESPER_SOCKET ]] && break; sleep 0.05; done
}
stop_daemon() { kill "$daemon" 2>/dev/null || true; wait "$daemon" 2>/dev/null || true; daemon=; }

common=(--socket "$HESPER_SOCKET" --hesperd "$FAKE" --no-notifications --no-status-item --no-hotkey --ephemeral
  --window-state "$dir/windows.json" --window-size 1500x950 --min-chars 80)

case $mode in
  test)
    shots=()
    if [[ -n ${SHOTS:-} ]]; then mkdir -p "$SHOTS"; shots=(--routines-shots "$(cd "$SHOTS" && pwd)"); fi
    start_daemon --agents "${AGENTS:-3}" --routines 5
    status=0
    "$APP" "${common[@]}" --routines-test-out "$dir/out.json" ${shots[@]+"${shots[@]}"} || status=$?
    [[ -f $dir/out.json ]] && cat "$dir/out.json"
    if [[ $status == 0 ]]; then
      stop_daemon
      rm -f "$dir/windows.json"
      start_daemon --agents 3 --no-routines
      "$APP" "${common[@]}" --routines-test-out "$dir/out2.json" --routines-phase noroutines || status=$?
      [[ -f $dir/out2.json ]] && cat "$dir/out2.json"
    fi
    [[ $status != 0 ]] && tail -20 "$dir/daemon.log" >&2
    exit $status
    ;;
  *) echo "unknown mode $mode" >&2; exit 2 ;;
esac
