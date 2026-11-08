package sessions

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// codexMsg is a rollout line with one assistant message.
func codexMsg(text string) string {
	return `{"timestamp":"2026-10-02T09:00:08.000Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"` + text + `"}]}}` + "\n"
}

// followEnv is a service with a Codex rollout of one message per text,
// the rollout's path and the byte offset of each line. FollowPoll is
// 5 ms until the test ends.
func followEnv(t *testing.T, texts ...string) (*Service, wire.Agent, string, []int64) {
	t.Helper()
	old := FollowPoll
	FollowPoll = 5 * time.Millisecond
	t.Cleanup(func() { FollowPoll = old })
	e := newEnv(t)
	s := e.open(Options{})
	const sid = "0199a000-0000-7000-8000-00000000f011"
	dir := filepath.Join(e.codex, "sessions", "2026", "11", "08")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "rollout-2026-11-08T10-00-00-"+sid+".jsonl")
	var data string
	var offs []int64
	for _, x := range texts {
		offs = append(offs, int64(len(data)))
		data += codexMsg(x)
	}
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	return s, wire.Agent{ID: "a1", Kind: wire.KindCodex, SessionID: sid}, path, offs
}

// follow runs FollowTranscript until the test ends; the items arrive on
// the first channel, what it returns on the second.
func follow(t *testing.T, s *Service, a wire.Agent, after int64, limit int) (<-chan wire.TranscriptItem, <-chan error, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	items, done, fin := make(chan wire.TranscriptItem, 100), make(chan error, 1), make(chan struct{})
	go func() {
		done <- s.FollowTranscript(ctx, a, after, limit, func(it wire.TranscriptItem) error {
			items <- it
			return nil
		})
		close(fin)
	}()
	t.Cleanup(func() { cancel(); <-fin })
	return items, done, cancel
}

func appendTo(t *testing.T, path, s string) {
	t.Helper()
	fh, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	if _, err := fh.WriteString(s); err != nil {
		t.Fatal(err)
	}
}

func nextItem(t *testing.T, items <-chan wire.TranscriptItem, within time.Duration) wire.TranscriptItem {
	t.Helper()
	select {
	case it := <-items:
		return it
	case <-time.After(within):
		t.Fatalf("no item within %v", within)
		return wire.TranscriptItem{}
	}
}

func noItem(t *testing.T, items <-chan wire.TranscriptItem, within time.Duration) {
	t.Helper()
	select {
	case it := <-items:
		t.Fatalf("unexpected item %+v", it)
	case <-time.After(within):
	}
}

func TestFollowBackfillThenTail(t *testing.T) {
	s, a, path, offs := followEnv(t, "m1", "m2", "m3", "m4", "m5")
	items, _, _ := follow(t, s, a, 0, 2)
	for i, want := range []string{"m4", "m5"} {
		if it := nextItem(t, items, time.Second); it.Text != want || it.Seq != offs[3+i] {
			t.Fatalf("backfill %d: %+v, want %s at %d", i, it, want, offs[3+i])
		}
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	appendTo(t, path, codexMsg("m6"))
	if it := nextItem(t, items, 100*time.Millisecond); it.Text != "m6" || it.Seq != st.Size() {
		t.Fatalf("tail: %+v, want m6 at %d", it, st.Size())
	}
}

func TestFollowAfter(t *testing.T) {
	s, a, _, offs := followEnv(t, "m1", "m2", "m3", "m4", "m5")
	// Not capped by limit: every item after After.
	items, _, _ := follow(t, s, a, offs[2], 1)
	for i, want := range []string{"m4", "m5"} {
		if it := nextItem(t, items, time.Second); it.Text != want || it.Seq != offs[3+i] {
			t.Fatalf("after %d: %+v, want %s at %d", i, it, want, offs[3+i])
		}
	}
	noItem(t, items, 50*time.Millisecond)
}

func TestFollowPartialLine(t *testing.T) {
	s, a, path, _ := followEnv(t, "m1")
	items, _, _ := follow(t, s, a, 0, 10)
	nextItem(t, items, time.Second)
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	line := codexMsg("half")
	appendTo(t, path, line[:len(line)/2])
	noItem(t, items, 50*time.Millisecond)
	appendTo(t, path, line[len(line)/2:])
	if it := nextItem(t, items, time.Second); it.Text != "half" || it.Seq != st.Size() {
		t.Fatalf("completed line: %+v, want half at %d", it, st.Size())
	}
	noItem(t, items, 20*time.Millisecond)
}

func TestFollowTruncate(t *testing.T) {
	s, a, path, _ := followEnv(t, "m1", "m2")
	items, _, _ := follow(t, s, a, 0, 10)
	nextItem(t, items, time.Second)
	nextItem(t, items, time.Second)
	if err := os.Truncate(path, 0); err != nil {
		t.Fatal(err)
	}
	if it := nextItem(t, items, time.Second); it.Kind != wire.ItemSystem || it.Text != "Transcript restarted" || it.Seq != -1 {
		t.Fatalf("after truncate: %+v", it)
	}
	appendTo(t, path, codexMsg("new"))
	if it := nextItem(t, items, time.Second); it.Text != "new" || it.Seq != 0 {
		t.Fatalf("after restart: %+v, want new at 0", it)
	}
}

func TestFollowStopsOnCancel(t *testing.T) {
	s, a, _, _ := followEnv(t, "m1")
	// Polls long enough that "within two" is not a race with the
	// scheduler under -race.
	FollowPoll = 50 * time.Millisecond
	items, done, cancel := follow(t, s, a, 0, 10)
	nextItem(t, items, time.Second)
	s.mu.Lock()
	n := len(s.transcripts)
	s.mu.Unlock()
	if n != 1 {
		t.Fatalf("while following: %d transcripts indexed, want 1", n)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("returned %v, want context.Canceled", err)
		}
	case <-time.After(2 * FollowPoll):
		t.Fatal("still following two polls after cancel")
	}
	s.mu.Lock()
	n = len(s.transcripts)
	s.mu.Unlock()
	if n != 0 {
		t.Fatalf("after cancel: %d transcripts indexed, want 0", n)
	}
}
