package vt

import (
	"strconv"
	"unicode/utf8"
)

// keyboard applies the kitty keyboard protocol's CSI = u (set), CSI > u
// (push) and CSI < u (pop); the query (CSI ? u) is the caller's to answer.
func (s *Screen) keyboard() {
	switch s.private {
	case '=':
		flags, mode := s.param(0, 0), s.param(1, 1)
		switch mode {
		case 1:
			s.modes.Keyboard = flags
		case 2:
			s.modes.Keyboard |= flags
		case 3:
			s.modes.Keyboard &^= flags
		}
	case '>':
		if len(s.keys) >= 16 {
			s.keys = s.keys[1:]
		}
		s.keys = append(s.keys, s.modes.Keyboard)
		s.modes.Keyboard = s.param(0, 0)
	case '<':
		for range s.param(0, 1) {
			if len(s.keys) == 0 {
				s.modes.Keyboard = 0
				break
			}
			s.modes.Keyboard = s.keys[len(s.keys)-1]
			s.keys = s.keys[:len(s.keys)-1]
		}
	}
}

// Resize changes the screen to w columns and h rows, keeping what fits:
// the rows from the top, or, when the cursor would fall off the bottom,
// the rows up to the cursor's. The scroll region becomes the whole screen.
// Programs redraw on SIGWINCH; this only keeps the screen sane until then.
func (s *Screen) Resize(w, h int) {
	w, h = max(w, 1), max(h, 1)
	if w == s.w && h == s.h {
		return
	}
	shift := max(s.y-(h-1), 0)
	resize := func(old [][]Cell, off int) [][]Cell {
		g := newGrid(w, h)
		for y := range h {
			if y+off >= len(old) {
				break
			}
			copy(g[y], old[y+off])
		}
		return g
	}
	mainOff, altOff := 0, 0
	if s.altOn {
		altOff = shift
	} else {
		mainOff = shift
	}
	s.main, s.alt = resize(s.main, mainOff), resize(s.alt, altOff)
	if s.altOn {
		s.grid = s.alt
	} else {
		s.grid = s.main
	}
	s.w, s.h = w, h
	s.dirty = make([]bool, h)
	for _, g := range [][][]Cell{s.main, s.alt} {
		for y := range h {
			row := g[y]
			if last := row[w-1]; last.Width == 2 {
				row[w-1] = blank(last.A)
			}
			if row[0].Width == 0 {
				row[0] = blank(row[0].A)
			}
		}
	}
	s.x, s.y, s.pending = clamp(s.x, 0, w-1), clamp(s.y-shift, 0, h-1), false
	s.top, s.bot = 0, h-1
	for _, v := range []*saved{&s.save, &s.aSav} {
		v.x, v.y = clamp(v.x, 0, w-1), clamp(v.y, 0, h-1)
	}
	tabs := make([]bool, w)
	copy(tabs, s.tabs)
	for x := len(s.tabs); x < w; x++ {
		tabs[x] = x%8 == 0
	}
	s.tabs = tabs
	s.markAll()
}

