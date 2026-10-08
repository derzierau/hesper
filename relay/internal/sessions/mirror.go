package sessions

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/derzierau/hesper/relay/pkg/transfer"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

// The transcript mirror: the transcripts of other Macs' sessions active
// within MirrorDays are copied here, so "resume here" works while their
// Mac sleeps. A copy is $STATE/history/mirror/<machine>/<kind>/<id>.jsonl.zst,
// a sequence of zstd frames (one per chunk, so it stays a valid stream
// while it grows): transcripts only grow, so a copy is brought up to date
// from the size it has (resumable; nothing is fetched twice), a
// transcript whose version did not change is not asked for at all
// (dedupe by size and mtime), and one that shrank starts over. Each chunk
// (≤ 2 MiB of transcript, compressed) is sealed by the home with
// pkg/transfer for this request's ephemeral key and opened here with the
// home's pinned transfer key: only this Mac can read it, inside the
// end-to-end channel. The mirror runs at background priority, one
// chunk at a time, and waits while agents here are working. Copies whose
// session went quiet more than two days past the window are dropped.

var (
	mirrorChunk = 2 << 20
	mirrorPause = 30 * time.Millisecond
)

var safeName = regexp.MustCompile(`[^A-Za-z0-9._-]`)

type mirrorer struct {
	s       *Service
	mu      sync.Mutex
	running map[string]bool
	again   map[string]bool
}

func newMirrorer(s *Service) *mirrorer {
	return &mirrorer{s: s, running: map[string]bool{}, again: map[string]bool{}}
}

func (mr *mirrorer) dir() string { return filepath.Join(mr.s.opt.StateDir, "history", "mirror") }

func (mr *mirrorer) kick(machine string) {
	mr.mu.Lock()
	if mr.running[machine] {
		mr.again[machine] = true
		mr.mu.Unlock()
		return
	}
	mr.running[machine] = true
	mr.mu.Unlock()
	mr.s.wg.Add(1)
	go func() {
		defer mr.s.wg.Done()
		if !mr.s.opt.Foreground {
			defer background()()
		}
		for {
			if err := mr.pass(machine); err != nil && !errors.Is(err, errClosed) {
				mr.s.opt.Logf("history: mirror from %s: %v", machine, err)
			}
			mr.mu.Lock()
			if !mr.again[machine] {
				delete(mr.running, machine)
				mr.mu.Unlock()
				return
			}
			mr.again[machine] = false
			mr.mu.Unlock()
		}
	}()
}

type mirrorRow struct {
	version  string
	raw      int64
	bytes    int64
	path     string
	complete bool
}

func (mr *mirrorer) state(key string) *mirrorRow {
	var m mirrorRow
	var complete int
	err := mr.s.db.r.QueryRow(`SELECT version, raw, bytes, path, complete FROM mirror WHERE key = ?`, key).Scan(&m.version, &m.raw, &m.bytes, &m.path, &complete)
	if err != nil {
		return nil
	}
	m.complete = complete != 0
	return &m
}

// pass brings every recent session of machine's up to date here, newest
// first, and drops copies that left the window.
func (mr *mirrorer) pass(machine string) error {
	s := mr.s
	node := s.nodeOf(machine)
	if node == "" || node == s.db.node {
		return nil
	}
	cutoff := s.opt.Now().Add(-time.Duration(s.opt.MirrorDays) * 24 * time.Hour).UnixMilli()
	rows, err := s.db.r.Query(`SELECT key, version, size FROM sessions WHERE node = ? AND deleted = 0 AND last >= ? AND size > 0 ORDER BY last DESC`, node, cutoff)
	if err != nil {
		return err
	}
	type want struct {
		key, version string
		size         int64
	}
	var wants []want
	for rows.Next() {
		var w want
		if rows.Scan(&w.key, &w.version, &w.size) == nil {
			wants = append(wants, w)
		}
	}
	rows.Close()
	for _, w := range wants {
		if m := mr.state(w.key); m != nil && m.complete && m.version == w.version {
			continue
		}
		if err := mr.fetch(machine, w.key); err != nil {
			return err
		}
	}
	mr.expire(node)
	return nil
}

