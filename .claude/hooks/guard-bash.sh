#!/bin/bash
# PreToolUse(Bash): refuse commands that open windows (the app's UI and perf
# targets steal focus from the person working) or act on the deployed relay.
# AGENTS.md lists the same commands under "Never run without asking".
command -v jq >/dev/null || exit 0
input=$(cat)
cmd=$(jq -r '.tool_input.command // ""' <<<"$input")
cwd=$(jq -r '.cwd // ""' <<<"$input")

deny() {
  jq -n --arg r "$1" '{hookSpecificOutput: {hookEventName: "PreToolUse",
    permissionDecision: "deny", permissionDecisionReason: $r}}'
  exit 0
}

# One shell segment at a time, so "test -z ... && make -C app build" is judged
# by its make segment alone.
while IFS= read -r seg; do
  read -ra w <<<"$seg"
  ((${#w[@]})) || continue

  # Follow "cd dir && ..." so later segments are judged in that directory.
  if [[ "${w[0]}" == cd && -n "${w[1]}" ]]; then
    [[ "${w[1]}" == /* ]] && cwd=${w[1]} || cwd="$cwd/${w[1]}"
    cwd=${cwd%/}; cwd=${cwd//\/.\//\/}
    continue
  fi

  if [[ "${w[0]}" == just ]]; then
    in_relay=false
    [[ "$cwd" == */relay || "$seg" =~ (-f|--justfile)[[:space:]=]+[^[:space:]]*relay/ ]] && in_relay=true
    recipe=""
    for x in "${w[@]:1}"; do [[ "$x" == -* || "$x" == *relay* ]] || { recipe=$x; break; }; done
    if $in_relay && [[ -n "$recipe" && "$recipe" != test-remote ]]; then
      deny "just $recipe acts on the deployed relay. Ask the user to run it."
    fi
  fi
  # The program this segment runs: "python3 x.py" and "bash x.sh" run x.
  prog=${w[0]}
  [[ "$prog" =~ ^(python3?|bash|sh|zsh)$ && -n "${w[1]}" ]] && prog=${w[1]}
  [[ "$prog" == *deploy.py ]] && deny "deploy.py acts on the deployed relay. Ask the user to run it."
  [[ "$prog" =~ Tools/(run-[a-z-]+|layout-matrix)\.sh$ ]] && deny "The app's UI harness opens windows and steals focus. Ask the user to run it."

  if [[ "${w[0]}" == make ]]; then
    in_app=false
    [[ "$cwd" == */app ]] && in_app=true
    targets=()
    for ((i = 1; i < ${#w[@]}; i++)); do
      case "${w[i]}" in
        -C) ((i++)); [[ "${w[i]}" =~ ^(\./)?app/?$ ]] && in_app=true || in_app=false ;;
        -C*) [[ "${w[i]#-C}" =~ ^(\./)?app/?$ ]] && in_app=true || in_app=false ;;
        -*|*=*) ;;
        *) targets+=("${w[i]}") ;;
      esac
    done
    if $in_app; then
      for t in "${targets[@]}"; do
        case "$t" in
          test|test-ui|test-ui-*|run|layout|layout-*|perf|perf-*)
            deny "make $t in app/ opens windows and steals focus. Run the headless checks (make -C app test-unit test-fake build) and ask the user to run $t." ;;
        esac
      done
    fi
  fi
done < <(perl -pe 's/&&|\|\||;|\|/\n/g' <<<"$cmd")
exit 0
