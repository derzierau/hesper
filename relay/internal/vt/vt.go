// Package vt is a small terminal emulator: it keeps the screen a program's
// output draws (a grid of cells with their attributes, the cursor and the
// modes a viewer needs) from the bytes alone, so hesperd keeps each agent's
// screen (internal/ptyhost) to redraw viewers and answer terminal queries.
//
// It covers what a terminal interprets for the agents' TUIs (Claude Code,
// Codex) and shells: UTF-8 with wide and combining characters, SGR (16, 256
// and RGB colors, colon forms, underline styles), cursor movement and
// save/restore, erase and insert/delete of characters and lines, scroll
// regions and scrolling, origin and autowrap modes with the deferred wrap,
// insert mode, tab stops, the alternate screen (47, 1047, 1048, 1049), the
// DEC line drawing set, and the modes a viewer must follow (cursor keys,
// keypad, bracketed paste, cursor visibility, synchronized updates). Mouse,
// title, clipboard and other reports are ignored here (ptyhost answers the
// queries it supports).
//
// Not a full xterm; tmux is its ground truth in the tests (Load reads a
// tmux capture). Not safe for concurrent use; callers lock.
package vt

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

// Color is a cell color: Default, an indexed color (0–255) or RGB.
type Color uint32

const (
	Default      Color = 0
	indexedColor Color = 1 << 24
	rgbColor     Color = 2 << 24
)

// Indexed is palette color i.
func Indexed(i uint8) Color { return indexedColor | Color(i) }

// RGB is a 24-bit color.
func RGB(r, g, b uint8) Color { return rgbColor | Color(r)<<16 | Color(g)<<8 | Color(b) }

// Attribute flags.
const (
	Bold uint16 = 1 << iota
	Dim
	Italic
	Blink
	Reverse
	Hidden
	Strike
	Overline
)

// Attr is a cell's look. Underline is 0 (none) or a style: 1 single,
// 2 double, 3 curly, 4 dotted, 5 dashed.
type Attr struct {
	FG, BG, UL Color
	Flags      uint16
	Underline  uint8
}

// Cell is one column of a row. Width 2 starts a wide character, whose
// second column is a cell of width 0.
type Cell struct {
	R     rune
	Comb  string // combining characters after R
	Width uint8
	A     Attr
}

func blank(a Attr) Cell { return Cell{R: ' ', Width: 1, A: Attr{BG: a.BG}} }

// Modes the viewer follows.
type Modes struct {
	AppCursor, AppKeypad, BracketedPaste, CursorVisible, Sync, Alt bool
	// Mouse is the mouse tracking mode on (9, 1000, 1001, 1002 or 1003;
	// 0: off), MouseFormat its encoding (1005, 1006, 1015, 1016; 0: X10).
	Mouse, MouseFormat int
	// Focus: focus in/out reports (1004).
	Focus bool
	// Keyboard is the kitty keyboard protocol's flags in effect (CSI = u,
	// CSI > u, CSI < u), CursorStyle the DECSCUSR shape (0: default).
	Keyboard, CursorStyle int
}

type saved struct {
	x, y    int
	pen     Attr
	origin  bool
	g       [2]bool
	shift   int
	pending bool
}

// Screen is the emulated screen of one pane.
type Screen struct {
	w, h       int
	main, alt  [][]Cell
	grid       [][]Cell
	altOn      bool
	x, y       int
	pending    bool // the deferred wrap after writing the last column
	pen        Attr
	top, bot   int // scroll region, inclusive
	wrap       bool
	origin     bool
	insert     bool
	modes      Modes
	tabs       []bool
	g          [2]bool // G0/G1 is the DEC line drawing set
	shift      int     // 0: G0, 1: G1 (SO/SI)
	save, aSav saved
	dirty      []bool
	keys       []int   // kitty keyboard flags pushed before the current ones
	hist       history // scrollback (history.go)
	// MainUnknown: the main screen was not seen (loaded while the
	// alternate screen was on); leaving the alternate screen then
	// cannot restore it and the caller should resync.
	MainUnknown bool
	// Resync tells the caller the emulator cannot follow (e.g. leaving an
	// unknown alternate screen) and the screen should be reloaded; the
	// caller clears it.
	Resync bool

	// parser
	state   int
	params  []int  // flattened parameters, -1 = default
	sub     []bool // params[i] is a colon subparameter of the one before
	private byte
	inter   []byte
	utf     []byte
	zwj     bool // the last character was a zero width joiner
}

const (
	ground = iota
	escape
	escInter
	csi
	osc
	oscEsc
	str // DCS, SOS, PM, APC: ignored until ST
	strEsc
	charset
)

// New returns a blank screen of w columns and h rows.
func New(w, h int) *Screen {
	s := &Screen{}
	s.reset(w, h)
	return s
}

func newGrid(w, h int) [][]Cell {
	g := make([][]Cell, h)
	for i := range g {
		g[i] = make([]Cell, w)
		for j := range g[i] {
			g[i][j] = blank(Attr{})
		}
	}
	return g
}

