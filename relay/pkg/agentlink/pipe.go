package agentlink

import (
	"context"
	"encoding/binary"
	"net"
	"sync"
)

// Byte-stream channels (port forwards) have flow control: each side may
// send Window bytes, and the receiver grants more (W) once it wrote them
// on. So the link's reader never blocks on a slow connection (the relay
// ends a link whose writes stall), and one bulk transfer never queues
// more than Window per direction ahead of the terminals on the link.
const (
	// Window is what a side may send before the other grants more.
	Window = 256 << 10
	// Chunk bounds a byte-stream channel's data frame.
	Chunk = 32 << 10
	// grantStep: a receiver grants in steps of at least this.
	grantStep = 64 << 10
)

// Pipe bridges one byte-stream channel of a link to a connection: what
// nc sends goes out as D frames while the peer has room; D frames from
// the peer (Data, called by the link's reader, never blocking) are
// written to nc and then granted back. The peer's X (Close) ends it
// once what the peer sent was written.
type Pipe struct {
	c  *Conn
	ch uint32
	nc net.Conn

	mu      sync.Mutex
	credit  int    // what this side may still send
	room    int    // what the peer may still send
	in      []byte // from the peer, not yet written to nc
	spare   []byte
	peerX   bool // the peer closed: write in, then end
	ended   bool
	local   bool          // ended on nc's side: the caller sends X
	wakeIn  chan struct{} // in or peerX changed
	wakeOut chan struct{} // credit grew
	done    chan struct{}
}

// NewPipe bridges channel ch of c to nc; Run runs it.
func NewPipe(c *Conn, ch uint32, nc net.Conn) *Pipe {
	return &Pipe{c: c, ch: ch, nc: nc, credit: Window, room: Window,
		wakeIn: make(chan struct{}, 1), wakeOut: make(chan struct{}, 1), done: make(chan struct{})}
}

func wake(c chan struct{}) {
	select {
	case c <- struct{}{}:
	default:
	}
}

// Data takes a D frame's payload. false: the peer sent more than it was
// granted (the caller closes the channel).
func (p *Pipe) Data(b []byte) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ended || p.peerX {
		return true
	}
	if len(b) > p.room {
		return false
	}
	p.room -= len(b)
	p.in = append(p.in, b...)
	wake(p.wakeIn)
	return true
}

// Window takes a W frame's payload. false: malformed, or more than this
// side ever sent.
func (p *Pipe) Window(b []byte) bool {
	if len(b) != 4 {
		return false
	}
	n := binary.BigEndian.Uint32(b)
	p.mu.Lock()
	defer p.mu.Unlock()
	if uint64(p.credit)+uint64(n) > Window {
		return false
	}
	p.credit += int(n)
	wake(p.wakeOut)
	return true
}

// Close takes the peer's X: what it sent is written, then the pipe ends.
func (p *Pipe) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.peerX = true
	wake(p.wakeIn)
}

// Run bridges until the channel ends: nc closes or fails, the peer
// closes, a link write fails or ctx (the link's) ends. It closes nc and
// reports whether the end came from nc's side; the caller then sends X.
func (p *Pipe) Run(ctx context.Context) bool {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		p.fromConn(ctx)
	}()
	go func() {
		defer wg.Done()
		p.toConn(ctx)
	}()
	select {
	case <-ctx.Done():
		p.end(false)
	case <-p.done:
	}
	wg.Wait()
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.local
}

func (p *Pipe) end(local bool) {
	p.mu.Lock()
	if p.ended {
		p.mu.Unlock()
		return
	}
	p.ended, p.local = true, local
	p.mu.Unlock()
	close(p.done)
	p.nc.Close()
}

// fromConn sends what nc reads, within the peer's grants.
func (p *Pipe) fromConn(ctx context.Context) {
	buf := make([]byte, Chunk)
	for {
		p.mu.Lock()
		n := min(p.credit, Chunk)
		p.mu.Unlock()
		if n == 0 {
			select {
			case <-p.wakeOut:
				continue
			case <-p.done:
				return
			}
		}
		got, err := p.nc.Read(buf[:n])
		if got > 0 {
			p.mu.Lock()
			p.credit -= got
			p.mu.Unlock()
			if p.c.Write(ctx, KindData, p.ch, buf[:got]) != nil {
				p.end(false)
				return
			}
		}
		if err != nil {
			p.end(true)
			return
		}
	}
}

// toConn writes the peer's bytes to nc and grants them back.
func (p *Pipe) toConn(ctx context.Context) {
	owed := 0
	var grant [4]byte
	for {
		p.mu.Lock()
		data, peerX := p.in, p.peerX
		p.in, p.spare = p.spare[:0], nil
		p.mu.Unlock()
		if len(data) > 0 {
			if _, err := p.nc.Write(data); err != nil {
				p.end(true)
				return
			}
			owed += len(data)
			p.mu.Lock()
			if p.spare == nil {
				p.spare = data[:0]
			}
			if owed >= grantStep {
				p.room += owed
			}
			p.mu.Unlock()
			if owed >= grantStep {
				binary.BigEndian.PutUint32(grant[:], uint32(owed))
				owed = 0
				if p.c.Write(ctx, KindWindow, p.ch, grant[:]) != nil {
					p.end(false)
					return
				}
			}
			continue
		}
		if peerX {
			p.end(false)
			return
		}
		select {
		case <-p.wakeIn:
		case <-p.done:
			return
		}
	}
}