// expire drops copies of sessions quiet for longer than the window (and
// two days), deleted ones and ones that moved.
func (mr *mirrorer) expire(node string) {
	s := mr.s
	cutoff := s.opt.Now().Add(-time.Duration(s.opt.MirrorDays+2) * 24 * time.Hour).UnixMilli()
	rows, err := s.db.r.Query(`SELECT m.key, m.path FROM mirror m JOIN sessions s ON s.key = m.key WHERE s.node = ? AND (s.last < ? OR s.deleted = 1)`, node, cutoff)
	if err != nil {
		return
	}
	type gone struct{ key, path string }
	var list []gone
	for rows.Next() {
		var g gone
		if rows.Scan(&g.key, &g.path) == nil {
			list = append(list, g)
		}
	}
	rows.Close()
	for _, g := range list {
		mr.drop(g.key, g.path)
	}
}

func (mr *mirrorer) drop(key, path string) {
	os.Remove(path)
	mr.s.db.write(func(tx sqlTx) error {
		_, err := tx.Exec(`DELETE FROM mirror WHERE key = ?`, key)
		return err
	})
	mr.s.edit(key, func(r *Record, st Stamp) bool {
		if r.Mirrors[mr.s.db.node].At == 0 {
			return false
		}
		r.Mirrors[mr.s.db.node] = MirrorMark{At: 0, S: st}
		return true
	})
}

type transcriptParams struct {
	Key      string `json:"key"`
	Offset   int64  `json:"offset"`
	EPK      string `json:"epk"`
	Download string `json:"download"`
}

type transcriptResult struct {
	Data    []byte `json:"data"` // sealed zstd frame
	N       int64  `json:"n"`    // transcript bytes in it
	Size    int64  `json:"size"` // the transcript's size now
	Version string `json:"version"`
}

