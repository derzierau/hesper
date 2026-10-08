package ptyhost

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"

	"github.com/derzierau/hesper/relay/internal/vt"
)

// The daemon is the agent's terminal: it answers the queries
// a program asks its terminal (device attributes, cursor position, colors,
// modes, kitty keyboard flags, window size) from its own screen copy and
// takes them out of the output viewers get, so no viewer answers too (a
// read-only tile could not, and two answers would confuse the program).
// Everything else passes through unchanged.

// Palette is what color queries (OSC 4, 10, 11, 12) answer: Tokyo Night.
var Palette = struct {
	FG, BG, Cursor string
	ANSI           [16]string
}{
	FG: "c0caf5", BG: "1a1b26", Cursor: "c0caf5",
	ANSI: [16]string{"15161e", "f7768e", "9ece6a", "e0af68", "7aa2f7", "bb9af7", "7dcfff", "a9b1d6",
		"414868", "f7768e", "9ece6a", "e0af68", "7aa2f7", "bb9af7", "7dcfff", "c0caf5"},
}

// Cell pixel size reported to CSI 14 t / 16 t.
const cellW, cellH = 8, 16

const (
	fGround = iota
	fEsc
	fCSI     // held: might be a query
	fCSIPass // too long to be one: passed until its final byte
	fOSC     // held
	fOSCEsc
	fOSCPass
	fOSCPassEsc
	fDCS // held
	fDCSEsc
	fStrPass // DCS that is no query, SOS, PM, APC: passed until ST
	fStrPassEsc
)

const holdLimit = 512

// filter splits program output into what passes to the screen and viewers
// and the queries the daemon answers. Sequences split across reads are held
// until complete (or until they cannot be queries).
type filter struct {
	state int
	held  []byte
}

// feed runs p through the filter: pass gets the bytes that go on, in
// order, query each complete query sequence (removed from the output).
func (f *filter) feed(p []byte, pass func([]byte), query func([]byte)) {
	start := 0 // first byte of p not yet passed or held
	flush := func(i int) {
		if i > start {
			pass(p[start:i])
		}
		start = i
	}
	hold := func(i int) { // p[start:i+1] joins held
		f.held = append(f.held, p[start:i+1]...)
		start = i + 1
	}
	release := func() {
		if len(f.held) > 0 {
			pass(f.held)
			f.held = nil
		}
	}
	for i := 0; i < len(p); i++ {
		b := p[i]
		switch f.state {
		case fGround:
			if b == 0x1b {
				flush(i)
				hold(i)
				f.state = fEsc
			}
		case fEsc:
			hold(i)
			switch b {
			case '[':
				f.state = fCSI
			case ']':
				f.state = fOSC
			case 'P':
				f.state = fDCS
			case 'X', '^', '_':
				release()
				f.state = fStrPass
			case 0x1b:
				f.held = f.held[1:] // the first ESC was nothing: pass it
				pass([]byte{0x1b})
			default:
				release()
				f.state = fGround
			}
		case fCSI:
			switch {
			case b == 0x1b:
				release()
				hold(i)
				f.state = fEsc
			case b == 0x18 || b == 0x1a:
				hold(i)
				release()
				f.state = fGround
			case b >= 0x40 && b <= 0x7e:
				hold(i)
				if isCSIQuery(f.held) {
					query(f.held)
					f.held = nil
				} else {
					release()
				}
				f.state = fGround
			default:
				hold(i)
				if len(f.held) > 64 {
					release()
					f.state = fCSIPass
				}
			}
		case fCSIPass:
			switch {
			case b == 0x1b:
				flush(i)
				hold(i)
				f.state = fEsc
			case b == 0x18 || b == 0x1a || (b >= 0x40 && b <= 0x7e):
				f.state = fGround
			}
		case fOSC, fDCS:
			hold(i)
			switch {
			case b == 0x07 && f.state == fOSC:
				f.finishString(query, pass)
			case b == 0x1b:
				f.state++ // fOSCEsc, fDCSEsc
			case len(f.held) > holdLimit || !f.mightQuery():
				release()
				if f.state == fOSC {
					f.state = fOSCPass
				} else {
					f.state = fStrPass
				}
			}
		case fOSCEsc, fDCSEsc:
			hold(i)
			if b == '\\' {
				f.finishString(query, pass)
			} else {
				f.state-- // as the screen's parser: still in the string
			}
		case fOSCPass:
			switch b {
			case 0x07:
				f.state = fGround
			case 0x1b:
				f.state = fOSCPassEsc
			}
		case fOSCPassEsc, fStrPassEsc:
			if b == '\\' {
				f.state = fGround
			} else if f.state == fOSCPassEsc {
				f.state = fOSCPass
			} else {
				f.state = fStrPass
			}
		case fStrPass:
			if b == 0x1b {
				f.state = fStrPassEsc
			}
		}
	}
	if f.state == fGround || f.state == fCSIPass || f.state == fOSCPass || f.state == fOSCPassEsc || f.state == fStrPass || f.state == fStrPassEsc {
		flush(len(p))
	}
}

