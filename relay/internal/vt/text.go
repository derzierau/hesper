package vt

import "strings"

// Plain text of the screen (agents.screen): what a person reads, without
// escape sequences or attributes.

// RowText is row y's characters without attributes, trailing blanks
// dropped.
func (s *Screen) RowText(y int) string {
	return strings.TrimRight(s.Text(y), " ")
}

// HistoryText is scrollback line i (0: the oldest) without its SGR
// sequences.
func (s *Screen) HistoryText(i int) string {
	return strings.TrimRight(StripANSI(s.HistoryLine(i)), " ")
}

// PlainLines are the screen's last rows rows (all with rows <= 0), after
// the last scrollback lines of the scrollback (none with scrollback <= 0),
// oldest first, as plain text. The scrollback is the main screen's: with
// the alternate screen on it is what the program showed before.
func (s *Screen) PlainLines(rows, scrollback int) []string {
	h := s.h
	if rows <= 0 || rows > h {
		rows = h
	}
	n := s.hist.n
	if scrollback < n {
		n = max(scrollback, 0)
	}
	out := make([]string, 0, n+rows)
	for i := s.hist.n - n; i < s.hist.n; i++ {
		out = append(out, s.HistoryText(i))
	}
	for y := h - rows; y < h; y++ {
		out = append(out, s.RowText(y))
	}
	return out
}

// StripANSI removes escape sequences from terminal output: CSI (ESC [ …
// final byte), OSC and other string sequences (to BEL or ESC \), and
// two- and three-byte escapes; other bytes stay.
func StripANSI(text string) string {
	if !strings.ContainsRune(text, 0x1b) {
		return text
	}
	var b strings.Builder
	b.Grow(len(text))
	for i := 0; i < len(text); i++ {
		c := text[i]
		if c != 0x1b {
			b.WriteByte(c)
			continue
		}
		if i+1 >= len(text) {
			break
		}
		switch text[i+1] {
		case '[':
			j := i + 2
			for j < len(text) && (text[j] < 0x40 || text[j] > 0x7e) {
				j++
			}
			i = j
		case ']', 'P', '_', '^', 'X':
			j := i + 2
			for j < len(text) {
				if text[j] == 0x07 {
					break
				}
				if text[j] == 0x1b && j+1 < len(text) && text[j+1] == '\\' {
					j++
					break
				}
				j++
			}
			i = j
		case '(', ')', '*', '+', '#', '%', ' ':
			i += 2 // a designation: one more byte
		default:
			i++
		}
	}
	return b.String()
}
