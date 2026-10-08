package ptyhost

import (
	"encoding/json"
	"strconv"

	"github.com/derzierau/hesper/relay/internal/vt"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Scrolling a view (FrameScroll): the window can move up into the
// scrollback the screen copy keeps (vt history). While it is up, output
// never moves it: the lines that scroll off the top add to its offset, so
// it keeps showing the same lines, and the viewer is told how many new
// lines wait below ("↓ N new lines"). Offset 0 is live again.

// scrollTo applies a SCROLL frame from the viewer.
func (v *view) scrollTo(p []byte) {
	var sc wire.Scroll
	if json.Unmarshal(p, &sc) != nil {
		return
	}
	v.mu.Lock()
	if sc.Offset != nil {
		v.scroll = *sc.Offset
	} else {
		v.scroll += sc.Delta
	}
	v.scroll = max(v.scroll, 0)
	v.tell = true
	v.mu.Unlock()
	v.kick()
}

// place returns the offset to render (v.mu held; total: the screen's
// history total, under the Term's lock): a scrolled window follows the
// lines appended since it was placed.
func (v *view) place(total uint64) int {
	if v.scroll <= 0 {
		v.scroll, v.pinned, v.newLines = 0, false, 0
		return 0
	}
	if v.pinned && total > v.pinTotal {
		added := int(total - v.pinTotal)
		v.scroll += added
		v.newLines += added
	}
	v.pinned, v.pinTotal = true, total
	return v.scroll
}

// state is what to tell the viewer after a render (nil: nothing new). A
// live window that stays live is not reported on every line.
func (v *view) state(offset, maxOffset int) *wire.ScrollState {
	v.mu.Lock()
	defer v.mu.Unlock()
	if offset > maxOffset {
		// Clamped by the window: keep the real place.
		v.scroll = maxOffset
		offset = maxOffset
	}
	s := wire.ScrollState{Offset: offset, Max: maxOffset, New: v.newLines}
	if offset == 0 {
		s.New = 0
	}
	if s == v.told && !v.tell {
		return nil
	}
	if !v.tell && offset == 0 && v.told.Offset == 0 {
		v.told = s // live and was live: not worth a frame per line
		return nil
	}
	v.tell = false
	v.told = s
	return &s
}

// WindowAt is Window moved offset lines up into the scrollback (0: the
// live window). It also returns the largest offset there is (the window's
// top on the oldest scrollback line). The alternate screen has no
// scrollback: offset is ignored there. The cursor is hidden while the
// window is up.
func WindowAt(s *vt.Screen, cols, rows, offset int) ([]string, string, int) {
	if offset <= 0 || s.Modes().Alt {
		lines, cursor := Window(s, cols, rows)
		maxOffset := 0
		if !s.Modes().Alt {
			maxOffset = liveTop(s, rows) + s.HistoryLen()
		}
		return lines, cursor, maxOffset
	}
	w, h := s.Size()
	if cols <= 0 || cols > w {
		cols = w
	}
	rows = max(1, min(rows, h))
	hist := s.HistoryLen()
	maxOffset := liveTop(s, rows) + hist
	offset = min(offset, maxOffset)
	// Content index: scrollback lines, then screen rows.
	start := hist + liveTop(s, rows) - offset
	lines := make([]string, rows)
	for i := range rows {
		n := start + i
		if n < hist {
			lines[i] = vt.ClipANSI(s.HistoryLine(n), cols)
		} else {
			lines[i], _ = s.RowANSI(n-hist, cols)
		}
	}
	return lines, "\x1b[?25l", maxOffset
}

// liveTop is the screen row the live window starts at (Window's rule).
func liveTop(s *vt.Screen, rows int) int {
	_, h := s.Size()
	rows = max(1, min(rows, h))
	_, cy := s.Cursor()
	bottom := cy
	for y := h - 1; y > bottom; y-- {
		if !s.Blank(y) {
			bottom = y
			break
		}
	}
	return min(max(bottom-rows+1, 0), h-rows)
}

// The most of the scrollback a read-write attach gets.
var (
	HistoryPreambleLines = 5000
	HistoryPreambleBytes = 1 << 20
)

// HistoryPreamble is what a read-write attach gets before its redraw: the
// scrollback printed from the top of a cleared screen, then, from the last
// row, one newline per line still on the screen, so every line ends up in
// the viewer's own scrollback in order (and nothing else does); the redraw
// paints the screen after it. Nothing on the alternate screen or with no
// scrollback.
func HistoryPreamble(s *vt.Screen, b []byte) []byte {
	total := s.HistoryLen()
	if total == 0 || s.Modes().Alt {
		return b
	}
	// The newest lines, bounded (an attach must stay well under a slow
	// viewer's 4 MiB).
	first, size := total, 0
	for first > 0 && total-first < HistoryPreambleLines {
		l := len(s.HistoryLine(first-1)) + 6
		if size+l > HistoryPreambleBytes {
			break
		}
		size += l
		first--
	}
	n := total - first
	if n == 0 {
		return b
	}
	_, h := s.Size()
	b = append(b, "\x1b[0m\x1b[?1049l\x1b[r\x1b[?7l\x1b[H\x1b[2J"...)
	for i := range n {
		if i > 0 {
			b = append(b, "\r\n"...)
		}
		b = append(b, s.HistoryLine(first+i)...)
		b = append(b, "\x1b[0m"...)
	}
	b = append(b, "\x1b["...)
	b = strconv.AppendInt(b, int64(h), 10)
	b = append(b, ";1H"...)
	for range min(n, h) {
		b = append(b, '\n')
	}
	return b
}
