package host

// The host's side of the direct path (pkg/direct, docs/direct-path.md):
// a TCP listener on each private address of this machine's interfaces,
// never on a wildcard or public address. Its addresses are handed out only
// inside the end-to-end channel (direct.offer, to approved controllers
// whose channel key is bound to their device key), together with a token
// the controller's handshake must present. A connection is accepted only
// after the Noise handshake proved a controller static key bound to an
// approved device key; everything else is closed without an answer. On an
// accepted connection the controller sends the same part K-signed inner
// requests as through the relay's channel; they run here exactly like
// those (perform: signature, rights, nonces, audit with route "direct"),
// serialized with the relay's, and share the channel's memory of inner
// request IDs, so a request resent through the relay never runs twice.

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"slices"
	"sync"
	"time"

	"github.com/derzierau/hesper/relay/pkg/client"
	"github.com/derzierau/hesper/relay/pkg/devicekey"
	"github.com/derzierau/hesper/relay/pkg/direct"
	"github.com/derzierau/hesper/relay/pkg/e2e"
	"github.com/derzierau/hesper/relay/pkg/protocol"
)

// Direct path limits.
const (
	// directTokenTTL: how long an offer's token opens a connection.
	directTokenTTL = 10 * time.Minute
	// directOfferFresh: connections of a device stay open while it fetched
	// an offer through the relay within this time (controllers refresh
	// every 10 minutes), so a device the relay no longer serves loses the
	// direct path too.
	directOfferFresh = 30 * time.Minute
	// directStreamWait: how long a terminal.open on the direct path waits
	// for its stream connection.
	directStreamWait = 15 * time.Second
	directHandshake  = 3 * time.Second
	// At most this many handshakes at once, connections in all and per
	// device, and queued requests per connection.
	directMaxHandshakes = 16
	directMaxConns      = 64
	directMaxPerDevice  = 16
	directQueue         = 16
	// Request deadlines: the controller's, at most directMaxDeadline.
	directDefaultDeadline = 20 * time.Second
	directMaxDeadline     = 30 * time.Second
	// Per peer address: at most directPeerBurst connections per 10 s, and
	// after directPeerFails failed handshakes in a minute it is ignored
	// for a minute.
	directPeerBurst = 20
	directPeerFails = 5
)

// Direct is the host's listener for the direct path. Start runs it.
type Direct struct {
	E2E *E2E // static key, approved devices, inner request IDs, audit
	// Port (hesperd serve --direct-port): 0 picks one at random and uses it
	// on every address where it is free.
	Port int
	// Addrs lists the addresses to listen on (default direct.LocalAddrs:
	// private addresses of this machine's interfaces). Tests use loopback.
	Addrs func() []netip.Addr
	// Reason (optional) says why the path is off, for offers (e.g. the
	// firewall check of hesperd).
	Reason string
	Logger *slog.Logger
	Now    func() time.Time

	mu        sync.Mutex
	r         *Runner
	ctx       context.Context
	cancel    context.CancelFunc
	terminals *terminalManager
	listeners map[netip.Addr]net.Listener
	port      int
	tokens    map[string][]directToken // device → tokens of its recent offers
	offered   map[string]time.Time     // device → its last offer
	conns     map[*directConn]bool
	streams   map[string]*directStream // terminal ticket ID → waiting stream
	hellos    map[string]time.Time     // ephemeral keys seen (replayed handshakes)
	peers     map[netip.Addr]*directPeer
	slots     chan struct{}
	wg        sync.WaitGroup
}

type directToken struct {
	value   []byte
	expires time.Time
}

type directConn struct {
	conn         *direct.Conn
	device, name string
	stream       bool
	remote       string
}

type directStream struct {
	device  string
	ch      chan *client.Terminal
	expires time.Time
}

type directPeer struct {
	window   time.Time
	count    int
	fails    int
	failedAt time.Time
	banned   time.Time
	audited  time.Time
}

