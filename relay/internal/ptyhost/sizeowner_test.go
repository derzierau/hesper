package ptyhost

import (
	"strings"
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/internal/vt"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

// The garbled panes in windows that are not key (Hesper.app): the owner's
// window sizes the PTY; a second pane of the same agent with another grid
// attached read-write without owner and rendered the raw stream in its
// own grid (libghostty sizes its grid from the pane's frame), so rows
// wrapped and cursor-addressed redraws landed in the wrong cells. A view
// of that grid shows the PTY's rows clipped, never reflowed, and never
// sizes the PTY while the owner holds it.
func TestPaneOfAnotherGridNeedsAView(t *testing.T) {
	tm := start(t, "lines", 80, 24)
	tm.mu.Lock()
	tm.cfg.FitDebounce = 30 * time.Millisecond
	tm.mu.Unlock()
	waitFor(t, "prompt", func() bool { return screenHas(tm, "prompt>") })

	// The key window's pane: the one owner, its grid is the PTY's.
	_, owner := attach(t, tm, wire.AttachRequest{Mode: wire.ModeRW, Owner: true, Cols: 80, Rows: 24})
	if c, r := tm.Size(); c != 80 || r != 24 {
		t.Fatalf("owner size %dx%d", c, r)
	}

	// The bug: another window's pane, 50 columns, read-write without
	// owner, raw stream into a 50-column terminal.
	rawR, raw := attach(t, tm, wire.AttachRequest{Mode: wire.ModeRW, Cols: 50, Rows: 24})
	pane := vt.New(50, 24)
	for !strings.Contains(strings.Join(vtText(pane), "\n"), "prompt>") {
		typ, p := readFrame(t, rawR, raw)
		if typ == wire.FrameData {
			pane.Write(p)
		}
	}
	want := screenRows(tm, 0, 24, 50)
	if strings.Join(vtText(pane), "\n") == strings.Join(want, "\n") {
		t.Fatalf("expected the raw stream to garble a 50-column pane of an 80-column PTY")
	}

	// The fix: that pane is a view with a fit (`hesperd attach --fit`).
	vc := attachView(t, tm, wire.AttachRequest{Mode: wire.ModeRO, Cols: 50, Rows: 24,
		View: &wire.View{Rows: 24, Cols: 50}, Fit: &wire.Size{Cols: 50, Rows: 24}})
	vc.until(t, "the prompt", func(rows []string) bool { return rows[23] == "prompt>" })
	if got := vc.text(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("view\n%q\nwant the PTY's rows clipped\n%q", got, want)
	}
	// Typing in the owner's pane: the view follows, still clipped.
	wire.WriteFrame(owner, wire.FrameData, []byte(strings.Repeat("typed ", 12)))
	waitFor(t, "echo", func() bool { return screenHas(tm, "typed typed") })
	vc.until(t, "the echo", func(rows []string) bool {
		return strings.Join(rows, "\n") == strings.Join(screenRows(tm, 0, 24, 50), "\n")
	})
	// The view's fit never overrides the owner: one size.
	time.Sleep(5 * tm.cfg.FitDebounce)
	if c, r := tm.Size(); c != 80 || r != 24 {
		t.Fatalf("a view resized the owner's PTY to %dx%d", c, r)
	}
}

func vtText(s *vt.Screen) []string {
	_, h := s.Size()
	out := make([]string, h)
	for y := range h {
		out[y] = strings.TrimRight(s.Text(y), " ")
	}
	return out
}
