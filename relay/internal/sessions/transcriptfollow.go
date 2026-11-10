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
// wire.TranscriptMaxLimit), oldest first, like a page;
// after > 0, every item whose Seq is greater than after, sent in bounded
// batches (a phone catching up after a reconnect); an after at or past
// the end, or more than wire.TranscriptMaxLimit lines behind, sends the
// restart marker and the newest limit items instead. A file that shrank was
// rewritten: it sends {Seq: -1, Kind: system, Text: "Transcript
// restarted"} (a negative Seq is a marker, never a line), then the
// newest limit items of the rewritten file as after <= 0 does, and
// continues from its end.
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
	f := s.transcriptIndex(key, a.Kind, path)
	defer s.releaseTranscript(key, f)
	// The caller is a goroutine of its own: wait for the first pass.
	select {
	case <-f.ready:
	case <-ctx.Done():
		return ctx.Err()
	}
	if f.err != nil {
		return fmt.Errorf("follow transcript of %s: %w", a.ID, f.err)
	}

	poll := FollowPoll
	items, next, gen, err := f.backfill(after, limit)
	for {
		if err != nil {
			return fmt.Errorf("follow transcript of %s: %w", a.ID, err)
		}
		for _, it := range items {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := emit(it); err != nil {
				return fmt.Errorf("follow transcript of %s: %w", a.ID, err)
			}
		}
		// Drain a reconnect backlog in bounded batches before polling for
		// new lines. emit runs between batches, without the index lock.
		items, next, gen, err = f.tail(next, gen, limit)
		if err != nil || len(items) > 0 {
			continue
		}
		t := time.NewTimer(poll)
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-t.C:
		}
		items, next, gen, err = f.tail(next, gen, limit)
	}
}

// transcriptCursor resumes at an item within a source line, or at the
// next line's offset when item is zero.
type transcriptCursor struct {
	line int64
	item int
}

// backfill is FollowTranscript's first send (see there), the cursor to
// tail from and the index's generation.
func (f *transcriptFile) backfill(after int64, limit int) ([]wire.TranscriptItem, transcriptCursor, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.update(); err != nil {
		return nil, transcriptCursor{}, 0, err
	}
	if after <= 0 {
		p, err := f.pageIndexed(0, limit)
		return p.Items, transcriptCursor{line: f.size}, f.gen, err
	}
	// From the line that holds after: its later items count too.
	i := max(sort.Search(len(f.offsets), func(i int) bool { return f.offsets[i] > after })-1, 0)
	// An item's Seq lies inside its line, so a cursor at or past the end
	// is from a file that was since rewritten shorter. More than
	// TranscriptMaxLimit lines behind (each has at least one item) is
	// too much to send at once. Either way the phone starts over.
	if after >= f.size || len(f.offsets)-i > wire.TranscriptMaxLimit {
		items, err := f.restart(limit)
		return items, transcriptCursor{line: f.size}, f.gen, err
	}
	items, next, err := f.linesFrom(transcriptCursor{line: f.offsets[i]}, after)
	return items, next, f.gen, err
}

// restart is the restart marker and the newest limit items. f.mu is
// held.
func (f *transcriptFile) restart(limit int) ([]wire.TranscriptItem, error) {
	p, err := f.pageIndexed(0, limit)
	return append([]wire.TranscriptItem{{Seq: -1, Kind: wire.ItemSystem, Text: "Transcript restarted"}}, p.Items...), err
}

// tail is the items from cursor on, the cursor to continue from and
// the index's generation. An index of
// another generation than gen (the file shrank) sends the restart
// marker and the newest limit items, and continues from the new end.
func (f *transcriptFile) tail(from transcriptCursor, gen, limit int) ([]wire.TranscriptItem, transcriptCursor, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.update(); err != nil {
		return nil, from, gen, err
	}
	if f.gen != gen {
		items, err := f.restart(limit)
		return items, transcriptCursor{line: f.size}, f.gen, err
	}
	items, next, err := f.linesFrom(from, -1)
	return items, next, f.gen, err
}

// transcriptBatchLines caps lines parsed while holding the index lock.
const transcriptBatchLines = 64

// linesFrom is a bounded batch of items from cursor on, excluding
// sequences through after on the first batch. f.mu is held.
func (f *transcriptFile) linesFrom(from transcriptCursor, after int64) ([]wire.TranscriptItem, transcriptCursor, error) {
	i := sort.Search(len(f.offsets), func(i int) bool { return f.offsets[i] >= from.line })
	if i >= len(f.offsets) {
		return nil, transcriptCursor{line: f.size}, nil
	}
	fh, err := os.Open(f.path)
	if err != nil {
		return nil, transcriptCursor{}, fmt.Errorf("open transcript: %w", err)
	}
	defer fh.Close()
	var items []wire.TranscriptItem
	size := 0
	for j := i; j < len(f.offsets); j++ {
		if j-i >= transcriptBatchLines {
			return items, transcriptCursor{line: f.offsets[j]}, nil
		}
		off := f.offsets[j]
		raw, err := lineAt(fh, off, f.size)
		if err != nil {
			return nil, transcriptCursor{}, err
		}
		line := itemsFrom(f.kind, bytes.TrimRight(raw, "\r\n"), off)
		start := 0
		if j == i && off == from.line {
			start = from.item
		}
		for k := start; k < len(line); k++ {
			if after >= 0 && line[k].Seq <= after {
				continue
			}
			n := itemsSize(line[k : k+1])
			if n > TranscriptPageBytes {
				return nil, transcriptCursor{}, fmt.Errorf("transcript item at %d exceeds batch limit", line[k].Seq)
			}
			if size+n > TranscriptPageBytes {
				return items, transcriptCursor{line: off, item: k}, nil
			}
			items = append(items, line[k])
			size += n
		}
	}
	return items, transcriptCursor{line: f.size}, nil
}
