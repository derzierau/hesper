package ptyhost

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/term"

	"github.com/derzierau/hesper/relay/internal/vt"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

// The test binary doubles as the program in the PTY (PTYHOST_HELPER).
func TestMain(m *testing.M) {
	if mode := os.Getenv("PTYHOST_HELPER"); mode != "" {
		helper(mode)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func helper(mode string) {
	switch mode {
	case "echo":
		// A TUI: alternate screen, colors, a cursor; echoes raw input.
		old, _ := term.MakeRaw(0)
		defer term.Restore(0, old)
		os.Stdout.WriteString("\x1b[?1049h\x1b[H\x1b[2J\x1b[1;34m┌ fake agent ┐\x1b[0m\r\n\x1b[38;2;200;100;50mcolored\x1b[0m 日本\r\n\x1b[?2004h\x1b[3;3H> ")
		buf := make([]byte, 4096)
		for {
			n, err := os.Stdin.Read(buf)
			if err != nil || n == 0 {
				return
			}
			if bytes.Contains(buf[:n], []byte{4}) {
				return
			}
			os.Stdout.Write(buf[:n])
		}
	case "lines":
		// Scrolls 30 numbered lines through the screen, then a prompt that
		// echoes input.
		old, _ := term.MakeRaw(0)
		defer term.Restore(0, old)
		for i := 1; i <= 30; i++ {
			fmt.Printf("line %02d \x1b[32mgreen\x1b[0m %s\r\n", i, strings.Repeat("abcdefghij", 6))
		}
		os.Stdout.WriteString("prompt> ")
		buf := make([]byte, 4096)
		for {
			n, err := os.Stdin.Read(buf)
			if err != nil || n == 0 || bytes.Contains(buf[:n], []byte{4}) {
				return
			}
			os.Stdout.Write(buf[:n])
		}
	case "flood":
		line := strings.Repeat("x", 99) + "\n"
		for range 60000 {
			os.Stdout.WriteString(line)
		}
		os.Exit(3)
	case "hup":
		signal.Ignore(syscall.SIGHUP)
		os.Stdout.WriteString("ignoring hup\r\n")
		time.Sleep(time.Minute)
	case "query":
		old, _ := term.MakeRaw(0)
		os.Stdout.WriteString("\x1b[5;7H\x1b[6n")
		r := bufio.NewReader(os.Stdin)
		reply, _ := r.ReadString('R')
		term.Restore(0, old)
		fmt.Printf("\r\ngot %q\r\n", reply)
		time.Sleep(200 * time.Millisecond)
	}
}

func start(t *testing.T, mode string, cols, rows int) *Term {
	t.Helper()
	exe, _ := os.Executable()
	tm, err := Start(Config{Argv: []string{exe}, Env: append(os.Environ(), "PTYHOST_HELPER="+mode, "TERM=xterm-256color"), Cols: cols, Rows: rows})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		tm.Signal(syscall.SIGKILL)
		<-tm.Done()
	})
	return tm
}

// attach connects a viewer through a socket pair.
func attach(t *testing.T, tm *Term, req wire.AttachRequest) (*bufio.Reader, net.Conn) {
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
	var rep wire.AttachReply
	if err := json.Unmarshal(line, &rep); err != nil || !rep.OK {
		t.Fatalf("reply %s %v", line, err)
	}
	t.Cleanup(func() { client.Close() })
	return r, client
}

