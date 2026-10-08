#!/bin/bash
# Multi-window checks against the TEST-ONLY fake daemon in a fresh temp dir
# (never the user's daemon, state, or window-state file).
#   run-windows.sh selftest   windows UI smoke test: phase 1, then a relaunch
#                             (phase 2) on the same daemon + window-state file
#                             (SHOTS=dir also writes screenshots)
#   run-windows.sh perf       the same agents on 1 wall, then on PERF_WALLS walls
set -euo pipefail
cd "$(dirname "$0")/.."
mode=${1:-selftest}
APP=build/Hesper.app/Contents/MacOS/Hesper
FAKE=$PWD/.build/fake-hesperd

dir=$(mktemp -d -t hesperw)
export HESPER_STATE_DIR=$dir
export HESPER_SOCKET=$dir/hesperd.sock
cleanup() {
  [[ -n "${daemon:-}" ]] && kill "$daemon" 2>/dev/null || true
  wait 2>/dev/null || true
  rm -rf "$dir"
}
trap cleanup EXIT

case $mode in
  selftest) "$FAKE" serve --agents 6 --tui agent >"$dir/daemon.log" 2>&1 & ;;
  perf) "$FAKE" serve --agents "${PERF_AGENTS:-16}" --tui "${PERF_TUI:-agent}" --fps "${PERF_FPS:-120}" >"$dir/daemon.log" 2>&1 & ;;
  *) echo "unknown mode $mode" >&2; exit 2 ;;
esac
daemon=$!
for _ in $(seq 100); do [[ -S $HESPER_SOCKET ]] && break; sleep 0.05; done

common=(--socket "$HESPER_SOCKET" --hesperd "$FAKE" --no-notifications --no-status-item --no-hotkey --ephemeral)
case $mode in
  selftest)
    state=$dir/windows.json
    shots=()
    if [[ -n ${SHOTS:-} ]]; then mkdir -p "$SHOTS"; shots=(--windows-shots "$(cd "$SHOTS" && pwd)"); fi
    status=0
    "$APP" "${common[@]}" --window-state "$state" --windows-test-out "$dir/p1.json" --windows-phase 1 \
      --window-size 900x600 --min-chars 80 ${shots[@]+"${shots[@]}"} || status=$?
    [[ -f $dir/p1.json ]] && cat "$dir/p1.json"
    if [[ $status == 0 ]]; then
      "$APP" "${common[@]}" --window-state "$state" --windows-test-out "$dir/p2.json" --windows-phase 2 --min-chars 80 || status=$?
      [[ -f $dir/p2.json ]] && cat "$dir/p2.json"
    fi
    [[ $status != 0 ]] && tail -20 "$dir/daemon.log" >&2
    exit $status
    ;;
  perf)
    out=$dir/walls-perf.json
    "$APP" "${common[@]}" --walls-perf-out "$out" --perf-walls "${PERF_WALLS:-3}" --perf-agents "${PERF_AGENTS:-16}" \
      --perf-seconds "${PERF_SECONDS:-10}" --min-chars "${PERF_MIN_CHARS:-60}" &
    app=$!
    # Helper processes (login + hesperd attach per tile) once all walls are up.
    sleep $(( ${PERF_SECONDS:-10} + 14 ))
    kids=$(pgrep -P "$app" | tr '\n' ',' | sed 's/,$//')
    echo "app helper processes (login): $(echo "$kids" | tr ',' '\n' | grep -c . || true)"
    wait $app || true
    cat "$out"
    mkdir -p build && cp "$out" build/walls-perf-last.json
    ;;
esac
