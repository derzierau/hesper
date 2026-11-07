package sessions

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/pkg/protocol"
)

const pullRequester = "n-0000000000000req"

// noise is n letters a zstd frame cannot shrink much (an index full of
// pasted logs and hashes compresses like this, not like prose).
func noise(r *rand.Rand, n int) string {
	const letters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789+/"
	b := make([]byte, n)
	for i := range b {
		b[i] = letters[r.Intn(len(letters))]
	}
	return string(b)
}

// pullRecord is entry i of this Mac's index with about size letters of
// text; echo: an entry whose every stamp is the requester's.
func pullRecord(s *Service, r *rand.Rand, i, size int, echo bool) *Record {
	node := s.db.node
	if echo {
		node = pullRequester
	}
	return &Record{Node: node, Home: "L", Kind: "claude", SID: fmt.Sprintf("%08x-0000-4000-8000-%012x", i, i),
		Meta:  Meta{Cwd: "/work/p", Title: fmt.Sprintf("entry %d", i), Prompts: noise(r, size/3), Answers: noise(r, size-size/3), LastActivity: time.Now().UnixMilli()},
		MetaS: Stamp{T: time.Now().UnixMilli(), N: node}}
}

// pullEverything walks sessions.pull as a peer does and returns the
// entries received; every page goes through check first.
func pullEverything(t *testing.T, s *Service, check func(pullResult)) []*Record {
	t.Helper()
	_, dec := codecs()
	var got []*Record
	var since int64
	epoch := ""
	for page := 0; ; page++ {
		if page > 1000 {
			t.Fatal("sessions.pull never ends")
		}
		res, err := s.pull(pullParams{Since: since, Epoch: epoch, From: pullRequester})
		if err != nil {
			t.Fatal(err)
		}
		check(res)
		var recs []*Record
		if len(res.Data) > 0 {
			data, err := dec.DecodeAll(res.Data, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(data, &recs); err != nil {
				t.Fatal(err)
			}
		}
		if len(recs) != res.Count {
			t.Fatalf("page %d: count %d, %d entries", page, res.Count, len(recs))
		}
		got = append(got, recs...)
		since, epoch = res.Next, res.Epoch
		if !res.More {
			return got
		}
	}
}

// Every page of a pull fits the relay's message limit once it is what
// travels: base64 in the result's JSON, sealed and base64 again in the
// end-to-end channel's frame. A page of 600 KiB compressed was 1.07 MB
// there, and the relay dropped the host's connection for it.
func TestPullPagesFitTheRelaysMessageLimit(t *testing.T) {
	e := newEnv(t)
	s := e.open(Options{})
	// The first 50 entries (a page halved from 400) are sized to pack to
	// ~600 KB: just under the old budget, well over what fits sealed.
	enc, _ := codecs()
	var r *rand.Rand
	var recs []*Record
	// zstd's ratio on these is not linear in their length: the size is
	// found by trying, not computed.
	size := 14000
	for ; ; size += 100 {
		r = rand.New(rand.NewSource(1))
		recs = recs[:0]
		for i := 0; i < 50; i++ {
			recs = append(recs, pullRecord(s, r, i, size, false))
		}
		data, _ := json.Marshal(recs)
		packed := len(enc.EncodeAll(data, nil))
		if packed > 592_000 && packed <= 612_000 {
			break
		}
		if size > 24000 {
			t.Fatalf("cannot size the entries: 50 pack to %d bytes", packed)
		}
	}
	echoes := 0
	for i := 50; i < 130; i++ {
		echo := i%13 == 5
		if echo {
			echoes++
		}
		recs = append(recs, pullRecord(s, r, i, size, echo))
	}
	if _, err := s.apply(recs, false); err != nil {
		t.Fatal(err)
	}
	largest := 0
	got := pullEverything(t, s, func(res pullResult) {
		largest = max(largest, len(res.Data))
		if sealed := len(res.Data)*4/3*4/3 + 4096; sealed >= protocol.MaxMessageBytes {
			t.Errorf("page of %d entries, %d bytes compressed: ~%d bytes sealed, over the relay's %d", res.Count, len(res.Data), sealed, protocol.MaxMessageBytes)
		}
	})
	t.Logf("largest page %d bytes compressed", largest)
	if want := len(recs) - echoes; len(got) != want {
		t.Fatalf("pulled %d entries, want %d", len(got), want)
	}
}

// An entry over the budget on its own is left out (it would break the
// link, not just this pull); the entries around it still arrive and the
// pull ends.
func TestPullSkipsAnEntryOverTheBudget(t *testing.T) {
	old := pullBytes
	pullBytes = 8 << 10
	defer func() { pullBytes = old }()
	e := newEnv(t)
	s := e.open(Options{})
	r := rand.New(rand.NewSource(2))
	var recs []*Record
	for i := 0; i < 7; i++ {
		size := 1000
		if i == 3 {
			size = 40000
		}
		recs = append(recs, pullRecord(s, r, i, size, false))
	}
	if _, err := s.apply(recs, false); err != nil {
		t.Fatal(err)
	}
	got := pullEverything(t, s, func(res pullResult) {
		if len(res.Data) > pullBytes {
			t.Errorf("page of %d entries is %d bytes, over the budget of %d", res.Count, len(res.Data), pullBytes)
		}
	})
	if len(got) != 6 {
		t.Fatalf("pulled %d entries, want the 6 within the budget", len(got))
	}
	for _, rec := range got {
		if rec.SID == recs[3].SID {
			t.Fatal("the entry over the budget was sent")
		}
	}
}
