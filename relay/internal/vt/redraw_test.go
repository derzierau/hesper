package vt

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// state is everything Redraw promises to carry.
func state(s *Screen) string {
	var b strings.Builder
	fmt.Fprintf(&b, "size %dx%d alt %v cursor %d,%d pending %v pen %+v\n", s.w, s.h, s.altOn, s.x, s.y, s.pending, s.pen)
	fmt.Fprintf(&b, "region %d-%d wrap %v origin %v insert %v g %v shift %d\n", s.top, s.bot, s.wrap, s.origin, s.insert, s.g, s.shift)
	fmt.Fprintf(&b, "modes %+v keys %v\n", s.modes, s.keys)
	fmt.Fprintf(&b, "save %d,%d %+v g %v shift %d\n", s.save.x, s.save.y, s.save.pen, s.save.g, s.save.shift)
	if s.altOn {
		fmt.Fprintf(&b, "asave %d,%d %+v\n", s.aSav.x, s.aSav.y, s.aSav.pen)
	}
	fmt.Fprintf(&b, "tabs %v\n", s.tabs)
	grids := [][][]Cell{s.main}
	if s.altOn {
		grids = append(grids, s.alt)
	}
	for i, g := range grids {
		for y, row := range g {
			fmt.Fprintf(&b, "grid %d row %d %+v\n", i, y, row)
		}
	}
	return b.String()
}

var redrawCases = []struct{ name, data string }{
	{"plain", "hello\r\nworld"},
	{"colors", "\x1b[1;31mred\x1b[0m \x1b[38;2;10;20;30;48;5;200mrgb\x1b[4:3;58;2;1;2;3mcurl\x1b[0m\x1b[44m   \x1b[K"},
	{"wide", "日本語 ok\r\n\x1b[1;79H字"},
	{"pending-wrap", "\x1b[1;75Habcdef"},
	{"pending-wide", "\x1b[3;79H字"},
	{"alt", "main text\x1b[2;3H\x1b[33m\x1b7\x1b[0m\x1b[?1049h\x1b[?1h\x1b=\x1b[?2004h\x1b[5;5H\x1b[7malt\x1b[0m\x1b[?25l"},
	{"alt-saved", "\x1b[?1049h\x1b[3;3H\x1b[32m\x1b7\x1b[0m\x1b[10;10Hx"},
	{"region-origin", "\x1b[3;10r\x1b[?6h\x1b[2;4Hin region\x1b[4h"},
	{"modes", "\x1b[?1002h\x1b[?1006h\x1b[?1004h\x1b[>1u\x1b[>5u\x1b[=7;1u\x1b[5 q\x1b[?2026h"},
	{"charsets", "\x1b(0lqk\x1b(B\x1b)0\x0eqq\x0f\x0e"},
	{"tabs", "\x1b[3g\x1b[1;5H\x1bH\x1b[1;30H\x1bH\x1b[H\tA\tB"},
	{"pen", "\x1b[1;3;35;45mtext"},
	{"scrolled", strings.Repeat("line\r\n", 40) + "end"},
}

func TestRedrawRecreatesScreen(t *testing.T) {
	for _, c := range redrawCases {
		t.Run(c.name, func(t *testing.T) {
			s := New(80, 24)
			s.WriteString(c.data)
			out := s.Redraw(nil)
			// Into a fresh terminal, and into one left in another state.
			for _, start := range []string{"", "\x1b[?1049h\x1b[?1000h\x1b[>3u\x1b[5;20r\x1b[?6h\x1b[4h\x1b[31mjunk\x1b(0\x0e\x1b[3g\x1b[?25l"} {
				v := New(80, 24)
				v.WriteString(start)
				v.Write(out)
				if got, want := state(v), state(s); got != want {
					t.Fatalf("start %q:\n%s", start, diffLines(got, want))
				}
			}
		})
	}
}

func diffLines(got, want string) string {
	g, w := strings.Split(got, "\n"), strings.Split(want, "\n")
	for i := range max(len(g), len(w)) {
		var a, b string
		if i < len(g) {
			a = g[i]
		}
		if i < len(w) {
			b = w[i]
		}
		if a != b {
			return fmt.Sprintf("got  %.400s\nwant %.400s", a, b)
		}
	}
	return ""
}

func TestResizeKeepsCursorRow(t *testing.T) {
	s := New(20, 10)
	s.WriteString("\x1b[10;1Hbottom\x1b[1;1Htop\x1b[10;7H")
	s.Resize(30, 5)
	if w, h := s.Size(); w != 30 || h != 5 {
		t.Fatalf("size %dx%d", w, h)
	}
	if x, y := s.Cursor(); x != 6 || y != 4 {
		t.Fatalf("cursor %d,%d", x, y)
	}
	if got := strings.TrimRight(s.Text(4), " "); got != "bottom" {
		t.Fatalf("row 4 %q", got)
	}
	s.Resize(10, 8)
	if got := strings.TrimRight(s.Text(4), " "); got != "bottom" {
		t.Fatalf("row 4 after grow %q", got)
	}
	if !reflect.DeepEqual(s.tabs, []bool{false, false, false, false, false, false, false, false, true, false}) {
		t.Fatalf("tabs %v", s.tabs)
	}
	// Still a working screen: the last column wraps and scrolls.
	s.WriteString("\x1b[8;10Hxy")
	if s.Text(6) != "         x" || s.Text(7) != "y         " {
		t.Fatalf("rows %q %q", s.Text(6), s.Text(7))
	}
}

// A full 200×60 screen of colored text: the attach redraw budget is 5 ms.
func BenchmarkRedraw200x60(b *testing.B) {
	s := New(200, 60)
	var in strings.Builder
	for y := range 60 {
		fmt.Fprintf(&in, "\x1b[%d;1H", y+1)
		for x := range 200 {
			fmt.Fprintf(&in, "\x1b[38;5;%dm%c", (x+y)%256, 'a'+rune(x%26))
		}
	}
	s.WriteString(in.String())
	buf := make([]byte, 0, 1<<20)
	b.ResetTimer()
	for b.Loop() {
		buf = s.Redraw(buf[:0])
	}
	b.ReportMetric(float64(len(buf)), "bytes")
}