// mightQuery reports whether the held OSC or DCS can still be a query.
func (f *filter) mightQuery() bool {
	body := f.held[2:]
	if f.state == fDCS {
		// XTGETTCAP (+q) or DECRQSS ($q).
		return len(body) < 2 || bytes.HasPrefix(body, []byte("+q")) || bytes.HasPrefix(body, []byte("$q"))
	}
	n := bytes.IndexByte(body, ';')
	if n < 0 {
		return len(body) <= 3 && allDigits(body)
	}
	switch string(body[:n]) {
	case "4", "10", "11", "12", "52":
		return true
	}
	return false
}

// finishString answers a held OSC or DCS that is a query, else passes it.
func (f *filter) finishString(query, pass func([]byte)) {
	seq := f.held
	f.held = nil
	f.state = fGround
	if isStringQuery(seq) {
		query(seq)
	} else {
		pass(seq)
	}
}

func allDigits(b []byte) bool {
	for _, c := range b {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// csiParts splits ESC [ private params intermediates final.
func csiParts(seq []byte) (private byte, params string, inter string, final byte) {
	body := seq[2 : len(seq)-1]
	final = seq[len(seq)-1]
	if len(body) > 0 && body[0] >= '<' && body[0] <= '?' {
		private, body = body[0], body[1:]
	}
	end := len(body)
	for end > 0 && body[end-1] >= 0x20 && body[end-1] <= 0x2f {
		end--
	}
	return private, string(body[:end]), string(body[end:]), final
}

func isCSIQuery(seq []byte) bool {
	private, params, inter, final := csiParts(seq)
	switch {
	case inter == "" && final == 'n':
		return (private == 0 && (params == "5" || params == "6")) || (private == '?' && (params == "6" || params == "996"))
	case inter == "" && final == 'c':
		return (private == 0 || private == '>' || private == '=') && (params == "" || params == "0")
	case inter == "" && final == 'q':
		return private == '>' && (params == "" || params == "0")
	case inter == "" && final == 'u':
		return private == '?' && params == ""
	case inter == "$" && final == 'p':
		return (private == 0 || private == '?') && allDigits([]byte(params)) && params != ""
	case inter == "" && final == 't':
		return private == 0 && (params == "14" || params == "16" || params == "18")
	}
	return false
}

func isStringQuery(seq []byte) bool {
	if len(seq) < 3 {
		return false
	}
	body := stringBody(seq)
	if seq[1] == 'P' {
		return strings.HasPrefix(body, "+q") || strings.HasPrefix(body, "$q")
	}
	parts := strings.Split(body, ";")
	switch parts[0] {
	case "10", "11", "12":
		return len(parts) == 2 && parts[1] == "?"
	case "4":
		return len(parts) >= 3 && len(parts)%2 == 1 && parts[2] == "?"
	case "52":
		return len(parts) == 3 && parts[2] == "?"
	}
	return false
}

// stringBody is an OSC's or DCS's content without introducer and terminator.
func stringBody(seq []byte) string {
	body := seq[2:]
	switch {
	case bytes.HasSuffix(body, []byte("\x1b\\")):
		body = body[:len(body)-2]
	case bytes.HasSuffix(body, []byte{0x07}):
		body = body[:len(body)-1]
	}
	return string(body)
}

// answer is the reply to a query, from the screen (nil: none).
func answer(seq []byte, s *vt.Screen) []byte {
	if seq[1] == '[' {
		return answerCSI(seq, s)
	}
	body := stringBody(seq)
	st := "\x1b\\"
	if seq[len(seq)-1] == 0x07 {
		st = "\x07"
	}
	if seq[1] == 'P' {
		if strings.HasPrefix(body, "+q") {
			return []byte("\x1bP0+r\x1b\\")
		}
		return []byte("\x1bP0$r\x1b\\")
	}
	parts := strings.Split(body, ";")
	switch parts[0] {
	case "10":
		return []byte("\x1b]10;" + rgb(Palette.FG) + st)
	case "11":
		return []byte("\x1b]11;" + rgb(Palette.BG) + st)
	case "12":
		return []byte("\x1b]12;" + rgb(Palette.Cursor) + st)
	case "4":
		var b strings.Builder
		for i := 1; i+1 < len(parts); i += 2 {
			n, err := strconv.Atoi(parts[i])
			if err != nil || n < 0 || n > 255 || parts[i+1] != "?" {
				continue
			}
			b.WriteString("\x1b]4;" + parts[i] + ";" + rgb(paletteColor(n)) + st)
		}
		return []byte(b.String())
	}
	return nil // OSC 52 read: the clipboard stays private
}

func answerCSI(seq []byte, s *vt.Screen) []byte {
	private, params, inter, final := csiParts(seq)
	cols, rows := s.Size()
	switch final {
	case 'n':
		switch {
		case private == 0 && params == "5":
			return []byte("\x1b[0n")
		case params == "6":
			x, y := s.Cursor()
			if _, origin, _ := s.Flags(); origin {
				// Relative to the scroll region in origin mode: not tracked
				// separately; the absolute row is close enough.
				_ = origin
			}
			p := ""
			if private == '?' {
				p = "?"
			}
			return fmt.Appendf(nil, "\x1b[%s%d;%dR", p, y+1, x+1)
		case params == "996":
			return []byte("\x1b[?997;1n") // dark
		}
	case 'c':
		switch private {
		case 0:
			return []byte("\x1b[?62;22c")
		case '>':
			return []byte("\x1b[>1;10;0c")
		case '=':
			return []byte("\x1bP!|00000000\x1b\\")
		}
	case 'q':
		return []byte("\x1bP>|hesperd\x1b\\")
	case 'u':
		return fmt.Appendf(nil, "\x1b[?%du", s.Modes().Keyboard)
	case 'p':
		n, _ := strconv.Atoi(params)
		v := 0
		if private == '?' {
			v = privateModeValue(n, s)
		} else if n == 4 {
			_, _, insert := s.Flags()
			v = setReset(insert)
		}
		p := ""
		if private == '?' {
			p = "?"
		}
		_ = inter
		return fmt.Appendf(nil, "\x1b[%s%d;%d$y", p, n, v)
	case 't':
		switch params {
		case "14":
			return fmt.Appendf(nil, "\x1b[4;%d;%dt", rows*cellH, cols*cellW)
		case "16":
			return fmt.Appendf(nil, "\x1b[6;%d;%dt", cellH, cellW)
		case "18":
			return fmt.Appendf(nil, "\x1b[8;%d;%dt", rows, cols)
		}
	}
	return nil
}

func setReset(on bool) int {
	if on {
		return 1
	}
	return 2
}

// privateModeValue is DECRQM's answer for a private mode: 1 set, 2 reset,
// 0 not recognized.
func privateModeValue(n int, s *vt.Screen) int {
	m := s.Modes()
	wrap, origin, _ := s.Flags()
	switch n {
	case 1:
		return setReset(m.AppCursor)
	case 6:
		return setReset(origin)
	case 7:
		return setReset(wrap)
	case 25:
		return setReset(m.CursorVisible)
	case 47, 1047, 1049:
		return setReset(m.Alt)
	case 2004:
		return setReset(m.BracketedPaste)
	case 2026:
		return setReset(m.Sync)
	case 1004:
		return setReset(m.Focus)
	case 9, 1000, 1001, 1002, 1003:
		return setReset(m.Mouse == n)
	case 1005, 1006, 1015, 1016:
		return setReset(m.MouseFormat == n)
	}
	return 0
}

func rgb(hex string) string {
	return "rgb:" + hex[0:2] + hex[0:2] + "/" + hex[2:4] + hex[2:4] + "/" + hex[4:6] + hex[4:6]
}

// paletteColor is color n of the 256: the theme's 16, then xterm's cube and
// grays.
func paletteColor(n int) string {
	if n < 16 {
		return Palette.ANSI[n]
	}
	if n < 232 {
		n -= 16
		level := func(v int) int {
			if v == 0 {
				return 0
			}
			return 55 + v*40
		}
		return fmt.Sprintf("%02x%02x%02x", level(n/36), level(n/6%6), level(n%6))
	}
	g := 8 + (n-232)*10
	return fmt.Sprintf("%02x%02x%02x", g, g, g)
}
