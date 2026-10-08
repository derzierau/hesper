package sessions

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Replication: every Mac pulls every other Mac's index entries (each
// Mac is both a host and a controller in part R, so pulling both ways
// is enough). A pull asks for the entries changed after the last
// sequence number it got from that node (deltas only; a peer whose
// database is new — another epoch — starts over), in batches of at most
// 400 entries and ~600 KB of zstd-compressed JSON, one request at a
// time with a pause between batches, so an initial sync of thousands of
// entries never floods the link. A link opening and the host's change
// hints (a tiny event on the link) start a pull. Merging is the same
// last-writer-wins per field as everywhere, so pulling an entry back
// that came from here changes nothing; entries whose every stamp is the
// requester's are not sent back at all. The requests travel signed
// inside the end-to-end channel: the relay sees nothing.

// Peers is how the history reaches other Macs (internal/remote.Fleet).
type Peers interface {
	// Call sends a signed host method to a machine (by this Mac's name).
	Call(ctx context.Context, machine, method string, params json.RawMessage) (json.RawMessage, error)
	// Linked are the machines with a link now.
	Linked() []string
	// TransferKey is a machine's published transfer key ("" unknown).
	TransferKey(machine string) string
	// Fetch has machine pack an export (agents.export of id, incremental
	// from have) and downloads it into dir; progress (optional) gets the
	// bytes received of the total (first with 0 once it is packed).
	Fetch(ctx context.Context, machine, id string, have []string, dir string, progress func(got, total int64)) error
}

// SetPeers connects the other Macs.
func (s *Service) SetPeers(p Peers) {
	s.mu.Lock()
	s.peers = p
	s.mu.Unlock()
}

func (s *Service) peerSet() Peers {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.peers
}

// batch limits of a pull.
var (
	pullEntries   = 400
	pullBytes     = 600 << 10 // compressed
	pullPause     = 20 * time.Millisecond
	pullRate      = time.Duration(8 << 20) // bytes per second
	pullHintDelay = 250 * time.Millisecond
)

type pullParams struct {
	Since int64  `json:"since"`
	Epoch string `json:"epoch,omitempty"`
	From  string `json:"from,omitempty"` // the requester's node
	Limit int    `json:"limit,omitempty"`
}

type pullResult struct {
	Node  string `json:"node"`
	Short string `json:"short"`
	Epoch string `json:"epoch"`
	Next  int64  `json:"next"` // the sequence number to ask after
	More  bool   `json:"more"`
	Count int    `json:"count"`
	Data  []byte `json:"data"` // zstd(JSON [Record])
}

// Hint is the host's link event: its node and newest sequence number.
type Hint struct {
	Node string `json:"node"`
	Seq  int64  `json:"seq"`
}

// HostHint is this Mac's current hint (for its links).
func (s *Service) HostHint() json.RawMessage {
	data, _ := json.Marshal(Hint{Node: s.db.node, Seq: s.db.highSeq()})
	return data
}

var (
	encOnce sync.Once
	encoder *zstd.Encoder
	decoder *zstd.Decoder
)

func codecs() (*zstd.Encoder, *zstd.Decoder) {
	encOnce.Do(func() {
		encoder, _ = zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault), zstd.WithEncoderConcurrency(1))
		decoder, _ = zstd.NewReader(nil, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(64<<20))
	})
	return encoder, decoder
}

func echo(r *Record, node string) bool {
	if node == "" {
		return false
	}
	all := true
	r.stamps(func(st Stamp) {
		if st.N != "" && st.N != node {
			all = false
		}
	})
	return all
}

// pull answers sessions.pull.
func (s *Service) pull(p pullParams) (pullResult, error) {
	res := pullResult{Node: s.db.node, Short: s.machine(), Epoch: s.db.epoch}
	since := p.Since
	if p.Epoch != s.db.epoch {
		since = 0
	}
	limit := p.Limit
	if limit <= 0 || limit > pullEntries {
		limit = pullEntries
	}
	enc, _ := codecs()
	for {
		rows, err := s.db.since(since, limit)
		if err != nil {
			return res, err
		}
		var recs []*Record
		next := since
		for _, rw := range rows {
			next = rw.seq
			if echo(&rw.rec, p.From) {
				continue
			}
			recs = append(recs, &rw.rec)
		}
		data, _ := json.Marshal(recs)
		packed := enc.EncodeAll(data, nil)
		if len(packed) > pullBytes && limit > 1 {
			limit /= 2
			continue
		}
		res.Next, res.Count, res.Data = next, len(recs), packed
		res.More = len(rows) == limit && next < s.db.highSeq()
		return res, nil
	}
}

