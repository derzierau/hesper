package host

// Port forwards: a controller's hesperd forwards a port of its own Mac to
// a port on this machine's loopback. Each connection is a byte-stream
// channel of the caller's link (pkg/agentlink.Pipe), opened by a signed
// ports.open. Only with hesperd serve --allow-forward PORTS, only for
// those ports, only to 127.0.0.1 or ::1, and only for a device with the
// shell right: reaching this machine's loopback services is no more than
// a shell here could do.

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/derzierau/hesper/relay/pkg/agentlink"
	"github.com/derzierau/hesper/relay/pkg/devicekey"
	"github.com/derzierau/hesper/relay/pkg/protocol"
)

// Port forward limits.
const (
	maxForwards  = 64               // forward channels of one link (within maxChannels)
	forwardDial  = 2 * time.Second  // connecting to a forwarded port
	forwardFlush = 30 * time.Second // writing what the controller sent before its X
)

// forwardLimits are a Service's (tests change them; zero: the defaults).
type forwardLimits struct {
	max   int
	flush time.Duration
	dial  func(ctx context.Context, port int) (net.Conn, error)
}

func (s *Service) forwardLimits() forwardLimits {
	l := s.fwdLimits
	if l.max == 0 {
		l.max = maxForwards
	}
	if l.flush == 0 {
		l.flush = forwardFlush
	}
	if l.dial == nil {
		l.dial = dialLoopback
	}
	return l
}

// Ports is the set of ports this machine forwards (--allow-forward); the
// zero value forwards none.
type Ports struct {
	all    bool
	ranges [][2]int
}

// ParsePorts reads --allow-forward: "all", or ports and ranges separated
// by commas ("3000,5173,8000-8999"). Empty forwards none.
func ParsePorts(spec string) (Ports, error) {
	var p Ports
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return p, nil
	}
	if spec == "all" {
		p.all = true
		return p, nil
	}
	for _, part := range strings.Split(spec, ",") {
		lo, hi, isRange := strings.Cut(strings.TrimSpace(part), "-")
		if !isRange {
			hi = lo
		}
		a, err1 := strconv.Atoi(lo)
		b, err2 := strconv.Atoi(hi)
		if err1 != nil || err2 != nil || a < 1 || b > 65535 || a > b {
			return Ports{}, fmt.Errorf("invalid port or range %q (want 1-65535, e.g. 3000,8000-8999 or all)", part)
		}
		p.ranges = append(p.ranges, [2]int{a, b})
	}
	return p, nil
}

// Any reports whether some port is forwarded.
func (p Ports) Any() bool { return p.all || len(p.ranges) > 0 }

// Allows reports whether port is forwarded.
func (p Ports) Allows(port int) bool {
	if port < 1 || port > 65535 {
		return false
	}
	if p.all {
		return true
	}
	for _, r := range p.ranges {
		if port >= r[0] && port <= r[1] {
			return true
		}
	}
	return false
}

// forwardAllowed: --allow-forward with port among its ports, and a
// verified caller with the shell right.
func (s *Service) forwardAllowed(ctx context.Context, port int) error {
	caller, ok := CallerFrom(ctx)
	switch {
	case !s.AllowForward.Any():
		return protocol.Err("forbidden", "This machine forwards no ports (hesperd serve --allow-forward)")
	case !s.AllowForward.Allows(port):
		return protocol.Err("forbidden", fmt.Sprintf("Port %d is not among the ports this machine forwards (hesperd serve --allow-forward)", port))
	case !ok || !caller.Verified || !caller.Has(devicekey.Shell):
		return protocol.Err("forbidden", "Port forwards to this machine need a device approved with the shell right")
	}
	return nil
}

