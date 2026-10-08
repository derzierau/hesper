#!/usr/bin/env python3
"""Synthetic Claude and Codex transcripts for the shared history tests
against the real hesperd (TEST-ONLY; shapes as relay/internal/sessions/
testdata). Writes into the given CLAUDE_CONFIG_DIR and CODEX_HOME only —
never the user's ~/.claude or ~/.codex.

    history-fixtures.py CLAUDE_DIR CODEX_DIR PROJECT_DIR [N]
"""
import json, os, random, sys, uuid
from datetime import datetime, timedelta, timezone

claude_dir, codex_dir, project = sys.argv[1], sys.argv[2], sys.argv[3]
n = int(sys.argv[4]) if len(sys.argv) > 4 else 2000
for d in (claude_dir, codex_dir):
    if os.path.realpath(d) in (os.path.realpath(os.path.expanduser("~/.claude")), os.path.realpath(os.path.expanduser("~/.codex"))):
        sys.exit("refusing to write into the user's own transcripts")

rnd = random.Random(7)
titles = ["Fix the flaky badge test on the rail", "Badge counts double after a move", "Push provider FCM migration",
          "Edition picker on the home screen", "Relay latency spikes on the mini", "Draft PR copy for the release",
          "iOS widgets refresh budget", "Auth token renewal race", "VT screen copy performance", "Debounce the ticker",
          "Scrollback for view attaches", "Worktree cleanup after merge", "Codex rollout indexing", "Treemap weights for attention"]
answers = ["20 runs, all green. The race was in the ticker's debounce; fixed in rail.py.",
           "I'd need the fleet.json from the mini to confirm.", "Committed as 911d3c7.",
           "Tests pass locally; CI is still running on the PR.", "The picker now remembers the last edition per account.",
           "p99 dropped from 180 ms to 22 ms after batching the writes."]
words = "quokka wombat ibex okapi gazelle capybara yak platypus heron lynx marten otter tapir".split()
now = datetime.now(timezone.utc)


def ts(t):
    return t.strftime("%Y-%m-%dT%H:%M:%S.000Z")


for i in range(n):
    title = titles[i % len(titles)] + ("" if i < len(titles) else f" ({i // len(titles) + 1})")
    extra = " ".join(rnd.sample(words, 3))
    prompt = f"{title}\nlook at {extra} first, then make it pass 20 times in a row"
    answer = answers[i % len(answers)] + f" ({extra})"
    t0 = now - timedelta(minutes=10 + i * rnd.randint(20, 60))
    sid = str(uuid.UUID(int=rnd.getrandbits(128), version=4))
    branch = ["main", "fix/flaky-badge", "feature/push-fcm", "widgets"][i % 4]
    cwd = project
    if i % 2 == 0:
        folder = os.path.join(claude_dir, "projects", cwd.replace("/", "-"))
        os.makedirs(folder, exist_ok=True)
        base = {"isSidechain": False, "userType": "external", "cwd": cwd, "sessionId": sid, "version": "2.1.290", "gitBranch": branch, "entrypoint": "cli"}
        lines = [
            dict(base, type="user", parentUuid=None, uuid="u-1", timestamp=ts(t0), message={"role": "user", "content": prompt}),
            dict(base, type="assistant", parentUuid="u-1", uuid="a-1", timestamp=ts(t0 + timedelta(seconds=5)), requestId="r1",
                 message={"id": f"msg_{i}", "type": "message", "role": "assistant", "model": "claude-opus",
                          "content": [{"type": "tool_use", "id": "t1", "name": "TodoWrite", "input": {"todos": [
                              {"content": "Open a PR", "status": "pending", "activeForm": "x"},
                              {"content": "Run the suite", "status": "completed", "activeForm": "x"}]}}],
                          "usage": {"input_tokens": 1000 + i, "output_tokens": 300}}),
            dict(base, type="user", parentUuid="a-1", uuid="u-2", timestamp=ts(t0 + timedelta(minutes=3)),
                 message={"role": "user", "content": "make it pass 20 times in a row, then stop"}),
            dict(base, type="assistant", parentUuid="u-2", uuid="a-2", timestamp=ts(t0 + timedelta(minutes=9)), requestId="r2",
                 message={"id": f"msg_{i}b", "type": "message", "role": "assistant", "model": "claude-opus",
                          "content": [{"type": "text", "text": answer}], "usage": {"input_tokens": 2000, "output_tokens": 400}}),
        ]
        path = os.path.join(folder, sid + ".jsonl")
    else:
        day = t0.strftime("%Y/%m/%d")
        folder = os.path.join(codex_dir, "sessions", day)
        os.makedirs(folder, exist_ok=True)
        lines = [
            {"timestamp": ts(t0), "ordinal": 0, "type": "session_meta", "payload": {"session_id": sid, "id": sid, "timestamp": ts(t0), "cwd": cwd,
                                                                                 "originator": "codex-tui", "cli_version": "0.160.1", "source": "cli",
                                                                                 "git": {"branch": branch}}},
            {"timestamp": ts(t0 + timedelta(seconds=1)), "ordinal": 1, "type": "response_item",
             "payload": {"type": "message", "role": "user", "content": [{"type": "input_text", "text": prompt}]}},
            {"timestamp": ts(t0 + timedelta(seconds=3)), "ordinal": 2, "type": "response_item",
             "payload": {"type": "function_call", "name": "update_plan", "call_id": "c1",
                         "arguments": json.dumps({"plan": [{"step": "Open a PR", "status": "pending"}, {"step": "Read it", "status": "completed"}]})}},
            {"timestamp": ts(t0 + timedelta(minutes=8)), "ordinal": 3, "type": "response_item",
             "payload": {"type": "message", "role": "assistant", "content": [{"type": "output_text", "text": answer}]}},
            {"timestamp": ts(t0 + timedelta(minutes=8)), "ordinal": 4, "type": "event_msg",
             "payload": {"type": "token_count", "info": {"total_token_usage": {"input_tokens": 5000 + i, "cached_input_tokens": 1000, "output_tokens": 700}}}},
        ]
        path = os.path.join(folder, f"rollout-{t0.strftime('%Y-%m-%dT%H-%M-%S')}-{sid}.jsonl")
    with open(path, "w") as f:
        for l in lines:
            f.write(json.dumps(l) + "\n")
    mt = (t0 + timedelta(minutes=9)).timestamp()
    os.utime(path, (mt, mt))
print(f"{n} synthetic sessions")