// fetch brings one copy up to date.
func (mr *mirrorer) fetch(machine, key string) error {
	s := mr.s
	peers := s.peerSet()
	if peers == nil {
		return errClosed
	}
	hostKey, err := transfer.ParsePublicKey(peers.TransferKey(machine))
	if err != nil {
		return wire.Errorf(wire.CodeUnavailable, "%s publishes no transfer key", machine)
	}
	_, kind, sid, ok := splitKey(key)
	if !ok {
		return nil
	}
	path := filepath.Join(mr.dir(), safeName.ReplaceAllString(machine, "_"), kind, sid+".jsonl.zst")
	m := mr.state(key)
	offset, written := int64(0), int64(0)
	if m != nil && m.path == path {
		if st, err := os.Stat(path); err == nil && st.Size() == m.bytes {
			offset, written = m.raw, m.bytes
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	eph, err := transfer.GenerateKey()
	if err != nil {
		return err
	}
	for {
		for !s.opt.Foreground && s.busy() {
			select {
			case <-s.stop:
				return errClosed
			case <-time.After(time.Second):
			}
		}
		var raw [6]byte
		rand.Read(raw[:])
		dl := "mr-" + hex.EncodeToString(raw[:])
		params, _ := json.Marshal(transcriptParams{Key: key, Offset: offset, EPK: transfer.EncodePublicKey(eph.PublicKey()), Download: dl})
		ctx, cancel := context.WithTimeout(s.ctx, 60*time.Second)
		res, err := peers.Call(ctx, machine, "sessions.transcript", params)
		cancel()
		if err != nil {
			return err
		}
		var tr transcriptResult
		if err := json.Unmarshal(res, &tr); err != nil {
			return err
		}
		if tr.Size < offset {
			// It shrank (rewritten): start over.
			offset, written = 0, 0
			os.Remove(path)
			continue
		}
		var frame []byte
		if tr.N > 0 {
			frame, err = transfer.NewRequesterDownload(eph, hostKey, dl).Open("transcript", 0, true, tr.Data)
			if err != nil {
				return wire.Errorf(wire.CodeForbidden, "a mirror chunk failed authentication")
			}
			flags := os.O_CREATE | os.O_WRONLY | os.O_APPEND
			if offset == 0 {
				flags |= os.O_TRUNC
			}
			f, err := os.OpenFile(path, flags, 0o600)
			if err != nil {
				return err
			}
			_, werr := f.Write(frame)
			cerr := f.Close()
			if werr != nil || cerr != nil {
				return errors.Join(werr, cerr)
			}
			offset += tr.N
			written += int64(len(frame))
		}
		complete := offset >= tr.Size
		s.db.write(func(tx sqlTx) error {
			_, err := tx.Exec(`INSERT INTO mirror (key, version, raw, bytes, path, complete) VALUES (?, ?, ?, ?, ?, ?)
 ON CONFLICT(key) DO UPDATE SET version = excluded.version, raw = excluded.raw, bytes = excluded.bytes, path = excluded.path, complete = excluded.complete`,
				key, tr.Version, offset, written, path, b2i(complete))
			return err
		})
		if complete {
			now := s.opt.Now().UnixMilli()
			s.edit(key, func(r *Record, st Stamp) bool {
				if r.Mirrors == nil {
					r.Mirrors = map[string]MirrorMark{}
				}
				if r.Mirrors[s.db.node].At > 0 {
					return false
				}
				r.Mirrors[s.db.node] = MirrorMark{At: now, S: st}
				return true
			})
			return nil
		}
		select {
		case <-s.stop:
			return errClosed
		case <-time.After(mirrorPause):
		}
	}
}

// transcriptChunk answers sessions.transcript: a sealed, compressed piece
// of one of this Mac's transcripts.
func (s *Service) transcriptChunk(p transcriptParams) (transcriptResult, error) {
	s.mu.Lock()
	key := s.hostKey
	s.mu.Unlock()
	if key == nil {
		return transcriptResult{}, wire.Errorf(wire.CodeUnavailable, "no transfer key")
	}
	rw, err := s.db.get(p.Key)
	if err != nil {
		return transcriptResult{}, err
	}
	if rw == nil || rw.rec.Node != s.db.node || rw.rec.Deleted || rw.rec.Meta.Path == "" {
		return transcriptResult{}, wire.Errorf(wire.CodeNotFound, "no transcript of %s here", p.Key)
	}
	f, err := os.Open(rw.rec.Meta.Path)
	if err != nil {
		return transcriptResult{}, wire.Errorf(wire.CodeNotFound, "the transcript is gone")
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return transcriptResult{}, err
	}
	res := transcriptResult{Size: st.Size(), Version: rw.rec.Meta.Version}
	if p.Offset >= st.Size() {
		return res, nil
	}
	sealer, err := transfer.NewHostDownload(key, p.EPK, p.Download)
	if err != nil {
		return transcriptResult{}, wire.Errorf(wire.CodeInvalid, "epk must be an X25519 public key")
	}
	enc, _ := codecs()
	n := int64(mirrorChunk)
	for {
		if rest := st.Size() - p.Offset; n > rest {
			n = rest
		}
		buf := make([]byte, n)
		got, err := f.ReadAt(buf, p.Offset)
		if err != nil && !errors.Is(err, io.EOF) {
			return transcriptResult{}, err
		}
		frame := enc.EncodeAll(buf[:got], nil)
		if len(frame) > transfer.ChunkSize && n > 4096 {
			n /= 2
			continue
		}
		sealed, err := sealer.Seal("transcript", 0, true, frame)
		if err != nil {
			return transcriptResult{}, err
		}
		res.Data, res.N = sealed, int64(got)
		return res, nil
	}
}

// mirrored is this Mac's complete copy of a session's transcript
// (decompressed into dst), false when there is none.
func (s *Service) mirrored(key, dst string) (bool, error) {
	m := s.mirror.state(key)
	if m == nil || m.raw == 0 {
		return false, nil
	}
	in, err := os.Open(m.path)
	if err != nil {
		return false, nil
	}
	defer in.Close()
	_, dec := codecs()
	_ = dec
	r, err := newZstdReader(in)
	if err != nil {
		return false, err
	}
	defer r.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return false, err
	}
	if _, err := io.Copy(out, r); err != nil {
		out.Close()
		return false, err
	}
	return true, out.Close()
}
