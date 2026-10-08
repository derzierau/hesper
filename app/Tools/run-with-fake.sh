#!/bin/bash
# Runs Hesper.app against the TEST-ONLY fake daemon in a fresh temp dir.
#   run-with-fake.sh selftest   UI smoke test (exit status = result; SHOTS=dir: screenshots)
#   run-with-fake.sh perf       Phase 0 measurement (prints JSON)
#   run-with-fake.sh run        interactive, with a demo scenario
#   run-with-fake.sh selftest-real   the UI smoke test against the REAL hesperd
#                                    (relay/dist/hesperd) whose profiles all run
#                                    the fake TUI: never claude/codex
set -euo pipefail
cd "$(dirname "$0")/.."
mode=${1:-run}
APP=build/Hesper.app/Contents/MacOS/Hesper
FAKE=$PWD/.build/fake-hesperd

# Short path: Unix socket paths are limited to 104 bytes.
dir=$(mktemp -d -t hesper)
export HESPER_STATE_DIR=$dir
export HESPER_SOCKET=$dir/hesperd.sock
cleanup() {
  [[ -n "${daemon:-}" ]] && kill "$daemon" 2>/dev/null || true
  wait 2>/dev/null || true
  rm -rf "$dir"
}
trap cleanup EXIT

# DAEMON=real with layout: the real hesperd (fake TUIs), e.g. to show
# scrollback, which only the real daemon keeps.
[[ $mode == layout && ${DAEMON:-} == real ]] && mode=layout-real

case $mode in
  selftest-real|layout-real)
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
    export HESPER_SELFTEST_PROJECT=$project
    start_daemon() { "$HESPERD" serve --state-dir "$dir" --config-dir "$dir/config" >>"$dir/daemon.log" 2>&1 & daemon=$!; }
    start_daemon
    ;;
  selftest)
    # The # list's folders: the run's own dir, never ~/projects.
    export HESPER_PROJECT_ROOT=$dir
    start_daemon() { "$FAKE" serve --agents 3 --tui agent >>"$dir/daemon.log" 2>&1 & daemon=$!; }
    start_daemon
    ;;
  perf) "$FAKE" serve --agents "${PERF_AGENTS:-16}" --tui "${PERF_TUI:-flood}" --fps "${PERF_FPS:-120}" ${FAKE_ARGS:-} >"$dir/daemon.log" 2>&1 & ;;
  run) "$FAKE" serve --agents "${AGENTS:-6}" --tui agent --scenario >"$dir/daemon.log" 2>&1 & ;;
  layout) "$FAKE" serve --agents "${AGENTS:-6}" --tui agent ${FAKE_ARGS:-} >"$dir/daemon.log" 2>&1 & ;;
  *) echo "unknown mode $mode" >&2; exit 2 ;;
esac
daemon=${daemon:-$!}
wait_socket() { for _ in $(seq 100); do [[ -S $HESPER_SOCKET ]] && break; sleep 0.05; done; }
wait_socket

common=(--socket "$HESPER_SOCKET" --hesperd "${HESPERD:-$FAKE}" --no-notifications)
[[ $mode == layout-real ]] && mode=layout
case $mode in
  selftest|selftest-real)
    out=$dir/selftest.json
    status=0
    shots=()
    if [[ -n ${SHOTS:-} ]]; then mkdir -p "$SHOTS"; shots=(--selftest-shots "$(cd "$SHOTS" && pwd)"); fi
    "$APP" "${common[@]}" --selftest-out "$out" ${shots[@]+"${shots[@]}"} || status=$?
    [[ -f $out ]] && cat "$out"
    if [[ $status == 0 ]]; then
      # Phase 2: a new app process finds the kept draft (with the real
      # hesperd also after a daemon restart: drafts.json).
      if [[ $mode == selftest-real ]]; then
        kill -TERM "$daemon" 2>/dev/null || true
        wait "$daemon" 2>/dev/null || true
        start_daemon
        wait_socket
      fi
      out2=$dir/selftest-restore.json
      "$APP" "${common[@]}" --selftest-out "$out2" --selftest-phase restore || status=$?
      [[ -f $out2 ]] && cat "$out2"
    fi
    [[ $status != 0 ]] && tail -20 "$dir/daemon.log" >&2
    exit $status
    ;;
  perf)
    out=$dir/perf.json
    hist=()
    [[ -n ${PERF_HISTORY:-} ]] && hist=(--perf-history "$PERF_HISTORY") # shared history: History open (open: no event stream)
    "$APP" "${common[@]}" --perf-out "$out" --perf-seconds "${PERF_SECONDS:-10}" --perf-agents "${PERF_AGENTS:-16}" ${hist[@]+"${hist[@]}"} &
    app=$!
    sleep 8
    # Every process the app started (login + hesperd attach per tile).
    kids=$(pgrep -P "$app" | tr '\n' ',' | sed 's/,$//')
    grand=$( [[ -n $kids ]] && pgrep -P "$kids" | tr '\n' ',' | sed 's/,$//' || true)
    pids=$(echo "$app,$kids,$grand" | sed 's/,,*/,/g; s/,$//')
    ps -o rss= -p "$pids" | awk -v n="$(echo "$pids" | tr ',' '\n' | wc -l | tr -d ' ')" '{s+=$1} END {printf "{\"processes\": %d, \"rssTotalMB\": %.1f}\n", n, s/1024}' > "$dir/procs.json"
    ps -o rss= -p "$app" | awk '{printf "app RSS %.1f MB\n", $1/1024}'
    wait $app || true
    cat "$out"
    echo; echo "processes (app + login + attach):"; cat "$dir/procs.json"
    mkdir -p build && cp "$out" build/perf-last.json
    ;;
  run)
    "$APP" "${common[@]}"
    ;;
  layout)
    # Dump the wall's layout (LayoutProbe) and screenshot the window.
    out=$dir/layout.json
    size=()
    [[ -n ${WINDOW_SIZE:-} ]] && size=(--window-size "$WINDOW_SIZE")
    [[ -n ${SHOW:-} ]] && size+=(--layout-show "$SHOW")
    [[ -n ${ARRANGEMENT:-} ]] && size+=(--arrangement "$ARRANGEMENT")
    "$APP" "${common[@]}" --layout-out "$out" --layout-hold 3 ${size[@]+"${size[@]}"} &
    app=$!
    for _ in $(seq 400); do [[ -s $out ]] && break; sleep 0.1; done
    if [[ -s $out ]]; then
      wid=$(sed -n 's/.*"windowNumber" : \([0-9]*\).*/\1/p' "$out")
      if [[ -n ${SHOT:-} && -n $wid ]]; then
        screencapture -x -o -l "$wid" "$SHOT" || echo "screencapture failed" >&2
        if [[ ${SHOW:-} == flap ]]; then
          # A frame sequence while the agent's state flaps (footer evidence).
          for n in 1 2 3 4 5 6; do sleep 0.45; screencapture -x -o -l "$wid" "${SHOT%.png}-$n.png" || true; done
          sleep 1.5
          [[ -f $out.flap.json ]] && cp "$out.flap.json" "${SHOT%.png}.flap.json"
        fi
        sheet=$(sed -n 's/.*"sheetWindowNumber" : \([0-9]*\).*/\1/p' "$out")
        if [[ -n $sheet && $sheet != 0 ]]; then
          screencapture -x -o -l "$sheet" "${SHOT%.png}-sheet.png" || true
        fi
      fi
      mkdir -p build && cp "$out" "${LAYOUT_OUT:-build/layout-last.json}"
      cat "$out"
    fi
    wait $app || true
    ;;
esac