func (s *Screen) reset(w, h int) {
	w, h = max(w, 1), max(h, 1)
	hist := s.hist // a reset keeps the scrollback
	*s = Screen{hist: hist, w: w, h: h, main: newGrid(w, h), alt: newGrid(w, h), wrap: true, bot: h - 1, dirty: make([]bool, h)}
	s.grid = s.main
	s.modes.CursorVisible = true
	s.tabs = make([]bool, w)
	for i := 8; i < w; i += 8 {
		s.tabs[i] = true
	}
	s.markAll()
}

// Size is the screen's columns and rows.
func (s *Screen) Size() (int, int) { return s.w, s.h }

// Cursor is the cursor position (column, row).
func (s *Screen) Cursor() (int, int) { return s.x, s.y }

// Modes returns the viewer-relevant modes.
func (s *Screen) Modes() Modes { m := s.modes; m.Alt = s.altOn; return m }

// Row returns row y (not a copy).
func (s *Screen) Row(y int) []Cell { return s.grid[y] }

// Dirty reports whether row y changed since ClearDirty.
func (s *Screen) Dirty(y int) bool { return s.dirty[y] }

// ClearDirty forgets the changes.
func (s *Screen) ClearDirty() { clear(s.dirty) }

func (s *Screen) markAll() {
	for i := range s.dirty {
		s.dirty[i] = true
	}
}

// Seed is the state Load cannot read from a capture: tmux's
// display-message formats of the pane.
type Seed struct {
	Width, Height                             int
	CursorX, CursorY                          int
	CursorVisible, AppCursor, AppKeypad       bool
	BracketedPaste, Alt, Insert, Wrap, Origin bool
	Top, Bottom                               int
}

// SeedFormat is the tmux format (display-message -p) ParseSeed reads.
const SeedFormat = "#{pane_width} #{pane_height} #{cursor_x} #{cursor_y} #{cursor_flag} #{keypad_cursor_flag} #{keypad_flag} #{bracket_paste_flag} " +
	"#{alternate_on} #{scroll_region_upper} #{scroll_region_lower} #{insert_flag} #{wrap_flag} #{origin_flag}"

// ParseSeed reads a line of SeedFormat.
func ParseSeed(line string) (Seed, bool) {
	f := strings.Fields(line)
	if len(f) != 14 {
		return Seed{}, false
	}
	n := make([]int, len(f))
	for i, v := range f {
		var err error
		if n[i], err = strconv.Atoi(v); err != nil {
			return Seed{}, false
		}
	}
	if n[0] < 1 || n[1] < 1 || n[0] > 10000 || n[1] > 10000 {
		return Seed{}, false
	}
	return Seed{Width: n[0], Height: n[1], CursorX: n[2], CursorY: n[3], CursorVisible: n[4] == 1, AppCursor: n[5] == 1, AppKeypad: n[6] == 1,
		BracketedPaste: n[7] == 1, Alt: n[8] == 1, Top: n[9], Bottom: n[10], Insert: n[11] == 1, Wrap: n[12] == 1, Origin: n[13] == 1}, true
}

// Load replaces the screen with a capture of the pane (tmux capture-pane
// -p -e -N: one line per row, attributes continuing across lines, line
// drawing between SO and SI) and the state tmux reports.
func (s *Screen) Load(rows []string, seed Seed) {
	s.reset(seed.Width, seed.Height)
	if seed.Alt {
		s.altOn, s.grid, s.MainUnknown = true, s.alt, true
	}
	s.wrap = false
	s.g[1] = true // capture-pane marks line drawing with SO/SI
	for y, row := range rows {
		if y >= s.h {
			break
		}
		s.x, s.y, s.pending, s.state = 0, y, false, ground
		s.WriteString(row)
	}
	s.state, s.utf, s.pen, s.g, s.shift = ground, nil, Attr{}, [2]bool{}, 0
	s.wrap, s.origin, s.insert = seed.Wrap, seed.Origin, seed.Insert
	s.top, s.bot = 0, s.h-1
	if seed.Top >= 0 && seed.Top < seed.Bottom && seed.Bottom < s.h {
		s.top, s.bot = seed.Top, seed.Bottom
	}
	s.x, s.y, s.pending = clamp(seed.CursorX, 0, s.w-1), clamp(seed.CursorY, 0, s.h-1), false
	s.modes = Modes{AppCursor: seed.AppCursor, AppKeypad: seed.AppKeypad, BracketedPaste: seed.BracketedPaste, CursorVisible: seed.CursorVisible}
	s.save = saved{x: 0, y: 0}
	s.aSav = s.save
	s.Resync = false
	s.markAll()
}

func clamp(v, lo, hi int) int { return min(max(v, lo), hi) }

// Write feeds program output.
func (s *Screen) Write(p []byte) (int, error) {
	for _, b := range p {
		s.feed(b)
	}
	return len(p), nil
}

