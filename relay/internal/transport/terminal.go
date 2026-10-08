package transport

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/derzierau/hesper/relay/internal/identity"
	"github.com/derzierau/hesper/relay/pkg/protocol"
)

// TerminalBroker bridges dedicated sockets. Each direction holds at most one
// 32 KiB application frame; blocking writes propagate pressure to the sender.
// Terminal traffic never enters the inventory/RPC queue or database.
type TerminalBroker struct {
	mu      sync.Mutex
	streams map[string]*terminalPair
	closed  bool
}
type terminalPair struct {
	host                 identity.Device
	controller           string
	hostConn, clientConn *websocket.Conn
	ctx                  context.Context
	cancel               context.CancelFunc
	paired               chan struct{}
}

func newTerminalBroker() *TerminalBroker { return &TerminalBroker{streams: map[string]*terminalPair{}} }
func (b *TerminalBroker) Revoke(id string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, p := range b.streams {
		if p.host.ID == id || p.controller == id {
			p.cancel()
		}
	}
}
func (b *TerminalBroker) CloseDenied(allowed func(string) bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, p := range b.streams {
		if !allowed(p.host.Owner) {
			p.cancel()
		}
	}
}
func (b *TerminalBroker) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	for _, p := range b.streams {
		p.cancel()
	}
}
func (s *Server) Revoke(id string) { s.Hub.Revoke(id); s.Terminals.Revoke(id) }

var streamID = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

