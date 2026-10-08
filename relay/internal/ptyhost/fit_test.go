package ptyhost

import (
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// fitView attaches a view that declares a fit and drains its frames.
func fitView(t *testing.T, tm *Term, cols, rows int) net.Conn {
	t.Helper()
	vc := attachView(t, tm, wire.AttachRequest{Mode: wire.ModeRO, Cols: cols, Rows: rows,
		View: &wire.View{Rows: rows, Cols: cols}, Fit: &wire.Size{Cols: cols, Rows: rows}})
	if !vc.rep.OK {
		t.Fatalf("view refused: %+v", vc.rep.Error)
	}
	go io.Copy(io.Discard, vc.r)
	return vc.conn
}

func sizeIs(tm *Term, cols, rows int) func() bool {
	return func() bool { c, r := tm.Size(); return c == cols && r == rows }
}

func TestFitFollowsLargestView(t *testing.T) {
	tm := start(t, "echo", 120, 40)
	tm.mu.Lock()
	tm.cfg.FitDebounce = 50 * time.Millisecond
	tm.mu.Unlock()
	resized := make(chan [2]int, 16)
	tm.cfg.OnResize = func(c, r int) { resized <- [2]int{c, r} }
	waitFor(t, "prompt", func() bool { return screenHas(tm, "> ") })

	// A tall tile: the PTY grows to it.
	a := fitView(t, tm, 120, 70)
	waitFor(t, "fit 120x70", sizeIs(tm, 120, 70))
	// A second, wider but shorter one: the largest of each side.
	b := fitView(t, tm, 150, 30)
	waitFor(t, "fit 150x70", sizeIs(tm, 150, 70))
	// The tall one goes: the other's fit, never below 80x24.
	a.Close()
	waitFor(t, "fit 150x30", sizeIs(tm, 150, 30))
	wire.WriteFrame(b, wire.FrameResize, wire.SizePayload(40, 10))
	waitFor(t, "minimum 80x24", sizeIs(tm, 80, 24))
	// A change of less than FitStep on both sides is no resize.
	wire.WriteFrame(b, wire.FrameResize, wire.SizePayload(81, 25))
	time.Sleep(4 * tm.cfg.FitDebounce)
	if c, r := tm.Size(); c != 80 || r != 24 {
		t.Fatalf("a 1 cell change resized to %dx%d", c, r)
	}
	// Plain views without a fit never size the PTY.
	vc := attachView(t, tm, wire.AttachRequest{Mode: wire.ModeRO, Cols: 200, Rows: 90, View: &wire.View{Rows: 90}})
	go io.Copy(io.Discard, vc.r)
	time.Sleep(4 * tm.cfg.FitDebounce)
	if c, r := tm.Size(); c != 80 || r != 24 {
		t.Fatalf("a view without fit resized to %dx%d", c, r)
	}
	if len(resized) == 0 {
		t.Fatal("OnResize was not told")
	}
}

func TestFitDebounced(t *testing.T) {
	tm := start(t, "echo", 100, 30)
	tm.mu.Lock()
	tm.cfg.FitDebounce = 150 * time.Millisecond
	tm.mu.Unlock()
	var resizes atomic.Int32
	tm.cfg.OnResize = func(c, r int) { resizes.Add(1) }
	waitFor(t, "prompt", func() bool { return screenHas(tm, "> ") })
	v := fitView(t, tm, 100, 40)
	// A drag: many sizes in quick succession end in one resize.
	for rows := 41; rows <= 60; rows++ {
		wire.WriteFrame(v, wire.FrameResize, wire.SizePayload(100, rows))
		time.Sleep(10 * time.Millisecond)
	}
	waitFor(t, "fit 100x60", sizeIs(tm, 100, 60))
	time.Sleep(2 * tm.cfg.FitDebounce)
	if n := resizes.Load(); n != 1 {
		t.Fatalf("%d resizes for one drag, want 1", n)
	}
}

func TestOwnerWinsOverFit(t *testing.T) {
	tm := start(t, "echo", 120, 40)
	tm.mu.Lock()
	tm.cfg.FitDebounce = 50 * time.Millisecond
	tm.mu.Unlock()
	waitFor(t, "prompt", func() bool { return screenHas(tm, "> ") })
	fitView(t, tm, 130, 60)
	waitFor(t, "fit", sizeIs(tm, 130, 60))
	// The owner attaches: its size, and fits change nothing meanwhile.
	_, owner := attach(t, tm, wire.AttachRequest{Mode: wire.ModeRW, Owner: true, Cols: 200, Rows: 50})
	if c, r := tm.Size(); c != 200 || r != 50 {
		t.Fatalf("owner size %dx%d", c, r)
	}
	fitView(t, tm, 140, 80)
	time.Sleep(4 * tm.cfg.FitDebounce)
	if c, r := tm.Size(); c != 200 || r != 50 {
		t.Fatalf("a fit overrode the owner: %dx%d", c, r)
	}
	// The owner leaves: back to the largest fit.
	owner.Close()
	waitFor(t, "fit after owner", sizeIs(tm, 140, 80))
}