func (d *Direct) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

func (d *Direct) logger() *slog.Logger {
	if d.Logger != nil {
		return d.Logger
	}
	return slog.Default()
}

func (d *Direct) addrs() []netip.Addr {
	if d.Addrs != nil {
		return d.Addrs()
	}
	return direct.LocalAddrs()
}

// Start listens and serves until ctx ends or Stop. Requests run through r.
func (d *Direct) Start(ctx context.Context, r *Runner) error {
	if d == nil {
		return nil
	}
	if d.E2E == nil || d.E2E.Key == nil || d.E2E.Auth == nil || d.E2E.Auth.MachineID == "" {
		return errors.New("the direct path needs the end-to-end channel and device keys")
	}
	r.init()
	d.mu.Lock()
	if d.ctx != nil {
		d.mu.Unlock()
		return nil
	}
	d.ctx, d.cancel = context.WithCancel(ctx)
	d.r = r
	d.terminals = &terminalManager{ctx: d.ctx, r: r, slots: r.termSlots}
	d.listeners, d.tokens, d.offered = map[netip.Addr]net.Listener{}, map[string][]directToken{}, map[string]time.Time{}
	d.conns, d.streams, d.hellos, d.peers = map[*directConn]bool{}, map[string]*directStream{}, map[string]time.Time{}, map[netip.Addr]*directPeer{}
	d.slots = make(chan struct{}, directMaxHandshakes)
	ctx = d.ctx
	d.mu.Unlock()
	d.refresh()
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		refresh := time.NewTicker(10 * time.Second)
		defer refresh.Stop()
		sweep := time.NewTicker(5 * time.Second)
		defer sweep.Stop()
		for {
			select {
			case <-ctx.Done():
				d.shutdown()
				return
			case <-refresh.C:
				d.refresh()
			case <-sweep.C:
				d.sweep()
			}
		}
	}()
	return nil
}

// Stop closes the listeners and every direct connection and waits for
// them (controllers fall back to the relay). Start may run again.
func (d *Direct) Stop() {
	if d == nil {
		return
	}
	d.mu.Lock()
	cancel, terminals := d.cancel, d.terminals
	d.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	d.wg.Wait()
	if terminals != nil {
		terminals.wg.Wait()
	}
	d.mu.Lock()
	d.ctx, d.cancel = nil, nil
	d.mu.Unlock()
}

func (d *Direct) shutdown() {
	d.mu.Lock()
	defer d.mu.Unlock()
	for addr, l := range d.listeners {
		l.Close()
		delete(d.listeners, addr)
	}
	for c := range d.conns {
		c.conn.Close()
	}
}

// Listening reports whether the host listens on at least one address.
func (d *Direct) Listening() bool {
	if d == nil {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.listeners) > 0
}

// refresh follows the interfaces: new private addresses get a listener,
// vanished ones lose theirs (a Mac moving between networks).
func (d *Direct) refresh() {
	want := d.addrs()
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.ctx == nil || d.ctx.Err() != nil {
		return
	}
	for addr, l := range d.listeners {
		if !slices.Contains(want, addr) {
			l.Close()
			delete(d.listeners, addr)
		}
	}
	for _, addr := range want {
		if _, ok := d.listeners[addr]; ok {
			continue
		}
		port := d.Port
		if port == 0 {
			port = d.port
		}
		l, err := net.Listen("tcp", netip.AddrPortFrom(addr, uint16(port)).String())
		if err != nil && d.Port == 0 && port != 0 {
			l, err = net.Listen("tcp", netip.AddrPortFrom(addr, 0).String())
		}
		if err != nil {
			d.logger().Warn("direct path: cannot listen", "address", addr, "error", err)
			continue
		}
		if d.port == 0 {
			d.port = l.Addr().(*net.TCPAddr).Port
		}
		d.listeners[addr] = l
		d.logger().Info("direct path: listening", "address", l.Addr().String())
		d.wg.Add(1)
		go d.accept(d.ctx, l)
	}
}

