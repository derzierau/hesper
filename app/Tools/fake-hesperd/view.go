package main

import (
	"strconv"
	"strings"
)

// viewFilter is the fake's stand-in for hesperd's view attach ("view":
// {rows, cols}): the real daemon renders the last rows from its screen
// copy; the fake has none, so it rewrites the fake TUI's own output, which
// only ever positions with CUP (ESC[r;cH) and never scrolls. Rows above the
// window are dropped, rows are shifted up, columns past the window are cut.
type viewFilter struct {
	rows, cols int
	hidden     bool   // the cursor is above the window: drop text
	col        int    // columns written since the last CUP
	esc        []byte // an escape sequence split across reads
}

// filter rewrites p for a PTY of ptyRows rows.
func (f *viewFilter) filter(p []byte, ptyRows int) []byte {
	offset := max(ptyRows-f.rows, 0)
	out := make([]byte, 0, len(p))
	for i := 0; i < len(p); i++ {
		b := p[i]
		if len(f.esc) > 0 || b == 0x1b {
			f.esc = append(f.esc, b)
			if !escDone(f.esc) {
				continue
			}
			seq := f.esc
			f.esc = nil
			out = f.sequence(out, seq, offset)
			continue
		}
		if f.hidden {
			continue
		}
		if b >= 0x20 && b&0xc0 != 0x80 { // a character's first byte
			f.col++
		}
		if f.cols > 0 && f.col > f.cols && b >= 0x20 {
			continue
		}
		out = append(out, b)
	}
	return out
}

func escDone(s []byte) bool {
	if len(s) < 2 {
		return false
	}
	if s[1] != '[' {
		// ESC ( B and friends take one more byte.
		if s[1] == '(' || s[1] == ')' {
			return len(s) >= 3
		}
		return true
	}
	if len(s) < 3 {
		return false
	}
	last := s[len(s)-1]
	return last >= 0x40 && last <= 0x7e
}

func (f *viewFilter) sequence(out, seq []byte, offset int) []byte {
	if len(seq) < 3 || seq[1] != '[' {
		return append(out, seq...)
	}
	final := seq[len(seq)-1]
	params := string(seq[2 : len(seq)-1])
	switch final {
	case 'H':
		row, col := 1, 1
		parts := strings.SplitN(params, ";", 2)
		if n, err := strconv.Atoi(parts[0]); err == nil {
			row = n
		}
		if len(parts) == 2 {
			if n, err := strconv.Atoi(parts[1]); err == nil {
				col = n
			}
		}
		row -= offset
		f.col = col - 1
		f.hidden = row < 1 || row > f.rows
		if f.hidden {
			return out
		}
		return append(out, "\x1b["+strconv.Itoa(row)+";"+strconv.Itoa(col)+"H"...)
	case 'J':
		return append(out, seq...)
	case 'K', 'm':
		if f.hidden {
			return out
		}
	}
	return append(out, seq...)
}
