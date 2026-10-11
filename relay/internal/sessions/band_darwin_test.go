package sessions

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// darwinThreadWho is setpriority(2)'s PRIO_DARWIN_THREAD: getpriority
// answers 1 for a thread in the background band, 0 otherwise.
const darwinThreadWho = 3

// The indexer's workers stay in the normal band while agents work and
// for old transcripts. A Go thread in macOS's background band gets no
// CPU while the Mac is loaded; hesperd froze for good (2026-10-09, 19
// agents) when a worker there held SQLite's shared-memory lock and the
// garbage collector's stop-the-world waited for its thread, with the
// mirror, also in the band, holding the registry's lock: every hook,
// hesperctl ls and the app waited behind it.
func TestIndexerStaysInTheNormalBand(t *testing.T) {
	e := newEnv(t)
	const n = 3
	base := fixture(t, "claude.jsonl")
	dir := filepath.Join(e.claude, "projects", strings.ReplaceAll(e.work, "/", "-"))
	os.MkdirAll(dir, 0o700)
	old := time.Now().Add(-30 * 24 * time.Hour) // older than two weeks: the band once took these
	for i := 0; i < n; i++ {
		sid := fmt.Sprintf("5b0c1d2e-0000-4000-8000-%012x", i+1)
		data := bytes.ReplaceAll(base, []byte(claudeSID), []byte(sid))
		data = bytes.ReplaceAll(data, []byte("/work/rail"), []byte(e.work))
		path := filepath.Join(dir, sid+".jsonl")
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		os.Chtimes(path, old, old)
	}
	var read, background atomic.Int32
	afterTranscript = func(*Service) {
		// On the worker that read the transcript.
		if p, err := unix.Getpriority(darwinThreadWho, 0); err == nil && p != 0 {
			background.Add(1)
		}
		read.Add(1)
	}
	t.Cleanup(func() { afterTranscript = nil })
	// Not Foreground: the throttled path hesperd runs; agents working.
	s, err := Open(Options{StateDir: e.state, ConfigDir: e.config, ClaudeHome: e.claude, CodexHome: e.codex, UserHome: e.home,
		Machine: "L", ScanEvery: time.Hour, FullScanEvery: time.Hour, Logf: t.Logf, Busy: func() bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	waitForWithin(t, "the transcripts read", 30*time.Second, func() bool { return read.Load() >= n })
	if got := background.Load(); got != 0 {
		t.Fatalf("%d of %d transcripts were read on a thread in the background band", got, read.Load())
	}
}