// Offer answers direct.offer for an approved device that asked through
// the relay's channel: the listening addresses and a fresh token.
func (d *Direct) Offer(device string) direct.Offer {
	offer := direct.Offer{V: direct.Version, Addrs: []string{}}
	if d == nil {
		offer.Reason = "off"
		return offer
	}
	now := d.now()
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.ctx == nil || len(d.listeners) == 0 {
		offer.Reason = d.Reason
		if offer.Reason == "" {
			offer.Reason = "not listening"
		}
		return offer
	}
	var addrs []netip.Addr
	ports := map[netip.Addr]uint16{}
	for addr, l := range d.listeners {
		plain := addr.WithZone("")
		addrs = append(addrs, plain)
		ports[plain] = uint16(l.Addr().(*net.TCPAddr).Port)
	}
	direct.SortAddrs(addrs)
	for _, addr := range addrs {
		offer.Addrs = append(offer.Addrs, netip.AddrPortFrom(addr, ports[addr]).String())
	}
	token := make([]byte, 16)
	rand.Read(token)
	tokens := slices.DeleteFunc(d.tokens[device], func(t directToken) bool { return now.After(t.expires) })
	tokens = append(tokens, directToken{value: token, expires: now.Add(directTokenTTL)})
	if len(tokens) > 4 {
		tokens = tokens[len(tokens)-4:]
	}
	d.tokens[device] = tokens
	d.offered[device] = now
	offer.Token, offer.Expires = token, now.Add(directTokenTTL).UnixMilli()
	return offer
}

// offer is direct.offer: only inside the relay's channel (the relay must
// not learn the addresses, and asking through it is how a controller shows
// it is still enrolled there), only for an approved device with a bound
// channel key.
func (r *Runner) offer(via *hostSession) (json.RawMessage, error) {
	if via == nil {
		return nil, protocol.Err(e2e.CodeRequired, "direct.offer is answered only inside the end-to-end channel")
	}
	if r.Direct == nil {
		return protocol.JSON(direct.Offer{V: direct.Version, Addrs: []string{}, Reason: "off"}), nil
	}
	if !via.bound || r.Auth == nil {
		return nil, protocol.Err("forbidden", "The direct path is only for approved devices")
	}
	list, _, err := r.Auth.Store.LoadControllers()
	if err != nil || !slices.ContainsFunc(list.Controllers, func(c Controller) bool { return c.Device == via.device }) {
		return nil, protocol.Err("forbidden", "This device is not approved on this host")
	}
	return protocol.JSON(r.Direct.Offer(via.device)), nil
}

func (d *Direct) accept(ctx context.Context, l net.Listener) {
	defer d.wg.Done()
	for {
		raw, err := l.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) || ctx.Err() != nil {
				return
			}
			time.Sleep(50 * time.Millisecond)
			continue
		}
		ip := netip.Addr{}
		if a, ok := raw.RemoteAddr().(*net.TCPAddr); ok {
			ip = a.AddrPort().Addr().Unmap().WithZone("")
		}
		if !d.admit(ip) {
			raw.Close()
			continue
		}
		select {
		case d.slots <- struct{}{}:
		default:
			raw.Close()
			continue
		}
		d.wg.Add(1)
		go func() {
			defer d.wg.Done()
			d.handshake(ctx, raw, ip)
		}()
	}
}

// admit rate-limits connections per peer address and ignores peers whose
// handshakes keep failing.
func (d *Direct) admit(ip netip.Addr) bool {
	now := d.now()
	d.mu.Lock()
	defer d.mu.Unlock()
	p := d.peers[ip]
	if p == nil {
		if len(d.peers) >= 1024 {
			return false
		}
		p = &directPeer{window: now}
		d.peers[ip] = p
	}
	if now.Before(p.banned) {
		return false
	}
	if now.Sub(p.window) > 10*time.Second {
		p.window, p.count = now, 0
	}
	p.count++
	return p.count <= directPeerBurst
}

