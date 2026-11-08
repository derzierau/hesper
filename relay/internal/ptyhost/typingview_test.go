package ptyhost

import (
	"strings"
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// A read-write view (the iPhone's text view) types into the agent and
// sees view rows: no scrollback preamble, no raw stream.
func TestTypingViewInput(t *testing.T) {
	tm := start(t, "lines", 80, 24)
	waitFor(t, "prompt", func() bool { return screenHas(tm, "prompt>") })
	vc := attachView(t, tm, wire.AttachRequest{Attach: "x", Mode: wire.ModeRW, View: &wire.View{Rows: 3, Cols: 80}})
	if !vc.rep.OK || vc.rep.View == nil {
		t.Fatalf("reply %+v", vc.rep)
	}
	typ, p := vc.next(t)
	if typ != wire.FrameData || !strings.HasPrefix(string(p), "\x1b[?2026h\x1b[0m\x1b[?1049l\x1b[r\x1b[?6l") {
		t.Fatalf("first frame is not a view render: %d %q", typ, p)
	}
	if strings.Contains(string(p), "\x1b[0m\x1b[?1049l\x1b[r\x1b[?7l\x1b[H\x1b[2J") || strings.Contains(string(p), "line 01") {
		t.Fatalf("a typing view got the history preamble: %q", p)
	}
	wire.WriteFrame(vc.conn, wire.FrameData, []byte("hello"))
	waitFor(t, "the typed input", func() bool { return screenHas(tm, "prompt> hello") })
	vc.until(t, "the echo", func(rows []string) bool { return rows[2] == "prompt> hello" })
}

// A typing view never sizes the PTY: no owner, no fit.
func TestTypingViewNoSizing(t *testing.T) {
	tm := start(t, "lines", 80, 24)
	tm.mu.Lock()
	tm.cfg.FitDebounce = 30 * time.Millisecond
	tm.mu.Unlock()
	waitFor(t, "prompt", func() bool { return screenHas(tm, "prompt>") })
	vc := attachView(t, tm, wire.AttachRequest{Attach: "x", Mode: wire.ModeRW, Owner: true, Cols: 50, Rows: 20,
		View: &wire.View{Rows: 20, Cols: 50}, Fit: &wire.Size{Cols: 50, Rows: 20}})
	if !vc.rep.OK || vc.rep.Cols != 80 || vc.rep.Rows != 24 {
		t.Fatalf("reply %+v", vc.rep)
	}
	wire.WriteFrame(vc.conn, wire.FrameResize, wire.SizePayload(40, 10))
	time.Sleep(400 * time.Millisecond)
	if c, r := tm.Size(); c != 80 || r != 24 {
		t.Fatalf("a typing view resized the PTY to %dx%d", c, r)
	}
	tm.mu.Lock()
	owner := tm.owner
	tm.mu.Unlock()
	if owner != nil {
		t.Fatal("a typing view became the size owner")
	}
}

// A read-only view still drops input.
func TestReadOnlyViewIgnoresInput(t *testing.T) {
	tm := start(t, "lines", 80, 24)
	waitFor(t, "prompt", func() bool { return screenHas(tm, "prompt>") })
	vc := attachView(t, tm, wire.AttachRequest{Attach: "x", Mode: wire.ModeRO, View: &wire.View{Rows: 3, Cols: 80}})
	vc.until(t, "the prompt", func(rows []string) bool { return rows[2] == "prompt>" })
	wire.WriteFrame(vc.conn, wire.FrameData, []byte("from-ro"))
	// The view reads its frames in order: once the resize shows, the
	// input before it was handled.
	wire.WriteFrame(vc.conn, wire.FrameResize, wire.SizePayload(80, 4))
	vc.term.Resize(80, 4)
	vc.until(t, "4 rows", func(rows []string) bool { return rows[3] == "prompt>" })
	_, rw := attach(t, tm, wire.AttachRequest{Mode: wire.ModeRW})
	wire.WriteFrame(rw, wire.FrameData, []byte("ok"))
	waitFor(t, "the rw input", func() bool { return screenHas(tm, "prompt> ok") })
	if screenHas(tm, "from-ro") {
		t.Fatal("a read-only view typed into the agent")
	}
}
