#!/bin/bash
# Shared history (History ⌘Y, the card, ghost cards, ⌘K) against the
# TEST-ONLY fake daemon in a fresh temp dir (never the user's daemon,
# state or window file).
#   run-history.sh test   UI test (SHOTS=dir: screenshots), then the same app
#                         against a daemon without sessions.* (History hidden)
#   run-history.sh perf   History's budgets (3000 sessions, 16 agents), JSON
#   run-history.sh real-test / real-perf   the same against the REAL hesperd
#                         (relay/dist/hesperd) indexing synthetic transcripts
#                         (Tools/history-fixtures.py) in temp CLAUDE_CONFIG_DIR /
#                         CODEX_HOME; every profile runs the fake TUI: never
#                         claude/codex, never ~/.claude or ~/.codex
set -euo pipefail
cd "$(dirname "$0")/.."
mode=${1:-test}
APP=build/Hesper.app/Contents/MacOS/Hesper
FAKE=$PWD/.build/fake-hesperd

dir=$(mktemp -d -t hesperh)
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
# The real hesperd in a temp world (as run-with-fake.sh selftest-real).
start_real() {
  HESPERD=${HESPERD:-$PWD/../relay/dist/hesperd}
  [[ -x $HESPERD ]] || { echo "build the real daemon first: make -C ../relay build" >&2; exit 2; }
  mkdir -p "$dir/config" "$dir/claude" "$dir/codex" "$dir/worktrees"
  export HESPER_CONFIG_DIR=$dir/config HESPER_WORKTREE_ROOT=$dir/worktrees HESPER_PROJECT_ROOT=$dir \
    CLAUDE_CONFIG_DIR=$dir/claude CODEX_HOME=$dir/codex HESPER_MACHINE=L
  tui='"'"$FAKE"'", "tui", "--mode", "agent", "--name", "{name}", "--"'
  cat >"$dir/config/profiles.json" <<JSON
{
  "claude": {"kind": "claude", "argv": [$tui]},
  "claude-auto-rc": {"kind": "claude", "argv": [$tui]},
  "claude-unattended": {"kind": "claude", "argv": [$tui]},
  "codex": {"kind": "codex", "argv": [$tui]},
  "codex-unattended": {"kind": "codex", "argv": [$tui]},
  "shell": {"kind": "shell", "argv": [$tui]}
}
JSON
  project=$dir/project
  mkdir -p "$project"
  git -C "$project" init -q
  git -C "$project" -c user.name=t -c user.email=t@t -c commit.gpgsign=false commit -q --allow-empty -m init
  python3 Tools/history-fixtures.py "$dir/claude" "$dir/codex" "$project" "${SESSIONS:-2000}"
  "$HESPERD" serve --state-dir "$dir" --config-dir "$dir/config" >>"$dir/daemon.log" 2>&1 &
  daemon=$!
  for _ in $(seq 100); do [[ -S $HESPER_SOCKET ]] && break; sleep 0.05; done
}

common=(--socket "$HESPER_SOCKET" --hesperd "$FAKE" --no-notifications --no-status-item --no-hotkey --ephemeral
  --window-state "$dir/windows.json" --window-size 1500x950 --min-chars 80)

case $mode in
  test)
    shots=()
    if [[ -n ${SHOTS:-} ]]; then mkdir -p "$SHOTS"; shots=(--history-shots "$(cd "$SHOTS" && pwd)"); fi
    start_daemon --agents "${AGENTS:-8}" --sessions 600 --indexing-ms 4000
    status=0
    "$APP" "${common[@]}" --history-test-out "$dir/out.json" ${shots[@]+"${shots[@]}"} || status=$?
    [[ -f $dir/out.json ]] && cat "$dir/out.json"
    if [[ $status == 0 ]]; then
      stop_daemon
      rm -f "$dir/windows.json"
      start_daemon --agents 3 --no-sessions
      "$APP" "${common[@]}" --history-test-out "$dir/out2.json" --history-phase nosessions || status=$?
      [[ -f $dir/out2.json ]] && cat "$dir/out2.json"
    fi
    [[ $status != 0 ]] && tail -20 "$dir/daemon.log" >&2
    exit $status
    ;;
  perf)
    start_daemon --agents "${PERF_AGENTS:-16}" --sessions "${PERF_SESSIONS:-3000}"
    "$APP" "${common[@]}" --history-perf-out "$dir/perf.json" || true
    mkdir -p build && cp "$dir/perf.json" build/perf-history-last.json
    cat "$dir/perf.json"
    ;;
  real-test)
    start_real
    shots=()
    if [[ -n ${SHOTS:-} ]]; then mkdir -p "$SHOTS"; shots=(--history-shots "$(cd "$SHOTS" && pwd)"); fi
    status=0
    "$APP" "${common[@]/$FAKE/$HESPERD}" --history-test-out "$dir/out.json" --history-phase real ${shots[@]+"${shots[@]}"} || status=$?
    [[ -f $dir/out.json ]] && cat "$dir/out.json"
    [[ $status != 0 ]] && tail -30 "$dir/daemon.log" >&2
    exit $status
    ;;
  real-perf)
    start_real
    "$APP" "${common[@]/$FAKE/$HESPERD}" --history-perf-out "$dir/perf.json" --perf-agents 0 || true
    mkdir -p build && cp "$dir/perf.json" build/perf-history-real-last.json
    cat "$dir/perf.json"
    ;;
  *) echo "unknown mode $mode" >&2; exit 2 ;;
esac
