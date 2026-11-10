package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

func TestLongMessagePageAndStreamStayBounded(t *testing.T) {
	long := strings.Repeat("a", 2<<20)
	s, a, path, _ := followEnv(t, long)
	f, err := openTranscript(path, wire.KindCodex)
	if err != nil {
		t.Fatal(err)
	}
	p, err := f.page(0, 1)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(p)
	if len(b) > TranscriptPageBytes || len(p.Items) != 1 || !p.Items[0].Truncated {
		t.Fatalf("page: %d JSON bytes, %d items", len(b), len(p.Items))
	}
	items, _, cancel := follow(t, s, a, 0, 1)
	defer cancel()
	it := nextItem(t, items, itemWait)
	b, _ = json.Marshal(it)
	if len(b)+1 >= 1<<20 || !it.Truncated {
		t.Fatalf("stream item: %d JSON bytes, truncated %v", len(b)+1, it.Truncated)
	}
}

func TestPagePaginatesEveryItemWithinOneLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "many.jsonl")
	var blocks []map[string]any
	for i := range 200 {
		blocks = append(blocks, map[string]any{"type": "text", "text": strings.Repeat(string(rune('a'+i%26)), 8000)})
	}
	line, _ := json.Marshal(map[string]any{"type": "assistant", "message": map[string]any{"content": blocks}})
	if err := os.WriteFile(path, append(line, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := openTranscript(path, wire.KindClaude)
	if err != nil {
		t.Fatal(err)
	}
	var got []wire.TranscriptItem
	before := int64(0)
	pages := 0
	for page := 0; page < 100; page++ {
		pages++
		p, err := f.page(before, 1000)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(p)
		if len(b) > TranscriptPageBytes || len(p.Items) == 0 {
			t.Fatalf("page %d: %d JSON bytes, %d items", page, len(b), len(p.Items))
		}
		got = append(p.Items, got...)
		if !p.More {
			break
		}
		before = p.Items[0].Seq
	}
	if len(got) != 200 || pages < 2 {
		t.Fatalf("paged %d of 200 items in %d pages", len(got), pages)
	}
	for i, it := range got {
		if it.Seq != int64(i) || !it.Truncated {
			t.Fatalf("item %d: seq %d, truncated %v", i, it.Seq, it.Truncated)
		}
	}
}

func TestTranscriptTextChunksReassembleLongMessage(t *testing.T) {
	long := strings.Repeat("é", 40000) + strings.Repeat("a", 1<<20)
	s, a, _, _ := followEnv(t, long)
	p, err := s.Transcript(context.Background(), a, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Items) != 1 || !p.Items[0].Truncated {
		t.Fatal("long message has no preview")
	}
	var got strings.Builder
	var offset int
	for part := 0; part < 100; part++ {
		chunk, err := s.TranscriptText(context.Background(), a, p.Items[0].Seq, offset, 1<<20)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(chunk)
		if len(b) >= 1<<20 || len(chunk.Text) > 64<<10 || chunk.Next <= offset {
			t.Fatalf("part %d: %d JSON bytes, next %d", part, len(b), chunk.Next)
		}
		got.WriteString(chunk.Text)
		offset = chunk.Next
		if !chunk.More {
			if got.String() != long {
				t.Fatalf("retrieved %d of %d bytes", got.Len(), len(long))
			}
			return
		}
	}
	t.Fatal("text retrieval did not finish")
}

func TestTranscriptTextRejectsWrongItemAndRuneOffset(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mixed.jsonl")
	line := `{"type":"assistant","message":{"content":[{"type":"text","text":"écho"},{"type":"tool_use","id":"call-1","name":"Read","input":{}}]}}` + "\n"
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := openTranscript(path, wire.KindClaude)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		seq    int64
		offset int
		limit  int
		code   string
	}{
		{0, 1, 0, wire.CodeInvalid}, // in the middle of é
		{0, -1, 0, wire.CodeInvalid},
		{0, 0, 1, wire.CodeInvalid},
		{0, 0, 2, wire.CodeInvalid},
		{0, 0, 3, wire.CodeInvalid},
		{1, 0, 0, wire.CodeNotFound}, // a tool has no full message text
		{2, 0, 0, wire.CodeNotFound},
	} {
		_, err := f.text(c.seq, c.offset, c.limit)
		var we *wire.Error
		if !errors.As(err, &we) || we.Code != c.code {
			t.Errorf("seq %d offset %d: %v, want %s", c.seq, c.offset, err, c.code)
		}
	}
	chunk, err := f.text(0, 0, 4)
	if err != nil || chunk.Text != "éch" || chunk.Next != 4 || !chunk.More {
		t.Fatalf("first UTF-8 chunk: %+v, %v", chunk, err)
	}
	chunk, err = f.text(0, chunk.Next, 4)
	if err != nil || chunk.Text != "o" || chunk.More {
		t.Fatalf("last UTF-8 chunk: %+v, %v", chunk, err)
	}
}

func TestToolMetadataCannotMakeUnpageableItem(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tool.jsonl")
	line, _ := json.Marshal(map[string]any{"type": "assistant", "message": map[string]any{"content": []map[string]any{
		{"type": "tool_use", "id": strings.Repeat("i", 1<<20), "name": strings.Repeat("n", 1<<20), "input": map[string]any{}},
	}}})
	if err := os.WriteFile(path, append(line, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := openTranscript(path, wire.KindClaude)
	if err != nil {
		t.Fatal(err)
	}
	p, err := f.page(0, 1)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(p)
	if len(p.Items) != 1 || len(b) > TranscriptPageBytes || p.More {
		t.Fatalf("one tool item: %d JSON bytes, %d items, more %v", len(b), len(p.Items), p.More)
	}
}

func TestTranscriptTextEscapingFitsRelayReply(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.jsonl")
	line, _ := json.Marshal(map[string]any{"type": "user", "message": map[string]any{"content": strings.Repeat("\x00", 100000)}})
	if err := os.WriteFile(path, append(line, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := openTranscript(path, wire.KindClaude)
	if err != nil {
		t.Fatal(err)
	}
	p, err := f.page(0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Items) != 1 || !p.Items[0].Truncated {
		t.Fatalf("control message preview: %d items", len(p.Items))
	}
	chunk, err := f.text(p.Items[0].Seq, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(chunk)
	if len(b) >= 1<<20 || !chunk.More {
		t.Fatalf("escaped chunk: %d JSON bytes, more %v", len(b), chunk.More)
	}
}

// Items past TranscriptPageBytes page in several calls, each within the
// cap, and together they are the whole transcript.
func TestTranscriptPageBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "big.jsonl")
	var data string
	for i := range 260 {
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
		b, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		if len(b) > TranscriptPageBytes {
			t.Fatalf("page %d: %d serialized bytes", pages, len(b))
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