// WriteString feeds program output.
func (s *Screen) WriteString(p string) {
	for i := 0; i < len(p); i++ {
		s.feed(p[i])
	}
}

func (s *Screen) feed(b byte) {
	// C0 controls act in the middle of sequences too (as in tmux), except
	// in strings.
	if b < 0x20 && s.state != osc && s.state != str && s.state != oscEsc && s.state != strEsc {
		switch b {
		case 0x1b:
			s.utf, s.zwj = s.utf[:0], false
			s.state = escape
			s.params, s.sub, s.inter, s.private = s.params[:0], s.sub[:0], s.inter[:0], 0
			return
		case 0x18, 0x1a: // CAN, SUB
			s.state = ground
			return
		}
		s.control(b)
		return
	}
	switch s.state {
	case ground:
		s.text(b)
	case escape:
		switch {
		case b >= 0x20 && b <= 0x2f:
			s.inter = append(s.inter, b)
			if b == '(' || b == ')' {
				s.state = charset
			} else {
				s.state = escInter
			}
		case b == '[':
			s.state = csi
		case b == ']':
			s.state = osc
		case b == 'P' || b == 'X' || b == '^' || b == '_':
			s.state = str
		default:
			s.state = ground
			s.esc(b)
		}
	case escInter:
		if b >= 0x20 && b <= 0x2f {
			s.inter = append(s.inter, b)
			return
		}
		s.state = ground
		if len(s.inter) == 1 && s.inter[0] == '#' && b == '8' {
			s.alignment()
		}
	case charset:
		s.state = ground
		switch {
		case len(s.inter) != 1:
		case s.inter[0] == '(':
			s.g[0] = b == '0'
		case s.inter[0] == ')':
			s.g[1] = b == '0'
		}
	case csi:
		switch {
		case b >= '0' && b <= '9':
			if len(s.params) == 0 {
				s.params, s.sub = append(s.params, -1), append(s.sub, false)
			}
			i := len(s.params) - 1
			if s.params[i] < 0 {
				s.params[i] = 0
			}
			if s.params[i] < 100000 {
				s.params[i] = s.params[i]*10 + int(b-'0')
			}
		case b == ';' || b == ':':
			if len(s.params) == 0 {
				s.params, s.sub = append(s.params, -1), append(s.sub, false)
			}
			if len(s.params) < 64 {
				s.params, s.sub = append(s.params, -1), append(s.sub, b == ':')
			}
		case b >= '<' && b <= '?':
			if len(s.params) == 0 && s.private == 0 {
				s.private = b
			}
		case b >= 0x20 && b <= 0x2f:
			s.inter = append(s.inter, b)
		case b >= 0x40 && b <= 0x7e:
			s.state = ground
			s.csi(b)
		default:
			s.state = ground
		}
	case osc:
		switch b {
		case 0x07:
			s.state = ground
		case 0x1b:
			s.state = oscEsc
		}
	case oscEsc:
		if b == '\\' {
			s.state = ground
		} else {
			s.state = osc
		}
	case str:
		if b == 0x1b {
			s.state = strEsc
		}
	case strEsc:
		if b == '\\' {
			s.state = ground
		} else {
			s.state = str
		}
	}
}

func (s *Screen) control(b byte) {
	s.zwj = false
	switch b {
	case 0x08: // BS
		if s.pending {
			s.pending = false
		} else if s.x > 0 {
			s.x--
		}
	case 0x09: // HT
		s.pending = false
		for s.x < s.w-1 {
			s.x++
			if s.tabs[s.x] {
				break
			}
		}
	case 0x0a, 0x0b, 0x0c: // LF, VT, FF
		s.index()
	case 0x0d: // CR
		s.x, s.pending = 0, false
	case 0x0e: // SO
		s.shift = 1
	case 0x0f: // SI
		s.shift = 0
	}
}

// decGraphics maps the DEC special graphics set (ESC ( 0) to Unicode.
var decGraphics = map[byte]rune{
	'`': '◆', 'a': '▒', 'b': '␉', 'c': '␌', 'd': '␍', 'e': '␊', 'f': '°', 'g': '±', 'h': '␤', 'i': '␋',
	'j': '┘', 'k': '┐', 'l': '┌', 'm': '└', 'n': '┼', 'o': '⎺', 'p': '⎻', 'q': '─', 'r': '⎼', 's': '⎽',
	't': '├', 'u': '┤', 'v': '┴', 'w': '┬', 'x': '│', 'y': '≤', 'z': '≥', '{': 'π', '|': '≠', '}': '£', '~': '·',
}

