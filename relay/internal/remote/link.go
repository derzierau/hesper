package remote

import (
	"context"
	"net"
	"sync"
	"time"

	"github.com/derzierau/hesper/relay/pkg/agentlink"
	"github.com/derzierau/hesper/relay/pkg/client"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

// link is this Mac's end of a link to one machine (pkg/agentlink).
type link struct {
	f      *Fleet
	m      *machine
	id     string // the stream's ticket: agents.attach names it
	route  string
	conn   *agentlink.Conn
	ctx    context.Context
	cancel context.CancelFunc

	mu       sync.Mutex
	bridges  map[uint32]*bridge
	forwards map[uint32]*fwdConn // port forwards' connections (forward.go)
	next     uint32
	moved    bool // its bridges went to a newer link

	loopsKick chan struct{} // its loops worker's (loops.go; under f.mu)
}

// linkSetup bounds opening a link.
var linkSetup = 20 * time.Second

// openLink opens a link to m (agents.link through the channel, on the
// direct path when it is up) and makes it m's current link; bridges of a
// link it replaces move to it.
func (f *Fleet) openLink(c *client.Controller, m *machine) {
	f.mu.Lock()
	creds := f.creds
	f.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), linkSetup)
	var how client.E2EReport
	t, err := c.OpenStream(client.WithE2EReport(client.RequireE2E(ctx), &how), creds, m.id, "agents.link", struct{}{})
	cancel()
	f.mu.Lock()
	defer f.mu.Unlock()
	m.linking = false
	if err != nil || f.c != c {
		if t != nil {
			t.Close()
		}
		if err != nil {
			m.retry = time.Now().Add(2 * time.Second)
			f.opt.Logf("remote: no link to %s: %v", m.short, err)
		}
		f.notifyLocked()
		return
	}
	lctx, lcancel := context.WithCancel(context.Background())
	l := &link{f: f, m: m, id: t.ID, route: client.RouteRelay, conn: agentlink.New(t), ctx: lctx, cancel: lcancel, bridges: map[uint32]*bridge{},
		forwards: map[uint32]*fwdConn{}}
	if t.Direct() {
		l.route = client.RouteDirect
	}
	old := m.link
	m.link = l
	if m.lost != nil {
		m.lost.Stop()
		m.lost = nil
	}
	f.notifyLocked()
	go l.read()
	go f.pingOne(c, m)
	go f.syncProjects(c, m) // projects step 1
	f.sessionsLinkUp(m)     // shared history
	if old != nil {
		old.mu.Lock()
		old.moved = true
		moving := old.bridges
		old.bridges = map[uint32]*bridge{}
		cut := old.forwards
		old.forwards = map[uint32]*fwdConn{}
		old.mu.Unlock()
		for _, b := range moving {
			go b.moveTo(l)
		}
		// A byte stream cannot resume on another link.
		for _, c := range cut {
			c.reset()
		}
		old.close()
	}
}

// gone reports whether the link closed or was replaced.
func (l *link) gone() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.ctx.Err() != nil || l.moved
}

func (l *link) close() {
	l.cancel()
	l.conn.Close()
}

// add registers a bridge on a new channel.
func (l *link) add(b *bridge) (uint32, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.ctx.Err() != nil || l.moved {
		return 0, false
	}
	l.next++
	l.bridges[l.next] = b
	return l.next, true
}

func (l *link) remove(ch uint32, notify bool) {
	l.mu.Lock()
	_, ok := l.bridges[ch]
	delete(l.bridges, ch)
	l.mu.Unlock()
	if ok && notify && l.ctx.Err() == nil {
		l.conn.Write(l.ctx, agentlink.KindClose, ch, nil)
	}
}

func (l *link) bridge(ch uint32) *bridge {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.bridges[ch]
}

// addForward registers a forwarded connection on a new channel.
func (l *link) addForward(nc net.Conn) (*fwdConn, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.ctx.Err() != nil || l.moved {
		return nil, false
	}
	l.next++
	c := &fwdConn{ch: l.next, nc: nc, pipe: agentlink.NewPipe(l.conn, l.next, nc)}
	c.ctx, c.cancel = context.WithCancel(l.ctx)
	l.forwards[c.ch] = c
	return c, true
}

// removeForward drops c's channel; notify tells the host.
func (l *link) removeForward(c *fwdConn, notify bool) {
	l.mu.Lock()
	ok := l.forwards[c.ch] == c
	if ok {
		delete(l.forwards, c.ch)
	}
	l.mu.Unlock()
	if ok && notify && l.ctx.Err() == nil {
		l.conn.Write(l.ctx, agentlink.KindClose, c.ch, nil)
	}
}

