package client

// The controller's side of the direct path (pkg/direct,
// docs/direct-path.md). With EnableDirect a controller that has the
// end-to-end channel asks every online host that advertises
// capabilities.direct for its addresses (direct.offer, inside the relay's
// channel), dials them at once with a short timeout (happy eyeballs) and,
// once one answers as the pinned host, sends its requests and opens its
// terminal streams there instead of through the relay. Snapshots and
// presence keep coming from the relay. When the direct connection fails
// (pings, a write, the host going away) everything falls back to the relay
// at once and the path is probed again later, sooner after a change of
// this machine's addresses.
//
// Requests in flight when the connection fails: one that was not written
// goes through the relay; one that may have reached the host is resent
// through the relay only when it is idempotent (snapshot, capture, ...),
// as the identical inner request (same ID, same signature), so the host's
// memory of inner IDs refuses it (duplicate_request) if it ran after all.
// Anything else fails with connection_lost (outcome unknown), never
// replayed, exactly like a relay connection lost mid-request.

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"maps"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/derzierau/hesper/relay/pkg/direct"
	"github.com/derzierau/hesper/relay/pkg/e2e"
	"github.com/derzierau/hesper/relay/pkg/protocol"
)

// Routes a host is reached by.
const (
	RouteDirect = "direct"
	RouteRelay  = "relay"
)

// DirectConfig turns on the direct path (EnableDirect). Zero values take
// the defaults.
type DirectConfig struct {
	// ProbeTimeout bounds dialing a host's addresses (default 300 ms).
	ProbeTimeout time.Duration
	// PingEvery and PingTimeout watch an established path (2 s each).
	PingEvery, PingTimeout time.Duration
	// RefreshOffer: an established path fetches a fresh offer through the
	// relay this often (10 minutes), which keeps it open on the host.
	RefreshOffer time.Duration
	// Retry is the first wait after a failed probe (15 s); it doubles up
	// to MaxRetry (5 minutes) and starts over when this machine's
	// addresses change.
	Retry, MaxRetry time.Duration
	// OnRoute (optional) is called when a host's route changes.
	OnRoute func(machineID, route string)
	// AllowLoopback lets offers name loopback addresses (tests only).
	AllowLoopback bool
	// LocalAddrs (optional) replaces this machine's address list, which a
	// change of triggers a probe (tests).
	LocalAddrs func() []netip.Addr
	// DialAddr (optional) maps a candidate to the address dialed (tests put
	// a proxy in between).
	DialAddr func(string) string
}

type directState struct {
	cfg   DirectConfig
	mu    sync.Mutex
	paths map[string]*directPath
	kick  chan struct{}
}

// directPath is the direct path to one host.
type directPath struct {
	machine   string
	live      *liveConn
	probing   bool
	refresh   bool
	nextProbe time.Time
	failures  int
	offered   time.Time
	route     string
}

// liveConn is one established direct connection.
type liveConn struct {
	conn    *direct.Conn
	addr    string
	require bool
	pong    chan struct{}
	mu      sync.Mutex
	pending map[string]chan directAnswer
	dead    bool
}

type directAnswer struct {
	resp e2e.Response
	lost bool
}

type relayKey struct{}

// ViaRelay makes requests (and terminal streams) made with ctx go through
// the relay, not the direct path.
func ViaRelay(ctx context.Context) context.Context { return context.WithValue(ctx, relayKey{}, true) }

func viaRelay(ctx context.Context) bool { v, _ := ctx.Value(relayKey{}).(bool); return v }

// EnableDirect turns on the direct path. It needs the end-to-end channel
// (EnableE2E) and its own relay connection (a fleet sync's controller);
// through a fleet sync the fleet decides.
func (c *Controller) EnableDirect(cfg DirectConfig) {
	if c.fleet != nil || c.e2e == nil {
		return
	}
	if cfg.ProbeTimeout <= 0 {
		cfg.ProbeTimeout = 300 * time.Millisecond
	}
	if cfg.PingEvery <= 0 {
		cfg.PingEvery = 2 * time.Second
	}
	if cfg.PingTimeout <= 0 {
		cfg.PingTimeout = 2 * time.Second
	}
	if cfg.RefreshOffer <= 0 {
		cfg.RefreshOffer = 10 * time.Minute
	}
	if cfg.Retry <= 0 {
		cfg.Retry = 15 * time.Second
	}
	if cfg.MaxRetry <= 0 {
		cfg.MaxRetry = 5 * time.Minute
	}
	if cfg.LocalAddrs == nil {
		cfg.LocalAddrs = direct.LocalAddrs
	}
	state := &directState{cfg: cfg, paths: map[string]*directPath{}, kick: make(chan struct{}, 1)}
	c.dmu.Lock()
	if c.direct != nil {
		c.dmu.Unlock()
		return
	}
	c.direct = state
	c.dmu.Unlock()
	go c.directLoop(state)
	c.kickDirect()
}

