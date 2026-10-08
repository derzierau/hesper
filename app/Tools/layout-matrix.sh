#!/bin/bash
# Layout dumps + screenshots over arrangements, agent counts and window sizes
# (make layout-matrix OUT=dir). Each run: a fresh fake daemon (agents in a mix
# of states) and app instance.
set -uo pipefail
cd "$(dirname "$0")/.."
out=${OUT:-build/layout}
mkdir -p "$out"
arrangements=${ARRANGEMENTS:-"shelf columns treemap mainStack"}
counts=${COUNTS:-"3 7 12"}
sizes=${SIZES:-"1728x1040"}
for a in $arrangements; do
  for size in $sizes; do
    for n in $counts; do
      name="$out/$a-$n-$size"
      ARRANGEMENT=$a AGENTS=$n WINDOW_SIZE=$size SHOT="$name.png" LAYOUT_OUT="$name.json" FAKE_ARGS="${FAKE_ARGS:---demo-states}" \
        Tools/run-with-fake.sh layout >/dev/null 2>&1
      if [[ -f $name.json ]]; then
        python3 Tools/layout-summary.py "$name.json" | tail -1 | sed "s|^|$a $n agents $size: |"
      else
        echo "$a $n agents $size: no dump"
      fi
    done
  done
done
