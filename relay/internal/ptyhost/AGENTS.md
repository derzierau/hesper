# relay/internal/ptyhost: hot path

PTY output fans out from here to every view, and keystrokes come back in.

- Measure before and after any change here, and put both in the commit body:
  `go test -run '^$' -bench . -benchmem -count 6 ./internal/ptyhost/ > "$TMPDIR/pty-new.txt"`
  (run it on the base commit first for `pty-old.txt`), then
  `go run golang.org/x/perf/cmd/benchstat@latest "$TMPDIR/pty-old.txt" "$TMPDIR/pty-new.txt"`.
  `BenchmarkKeystrokeRoundTrip` and `BenchmarkFanOut16` are the ones that matter.
  A regression over 5% needs a reason in the commit body.
- A slow or stuck viewer must never block the PTY read loop or other viewers.
  Views coalesce through their one-slot `wake` channel, and a viewer 4 MiB
  behind gets a fresh redraw instead of what it missed. Keep both.
- Every channel has a capacity and every queue a cap with a stated overflow
  policy (drop, coalesce or close). No unbounded slices of pending output.
- Every goroutine has an owner that stops it and waits for it (`done`,
  `writerDone`). Tests run with `-race`; a leaked goroutine fails review.
- No logging per read or per frame.
