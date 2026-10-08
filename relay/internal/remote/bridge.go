package remote

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"net"
	"sync"
	"time"

	"github.com/derzierau/hesper/relay/pkg/agentlink"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

// A bridge is one local attach connection to a remote agent. It carries
// the local attach protocol over a channel of the machine's link: the
// local client's frames go to the host unchanged, the host's reply line
// and frames come back unchanged, so `hesperd attach M/x` behaves exactly
// like `hesperd attach L/x` (redraw first, the size owner rule, EXIT).
//
// The bridge parses the host's stream into whole frames and queues them
// for the local client. It reattaches with a fresh screen, never leaving
// the client mid-frame: when the client reads too slowly (the queue
// passes QueueLimit, as ptyhost does for local viewers), when the host
// ends the channel without EXIT, when the link breaks (it waits for the
// next link, up to RelinkWait) and when the link moves to the direct
// path. The client then gets a SIZE frame and the new redraw.

// Bridge limits.
var (
	QueueLimit   = 4 << 20
	RelinkWait   = 30 * time.Second
	attachSetup  = 15 * time.Second
	writeTimeout = 30 * time.Second
)

func decodeEvent(p []byte, e *agentlink.Event) error { return json.Unmarshal(p, e) }

type bridge struct {
	f     *Fleet
	short string
	conn  net.Conn
	r     *bufio.Reader
	req   wire.AttachRequest // the local client's, Attach = the host's local id

	mu       sync.Mutex
	link     *link
	ch       uint32
	inReply  bool   // the current channel's reply line is still to come
	pending  []byte // host bytes not yet a whole line or frame
	replied  bool   // the client got its reply line
	queue    [][]byte
	queued   int
	exited   bool
	closed   bool
	resyncs  int
	resync   bool // the queue overflowed: reattach once the client read what it has
	resizing bool // a reattach is under way
	wake     chan struct{}
	writer   chan struct{} // closed when the writer ended
}

// Attach serves an attach connection to a remote agent (agents.Remote).
func (f *Fleet) Attach(conn net.Conn, r *bufio.Reader, req wire.AttachRequest) {
	defer conn.Close()
	short, local := splitID(req.Attach)
	refuse := func(err error) {
		we, ok := wireError(err).(*wire.Error)
		if !ok {
			we = wire.Errorf(wire.CodeRemote, "%v", err)
		}
		line, _ := json.Marshal(wire.AttachReply{Error: we})
		conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		conn.Write(append(line, '\n'))
	}
	if req.Mode != wire.ModeRO && req.Mode != wire.ModeRW {
		refuse(wire.Errorf(wire.CodeInvalid, "mode must be rw or ro"))
		return
	}
	req.Attach = local
	b := &bridge{f: f, short: short, conn: conn, r: r, req: req, wake: make(chan struct{}, 1), writer: make(chan struct{})}
	ctx, cancel := context.WithTimeout(context.Background(), attachSetup)
	defer cancel()
	for attempt := 0; ; attempt++ {
		_, l, err := f.waitLink(ctx, short, 10*time.Second)
		if err == nil {
			err = b.open(l, req)
		}
		if err == nil {
			break
		}
		// The link closed or was replaced (moved to the direct path)
		// while the attach was on its way: once more on the current one.
		if l != nil && l.gone() && attempt < 3 && ctx.Err() == nil {
			continue
		}
		refuse(err)
		return
	}
	go b.write()
	b.readLocal()
	b.finish()
	<-b.writer
}

func splitID(id string) (string, string) {
	for i := 0; i < len(id); i++ {
		if id[i] == '/' {
			return id[:i], id[i+1:]
		}
	}
	return "", id
}