func (s *Server) terminal(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Origin") != "" {
		reply(w, 403, protocol.Err("forbidden", "Native client required"))
		return
	}
	if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		reply(w, 401, protocol.Err("unauthorized", "Bearer credential required"))
		return
	}
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	d, err := s.Repository.Authenticate(r.Context(), token)
	if err != nil || (s.Policy != nil && (!s.Policy.Allowed(d.Owner) || d.CredentialExpiresAt.IsZero())) {
		reply(w, 401, protocol.Err("unauthorized", "Invalid credential"))
		return
	}
	// wire names: kept as "X-Ghosty-Stream" and "X-Ghosty-Controller" until
	// the next relay deploy.
	id := r.Header.Get("X-Ghosty-Stream")
	controller := r.Header.Get("X-Ghosty-Controller")
	if !streamID.MatchString(id) {
		reply(w, 400, protocol.Err("invalid_request", "Invalid stream ID"))
		return
	}
	b := s.Terminals
	b.mu.Lock()
	// Recheck under the same lock used by revocation: no socket can appear after
	// a committed revoke without checking the repository again.
	_, err = s.Repository.Authenticate(r.Context(), token)
	if err != nil || b.closed || (s.Policy != nil && !s.Policy.Allowed(d.Owner)) {
		b.mu.Unlock()
		reply(w, 401, protocol.Err("unauthorized", "Unavailable"))
		return
	}
	var p *terminalPair
	if d.Role == identity.Host {
		devices, e := s.Repository.Devices(r.Context(), d.Owner)
		valid := false
		for _, v := range devices {
			if v.ID == controller && v.Owner == d.Owner && v.Role == identity.Controller && !v.Revoked {
				valid = true
			}
		}
		count := 0
		for _, v := range b.streams {
			if v.host.ID == d.ID {
				count++
			}
		}
		if e != nil || !valid || b.streams[id] != nil || len(b.streams) >= 32 || count >= 4 {
			b.mu.Unlock()
			reply(w, 403, protocol.Err("unavailable", "Terminal registration rejected"))
			return
		}
		ctx, cancel := context.WithCancel(context.Background())
		p = &terminalPair{host: d, controller: controller, ctx: ctx, cancel: cancel, paired: make(chan struct{})}
		b.streams[id] = p
	} else {
		p = b.streams[id]
		if d.Role != identity.Controller || p == nil || p.host.Owner != d.Owner || p.controller != d.ID || p.clientConn != nil || p.ctx.Err() != nil {
			b.mu.Unlock()
			reply(w, 403, protocol.Err("unavailable", "Terminal unavailable"))
			return
		}
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{protocol.TerminalProtocol}})
	if err != nil {
		if d.Role == identity.Host {
			delete(b.streams, id)
			p.cancel()
		}
		b.mu.Unlock()
		return
	}
	conn.SetReadLimit(protocol.TerminalChunk)
	if conn.Subprotocol() != protocol.TerminalProtocol {
		if d.Role == identity.Host {
			delete(b.streams, id)
			p.cancel()
		}
		b.mu.Unlock()
		conn.CloseNow()
		return
	}
	if d.Role == identity.Host {
		p.hostConn = conn
	} else {
		p.clientConn = conn
	}
	// Send registration before releasing the pair to its bridge writer.
	writeCtx, stop := context.WithTimeout(p.ctx, 5*time.Second)
	err = conn.Write(writeCtx, websocket.MessageText, protocol.JSON(protocol.TerminalControl{Type: "ready"}))
	stop()
	if err != nil {
		p.cancel()
	}
	if d.Role == identity.Controller {
		close(p.paired)
	}
	b.mu.Unlock()
	defer conn.CloseNow()
	expiry := time.NewTimer(24 * time.Hour)
	if !d.CredentialExpiresAt.IsZero() {
		expiry.Reset(max(time.Until(d.CredentialExpiresAt), 0))
	}
	defer expiry.Stop()
	// Repository checks also cover callers that revoke directly, independent of
	// the HTTP administration and GitHub token-reuse callbacks.
	check := time.NewTicker(time.Second)
	defer check.Stop()
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer p.cancel()
		if d.Role == identity.Host {
			s.bridge(p)
		} else {
			<-p.ctx.Done()
		}
	}()
	defer func() {
		p.cancel()
		conn.CloseNow()
		<-done
		if d.Role == identity.Host {
			b.mu.Lock()
			delete(b.streams, id)
			b.mu.Unlock()
		}
	}()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-p.ctx.Done():
			return
		case <-expiry.C:
			return
		case <-check.C:
			if _, e := s.Repository.Authenticate(p.ctx, token); e != nil {
				return
			}
			if s.Policy != nil && !s.Policy.Allowed(d.Owner) {
				return
			}
		}
	}
}
func (s *Server) bridge(p *terminalPair) {
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	select {
	case <-p.paired:
	case <-timer.C:
		return
	case <-p.ctx.Done():
		return
	}
	defer p.clientConn.CloseNow()
	defer p.hostConn.CloseNow()
	var workers sync.WaitGroup
	forward := func(src, dst *websocket.Conn, fromHost bool) {
		defer workers.Done()
		defer p.cancel()
		for {
			kind, data, err := src.Read(p.ctx)
			if err != nil {
				return
			}
			if kind == websocket.MessageText {
				var control protocol.TerminalControl
				if fromHost || len(data) > 256 || json.Unmarshal(data, &control) != nil || control.Type != "resize" || control.Columns < 2 || control.Rows < 2 || control.Columns > 1000 || control.Rows > 1000 {
					return
				}
			} else if kind != websocket.MessageBinary {
				return
			}
			ctx, cancel := context.WithTimeout(p.ctx, 10*time.Second)
			err = dst.Write(ctx, kind, data)
			cancel()
			if err != nil {
				return
			}
		}
	}
	workers.Add(2)
	go forward(p.hostConn, p.clientConn, true)
	go forward(p.clientConn, p.hostConn, false)
	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()
	defer func() { p.cancel(); p.hostConn.CloseNow(); p.clientConn.CloseNow(); workers.Wait() }()
	for {
		select {
		case <-p.ctx.Done():
			return
		case <-ping.C:
			ctx, cancel := context.WithTimeout(p.ctx, 10*time.Second)
			err := p.hostConn.Ping(ctx)
			if err == nil {
				err = p.clientConn.Ping(ctx)
			}
			cancel()
			if err != nil {
				return
			}
		}
	}
}
