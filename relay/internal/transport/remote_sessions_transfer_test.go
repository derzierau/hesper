package transport_test

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/internal/handoff"
	"github.com/derzierau/hesper/relay/internal/host"
	"github.com/derzierau/hesper/relay/internal/sessions"
	"github.com/derzierau/hesper/relay/pkg/client"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

// writeLargeClaude writes a Claude transcript of about size bytes: turns
// whose text is part prose, part random (tool output, hashes), so it
// compresses about as a real one does, not to nothing.
func writeLargeClaude(t *testing.T, home, cwd, sid string, at time.Time, size int) string {
	t.Helper()
	path := filepath.Join(home, ".claude", "projects", handoff.ClaudeSlug(cwd), sid+".jsonl")
	os.MkdirAll(filepath.Dir(path), 0o700)
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	random := make([]byte, 700)
	written := 0
	for i := 0; written < size; i++ {
		rand.Read(random)
		text := fmt.Sprintf("Step %d: ran the suite again; output follows. %s %s", i, hex.EncodeToString(random),
			strings.Repeat("The race was in the debounce of the badge. ", 12))
		base := map[string]any{"parentUuid": nil, "isSidechain": false, "userType": "external", "cwd": cwd, "sessionId": sid, "version": "2.1.290",
			"gitBranch": "main", "uuid": fmt.Sprintf("u-%d", i), "timestamp": at.Add(time.Duration(i) * time.Second).UTC().Format(time.RFC3339Nano)}
		if i%2 == 0 {
			base["type"] = "user"
			base["message"] = map[string]any{"role": "user", "content": text}
		} else {
			base["type"] = "assistant"
			base["message"] = map[string]any{"id": fmt.Sprintf("msg_%d", i), "role": "assistant", "content": []any{map[string]any{"type": "text", "text": text}},
				"usage": map[string]any{"input_tokens": 10, "output_tokens": 5}}
		}
		line := claudeLine(base)
		f.WriteString(line)
		written += len(line)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-10 * time.Minute)
	os.Chtimes(path, old, old)
	return path
}

// sessionMoves collects the agents.moving notes of session transfers.
func sessionMoves(t *testing.T, sub *wire.Client, stop <-chan struct{}) <-chan []wire.Moving {
	out := make(chan []wire.Moving, 1)
	go func() {
		var got []wire.Moving
		for {
			select {
			case n := <-sub.Notifications():
				if n.Method != wire.NoteMoving {
					continue
				}
				var m wire.Moving
				if json.Unmarshal(n.Params, &m) == nil && m.Session {
					got = append(got, m)
				}
			case <-stop:
				out <- got
				return
			}
		}
	}()
	return out
}

// A long session forked from the laptop (its home) to the mini over a
// slow link: one request through the relay lasts at most 3 s here, the
// transfer much longer (each chunk is held back), yet the fork succeeds:
// the mini runs it as its own operation and the laptop follows it,
// telling its subscribers the progress (packing, transfer with percent
// and size, unpacking, starting, done); chunks whose answers are lost
// are asked again and the transfer goes on. The transcript travels
// compressed; the mini, without the repository, clones it from its
// remote and gets only the newer commits and the uncommitted work.
func TestSessionsForkLargeOverSlowLink(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	w := newWorld(t, worldOptions{hubTimeout: 3 * time.Second})
	L, M := w.L, w.M
	appRepo(t, L, true)
	L.waitLinked(t, "M")
	M.waitLinked(t, "L")
	const sid = "fffffff0-0000-4000-8000-0000000000f0"
	const size = 30 << 20
	writeLargeClaude(t, L.home, L.project, sid, time.Now().Add(-time.Hour), size)
	eventually2(t, 60*time.Second, "M lists L's session", func() bool { _, ok := M.sessionByID(t, "L:claude:"+sid); return ok })

	host.ServeDelay.Store(int64(150 * time.Millisecond))
	// And two chunks whose answers are lost on the way: sent again, the
	// transfer goes on from there.
	host.ServeStall.Store(2)
	t.Cleanup(func() { host.ServeDelay.Store(0); host.ServeStall.Store(0) })
	sub := L.client(t)
	if err := sub.Call(w.ctx, "agents.subscribe", nil, nil); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	moves := sessionMoves(t, sub, stop)
	started := time.Now()
	var forked sessions.ResumeResult
	if err := L.call(t, "sessions.fork", wire.SessionResumeParams{ID: "L:claude:" + sid, Machine: "M"}, &forked); err != nil {
		t.Fatalf("fork to M over a slow link: %v", err)
	}
	took := time.Since(started)
	host.ServeDelay.Store(0)
	if took < 3*time.Second {
		t.Fatalf("the transfer took %v: not longer than one relay request, the test proves nothing", took)
	}
	if !strings.HasPrefix(forked.ID, "M/") || forked.Note != "" {
		t.Fatalf("forked %+v", forked)
	}
	eventually(t, "M forked it", func() bool { return strings.Contains(M.argvs(forked.ID), "--fork-session") })
	// The conversation is whole on M.
	placed := filepath.Join(M.home, ".claude", "projects", handoff.ClaudeSlug(M.project), sid+".jsonl")
	if st, err := os.Stat(placed); err != nil || st.Size() < size {
		t.Fatalf("placed transcript: %v %v", st, err)
	}
	// M cloned the project from its remote; the uncommitted work came.
	if refs := gitIn(t, M.project, "for-each-ref", "refs/remotes/origin"); !strings.Contains(refs, "refs/remotes/origin/main") {
		t.Fatalf("M did not clone from the remote: %q", refs)
	}
	if data, err := os.ReadFile(filepath.Join(M.project, "a.txt")); err != nil || !strings.Contains(string(data), "UNCOMMITTED-WORK") {
		t.Fatalf("uncommitted work: %q %v", data, err)
	}
	// The progress, on L's subscription.
	time.Sleep(200 * time.Millisecond)
	close(stop)
	got := <-moves
	steps := map[string]bool{}
	var transfer []wire.Moving
	for _, m := range got {
		if m.ID != "L:claude:"+sid || m.To != "M" || !m.Fork {
			t.Fatalf("a note of another transfer: %+v", m)
		}
		steps[m.Step] = true
		if m.Step == wire.MoveTransfer {
			transfer = append(transfer, m)
		}
	}
	for _, s := range []string{wire.MoveCheckpoint, wire.MoveTransfer, wire.MoveWorktree, wire.MoveResume, wire.MoveDone} {
		if !steps[s] {
			t.Fatalf("no %s step: %+v", s, got)
		}
	}
	if len(transfer) < 2 {
		t.Fatalf("transfer progress: %+v", transfer)
	}
	lastT := transfer[len(transfer)-1]
	if lastT.Total == 0 || lastT.Total >= size || lastT.Bytes > lastT.Total {
		t.Fatalf("the transfer's size %d (the transcript is %d: compressed?) done %d", lastT.Total, size, lastT.Bytes)
	}
	for i := 1; i < len(transfer); i++ {
		if transfer[i].Percent < transfer[i-1].Percent {
			t.Fatalf("percent went back: %+v", transfer)
		}
	}
	if got[len(got)-1].Step != wire.MoveDone || got[len(got)-1].Agent != forked.ID {
		t.Fatalf("last note %+v", got[len(got)-1])
	}
	t.Logf("30 MB transcript: %d bytes travelled in %v", lastT.Total, took)
}

