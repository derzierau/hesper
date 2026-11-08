package sessions

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

func TestFollowBackfillIsBatched(t *testing.T) {
	texts := make([]string, 130)
	for i := range texts {
		texts[i] = strings.Repeat("x", 1024)
	}
	_, a, path, offs := followEnv(t, texts...)
	f, err := openTranscript(path, a.Kind)
	if err != nil {
		t.Fatal(err)
	}
	items, next, gen, err := f.backfill(offs[1], 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) == 0 || len(items) > 64 {
		t.Fatalf("first backfill held %d items, want 1..64", len(items))
	}
	if next >= f.size {
		t.Fatalf("first backfill advanced to %d of %d before emitting the rest", next, f.size)
	}
	got := append([]wire.TranscriptItem(nil), items...)
	for next < f.size {
		items, next, gen, err = f.tail(next, gen, 1)
		if err != nil {
			t.Fatal(err)
		}
		if len(items) == 0 || len(items) > 64 {
			t.Fatalf("tail batch held %d items, want 1..64", len(items))
		}
		got = append(got, items...)
	}
	if len(got) != len(texts)-2 {
		t.Fatalf("emitted %d items, want %d", len(got), len(texts)-2)
	}
	for i, item := range got {
		if item.Seq != offs[i+2] || item.Text != texts[i+2] {
			t.Fatalf("item %d: %+v, want seq %d", i, item, offs[i+2])
		}
	}
}

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
	s, a, path, _ := followEnv(t, "m1", "m2", "m3")
	items, _, _ := follow(t, s, a, 0, 10)
	for range 3 {
		nextItem(t, items, time.Second)
	}
	restarted := func(when string) {
		t.Helper()
		if it := nextItem(t, items, time.Second); it.Kind != wire.ItemSystem || it.Text != "Transcript restarted" || it.Seq != -1 {
			t.Fatalf("%s: %+v, want the restart marker", when, it)
		}
	}
	// Truncated to 0 and two items written in one step (a rename, so no
	// poll sees it empty): the marker, then the rewritten file's items.
	n1 := codexMsg("n1")
	tmp := path + ".new"
	if err := os.WriteFile(tmp, []byte(n1+codexMsg("n2")), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
	restarted("after a rewrite")
	for _, want := range []wire.TranscriptItem{{Text: "n1", Seq: 0}, {Text: "n2", Seq: int64(len(n1))}} {
		if it := nextItem(t, items, time.Second); it.Text != want.Text || it.Seq != want.Seq {
			t.Fatalf("after a rewrite: %+v, want %s at %d", it, want.Text, want.Seq)
		}
	}
	// Truncated to 0, then two items appended.
	if err := os.Truncate(path, 0); err != nil {
		t.Fatal(err)
	}
	restarted("after truncate")
	appendTo(t, path, codexMsg("new")+codexMsg("new2"))
	if it := nextItem(t, items, time.Second); it.Text != "new" || it.Seq != 0 {
		t.Fatalf("after restart: %+v, want new at 0", it)
	}
	if it := nextItem(t, items, time.Second); it.Text != "new2" || it.Seq != int64(len(codexMsg("new"))) {
		t.Fatalf("after restart: %+v, want new2", it)
	}
}

func TestFollowStopsOnCancel(t *testing.T) {
	s, a, _, _ := followEnv(t, "m1")
	// Polls long enough that "within two" is not a race with the
	// scheduler under -race.
	FollowPoll = 50 * time.Millisecond
	oldKeep := IndexKeep
	IndexKeep = time.Millisecond
	t.Cleanup(func() { IndexKeep = oldKeep })
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
	// Dropped IndexKeep after the stream (TestTranscriptIndexKeep).
	deadline := time.Now().Add(time.Second)
	for {
		s.mu.Lock()
		n = len(s.transcripts)
		s.mu.Unlock()
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("after cancel: %d transcripts indexed, want 0", n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
