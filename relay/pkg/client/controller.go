package client

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"maps"
	"sort"
	"sync"
	"time"

	"github.com/derzierau/hesper/relay/pkg/devicekey"
	"github.com/derzierau/hesper/relay/pkg/protocol"
)

type Controller struct {
	link    *Link
	fleet   *fleetLink // set when requests go through a running fleet sync
	device  string     // this controller's relay device ID
	signer  devicekey.Signer
	ctx     context.Context
	cancel  context.CancelFunc
	mu      sync.Mutex
	pending map[string]chan protocol.Message
	updates chan []protocol.Machine
	done    chan struct{}

	// The end-to-end channel (e2e.go): its configuration, one channel per
	// host, and the latest inventory (ready closes with the first one).
	e2e       *E2EConfig
	chanMu    sync.Mutex
	channels  map[string]*channel
	invMu     sync.Mutex
	inventory map[string]protocol.Machine
	ready     chan struct{}

	// The direct path (direct.go), nil until EnableDirect.
	dmu    sync.Mutex
	direct *directState
}

func NewController(ctx context.Context, credentials protocol.Credentials) (*Controller, error) {
	if credentials.Role != "controller" {
		return nil, protocol.Err("forbidden", "Controller credentials required")
	}
	link, err := Dial(ctx, credentials)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	c := &Controller{link: link, device: credentials.DeviceID, ctx: ctx, cancel: cancel, pending: map[string]chan protocol.Message{}, updates: make(chan []protocol.Machine, 1), done: make(chan struct{}), ready: make(chan struct{})}
	go c.read()
	go func() {
		t := time.NewTicker(15 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if link.Ping(ctx) != nil {
					cancel()
					return
				}
			}
		}
	}()
	return c, nil
}
func (c *Controller) read() {
	defer close(c.done)
	defer close(c.updates)
	defer c.cancel()
	defer c.link.Close()
	inventory := map[string]protocol.Machine{}
	for {
		m, err := c.link.Read(c.ctx)
		if err != nil {
			return
		}
		if m.ID != "" {
			c.mu.Lock()
			ch := c.pending[m.ID]
			delete(c.pending, m.ID)
			c.mu.Unlock()
			if ch != nil {
				ch <- m
			}
		}
		if m.Type == "machines" || m.Type == "machine.updated" {
			if m.Type == "machines" {
				inventory = map[string]protocol.Machine{}
				for _, machine := range m.Machines {
					inventory[machine.ID] = machine
				}
			} else if m.Removed {
				delete(inventory, m.MachineID)
			} else if len(m.Machines) == 1 {
				inventory[m.MachineID] = m.Machines[0]
			}
			machines := make([]protocol.Machine, 0, len(inventory))
			for _, machine := range inventory {
				machines = append(machines, machine)
			}
			sort.Slice(machines, func(i, j int) bool { return machines[i].ID < machines[j].ID })
			c.invMu.Lock()
			c.inventory = maps.Clone(inventory)
			c.invMu.Unlock()
			c.kickDirect()
			if m.Type == "machines" {
				select {
				case <-c.ready:
				default:
					close(c.ready)
				}
			}
			select {
			case <-c.updates:
			default:
			}
			select {
			case c.updates <- machines:
			default:
			}
		}
	}
}
func (c *Controller) exchange(ctx context.Context, m protocol.Message) (protocol.Message, error) {
	if c.fleet != nil {
		return c.exchangeFleet(ctx, m)
	}
	var id [16]byte
	rand.Read(id[:])
	m.ID = hex.EncodeToString(id[:])
	ch := make(chan protocol.Message, 1)
	c.mu.Lock()
	if len(c.pending) >= 16 {
		c.mu.Unlock()
		return protocol.Message{}, protocol.Err("busy", "Too many pending requests")
	}
	c.pending[m.ID] = ch
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.pending, m.ID); c.mu.Unlock() }()
	if err := c.link.Send(ctx, m); err != nil {
		return protocol.Message{}, err
	}
	select {
	case result := <-ch:
		if result.Error != nil {
			return result, result.Error
		}
		return result, nil
	case <-ctx.Done():
		return protocol.Message{}, protocol.Err("timeout", "Request cancelled or timed out; outcome may be unknown")
	case <-c.ctx.Done():
		return protocol.Message{}, protocol.Err("connection_lost", "Disconnected; outcome may be unknown. Requests are never replayed automatically.")
	}
}
func (c *Controller) Machines(ctx context.Context) ([]protocol.Machine, error) {
	m, err := c.exchange(ctx, protocol.Message{Type: "list"})
	return m.Machines, err
}

