// Package controlsock is the control socket of the process that holds
// this Mac's controller connection to the relay (hesperd's controller
// role). The relay keeps one connection per device, so hesperctl's relay
// commands (pair-host, machines, request, watch) send their requests
// through it (client.DialFleet) instead of replacing it. The commands sign
// in their own process; the socket forwards params and signatures
// unchanged and never signs for a caller.
package controlsock

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/derzierau/hesper/relay/pkg/client"
	"github.com/derzierau/hesper/relay/pkg/protocol"
)

// Tunables and counters.
var (
	controlWait     = 5 * time.Second // how long a request waits for the connection
	controlSlots    = 8               // forwarded requests in flight at once
	controlInterval = 20 * time.Millisecond
	// Requests counts the requests forwarded for other commands.
	Requests atomic.Int64
)

// Server forwards requests of other commands through hesperd's
// current relay connection.
type Server struct {
	deviceID string
	mu       sync.Mutex
	current  *client.Controller
	changed  chan struct{}
	machines []protocol.Machine
	watchers map[chan []protocol.Machine]bool
	// The relay closes a connection that sends more than 60 messages a
	// second or whose 16-message send queue overflows, so forwarded
	// requests are paced and only a few are in flight at once.
	slots chan struct{}
	pace  sync.Mutex
	next  time.Time
}

func New(deviceID string) *Server {
	return &Server{deviceID: deviceID, changed: make(chan struct{}), watchers: map[chan []protocol.Machine]bool{}, slots: make(chan struct{}, controlSlots)}
}

// Set publishes the's relay connection, nil while it reconnects.
func (s *Server) Set(c *client.Controller) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.current = c
	close(s.changed)
	s.changed = make(chan struct{})
}

// Publish hands hesperd's latest inventory to every watcher.
func (s *Server) Publish(machines []protocol.Machine) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.machines = machines
	for w := range s.watchers {
		select {
		case <-w:
		default:
		}
		w <- machines
	}
}

// controller waits up to controlWait for the fleet to be connected.
func (s *Server) controller(ctx context.Context) (*client.Controller, error) {
	timer := time.NewTimer(controlWait)
	defer timer.Stop()
	for {
		s.mu.Lock()
		c, changed := s.current, s.changed
		s.mu.Unlock()
		if c != nil {
			return c, nil
		}
		select {
		case <-changed:
		case <-timer.C:
			return nil, protocol.Err("unavailable", "hesperd is reconnecting to the relay")
		case <-ctx.Done():
			return nil, protocol.Err("unavailable", "hesperd is reconnecting to the relay")
		}
	}
}

// route is how the fleet reaches a host right now ("" while reconnecting).
func (s *Server) route(machineID string) string {
	s.mu.Lock()
	c := s.current
	s.mu.Unlock()
	if c == nil {
		return ""
	}
	return c.Route(machineID)
}

// wait spaces forwarded requests controlInterval apart.
func (s *Server) wait(ctx context.Context) error {
	s.pace.Lock()
	at := s.next
	if now := time.Now(); now.After(at) {
		at = now
	}
	s.next = at.Add(controlInterval)
	s.pace.Unlock()
	timer := time.NewTimer(time.Until(at))
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return protocol.Err("timeout", "Request timed out waiting for the hesperd's connection")
	}
}

// Listen opens the control socket: its directory is created 0700,
// the socket is 0600, a stale socket is replaced, a live one refused.
func Listen(path string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("%s exists and is not a socket", path)
		}
		if conn, err := net.DialTimeout("unix", path, time.Second); err == nil {
			conn.Close()
			return nil, fmt.Errorf("another hesperd serves %s", path)
		}
		if err := os.Remove(path); err != nil {
			return nil, err
		}
	}
	l, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0600); err != nil {
		l.Close()
		return nil, err
	}
	return l, nil
}

func (s *Server) Serve(ctx context.Context, l net.Listener) {
	for {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		// Only this user: the socket is 0600 and the peer is checked too.
		if uid, err := peerUID(conn); err != nil || uid != os.Getuid() {
			conn.Close()
			continue
		}
		go s.handle(ctx, conn)
	}
}

