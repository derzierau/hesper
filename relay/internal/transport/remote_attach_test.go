package transport_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/internal/remote"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

// spawnShell starts a fake shell on M through L (Touch ID: the strong
// software key) and returns it.
func spawnShell(t *testing.T, w *world) wire.Agent {
	t.Helper()
	w.L.waitLinked(t, "M")
	var a wire.Agent
	if err := w.L.call(t, "agents.spawn", wire.SpawnParams{Machine: "M", Kind: "shell", Project: w.M.project}, &a); err != nil {
		t.Fatal(err)
	}
	w.L.waitAgent(t, a.ID, inState(wire.StateIdle))
	// Its prompt: the fake shell reads raw keys from then on.
	look := attach(t, w.L, wire.AttachRequest{Attach: a.ID, Mode: wire.ModeRO})
	readUntil(t, look, "$")
	look.Close()
	return a
}

// Attaching through L to an agent on M is the local attach protocol: the
// reply with the PTY's size, a redraw, live output; rw types, ro does not;
// the owner sizes the PTY, nobody else; EXIT ends it.
func TestRemoteAttachReadWriteOwnerAndExit(t *testing.T) {
	w := newWorld(t, worldOptions{shell: true})
	a := spawnShell(t, w)
	ro := attach(t, w.L, wire.AttachRequest{Attach: a.ID, Mode: wire.ModeRO, Cols: 50, Rows: 10})
	if ro.Reply.Cols != 120 || ro.Reply.Rows != 40 {
		t.Fatalf("ro reply %+v", ro.Reply)
	}
	readUntil(t, ro, "$")
	rw := attach(t, w.L, wire.AttachRequest{Attach: a.ID, Mode: wire.ModeRW, Cols: 100, Rows: 30, Owner: true})
	if rw.Reply.Cols != 100 || rw.Reply.Rows != 30 {
		t.Fatalf("owner reply %+v", rw.Reply)
	}
	readUntil(t, rw, "$")
	rw.Input([]byte("TYPED-SECRET-91\r"))
	// The read-only viewer is told the new size, then sees the typing.
	if types := readUntil(t, ro, "ran TYPED-SECRET-91"); !strings.ContainsRune(string(types), rune(wire.FrameSize)) {
		t.Fatalf("no SIZE for the read-only viewer: frames %v", types)
	}
	readUntil(t, rw, "ran TYPED-SECRET-91")
	ro.Input([]byte("NOT-TYPED\r"))
	ro.Resize(20, 5)
	rw.Resize(90, 25)
	w.L.waitAgent(t, a.ID, func(x wire.Agent) bool { return x.Size == wire.Size{Cols: 90, Rows: 25} })
	time.Sleep(200 * time.Millisecond)
	if got := w.M.waitAgent(t, localOn(a.ID, "M"), func(wire.Agent) bool { return true }); got.Size != (wire.Size{Cols: 90, Rows: 25}) {
		t.Fatalf("size %+v", got.Size)
	}
	rw.Input([]byte("exit\r"))
	for {
		typ, p, err := rw.ReadFrame()
		if err != nil {
			t.Fatalf("no EXIT: %v", err)
		}
		if typ == wire.FrameExit {
			if !strings.Contains(string(p), `"code":0`) {
				t.Fatalf("exit %s", p)
			}
			break
		}
	}
	if leaked := w.tap.sawAny("TYPED-SECRET-91", "ran TYPED"); leaked != "" {
		t.Fatalf("the relay saw %q", leaked)
	}
	screen := readAll(t, ro)
	if strings.Contains(screen, "NOT-TYPED") {
		t.Fatal("a read-only viewer typed")
	}
}

func localOn(id, machine string) string {
	_, l, _ := strings.Cut(id, "/")
	return machine + "/" + l
}

// readAll reads a viewer to its end (EXIT or close).
func readAll(t *testing.T, a *wire.AttachConn) string {
	t.Helper()
	var b strings.Builder
	timer := time.AfterFunc(10*time.Second, func() { a.Close() })
	defer timer.Stop()
	for {
		typ, p, err := a.ReadFrame()
		if err != nil || typ == wire.FrameExit {
			return b.String()
		}
		if typ == wire.FrameData {
			b.Write(p)
		}
	}
}

// A viewer that does not read loses what it missed and gets SIZE and a
// fresh redraw instead; the agent and the other viewers never wait for it.
func TestRemoteSlowViewerIsResynced(t *testing.T) {
	saved := remote.QueueLimit
	remote.QueueLimit = 8 << 10
	t.Cleanup(func() { remote.QueueLimit = saved })
	w := newWorld(t, worldOptions{shell: true})
	a := spawnShell(t, w)
	slow := attach(t, w.L, wire.AttachRequest{Attach: a.ID, Mode: wire.ModeRO})
	// The typing goes in on M itself: a bridged rw viewer that the test
	// does not read while it types would be resynced too, and what is
	// typed during its reattach is dropped (LAST-LINE with it).
	rw := attach(t, w.M, wire.AttachRequest{Attach: localOn(a.ID, "M"), Mode: wire.ModeRW})
	readUntil(t, rw, "$")
	// Lots of output while the slow viewer reads nothing: ~1 MB, past
	// what the kernel buffers on the slow viewer's socket (Linux takes
	// ~200 KB before a write blocks, macOS far less) plus the bridge's
	// queue, so the queue overflows on every OS.
	line := []byte(strings.Repeat("x", 2000) + "\r")
	for range 250 {
		rw.Input(line)
	}
	rw.Input([]byte("LAST-LINE\r"))
	readUntil(t, rw, "ran LAST-LINE")
	types := readUntil(t, slow, "ran LAST-LINE")
	if !strings.ContainsRune(string(types), rune(wire.FrameSize)) {
		t.Fatalf("the slow viewer was not resynced (frames %v)", types)
	}
}

// A view of an agent on M scrolls into M's scrollback through L: the
// SCROLL frame goes through the bridge unchanged, and so does the answer.
func TestRemoteViewScrollsThroughTheBridge(t *testing.T) {
	w := newWorld(t, worldOptions{shell: true})
	a := spawnShell(t, w)
	rw := attach(t, w.L, wire.AttachRequest{Attach: a.ID, Mode: wire.ModeRW})
	readUntil(t, rw, "$")
	for i := range 60 {
		rw.Input([]byte(fmt.Sprintf("SCROLL-%02d\r", i)))
	}
	readUntil(t, rw, "ran SCROLL-59")
	view := attach(t, w.L, wire.AttachRequest{Attach: a.ID, Mode: wire.ModeRO, Cols: 60, Rows: 5, View: &wire.View{}})
	readUntil(t, view, "SCROLL-59")
	off := 40
	if err := view.Scroll(wire.Scroll{Offset: &off}); err != nil {
		t.Fatal(err)
	}
	var screen strings.Builder
	var state wire.ScrollState
	timer := time.AfterFunc(10*time.Second, func() { view.Close() })
	defer timer.Stop()
	for state.Offset != 40 || !strings.Contains(screen.String(), "SCROLL-3") {
		typ, p, err := view.ReadFrame()
		if err != nil {
			t.Fatalf("no scroll state through the bridge: %v (state %+v)", err, state)
		}
		switch typ {
		case wire.FrameScroll:
			json.Unmarshal(p, &state)
		case wire.FrameData:
			screen.Write(p)
		}
	}
	if state.Max < 40 {
		t.Fatalf("state %+v", state)
	}
}