// SetSigner makes Request sign every request but snapshot with this
// device's keys (Part K). Without a signer requests go unsigned, which only
// hosts that do not enforce device keys yet accept.
func (c *Controller) SetSigner(s devicekey.Signer) { c.signer = s }

// DeviceID is this controller's relay device ID.
func (c *Controller) DeviceID() string { return c.device }

type signingKey struct{}

// WithSigning sets how requests made with ctx are signed: opts.Strong
// signs with the strong (Touch ID) key also for methods that do not need
// it, opts.Reason is the Touch ID prompt. shell.open and shell.close always
// use the strong key.
func WithSigning(ctx context.Context, opts devicekey.Options) context.Context {
	return context.WithValue(ctx, signingKey{}, opts)
}

// Request sends one request. Params are normalized (devicekey.NormalizeParams)
// and, with a signer, signed in this process, so a strong-key Touch ID
// prompt appears for the program the user is using. Through a fleet sync
// the signature travels with the request; the fleet only forwards it.
func (c *Controller) Request(ctx context.Context, machineID, method string, params any) (json.RawMessage, error) {
	raw, ok := params.(json.RawMessage)
	if !ok {
		data, err := json.Marshal(params)
		if err != nil {
			return nil, protocol.Err("invalid_request", "Parameters are not JSON")
		}
		raw = data
	}
	normalized, err := devicekey.NormalizeParams(raw)
	if err != nil {
		return nil, protocol.Err("invalid_request", err.Error())
	}
	var auth *protocol.Auth
	if c.signer != nil && !devicekey.Unsigned(method) {
		opts, _ := ctx.Value(signingKey{}).(devicekey.Options)
		opts.Strong = opts.Strong || devicekey.NeedsStrong(method, normalized)
		if opts.Strong && opts.Reason == "" {
			opts.Reason = "allow " + method + " on another machine"
		}
		if auth, err = devicekey.SignRequest(c.signer, c.device, machineID, method, normalized, opts, time.Now()); err != nil {
			return nil, protocol.Err("unsigned", "Could not sign the request with this device's key: "+err.Error())
		}
	}
	return c.Forward(ctx, machineID, method, normalized, auth)
}

// Forward sends a request exactly as given, with the caller's signature
// (or none). The fleet sync's control socket uses it: it never signs for
// the commands it serves. With EnableE2E it travels inside the end-to-end
// channel when the host has one (e2e.go).
func (c *Controller) Forward(ctx context.Context, machineID, method string, params json.RawMessage, auth *protocol.Auth) (json.RawMessage, error) {
	report(ctx, false, false)
	if c.fleet == nil && c.e2e != nil && method != "devices.request" {
		result, handled, err := c.forwardE2E(ctx, machineID, method, params, auth)
		if handled {
			return result, err
		}
	} else if c.fleet == nil && requiresE2E(ctx) {
		return nil, protocol.Err("e2e_required", "This controller has no end-to-end channel configured")
	}
	m, err := c.exchange(ctx, protocol.Message{Type: "request", MachineID: machineID, Method: method, Params: params, Auth: auth})
	if c.fleet != nil {
		report(ctx, m.E2E, false)
		reportRoute(ctx, m.Route, m.DirectAddr)
		if err == nil && requiresE2E(ctx) && !m.E2E {
			// An older fleet sync forwarded it in plaintext.
			return nil, protocol.Err("e2e_required", "The fleet sync sent this request without the end-to-end channel; restart hesperd after updating")
		}
	}
	return m.Result, err
}
func (c *Controller) Updates() <-chan []protocol.Machine { return c.updates }
func (c *Controller) Close() {
	c.cancel()
	if c.fleet != nil {
		c.fleet.conn.Close()
	} else {
		c.link.Close()
	}
	<-c.done
}