// syncer pulls from each linked machine, one pull at a time each.
type syncer struct {
	s       *Service
	mu      sync.Mutex
	running map[string]bool
	again   map[string]bool
	// Pulled counts entries received (tests, benchmarks).
	Pulled int64
}

func newSyncer(s *Service) *syncer {
	return &syncer{s: s, running: map[string]bool{}, again: map[string]bool{}}
}

// LinkUp: a link to machine opened (internal/remote): pull from it.
func (s *Service) LinkUp(machine string) { s.sync.kick(machine) }

// HintFrom: machine's hint arrived on its link: pull if it is ahead.
func (s *Service) HintFrom(machine string, raw json.RawMessage) {
	var h Hint
	if json.Unmarshal(raw, &h) != nil || h.Node == "" || h.Node == s.db.node {
		return
	}
	var seq int64
	s.db.r.QueryRow(`SELECT seq FROM peers WHERE node = ?`, h.Node).Scan(&seq)
	if h.Seq > seq {
		s.sync.kick(machine)
	}
}

func (y *syncer) kick(machine string) {
	y.mu.Lock()
	if y.running[machine] {
		y.again[machine] = true
		y.mu.Unlock()
		return
	}
	y.running[machine] = true
	y.mu.Unlock()
	y.s.wg.Add(1)
	go func() {
		defer y.s.wg.Done()
		for {
			select {
			case <-y.s.stop:
				return
			case <-time.After(pullHintDelay):
			}
			err := y.pullAll(machine)
			if err == nil {
				y.s.mirror.kick(machine)
			}
			y.mu.Lock()
			if !y.again[machine] {
				delete(y.running, machine)
				y.mu.Unlock()
				return
			}
			y.again[machine] = false
			y.mu.Unlock()
		}
	}()
}

// pullAll pulls from machine until it has nothing newer.
func (y *syncer) pullAll(machine string) error {
	s := y.s
	peers := s.peerSet()
	if peers == nil {
		return errClosed
	}
	var node, epoch string
	var seq int64
	s.db.r.QueryRow(`SELECT node, epoch, seq FROM peers WHERE short = ?`, machine).Scan(&node, &epoch, &seq)
	_, dec := codecs()
	for {
		select {
		case <-s.stop:
			return errClosed
		default:
		}
		params, _ := json.Marshal(pullParams{Since: seq, Epoch: epoch, From: s.db.node})
		ctx, cancel := context.WithTimeout(s.ctx, 60*time.Second)
		raw, err := peers.Call(ctx, machine, "sessions.pull", params)
		cancel()
		if err != nil {
			return err
		}
		var res pullResult
		if err := json.Unmarshal(raw, &res); err != nil || res.Node == "" || res.Node == s.db.node {
			return wire.Errorf(wire.CodeRemote, "bad sessions.pull answer from %s", machine)
		}
		if res.Epoch != epoch || res.Node != node {
			seq = 0 // a new database there: its sequence numbers start over
		}
		var recs []*Record
		if len(res.Data) > 0 {
			data, err := dec.DecodeAll(res.Data, nil)
			if err != nil {
				return err
			}
			if err := json.Unmarshal(data, &recs); err != nil {
				return err
			}
		}
		valid := recs[:0]
		for _, r := range recs {
			if r != nil && r.valid() {
				valid = append(valid, r)
			}
		}
		if _, err := s.apply(valid, false); err != nil {
			return err
		}
		y.mu.Lock()
		y.Pulled += int64(len(valid))
		y.mu.Unlock()
		node, epoch, seq = res.Node, res.Epoch, res.Next
		s.mu.Lock()
		s.names[node] = machine
		s.mu.Unlock()
		s.db.write(func(tx sqlTx) error {
			_, err := tx.Exec(`INSERT INTO peers (node, epoch, seq, short) VALUES (?, ?, ?, ?)
 ON CONFLICT(node) DO UPDATE SET epoch = excluded.epoch, seq = excluded.seq, short = excluded.short`, node, epoch, seq, machine)
			if err == nil {
				// One name, one node: a machine reinstalled has a new node.
				_, err = tx.Exec(`UPDATE peers SET short = '' WHERE short = ? AND node != ?`, machine, node)
			}
			return err
		})
		if !res.More {
			return nil
		}
		// Rate limit: at most ~8 MB/s of compressed entries, so a first
		// sync never crowds out the agents' traffic to that Mac.
		pause := pullPause + time.Duration(len(res.Data))*time.Second/pullRate
		select {
		case <-s.stop:
			return errClosed
		case <-time.After(pause):
		}
	}
}
