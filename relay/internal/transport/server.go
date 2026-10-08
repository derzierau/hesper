// Package transport adapts HTTP and WebSockets to enrollment and relay services.
package transport

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/derzierau/hesper/relay/internal/auth"
	"github.com/derzierau/hesper/relay/internal/identity"
	"github.com/derzierau/hesper/relay/internal/relay"
	"github.com/derzierau/hesper/relay/pkg/protocol"
)

type Server struct {
	Terminals  *TerminalBroker
	Auth       *auth.HTTP
	Policy     auth.AccessPolicy
	Repository identity.Repository
	Hub        *relay.Hub
	mu         sync.Mutex
	pairStart  time.Time
	pairCount  int
	// Enrollment and revocation are serialized with registration so a credential
	// cannot become live in the hub after its persistent revocation committed.
	enrollment sync.Mutex
}

func New(repository identity.Repository, hub *relay.Hub) *Server {
	return &Server{Repository: repository, Hub: hub, Terminals: newTerminalBroker()}
}
func reply(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}
func decode(w http.ResponseWriter, r *http.Request, value any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(value); err != nil {
		return protocol.Err("invalid_request", "Invalid JSON request")
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return protocol.Err("invalid_request", "Expected one JSON object")
	}
	return nil
}
func (s *Server) PublicHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		reply(w, 200, map[string]any{"ok": true, "protocol": protocol.Version})
	})
	if s.Auth != nil {
		handler := s.Auth.Handler()
		mux.Handle("/auth/", handler)
		mux.Handle("/v1/auth/", handler)
	} else {
		mux.HandleFunc("POST /v1/pair", s.pair)
	}
	mux.HandleFunc("GET /v1/connect", s.connect)
	mux.HandleFunc("GET /v1/terminal", s.terminal)
	return mux
}
func (s *Server) pair(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Origin") != "" {
		reply(w, 403, protocol.Err("forbidden", "Native client required"))
		return
	}
	s.mu.Lock()
	if time.Since(s.pairStart) > time.Minute {
		s.pairStart = time.Now()
		s.pairCount = 0
	}
	s.pairCount++
	allowed := s.pairCount <= 30
	s.mu.Unlock()
	if !allowed {
		reply(w, 429, protocol.Err("rate_limit", "Pairing rate exceeded"))
		return
	}
	var req protocol.PairRequest
	if err := decode(w, r, &req); err != nil {
		reply(w, 400, err)
		return
	}
	s.enrollment.Lock()
	defer s.enrollment.Unlock()
	d, token, err := s.Repository.Redeem(r.Context(), req.Invitation, req.Name)
	if err != nil {
		reply(w, 401, protocol.PublicError(err))
		return
	}
	s.Hub.Register(d)
	reply(w, 200, protocol.Credentials{DeviceID: d.ID, Role: string(d.Role), Token: token})
}

type wsPeer struct {
	queue  chan protocol.Message
	cancel context.CancelFunc
}

func (p *wsPeer) Send(m protocol.Message) bool {
	m.Version = protocol.Version
	select {
	case p.queue <- m:
		return true
	default:
		p.cancel()
		return false
	}
}
func (p *wsPeer) Close() { p.cancel() }
func (s *Server) connect(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Origin") != "" {
		reply(w, 403, protocol.Err("forbidden", "Native client required"))
		return
	}
	authorization := r.Header.Get("Authorization")
	if !strings.HasPrefix(authorization, "Bearer ") {
		reply(w, 401, protocol.Err("unauthorized", "Bearer credential required"))
		return
	}
	token := strings.TrimPrefix(authorization, "Bearer ")
	d, err := s.Repository.Authenticate(r.Context(), token)
	if err != nil || (s.Policy != nil && (!s.Policy.Allowed(d.Owner) || d.CredentialExpiresAt.IsZero())) {
		reply(w, 401, protocol.Err("unauthorized", "Invalid credential"))
		return
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{protocol.Subprotocol}})
	if err != nil {
		return
	}
	defer conn.CloseNow()
	if conn.Subprotocol() != protocol.Subprotocol {
		conn.Close(websocket.StatusPolicyViolation, "ghosty.v2 required")
		return
	}
	conn.SetReadLimit(protocol.MaxMessageBytes)
	ctx, cancel := context.WithCancel(r.Context())
	if !d.CredentialExpiresAt.IsZero() {
		cancel()
		ctx, cancel = context.WithDeadline(r.Context(), d.CredentialExpiresAt)
	}
	defer cancel()
	peer := &wsPeer{queue: make(chan protocol.Message, 16), cancel: cancel}
	c, err := s.Hub.Connect(d, peer)
	if err != nil {
		return
	}
	defer s.Hub.Disconnect(c)
	var workers sync.WaitGroup
	workers.Add(2)
	go func() {
		defer workers.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case m := <-peer.queue:
				writeCtx, stop := context.WithTimeout(ctx, 10*time.Second)
				data, err := json.Marshal(m)
				if err == nil && len(data) > protocol.MaxMessageBytes {
					err = protocol.Err("too_large", "Message limit exceeded")
				}
				if err == nil {
					err = conn.Write(writeCtx, websocket.MessageText, data)
				}
				stop()
				if err != nil {
					cancel()
					return
				}
			}
		}
	}()
	go func() {
		defer workers.Done()
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			pingCtx, stop := context.WithTimeout(ctx, 10*time.Second)
			err := conn.Ping(pingCtx)
			stop()
			if err != nil {
				cancel()
				return
			}
		}
	}()
	defer func() { cancel(); workers.Wait() }()
	start, count := time.Now(), 0
	for {
		var m protocol.Message
		if err := wsjson.Read(ctx, conn, &m); err != nil {
			return
		}
		if time.Since(start) >= time.Second {
			start, count = time.Now(), 0
		}
		count++
		if count > 60 {
			return
		}
		if err := s.Hub.Handle(c, m); err != nil {
			return
		}
	}
}

// AdminHandler is served ONLY on a mode-0600 Unix socket, never on public TCP.
func (s *Server) AdminHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/invitations", func(w http.ResponseWriter, r *http.Request) {
		if s.Auth != nil {
			reply(w, 403, protocol.Err("forbidden", "Use GitHub login"))
			return
		}
		var req struct {
			Owner string        `json:"owner"`
			Role  identity.Role `json:"role"`
		}
		if err := decode(w, r, &req); err != nil {
			reply(w, 400, err)
			return
		}
		expires := time.Now().Add(10 * time.Minute)
		token, err := s.Repository.Invite(r.Context(), identity.Invitation{Owner: req.Owner, Role: req.Role, Expires: expires})
		if err != nil {
			reply(w, 400, protocol.PublicError(err))
			return
		}
		reply(w, 201, map[string]any{"invitation": token, "expires": expires})
	})
	mux.HandleFunc("GET /v1/devices", func(w http.ResponseWriter, r *http.Request) {
		devices, err := s.Repository.Devices(r.Context(), "")
		if err != nil {
			reply(w, 500, protocol.PublicError(err))
			return
		}
		reply(w, 200, devices)
	})
	mux.HandleFunc("DELETE /v1/devices/{id}", func(w http.ResponseWriter, r *http.Request) {
		s.enrollment.Lock()
		defer s.enrollment.Unlock()
		id := r.PathValue("id")
		if err := s.Repository.Revoke(r.Context(), id); err != nil {
			reply(w, 404, protocol.PublicError(err))
			return
		}
		s.Revoke(id)
		reply(w, 200, map[string]bool{"revoked": true})
	})
	return mux
}
