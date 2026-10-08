package host

// Links (rebuild contract part R, pkg/agentlink): a controller's hesperd
// opens one link per host with agents.link (right observe), a terminal
// stream like the old terminal.open's: through the relay and sealed with
// a secret that travels inside the end-to-end channel, or on the direct
// path as a direct connection of its own. On it the host sends its agent
// events (channel 0) and serves attach channels, each opened by a signed
// agents.attach (right observe for "ro", type for "rw"; shells need the
// shell right) and bridged to the agent's PTY exactly like a local attach
// (internal/ptyhost: the redraw, the size owner rule, slow viewers resynced
// with a fresh redraw).

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"slices"
	"sync"
	"time"

	"github.com/derzierau/hesper/relay/internal/identity"
	"github.com/derzierau/hesper/relay/internal/perf"
	"github.com/derzierau/hesper/relay/pkg/agentlink"
	"github.com/derzierau/hesper/relay/pkg/client"
	"github.com/derzierau/hesper/relay/pkg/devicekey"
	"github.com/derzierau/hesper/relay/pkg/e2e"
	"github.com/derzierau/hesper/relay/pkg/protocol"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Link limits.
const (
	maxLinksPerDevice = 4
	maxChannels       = 256
	chanQueue         = 256 // input chunks queued for one attach
)

// linkWatch is how often a link checks that its device is still approved.
var linkWatch = 5 * time.Second

// terminalManager opens the links of one host connection (the relay's
// or the direct path's). Losing that connection ends its links.
type terminalManager struct {
	ctx   context.Context
	r     *Runner
	wg    sync.WaitGroup
	slots chan struct{}
}

type links struct {
	mu   sync.Mutex
	byID map[string]*hostLink
}

type hostLink struct {
	id, device string
	shells     bool // its caller may see shells
	conn       *agentlink.Conn
	ctx        context.Context
	cancel     context.CancelFunc
	opened     time.Time
	mu         sync.Mutex
	chans      map[uint32]*hostChan
}

type hostChan struct {
	in   chan []byte
	done chan struct{}
	pipe net.Conn
	once sync.Once
}

func (l *links) add(link *hostLink) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.byID == nil {
		l.byID = map[string]*hostLink{}
	}
	var mine []*hostLink
	for _, o := range l.byID {
		if o.device == link.device {
			mine = append(mine, o)
		}
	}
	slices.SortFunc(mine, func(a, b *hostLink) int { return a.opened.Compare(b.opened) })
	for len(mine) >= maxLinksPerDevice {
		mine[0].cancel()
		delete(l.byID, mine[0].id)
		mine = mine[1:]
	}
	l.byID[link.id] = link
}

func (l *links) get(id string) *hostLink {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.byID[id]
}

func (l *links) drop(link *hostLink) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.byID[link.id] == link {
		delete(l.byID, link.id)
	}
}

// Links is the number of open links (tests).
func (s *Service) Links() int {
	s.links.mu.Lock()
	defer s.links.mu.Unlock()
	return len(s.links.byID)
}