func (s *Server) handle(parent context.Context, conn net.Conn) {
	ctx, cancel := context.WithCancel(parent)
	var workers sync.WaitGroup
	defer workers.Wait()
	defer cancel()
	context.AfterFunc(ctx, func() { conn.Close() })
	var write sync.Mutex
	reply := func(r client.ControlResponse) {
		line, _ := json.Marshal(r)
		write.Lock()
		defer write.Unlock()
		conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		if _, err := conn.Write(append(line, '\n')); err != nil {
			cancel()
		}
	}
	busy := make(chan struct{}, 32)
	lines := bufio.NewScanner(conn)
	lines.Buffer(make([]byte, 64*1024), client.MaxControlLine)
	for lines.Scan() {
		var r client.ControlRequest
		if err := json.Unmarshal(lines.Bytes(), &r); err != nil {
			reply(client.ControlResponse{Error: protocol.Err("invalid_request", "Invalid control request")})
			return
		}
		if r.Op == "watch" {
			workers.Add(1)
			go func() { defer workers.Done(); s.watch(ctx, r.ID, reply) }()
			continue
		}
		select {
		case busy <- struct{}{}:
		default:
			reply(client.ControlResponse{ID: r.ID, Error: protocol.Err("busy", "Too many pending requests")})
			continue
		}
		workers.Add(1)
		go func() {
			defer workers.Done()
			defer func() { <-busy }()
			// The fleet's connection holds the end-to-end channels; the
			// caller learns whether its request went through one, and may
			// insist on it.
			var how client.E2EReport
			requestCtx := client.WithE2EReport(ctx, &how)
			if r.RequireE2E {
				requestCtx = client.RequireE2E(requestCtx)
			}
			if r.Relay {
				requestCtx = client.ViaRelay(requestCtx)
			}
			result, err := s.answer(requestCtx, r)
			response := client.ControlResponse{ID: r.ID, Result: result, E2E: how.Used, Route: how.Route, Direct: how.Addr}
			if err != nil {
				response = client.ControlResponse{ID: r.ID, Error: controlError(err)}
			}
			reply(response)
		}()
	}
}

func controlError(err error) *protocol.Error {
	var fault *protocol.Error
	if errors.As(err, &fault) {
		return fault
	}
	return protocol.Err("connection_lost", "Fleet sync lost the relay; outcome may be unknown: "+err.Error())
}

func (s *Server) answer(ctx context.Context, r client.ControlRequest) (json.RawMessage, error) {
	if r.Op == "hello" {
		s.mu.Lock()
		defer s.mu.Unlock()
		return protocol.JSON(client.ControlHello{DeviceID: s.deviceID, Connected: s.current != nil}), nil
	}
	deadline := time.Now().Add(time.Minute)
	if r.Deadline > 0 {
		deadline = time.UnixMilli(r.Deadline)
	}
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	if r.Op == "request" || r.Op == "inventory" {
		// Callers queue for the connection's slots instead of failing busy.
		select {
		case s.slots <- struct{}{}:
			defer func() { <-s.slots }()
		case <-ctx.Done():
			return nil, protocol.Err("timeout", "Request timed out waiting for the hesperd's connection")
		}
		// Pacing protects the relay connection; requests on the direct
		// path do not use it.
		if !(r.Op == "request" && !r.Relay && s.route(r.MachineID) == client.RouteDirect) {
			if err := s.wait(ctx); err != nil {
				return nil, err
			}
		}
	}
	switch r.Op {
	case "request":
		params := r.Params
		if len(params) == 0 {
			params = json.RawMessage("{}")
		}
		if r.MachineID == "" || r.Method == "" || !json.Valid(params) {
			return nil, protocol.Err("invalid_request", "machineId, method and JSON params are required")
		}
		c, err := s.controller(ctx)
		if err != nil {
			return nil, err
		}
		Requests.Add(1)
		// The caller signed (or did not); the fleet holds no key for it.
		return c.Forward(ctx, r.MachineID, r.Method, params, r.Auth)
	case "routes":
		c, err := s.controller(ctx)
		if err != nil {
			return nil, err
		}
		s.mu.Lock()
		machines := s.machines
		s.mu.Unlock()
		routes := map[string]string{}
		for _, m := range machines {
			if route := c.Route(m.ID); route != "" {
				routes[m.ID] = route
			}
		}
		return protocol.JSON(routes), nil
	case "inventory":
		c, err := s.controller(ctx)
		if err != nil {
			return nil, err
		}
		machines, err := c.Machines(ctx)
		if err != nil {
			return nil, err
		}
		if machines == nil {
			machines = []protocol.Machine{}
		}
		return protocol.JSON(machines), nil
	}
	return nil, protocol.Err("invalid_request", "Unknown op "+r.Op)
}

// watch sends hesperd's inventory now and after every change.
func (s *Server) watch(ctx context.Context, id string, reply func(client.ControlResponse)) {
	updates := make(chan []protocol.Machine, 1)
	s.mu.Lock()
	if s.machines != nil {
		updates <- s.machines
	}
	s.watchers[updates] = true
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.watchers, updates); s.mu.Unlock() }()
	for {
		select {
		case <-ctx.Done():
			return
		case machines := <-updates:
			reply(client.ControlResponse{ID: id, Result: protocol.JSON(machines)})
		}
	}
}