// When the home's transfer stops moving, or the home is away, the
// session comes from this Mac's copy (the mirror); when that cannot be
// used either, the error says why for both ways.
func TestSessionsForkFallsBackToTheMirror(t *testing.T) {
	w := newWorld(t, worldOptions{hubTimeout: 3 * time.Second})
	L, M := w.L, w.M
	L.waitLinked(t, "M")
	M.waitLinked(t, "L")
	const (
		sidR   = "aaaaaaa1-0000-4000-8000-0000000000a1" // recent: mirrored on M
		sidOld = "aaaaaaa2-0000-4000-8000-0000000000a2" // 20 days old: not mirrored
	)
	writeClaude(t, L.home, L.project, sidR, "", time.Now().Add(-time.Hour), "Mirror me", "Copied to the mini.")
	oldPath := writeClaude(t, L.home, L.project, sidOld, "", time.Now().Add(-20*24*time.Hour), "Old work", "Long ago.")
	old := time.Now().Add(-20 * 24 * time.Hour)
	os.Chtimes(oldPath, old, old)
	eventually(t, "M mirrors L's recent session", func() bool {
		s, ok := M.sessionByID(t, "L:claude:"+sidR)
		return ok && len(s.Mirrored) == 2
	})
	eventually(t, "M lists the old one", func() bool { _, ok := M.sessionByID(t, "L:claude:"+sidOld); return ok })

	// L is reachable, but its transfer stalls: no chunk gets through
	// within the idle time, so M falls back to its copy.
	oldIdle := client.TransferIdle
	client.TransferIdle = time.Second
	host.ServeDelay.Store(int64(4 * time.Second))
	var res sessions.ResumeResult
	err := M.call(t, "sessions.fork", wire.SessionResumeParams{ID: "L:claude:" + sidR, Machine: "M"}, &res)
	host.ServeDelay.Store(0)
	client.TransferIdle = oldIdle
	if err != nil {
		t.Fatalf("fork with a stalled transfer: %v", err)
	}
	if !strings.HasPrefix(res.ID, "M/") || !strings.Contains(res.Note, "the transfer from L failed") || !strings.Contains(res.Note, "this Mac's copy") {
		t.Fatalf("stalled fork %+v", res)
	}
	eventually(t, "M forked it from its copy", func() bool { return strings.Contains(M.argvs(res.ID), "--fork-session") })

	// L goes away: a fork on M from its copy.
	L.stop()
	eventually(t, "M lost L", func() bool { return M.d.Fleet.Route("L") == "" })
	if err := M.call(t, "sessions.fork", wire.SessionResumeParams{ID: "L:claude:" + sidR, Machine: "M"}, &res); err != nil {
		t.Fatalf("fork with L away: %v", err)
	}
	if !strings.HasPrefix(res.ID, "M/") || !strings.Contains(res.Note, "L is not reachable") {
		t.Fatalf("offline fork %+v", res)
	}
	eventually(t, "M forked it offline", func() bool { return strings.Contains(M.argvs(res.ID), "--fork-session") })

	// No copy of the old one: the error names both reasons.
	err = M.call(t, "sessions.fork", wire.SessionResumeParams{ID: "L:claude:" + sidOld, Machine: "M"}, nil)
	var we *wire.Error
	if !errors.As(err, &we) || !strings.Contains(we.Message, "could not get the session from L: L is not connected") ||
		!strings.Contains(we.Message, "this Mac's copy: there is none") {
		t.Fatalf("both ways failed: %v", err)
	}
}