// openLink answers agents.link: a ticket for the link's stream.
func (m *terminalManager) openLink(opCtx context.Context, req protocol.Message) (json.RawMessage, error) {
	if req.Deadline == 0 || time.Now().After(time.UnixMilli(req.Deadline)) {
		return nil, protocol.Err("expired", "Link request expired")
	}
	var p struct{}
	if err := params(req.Params, &p); err != nil {
		return nil, err
	}
	svc := m.r.Service
	if svc.Agents == nil {
		return nil, protocol.Err("unsupported", "Host serves no agents")
	}
	if !protocol.ValidID(req.ControllerID) {
		return nil, protocol.Err("invalid_request", "Invalid controller")
	}
	if !ViaE2E(opCtx) {
		// Agent names, tasks and screens never travel in plaintext.
		return nil, protocol.Err(e2e.CodeRequired, "Links are opened only through the end-to-end channel")
	}
	caller, _ := CallerFrom(opCtx)
	select {
	case m.slots <- struct{}{}:
	default:
		return nil, protocol.Err("busy", "Host link limit reached")
	}
	ctx, cancel := context.WithCancel(m.ctx)
	setupTimer := time.AfterFunc(time.Until(time.UnixMilli(req.Deadline)), cancel)
	defer setupTimer.Stop()
	id := identity.Secret()
	var stream *client.Terminal
	var secret []byte
	var err error
	direct := directOf(opCtx)
	var arrival <-chan *client.Terminal
	if direct != nil {
		arrival, err = m.r.Direct.expectStream(id, req.ControllerID)
	} else {
		c := m.r.Credentials
		if m.r.CredentialsPath != "" {
			c, err = client.FreshCredentials(ctx, m.r.CredentialsPath)
			// A postponed renewal still leaves a usable access token until it expires.
			if errors.Is(err, client.ErrRenewalPostponed) && time.Until(c.ExpiresAt) > 0 {
				err = nil
			}
		}
		if err == nil {
			stream, err = client.DialTerminal(ctx, c, id, req.ControllerID)
		}
		if err == nil {
			secret = e2e.NewStreamSecret()
			if err = stream.EnableE2E(secret, id, true); err != nil {
				stream.Close()
			} else if perf.Host.Enabled() {
				stream.Timing = func(seal bool, d time.Duration) {
					if seal {
						perf.Host.Observe(perf.StreamSeal, d)
					} else {
						perf.Host.Observe(perf.StreamOpen, d)
					}
				}
			}
		}
	}
	if err == nil && (!setupTimer.Stop() || ctx.Err() != nil) {
		if stream != nil {
			stream.Close()
		}
		err = protocol.Err("expired", "Link setup timed out")
	}
	if err != nil {
		cancel()
		<-m.slots
		return nil, err
	}
	link := &hostLink{id: id, device: req.ControllerID, shells: svc.AllowShell && caller.Has(devicekey.Shell),
		ctx: ctx, cancel: cancel, opened: time.Now(), chans: map[uint32]*hostChan{}}
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		defer func() { <-m.slots }()
		defer cancel()
		if arrival != nil {
			wait := time.NewTimer(directStreamWait)
			select {
			case stream = <-arrival:
			case <-wait.C:
			case <-ctx.Done():
			}
			wait.Stop()
			m.r.Direct.forgetStream(id)
			if stream == nil {
				select {
				case late := <-arrival:
					late.Close()
				default:
				}
				return
			}
		}
		link.conn = agentlink.New(stream)
		svc.links.add(link)
		defer svc.links.drop(link)
		m.r.serveLink(link)
	}()
	return protocol.JSON(protocol.TerminalTicket{ID: id, E2E: secret, Direct: direct != nil}), nil
}

// serveLink runs a link until it ends: events out, attach input in, and a
// look every few seconds whether its device is still approved.
func (r *Runner) serveLink(link *hostLink) {
	ctx, cancel := link.ctx, link.cancel
	defer cancel()
	defer link.conn.Close()
	defer link.closeAll()
	reg := r.Service.Agents
	var workers sync.WaitGroup
	defer workers.Wait()
	defer cancel()
	workers.Add(5)
	go func() {
		// projects step 1: the shared project state (projects.go)
		defer workers.Done()
		r.Service.serveProjects(ctx, link)
	}()
	go func() {
		// shared history: change hints (sessions.go)
		defer workers.Done()
		r.Service.serveSessions(ctx, link)
	}()
	go func() {
		// Events: the full list, then every change. The subscription comes
		// first, so nothing between the list and it is missed (its first
		// notes repeat the list, harmlessly).
		defer workers.Done()
		defer cancel()
		sub := reg.Subscribe()
		defer reg.Unsubscribe(sub)
		list := []wire.Agent{}
		for _, a := range reg.List() {
			if a.Kind != wire.KindShell || link.shells {
				list = append(list, a)
			}
		}
		if link.conn.WriteEvent(ctx, agentlink.Event{Machine: reg.Machine(), List: list, Full: true}) != nil {
			return
		}
		for {
			notes := sub.Wait(ctx.Done())
			if notes == nil {
				return
			}
			for _, n := range notes {
				e := agentlink.Event{Removed: n.Removed, Reason: n.Reason}
				if n.Agent != nil {
					if n.Agent.Kind == wire.KindShell && !link.shells {
						continue
					}
					e = agentlink.Event{Changed: n.Agent}
				}
				if link.conn.WriteEvent(ctx, e) != nil {
					return
				}
			}
		}
	}()
	go func() {
		defer workers.Done()
		defer cancel()
		for {
			kind, ch, payload, err := link.conn.Read(ctx)
			if err != nil {
				return
			}
			switch kind {
			case agentlink.KindData:
				link.input(ch, payload)
			case agentlink.KindClose:
				link.closeChan(ch, false)
			default:
				return
			}
		}
	}()
	go func() {
		defer workers.Done()
		tick := time.NewTicker(linkWatch)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
			if r.Auth != nil && !r.Auth.approved(link.device) {
				r.Auth.Audit("link.revoked", map[string]any{"device": link.device, "ok": false, "e2e": true, "detail": "device no longer approved"})
				cancel()
				return
			}
		}
	}()
	<-ctx.Done()
}