func (l *link) forward(ch uint32) *fwdConn {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.forwards[ch]
}

// read takes the host's frames until the link ends.
func (l *link) read() {
	defer l.lost()
	for {
		kind, ch, payload, err := l.conn.Read(l.ctx)
		if err != nil {
			return
		}
		switch kind {
		case agentlink.KindEvent:
			var e agentlink.Event
			if decodeEvent(payload, &e) != nil {
				return
			}
			if len(e.Projects) > 0 { // projects step 1
				l.f.projectsEvent(l, e.Projects)
				continue
			}
			if len(e.Sessions) > 0 { // shared history
				l.f.sessionsEvent(l, e.Sessions)
				continue
			}
			if len(e.Loops) > 0 { // loops.go
				l.f.loopsEvent(l, e.Loops)
				continue
			}
			l.f.event(l, e)
		case agentlink.KindData:
			if c := l.forward(ch); c != nil {
				if !c.pipe.Data(payload) {
					l.removeForward(c, true) // past its window
					c.reset()
				}
			} else if b := l.bridge(ch); b != nil {
				b.fromHost(l, ch, payload)
			}
		case agentlink.KindWindow:
			if c := l.forward(ch); c != nil && !c.pipe.Window(payload) {
				l.removeForward(c, true)
				c.reset()
			}
		case agentlink.KindClose:
			if c := l.forward(ch); c != nil {
				l.removeForward(c, false)
				c.nc.SetWriteDeadline(time.Now().Add(l.f.forwardFlush()))
				c.pipe.Close()
			} else if b := l.bridge(ch); b != nil {
				l.remove(ch, false)
				b.hostClosed(l, ch)
			}
		default:
			return
		}
	}
}

// lost: the link ended. Its bridges wait for the next one; the machine's
// agents stay listed for the grace period.
func (l *link) lost() {
	l.close()
	f, m := l.f, l.m
	f.mu.Lock()
	if m.link == l {
		m.link = nil
		m.retry = time.Now().Add(time.Second)
		if m.lost == nil {
			m.lost = time.AfterFunc(f.opt.Grace, func() {
				f.mu.Lock()
				defer f.mu.Unlock()
				if m.link == nil {
					f.dropAgentsLocked(m)
				}
				m.lost = nil
			})
		}
	}
	f.notifyLocked()
	f.mu.Unlock()
	l.mu.Lock()
	bridges := l.bridges
	l.bridges = map[uint32]*bridge{}
	cut := l.forwards
	l.forwards = map[uint32]*fwdConn{}
	l.mu.Unlock()
	for _, b := range bridges {
		go b.linkLost(l)
	}
	for _, c := range cut {
		c.reset()
	}
}

// event applies one of the host's agent events.
func (f *Fleet) event(l *link, e agentlink.Event) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := l.m
	if m.link != l {
		return // a link being replaced
	}
	switch {
	case e.Full:
		m.hostShort = e.Machine
		if e.Node != "" && e.Node != m.node {
			if m.backfill {
				f.renameStandInLocked(m, e.Node) // loops.go
			}
			m.node = e.Node
		}
		listed := map[string]bool{}
		for _, a := range e.List {
			listed[localID(a.ID)] = true
		}
		isHost := func(local string) bool { return listed[local] }
		fresh := map[string]wire.Agent{}
		for _, a := range e.List {
			a = m.namedIn(a, isHost)
			a.Loop = f.loopOfLocked(m, localID(a.ID)) // loops.go
			fresh[localID(a.ID)] = a
		}
		for local, a := range m.agents {
			if _, ok := fresh[local]; !ok {
				f.emitRemovedLocked(a.ID)
			}
		}
		m.agents = fresh
		for _, a := range e.List {
			f.emitChangedLocked(fresh[localID(a.ID)])
		}
		f.armBackfillLocked(m) // loops.go
	case e.Changed != nil:
		a := m.named(*e.Changed)
		a.Loop = f.loopOfLocked(m, localID(a.ID)) // loops.go
		m.agents[localID(a.ID)] = a
		f.emitChangedLocked(a)
		f.backfillLoopsLocked(m) // loops.go
	case e.Removed != "":
		local := localID(e.Removed)
		if a, ok := m.agents[local]; ok {
			delete(m.agents, local)
			f.emitRemovedToLocked(a.ID, e.Reason, e.To)
		}
	}
}