// refuse closes a handshake without an answer, counts the failure against
// the peer and audits it (once a minute per peer).
func (d *Direct) refuse(raw net.Conn, ip netip.Addr, device, name, detail string) {
	now := d.now()
	d.mu.Lock()
	p := d.peers[ip]
	audit := true
	if p != nil {
		if now.Sub(p.failedAt) > time.Minute {
			p.fails = 0
		}
		p.fails++
		p.failedAt = now
		if p.fails >= directPeerFails {
			p.banned = now.Add(time.Minute)
		}
		audit = now.Sub(p.audited) > time.Minute
		if audit {
			p.audited = now
		}
	}
	d.mu.Unlock()
	// Closed once the failure counts: a peer that sees the close and dials
	// again at once already meets its ban.
	raw.Close()
	if audit {
		d.E2E.Auth.Audit("direct.refused", map[string]any{"device": device, "name": name, "ok": false, "e2e": true, "route": "direct",
			"detail": detail + " (from " + ip.String() + ")"})
	}
}

func (d *Direct) handshake(ctx context.Context, raw net.Conn, ip netip.Addr) {
	held := true
	release := func() {
		if held {
			held = false
			<-d.slots
		}
	}
	defer release()
	now := d.now()
	raw.SetDeadline(now.Add(directHandshake))
	key := d.E2E.Key
	r, err := direct.ReadHello(raw, key.Bytes(), key.PublicKey().Bytes(), d.E2E.Auth.MachineID)
	if err != nil {
		d.refuse(raw, ip, "", "", "invalid handshake (not a Hesper controller of this host)")
		return
	}
	h := r.Hello
	if !r.Fresh(now) {
		d.refuse(raw, ip, h.Device, "", "handshake timestamp outside ±2 minutes")
		return
	}
	d.mu.Lock()
	for k, until := range d.hellos {
		if now.After(until) {
			delete(d.hellos, k)
		}
	}
	replayed := !d.hellos[string(r.Ephemeral)].IsZero()
	if !replayed && len(d.hellos) < 65536 {
		d.hellos[string(r.Ephemeral)] = now.Add(2 * direct.MaxSkew)
	}
	d.mu.Unlock()
	if replayed {
		d.refuse(raw, ip, h.Device, "", "replayed handshake")
		return
	}
	// The controller's static key must be bound to an approved device key:
	// no unbound keys on the direct path, enforcement or not.
	list, _, err := d.E2E.Auth.Store.LoadControllers()
	if err != nil {
		d.refuse(raw, ip, h.Device, "", "allowlist unreadable")
		return
	}
	var c *Controller
	for i := range list.Controllers {
		if list.Controllers[i].Device == h.Device {
			c = &list.Controllers[i]
		}
	}
	if c == nil {
		d.refuse(raw, ip, h.Device, "", "device not approved")
		return
	}
	deviceKey, _, err := devicekey.ParsePublicKey(c.Key)
	if err != nil || e2e.VerifyBinding(deviceKey, r.PeerStatic, h.Binding) != nil {
		d.refuse(raw, ip, c.Device, c.Name, "end-to-end key not bound to the approved device key")
		return
	}
	d.mu.Lock()
	ok, why := true, ""
	if h.Stream != "" {
		s := d.streams[h.Stream]
		if s == nil || s.device != c.Device || now.After(s.expires) {
			ok, why = false, "unknown terminal stream"
		}
	} else if !slices.ContainsFunc(d.tokens[c.Device], func(t directToken) bool {
		return !now.After(t.expires) && subtle.ConstantTimeCompare(t.value, h.Token) == 1
	}) {
		ok, why = false, "no valid offer token (direct.offer through the relay first)"
	}
	mine := 0
	for o := range d.conns {
		if o.device == c.Device {
			mine++
		}
	}
	if ok && (len(d.conns) >= directMaxConns || mine >= directMaxPerDevice) {
		ok, why = false, "too many direct connections"
	}
	d.mu.Unlock()
	if !ok {
		d.refuse(raw, ip, c.Device, c.Name, why)
		return
	}
	conn, err := r.Accept(direct.Welcome{V: direct.Version, Require: d.E2E.Require})
	if err != nil {
		raw.Close()
		return
	}
	raw.SetDeadline(time.Time{})
	dc := &directConn{conn: conn, device: c.Device, name: c.Name, stream: h.Stream != "", remote: ip.String()}
	d.mu.Lock()
	if d.ctx == nil || d.ctx.Err() != nil {
		d.mu.Unlock()
		conn.Close()
		return
	}
	d.conns[dc] = true
	if p := d.peers[ip]; p != nil {
		p.fails = 0
	}
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		delete(d.conns, dc)
		d.mu.Unlock()
	}()
	release()
	if dc.stream {
		if !d.deliver(h.Stream, c.Device, client.NewDirectTerminal(conn)) {
			conn.Close()
			return
		}
		// The terminal's bridge owns the connection now.
		select {
		case <-conn.Done():
		case <-ctx.Done():
			conn.Close()
		}
		return
	}
	d.E2E.Auth.Audit("direct.session", map[string]any{"device": c.Device, "name": c.Name, "method": "direct", "ok": true, "e2e": true, "route": "direct",
		"detail": "requests from " + dc.remote})
	d.serve(ctx, dc)
}

