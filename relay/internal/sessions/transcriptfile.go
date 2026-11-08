package sessions

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// agents.transcript: an agent's transcript in pages of items. The first
// page reads the file once and keeps the byte offset of every line that
// has items; a page re-reads and parses only the lines it returns, and a
// later page indexes only what was appended since. A page is made of
// whole lines, so Before (an item's Seq, which lies inside its line)
// names a line.

// transcriptFile indexes one transcript: the offset of every line that
// produced items.
type transcriptFile struct {
	path, kind string

	mu      sync.Mutex
	offsets []int64 // ascending
	size    int64   // bytes indexed: to the end of the last complete line
	gen     int     // times the file shrank and was indexed again

	// ready closes when the first pass (indexTranscript) is done; err is
	// its error, read after ready.
	ready chan struct{}
	err   error
	// refs counts the pages and streams using it; idle drops it
	// IndexKeep after the last. s.mu guards both.
	refs int
	idle *time.Timer
}

// openTranscript indexes the transcript at path in one pass (tests).
func openTranscript(path, kind string) (*transcriptFile, error) {
	f := &transcriptFile{path: path, kind: kind}
	if err := f.update(); err != nil {
		return nil, err
	}
	return f, nil
}

// update indexes the lines appended since the last pass; a file that
// shrank was rewritten and is indexed again from the start. f.mu is held
// (or f is not shared yet).
func (f *transcriptFile) update() error {
	fh, err := os.Open(f.path)
	if err != nil {
		return fmt.Errorf("open transcript: %w", err)
	}
	defer fh.Close()
	st, err := fh.Stat()
	if err != nil {
		return fmt.Errorf("stat transcript: %w", err)
	}
	if st.Size() < f.size {
		f.offsets, f.size = nil, 0
		f.gen++
	}
	if st.Size() == f.size {
		return nil
	}
	if _, err := fh.Seek(f.size, io.SeekStart); err != nil {
		return fmt.Errorf("seek transcript: %w", err)
	}
	base := f.size
	n, err := readLinesAt(fh, func(line []byte, off int64) {
		if len(itemsFrom(f.kind, line, base+off)) > 0 {
			f.offsets = append(f.offsets, base+off)
		}
	}, nil)
	f.size = base + n
	if err != nil {
		return fmt.Errorf("read transcript: %w", err)
	}
	return nil
}

// page returns items before seq before (before <= 0: the end), oldest
// first. An item inside a multi-item line can be a page boundary.
func (f *transcriptFile) page(before int64, limit int) (wire.TranscriptPage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.update(); err != nil {
		return wire.TranscriptPage{}, err
	}
	return f.pageIndexed(before, limit)
}

// pageIndexed is page over what is indexed now. f.mu is held.
func (f *transcriptFile) pageIndexed(before int64, limit int) (wire.TranscriptPage, error) {
	fh, err := os.Open(f.path)
	if err != nil {
		return wire.TranscriptPage{}, fmt.Errorf("open transcript: %w", err)
	}
	defer fh.Close()
	end := len(f.offsets)
	if before > 0 {
		end = sort.Search(len(f.offsets), func(i int) bool { return f.offsets[i] >= before })
	}
	var newest []wire.TranscriptItem
	size := 0
	for i := end - 1; i >= 0; i-- {
		off := f.offsets[i]
		raw, err := lineAt(fh, off, f.size)
		if err != nil {
			return wire.TranscriptPage{}, err
		}
		items := itemsFrom(f.kind, bytes.TrimRight(raw, "\r\n"), off)
		for j := len(items) - 1; j >= 0; j-- {
			it := items[j]
			if before > 0 && it.Seq >= before {
				continue
			}
			m := itemsSize([]wire.TranscriptItem{it})
			if len(newest) >= limit || size+m > TranscriptPageBytes-128 {
				p := wire.TranscriptPage{Items: make([]wire.TranscriptItem, len(newest)), More: true}
				for k, item := range newest {
					p.Items[len(newest)-1-k] = item
				}
				return p, nil
			}
			newest = append(newest, it)
			size += m
		}
	}
	p := wire.TranscriptPage{Items: make([]wire.TranscriptItem, len(newest))}
	for k, item := range newest {
		p.Items[len(newest)-1-k] = item
	}
	return p, nil
}

// TranscriptPageBytes caps a page's JSON, like a review part
// (review.PartSize). Leave 128 bytes for the items/more envelope and a
// possible restart marker in a stream. A page stops before the item
// that would pass it (More is then true).
const TranscriptPageBytes = 384 << 10

// itemsSize is the items' size as JSON in a page, a comma each.
func itemsSize(items []wire.TranscriptItem) int {
	n := 0
	for _, it := range items {
		b, _ := json.Marshal(it)
		n += len(b) + 1
	}
	return n
}