// Redraw appends to b the output that makes a terminal of the screen's size,
// in whatever state it was, show this screen exactly: both screens' cells
// with their attributes (the main screen under the alternate one), the
// saved cursors, scroll region, the modes a viewer follows (cursor keys,
// keypad, bracketed paste, mouse, focus, kitty keyboard flags, cursor
// shape and visibility), character sets, the pen, tab stops and the cursor,
// a pending wrap included. It is one synchronized update.
func (s *Screen) Redraw(b []byte) []byte {
	b = append(b, "\x1b[?2026h\x1b[0m\x1b[?1049l\x1b[r\x1b[?6l\x1b[?7l\x1b[4l\x1b(B\x1b)B\x0f\x1b[H\x1b[2J"...)
	b = s.paint(b, s.main)
	if s.altOn {
		b = s.saveState(b, s.save)
		b = append(b, "\x1b[?1049h\x1b[0m\x1b(B\x1b)B\x0f\x1b[2J"...)
		b = s.paint(b, s.alt)
		b = s.saveState(b, s.aSav)
		b = append(b, "\x1b7"...)
	} else {
		b = s.saveState(b, s.save)
		b = append(b, "\x1b7"...)
	}
	b = append(b, "\x1b[0m\x1b(B\x1b)B\x0f"...)
	b = append(b, "\x1b[3g"...)
	for x, on := range s.tabs {
		if on {
			b = cup(b, 0, x)
			b = append(b, "\x1bH"...)
		}
	}
	if s.top != 0 || s.bot != s.h-1 {
		b = append(b, "\x1b["...)
		b = strconv.AppendInt(b, int64(s.top+1), 10)
		b = append(b, ';')
		b = strconv.AppendInt(b, int64(s.bot+1), 10)
		b = append(b, 'r')
	}
	m := s.modes
	b = mode(b, "?1", m.AppCursor)
	if m.AppKeypad {
		b = append(b, "\x1b="...)
	} else {
		b = append(b, "\x1b>"...)
	}
	b = mode(b, "?2004", m.BracketedPaste)
	for _, n := range []int{9, 1000, 1001, 1002, 1003} {
		b = mode(b, "?"+strconv.Itoa(n), n == m.Mouse)
	}
	for _, n := range []int{1005, 1006, 1015, 1016} {
		b = mode(b, "?"+strconv.Itoa(n), n == m.MouseFormat)
	}
	b = mode(b, "?1004", m.Focus)
	b = append(b, "\x1b[<99u"...)
	if len(s.keys) > 0 {
		b = append(b, "\x1b[="...)
		b = strconv.AppendInt(b, int64(s.keys[0]), 10)
		b = append(b, ";1u"...)
		for _, k := range append(s.keys[1:len(s.keys):len(s.keys)], m.Keyboard) {
			b = append(b, "\x1b[>"...)
			b = strconv.AppendInt(b, int64(k), 10)
			b = append(b, 'u')
		}
	} else if m.Keyboard != 0 {
		b = append(b, "\x1b[="...)
		b = strconv.AppendInt(b, int64(m.Keyboard), 10)
		b = append(b, ";1u"...)
	}
	b = append(b, "\x1b["...)
	b = strconv.AppendInt(b, int64(m.CursorStyle), 10)
	b = append(b, " q"...)
	if s.origin {
		b = append(b, "\x1b[?6h"...)
	}
	if s.wrap {
		b = append(b, "\x1b[?7h"...)
	}
	// The cursor last: rewriting the cell before a pending wrap sets the
	// pending wrap again.
	if c := s.grid[s.y][s.x]; s.pending && s.wrap && c.Width == 1 {
		b = s.cupRel(b, s.y, s.x)
		b = AppendSGR(b, c.A)
		b = appendCell(b, c)
	} else if s.pending && s.wrap && s.x > 0 && s.grid[s.y][s.x-1].Width == 2 {
		c := s.grid[s.y][s.x-1]
		b = s.cupRel(b, s.y, s.x-1)
		b = AppendSGR(b, c.A)
		b = appendCell(b, c)
	} else {
		b = s.cupRel(b, s.y, s.x)
	}
	if s.insert {
		b = append(b, "\x1b[4h"...)
	}
	b = charsets(b, s.g, s.shift)
	b = AppendSGR(b, s.pen)
	b = mode(b, "?25", m.CursorVisible)
	if !m.Sync {
		b = append(b, "\x1b[?2026l"...)
	}
	return b
}

// Flags are the screen modes Modes leaves out: autowrap, origin, insert.
func (s *Screen) Flags() (wrap, origin, insert bool) { return s.wrap, s.origin, s.insert }

// saveState sets the cursor, pen and character sets of v, for a DECSC
// (or the one ?1049h does) to save.
func (s *Screen) saveState(b []byte, v saved) []byte {
	b = cup(b, v.y, v.x)
	b = AppendSGR(b, v.pen)
	return charsets(b, v.g, v.shift)
}

func charsets(b []byte, g [2]bool, shift int) []byte {
	if g[0] {
		b = append(b, "\x1b(0"...)
	}
	if g[1] {
		b = append(b, "\x1b)0"...)
	}
	if shift == 1 {
		b = append(b, 0x0e)
	}
	return b
}

// cupRel moves the cursor to (y, x) of the screen, relative to the scroll
// region when origin mode is on.
func (s *Screen) cupRel(b []byte, y, x int) []byte {
	if s.origin {
		y -= s.top
	}
	return cup(b, y, x)
}

func cup(b []byte, y, x int) []byte {
	b = append(b, "\x1b["...)
	b = strconv.AppendInt(b, int64(y+1), 10)
	b = append(b, ';')
	b = strconv.AppendInt(b, int64(x+1), 10)
	return append(b, 'H')
}

func mode(b []byte, n string, on bool) []byte {
	b = append(b, "\x1b["...)
	b = append(b, n...)
	if on {
		return append(b, 'h')
	}
	return append(b, 'l')
}

func appendCell(b []byte, c Cell) []byte {
	b = utf8.AppendRune(b, c.R)
	return append(b, c.Comb...)
}

// paint draws grid g, autowrap off, from a cleared screen at the default
// attributes; it leaves the attributes at the default.
func (s *Screen) paint(b []byte, g [][]Cell) []byte {
	for y, row := range g {
		end := len(row)
		for end > 0 && row[end-1].R == ' ' && row[end-1].Comb == "" && row[end-1].A == (Attr{}) && row[end-1].Width == 1 {
			end--
		}
		if end == 0 {
			continue
		}
		b = cup(b, y, 0)
		var cur Attr
		for x := 0; x < end; x++ {
			c := row[x]
			if c.Width == 0 {
				continue
			}
			if c.A != cur {
				b = AppendSGR(b, c.A)
				cur = c.A
			}
			b = appendCell(b, c)
		}
		if cur != (Attr{}) {
			b = append(b, "\x1b[0m"...)
		}
	}
	return b
}
