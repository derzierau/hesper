package agentlink

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// memStream is one end of an in-memory stream pair.
type memStream struct {
	in, out chan []byte
	closed  chan struct{}
}

func memPair() (*memStream, *memStream) {
	a, b := make(chan []byte, 64), make(chan []byte, 64)
	done := make(chan struct{})
	return &memStream{in: a, out: b, closed: done}, &memStream{in: b, out: a, closed: done}
}

func (s *memStream) Write(ctx context.Context, p []byte) error {
	select {
	case s.out <- append([]byte(nil), p...):
		return nil
	case <-s.closed:
		return io.ErrClosedPipe
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *memStream) Read(ctx context.Context) (websocket.MessageType, []byte, error) {
	select {
	case p := <-s.in:
		return websocket.MessageBinary, p, nil
	case <-s.closed:
		return 0, nil, io.EOF
	case <-ctx.Done():
		return 0, nil, ctx.Err()
	}
}

func (s *memStream) Close() error { return nil }

// side is one end of a link with one byte-stream channel; its reader
// checks the peer kept within its window.
func side(t *testing.T, ctx context.Context, s Stream, nc net.Conn) (*Conn, *Pipe, chan bool) {
	c := New(s)
	p := NewPipe(c, 5, nc)
	violated := make(chan bool, 1)
	go func() {
		for {
			kind, ch, payload, err := c.Read(ctx)
			if err != nil {
				return
			}
			if ch != 5 {
				t.Errorf("frame on channel %d", ch)
			}
			ok := true
			switch kind {
			case KindData:
				if len(payload) > Chunk {
					t.Errorf("a %d-byte data frame", len(payload))
				}
				ok = p.Data(payload)
			case KindWindow:
				ok = p.Window(payload)
			case KindClose:
				p.Close()
			}
			if !ok {
				violated <- true
				return
			}
		}
	}()
	return c, p, violated
}

// 8 MB each way through a slow reader: every byte arrives in order, and
// neither side ever sends more than it was granted.
func TestPipeKeepsWithinTheWindow(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	sa, sb := memPair()
	appA, nearA := net.Pipe()
	appB, nearB := net.Pipe()
	_, pa, va := side(t, ctx, sa, nearA)
	_, pb, vb := side(t, ctx, sb, nearB)
	go pa.Run(ctx)
	go pb.Run(ctx)
	data := make([]byte, 8<<20)
	rand.Read(data)
	// B's application echoes, slowly.
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := appB.Read(buf)
			if n > 0 {
				time.Sleep(10 * time.Microsecond)
				appB.Write(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()
	go appA.Write(data)
	got := make([]byte, len(data))
	if _, err := io.ReadFull(appA, got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("the echo differs")
	}
	select {
	case <-va:
		t.Fatal("B sent past its window")
	case <-vb:
		t.Fatal("A sent past its window")
	default:
	}
}

// The peer's close delivers what it sent first; nc's close tells the
// caller to send X.
func TestPipeEnds(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	app, near := net.Pipe()
	p := NewPipe(New(&chunky{}), 1, near)
	p.Data([]byte("last words"))
	p.Close()
	ran := make(chan bool, 1)
	go func() { ran <- p.Run(ctx) }()
	got, _ := io.ReadAll(app)
	if string(got) != "last words" || <-ran {
		t.Fatalf("got %q", got)
	}
	app, near = net.Pipe()
	p = NewPipe(New(&chunky{}), 1, near)
	go func() { ran <- p.Run(ctx) }()
	app.Close()
	if !<-ran {
		t.Fatal("nc's end was not reported")
	}
}

func TestPipeRefusesPastTheWindow(t *testing.T) {
	_, near := net.Pipe()
	p := NewPipe(New(&chunky{}), 1, near)
	if !p.Data(make([]byte, Window)) || p.Data([]byte{1}) {
		t.Fatal("data past the window accepted")
	}
	grant := func(n uint32) []byte { return binary.BigEndian.AppendUint32(nil, n) }
	if p.Window(grant(1)) || p.Window([]byte{0, 1}) {
		t.Fatal("a grant past the window accepted")
	}
}