func (s *Screen) text(b byte) {
	if b == 0x7f {
		return
	}
	if len(s.utf) == 0 {
		switch {
		case b < 0x80:
			r := rune(b)
			if s.g[s.shift] {
				if g, ok := decGraphics[b]; ok {
					r = g
				}
			}
			s.put(r)
		case b < 0xc2 || b > 0xf4: // a stray continuation or an invalid start
			s.put(utf8.RuneError)
		default:
			s.utf = append(s.utf, b)
		}
		return
	}
	if b&0xc0 != 0x80 {
		// Anything but a continuation cuts the unfinished character.
		s.utf = s.utf[:0]
		s.put(utf8.RuneError)
		s.text(b)
		return
	}
	s.utf = append(s.utf, b)
	if utf8.FullRune(s.utf) {
		r, _ := utf8.DecodeRune(s.utf)
		s.utf = s.utf[:0]
		s.put(r)
	}
}

// put writes one character at the cursor.
func (s *Screen) put(r rune) {
	width := RuneWidth(r)
	if width == 0 || s.zwj {
		// After a zero width joiner the next character joins the cell
		// before (as tmux does with emoji sequences).
		s.combine(r)
		return
	}
	if s.pending {
		if s.wrap {
			s.x, s.pending = 0, false
			s.index()
		} else {
			s.pending = false
		}
	}
	if width == 2 && s.x == s.w-1 {
		if !s.wrap {
			return // no room, tmux drops it
		}
		s.erase(s.y, s.x, s.x+1)
		s.x = 0
		s.index()
	}
	if width > s.w {
		return
	}
	row := s.grid[s.y]
	if s.insert {
		copy(row[s.x+width:], row[s.x:])
		s.fixEdge(s.y)
	}
	s.clearWide(s.y, s.x)
	if width == 2 {
		s.clearWide(s.y, s.x+1)
	}
	row[s.x] = Cell{R: r, Width: uint8(width), A: s.pen}
	if width == 2 {
		row[s.x+1] = Cell{Width: 0, A: s.pen}
	}
	s.dirty[s.y] = true
	if s.x+width >= s.w {
		s.x = s.w - 1
		s.pending = true
	} else {
		s.x += width
	}
}

// combine appends a zero-width character to the one before the cursor.
func (s *Screen) combine(r rune) {
	joined := s.zwj
	s.zwj = false
	x, y := s.x-1, s.y
	if s.pending {
		x = s.x
	}
	if x < 0 {
		return
	}
	row := s.grid[y]
	if row[x].Width == 0 && x > 0 {
		x--
	}
	if row[x].R == ' ' && row[x].Comb == "" && r != 0x200d {
		return
	}
	s.zwj = r == 0x200d
	if len(row[x].Comb) < 32 {
		row[x].Comb += string(r)
	}
	if joined {
		s.dirty[y] = true
		return
	}
	// An emoji presentation selector widens a narrow emoji (as tmux does).
	if r == 0xfe0f && row[x].Width == 1 && emojiDefaultText(row[x].R) && x+1 < s.w && !s.pending {
		row[x].Width = 2
		s.clearWide(y, x+1)
		row[x+1] = Cell{Width: 0, A: row[x].A}
		if s.x+1 >= s.w {
			s.x, s.pending = s.w-1, true
		} else {
			s.x++
		}
	}
	s.dirty[y] = true
}

// clearWide blanks the other half of a wide character at (y, x) before x
// is overwritten.
func (s *Screen) clearWide(y, x int) {
	row := s.grid[y]
	if x < 0 || x >= s.w {
		return
	}
	switch {
	case row[x].Width == 0 && x > 0:
		row[x-1] = blank(row[x-1].A)
	case row[x].Width == 2 && x+1 < s.w:
		row[x+1] = blank(row[x+1].A)
	}
}

// fixEdge blanks a wide character cut at the right edge.
func (s *Screen) fixEdge(y int) {
	row := s.grid[y]
	if last := row[s.w-1]; last.Width == 2 {
		row[s.w-1] = blank(last.A)
	}
	if row[0].Width == 0 {
		row[0] = blank(row[0].A)
	}
}

// erase blanks columns [from, to) of row y with the current background.
func (s *Screen) erase(y, from, to int) {
	from, to = max(from, 0), min(to, s.w)
	if from >= to {
		return
	}
	s.clearWide(y, from)
	s.clearWide(y, to-1)
	row := s.grid[y]
	for i := from; i < to; i++ {
		row[i] = blank(s.pen)
	}
	s.dirty[y] = true
}

func (s *Screen) eraseRows(from, to int) {
	for y := max(from, 0); y < min(to, s.h); y++ {
		s.erase(y, 0, s.w)
	}
}

// scrollUp moves rows top..bot up by n, blank rows at the bottom.
func (s *Screen) scrollUp(top, bot, n int) {
	n = min(n, bot-top+1)
	if n <= 0 {
		return
	}
	g := s.grid
	moved := make([][]Cell, n)
	copy(moved, g[top:top+n])
	if top == 0 {
		// Lines leaving the top of the main screen: the scrollback.
		for _, row := range moved {
			s.pushHistory(row)
		}
	}
	copy(g[top:], g[top+n:bot+1])
	for i, row := range moved {
		g[bot-n+1+i] = row
		for j := range row {
			row[j] = blank(s.pen)
		}
	}
	for y := top; y <= bot; y++ {
		s.dirty[y] = true
	}
}

