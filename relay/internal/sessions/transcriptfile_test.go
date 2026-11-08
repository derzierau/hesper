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

// Items past TranscriptPageBytes page in several calls, each within the
// cap, and together they are the whole transcript.
func TestTranscriptPageBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "big.jsonl")
	var data string
	for i := range 60 {
		data += codexMsg(strings.Repeat(string(rune('a'+i%26)), 20<<10))
	}
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	all, _ := fixtureItems(t, wire.KindCodex, path)
	f, err := openTranscript(path, wire.KindCodex)
	if err != nil {
		t.Fatal(err)
	}
	var got []wire.TranscriptItem
	pages, before := 0, int64(0)
	for {
		p, err := f.page(before, wire.TranscriptMaxLimit)
		if err != nil {
			t.Fatal(err)
		}
		pages++
		if n := itemsSize(p.Items); n > TranscriptPageBytes {
			t.Fatalf("page %d: %d bytes of items", pages, n)
		}
		got = append(p.Items, got...)
		if !p.More {
			break
		}
		before = p.Items[0].Seq
	}
	if pages < 3 {
		t.Errorf("%d pages, want 3 or more", pages)
	}
	sameItems(t, got, all)
}

// A page while the first pass still runs answers busy; once it is done,
// the page comes.
func TestTranscriptIndexBusy(t *testing.T) {
	s, a, _, _ := followEnv(t, "one", "two")
	oldWait, oldHook := transcriptWait, transcriptIndexHook
	t.Cleanup(func() { transcriptWait, transcriptIndexHook = oldWait, oldHook })
	release := make(chan struct{})
	transcriptWait = 20 * time.Millisecond
	transcriptIndexHook = func() { <-release }
	var we *wire.Error
	if _, err := s.Transcript(context.Background(), a, 0, 0); !errors.As(err, &we) || we.Code != wire.CodeBusy {
		t.Fatalf("page during the first pass: %v, want busy", err)
	}
	close(release)
	deadline := time.Now().Add(2 * time.Second)
	for {
		p, err := s.Transcript(context.Background(), a, 0, 0)
		if err == nil {
			if len(p.Items) != 2 || p.Items[1].Text != "two" {
				t.Fatalf("page %+v", p)
			}
			return
		}
		if !errors.As(err, &we) || we.Code != wire.CodeBusy || time.Now().After(deadline) {
			t.Fatalf("page after the first pass: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// The index outlives its last user by IndexKeep: a later page reuses
// it; after IndexKeep it is gone.
func TestTranscriptIndexKeep(t *testing.T) {
	s, a, _, _ := followEnv(t, "one")
	old := IndexKeep
	t.Cleanup(func() { IndexKeep = old })
	IndexKeep = time.Hour
	key := a.Kind + ":" + a.SessionID
	index := func() *transcriptFile {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.transcripts[key]
	}
	if _, err := s.Transcript(context.Background(), a, 0, 0); err != nil {
		t.Fatal(err)
	}
	first := index()
	if first == nil {
		t.Fatal("no index after a page")
	}
	// A stream comes and goes; the page after it finds the same index.
	_, _, cancel := follow(t, s, a, 0, 0)
	cancel()
	if _, err := s.Transcript(context.Background(), a, 0, 0); err != nil {
		t.Fatal(err)
	}
	if index() != first {
		t.Fatal("the index was built again")
	}
	IndexKeep = 10 * time.Millisecond
	if _, err := s.Transcript(context.Background(), a, 0, 0); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for index() != nil {
		if time.Now().After(deadline) {
			t.Fatal("index kept past IndexKeep")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
