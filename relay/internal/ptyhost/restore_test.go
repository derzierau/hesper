package ptyhost

import (
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// The Mac stayed at the phone's 37 columns after the phone closed the
// agent: a remote owner that lets go gives the PTY back the size it had
// before the first remote take-over.
func TestRemoteOwnerDetachRestoresSize(t *testing.T) {
	tm := start(t, "lines", 120, 40)
	resized := make(chan [2]int, 16)
	tm.mu.Lock()
	tm.cfg.OnResize = func(c, r int) { resized <- [2]int{c, r} }
	tm.mu.Unlock()
	waitFor(t, "prompt", func() bool { return screenHas(tm, "prompt>") })
	phone := Origin{Device: "phone-1", Name: "iPhone"}

	owner := attachFrom(t, tm, wire.AttachRequest{Mode: wire.ModeRW, Owner: true, Cols: 37, Rows: 27}, phone)
	waitFor(t, "the phone's size", sizeIs(tm, 37, 27))
	if got := <-resized; got != [2]int{37, 27} {
		t.Fatalf("OnResize %v", got)
	}
	owner.Close()
	waitFor(t, "the size before", sizeIs(tm, 120, 40))
	select {
	case got := <-resized:
		if got != [2]int{120, 40} {
			t.Fatalf("OnResize %v, want [120 40]", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("OnResize was not told")
	}
}

// A local owner after the remote one sized the PTY itself: nothing to
// restore when both let go.
func TestLocalOwnerAfterRemoteClearsRestore(t *testing.T) {
	tm := start(t, "lines", 120, 40)
	waitFor(t, "prompt", func() bool { return screenHas(tm, "prompt>") })
	phone := Origin{Device: "phone-1", Name: "iPhone"}

	remote := attachFrom(t, tm, wire.AttachRequest{Mode: wire.ModeRW, Owner: true, Cols: 37, Rows: 27}, phone)
	waitFor(t, "the phone's size", sizeIs(tm, 37, 27))
	_, local := attach(t, tm, wire.AttachRequest{Mode: wire.ModeRW, Owner: true, Cols: 100, Rows: 30})
	waitFor(t, "the Mac's size", sizeIs(tm, 100, 30))
	remote.Close()
	local.Close()
	waitFor(t, "both detached", func() bool { return tm.Viewers() == 0 })
	time.Sleep(50 * time.Millisecond)
	if c, r := tm.Size(); c != 100 || r != 30 {
		t.Fatalf("size %dx%d, want 100x30", c, r)
	}
}

// A view that asks for a fit wins over the restore once the remote owner
// lets go, as the fit always did.
func TestRemoteOwnerDetachWithFitLetsFitWin(t *testing.T) {
	tm := start(t, "lines", 120, 40)
	tm.mu.Lock()
	tm.cfg.FitDebounce = 30 * time.Millisecond
	tm.mu.Unlock()
	waitFor(t, "prompt", func() bool { return screenHas(tm, "prompt>") })
	phone := Origin{Device: "phone-1", Name: "iPhone"}

	owner := attachFrom(t, tm, wire.AttachRequest{Mode: wire.ModeRW, Owner: true, Cols: 37, Rows: 27}, phone)
	waitFor(t, "the phone's size", sizeIs(tm, 37, 27))
	fitView(t, tm, 90, 30)
	time.Sleep(4 * tm.cfg.FitDebounce)
	if c, r := tm.Size(); c != 37 || r != 27 {
		t.Fatalf("the fit overrode the owner: %dx%d", c, r)
	}
	owner.Close()
	waitFor(t, "the fit", sizeIs(tm, 90, 30))
	time.Sleep(4 * tm.cfg.FitDebounce)
	if c, r := tm.Size(); c != 90 || r != 30 {
		t.Fatalf("size %dx%d, want the fit 90x30", c, r)
	}
}

// A fit view can disappear before its debounce runs. The Mac must still
// recover the size it had before the phone took over.
func TestRemoteOwnerDetachThenFitClosesRestoresSize(t *testing.T) {
	tm := start(t, "lines", 120, 40)
	tm.mu.Lock()
	tm.cfg.FitDebounce = 300 * time.Millisecond
	tm.mu.Unlock()
	waitFor(t, "prompt", func() bool { return screenHas(tm, "prompt>") })
	phone := Origin{Device: "phone-1", Name: "iPhone"}

	owner := attachFrom(t, tm, wire.AttachRequest{Mode: wire.ModeRW, Owner: true, Cols: 37, Rows: 27}, phone)
	waitFor(t, "the phone's size", sizeIs(tm, 37, 27))
	view := fitView(t, tm, 90, 30)
	owner.Close()
	waitFor(t, "the phone released", func() bool { _, owned := tm.SizeOwner(); return !owned })
	view.Close()
	waitFor(t, "the fit view closed", func() bool {
		tm.mu.Lock()
		defer tm.mu.Unlock()
		return len(tm.views) == 0
	})
	waitFor(t, "the Mac's original size", sizeIs(tm, 120, 40))
}