// scrollDown moves rows top..bot down by n, blank rows at the top.
func (s *Screen) scrollDown(top, bot, n int) {
	n = min(n, bot-top+1)
	if n <= 0 {
		return
	}
	g := s.grid
	moved := make([][]Cell, n)
	copy(moved, g[bot-n+1:bot+1])
	copy(g[top+n:bot+1], g[top:bot-n+1])
	for i, row := range moved {
		g[top+i] = row
		for j := range row {
			row[j] = blank(s.pen)
		}
	}
	for y := top; y <= bot; y++ {
		s.dirty[y] = true
	}
}

func (s *Screen) index() {
	s.pending = false
	if s.y == s.bot {
		s.scrollUp(s.top, s.bot, 1)
	} else if s.y < s.h-1 {
		s.y++
	}
}

func (s *Screen) reverseIndex() {
	s.pending = false
	if s.y == s.top {
		s.scrollDown(s.top, s.bot, 1)
	} else if s.y > 0 {
		s.y--
	}
}

func (s *Screen) esc(b byte) {
	switch b {
	case '7':
		s.saveCursor()
	case '8':
		s.restoreCursor()
	case 'D':
		s.index()
	case 'E':
		s.x = 0
		s.index()
	case 'M':
		s.reverseIndex()
	case 'H':
		s.tabs[s.x] = true
	case 'c':
		s.reset(s.w, s.h)
	case '=':
		s.modes.AppKeypad = true
	case '>':
		s.modes.AppKeypad = false
	}
}

func (s *Screen) alignment() {
	for y := range s.h {
		for x := range s.w {
			s.grid[y][x] = Cell{R: 'E', Width: 1}
		}
		s.dirty[y] = true
	}
	s.x, s.y, s.pending = 0, 0, false
}

func (s *Screen) saveCursor() {
	v := saved{x: s.x, y: s.y, pen: s.pen, origin: s.origin, g: s.g, shift: s.shift, pending: s.pending}
	if s.altOn {
		s.aSav = v
	} else {
		s.save = v
	}
}

func (s *Screen) restoreCursor() {
	v := s.save
	if s.altOn {
		v = s.aSav
	}
	s.x, s.y, s.pen, s.origin, s.g, s.shift, s.pending = clamp(v.x, 0, s.w-1), clamp(v.y, 0, s.h-1), v.pen, v.origin, v.g, v.shift, v.pending
}

// param i, or def when missing or zero (as most CSI parameters).
func (s *Screen) param(i, def int) int {
	n := 0
	for j := range s.params {
		if s.sub[j] {
			continue
		}
		if n == i {
			if s.params[j] <= 0 {
				return def
			}
			return s.params[j]
		}
		n++
	}
	return def
}

func (s *Screen) count() int {
	n := 0
	for _, sub := range s.sub {
		if !sub {
			n++
		}
	}
	return n
}

func (s *Screen) moveTo(x, y int) {
	if s.origin {
		y = clamp(y+s.top, s.top, s.bot)
	}
	s.x, s.y, s.pending = clamp(x, 0, s.w-1), clamp(y, 0, s.h-1), false
}

