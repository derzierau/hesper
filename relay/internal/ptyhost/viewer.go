package ptyhost

import (
	"bufio"
	"encoding/json"
	"net"
	"sync"
	"time"

	"github.com/derzierau/hesper/relay/internal/perf"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

// QueueLimit bounds the output queued for one viewer; past it the viewer
// is resynced with a fresh redraw.
var QueueLimit = 4 << 20

// WriteTimeout drops a viewer that takes no data for this long.
var WriteTimeout = 30 * time.Second

type item struct {
	typ  byte
	data []byte
}

type viewer struct {
	t     *Term
	conn  net.Conn
	rw    bool
	mu    sync.Mutex
	queue []item
	bytes int
	// resync: the queue was dropped; the writer sends a redraw next.
	resync bool
	closed bool
	wake   chan struct{}
	// Resyncs counts the redraws a slow read caused (tests).
	resyncs int
}

// push queues an item; the Term's lock is held.
func (v *viewer) push(it item) {
	v.mu.Lock()
	switch {
	case v.closed:
	case v.resync && it.typ != wire.FrameExit:
		// The redraw to come shows it.
	case v.bytes+len(it.data) > QueueLimit:
		v.queue, v.bytes, v.resync = nil, 0, true
		v.resyncs++
	default:
		v.queue = append(v.queue, it)
		v.bytes += len(it.data)
	}
	v.mu.Unlock()
	select {
	case v.wake <- struct{}{}:
	default:
	}
}

// Attach serves an attach connection whose request line was read (r holds
// what followed it). It answers the request, then runs until either side
// ends; it closes conn.
func (t *Term) Attach(conn net.Conn, r *bufio.Reader, req wire.AttachRequest) {
	defer conn.Close()
	rw := req.Mode == wire.ModeRW
	if req.Mode != wire.ModeRW && req.Mode != wire.ModeRO {
		reply(conn, wire.AttachReply{Error: wire.Errorf(wire.CodeInvalid, "mode must be rw or ro")})
		return
	}
	if req.View != nil {
		if rw {
			reply(conn, wire.AttachReply{Error: wire.Errorf(wire.CodeInvalid, "a view attach must be read-only")})
			return
		}
		t.attachView(conn, r, req)
		return
	}
	v := &viewer{t: t, conn: conn, rw: rw, wake: make(chan struct{}, 1)}
	start := time.Now()
	t.mu.Lock()
	resized := false
	if rw && req.Owner {
		t.owner = v
		if t.exit == nil && req.Cols > 0 && req.Rows > 0 {
			resized, _ = t.resizeLocked(req.Cols, req.Rows)
		}
	}
	line, _ := json.Marshal(wire.AttachReply{OK: true, Cols: t.cols, Rows: t.rows})
	var first []byte
	if rw && (req.History == nil || *req.History) {
		// A read-write attach (focus, the active tile) gets the
		// scrollback first, so its own terminal scrolls back like any
		// terminal (scroll.go).
		first = HistoryPreamble(t.screen, append(first, "\x1b[?2026h"...))
	}
	v.queue = []item{{typ: replyItem, data: append(line, '\n')}, {typ: wire.FrameData, data: t.screen.Redraw(first)}}
	if t.exit != nil {
		v.queue = append(v.queue, item{typ: wire.FrameExit, data: wire.ExitPayload(t.exit)})
	} else {
		t.viewers[v] = struct{}{}
	}
	cols, rows := t.cols, t.rows
	t.mu.Unlock()
	perf.Host.Since(StageRedraw, start)
	if resized && t.cfg.OnResize != nil {
		t.cfg.OnResize(cols, rows)
	}
	select {
	case v.wake <- struct{}{}:
	default:
	}
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		v.write()
	}()
	v.read(r)
	t.detach(v)
	conn.Close()
	<-writerDone
}

// replyItem is the attach reply line, written before any frame.
const replyItem = 0xff