func fileConn(t *testing.T, fd int) net.Conn {
	f := os.NewFile(uintptr(fd), "sock")
	c, err := net.FileConn(f)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func waitFor(t *testing.T, what string, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !f() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func screenHas(tm *Term, text string) bool {
	found := false
	tm.WithScreen(func(s *vt.Screen) {
		_, h := s.Size()
		for y := range h {
			if strings.Contains(s.Text(y), text) {
				found = true
			}
		}
	})
	return found
}

func readFrame(t *testing.T, r *bufio.Reader, conn net.Conn) (byte, []byte) {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	typ, p, err := wire.ReadFrame(r, nil)
	if err != nil {
		t.Fatal(err)
	}
	return typ, p
}

func TestAttachRedrawEqualsScreen(t *testing.T) {
	tm := start(t, "echo", 80, 24)
	waitFor(t, "prompt", func() bool { return screenHas(tm, "colored") })
	r, conn := attach(t, tm, wire.AttachRequest{Attach: "x", Mode: wire.ModeRO, Cols: 80, Rows: 24})
	typ, p := readFrame(t, r, conn)
	if typ != wire.FrameData {
		t.Fatalf("first frame %d", typ)
	}
	view := vt.New(80, 24)
	view.Write(p)
	if got, want := string(view.Redraw(nil)), string(tm.Redraw()); got != want {
		t.Fatalf("viewer screen differs:\n%q\n%q", got, want)
	}
	if !view.Modes().Alt || !view.Modes().BracketedPaste {
		t.Fatalf("modes %+v", view.Modes())
	}
}

func TestReadOnlyRefusesInput(t *testing.T) {
	tm := start(t, "echo", 80, 24)
	waitFor(t, "prompt", func() bool { return screenHas(tm, "> ") })
	_, ro := attach(t, tm, wire.AttachRequest{Mode: wire.ModeRO})
	wire.WriteFrame(ro, wire.FrameData, []byte("from-ro"))
	_, rw := attach(t, tm, wire.AttachRequest{Mode: wire.ModeRW})
	wire.WriteFrame(rw, wire.FrameData, []byte("from-rw"))
	waitFor(t, "rw input", func() bool { return screenHas(tm, "from-rw") })
	if screenHas(tm, "from-ro") {
		t.Fatal("read-only input reached the program")
	}
}

func TestSizeOwnerRule(t *testing.T) {
	tm := start(t, "echo", 80, 24)
	waitFor(t, "prompt", func() bool { return screenHas(tm, "> ") })
	tileR, tile := attach(t, tm, wire.AttachRequest{Mode: wire.ModeRO, Cols: 40, Rows: 10})
	readFrame(t, tileR, tile) // redraw
	if c, r := tm.Size(); c != 80 || r != 24 {
		t.Fatalf("a read-only viewer resized the PTY to %dx%d", c, r)
	}
	// A rw viewer that is not the owner cannot resize.
	_, other := attach(t, tm, wire.AttachRequest{Mode: wire.ModeRW, Cols: 50, Rows: 20})
	wire.WriteFrame(other, wire.FrameResize, wire.SizePayload(50, 20))
	// The owner sets the size at attach; viewers get SIZE.
	_, owner := attach(t, tm, wire.AttachRequest{Mode: wire.ModeRW, Owner: true, Cols: 100, Rows: 30})
	if c, r := tm.Size(); c != 100 || r != 30 {
		t.Fatalf("owner size %dx%d", c, r)
	}
	for {
		typ, p := readFrame(t, tileR, tile)
		if typ == wire.FrameSize {
			if c, r, _ := wire.ParseSize(p); c != 100 || r != 30 {
				t.Fatalf("SIZE %dx%d", c, r)
			}
			break
		}
	}
	wire.WriteFrame(owner, wire.FrameResize, wire.SizePayload(120, 40))
	waitFor(t, "owner resize", func() bool { c, r := tm.Size(); return c == 120 && r == 40 })
	wire.WriteFrame(other, wire.FrameResize, wire.SizePayload(50, 20))
	time.Sleep(50 * time.Millisecond)
	if c, r := tm.Size(); c != 120 || r != 40 {
		t.Fatalf("a non-owner resized to %dx%d", c, r)
	}
	// The owner leaves: the size stays, and nobody else owns it.
	owner.Close()
	waitFor(t, "detach", func() bool { return tm.Viewers() == 2 })
	wire.WriteFrame(other, wire.FrameResize, wire.SizePayload(50, 20))
	time.Sleep(50 * time.Millisecond)
	if c, r := tm.Size(); c != 120 || r != 40 {
		t.Fatalf("size after the owner left %dx%d", c, r)
	}
}

func TestSlowViewerResyncs(t *testing.T) {
	old := QueueLimit
	QueueLimit = 256 << 10
	t.Cleanup(func() { QueueLimit = old })
	exe, _ := os.Executable()
	tm, err := Start(Config{Argv: []string{exe}, Env: append(os.Environ(), "PTYHOST_HELPER=flood"), Cols: 100, Rows: 30})
	if err != nil {
		t.Fatal(err)
	}
	// A viewer that reads nothing while 6 MB pass.
	r, conn := attach(t, tm, wire.AttachRequest{Mode: wire.ModeRO})
	select {
	case <-tm.Done():
	case <-time.After(20 * time.Second):
		t.Fatal("a slow viewer held the program up")
	}
	if code := tm.Exit().Code; code == nil || *code != 3 {
		t.Fatalf("exit %+v", tm.Exit())
	}
	// It still ends on the final screen, then EXIT.
	view := vt.New(100, 30)
	sawSize := false
	for {
		typ, p := readFrame(t, r, conn)
		switch typ {
		case wire.FrameSize:
			sawSize = true
			view = vt.New(100, 30)
		case wire.FrameData:
			view.Write(p)
		case wire.FrameExit:
			var e wire.Exit
			json.Unmarshal(p, &e)
			if e.Code == nil || *e.Code != 3 {
				t.Fatalf("EXIT %s", p)
			}
			if !sawSize {
				t.Fatal("no resync")
			}
			if got, want := string(view.Redraw(nil)), string(tm.Redraw()); got != want {
				t.Fatal("after the resync the viewer's screen differs")
			}
			return
		}
	}
}

func TestQueriesAnsweredByDaemon(t *testing.T) {
	tm := start(t, "query", 80, 24)
	r, conn := attach(t, tm, wire.AttachRequest{Mode: wire.ModeRO})
	waitFor(t, "reply", func() bool { return screenHas(tm, "got") })
	if !screenHas(tm, `got "\x1b[5;7R"`) {
		tm.WithScreen(func(s *vt.Screen) { t.Fatalf("screen %q", s.Text(5)) })
	}
	// The viewer never saw the query.
	var all []byte
	for {
		typ, p := readFrame(t, r, conn)
		if typ == wire.FrameExit {
			break
		}
		all = append(all, p...)
	}
	if bytes.Contains(all, []byte("\x1b[6n")) {
		t.Fatal("the query reached the viewer")
	}
}

func TestStopHangsUpThenKills(t *testing.T) {
	tm := start(t, "hup", 80, 24)
	waitFor(t, "start", func() bool { return screenHas(tm, "ignoring") })
	tm.Stop(200 * time.Millisecond)
	select {
	case <-tm.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("not killed")
	}
	if e := tm.Exit(); e.Signal != "SIGKILL" {
		t.Fatalf("exit %+v", e)
	}
}

func TestExitedTermSendsExit(t *testing.T) {
	code := 0
	tm := Exited(80, 24, &wire.Exit{Code: &code})
	r, conn := attach(t, tm, wire.AttachRequest{Mode: wire.ModeRW, Owner: true, Cols: 10, Rows: 5})
	if typ, _ := readFrame(t, r, conn); typ != wire.FrameData {
		t.Fatal("no redraw")
	}
	if typ, _ := readFrame(t, r, conn); typ != wire.FrameExit {
		t.Fatal("no EXIT")
	}
}

// Keystroke cost in the daemon: an attach DATA frame to the program and
// its echo back to the viewer (the helper echoes in raw mode).
func BenchmarkKeystrokeRoundTrip(b *testing.B) {
	exe, _ := os.Executable()
	tm, err := Start(Config{Argv: []string{exe}, Env: append(os.Environ(), "PTYHOST_HELPER=echo"), Cols: 80, Rows: 24})
	if err != nil {
		b.Fatal(err)
	}
	defer func() { tm.Signal(syscall.SIGKILL); <-tm.Done() }()
	fds, _ := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	sf, cf := os.NewFile(uintptr(fds[0]), "s"), os.NewFile(uintptr(fds[1]), "c")
	server, _ := net.FileConn(sf)
	client, _ := net.FileConn(cf)
	sf.Close()
	cf.Close()
	go tm.Attach(server, bufio.NewReader(server), wire.AttachRequest{Mode: wire.ModeRW})
	r := bufio.NewReader(client)
	wire.ReadLine(r)
	wire.ReadFrame(r, nil) // redraw
	for !screenHasT(tm, "> ") {
		time.Sleep(time.Millisecond)
	}
	b.ResetTimer()
	for b.Loop() {
		wire.WriteFrame(client, wire.FrameData, []byte("k"))
		for {
			typ, p, err := wire.ReadFrame(r, nil)
			if err != nil {
				b.Fatal(err)
			}
			if typ == wire.FrameData && bytes.Contains(p, []byte("k")) {
				break
			}
		}
	}
}

func screenHasT(tm *Term, s string) bool { return screenHas(tm, s) }

// Fan-out cost per output chunk with 16 viewers attached.
func BenchmarkFanOut16(b *testing.B) {
	tm := &Term{screen: vt.New(120, 40), cols: 120, rows: 40, viewers: map[*viewer]struct{}{}}
	for range 16 {
		v := &viewer{t: tm, wake: make(chan struct{}, 1)}
		tm.viewers[v] = struct{}{}
	}
	chunk := item{typ: wire.FrameData, data: []byte(strings.Repeat("\x1b[32mok\x1b[0m line\r\n", 20))}
	b.ResetTimer()
	for b.Loop() {
		tm.mu.Lock()
		tm.screen.Write(chunk.data)
		for v := range tm.viewers {
			v.push(chunk)
			v.mu.Lock()
			v.queue, v.bytes = v.queue[:0], 0
			v.mu.Unlock()
		}
		tm.mu.Unlock()
	}
}