func (s *Screen) csi(b byte) {
	if len(s.inter) > 0 || s.private == '>' || s.private == '<' || s.private == '=' {
		switch {
		case s.private == 0 && len(s.inter) == 1 && s.inter[0] == '!' && b == 'p':
			s.softReset()
		case s.private == 0 && len(s.inter) == 1 && s.inter[0] == ' ' && b == 'q':
			s.modes.CursorStyle = s.param(0, 0)
		case len(s.inter) == 0 && b == 'u':
			s.keyboard()
		}
		return // modifyOtherKeys, ...: not the screen's
	}
	if s.private == '?' {
		switch b {
		case 'h', 'l':
			for i := range s.params {
				if !s.sub[i] {
					s.privateMode(s.params[i], b == 'h')
				}
			}
		}
		return
	}
	n := s.param(0, 1)
	switch b {
	case '@': // ICH
		s.pending = false
		row := s.grid[s.y]
		n = min(n, s.w-s.x)
		s.clearWide(s.y, s.x)
		copy(row[s.x+n:], row[s.x:])
		for i := s.x; i < s.x+n; i++ {
			row[i] = blank(s.pen)
		}
		s.fixEdge(s.y)
		s.dirty[s.y] = true
	case 'A':
		top := 0
		if s.y >= s.top {
			top = s.top
		}
		s.y, s.pending = max(s.y-n, top), false
	case 'B', 'e':
		bot := s.h - 1
		if s.y <= s.bot {
			bot = s.bot
		}
		s.y, s.pending = min(s.y+n, bot), false
	case 'C', 'a':
		s.x, s.pending = min(s.x+n, s.w-1), false
	case 'D':
		if s.pending {
			s.pending = false
		}
		s.x = max(s.x-n, 0)
	case 'E', 'F':
		s.x, s.pending = 0, false
		if b == 'E' {
			s.y = min(s.y+n, s.h-1)
			if s.y-n <= s.bot {
				s.y = min(s.y, s.bot)
			}
		} else {
			s.y = max(s.y-n, 0)
			if s.y+n >= s.top {
				s.y = max(s.y, s.top)
			}
		}
	case 'G', '`':
		s.x, s.pending = clamp(n-1, 0, s.w-1), false
	case 'H', 'f':
		s.moveTo(s.param(1, 1)-1, s.param(0, 1)-1)
	case 'I':
		s.pending = false
		for range n {
			s.control(0x09)
		}
	case 'J':
		s.pending = false
		switch s.param(0, 0) {
		case 0:
			s.erase(s.y, s.x, s.w)
			s.eraseRows(s.y+1, s.h)
		case 1:
			s.eraseRows(0, s.y)
			s.erase(s.y, 0, s.x+1)
		case 2:
			s.eraseRows(0, s.h)
		case 3:
			s.ClearHistory() // erase saved lines
		}
	case 'K':
		s.pending = false
		switch s.param(0, 0) {
		case 0:
			s.erase(s.y, s.x, s.w)
		case 1:
			s.erase(s.y, 0, s.x+1)
		case 2:
			s.erase(s.y, 0, s.w)
		}
	case 'L':
		if s.y >= s.top && s.y <= s.bot {
			s.scrollDown(s.y, s.bot, n)
			s.x, s.pending = 0, false
		}
	case 'M':
		if s.y >= s.top && s.y <= s.bot {
			s.scrollUp(s.y, s.bot, n)
			s.x, s.pending = 0, false
		}
	case 'P': // DCH
		s.pending = false
		row := s.grid[s.y]
		n = min(n, s.w-s.x)
		s.clearWide(s.y, s.x)
		s.clearWide(s.y, s.x+n-1)
		if s.x+n < s.w {
			s.clearWide(s.y, s.x+n)
		}
		copy(row[s.x:], row[s.x+n:])
		for i := s.w - n; i < s.w; i++ {
			row[i] = blank(s.pen)
		}
		s.fixEdge(s.y)
		s.dirty[s.y] = true
	case 'S':
		s.scrollUp(s.top, s.bot, n)
	case 'T':
		if s.count() <= 1 {
			s.scrollDown(s.top, s.bot, n)
		}
	case 'X':
		s.pending = false
		s.erase(s.y, s.x, s.x+n)
	case 'Z':
		s.pending = false
		for range n {
			for s.x > 0 {
				s.x--
				if s.tabs[s.x] {
					break
				}
			}
		}
	case 'b': // REP
		if s.x > 0 || s.pending {
			x := s.x - 1
			if s.pending {
				x = s.x
			}
			c := s.grid[s.y][x]
			if c.Width == 0 && x > 0 {
				c = s.grid[s.y][x-1]
			}
			if c.Width > 0 {
				for range min(n, 65535) {
					s.put(c.R)
				}
			}
		}
	case 'd':
		y := n - 1
		if s.origin {
			y += s.top
		}
		s.y, s.pending = clamp(y, 0, s.h-1), false
	case 'g':
		switch s.param(0, 0) {
		case 0:
			s.tabs[s.x] = false
		case 3:
			clear(s.tabs)
		}
	case 'h', 'l':
		for i := range s.params {
			if !s.sub[i] && s.params[i] == 4 {
				s.insert = b == 'h'
			}
		}
	case 'm':
		s.sgr()
	case 'r':
		top, bot := s.param(0, 1)-1, s.param(1, s.h)-1
		bot = min(bot, s.h-1)
		if top < bot {
			s.top, s.bot = top, bot
			s.moveTo(0, 0)
		}
	case 's':
		if len(s.params) == 0 {
			s.saveCursor()
		}
	case 'u':
		if len(s.params) == 0 {
			s.restoreCursor()
		}
	}
}

func (s *Screen) softReset() {
	s.pen, s.insert, s.origin, s.wrap = Attr{}, false, false, true
	s.top, s.bot = 0, s.h-1
	s.modes.CursorVisible, s.modes.AppCursor, s.modes.AppKeypad = true, false, false
	s.g, s.shift = [2]bool{}, 0
}

func (s *Screen) privateMode(mode int, on bool) {
	switch mode {
	case 1:
		s.modes.AppCursor = on
	case 6:
		s.origin = on
		s.moveTo(0, 0)
	case 7:
		s.wrap = on
		if !on {
			s.pending = false
		}
	case 25:
		s.modes.CursorVisible = on
	case 47, 1047:
		s.switchScreen(on, false)
	case 1048:
		if on {
			s.saveCursor()
		} else {
			s.restoreCursor()
		}
	case 1049:
		s.switchScreen(on, true)
	case 2004:
		s.modes.BracketedPaste = on
	case 2026:
		s.modes.Sync = on
	case 9, 1000, 1001, 1002, 1003:
		if on {
			s.modes.Mouse = mode
		} else if s.modes.Mouse == mode {
			s.modes.Mouse = 0
		}
	case 1005, 1006, 1015, 1016:
		if on {
			s.modes.MouseFormat = mode
		} else if s.modes.MouseFormat == mode {
			s.modes.MouseFormat = 0
		}
	case 1004:
		s.modes.Focus = on
	}
}

