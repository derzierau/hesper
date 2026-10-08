#!/usr/bin/env python3
"""Summarizes a LayoutProbe dump (make layout): per tile the card, font,
rows shown and the slack between the terminal area and the engine's grid."""
import json
import sys

d = json.load(open(sys.argv[1]))
print(f"window {d['window']} wall {d['wall']} scale {d['scale']} {d.get('arrangement', '')} content {d.get('contentSize')}")
for t in d['tiles']:
    if t.get('quiet'):
        print(f"  card {t['card']} shelf ({t['state']})")
        continue
    print(f"  card {t['card']} font {t['fontSize']:5} agent {t['agentCols']}x{t['agentRows']} "
          f"engine {t['engineCols']}x{t['engineRows']} layout rows {t.get('layoutRows', '-')} "
          f"area {t['terminalAreaPx']} grid {t['gridPx']} slack {t['slackPx']} card slack {t.get('cardSlackPx', '-')}")
print(f"max vertical slack (area - grid): {d['maxVerticalSlackPx']} px; max content slack: {d['maxContentSlackPx']} px")
