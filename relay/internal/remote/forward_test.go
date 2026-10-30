package remote

import (
	"context"
	"errors"
	"net"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/derzierau/hesper/relay/pkg/agentlink"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

func testFleet(t *testing.T) *Fleet {
	return New(Options{StateDir: t.TempDir(), Logf: t.Logf})
}

// A port taken on ::1 is taken: a browser's localhost may go there first
// (dev servers that listen on ::1 only).
func TestForwardPortTakenOnIPv6(t *testing.T) {
	ln6, err := net.Listen("tcp", "[::1]:0")
	if err != nil {
		t.Skip("no IPv6 loopback")
	}
	defer ln6.Close()
	port := ln6.Addr().(*net.TCPAddr).Port
	f := testFleet(t)
	fw := &forward{Forward: wire.Forward{Machine: "M", Port: 3000, Local: port}, conns: map[*fwdConn]struct{}{}}
	var we *wire.Error
	if err := f.listenForward(fw); !errors.As(err, &we) || we.Code != wire.CodeExists {
		t.Fatalf("listened on a port taken on ::1: %v", err)
	}
	ln4, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatalf("127.0.0.1 kept: %v", err)
	}
	ln4.Close()
}

// fakeListener fails Accept with errs, one per call, then reports closed.
type fakeListener struct {
	errs  []error
	calls int
}

func (l *fakeListener) Accept() (net.Conn, error) {
	l.calls++
	if len(l.errs) > 0 {
		err := l.errs[0]
		l.errs = l.errs[1:]
		return nil, err
	}
	return nil, net.ErrClosed
}
func (l *fakeListener) Close() error   { return nil }
func (l *fakeListener) Addr() net.Addr { return &net.TCPAddr{} }

// Accept errors: temporary ones (out of file descriptors) are retried,
// others end the listener and show in the forward's error.
func TestForwardAcceptErrors(t *testing.T) {
	f := testFleet(t)
	fw := &forward{Forward: wire.Forward{Machine: "M", Port: 3000, Local: 3000}, conns: map[*fwdConn]struct{}{}}
	ln := &fakeListener{errs: []error{&net.OpError{Op: "accept", Err: syscall.EMFILE}, &net.OpError{Op: "accept", Err: syscall.EMFILE}}}
	done := make(chan struct{})
	go func() { f.acceptForward(fw, ln); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("still accepting")
	}
	if ln.calls != 3 {
		t.Fatalf("%d accepts", ln.calls)
	}
	ln = &fakeListener{errs: []error{&net.OpError{Op: "accept", Err: syscall.EINVAL}, &net.OpError{Op: "accept", Err: syscall.EMFILE}}}
	f.acceptForward(fw, ln)
	if ln.calls != 1 || fw.Error == "" {
		t.Fatalf("%d accepts, error %q", ln.calls, fw.Error)
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

// When the host closes a forwarded connection, what it sent is written
// to the local client for at most the flush time, even if the client
// never reads.
func TestForwardDrainIsBounded(t *testing.T) {
	f := testFleet(t)
	f.fwd.flush = 200 * time.Millisecond
	a, b := make(chan []byte, 64), make(chan []byte, 64)
	ctx, cancel := context.WithCancel(context.Background())
	l := &link{f: f, m: &machine{short: "M", agents: map[string]wire.Agent{}}, id: "link-1", conn: agentlink.New(memStream{in: a, out: b}),
		ctx: ctx, cancel: cancel, bridges: map[uint32]*bridge{}, forwards: map[uint32]*fwdConn{}}
	read := make(chan struct{})
	go func() { l.read(); close(read) }()
	defer func() { cancel(); <-read }()
	app, near := net.Pipe() // a local client that never reads
	defer app.Close()
	c, ok := l.addForward(near)
	if !ok {
		t.Fatal("no channel")
	}
	ran := make(chan struct{})
	go func() { c.pipe.Run(c.ctx); close(ran) }()
	host := agentlink.New(memStream{in: b, out: a})
	host.Write(ctx, agentlink.KindData, c.ch, []byte("never read"))
	host.Write(ctx, agentlink.KindClose, c.ch, nil)
	select {
	case <-ran:
	case <-time.After(5 * time.Second):
		t.Fatal("the drain never gave up")
	}
}
