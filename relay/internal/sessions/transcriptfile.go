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
	refs    int     // streams following it (FollowTranscript); s.mu guards it
}

// openTranscript indexes the transcript at path in one pass.
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

// page is the items of the last indexed lines before byte offset before
// (before <= 0: the end), oldest first: whole lines, newest back, until
// they hold at least limit items or TranscriptPageBytes. A before inside
// an indexed line leaves that line out too. More: an indexed line exists
// before the first one returned.
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
	var lines [][]wire.TranscriptItem // newest first
	n, size, i := 0, 0, end-1
	for ; i >= 0 && n < limit; i-- {
		off := f.offsets[i]
		raw, err := lineAt(fh, off, f.size)
		if err != nil {
			return wire.TranscriptPage{}, err
		}
		if i == end-1 && before > 0 && before < off+int64(len(raw)) {
			continue
		}
		items := itemsFrom(f.kind, bytes.TrimRight(raw, "\r\n"), off)
		// The first line goes in whatever its size, so paging back
		// always moves.
		m := itemsSize(items)
		if len(lines) > 0 && size+m > TranscriptPageBytes {
			break
		}
		lines = append(lines, items)
		n += len(items)
		size += m
	}
	p := wire.TranscriptPage{Items: make([]wire.TranscriptItem, 0, n), More: i >= 0}
	for j := len(lines) - 1; j >= 0; j-- {
		p.Items = append(p.Items, lines[j]...)
	}
	return p, nil
}

// TranscriptPageBytes caps a page's items, marshalled, like a review
// part (review.PartSize): one reply stays well inside a relay frame. A
// page stops before the line that would pass it (More is then true);
// its first line goes in whatever its size.
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
	f, err := s.transcriptIndex(a.Kind+":"+a.SessionID, a.Kind, path)
	if err != nil {
		return wire.TranscriptPage{}, err
	}
	return f.page(before, limit)
}

// transcriptIndex is the index of key's transcript at path. It is built
// without s.mu (that reads the whole file) when there is none yet or the
// transcript moved; of two built at once, the first stored is kept.
func (s *Service) transcriptIndex(key, kind, path string) (*transcriptFile, error) {
	s.mu.Lock()
	f := s.transcripts[key]
	s.mu.Unlock()
	if f != nil && f.path == path {
		return f, nil
	}
	nf, err := openTranscript(path, kind)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if f := s.transcripts[key]; f != nil && f.path == path {
		return f, nil
	}
	if s.transcripts == nil {
		s.transcripts = map[string]*transcriptFile{}
	}
	s.transcripts[key] = nf
	return nf, nil
}