func (c *Controller) directState() *directState {
	c.dmu.Lock()
	defer c.dmu.Unlock()
	return c.direct
}

// kickDirect makes the direct loop look at the inventory now.
func (c *Controller) kickDirect() {
	if d := c.directState(); d != nil {
		select {
		case d.kick <- struct{}{}:
		default:
		}
	}
}

// Route reports how requests to a host travel right now: "direct" or
// "relay" ("" for a host the inventory does not list online).
func (c *Controller) Route(machineID string) string {
	if d := c.directState(); d != nil {
		d.mu.Lock()
		p := d.paths[machineID]
		up := p != nil && p.live != nil
		d.mu.Unlock()
		if up {
			return RouteDirect
		}
	}
	c.invMu.Lock()
	m, ok := c.inventory[machineID]
	c.invMu.Unlock()
	if !ok || !m.Online {
		return ""
	}
	return RouteRelay
}

// AwaitDirect waits up to timeout for the direct path to a host that
// advertises it (not at all for others), so that a short-lived command's
// first terminal stream can use it. It reports whether the path is up.
func (c *Controller) AwaitDirect(ctx context.Context, machineID string, timeout time.Duration) bool {
	d := c.directState()
	if d == nil {
		return false
	}
	if m, ok := c.machine(ctx, machineID); !ok || !advertisesDirect(m) {
		return false
	}
	c.kickDirect()
	deadline := time.Now().Add(timeout)
	for ctx.Err() == nil && time.Now().Before(deadline) {
		d.mu.Lock()
		p := d.paths[machineID]
		up := p != nil && p.live != nil
		failed := p != nil && !p.probing && p.failures > 0
		d.mu.Unlock()
		if up || failed {
			return up
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

// DirectAddr is the address of the direct connection to a host, "" when
// there is none.
func (c *Controller) DirectAddr(machineID string) string {
	if lc := c.liveConn(machineID); lc != nil {
		return lc.addr
	}
	return ""
}

func (c *Controller) liveConn(machineID string) *liveConn {
	d := c.directState()
	if d == nil {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if p := d.paths[machineID]; p != nil {
		return p.live
	}
	return nil
}

func advertisesDirect(m protocol.Machine) bool {
	if !m.Online || len(m.Snapshot) == 0 {
		return false
	}
	var snap struct {
		Capabilities struct {
			E2E    bool `json:"e2e"`
			Direct bool `json:"direct"`
		} `json:"capabilities"`
	}
	return json.Unmarshal(m.Snapshot, &snap) == nil && snap.Capabilities.E2E && snap.Capabilities.Direct
}

func addrsKey(addrs []netip.Addr) string {
	parts := make([]string, len(addrs))
	for i, a := range addrs {
		parts[i] = a.String()
	}
	slices.Sort(parts)
	return strings.Join(parts, ",")
}

func (c *Controller) directLoop(d *directState) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	local := addrsKey(d.cfg.LocalAddrs())
	checked := time.Now()
	for {
		select {
		case <-c.ctx.Done():
			d.mu.Lock()
			for _, p := range d.paths {
				if p.live != nil {
					p.live.conn.Close()
				}
			}
			d.mu.Unlock()
			return
		case <-d.kick:
		case <-tick.C:
		}
		now := time.Now()
		changed := false
		if now.Sub(checked) >= 3*time.Second {
			checked = now
			if key := addrsKey(d.cfg.LocalAddrs()); key != local {
				local, changed = key, true
			}
		}
		c.invMu.Lock()
		inventory := maps.Clone(c.inventory)
		c.invMu.Unlock()
		d.mu.Lock()
		for id, m := range inventory {
			p := d.paths[id]
			if p == nil {
				p = &directPath{machine: id}
				d.paths[id] = p
			}
			if !advertisesDirect(m) {
				if p.live != nil {
					p.live.conn.Close()
				}
				continue
			}
			if changed {
				// Another network: try again now; a live connection's
				// pings tell whether it survived.
				p.nextProbe, p.failures = now, 0
			}
			switch {
			case p.live != nil && now.Sub(p.offered) >= d.cfg.RefreshOffer && !p.refresh:
				p.refresh = true
				go c.refreshOffer(d, p)
			case p.live == nil && !p.probing && !now.Before(p.nextProbe):
				p.probing = true
				go c.probe(d, p)
			}
		}
		for id, p := range d.paths {
			if _, ok := inventory[id]; !ok && p.live != nil {
				p.live.conn.Close()
			}
		}
		d.mu.Unlock()
	}
}

// fetchOffer asks a host for its addresses inside the relay's channel.
func (c *Controller) fetchOffer(ctx context.Context, machineID string) (direct.Offer, error) {
	var offer direct.Offer
	var how E2EReport
	raw, err := c.Forward(WithE2EReport(ViaRelay(ctx), &how), machineID, "direct.offer", json.RawMessage("{}"), nil)
	if err != nil {
		return offer, err
	}
	if !how.Used {
		return offer, errors.New("direct.offer did not travel inside the channel")
	}
	if err := json.Unmarshal(raw, &offer); err != nil || offer.V != direct.Version {
		return offer, errors.New("invalid direct offer")
	}
	return offer, nil
}

func (c *Controller) pinnedKey(machineID string) ([]byte, error) {
	if c.e2e == nil {
		return nil, errors.New("no end-to-end channel")
	}
	trust, err := e2e.LoadTrust(c.e2e.TrustPath)
	if err != nil {
		return nil, err
	}
	pinned, ok := trust.Hosts[machineID]
	if !ok || pinned.E2E == 0 {
		return nil, errors.New("no pinned host key with the channel")
	}
	key, err := base64.StdEncoding.DecodeString(pinned.Key)
	if err != nil || len(key) != 32 {
		return nil, errors.New("invalid pinned host key")
	}
	return key, nil
}

func (c *Controller) probe(d *directState, p *directPath) {
	ctx, cancel := context.WithTimeout(c.ctx, 15*time.Second)
	defer cancel()
	failed := func(wait time.Duration) {
		d.mu.Lock()
		defer d.mu.Unlock()
		p.probing = false
		if wait == 0 {
			wait = d.cfg.Retry << min(p.failures, 8)
		}
		p.failures++
		p.nextProbe = time.Now().Add(min(wait, d.cfg.MaxRetry))
	}
	// The channel through the relay comes first: it pins and proves the
	// host's key, and carries the offer.
	offer, err := c.fetchOffer(ctx, p.machine)
	if err != nil || len(offer.Addrs) == 0 {
		failed(0)
		return
	}
	key, err := c.pinnedKey(p.machine)
	if err != nil {
		failed(d.cfg.MaxRetry)
		return
	}
	allow := func(a netip.Addr) bool { return d.cfg.AllowLoopback && a.IsLoopback() }
	candidates := direct.Candidates(offer.Addrs, allow, direct.LinkLocalZones(), 8)
	if d.cfg.DialAddr != nil {
		for i := range candidates {
			candidates[i] = d.cfg.DialAddr(candidates[i])
		}
	}
	conn, addr, err := c.race(ctx, d.cfg.ProbeTimeout, candidates, direct.DialConfig{MachineID: p.machine, HostKey: key,
		Identity: c.e2e.Identity, Device: c.device, Token: offer.Token})
	if err != nil {
		failed(0)
		return
	}
	lc := &liveConn{conn: conn, addr: addr, require: conn.Welcome.Require, pong: make(chan struct{}, 1), pending: map[string]chan directAnswer{}}
	d.mu.Lock()
	p.probing, p.failures, p.offered = false, 0, time.Now()
	if c.ctx.Err() != nil {
		d.mu.Unlock()
		conn.Close()
		return
	}
	p.live = lc
	d.mu.Unlock()
	go c.readDirect(d, p, lc)
	go c.pingDirect(d, lc)
	c.routeChanged(d, p)
	c.noteDirect(p.machine)
}

// race dials every candidate, the next one 50 ms after the previous, and
// keeps the first that completes the handshake as the pinned host.
func (c *Controller) race(ctx context.Context, timeout time.Duration, candidates []string, cfg direct.DialConfig) (*direct.Conn, string, error) {
	if len(candidates) == 0 {
		return nil, "", errors.New("no usable address")
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	type outcome struct {
		conn *direct.Conn
		addr string
		err  error
	}
	results := make(chan outcome, len(candidates))
	for i, addr := range candidates {
		go func() {
			if i > 0 {
				wait := time.NewTimer(time.Duration(i) * 50 * time.Millisecond)
				select {
				case <-wait.C:
				case <-ctx.Done():
					wait.Stop()
					results <- outcome{err: ctx.Err()}
					return
				}
			}
			conn, err := direct.Dial(ctx, addr, cfg)
			results <- outcome{conn, addr, err}
		}()
	}
	var win outcome
	err := errors.New("no address answered")
	for range candidates {
		o := <-results
		switch {
		case o.err == nil && win.conn == nil:
			win = o
			cancel()
		case o.conn != nil:
			o.conn.Close()
		case o.err != nil && win.conn == nil:
			err = o.err
		}
	}
	if win.conn == nil {
		return nil, "", err
	}
	return win.conn, win.addr, nil
}

func (c *Controller) refreshOffer(d *directState, p *directPath) {
	ctx, cancel := context.WithTimeout(c.ctx, 15*time.Second)
	defer cancel()
	_, err := c.fetchOffer(ctx, p.machine)
	d.mu.Lock()
	defer d.mu.Unlock()
	p.refresh = false
	var fault *protocol.Error
	switch {
	case err == nil:
		p.offered = time.Now()
	case errors.As(err, &fault) && (fault.Code == "forbidden" || fault.Code == "key_changed"):
		// No longer approved (or another key): off the direct path too.
		if p.live != nil {
			p.live.conn.Close()
		}
	default:
		// The relay is unreachable: keep the direct path; retry in a minute
		// (the host closes it after 30 minutes without an offer).
		p.offered = time.Now().Add(-d.cfg.RefreshOffer + time.Minute)
	}
}

func (c *Controller) readDirect(d *directState, p *directPath, lc *liveConn) {
	defer c.lostDirect(d, p, lc)
	for {
		kind, body, err := lc.conn.ReadMessage()
		if err != nil {
			return
		}
		switch kind {
		case direct.KindResponse:
			var resp e2e.Response
			if json.Unmarshal(body, &resp) != nil {
				lc.conn.Close()
				return
			}
			lc.mu.Lock()
			ch := lc.pending[resp.ID]
			delete(lc.pending, resp.ID)
			lc.mu.Unlock()
			if ch != nil {
				ch <- directAnswer{resp: resp}
			}
		case direct.KindPong:
			select {
			case lc.pong <- struct{}{}:
			default:
			}
		case direct.KindPing:
			if len(body) > 64 || lc.conn.WriteMessage(c.ctx, direct.KindPong, body) != nil {
				lc.conn.Close()
				return
			}
		default:
			lc.conn.Close()
			return
		}
	}
}

func (c *Controller) pingDirect(d *directState, lc *liveConn) {
	tick := time.NewTicker(d.cfg.PingEvery)
	defer tick.Stop()
	var seq uint64
	for {
		select {
		case <-lc.conn.Done():
			return
		case <-c.ctx.Done():
			lc.conn.Close()
			return
		case <-tick.C:
		}
		seq++
		select {
		case <-lc.pong:
		default:
		}
		if lc.conn.WriteMessage(c.ctx, direct.KindPing, binary.BigEndian.AppendUint64(nil, seq)) != nil {
			lc.conn.Close()
			return
		}
		wait := time.NewTimer(d.cfg.PingTimeout)
		select {
		case <-lc.pong:
			wait.Stop()
		case <-wait.C:
			lc.conn.Close()
			return
		case <-lc.conn.Done():
			wait.Stop()
			return
		}
	}
}

// lostDirect: the connection ended; requests waiting on it learn that it
// was lost, the route goes back to the relay, a probe follows soon.
func (c *Controller) lostDirect(d *directState, p *directPath, lc *liveConn) {
	lc.conn.Close()
	lc.mu.Lock()
	lc.dead = true
	for id, ch := range lc.pending {
		ch <- directAnswer{lost: true}
		delete(lc.pending, id)
	}
	lc.mu.Unlock()
	d.mu.Lock()
	if p.live != lc {
		d.mu.Unlock()
		return
	}
	p.live = nil
	p.nextProbe = time.Now().Add(2 * time.Second)
	d.mu.Unlock()
	c.routeChanged(d, p)
}

func (c *Controller) routeChanged(d *directState, p *directPath) {
	d.mu.Lock()
	route := RouteRelay
	if p.live != nil {
		route = RouteDirect
	}
	changed := p.route != route
	p.route = route
	d.mu.Unlock()
	if changed && d.cfg.OnRoute != nil {
		d.cfg.OnRoute(p.machine, route)
	}
}

// noteDirect records in trusted-hosts.json when this controller last
// reached a host directly (at most once an hour).
func (c *Controller) noteDirect(machineID string) {
	if c.e2e == nil || c.e2e.TrustPath == "" {
		return
	}
	now := time.Now().Unix()
	_ = e2e.UpdateTrust(c.e2e.TrustPath, func(t *e2e.Trust) (bool, error) {
		h, ok := t.Hosts[machineID]
		if !ok || now-h.Direct < 3600 {
			return false, nil
		}
		h.Direct = now
		t.Hosts[machineID] = h
		return true, nil
	})
}

// idempotent: requests that may be resent through the relay after the
// direct connection failed with them on the way.
func idempotent(method string) bool {
	switch method {
	case "snapshot", "ping", "agents.list", "agents.plan", "agents.probe", "download", "projects.recent", "profiles.list", "fs.stat":
		return true
	}
	return false
}

// tryDirect sends an inner request on the direct path. fallback means it
// did not (or, for an idempotent request, may be resent through the
// relay); otherwise result and err are the answer.
func (c *Controller) tryDirect(ctx context.Context, machineID string, inner e2e.Request) (result json.RawMessage, err error, fallback bool) {
	lc := c.liveConn(machineID)
	if lc == nil {
		return nil, nil, true
	}
	select {
	case <-lc.conn.Done():
		return nil, nil, true
	default:
	}
	ch := make(chan directAnswer, 1)
	lc.mu.Lock()
	if lc.dead {
		lc.mu.Unlock()
		return nil, nil, true
	}
	lc.pending[inner.ID] = ch
	lc.mu.Unlock()
	forget := func() {
		lc.mu.Lock()
		delete(lc.pending, inner.ID)
		lc.mu.Unlock()
	}
	deadline := time.Now().Add(20 * time.Second)
	if d, ok := ctx.Deadline(); ok {
		deadline = d
	}
	body, _ := json.Marshal(direct.Request{Request: inner, Deadline: deadline.UnixMilli()})
	lost := func() (json.RawMessage, error, bool) {
		if idempotent(inner.Method) {
			return nil, nil, true
		}
		m, _ := c.machine(ctx, machineID)
		name := m.Name
		if name == "" {
			name = machineID
		}
		return nil, protocol.Err("connection_lost", "The direct connection to "+name+" dropped with this request on the way; outcome unknown, it is not replayed. Refresh before retrying."), false
	}
	if err := lc.conn.WriteMessage(ctx, direct.KindRequest, body); err != nil {
		forget()
		lc.conn.Close()
		return lost()
	}
	select {
	case a := <-ch:
		if a.lost {
			return lost()
		}
		report(ctx, true, lc.require)
		reportRoute(ctx, RouteDirect, lc.addr)
		if a.resp.Error != nil {
			return nil, a.resp.Error, false
		}
		return a.resp.Result, nil, false
	case <-ctx.Done():
		forget()
		return nil, protocol.Err("timeout", "Request cancelled or timed out; outcome may be unknown"), false
	case <-c.ctx.Done():
		forget()
		return nil, protocol.Err("connection_lost", "Disconnected; outcome may be unknown. Requests are never replayed automatically."), false
	}
}

// openDirectStream connects a terminal stream the host opened on the
// direct path (ticket.Direct) to addr, the address of the direct
// connection that carried terminal.open.
func (c *Controller) openDirectStream(ctx context.Context, machineID, addr, streamID string) (*Terminal, error) {
	if c.e2e == nil || addr == "" {
		return nil, errors.New("no direct address for the stream")
	}
	key, err := c.pinnedKey(machineID)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	conn, err := direct.Dial(ctx, addr, direct.DialConfig{MachineID: machineID, HostKey: key, Identity: c.e2e.Identity, Device: c.device, Stream: streamID})
	if err != nil {
		return nil, err
	}
	return NewDirectTerminal(conn), nil
}
