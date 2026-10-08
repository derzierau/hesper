package client

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sort"
	"sync"
	"time"

	"github.com/derzierau/hesper/relay/pkg/protocol"
)

// The relay keeps one connection per device: a second connection with the
// same controller credentials replaces the first. hesperd's controller
// role therefore serves a local control socket (internal/controlsock), and
// hesperctl's relay commands on the same machine send their requests
// through its connection instead of opening their own.

// ErrNoFleet means no fleet sync for these credentials answers on the
// control socket; callers then connect to the relay themselves.
var ErrNoFleet = errors.New("no fleet sync on the control socket")

// FleetProbe is how long DialFleet waits for a fleet sync to answer.
var FleetProbe = 200 * time.Millisecond

// MaxControlLine bounds one line on the control socket: a relay message
// (at most protocol.MaxMessageBytes) plus its envelope.
const MaxControlLine = 2 << 20

// ControlRequest is one line a caller writes to the control socket. Op is
// hello, request (MachineID, Method, Params), inventory (the machines) or
// watch (the machines now and after every change). Deadline is the
// caller's, in Unix milliseconds.
type ControlRequest struct {
	ID        string          `json:"id"`
	Op        string          `json:"op"`
	MachineID string          `json:"machineId,omitempty"`
	Method    string          `json:"method,omitempty"`
	Params    json.RawMessage `json:"params,omitempty"`
	Deadline  int64           `json:"deadline,omitempty"`
	// Auth is the caller's device-key signature of the request. The caller
	// signs (its own process asks for Touch ID); the fleet forwards Auth and
	// Params unchanged and never signs for a caller.
	Auth *protocol.Auth `json:"auth,omitempty"`
	// RequireE2E: the fleet must send this request through the end-to-end
	// channel or refuse it (client.RequireE2E).
	RequireE2E bool `json:"requireE2e,omitempty"`
	// Relay: send it through the relay, not the direct path (ViaRelay).
	Relay bool `json:"relay,omitempty"`
}

