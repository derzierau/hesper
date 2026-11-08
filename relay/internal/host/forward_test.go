package host

import (
	"context"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/derzierau/hesper/relay/pkg/agentlink"
	"github.com/derzierau/hesper/relay/pkg/devicekey"
	"github.com/derzierau/hesper/relay/pkg/protocol"
)

func TestParsePorts(t *testing.T) {
	p, err := ParsePorts("3000, 5173,8000-8999")
	if err != nil {
		t.Fatal(err)
	}
	for port, want := range map[int]bool{3000: true, 5173: true, 8000: true, 8999: true, 2999: false, 9000: false, 0: false, 70000: false} {
		if p.Allows(port) != want {
			t.Errorf("%d: %v", port, !want)
		}
	}
	if all, _ := ParsePorts("all"); !all.Allows(1) || !all.Allows(65535) || all.Allows(0) {
		t.Error("all")
	}
	if none, _ := ParsePorts(""); none.Any() || none.Allows(3000) {
		t.Error("empty forwards a port")
	}
	for _, bad := range []string{"x", "0", "65536", "9-1", "3000,", "-5", "1-2-3"} {
		if _, err := ParsePorts(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

// memStream is one end of an in-memory link stream.
type memStream struct{ in, out chan []byte }

func (s memStream) Write(ctx context.Context, p []byte) error {
	select {
	case s.out <- append([]byte(nil), p...):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s memStream) Read(ctx context.Context) (websocket.MessageType, []byte, error) {
	select {
	case p := <-s.in:
		return websocket.MessageBinary, p, nil
	case <-ctx.Done():
		return 0, nil, ctx.Err()
	}
}

func (s memStream) Close() error { return nil }

// ports.open bridges a connection to the host's loopback port on a
// channel of the caller's link, with every gate in front of it.
func TestPortsOpen(t *testing.T) {
	reg, _ := testRegistry(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	served := make(chan struct{})
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		io.Copy(c, c) // echo until the forward ends
		c.Close()
		close(served)
	}()
	port := ln.Addr().(*net.TCPAddr).Port
	allowed, _ := ParsePorts("1-65535")
	s := &Service{Agents: reg, AllowForward: allowed}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	a, b := make(chan []byte, 64), make(chan []byte, 64)
	lctx, lcancel := context.WithCancel(ctx)
	var reader sync.WaitGroup
	defer reader.Wait()
	defer lcancel()
	link := &hostLink{id: "link-1", device: "laptop-1", conn: agentlink.New(memStream{in: a, out: b}), ctx: lctx, cancel: lcancel,
		opened: time.Now(), chans: map[uint32]*hostChan{}}
	s.links.add(link)
	reader.Add(1)
	go func() {
		// serveLink's frame reader
		defer reader.Done()
		for {
			kind, ch, payload, err := link.conn.Read(lctx)
			if err != nil {
				return
			}
			switch kind {
			case agentlink.KindData:
				link.input(ch, payload)
			case agentlink.KindClose:
				link.closeChan(ch, false)
			case agentlink.KindWindow:
				link.window(ch, payload)
			}
		}
	}()
	shell := Caller{Device: "laptop-1", Rights: []string{devicekey.Observe, devicekey.Shell}, Verified: true}
	open := func(caller Caller, device, linkID string, port int) error {
		_, err := s.Execute(WithCaller(ctx, caller), protocol.Message{Method: "ports.open", ControllerID: device,
			Params: protocol.JSON(map[string]any{"link": linkID, "ch": 3, "port": port}), Deadline: time.Now().Add(10 * time.Second).UnixMilli()})
		return err
	}
	noShell := shell
	noShell.Rights = []string{devicekey.Observe, devicekey.Transfer, devicekey.Type}
	unverified := shell
	unverified.Verified = false
	for name, err := range map[string]error{
		"no shell right":  open(noShell, "laptop-1", "link-1", port),
		"unverified":      open(unverified, "laptop-1", "link-1", port),
		"another's link":  open(Caller{Device: "phone", Rights: shell.Rights, Verified: true}, "phone", "link-1", port),
		"no such link":    open(shell, "laptop-1", "link-2", port),
		"nothing listens": open(shell, "laptop-1", "link-1", freePort(t)),
		"invalid port":    open(shell, "laptop-1", "link-1", 0),
		"no --allow-forward": func() error {
			s.AllowForward = Ports{}
			defer func() { s.AllowForward = allowed }()
			return open(shell, "laptop-1", "link-1", port)
		}(),
		"port not allowed": func() error {
			s.AllowForward, _ = ParsePorts("1")
			defer func() { s.AllowForward = allowed }()
			return open(shell, "laptop-1", "link-1", port)
		}(),
	} {
		want := "forbidden"
		switch name {
		case "another's link", "no such link":
			want = "not_found"
		case "nothing listens":
			want = "unavailable"
		}
		if code(err) != want {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := open(shell, "laptop-1", "link-1", port); err != nil {
		t.Fatal(err)
	}
	if err := open(shell, "laptop-1", "link-1", port); code(err) != "invalid_request" {
		t.Fatalf("channel reused: %v", err)
	}
	// The controller's side of channel 3.
	ctl := agentlink.New(memStream{in: b, out: a})
	app, near := net.Pipe()
	pipe := agentlink.NewPipe(ctl, 3, near)
	go func() {
		for {
			kind, _, payload, err := ctl.Read(lctx)
			if err != nil {
				return
			}
			switch kind {
			case agentlink.KindData:
				pipe.Data(payload)
			case agentlink.KindWindow:
				pipe.Window(payload)
			case agentlink.KindClose:
				pipe.Close()
			}
		}
	}()
	ended := make(chan bool, 1)
	go func() {
		if pipe.Run(lctx) {
			ctl.Write(lctx, agentlink.KindClose, 3, nil)
		}
		ended <- true
	}()
	app.Write([]byte("ping over the link"))
	got := make([]byte, len("ping over the link"))
	if _, err := io.ReadFull(app, got); err != nil || string(got) != "ping over the link" {
		t.Fatalf("echo %q %v", got, err)
	}
	app.Close()
	<-ended
	select {
	case <-served:
	case <-ctx.Done():
		t.Fatal("the host kept the forwarded connection open")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		link.mu.Lock()
		n := len(link.chans)
		link.mu.Unlock()
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the channel stayed")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func freePort(t *testing.T) int {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// forwardLink is a host link for port forwards with its frame reader
// running (stopped and joined at the end); it returns the controller's
// end and a ports.open on it.
func forwardLink(t *testing.T, limits forwardLimits) (*hostLink, *agentlink.Conn, func(ctx context.Context, ch uint32) error) {
	t.Helper()
	reg, _ := testRegistry(t)
	all, _ := ParsePorts("all")
	s := &Service{Agents: reg, AllowForward: all, fwdLimits: limits}
	a, b := make(chan []byte, 64), make(chan []byte, 64)
	lctx, lcancel := context.WithCancel(context.Background())
	link := &hostLink{id: "link-1", device: "laptop-1", conn: agentlink.New(memStream{in: a, out: b}), ctx: lctx, cancel: lcancel,
		opened: time.Now(), chans: map[uint32]*hostChan{}}
	s.links.add(link)
	var reader sync.WaitGroup
	reader.Add(1)
	t.Cleanup(func() {
		lcancel()
		reader.Wait()
	})
	go func() {
		defer reader.Done()
		for {
			kind, ch, payload, err := link.conn.Read(lctx)
			if err != nil {
				return
			}
			switch kind {
			case agentlink.KindData:
				link.input(ch, payload)
			case agentlink.KindClose:
				link.closeChan(ch, false)
			case agentlink.KindWindow:
				link.window(ch, payload)
			}
		}
	}()
	shell := Caller{Device: "laptop-1", Rights: []string{devicekey.Observe, devicekey.Shell}, Verified: true}
	open := func(ctx context.Context, ch uint32) error {
		_, err := s.Execute(WithCaller(ctx, shell), protocol.Message{Method: "ports.open", ControllerID: "laptop-1",
			Params: protocol.JSON(map[string]any{"link": "link-1", "ch": ch, "port": 3000}), Deadline: time.Now().Add(10 * time.Second).UnixMilli()})
		return err
	}
	return link, agentlink.New(memStream{in: b, out: a}), open
}

func chans(l *hostLink) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.chans)
}

// pipes dials one end of a fresh net.Pipe each time; the other end (a
// service that never reads) stays open until the test ends.
func pipes(t *testing.T) func(context.Context, int) (net.Conn, error) {
	return func(context.Context, int) (net.Conn, error) {
		service, near := net.Pipe()
		t.Cleanup(func() { service.Close() })
		return near, nil
	}
}

// A ports.open whose request ended while the host dialed (the controller
// gave up and already sent X) leaves no channel and closes the connection.
func TestPortsOpenGivenUpWhileDialing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	service, near := net.Pipe()
	link, _, open := forwardLink(t, forwardLimits{dial: func(context.Context, int) (net.Conn, error) {
		cancel()
		return near, nil
	}})
	if err := open(ctx, 3); err == nil {
		t.Fatal("opened after the request ended")
	}
	if n := chans(link); n != 0 {
		t.Fatalf("%d channels left", n)
	}
	service.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := service.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("the service's connection stayed open: %v", err)
	}
}

// A connection the controller closed counts toward the link's forwards
// until what it sent was written, or the flush gave up.
func TestPortsOpenDrainingCounts(t *testing.T) {
	link, ctl, open := forwardLink(t, forwardLimits{max: 1, flush: 300 * time.Millisecond, dial: pipes(t)})
	ctx := context.Background()
	if err := open(ctx, 3); err != nil {
		t.Fatal(err)
	}
	ctl.Write(ctx, agentlink.KindData, 3, []byte("unread"))
	ctl.Write(ctx, agentlink.KindClose, 3, nil)
	for chans(link) != 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if err := open(ctx, 5); code(err) != "busy" {
		t.Fatalf("a draining connection did not count: %v", err)
	}
	// The flush gives up; the slot is free again.
	deadline := time.Now().Add(5 * time.Second)
	for open(ctx, 7) != nil {
		if time.Now().After(deadline) {
			t.Fatal("the draining connection never let go")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
