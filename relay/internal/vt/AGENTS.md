# relay/internal/vt: hot path

Every byte an agent prints passes through this terminal emulator, and every
redraw a viewer sees comes out of it.

- Measure before and after any change here, and put both in the commit body:
  `go test -run '^$' -bench . -benchmem -count 6 ./internal/vt/ > "$TMPDIR/vt-new.txt"`
  (run it on the base commit first for `vt-old.txt`), then
  `go run golang.org/x/perf/cmd/benchstat@latest "$TMPDIR/vt-old.txt" "$TMPDIR/vt-new.txt"`.
  A regression over 5% in time or allocs per op needs a reason in the commit body.
- `Feed` and `RowANSI` must not allocate per byte or per cell. Reuse buffers;
  no `fmt`, string concatenation or `[]byte(string)` conversions in loops.
- History is bounded by lines and bytes, whichever comes first. A change to
  either limit or to eviction updates `history_test.go`.
- tmux is the ground truth: a behaviour change needs a test that compares
  against tmux, not only an expected string.
- Optimize from a profile (`-cpuprofile`, `-memprofile`), not a guess.
