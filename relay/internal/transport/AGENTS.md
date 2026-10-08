# relay/internal/transport: hot path

Links between hesperd and the relay carry every terminal frame and event.
The rules for frames, backpressure and limits are in
`relay/docs/terminal-streaming.md`; a change that breaks them updates that
doc in the same commit.

- Measure before and after any change on the frame path:
  `go test -run '^$' -bench . -benchmem -count 6 ./internal/transport/ > "$TMPDIR/tr-new.txt"`
  (base commit first for `tr-old.txt`), then
  `go run golang.org/x/perf/cmd/benchstat@latest "$TMPDIR/tr-old.txt" "$TMPDIR/tr-new.txt"`,
  and `make latency` from `relay/` for keystroke latency. Put both in the
  commit body.
- Backpressure comes from the socket. Each relay direction holds at most
  one application frame, and a message carries at most 32 KiB.
- Per-attach queues stay bounded: past 4 MiB the attach is dropped and
  reattached with a redraw, so one slow tile never holds up the link.
  Never add an unbounded buffer.
- No allocation or logging per frame on the bridge path.
- Remote behaviour is tested with `just test-remote`, which runs an
  in-process relay and never contacts a deployed one.
