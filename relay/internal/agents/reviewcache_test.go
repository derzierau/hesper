package agents

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// waitScanned waits until id's scan (its settle's) is done.
func (h *harness) waitScanned(id string) {
	h.t.Helper()
	_, local := h.reg.split(id)
	waitFor(h.t, func() bool {
		rc := h.reg.reviewScans()
		rc.mu.Lock()
		defer rc.mu.Unlock()
		e := rc.entries[local]
		return e != nil && e.scan != nil && e.running == nil && e.timer == nil
	})
}

// review.list runs Git for a folder only when it may have changed: a
// list after a list reuses the scan; a hook event, a new or edited file
// the scan found, a stage or commit, or review.accept & co. redo it.
func TestReviewListReusesItsScan(t *testing.T) {
	h := reviewHarness(t)
	a := h.spawn(wire.SpawnParams{Task: "do it"})
	h.waitSettled(a.ID)
	h.waitScanned(a.ID)
	list := func(wantFiles, wantAdded int) {
		t.Helper()
		item := findItem(h.reviewList(), a.ID)
		if wantFiles == 0 {
			if item != nil {
				t.Fatalf("listed: %+v", item)
			}
			return
		}
		if item == nil || item.Files != wantFiles || item.Added != wantAdded {
			t.Fatalf("item %+v, want %d files, %d added", item, wantFiles, wantAdded)
		}
	}
	scans := func() int64 { return scansRun.Load() }

	// A new file in the folder: scanned once, then reused.
	os.WriteFile(filepath.Join(h.project, "c.txt"), []byte("c\n"), 0o644)
	n := scans()
	list(1, 1)
	if got := scans() - n; got != 1 {
		t.Fatalf("%d scans for a changed folder", got)
	}
	n = scans()
	for i := 0; i < 5; i++ {
		list(1, 1)
	}
	if got := scans() - n; got != 0 {
		t.Fatalf("%d scans for an unchanged folder", got)
	}
	// The agent works again (it may have written anything) and settles.
	n = scans()
	for _, event := range []string{"PostToolUse", "Stop"} {
		if err := h.reg.Hook(wire.HookParams{Agent: a.ID, Source: "claude", Event: event, Payload: mustJSON(map[string]any{"session_id": a.SessionID, "tool_name": "Bash"})}); err != nil {
			t.Fatal(err)
		}
	}
	h.waitState(a.ID, wire.StateDone)
	list(1, 1)
	h.waitScanned(a.ID) // the settle's scan, unless the list's was it
	if got := scans() - n; got != 1 {
		t.Fatalf("%d scans after the agent worked", got)
	}
	// An edit of a file the scan found changed (same folder mtime).
	time.Sleep(10 * time.Millisecond)
	os.WriteFile(filepath.Join(h.project, "c.txt"), []byte("c\nc\n"), 0o644)
	n = scans()
	list(1, 2)
	if got := scans() - n; got != 1 {
		t.Fatalf("%d scans after an edit", got)
	}
	// A stage moves the index.
	gitIn(t, h.project, "add", "c.txt")
	n = scans()
	list(1, 2)
	if got := scans() - n; got != 1 {
		t.Fatalf("%d scans after a stage", got)
	}
	// review.accept commits it: rescanned, nothing left.
	if err := h.call("review.accept", wire.ReviewAcceptParams{ID: a.ID}, nil); err != nil {
		t.Fatal(err)
	}
	list(0, 0)
}

