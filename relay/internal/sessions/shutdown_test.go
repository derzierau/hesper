package sessions

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime/pprof"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// closeWithin calls s.Close and fails (with every goroutine's stack) when
// it takes longer than limit.
func closeWithin(t *testing.T, s *Service, limit time.Duration) time.Duration {
	t.Helper()
	start := time.Now()
	done := make(chan struct{})
	go func() { s.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(limit):
		pprof.Lookup("goroutine").WriteTo(os.Stderr, 2)
		<-done
		t.Errorf("Close took %v (limit %v)", time.Since(start).Round(time.Millisecond), limit)
	}
	return time.Since(start)
}

// The real ~/.claude and ~/.codex (read-only; the index in a temp
// folder): Close in the middle of the first pass returns promptly.
// HESPER_HISTORY_CLOSE=1 (HESPER_HISTORY_CLOSE_AFTER: seconds of indexing
// first, default 3).
func TestRealHistoryCloseMidPass(t *testing.T) {
	if os.Getenv("HESPER_HISTORY_CLOSE") == "" {
		t.Skip("HESPER_HISTORY_CLOSE=1 indexes the real ~/.claude and ~/.codex (read-only) into a temp index and closes mid-pass")
	}
	home, _ := os.UserHomeDir()
	after := 3.0
	if v, err := strconv.ParseFloat(os.Getenv("HESPER_HISTORY_CLOSE_AFTER"), 64); err == nil {
		after = v
	}
	opt := Options{StateDir: t.TempDir(), ClaudeHome: filepath.Join(home, ".claude"), CodexHome: filepath.Join(home, ".codex"), UserHome: home,
		Machine: "L", ScanEvery: time.Hour, FullScanEvery: time.Hour, Logf: t.Logf}
	if os.Getenv("HESPER_HISTORY_BENCH_BUSY") != "" {
		opt.Busy = func() bool { return true }
	}
	s, err := Open(opt)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Duration(after * float64(time.Second)))
	s.mu.Lock()
	done, total := s.indexing.Done, s.indexing.Total
	s.mu.Unlock()
	d := closeWithin(t, s, 2*time.Second)
	t.Logf("closed after %.1f s of indexing (%d of %d transcripts): Close took %v", after, done, total, d.Round(time.Millisecond))
}

// Close in the middle of the first pass (background band, throttled, a
// busy Mac) returns within a bound, and the next start indexes every
// transcript, including the one that was being read.
func TestCloseMidPassThenResume(t *testing.T) {
	e := newEnv(t)
	const n = 40
	pad := bytes.Repeat([]byte("x"), 64<<10)
	base := fixture(t, "claude.jsonl")
	dir := filepath.Join(e.claude, "projects", strings.ReplaceAll(e.work, "/", "-"))
	os.MkdirAll(dir, 0o700)
	for i := 0; i < n; i++ {
		sid := fmt.Sprintf("5b0c1d2e-0000-4000-8000-%012x", i+1)
		data := bytes.ReplaceAll(base, []byte(claudeSID), []byte(sid))
		data = bytes.ReplaceAll(data, []byte("/work/rail"), []byte(e.work))
		var b bytes.Buffer
		b.Write(data)
		for j := 0; j < 32; j++ { // 2 MiB of lines the index skips
			fmt.Fprintf(&b, "{\"type\":\"progress\",\"sessionId\":%q,\"data\":\"%s\"}\n", sid, pad)
		}
		if err := os.WriteFile(filepath.Join(dir, sid+".jsonl"), b.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	opt := Options{StateDir: e.state, ConfigDir: e.config, ClaudeHome: e.claude, CodexHome: e.codex, UserHome: e.home,
		Machine: "L", ScanEvery: time.Hour, FullScanEvery: time.Hour, Logf: t.Logf, Busy: func() bool { return true }}
	// The pass is held after a few transcripts until Close: mid-pass
	// however fast or loaded the machine (no race with the pass).
	const before = 3
	var read atomic.Int32
	var gate atomic.Bool // the first service only; the restart runs free
	gate.Store(true)
	reached := make(chan struct{})
	afterTranscript = func(s *Service) {
		if !gate.Load() {
			return
		}
		if read.Add(1) == before {
			close(reached)
		}
		if read.Load() >= before {
			<-s.stop
		}
	}
	t.Cleanup(func() { afterTranscript = nil })
	s, err := Open(opt)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-reached:
	case <-time.After(30 * time.Second):
		t.Fatal("the pass never read a transcript")
	}
	// closeWait (for a reader mid-transcript) and the index's own close,
	// with room for a loaded machine; a Close that hangs fails.
	d := closeWithin(t, s, closeWait+3*time.Second)
	gate.Store(false)
	if got := read.Load(); got >= n {
		t.Fatalf("the pass finished (%d of %d) before Close", got, n)
	}
	t.Logf("closed after %d of %d transcripts in %v", read.Load(), n, d.Round(time.Millisecond))

	opt.Busy, opt.Foreground, opt.ScanEvery, opt.FullScanEvery = nil, true, 30*time.Millisecond, 100*time.Millisecond
	s2, err := Open(opt)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	// 80 MiB to read again (6 s under -race on an idle Mac): room for a
	// loaded machine.
	waitForWithin(t, "every transcript indexed after the restart", 90*time.Second, func() bool {
		res, err := s2.Search(wire.SessionSearchParams{Limit: 200})
		return err == nil && len(res.Items) == n
	})
}
