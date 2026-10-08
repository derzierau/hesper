package attachtty

import (
	"testing"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

func TestScrollParser(t *testing.T) {
	var sp scrollParser
	got := sp.feed([]byte("S+5;S-2;S=0;garbageS+1"))
	if len(got) != 3 || got[0].Delta != 5 || got[1].Delta != -2 || got[2].Offset == nil || *got[2].Offset != 0 {
		t.Fatalf("%+v", got)
	}
	// Split across reads.
	got = sp.feed([]byte("2;"))
	if len(got) != 1 || got[0].Delta != 12 {
		t.Fatalf("split: %+v", got)
	}
	if got := sp.feed([]byte("Sx;S=;")); len(got) != 0 {
		t.Fatalf("bad commands: %+v", got)
	}
	big := make([]byte, 100)
	sp.feed(big)
	if len(sp.buf) != 0 {
		t.Fatal("junk kept")
	}
	if s := string(scrollTitle([]byte(`{"offset":3}`))); s != "\x1b]2;hesper-scroll {\"offset\":3}\a" {
		t.Fatalf("%q", s)
	}
	_ = wire.Scroll{}
}
