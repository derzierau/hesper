package vt

import (
	"strings"
	"unicode/utf8"
)

// Scrollback: the lines that leave the top of the MAIN screen (a scroll
// with the region's top at row 0), kept as RowANSI strings (SGR + text,
// trailing blanks dropped) in a ring bounded by lines and bytes. The
// alternate screen has none (full-screen programs redraw themselves);
// ED 3 (erase saved lines) clears it; a full reset keeps it.

// History limits: lines and bytes (whichever is reached first).
var (
	DefaultHistoryLines = 10000
	DefaultHistoryBytes = 4 << 20
)

type history struct {
	lines      []string // ring
	start, n   int
	bytes      int
	limitLines int
	limitBytes int
	total      uint64 // lines ever added (a stable position for viewers)
}

func (h *history) init() {
	if h.limitLines == 0 {
		h.limitLines = DefaultHistoryLines
	}
	if h.limitBytes == 0 {
		h.limitBytes = DefaultHistoryBytes
	}
}

func (h *history) push(line string) {
	h.init()
	if h.limitLines <= 0 {
		return
	}
	for h.n > 0 && (h.n >= h.limitLines || h.bytes+len(line) > h.limitBytes) {
		h.bytes -= len(h.lines[h.start])
		h.lines[h.start] = ""
		h.start = (h.start + 1) % len(h.lines)
		h.n--
	}
	if h.n == len(h.lines) {
		// Grow the ring (in order) up to the line limit.
		size := min(max(64, 2*len(h.lines)), h.limitLines)
		grown := make([]string, size)
		for i := range h.n {
			grown[i] = h.lines[(h.start+i)%len(h.lines)]
		}
		h.lines, h.start = grown, 0
	}
	h.lines[(h.start+h.n)%len(h.lines)] = line
	h.n++
	h.bytes += len(line)
	h.total++
}

func (h *history) clear() {
	h.lines, h.start, h.n, h.bytes = nil, 0, 0, 0
}

// SetHistoryLimit bounds the scrollback (lines ≤ 0: none kept).
func (s *Screen) SetHistoryLimit(lines, bytes int) {
	s.hist.limitLines, s.hist.limitBytes = lines, bytes
	if lines <= 0 {
		s.hist.clear()
		s.hist.limitLines = -1
	}
}

// HistoryLen is how many scrollback lines there are.
func (s *Screen) HistoryLen() int { return s.hist.n }

// HistoryLine is scrollback line i (0: the oldest), as RowANSI renders a
// row: SGR sequences and text, no trailing blanks.
func (s *Screen) HistoryLine(i int) string {
	if i < 0 || i >= s.hist.n {
		return ""
	}
	return s.hist.lines[(s.hist.start+i)%len(s.hist.lines)]
}

// HistoryTotal counts every line ever added to the scrollback (dropped
// and cleared ones included): viewers keep their place with it.
func (s *Screen) HistoryTotal() uint64 { return s.hist.total }

// ClearHistory forgets the scrollback (ED 3).
func (s *Screen) ClearHistory() { s.hist.clear() }

func (s *Screen) pushHistory(row []Cell) {
	if s.altOn || s.hist.limitLines < 0 {
		return
	}
	line, _ := rowANSI(row, 0)
	s.hist.push(line)
}

// ClipANSI cuts a RowANSI line to cols columns (SGR sequences kept, a wide
// character that does not fit whole left out).
func ClipANSI(line string, cols int) string {
	if cols <= 0 {
		return line
	}
	var b strings.Builder
	w := 0
	for i := 0; i < len(line); {
		if line[i] == 0x1b && i+1 < len(line) && line[i+1] == '[' {
			j := i + 2
			for j < len(line) && (line[j] < 0x40 || line[j] > 0x7e) {
				j++
			}
			if j < len(line) {
				j++
			}
			b.WriteString(line[i:j])
			i = j
			continue
		}
		r, size := utf8.DecodeRuneInString(line[i:])
		rw := RuneWidth(r)
		if w+rw > cols {
			break
		}
		w += rw
		b.WriteString(line[i : i+size])
		i += size
	}
	return b.String()
}