// open attaches on a channel of l with req (agents.attach, signed).
func (b *bridge) open(l *link, req wire.AttachRequest) error {
	ch, ok := l.add(b)
	if !ok {
		return wire.Errorf(wire.CodeUnavailable, "the link to %s just closed", b.short)
	}
	b.mu.Lock()
	b.link, b.ch, b.inReply, b.pending = l, ch, true, nil
	b.mu.Unlock()
	f := b.f
	f.mu.Lock()
	c := f.c
	f.mu.Unlock()
	if c == nil {
		l.remove(ch, false)
		return wire.Errorf(wire.CodeUnavailable, "not connected to the relay")
	}
	request, _ := json.Marshal(req)
	ctx, cancel := context.WithTimeout(context.Background(), attachSetup)
	defer cancel()
	_, err := f.request(ctx, c, l.m, "agents.attach", map[string]any{"link": l.id, "ch": ch, "mode": req.Mode, "request": json.RawMessage(request)})
	if err != nil {
		l.remove(ch, false)
		b.mu.Lock()
		if b.link == l && b.ch == ch {
			b.link = nil
		}
		b.mu.Unlock()
		return err
	}
	return nil
}

// fromHost takes the host's bytes of a channel.
func (b *bridge) fromHost(l *link, ch uint32, p []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || l != b.link || ch != b.ch {
		return
	}
	b.pending = append(b.pending, p...)
	if b.inReply {
		i := bytes.IndexByte(b.pending, '\n')
		if i < 0 {
			if len(b.pending) > 64<<10 {
				b.closeLocked()
			}
			return
		}
		line := b.pending[:i+1]
		var reply wire.AttachReply
		json.Unmarshal(line, &reply)
		if !b.replied {
			b.replied = true
			b.enqueueLocked(append([]byte(nil), line...))
			if !reply.OK {
				b.exited = true // the client reads the refusal, then the end
			}
		} else if reply.OK {
			b.enqueueLocked(wire.AppendFrame(nil, wire.FrameSize, wire.SizePayload(reply.Cols, reply.Rows)))
		} else {
			b.closeLocked()
			return
		}
		b.pending = b.pending[i+1:]
		b.inReply = false
	}
	for len(b.pending) >= 5 {
		n := int(binary.BigEndian.Uint32(b.pending[1:5]))
		if n > wire.MaxFrame {
			b.closeLocked()
			return
		}
		if len(b.pending) < 5+n {
			break
		}
		frame := append([]byte(nil), b.pending[:5+n]...)
		b.pending = b.pending[5+n:]
		if frame[0] == wire.FrameExit {
			b.exited = true
		}
		if b.queued+len(frame) > QueueLimit && !b.exited {
			// The client reads too slowly: drop what it has not read and
			// reattach for a fresh screen.
			b.queue, b.queued = nil, 0
			b.resyncLocked()
			return
		}
		b.enqueueLocked(frame)
	}
	if len(b.pending) == 0 {
		b.pending = nil
	}
}

func (b *bridge) enqueueLocked(item []byte) {
	b.queue = append(b.queue, item)
	b.queued += len(item)
	select {
	case b.wake <- struct{}{}:
	default:
	}
}

// resyncLocked ends the current channel; the writer reattaches (a new
// channel, its reply turned into SIZE, a redraw) when the client has read
// what was already written to it. Reattaching at once would let a client
// that reads nothing make the bridge reattach over and over, each time
// with a request through the relay and a full redraw over the link.
func (b *bridge) resyncLocked() {
	l, ch := b.link, b.ch
	b.link = nil
	b.resyncs++
	b.resync = true
	if l != nil {
		go l.remove(ch, true)
	}
	b.wakeLocked()
}

// hostClosed: the host ended the channel. After EXIT that is the end;
// otherwise the bridge reattaches.
func (b *bridge) hostClosed(l *link, ch uint32) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || l != b.link || ch != b.ch {
		return
	}
	if b.exited {
		b.link = nil
		b.wakeLocked()
		return
	}
	b.link = nil
	go b.reattach(l)
}

// linkLost: the link broke; the next one carries the attach on.
func (b *bridge) linkLost(l *link) {
	b.mu.Lock()
	if b.closed || b.link != l {
		b.mu.Unlock()
		return
	}
	b.link = nil
	b.mu.Unlock()
	b.reattach(nil)
}

