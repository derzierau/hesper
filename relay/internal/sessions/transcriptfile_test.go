package sessions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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