func reply(conn net.Conn, r wire.AttachReply) {
	line, _ := json.Marshal(r)
	conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	conn.Write(append(line, '\n'))
}

// Refuse answers an attach request with an error.
func Refuse(conn net.Conn, err *wire.Error) {
	reply(conn, wire.AttachReply{Error: err})
}

func (t *Term) detach(v *viewer) {
	t.mu.Lock()
	delete(t.viewers, v)
	if t.owner == v {
		// The size stays, unless view attaches ask for a fit.
		t.owner = nil
		t.scheduleFit()
	}
	t.mu.Unlock()
	v.mu.Lock()
	v.closed, v.queue = true, nil
	v.mu.Unlock()
	select {
	case v.wake <- struct{}{}:
	default:
	}
}

// read takes the viewer's frames: input (rw only) and resizes (the size
// owner only).
func (v *viewer) read(r *bufio.Reader) {
	var buf []byte
	for {
		typ, p, err := wire.ReadFrame(r, buf)
		if err != nil {
			return
		}
		if cap(p) > cap(buf) {
			buf = p[:0]
		}
		switch typ {
		case wire.FrameData:
			if !v.rw {
				continue // read-only: refused
			}
			start := time.Now()
			if v.t.Input(p) != nil {
				continue
			}
			perf.Host.Since(StageInput, start)
		case wire.FrameResize:
			cols, rows, ok := wire.ParseSize(p)
			if !ok {
				continue
			}
			v.t.mu.Lock()
			owner := v.t.owner == v
			v.t.mu.Unlock()
			if owner {
				v.t.Resize(cols, rows)
			}
		}
	}
}

// write sends the queue: DATA chunks in a row go out as one frame (one
// writev), SIZE and EXIT as their own; after EXIT the connection closes.
func (v *viewer) write() {
	var bufs net.Buffers
	for range v.wake {
		for {
			v.mu.Lock()
			if v.closed {
				v.mu.Unlock()
				return
			}
			if v.resync {
				v.resync = false
				v.mu.Unlock()
				v.t.mu.Lock()
				redraw := v.t.screen.Redraw(nil)
				size := wire.SizePayload(v.t.cols, v.t.rows)
				exit := v.t.exit
				v.mu.Lock()
				v.queue = []item{{typ: wire.FrameSize, data: size}, {typ: wire.FrameData, data: redraw}}
				if exit != nil {
					v.queue = append(v.queue, item{typ: wire.FrameExit, data: wire.ExitPayload(exit)})
				}
				v.bytes = len(redraw)
				v.mu.Unlock()
				v.t.mu.Unlock()
				continue
			}
			queue := v.queue
			v.queue, v.bytes = nil, 0
			v.mu.Unlock()
			if len(queue) == 0 {
				break
			}
			for i := 0; i < len(queue); {
				it := queue[i]
				bufs = bufs[:0]
				end := i + 1
				switch it.typ {
				case replyItem:
					bufs = append(bufs, it.data)
				case wire.FrameData:
					n := len(it.data)
					for end < len(queue) && queue[end].typ == wire.FrameData {
						n += len(queue[end].data)
						end++
					}
					h := wire.FrameHeader(wire.FrameData, n)
					bufs = append(bufs, h[:])
					for _, q := range queue[i:end] {
						bufs = append(bufs, q.data)
					}
				default:
					h := wire.FrameHeader(it.typ, len(it.data))
					bufs = append(bufs, h[:], it.data)
				}
				v.conn.SetWriteDeadline(time.Now().Add(WriteTimeout))
				if _, err := bufs.WriteTo(v.conn); err != nil {
					v.conn.Close()
					return
				}
				if it.typ == wire.FrameExit {
					// Let the client read EXIT, then end.
					if c, ok := v.conn.(interface{ CloseWrite() error }); ok {
						c.CloseWrite()
					}
					v.conn.SetReadDeadline(time.Now().Add(2 * time.Second))
					return
				}
				i = end
			}
		}
	}
}
