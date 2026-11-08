package sessions

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// agents.transcriptStream: an agent's transcript followed as it grows.
// The follower shares the transcript's index with agents.transcript and
// polls it: each poll indexes what was appended (complete lines only; a
// partial last line waits for its newline) and sends the items of the
// lines past the last one sent.

// FollowPoll is how often a followed transcript is looked at.
var FollowPoll = 250 * time.Millisecond

// FollowTranscript sends the agent's transcript items to emit, then
// every new complete line's items as the file grows, until ctx ends
// (ctx.Err()) or emit returns an error. First: after <= 0, the newest
// limit items (0: wire.TranscriptDefaultLimit, at most
// wire.TranscriptMaxLimit) as whole lines, oldest first, like a page;
// after > 0, every item whose Seq is greater than after, not capped by
// limit (a phone catching up after a reconnect). A file that shrank was
// rewritten: it sends {Seq: -1, Kind: system, Text: "Transcript
// restarted"} (a negative Seq is a marker, never a line) and continues
// from the new end.
func (s *Service) FollowTranscript(ctx context.Context, a wire.Agent, after int64, limit int, emit func(wire.TranscriptItem) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	switch {
	case limit <= 0:
		limit = wire.TranscriptDefaultLimit
	case limit > wire.TranscriptMaxLimit:
		limit = wire.TranscriptMaxLimit
	}
	path, err := s.TranscriptPath(a)
	if err != nil {
		return err
	}
	key := a.Kind + ":" + a.SessionID
	f, err := s.transcriptIndex(key, a.Kind, path)
	if err != nil {
		return err
	}
	s.mu.Lock()
	f.refs++
	s.mu.Unlock()
	defer s.unfollow(key, f)

	poll := FollowPoll
	items, next, gen, err := f.backfill(after, limit)
	for {
		if err != nil {
			return fmt.Errorf("follow transcript of %s: %w", a.ID, err)
		}
		for _, it := range items {
			if err := emit(it); err != nil {
				return fmt.Errorf("follow transcript of %s: %w", a.ID, err)
			}
		}
		t := time.NewTimer(poll)
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-t.C:
		}
		items, next, gen, err = f.tail(next, gen)
	}
}

// unfollow drops a follower of key's transcript f; the last one drops
// the index unless it was replaced meanwhile.
func (s *Service) unfollow(key string, f *transcriptFile) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if f.refs--; f.refs == 0 && s.transcripts[key] == f {
		delete(s.transcripts, key)
	}
}

// backfill is FollowTranscript's first send (see there), the offset to
// tail from and the index's generation.
func (f *transcriptFile) backfill(after int64, limit int) ([]wire.TranscriptItem, int64, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.update(); err != nil {
		return nil, 0, 0, err
	}
	if after <= 0 {
		p, err := f.pageIndexed(0, limit)
		return p.Items, f.size, f.gen, err
	}
	// From the line that holds after: its later items count too.
	i := max(sort.Search(len(f.offsets), func(i int) bool { return f.offsets[i] > after })-1, 0)
	items, err := f.linesFrom(i)
	var out []wire.TranscriptItem
	for _, it := range items {
		if it.Seq > after {
			out = append(out, it)
		}
	}
	return out, f.size, f.gen, err
}

// tail is the items of the lines indexed from byte offset from on, the
// offset to continue from and the index's generation. An index of
// another generation than gen (the file shrank) sends only the restart
// notice and continues from the new end.
func (f *transcriptFile) tail(from int64, gen int) ([]wire.TranscriptItem, int64, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.update(); err != nil {
		return nil, from, gen, err
	}
	if f.gen != gen {
		return []wire.TranscriptItem{{Seq: -1, Kind: wire.ItemSystem, Text: "Transcript restarted"}}, f.size, f.gen, nil
	}
	items, err := f.linesFrom(sort.Search(len(f.offsets), func(i int) bool { return f.offsets[i] >= from }))
	return items, f.size, f.gen, err
}

// linesFrom is the items of the i-th indexed line on. f.mu is held.
func (f *transcriptFile) linesFrom(i int) ([]wire.TranscriptItem, error) {
	if i >= len(f.offsets) {
		return nil, nil
	}
	fh, err := os.Open(f.path)
	if err != nil {
		return nil, fmt.Errorf("open transcript: %w", err)
	}
	defer fh.Close()
	var items []wire.TranscriptItem
	for _, off := range f.offsets[i:] {
		raw, err := lineAt(fh, off, f.size)
		if err != nil {
			return nil, err
		}
		items = append(items, itemsFrom(f.kind, bytes.TrimRight(raw, "\r\n"), off)...)
	}
	return items, nil
}
