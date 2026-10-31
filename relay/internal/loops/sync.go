package loops

import (
	"encoding/json"
	"sort"
)

// Sync between Macs (internal/host, internal/remote), like the shared
// history's: a host sends a small hint (node, epoch, newest sequence
// number) on every link when it opens and after changes; a controller
// that is behind pulls the records changed since what it has
// (loops.pull) and pushes its own changes (loops.sync). Batches hold at
// most 512 KiB of records; records merge one by one (merge.go), so the
// exchange settles after a round. Records whose every write came from the
// asking Mac are not sent back. The requests travel signed inside the
// end-to-end channel: the relay sees none of it. Separate from
// projects.sync, which older hosts decode strictly.

// maxBatch bounds one pull's or push's records (JSON bytes).
var maxBatch = 512 << 10

// Hint is a host's link event: its node, epoch and newest sequence
// number.
type Hint struct {
	Node  string `json:"node"`
	Epoch string `json:"epoch"`
	Seq   int64  `json:"seq"`
}

// PullParams are loops.pull's: the changes after Since of epoch Epoch (an
// other epoch: from the start), for the Mac From.
type PullParams struct {
	Since int64  `json:"since"`
	Epoch string `json:"epoch,omitempty"`
	From  string `json:"from,omitempty"`
}

// PullResult is loops.pull's: a batch of records and the sequence number
// to ask after next (More: there are more).
type PullResult struct {
	Node    string   `json:"node"`
	Epoch   string   `json:"epoch"`
	Next    int64    `json:"next"`
	More    bool     `json:"more,omitempty"`
	Records []Record `json:"records"`
}

// SyncParams are loops.sync's: records from the Mac From.
type SyncParams struct {
	From    string   `json:"from,omitempty"`
	Records []Record `json:"records"`
}

// SyncResult is loops.sync's: the host's hint after the merge.
type SyncResult struct {
	Hint
	Changed bool `json:"changed,omitempty"`
}

// Hint is this Mac's current hint.
func (s *Store) Hint() Hint {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Hint{Node: s.node, Epoch: s.epoch, Seq: s.seq}
}

// HostHint is the hint as a link event carries it.
func (s *Store) HostHint() json.RawMessage {
	data, _ := json.Marshal(s.Hint())
	return data
}

// Pull answers loops.pull (and makes a controller's pushes).
func (s *Store) Pull(p PullParams) PullResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	res := PullResult{Node: s.node, Epoch: s.epoch, Records: []Record{}}
	since := p.Since
	if p.Epoch != s.epoch {
		since = 0
	}
	type change struct {
		key string
		seq int64
	}
	var changes []change
	for k, q := range s.seqs {
		if q > since {
			changes = append(changes, change{k, q})
		}
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].seq < changes[j].seq })
	res.Next = since
	size := 0
	for i, c := range changes {
		r := s.recordLocked(c.key)
		if r == nil {
			res.Next = c.seq
			continue
		}
		if echo(r, p.From) {
			res.Next = c.seq
			continue
		}
		data, _ := json.Marshal(r)
		if size > 0 && size+len(data) > maxBatch {
			res.More = i < len(changes)
			break
		}
		size += len(data)
		res.Records = append(res.Records, *r)
		res.Next = c.seq
	}
	if !res.More {
		res.Next = max(res.Next, s.seq)
	}
	return res
}

// echo: every write in r came from node (it would only go back there).
func echo(r *Record, node string) bool {
	if node == "" {
		return false
	}
	all := true
	r.stamps(func(st stamp) {
		if st.N != "" && st.N != node {
			all = false
		}
	})
	return all
}

// Sync answers loops.sync: merge the records, report the hint.
func (s *Store) Sync(p SyncParams) SyncResult {
	changed := s.Merge(p.Records)
	return SyncResult{Hint: s.Hint(), Changed: changed}
}

// RefOf is the ref of an agent id as this Mac names it ("" when its
// machine's node is unknown).
func (s *Store) RefOf(id string) string { return s.refOf(id) }