// switchScreen enters or leaves the alternate screen (tmux clears it on
// entering; 1049 also saves and restores the cursor).
func (s *Screen) switchScreen(on, cursor bool) {
	if on == s.altOn {
		return
	}
	if on {
		if cursor {
			s.saveCursor()
		}
		s.altOn, s.grid = true, s.alt
		for y := range s.h {
			for x := range s.w {
				s.alt[y][x] = blank(Attr{})
			}
		}
	} else {
		s.altOn, s.grid = false, s.main
		if cursor {
			s.restoreCursor()
		}
		if s.MainUnknown {
			s.MainUnknown, s.Resync = false, true
		}
	}
	s.markAll()
}

// sgr applies Select Graphic Rendition parameters.
func (s *Screen) sgr() {
	if len(s.params) == 0 {
		s.pen = Attr{}
		return
	}
	p := s.params
	for i := 0; i < len(p); i++ {
		if s.sub[i] {
			continue
		}
		v := p[i]
		if v < 0 {
			v = 0
		}
		// Colon subparameters of this one.
		j := i + 1
		for j < len(p) && s.sub[j] {
			j++
		}
		subs := p[i+1 : j]
		switch {
		case v == 0:
			s.pen = Attr{}
		case v == 1:
			s.pen.Flags |= Bold
		case v == 2:
			s.pen.Flags |= Dim
		case v == 3:
			s.pen.Flags |= Italic
		case v == 4:
			style := 1
			if len(subs) > 0 {
				style = max(subs[0], 0)
			}
			s.pen.Underline = uint8(min(style, 5))
		case v == 5 && len(subs) == 1 && subs[0] == 3:
			// tmux's capture-pane writes overline (53) as 5:3.
			s.pen.Flags |= Overline
		case v == 5 || v == 6:
			s.pen.Flags |= Blink
		case v == 7:
			s.pen.Flags |= Reverse
		case v == 8:
			s.pen.Flags |= Hidden
		case v == 9:
			s.pen.Flags |= Strike
		case v == 21:
			s.pen.Underline = 2
		case v == 22:
			s.pen.Flags &^= Bold | Dim
		case v == 23:
			s.pen.Flags &^= Italic
		case v == 24:
			s.pen.Underline = 0
		case v == 25:
			s.pen.Flags &^= Blink
		case v == 27:
			s.pen.Flags &^= Reverse
		case v == 28:
			s.pen.Flags &^= Hidden
		case v == 29:
			s.pen.Flags &^= Strike
		case v >= 30 && v <= 37:
			s.pen.FG = Indexed(uint8(v - 30))
		case v == 39:
			s.pen.FG = Default
		case v >= 40 && v <= 47:
			s.pen.BG = Indexed(uint8(v - 40))
		case v == 49:
			s.pen.BG = Default
		case v == 53:
			s.pen.Flags |= Overline
		case v == 55:
			s.pen.Flags &^= Overline
		case v == 59:
			s.pen.UL = Default
		case v >= 90 && v <= 97:
			s.pen.FG = Indexed(uint8(v - 90 + 8))
		case v >= 100 && v <= 107:
			s.pen.BG = Indexed(uint8(v - 100 + 8))
		case v == 38 || v == 48 || v == 58:
			var c Color
			var ok bool
			if len(subs) > 0 {
				c, ok = extendedColor(subs, true)
			} else {
				var used int
				c, ok, used = extendedSemicolon(p[i+1:], s.sub[i+1:])
				j = i + 1 + used
			}
			if ok {
				switch v {
				case 38:
					s.pen.FG = c
				case 48:
					s.pen.BG = c
				default:
					s.pen.UL = c
				}
			}
		}
		i = j - 1
	}
}

// extendedColor reads 5:N or 2:[colorspace:]R:G:B (colon form).
func extendedColor(subs []int, colon bool) (Color, bool) {
	if len(subs) == 0 {
		return 0, false
	}
	switch subs[0] {
	case 5:
		if len(subs) >= 2 && subs[1] >= 0 && subs[1] <= 255 {
			return Indexed(uint8(subs[1])), true
		}
	case 2:
		rgb := subs[1:]
		if colon && len(rgb) >= 4 {
			rgb = rgb[1:] // the colorspace ID
		}
		if len(rgb) >= 3 {
			c := [3]uint8{}
			for k := range 3 {
				c[k] = uint8(clamp(rgb[k], 0, 255))
			}
			return RGB(c[0], c[1], c[2]), true
		}
	}
	return 0, false
}