// serve reads requests from one controller connection and runs them in
// order (one at a time, like the relay's), answering pings at once.
func (d *Direct) serve(ctx context.Context, dc *directConn) {
	defer dc.conn.Close()
	stop := context.AfterFunc(ctx, func() { dc.conn.Close() })
	defer stop()
	queue := make(chan direct.Request, directQueue)
	done := make(chan struct{})
	answer := func(resp e2e.Response) {
		body, _ := json.Marshal(resp)
		if err := dc.conn.WriteMessage(ctx, direct.KindResponse, body); err != nil {
			dc.conn.Close()
		}
	}
	go func() {
		defer close(done)
		for req := range queue {
			answer(d.run(ctx, dc, req))
		}
	}()
	defer func() { close(queue); <-done }()
	for {
		kind, body, err := dc.conn.ReadMessage()
		if err != nil {
			return
		}
		switch kind {
		case direct.KindPing:
			if len(body) > 64 || dc.conn.WriteMessage(ctx, direct.KindPong, body) != nil {
				return
			}
		case direct.KindRequest:
			var req direct.Request
			decoder := json.NewDecoder(bytes.NewReader(body))
			decoder.DisallowUnknownFields()
			if decoder.Decode(&req) != nil {
				return
			}
			select {
			case queue <- req:
			default:
				answer(e2e.Response{ID: req.ID, Error: protocol.Err("busy", "Too many pending requests")})
			}
		default:
			return
		}
	}
}