// moveTo: the link was replaced (the direct path came up).
func (b *bridge) moveTo(l *link) {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.link = nil
	b.mu.Unlock()
	b.reattachOn(l)
}

// reattach waits for a link other than old (or any, with old nil when it
// is gone) and attaches again.
func (b *bridge) reattach(old *link) {
	ctx, cancel := context.WithTimeout(context.Background(), RelinkWait)
	defer cancel()
	for {
		_, l, err := b.f.waitLink(ctx, b.short, RelinkWait)
		if err != nil {
			b.finish()
			return
		}
		if l.ctx.Err() == nil {
			if b.reattachOn(l) {
				return
			}
		}
		if ctx.Err() != nil {
			b.finish()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// reattachOn attaches again on l; the owner claims the size it last had.
func (b *bridge) reattachOn(l *link) bool {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return true
	}
	req := b.req
	b.resync = false // this attach is the fresh screen a pending resync wants
	b.mu.Unlock()
	// A reattach only resyncs: the client's terminal already has the
	// scrollback (and a big one could overflow the queue again).
	no := false
	req.History = &no
	if err := b.open(l, req); err != nil {
		if !l.gone() {
			b.f.opt.Logf("remote: reattach %s/%s: %v", b.short, req.Attach, err)
			b.finish()
			return true
		}
		return false
	}
	return true
}

// readLocal sends the client's frames to the host; RESIZE frames are
// remembered so a reattach keeps the owner's size.
func (b *bridge) readLocal() {
	var buf []byte
	for {
		typ, p, err := wire.ReadFrame(b.r, buf)
		if err != nil {
			return
		}
		if cap(p) > cap(buf) {
			buf = p[:0]
		}
		if typ == wire.FrameResize {
			if cols, rows, ok := wire.ParseSize(p); ok {
				b.mu.Lock()
				b.req.Cols, b.req.Rows = cols, rows
				b.mu.Unlock()
			}
		}
		frame := wire.AppendFrame(make([]byte, 0, 5+len(p)), typ, p)
		b.mu.Lock()
		l, ch := b.link, b.ch
		b.mu.Unlock()
		if l == nil {
			continue // reattaching: typed input is dropped, as on a reconnect
		}
		if l.conn.Write(l.ctx, agentlink.KindData, ch, frame) != nil {
			continue
		}
	}
}

// write sends the queue to the client; after EXIT (or a refusal) the
// connection ends.
func (b *bridge) write() {
	defer close(b.writer)
	for range b.wake {
		for {
			b.mu.Lock()
			queue := b.queue
			b.queue, b.queued = nil, 0
			closed, exited := b.closed, b.exited
			resync := len(queue) == 0 && b.resync && !closed
			if resync {
				b.resync = false
			}
			b.mu.Unlock()
			if resync {
				go b.reattach(nil)
			}
			if len(queue) == 0 {
				if closed || exited && b.drained() {
					b.conn.Close()
					return
				}
				break
			}
			bufs := net.Buffers(queue)
			b.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
			if _, err := bufs.WriteTo(b.conn); err != nil {
				b.conn.Close()
				b.finish()
				return
			}
		}
	}
}

func (b *bridge) drained() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.queue) == 0
}

func (b *bridge) wakeLocked() {
	select {
	case b.wake <- struct{}{}:
	default:
	}
}

func (b *bridge) closeLocked() {
	if b.closed {
		return
	}
	b.closed = true
	if l := b.link; l != nil {
		go l.remove(b.ch, true)
	}
	b.link = nil
	b.wakeLocked()
}

// finish ends the bridge: the host's channel closes, the client's
// connection too (after what is queued, when the agent exited).
func (b *bridge) finish() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.exited && len(b.queue) > 0 && !b.closed {
		// The writer closes after EXIT.
		if l := b.link; l != nil {
			go l.remove(b.ch, true)
			b.link = nil
		}
		b.wakeLocked()
		return
	}
	b.closeLocked()
	b.conn.Close()
}