// ControlResponse answers the request with the same ID; watch answers
// repeatedly.
type ControlResponse struct {
	ID     string          `json:"id"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *protocol.Error `json:"error,omitempty"`
	// E2E: the request traveled through the end-to-end channel.
	E2E bool `json:"e2e,omitempty"`
	// Route: "direct" or "relay"; Direct: the direct connection's address
	// (a terminal stream opened on the direct path connects there).
	Route  string `json:"route,omitempty"`
	Direct string `json:"direct,omitempty"`
}

// ControlHello is the result of hello: the fleet's controller device and
// whether it is connected to the relay right now.
type ControlHello struct {
	DeviceID  string `json:"deviceId"`
	Connected bool   `json:"connected"`
}

const watchID = "watch"

type fleetLink struct {
	conn net.Conn
	mu   sync.Mutex
}

func (l *fleetLink) send(ctx context.Context, r ControlRequest) error {
	line, err := json.Marshal(r)
	if err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	deadline := time.Now().Add(10 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	l.conn.SetWriteDeadline(deadline)
	_, err = l.conn.Write(append(line, '\n'))
	return err
}

// DialFleet connects to the fleet sync serving the control socket at path
// for the controller deviceID. It fails with ErrNoFleet when nothing
// answers within FleetProbe or the fleet runs for another device. With
// watch, Updates delivers the machines as the fleet sees them.
func DialFleet(ctx context.Context, path, deviceID string, watch bool) (*Controller, error) {
	dialer := net.Dialer{Timeout: FleetProbe}
	conn, err := dialer.DialContext(ctx, "unix", path)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNoFleet, err)
	}
	link := &fleetLink{conn: conn}
	lines := bufio.NewScanner(conn)
	lines.Buffer(make([]byte, 64*1024), MaxControlLine)
	conn.SetReadDeadline(time.Now().Add(FleetProbe))
	var hello ControlHello
	err = link.send(ctx, ControlRequest{ID: "hello", Op: "hello"})
	if err == nil && !lines.Scan() {
		err = lines.Err()
		if err == nil {
			err = errors.New("closed")
		}
	}
	if err == nil {
		var r ControlResponse
		if err = json.Unmarshal(lines.Bytes(), &r); err == nil && r.Error != nil {
			err = r.Error
		}
		if err == nil {
			err = json.Unmarshal(r.Result, &hello)
		}
	}
	if err == nil && (hello.DeviceID == "" || hello.DeviceID != deviceID) {
		err = errors.New("the fleet sync runs for another device")
	}
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("%w: %v", ErrNoFleet, err)
	}
	conn.SetReadDeadline(time.Time{})
	ctx, cancel := context.WithCancel(ctx)
	c := &Controller{fleet: link, device: deviceID, ctx: ctx, cancel: cancel, pending: map[string]chan protocol.Message{}, updates: make(chan []protocol.Machine, 1), done: make(chan struct{})}
	context.AfterFunc(ctx, func() { conn.Close() })
	go c.readFleet(lines)
	if watch {
		if err := link.send(ctx, ControlRequest{ID: watchID, Op: "watch"}); err != nil {
			c.Close()
			return nil, fmt.Errorf("%w: %v", ErrNoFleet, err)
		}
	}
	return c, nil
}

// ViaFleet reports whether requests go through a fleet sync's connection.
func (c *Controller) ViaFleet() bool { return c.fleet != nil }

func (c *Controller) readFleet(lines *bufio.Scanner) {
	defer close(c.done)
	defer close(c.updates)
	defer c.cancel()
	defer c.fleet.conn.Close()
	for lines.Scan() {
		var r ControlResponse
		if json.Unmarshal(lines.Bytes(), &r) != nil {
			return
		}
		if r.ID == watchID {
			var machines []protocol.Machine
			if r.Error != nil || json.Unmarshal(r.Result, &machines) != nil {
				return
			}
			sort.Slice(machines, func(i, j int) bool { return machines[i].ID < machines[j].ID })
			select {
			case <-c.updates:
			default:
			}
			c.updates <- machines
			continue
		}
		c.mu.Lock()
		ch := c.pending[r.ID]
		delete(c.pending, r.ID)
		c.mu.Unlock()
		if ch != nil {
			ch <- protocol.Message{Type: "result", ID: r.ID, Result: r.Result, Error: r.Error, E2E: r.E2E, Route: r.Route, DirectAddr: r.Direct}
		}
	}
}

func (c *Controller) exchangeFleet(ctx context.Context, m protocol.Message) (protocol.Message, error) {
	var id [16]byte
	rand.Read(id[:])
	r := ControlRequest{ID: hex.EncodeToString(id[:])}
	switch m.Type {
	case "request":
		r.Op, r.MachineID, r.Method, r.Params, r.Auth = "request", m.MachineID, m.Method, m.Params, m.Auth
		r.RequireE2E = requiresE2E(ctx)
		r.Relay = viaRelay(ctx)
	case "list":
		r.Op = "inventory"
	case "routes":
		r.Op = "routes"
	default:
		return protocol.Message{}, protocol.Err("unsupported", "Run this with --direct: the fleet sync does not forward "+m.Type)
	}
	if d, ok := ctx.Deadline(); ok {
		r.Deadline = d.UnixMilli()
	}
	ch := make(chan protocol.Message, 1)
	c.mu.Lock()
	if len(c.pending) >= 16 {
		c.mu.Unlock()
		return protocol.Message{}, protocol.Err("busy", "Too many pending requests")
	}
	c.pending[r.ID] = ch
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.pending, r.ID); c.mu.Unlock() }()
	if err := c.fleet.send(ctx, r); err != nil {
		return protocol.Message{}, protocol.Err("connection_lost", "The fleet sync went away; outcome may be unknown. Requests are never replayed automatically.")
	}
	select {
	case result := <-ch:
		if result.Error != nil {
			return result, result.Error
		}
		if m.Type == "list" {
			if err := json.Unmarshal(result.Result, &result.Machines); err != nil {
				return result, err
			}
		}
		if m.Type == "routes" {
			result.Type = "routes"
		}
		return result, nil
	case <-ctx.Done():
		return protocol.Message{}, protocol.Err("timeout", "Request cancelled or timed out; outcome may be unknown")
	case <-c.ctx.Done():
		return protocol.Message{}, protocol.Err("connection_lost", "The fleet sync went away; outcome may be unknown. Requests are never replayed automatically.")
	}
}

// FleetRoutes asks the fleet sync how it reaches each host ("direct" or
// "relay"); a fleet sync without the direct path answers an error.
func (c *Controller) FleetRoutes(ctx context.Context) (map[string]string, error) {
	if c.fleet == nil {
		return nil, ErrNoFleet
	}
	m, err := c.exchangeFleet(ctx, protocol.Message{Type: "routes"})
	if err != nil {
		return nil, err
	}
	routes := map[string]string{}
	err = json.Unmarshal(m.Result, &routes)
	return routes, err
}