// A scan that fails (killed at its deadline) is not retried by every
// review.list but after a backoff; review.accept & co. try at once.
func TestReviewScanBacksOffAfterAFailure(t *testing.T) {
	h := reviewHarness(t)
	a := h.spawn(wire.SpawnParams{Task: "do it"})
	h.waitSettled(a.ID)
	h.waitScanned(a.ID)
	timeout := reviewScanTimeout
	reviewScanTimeout = time.Nanosecond // every scan dies at once
	t.Cleanup(func() { reviewScanTimeout = timeout })
	os.WriteFile(filepath.Join(h.project, "c.txt"), []byte("c\n"), 0o644)
	n := scansRun.Load()
	h.reviewList()
	if got := scansRun.Load() - n; got != 1 {
		t.Fatalf("%d scans", got)
	}
	_, local := h.reg.split(a.ID)
	rc := h.reg.reviewScans()
	rc.mu.Lock()
	failures, retry := rc.entries[local].failures, time.Until(rc.entries[local].retryAt)
	rc.mu.Unlock()
	if failures != 1 || retry < reviewBackoff/2 {
		t.Fatalf("failures %d, retry in %v", failures, retry)
	}
	n = scansRun.Load()
	for i := 0; i < 5; i++ {
		h.reviewList()
	}
	if got := scansRun.Load() - n; got != 0 {
		t.Fatalf("%d scans while backing off", got)
	}
	reviewScanTimeout = timeout
	h.reg.NoteReview(a.ID) // what review.accept & co. do
	if item := findItem(h.reviewList(), a.ID); item == nil || item.Files != 1 {
		t.Fatalf("after the backoff was lifted: %+v", item)
	}
}

// The registry's lock under the load of 2026-10-09: agents' hooks,
// lists, attaches' lookups, the indexer's Working and review.list at
// once, with a subscriber that never reads its socket and one that never
// takes its notes. Every call returns quickly (nothing blocks on a
// subscriber under the lock); run with -race.
func TestRegistryLockUnderLoad(t *testing.T) {
	h := reviewHarness(t)
	var ids []wire.Agent
	for i := 0; i < 3; i++ {
		a := h.spawn(wire.SpawnParams{Task: "do it", Worktree: mustJSON(true)})
		ids = append(ids, h.waitSettled(a.ID))
	}
	// A dead client: subscribed, never reads.
	conn, err := net.Dial("unix", h.sock)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.Write([]byte(`{"jsonrpc":"2.0","id":"1","method":"agents.subscribe"}` + "\n"))
	// An in-process subscriber that never waits.
	idle := h.reg.SubscribeMoves()
	defer h.reg.Unsubscribe(idle)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var slowest atomic.Int64
	timed := func(f func()) {
		start := time.Now()
		f()
		if d := int64(time.Since(start)); d > slowest.Load() {
			slowest.Store(d)
		}
	}
	var ops atomic.Int64
	var wg sync.WaitGroup
	run := func(f func(i int)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; ctx.Err() == nil; i++ {
				timed(func() { f(i) })
				ops.Add(1)
			}
		}()
	}
	big := strings.Repeat("x", 2000) // a hook payload the size of the dump's
	for _, a := range ids {
		run(func(i int) {
			h.reg.Hook(wire.HookParams{Agent: a.ID, Source: "claude", Event: "PostToolUse",
				Payload: mustJSON(map[string]any{"session_id": a.SessionID, "tool_name": "Bash", "tool_input": map[string]any{"command": big}})})
		})
	}
	run(func(int) { h.reg.List() })
	run(func(int) { h.reg.Working() })
	run(func(i int) { h.reg.Get(ids[i%len(ids)].ID) })
	run(func(int) { h.reg.NoteReview(ids[0].ID) })
	run(func(int) {
		lctx, lcancel := context.WithTimeout(ctx, 2*time.Second)
		h.reg.ReviewList(lctx)
		lcancel()
	})
	wg.Wait()
	if d := time.Duration(slowest.Load()); d > 2500*time.Millisecond {
		t.Fatalf("a call took %v", d)
	}
	t.Logf("%d calls, the slowest %v", ops.Load(), time.Duration(slowest.Load()).Round(time.Millisecond))
	// The registry still answers (a hang shows here as a timeout).
	done := make(chan struct{})
	go func() { h.reg.List(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the registry's lock is stuck")
	}
}