// extendedSemicolon reads 38;5;N or 38;2;R;G;B and returns how many
// parameters it used.
func extendedSemicolon(p []int, sub []bool) (Color, bool, int) {
	if len(p) == 0 {
		return 0, false, 0
	}
	switch p[0] {
	case 5:
		if len(p) >= 2 {
			c, ok := extendedColor(p[:2], false)
			return c, ok, 2
		}
		return 0, false, len(p)
	case 2:
		if len(p) >= 4 {
			c, ok := extendedColor(p[:4], false)
			return c, ok, 4
		}
		return 0, false, len(p)
	}
	return 0, false, 1
}

// RowANSI is row y as terminal output, at most limit columns of it (all
// with limit <= 0): characters with SGR sequences from the default
// attributes on, trailing default blanks left out, a wide character that
// does not fit whole left out too. It returns the columns the output
// covers. The caller resets attributes before and after it.
func (s *Screen) RowANSI(y, limit int) (string, int) {
	return rowANSI(s.grid[y], limit)
}

func rowANSI(row []Cell, limit int) (string, int) {
	end := len(row)
	if limit > 0 && limit < end {
		end = limit
		if row[end].Width == 0 {
			end-- // the first half of a cut wide character
		}
	}
	for end > 0 && row[end-1].R == ' ' && row[end-1].Comb == "" && row[end-1].A == (Attr{}) && row[end-1].Width == 1 {
		end--
	}
	var b strings.Builder
	b.Grow(end + 16)
	var cur Attr
	for x := 0; x < end; x++ {
		c := row[x]
		if c.Width == 0 {
			continue
		}
		if c.A != cur {
			b.WriteString(SGR(c.A))
			cur = c.A
		}
		b.WriteRune(c.R)
		b.WriteString(c.Comb)
	}
	return b.String(), end
}

// Blank reports whether row y has no characters (only spaces).
func (s *Screen) Blank(y int) bool {
	for _, c := range s.grid[y] {
		if c.Width != 0 && (c.R != ' ' || c.Comb != "") {
			return false
		}
	}
	return true
}

// Text is row y's characters without attributes (tests, debugging).
func (s *Screen) Text(y int) string {
	var b strings.Builder
	for _, c := range s.grid[y] {
		if c.Width != 0 {
			b.WriteRune(c.R)
			b.WriteString(c.Comb)
		}
	}
	return b.String()
}

// SGR is the sequence that sets a from the default attributes.
func SGR(a Attr) string { return string(AppendSGR(nil, a)) }

// AppendSGR appends SGR(a) to b.
func AppendSGR(b []byte, a Attr) []byte {
	b = append(b, "\x1b[0"...)
	for _, f := range sgrFlags[:3] {
		if a.Flags&f.flag != 0 {
			b = append(b, f.code...)
		}
	}
	switch a.Underline {
	case 0:
	case 1:
		b = append(b, ";4"...)
	default:
		b = append(b, ";4:"...)
		b = strconv.AppendInt(b, int64(a.Underline), 10)
	}
	for _, f := range sgrFlags[3:] {
		if a.Flags&f.flag != 0 {
			b = append(b, f.code...)
		}
	}
	// Semicolon forms for colors (every terminal reads them), the colon
	// form only for the underline color, which only newer terminals have.
	b = appendColor(b, a.FG, 30, ";38", ";")
	b = appendColor(b, a.BG, 40, ";48", ";")
	b = appendColor(b, a.UL, 0, ";58", ":")
	return append(b, 'm')
}

var sgrFlags = [...]struct {
	flag uint16
	code string
}{{Bold, ";1"}, {Dim, ";2"}, {Italic, ";3"}, {Blink, ";5"}, {Reverse, ";7"}, {Hidden, ";8"}, {Strike, ";9"}, {Overline, ";53"}}

func appendColor(b []byte, c Color, base int, prefix, sep string) []byte {
	switch c & (3 << 24) {
	case indexedColor:
		i := int(c & 0xff)
		switch {
		case base != 0 && i < 8:
			b = append(b, ';')
			b = strconv.AppendInt(b, int64(base+i), 10)
		case base != 0 && i < 16:
			b = append(b, ';')
			b = strconv.AppendInt(b, int64(base+60+i-8), 10)
		default:
			b = append(b, prefix...)
			b = append(b, sep...)
			b = append(b, '5')
			b = append(b, sep...)
			b = strconv.AppendInt(b, int64(i), 10)
		}
	case rgbColor:
		b = append(b, prefix...)
		b = append(b, sep...)
		b = append(b, '2')
		b = append(b, sep...)
		if sep == ":" {
			b = append(b, ':')
		}
		b = strconv.AppendInt(b, int64(c>>16&0xff), 10)
		b = append(b, sep...)
		b = strconv.AppendInt(b, int64(c>>8&0xff), 10)
		b = append(b, sep...)
		b = strconv.AppendInt(b, int64(c&0xff), 10)
	}
	return b
}
