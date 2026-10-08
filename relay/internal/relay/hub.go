// Package relay routes messages between enrolled devices. It knows neither the
// transport nor the session provider, and never executes a host operation.
package relay

import (
	"encoding/json"
	"sort"
	"sync"
	"time"

	"github.com/derzierau/hesper/relay/internal/identity"
	"github.com/derzierau/hesper/relay/pkg/protocol"
)

// Peer methods must be nonblocking. A transport closes its connection if its
// bounded send queue fills; a reconnect receives a fresh inventory.
type Peer interface {
	Send(protocol.Message) bool
	Close()
}
type Connection struct {
	device   identity.Device
	peer     Peer
	snapshot json.RawMessage
	inflight map[string]bool
}
type request struct {
	host, controller *Connection
	clientID         string
	timer            *time.Timer
}
type Hub struct {
	mu          sync.Mutex
	devices     map[string]identity.Device
	owners      map[string]map[string]identity.Device
	connections map[string]*Connection
	pending     map[string]*request
	timeout     time.Duration
	closed      bool
	allowed     func(string) bool
}

func New(devices []identity.Device, timeout time.Duration) *Hub {
	h := &Hub{devices: map[string]identity.Device{}, owners: map[string]map[string]identity.Device{}, connections: map[string]*Connection{}, pending: map[string]*request{}, timeout: timeout}
	for _, d := range devices {
		h.devices[d.ID] = d
		if h.owners[d.Owner] == nil {
			h.owners[d.Owner] = map[string]identity.Device{}
		}
		h.owners[d.Owner][d.ID] = d
	}
	return h
}
func (h *Hub) Register(device identity.Device) {
	h.mu.Lock()
	defer h.mu.Unlock()
	// Never resurrect a device revoked concurrently with enrollment.
	if old, ok := h.devices[device.ID]; ok && old.Revoked {
		return
	}
	h.devices[device.ID] = device
	if h.owners[device.Owner] == nil {
		h.owners[device.Owner] = map[string]identity.Device{}
	}
	h.owners[device.Owner][device.ID] = device
	h.broadcastDevice(device.ID)
}
func (h *Hub) Connect(device identity.Device, peer Peer) (*Connection, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	known, ok := h.devices[device.ID]
	if (h.allowed != nil && !h.allowed(device.Owner)) || !ok || known.Revoked || known.Owner != device.Owner || known.Role != device.Role || h.closed {
		return nil, protocol.Err("unauthorized", "Device is unavailable")
	}
	if old := h.connections[device.ID]; old != nil {
		h.remove(old)
		old.peer.Close()
	}
	if len(h.connections) >= 256 {
		return nil, protocol.Err("busy", "Relay connection limit reached")
	}
	c := &Connection{device: device, peer: peer, inflight: map[string]bool{}}
	h.connections[device.ID] = c
	peer.Send(protocol.Message{Type: "hello", Result: protocol.JSON(map[string]string{"deviceId": device.ID, "role": string(device.Role)})})
	if device.Role == identity.Controller {
		peer.Send(protocol.Message{Type: "machines", Machines: h.machines(device.Owner)})
	} else {
		h.broadcastDevice(device.ID)
	}
	return c, nil
}
func (h *Hub) Disconnect(c *Connection) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.connections[c.device.ID] != c {
		return
	}
	h.remove(c)
	h.broadcastDevice(c.device.ID)
}
func (h *Hub) Revoke(id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	d, ok := h.devices[id]
	if !ok {
		return
	}
	d.Revoked = true
	h.devices[id] = d
	h.owners[d.Owner][id] = d
	if c := h.connections[id]; c != nil {
		h.remove(c)
		c.peer.Close()
	}
	h.broadcastDevice(id)
}
func (h *Hub) remove(c *Connection) {
	delete(h.connections, c.device.ID)
	for id, r := range h.pending {
		if r.host == c || r.controller == c {
			h.finish(id, nil, protocol.Err("connection_lost", "Connection lost; outcome may be unknown. Refresh before retrying."))
		}
	}
}
func (h *Hub) machines(owner string) []protocol.Machine {
	result := []protocol.Machine{}
	for _, d := range h.owners[owner] {
		if d.Owner != owner || d.Role != identity.Host || d.Revoked {
			continue
		}
		m := protocol.Machine{ID: d.ID, Name: d.Name}
		if c := h.connections[d.ID]; c != nil {
			m.Online = true
			m.Snapshot = c.snapshot
		}
		result = append(result, m)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}
func (h *Hub) broadcastDevice(id string) {
	d, ok := h.devices[id]
	if !ok || d.Role != identity.Host {
		return
	}
	machine := protocol.Machine{ID: d.ID, Name: d.Name}
	if c := h.connections[id]; c != nil {
		machine.Online = true
		machine.Snapshot = c.snapshot
	}
	m := protocol.Message{Type: "machine.updated", MachineID: id, Removed: d.Revoked}
	if !d.Revoked {
		m.Machines = []protocol.Machine{machine}
	}
	for _, device := range h.owners[d.Owner] {
		if device.Role == identity.Controller {
			if c := h.connections[device.ID]; c != nil {
				c.peer.Send(m)
			}
		}
	}
}
func (h *Hub) finish(id string, result json.RawMessage, err *protocol.Error) {
	r := h.pending[id]
	if r == nil {
		return
	}
	r.timer.Stop()
	delete(h.pending, id)
	delete(r.controller.inflight, r.clientID)
	r.controller.peer.Send(protocol.Message{Type: "result", ID: r.clientID, Result: result, Error: err})
}
func (h *Hub) Handle(c *Connection, m protocol.Message) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.connections[c.device.ID] != c || h.closed || (h.allowed != nil && !h.allowed(c.device.Owner)) {
		return protocol.Err("disconnected", "Connection is no longer current")
	}
	if m.Version != protocol.Version {
		return protocol.Err("protocol_version", "Unsupported protocol version")
	}
	if c.device.Role == identity.Host {
		switch m.Type {
		case "snapshot":
			if !json.Valid(m.Result) || len(m.Result) > 256*1024 {
				return protocol.Err("invalid_request", "Invalid or oversized snapshot")
			}
			c.snapshot = append(json.RawMessage(nil), m.Result...)
			h.broadcastDevice(c.device.ID)
		case "result":
			if r := h.pending[m.ID]; r != nil && r.host == c {
				h.finish(m.ID, m.Result, m.Error)
			}
		default:
			return protocol.Err("forbidden", "Hosts may only publish snapshots and results")
		}
		return nil
	}
	if m.Type == "list" {
		c.peer.Send(protocol.Message{Type: "machines", ID: m.ID, Machines: h.machines(c.device.Owner)})
		return nil
	}
	if m.Type != "request" || !protocol.ValidID(m.ID) || !protocol.ValidID(m.MachineID) || !protocol.ValidID(m.Method) || !json.Valid(m.Params) {
		return protocol.Err("invalid_request", "Invalid request envelope")
	}
	if c.inflight[m.ID] {
		return protocol.Err("duplicate_request", "Request ID is already in flight")
	}
	respond := func(err *protocol.Error) { c.peer.Send(protocol.Message{Type: "result", ID: m.ID, Error: err}) }
	if len(c.inflight) >= 16 || len(h.pending) >= 256 {
		respond(protocol.Err("busy", "Too many pending requests"))
		return nil
	}
	host := h.connections[m.MachineID]
	if host == nil || host.device.Role != identity.Host || host.device.Owner != c.device.Owner {
		respond(protocol.Err("unavailable", "Machine is unavailable"))
		return nil
	}
	id := identity.Secret()
	c.inflight[m.ID] = true
	r := &request{host: host, controller: c, clientID: m.ID}
	r.timer = time.AfterFunc(h.timeout, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.finish(id, nil, protocol.Err("timeout", "Request timed out; outcome may be unknown. Refresh before retrying."))
	})
	h.pending[id] = r
	if !host.peer.Send(protocol.Message{Type: "request", ID: id, ControllerID: c.device.ID, Method: m.Method, Params: m.Params, Auth: m.Auth, Deadline: time.Now().Add(h.timeout).UnixMilli()}) {
		h.finish(id, nil, protocol.Err("unavailable", "Machine is unavailable"))
	}
	return nil
}
func (h *Hub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	for id := range h.pending {
		h.finish(id, nil, protocol.Err("shutdown", "Relay shutting down; outcome may be unknown"))
	}
	for _, c := range h.connections {
		c.peer.Close()
	}
	h.connections = map[string]*Connection{}
}

// SetPolicy replaces the gate and immediately removes disallowed connections.
func (h *Hub) SetPolicy(allowed func(string) bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.allowed = allowed
	for _, c := range h.connections {
		if !allowed(c.device.Owner) {
			h.remove(c)
			c.peer.Close()
		}
	}
}
