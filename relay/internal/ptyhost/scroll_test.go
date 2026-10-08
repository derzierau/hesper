package ptyhost

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/derzierau/hesper/relay/internal/vt"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

func scrollTo(t *testing.T, vc *viewConn, sc wire.Scroll) {
	t.Helper()
	p, _ := json.Marshal(sc)
	if err := wire.WriteFrame(vc.conn, wire.FrameScroll, p); err != nil {
		t.Fatal(err)
	}
}

// untilState reads frames until a SCROLL state that f accepts.
func (vc *viewConn) untilState(t *testing.T, f func(wire.ScrollState) bool) wire.ScrollState {
	t.Helper()
	for range 200 {
		typ, p := vc.next(t)
		if typ != wire.FrameScroll {
			continue
		}
		var st wire.ScrollState
		json.Unmarshal(p, &st)
		if f(st) {
			return st
		}
	}
	t.Fatal("no such scroll state")
	return wire.ScrollState{}
}

func off(n int) *int { return &n }

// The lines helper prints line 01..30 and a prompt on 24 rows: lines 01-07
// are in the scrollback, 08-30 and the prompt on the screen.
func TestViewScrollsIntoTheScrollback(t *testing.T) {
	tm := start(t, "lines", 80, 24)
	waitFor(t, "prompt", func() bool { return screenHas(tm, "prompt>") })
	tm.WithScreen(func(s *vt.Screen) {
		if s.HistoryLen() != 7 || !strings.HasPrefix(vt.ClipANSI(s.HistoryLine(0), 0), "line 01") {
			t.Fatalf("history %d: %q", s.HistoryLen(), s.HistoryLine(0))
		}
	})
	vc := attachView(t, tm, wire.AttachRequest{Attach: "x", Mode: wire.ModeRO, View: &wire.View{Rows: 5, Cols: 20}})
	vc.until(t, "the prompt", func(rows []string) bool { return rows[4] == "prompt>" })
	scrollTo(t, vc, wire.Scroll{Offset: off(10)})
	st := vc.untilState(t, func(s wire.ScrollState) bool { return s.Offset == 10 })
	if st.Max != 26 || st.New != 0 {
		t.Fatalf("state %+v", st)
	}
	vc.until(t, "lines 17-21", func(rows []string) bool {
		return strings.HasPrefix(rows[0], "line 17") && strings.HasPrefix(rows[4], "line 21")
	})
	// To the top: the oldest scrollback lines; a delta past it stops there.
	scrollTo(t, vc, wire.Scroll{Delta: 100})
	vc.untilState(t, func(s wire.ScrollState) bool { return s.Offset == 26 })
	vc.until(t, "lines 01-05", func(rows []string) bool {
		return strings.HasPrefix(rows[0], "line 01") && strings.HasPrefix(rows[4], "line 05")
	})
	if a := vc.term.Row(0)[8].A; a.FG == 0 {
		t.Fatalf("scrollback lost its colors: %+v", a)
	}
	// Back to live.
	scrollTo(t, vc, wire.Scroll{Offset: off(0)})
	vc.untilState(t, func(s wire.ScrollState) bool { return s.Offset == 0 && s.New == 0 })
	vc.until(t, "the prompt again", func(rows []string) bool { return rows[4] == "prompt>" })
}

// While scrolled, output does not move the window; the viewer learns how
// many new lines wait below.
func TestViewScrollStaysPutUnderNewOutput(t *testing.T) {
	tm := start(t, "lines", 80, 24)
	waitFor(t, "prompt", func() bool { return screenHas(tm, "prompt>") })
	vc := attachView(t, tm, wire.AttachRequest{Attach: "x", Mode: wire.ModeRO, View: &wire.View{Rows: 5, Cols: 20}})
	vc.until(t, "the prompt", func(rows []string) bool { return rows[4] == "prompt>" })
	scrollTo(t, vc, wire.Scroll{Delta: 5})
	vc.until(t, "lines 22-26", func(rows []string) bool {
		return strings.HasPrefix(rows[0], "line 22") && strings.HasPrefix(rows[4], "line 26")
	})
	_, rw := attach(t, tm, wire.AttachRequest{Mode: wire.ModeRW})
	wire.WriteFrame(rw, wire.FrameData, []byte("a\r\nb\r\nc\r\n"))
	st := vc.untilState(t, func(s wire.ScrollState) bool { return s.New == 3 })
	if st.Offset != 8 {
		t.Fatalf("state %+v", st)
	}
	if rows := vc.text(); !strings.HasPrefix(rows[0], "line 22") || !strings.HasPrefix(rows[4], "line 26") {
		t.Fatalf("the window moved: %q", rows)
	}
	scrollTo(t, vc, wire.Scroll{Offset: off(0)})
	vc.untilState(t, func(s wire.ScrollState) bool { return s.Offset == 0 && s.New == 0 })
	vc.until(t, "live", func(rows []string) bool { return strings.HasPrefix(rows[3], "c") })
}

func TestAltScreenHasNoScrollback(t *testing.T) {
	s := vt.New(10, 3)
	s.WriteString("1\r\n2\r\n3\r\n4\r\n5")
	if _, _, m := WindowAt(s, 10, 3, 0); m != 2 {
		t.Fatalf("max %d", m)
	}
	s.WriteString("\x1b[?1049hfull screen")
	lines, _, m := WindowAt(s, 10, 3, 2)
	if m != 0 || !strings.Contains(strings.Join(lines, ""), "full") {
		t.Fatalf("alt: max %d %q", m, lines)
	}
}

// A read-write attach gets the scrollback before the redraw, so its own
// terminal scrolls back: fed into a terminal, the lines end up in its
// history and the screen is the program's.
func TestReadWriteAttachGetsTheScrollback(t *testing.T) {
	tm := start(t, "lines", 80, 24)
	waitFor(t, "prompt", func() bool { return screenHas(tm, "prompt>") })
	r, c := attach(t, tm, wire.AttachRequest{Mode: wire.ModeRW})
	typ, p := readFrame(t, r, c)
	if typ != wire.FrameData {
		t.Fatalf("frame %d", typ)
	}
	term := vt.New(80, 24)
	term.Write(p)
	if term.HistoryLen() != 7 || !strings.HasPrefix(vt.ClipANSI(term.HistoryLine(0), 0), "line 01") ||
		!strings.HasPrefix(vt.ClipANSI(term.HistoryLine(6), 0), "line 07") {
		t.Fatalf("history %d first %q", term.HistoryLen(), term.HistoryLine(0))
	}
	if got := strings.TrimRight(term.Text(23), " "); got != "prompt>" {
		t.Fatalf("screen bottom %q", got)
	}
	// Nor does a read-write resync (history: false).
	no := false
	r3, c3 := attach(t, tm, wire.AttachRequest{Mode: wire.ModeRW, History: &no})
	if _, p3 := readFrame(t, r3, c3); strings.Contains(string(p3), "line 01") {
		t.Fatal("history: false got the scrollback")
	}
	// A read-only (non-view) attach does not get it.
	r2, c2 := attach(t, tm, wire.AttachRequest{Mode: wire.ModeRO})
	_, p2 := readFrame(t, r2, c2)
	if strings.Contains(string(p2), "line 01") {
		t.Fatal("ro attach got the scrollback")
	}
}