// text returns one chunk of a message, selecting the exact item within
// its source line. A result or an unknown seq has no full-text endpoint.
func (f *transcriptFile) text(seq int64, offset, limit int) (wire.TranscriptTextChunk, error) {
	if seq < 0 || offset < 0 {
		return wire.TranscriptTextChunk{}, wire.Errorf(wire.CodeInvalid, "invalid transcript text offset")
	}
	if limit <= 0 || limit > transcriptTextBytes {
		limit = transcriptTextBytes
	}
	if limit < 4 {
		return wire.TranscriptTextChunk{}, wire.Errorf(wire.CodeInvalid, "transcript text limit must be at least 4 bytes")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.update(); err != nil {
		return wire.TranscriptTextChunk{}, err
	}
	i := sort.Search(len(f.offsets), func(i int) bool { return f.offsets[i] > seq }) - 1
	if i < 0 {
		return wire.TranscriptTextChunk{}, wire.Errorf(wire.CodeNotFound, "no transcript message at seq %d", seq)
	}
	fh, err := os.Open(f.path)
	if err != nil {
		return wire.TranscriptTextChunk{}, fmt.Errorf("open transcript: %w", err)
	}
	defer fh.Close()
	off := f.offsets[i]
	raw, err := lineAt(fh, off, f.size)
	if err != nil {
		return wire.TranscriptTextChunk{}, err
	}
	for _, it := range rawItemsFrom(f.kind, bytes.TrimRight(raw, "\r\n"), off) {
		if it.Seq != seq || it.Kind != wire.ItemUser && it.Kind != wire.ItemAssistant {
			continue
		}
		if offset > len(it.Text) || offset < len(it.Text) && !utf8.RuneStart(it.Text[offset]) {
			return wire.TranscriptTextChunk{}, wire.Errorf(wire.CodeInvalid, "offset is not a text boundary")
		}
		end := min(offset+limit, len(it.Text))
		for end < len(it.Text) && !utf8.RuneStart(it.Text[end]) {
			end--
		}
		return wire.TranscriptTextChunk{Text: it.Text[offset:end], Next: end, More: end < len(it.Text)}, nil
	}
	return wire.TranscriptTextChunk{}, wire.Errorf(wire.CodeNotFound, "no transcript message at seq %d", seq)
}

// A control byte can take six JSON bytes, so 64 KiB of text stays below
// the relay's 1 MiB response cap even with worst-case escaping.
const transcriptTextBytes = 64 << 10

// lineAt reads the line at off of a file indexed to size, newline
// included.
func lineAt(r io.ReaderAt, off, size int64) ([]byte, error) {
	raw, err := bufio.NewReader(io.NewSectionReader(r, off, size-off)).ReadBytes('\n')
	if err != nil {
		return nil, fmt.Errorf("read transcript line at %d: %w", off, err)
	}
	return raw, nil
}

// TranscriptPath is the agent's transcript on this Mac: the index's
// Meta.Path for "kind:sid", else the file a resume looks for (Claude:
// <ClaudeHome>/projects/*/<sid>.jsonl; Codex:
// <CodexHome>/sessions/*/*/*/rollout-*-<sid>.jsonl). "" and
// wire.CodeUnavailable when there is none.
func (s *Service) TranscriptPath(a wire.Agent) (string, error) {
	none := wire.Errorf(wire.CodeUnavailable, "no transcript for %s", a.ID)
	// A session id with glob or path characters names no file of ours.
	if (a.Kind != wire.KindClaude && a.Kind != wire.KindCodex) || a.SessionID == "" || strings.ContainsAny(a.SessionID, `/\*?[`) {
		return "", none
	}
	rw, err := s.db.get(s.db.node + ":" + a.Kind + ":" + a.SessionID)
	if err != nil {
		return "", fmt.Errorf("transcript of %s: %w", a.ID, err)
	}
	// A compressed (archived) rollout cannot be paged by offset.
	if rw != nil && rw.rec.Node == s.db.node && !rw.rec.Deleted && rw.rec.Meta.Path != "" && !strings.HasSuffix(rw.rec.Meta.Path, ".zst") {
		if _, err := os.Stat(rw.rec.Meta.Path); err == nil {
			return rw.rec.Meta.Path, nil
		}
	}
	var pattern string
	if a.Kind == wire.KindClaude {
		pattern = filepath.Join(s.opt.ClaudeHome, "projects", "*", a.SessionID+".jsonl")
	} else {
		pattern = filepath.Join(s.opt.CodexHome, "sessions", "*", "*", "*", "rollout-*-"+a.SessionID+".jsonl")
	}
	matches, _ := filepath.Glob(pattern)
	for _, m := range matches {
		if st, err := os.Stat(m); err == nil && st.Mode().IsRegular() {
			return m, nil
		}
	}
	return "", none
}

// Transcript answers agents.transcript: a page of the agent's transcript
// (limit 0: wire.TranscriptDefaultLimit, at most wire.TranscriptMaxLimit).
func (s *Service) Transcript(ctx context.Context, a wire.Agent, before int64, limit int) (wire.TranscriptPage, error) {
	if err := ctx.Err(); err != nil {
		return wire.TranscriptPage{}, err
	}
	switch {
	case limit <= 0:
		limit = wire.TranscriptDefaultLimit
	case limit > wire.TranscriptMaxLimit:
		limit = wire.TranscriptMaxLimit
	}
	path, err := s.TranscriptPath(a)
	if err != nil {
		return wire.TranscriptPage{}, err
	}
	key := a.Kind + ":" + a.SessionID
	f := s.transcriptIndex(key, a.Kind, path)
	defer s.releaseTranscript(key, f)
	// The host serves requests one at a time: a long first pass must not
	// hold the rest up, so the page asks again.
	t := time.NewTimer(transcriptWait)
	defer t.Stop()
	select {
	case <-f.ready:
	case <-t.C:
		return wire.TranscriptPage{}, wire.Errorf(wire.CodeBusy, "the transcript of %s is being read; try again", a.ID)
	case <-ctx.Done():
		return wire.TranscriptPage{}, ctx.Err()
	}
	if f.err != nil {
		return wire.TranscriptPage{}, f.err
	}
	return f.page(before, limit)
}

// TranscriptText retrieves a user or assistant item's full text in
// bounded, UTF-8-aligned chunks.
func (s *Service) TranscriptText(ctx context.Context, a wire.Agent, seq int64, offset, limit int) (wire.TranscriptTextChunk, error) {
	if err := ctx.Err(); err != nil {
		return wire.TranscriptTextChunk{}, err
	}
	path, err := s.TranscriptPath(a)
	if err != nil {
		return wire.TranscriptTextChunk{}, err
	}
	key := a.Kind + ":" + a.SessionID
	f := s.transcriptIndex(key, a.Kind, path)
	defer s.releaseTranscript(key, f)
	t := time.NewTimer(transcriptWait)
	defer t.Stop()
	select {
	case <-f.ready:
	case <-t.C:
		return wire.TranscriptTextChunk{}, wire.Errorf(wire.CodeBusy, "the transcript of %s is being read; try again", a.ID)
	case <-ctx.Done():
		return wire.TranscriptTextChunk{}, ctx.Err()
	}
	if f.err != nil {
		return wire.TranscriptTextChunk{}, f.err
	}
	return f.text(seq, offset, limit)
}

// IndexKeep is how long a transcript's index is kept after its last
// user (page or stream) is done, so the next page does not read the
// whole file again.
var IndexKeep = time.Minute

// transcriptWait is how long a page waits for a transcript's first
// pass before it answers busy.
var transcriptWait = 2 * time.Second

// transcriptIndexHook (tests) runs before a first pass.
var transcriptIndexHook func()

// transcriptIndex is the index of key's transcript at path, with one
// more user (releaseTranscript drops it). When there is none yet or the
// transcript moved, a new one is stored and its first pass runs in a
// goroutine; f.ready closes when it is done (f.err its error). Every
// user of the key shares that pass.
func (s *Service) transcriptIndex(key, kind, path string) *transcriptFile {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.transcripts[key]
	if f == nil || f.path != path {
		f = &transcriptFile{path: path, kind: kind, ready: make(chan struct{})}
		if s.transcripts == nil {
			s.transcripts = map[string]*transcriptFile{}
		}
		s.transcripts[key] = f
		go s.indexTranscript(key, f)
	}
	f.refs++
	if f.idle != nil {
		f.idle.Stop()
		f.idle = nil
	}
	return f
}

// indexTranscript is f's first pass. A pass that failed is dropped, so
// the next user tries again.
func (s *Service) indexTranscript(key string, f *transcriptFile) {
	if transcriptIndexHook != nil {
		transcriptIndexHook()
	}
	f.mu.Lock()
	err := f.update()
	f.mu.Unlock()
	if err != nil {
		s.mu.Lock()
		if s.transcripts[key] == f {
			delete(s.transcripts, key)
		}
		s.mu.Unlock()
	}
	f.err = err
	close(f.ready)
}

// releaseTranscript drops a user of key's index f; IndexKeep after the
// last one, f is dropped unless it was used or replaced meanwhile.
func (s *Service) releaseTranscript(key string, f *transcriptFile) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if f.refs--; f.refs > 0 || s.transcripts[key] != f {
		return
	}
	var t *time.Timer
	t = time.AfterFunc(IndexKeep, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if f.idle == t && f.refs == 0 && s.transcripts[key] == f {
			delete(s.transcripts, key)
		}
	})
	f.idle = t
}
