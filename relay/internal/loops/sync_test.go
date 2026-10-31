package loops

import (
	"strings"
	"testing"

	"github.com/derzierau/hesper/relay/internal/agents"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

// pullAll pulls everything from host into s, as internal/remote does.
func pullAll(t *testing.T, s, host *Store, since int64, epoch string) (int64, string, int) {
	t.Helper()
	batches := 0
	for {
		res := host.Pull(PullParams{Since: since, Epoch: epoch, From: s.Node()})
		s.Merge(roundTrip(t, res.Records))
		since, epoch = res.Next, res.Epoch
		batches++
		if !res.More {
			return since, epoch, batches
		}
	}
}

func TestPullBatchesEchoAndEpoch(t *testing.T) {
	old := maxBatch
	maxBatch = 4 << 10
	defer func() { maxBatch = old }()
	a, b := openTest(t, "", "n-a", nil), openTest(t, "", "n-b", nil)
	var ids []string
	for range 20 {
		id := newLoop(a, strings.Repeat("long title ", 40))
		ids = append(ids, id)
	}
	seq, epoch, batches := pullAll(t, b, a, 0, "")
	if batches < 2 || len(b.loops) != 20 || seq != a.Hint().Seq || epoch != a.Hint().Epoch {
		t.Fatalf("%d batches, %d loops, next %d of %d", batches, len(b.loops), seq, a.Hint().Seq)
	}
	// Nothing new: an empty answer.
	if res := a.Pull(PullParams{Since: seq, Epoch: epoch, From: "n-b"}); len(res.Records) != 0 || res.More {
		t.Fatalf("delta %+v", res)
	}
	// What came from a is not sent back to a (nor records never written).
	if res := b.Pull(PullParams{From: "n-a"}); len(res.Records) != 0 {
		t.Fatalf("echo: %d records", len(res.Records))
	}
	// One change: one record.
	addItem(t, a, ids[3], "new step")
	if res := a.Pull(PullParams{Since: seq, Epoch: epoch, From: "n-b"}); len(res.Records) != 1 || res.Records[0].Loop.ID != ids[3] {
		t.Fatalf("delta %+v", res.Records)
	}
	// Another epoch (a new store there) starts from the beginning.
	if res := a.Pull(PullParams{Since: seq, Epoch: "other", From: "n-b"}); len(res.Records) == 0 {
		t.Fatal("a new epoch got nothing")
	}
}

func TestSyncBothWays(t *testing.T) {
	a, b := openTest(t, "", "n-a", nil), openTest(t, "", "n-b", nil)
	la := a.Assign(agents.AssignInfo{Ref: "n-a/x", Agent: wire.Agent{Name: "on a"}})
	lb := b.Assign(agents.AssignInfo{Ref: "n-b/y", Agent: wire.Agent{Name: "on b"}})
	// b pulls a; b pushes to a.
	pullAll(t, b, a, 0, "")
	res := b.Pull(PullParams{From: a.Node()})
	if got := a.Sync(SyncParams{From: b.Node(), Records: roundTrip(t, res.Records)}); !got.Changed || got.Node != "n-a" {
		t.Fatalf("sync %+v", got)
	}
	for _, s := range []*Store{a, b} {
		if s.LoopOf("n-a/x") != la || s.LoopOf("n-b/y") != lb {
			t.Fatalf("%s: memberships %q %q", s.node, s.LoopOf("n-a/x"), s.LoopOf("n-b/y"))
		}
	}
	if a.Sync(SyncParams{Records: roundTrip(t, res.Records)}).Changed {
		t.Fatal("a second sync changed something")
	}
}
