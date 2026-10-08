package ptyhost

import (
	"bufio"
	"encoding/json"
	"net"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/internal/vt"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

// viewConn is a view attach and a terminal of the view's size fed by its
// frames.
type viewConn struct {
	r    *bufio.Reader
	conn net.Conn
	rep  wire.AttachReply
	term *vt.Screen
}

func attachView(t *testing.T, tm *Term, req wire.AttachRequest) *viewConn {
	t.Helper()
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	server, client := fileConn(t, fds[0]), fileConn(t, fds[1])
	go tm.Attach(server, bufio.NewReader(server), req)
	r := bufio.NewReader(client)
	line, err := wire.ReadLine(r)
	if err != nil {
		t.Fatal(err)
	}
	vc := &viewConn{r: r, conn: client}
	if err := json.Unmarshal(line, &vc.rep); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	if vc.rep.View != nil {
		vc.term = vt.New(vc.rep.View.Cols, vc.rep.View.Rows)
	}
	return vc
}

// next reads one frame into the view's terminal.
func (vc *viewConn) next(t *testing.T) (byte, []byte) {
	t.Helper()
	typ, p := readFrame(t, vc.r, vc.conn)
	if typ == wire.FrameData {
		vc.term.Write(p)
	}
	return typ, p
}

func (vc *viewConn) text() []string {
	_, h := vc.term.Size()
	out := make([]string, h)
	for y := range h {
		out[y] = strings.TrimRight(vc.term.Text(y), " ")
	}
	return out
}

func (vc *viewConn) until(t *testing.T, what string, f func([]string) bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !f(vc.text()) {
		if time.Now().After(deadline) {
			t.Fatalf("view never showed %s: %q", what, vc.text())
		}
		vc.next(t)
	}
}

// screenRows is the screen's rows y0..y1 (exclusive) clipped to cols.
func screenRows(tm *Term, y0, y1, cols int) []string {
	var out []string
	tm.WithScreen(func(s *vt.Screen) {
		for y := y0; y < y1; y++ {
			r := []rune(s.Text(y))
			if len(r) > cols {
				r = r[:cols]
			}
			out = append(out, strings.TrimRight(string(r), " "))
		}
	})
	return out
}

func TestViewShowsTheBottomRowsClipped(t *testing.T) {
	tm := start(t, "lines", 80, 24)
	waitFor(t, "prompt", func() bool { return screenHas(tm, "prompt>") })
	vc := attachView(t, tm, wire.AttachRequest{Attach: "x", Mode: wire.ModeRO, View: &wire.View{Rows: 5, Cols: 30}})
	if !vc.rep.OK || vc.rep.Cols != 80 || vc.rep.Rows != 24 || vc.rep.View.Rows != 5 || vc.rep.View.Cols != 30 {
		t.Fatalf("reply %+v", vc.rep)
	}
	vc.until(t, "the prompt", func(rows []string) bool { return rows[4] == "prompt>" })
	// The prompt is on the screen's last row: the window is rows 20-24.
	want := screenRows(tm, 19, 24, 30)
	if got := vc.text(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("view\n%q\nwant\n%q", got, want)
	}
	if got := vc.text()[3]; got != "line 30 green abcdefghijabcdef" {
		t.Fatalf("row not clipped to 30 columns: %q", got)
	}
	if a := vc.term.Row(3)[8].A; a.FG == 0 {
		t.Fatalf("colors lost: %+v", a)
	}
	if cx, cy := vc.term.Cursor(); cy != 4 || cx != 8 {
		t.Fatalf("cursor %d,%d", cx, cy)
	}
}

func TestViewFollowsTheCursorAboveTheBottom(t *testing.T) {
	// The echo helper draws on rows 1-3 of 24, the cursor on row 3.
	tm := start(t, "echo", 80, 24)
	waitFor(t, "prompt", func() bool { return screenHas(tm, "> ") })
	vc := attachView(t, tm, wire.AttachRequest{Attach: "x", Mode: wire.ModeRO, Cols: 40, Rows: 2, View: &wire.View{}})
	if vc.rep.View.Rows != 2 || vc.rep.View.Cols != 40 {
		t.Fatalf("view size should come from the request's cols/rows: %+v", vc.rep.View)
	}
	vc.until(t, "rows 2-3", func(rows []string) bool { return strings.HasPrefix(rows[0], "colored") && rows[1] == "  >" })
}

func TestViewSendsOnlyChangedRows(t *testing.T) {
	tm := start(t, "lines", 80, 24)
	waitFor(t, "prompt", func() bool { return screenHas(tm, "prompt>") })
	vc := attachView(t, tm, wire.AttachRequest{Attach: "x", Mode: wire.ModeRO, View: &wire.View{Rows: 6, Cols: 80}})
	vc.until(t, "the prompt", func(rows []string) bool { return rows[5] == "prompt>" })
	_, rw := attach(t, tm, wire.AttachRequest{Mode: wire.ModeRW})
	wire.WriteFrame(rw, wire.FrameData, []byte("hello"))
	vc.until(t, "the echo", func(rows []string) bool { return rows[5] == "prompt> hello" })
	wire.WriteFrame(rw, wire.FrameData, []byte("!"))
	typ, p := vc.next(t)
	if typ != wire.FrameData || strings.Contains(string(p), "line") || !strings.Contains(string(p), "\x1b[6;1H") {
		t.Fatalf("frame %d %q", typ, p)
	}
	if !strings.HasPrefix(string(p), "\x1b[?2026h") || !strings.HasSuffix(string(p), "\x1b[?2026l") {
		t.Fatalf("not one synchronized update: %q", p)
	}
	if got := vc.text()[5]; got != "prompt> hello!" {
		t.Fatalf("row %q", got)
	}
}

func TestViewResizeChangesTheWindow(t *testing.T) {
	tm := start(t, "lines", 80, 24)
	waitFor(t, "prompt", func() bool { return screenHas(tm, "prompt>") })
	vc := attachView(t, tm, wire.AttachRequest{Attach: "x", Mode: wire.ModeRO, View: &wire.View{Rows: 3, Cols: 80}})
	vc.until(t, "the prompt", func(rows []string) bool { return rows[2] == "prompt>" })
	wire.WriteFrame(vc.conn, wire.FrameResize, wire.SizePayload(20, 8))
	vc.term.Resize(20, 8)
	want := screenRows(tm, 16, 24, 20)
	vc.until(t, "8 rows of 20", func(rows []string) bool { return strings.Join(rows, "\n") == strings.Join(want, "\n") })
	if tm.Viewers() != 0 {
		t.Fatal("a view counted as an output viewer")
	}
	if c, r := tm.Size(); c != 80 || r != 24 {
		t.Fatalf("a view resized the PTY: %dx%d", c, r)
	}
}

func TestViewMustBeReadOnly(t *testing.T) {
	tm := start(t, "echo", 80, 24)
	vc := attachView(t, tm, wire.AttachRequest{Attach: "x", Mode: wire.ModeRW, View: &wire.View{Rows: 3}})
	if vc.rep.OK || vc.rep.Error == nil || vc.rep.Error.Code != wire.CodeInvalid {
		t.Fatalf("reply %+v", vc.rep)
	}
	vc = attachView(t, tm, wire.AttachRequest{Attach: "x", Mode: wire.ModeRO, View: &wire.View{Rows: 3, Anchor: "top"}})
	if vc.rep.OK {
		t.Fatal("unknown anchor accepted")
	}
}

func TestViewOfAnExitedTermSendsExit(t *testing.T) {
	code := 2
	tm := Exited(80, 24, &wire.Exit{Code: &code})
	vc := attachView(t, tm, wire.AttachRequest{Attach: "x", Mode: wire.ModeRO, View: &wire.View{Rows: 4, Cols: 80}})
	for {
		typ, p := vc.next(t)
		if typ == wire.FrameExit {
			var e wire.Exit
			json.Unmarshal(p, &e)
			if e.Code == nil || *e.Code != 2 {
				t.Fatalf("exit %s", p)
			}
			return
		}
	}
}

func TestViewEndsWithTheProgram(t *testing.T) {
	tm := start(t, "lines", 80, 24)
	waitFor(t, "prompt", func() bool { return screenHas(tm, "prompt>") })
	vc := attachView(t, tm, wire.AttachRequest{Attach: "x", Mode: wire.ModeRO, View: &wire.View{Rows: 3, Cols: 80}})
	vc.until(t, "the prompt", func(rows []string) bool { return rows[2] == "prompt>" })
	tm.Input([]byte{4})
	for {
		if typ, _ := vc.next(t); typ == wire.FrameExit {
			return
		}
	}
}

// BenchmarkViewWindow: the rows of a 40-row window onto a full, colored
// 120×40 screen (what one render costs before the diff).
func BenchmarkViewWindow(b *testing.B) {
	s := vt.New(120, 40)
	for y := range 40 {
		s.WriteString("\x1b[" + strconv.Itoa(y+1) + ";1H\x1b[38;5;" + strconv.Itoa(y) + "m" + strings.Repeat("x", 119))
	}
	b.ReportAllocs()
	for b.Loop() {
		Window(s, 120, 40)
	}
}