// run executes one request of the direct path like an inner request of
// the relay's channel.
func (d *Direct) run(ctx context.Context, dc *directConn, req direct.Request) e2e.Response {
	resp := e2e.Response{ID: req.ID}
	fail := func(err error) e2e.Response {
		resp.Error = protocol.PublicError(err)
		return resp
	}
	if req.ID == "" || len(req.ID) > 64 || !protocol.ValidID(req.Method) {
		return fail(protocol.Err("invalid_request", "Invalid request"))
	}
	switch req.Method {
	case e2e.HelloMethod, e2e.FrameMethod, "direct.offer", "devices.request":
		return fail(protocol.Err("invalid_request", req.Method+" is not sent on the direct path"))
	}
	if len(req.Params) == 0 {
		req.Params = json.RawMessage("{}")
	}
	if !json.Valid(req.Params) {
		return fail(protocol.Err("invalid_request", "Invalid parameters"))
	}
	if err := d.E2E.remember(dc.device, dc.name, req.ID, req.Method, "direct"); err != nil {
		return fail(err)
	}
	now := time.Now()
	deadline := time.UnixMilli(req.Deadline)
	if req.Deadline == 0 {
		deadline = now.Add(directDefaultDeadline)
	} else if deadline.After(now.Add(directMaxDeadline)) {
		deadline = now.Add(directMaxDeadline)
	}
	m := protocol.Message{Version: protocol.Version, Type: "request", ID: req.ID, ControllerID: dc.device, Method: req.Method,
		Params: req.Params, Auth: req.Auth, Deadline: deadline.UnixMilli()}
	result, err := d.r.perform(withDirect(ctx, dc), m, d.terminals)
	if err == nil && publishAfter(m.Method) {
		d.r.republish()
	}
	if err != nil {
		return fail(err)
	}
	resp.Result = result
	return resp
}

// expectStream registers a terminal stream that the controller will open
// as a direct connection of its own.
func (d *Direct) expectStream(id, device string) (<-chan *client.Terminal, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.ctx == nil || d.ctx.Err() != nil {
		return nil, protocol.Err("unavailable", "The direct path is closed")
	}
	ch := make(chan *client.Terminal, 1)
	d.streams[id] = &directStream{device: device, ch: ch, expires: d.now().Add(directStreamWait)}
	return ch, nil
}

func (d *Direct) forgetStream(id string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.streams, id)
}

func (d *Direct) deliver(id, device string, t *client.Terminal) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	s := d.streams[id]
	if s == nil || s.device != device {
		return false
	}
	delete(d.streams, id)
	s.ch <- t
	return true
}

// sweep closes connections of devices that are no longer approved or no
// longer fetch offers through the relay, and forgets expired state.
func (d *Direct) sweep() {
	now := d.now()
	approved := map[string]bool{}
	if list, _, err := d.E2E.Auth.Store.LoadControllers(); err == nil {
		for _, c := range list.Controllers {
			approved[c.Device] = true
		}
	}
	type closing struct {
		c      *directConn
		reason string
	}
	var ends []closing
	d.mu.Lock()
	for c := range d.conns {
		switch {
		case !approved[c.device]:
			ends = append(ends, closing{c, "device revoked on this host"})
		case now.Sub(d.offered[c.device]) > directOfferFresh:
			ends = append(ends, closing{c, "no offer through the relay for 30 minutes"})
		}
	}
	for device, tokens := range d.tokens {
		tokens = slices.DeleteFunc(tokens, func(t directToken) bool { return now.After(t.expires) })
		if len(tokens) == 0 {
			delete(d.tokens, device)
		} else {
			d.tokens[device] = tokens
		}
	}
	for ip, p := range d.peers {
		if now.Sub(p.window) > time.Minute && now.After(p.banned) && now.Sub(p.failedAt) > time.Minute {
			delete(d.peers, ip)
		}
	}
	for id, s := range d.streams {
		if now.After(s.expires.Add(time.Minute)) {
			delete(d.streams, id)
		}
	}
	d.mu.Unlock()
	for _, e := range ends {
		e.c.conn.Close()
		d.E2E.Auth.Audit("direct.closed", map[string]any{"device": e.c.device, "name": e.c.name, "ok": true, "e2e": true, "route": "direct", "detail": e.reason})
	}
}

// Conns reports the open direct connections (requests and streams).
func (d *Direct) Conns() int {
	if d == nil {
		return 0
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.conns)
}

// Addresses are the addresses listened on ("ip:port").
func (d *Direct) Addresses() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []string
	for _, l := range d.listeners {
		out = append(out, l.Addr().String())
	}
	slices.Sort(out)
	return out
}

func (c *directConn) String() string { return fmt.Sprintf("%s (%s)", c.name, c.remote) }