// openPort answers ports.open: a connection to port on this machine's
// loopback, bridged on channel ch of the caller's link.
func (s *Service) openPort(ctx context.Context, m protocol.Message) (json.RawMessage, error) {
	var p struct {
		Link string `json:"link"`
		Ch   uint32 `json:"ch"`
		Port int    `json:"port"`
	}
	if err := params(m.Params, &p); err != nil {
		return nil, err
	}
	if p.Ch == 0 {
		return nil, protocol.Err("invalid_request", "Invalid channel")
	}
	if err := s.forwardAllowed(ctx, p.Port); err != nil {
		return nil, err
	}
	link := s.links.get(p.Link)
	if link == nil || link.device != m.ControllerID {
		return nil, protocol.Err("not_found", "No such link")
	}
	limits := s.forwardLimits()
	if err := link.forwardRoom(p.Ch, limits.max); err != nil {
		return nil, err
	}
	nc, err := limits.dial(ctx, p.Port)
	if err != nil {
		return nil, protocol.Err("unavailable", fmt.Sprintf("Nothing accepts connections on port %d of this machine", p.Port))
	}
	c := &hostChan{done: make(chan struct{}), pipe: nc, flush: limits.flush}
	c.fwd = agentlink.NewPipe(link.conn, p.Ch, nc)
	link.mu.Lock()
	if link.ctx.Err() != nil {
		link.mu.Unlock()
		nc.Close()
		return nil, protocol.Err("not_found", "No such link")
	}
	if ctx.Err() != nil {
		// The controller gave up (its X came before the channel).
		link.mu.Unlock()
		nc.Close()
		return nil, protocol.Err("expired", "Request expired while connecting")
	}
	if err := link.forwardRoomLocked(p.Ch, limits.max); err != nil {
		link.mu.Unlock()
		nc.Close()
		return nil, err
	}
	link.chans[p.Ch] = c
	link.mu.Unlock()
	go func() {
		local := c.fwd.Run(link.ctx)
		link.forwardEnded(c)
		if local {
			link.endForward(p.Ch, c)
		}
	}()
	return protocol.JSON(struct{}{}), nil
}

// dialLoopback connects to port on 127.0.0.1, else ::1.
func dialLoopback(ctx context.Context, port int) (net.Conn, error) {
	d := net.Dialer{Timeout: forwardDial}
	nc, err := d.DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		nc, err = d.DialContext(ctx, "tcp", net.JoinHostPort("::1", strconv.Itoa(port)))
	}
	return nc, err
}

func (l *hostLink) forwardRoom(ch uint32, max int) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.forwardRoomLocked(ch, max)
}

// forwardRoomLocked: ch is free and the link has room for one more of
// at most max forwards (connections the controller closed count until
// they flushed).
func (l *hostLink) forwardRoomLocked(ch uint32, max int) error {
	n := l.draining
	for _, c := range l.chans {
		if c.fwd != nil {
			n++
		}
	}
	switch {
	case l.chans[ch] != nil || len(l.chans) >= maxChannels:
		return protocol.Err("invalid_request", "Channel in use")
	case n >= max:
		return protocol.Err("busy", "Too many forwarded connections on this link")
	}
	return nil
}

// drainForward takes the controller's X for a forward channel: the
// channel is free, but the connection counts until what the controller
// sent was written, at most c.flush.
func (l *hostLink) drainForward(c *hostChan) {
	l.mu.Lock()
	if !c.ended {
		c.draining = true
		l.draining++
	}
	l.mu.Unlock()
	c.pipe.SetWriteDeadline(time.Now().Add(c.flush))
	c.fwd.Close()
}

// forwardEnded: c's pipe ended.
func (l *hostLink) forwardEnded(c *hostChan) {
	l.mu.Lock()
	defer l.mu.Unlock()
	c.ended = true
	if c.draining {
		c.draining = false
		l.draining--
	}
}

// endForward: the forwarded connection ended here; the controller hears
// of it (unless the channel already went).
func (l *hostLink) endForward(ch uint32, c *hostChan) {
	l.mu.Lock()
	mine := l.chans[ch] == c
	if mine {
		delete(l.chans, ch)
	}
	l.mu.Unlock()
	c.once.Do(func() { close(c.done) })
	if mine {
		l.conn.Write(l.ctx, agentlink.KindClose, ch, nil)
	}
}

// window takes a W frame: a grant for a forward channel.
func (l *hostLink) window(ch uint32, p []byte) {
	l.mu.Lock()
	c := l.chans[ch]
	l.mu.Unlock()
	if c != nil && c.fwd != nil && !c.fwd.Window(p) {
		l.closeChan(ch, true)
	}
}