// approved reports whether a device may keep a link open: approved with
// the observe right, or no enforcement yet.
func (a *Authorizer) approved(device string) bool {
	list, exists, err := a.Store.LoadControllers()
	if err != nil {
		return false
	}
	if !exists && !a.Require {
		return true
	}
	for _, c := range list.Controllers {
		if c.Device == device {
			return slices.Contains(c.Rights, devicekey.Observe)
		}
	}
	return false
}

func (l *hostLink) input(ch uint32, p []byte) {
	l.mu.Lock()
	c := l.chans[ch]
	l.mu.Unlock()
	if c == nil {
		return
	}
	select {
	case c.in <- p:
	case <-c.done:
	default:
		// The agent takes no input this fast: end this attach rather
		// than hold up the link.
		l.closeChan(ch, true)
	}
}

// closeChan ends an attach channel; notify tells the controller.
func (l *hostLink) closeChan(ch uint32, notify bool) {
	l.mu.Lock()
	c := l.chans[ch]
	delete(l.chans, ch)
	l.mu.Unlock()
	if c == nil {
		return
	}
	c.once.Do(func() {
		close(c.done)
		c.pipe.Close()
	})
	if notify {
		l.conn.Write(l.ctx, agentlink.KindClose, ch, nil)
	}
}

func (l *hostLink) closeAll() {
	l.mu.Lock()
	chans := l.chans
	l.chans = map[uint32]*hostChan{}
	l.mu.Unlock()
	for _, c := range chans {
		c.once.Do(func() {
			close(c.done)
			c.pipe.Close()
		})
	}
}

// attach answers agents.attach: the agent's terminal on a channel of the
// caller's link.
func (s *Service) attach(ctx context.Context, m protocol.Message) (json.RawMessage, error) {
	// The attach request travels as the local one (wire.AttachRequest,
	// every option of it), so a remote attach behaves like a local one;
	// mode is repeated outside it for the rights table.
	var p struct {
		Link    string          `json:"link"`
		Ch      uint32          `json:"ch"`
		Mode    string          `json:"mode"`
		Request json.RawMessage `json:"request"`
	}
	if err := params(m.Params, &p); err != nil {
		return nil, err
	}
	var req wire.AttachRequest
	if err := json.Unmarshal(p.Request, &req); err != nil {
		return nil, protocol.Err("invalid_request", "Invalid attach request")
	}
	if req.Mode != p.Mode || p.Mode != wire.ModeRO && p.Mode != wire.ModeRW {
		return nil, protocol.Err("invalid_request", "mode must be ro or rw, the same as the request's")
	}
	if p.Ch == 0 || req.Cols < 0 || req.Rows < 0 || req.Cols > 1000 || req.Rows > 1000 {
		return nil, protocol.Err("invalid_request", "Invalid channel or size")
	}
	link := s.links.get(p.Link)
	if link == nil || link.device != m.ControllerID {
		return nil, protocol.Err("not_found", "No such link")
	}
	a, err := s.agent(ctx, req.Attach)
	if err != nil {
		return nil, err
	}
	if a.Kind == wire.KindShell {
		if err := s.shellAllowed(ctx, m, false); err != nil {
			return nil, err
		}
	}
	term, err := s.Agents.Term(a.ID)
	if err != nil {
		return nil, publicError(err)
	}
	near, far := net.Pipe()
	c := &hostChan{in: make(chan []byte, chanQueue), done: make(chan struct{}), pipe: far}
	link.mu.Lock()
	switch {
	case link.ctx.Err() != nil:
		link.mu.Unlock()
		return nil, protocol.Err("not_found", "No such link")
	case link.chans[p.Ch] != nil || len(link.chans) >= maxChannels:
		link.mu.Unlock()
		return nil, protocol.Err("invalid_request", "Channel in use")
	}
	link.chans[p.Ch] = c
	link.mu.Unlock()
	req.Attach = a.ID
	req.Owner = req.Owner && req.Mode == wire.ModeRW
	go term.Attach(near, bufio.NewReader(near), req)
	go func() {
		// The agent's side to the controller.
		buf := make([]byte, 32<<10)
		for {
			n, err := far.Read(buf)
			if n > 0 {
				if link.conn.Write(link.ctx, agentlink.KindData, p.Ch, buf[:n]) != nil {
					link.cancel()
					return
				}
			}
			if err != nil {
				link.closeChan(p.Ch, true)
				return
			}
		}
	}()
	go func() {
		// The controller's side (input, resizes) to the agent.
		for {
			select {
			case data := <-c.in:
				far.SetWriteDeadline(time.Now().Add(30 * time.Second))
				started := time.Now()
				if _, err := far.Write(data); err != nil {
					link.closeChan(p.Ch, true)
					return
				}
				perf.Host.Since(perf.StreamInput, started)
			case <-c.done:
				return
			}
		}
	}()
	return protocol.JSON(struct{}{}), nil
}
