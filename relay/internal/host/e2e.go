package host

// The host's side of the end-to-end channel (pkg/e2e, contract Part N).
// Runner hands e2e.hello to Hello and opens every "e2e" request with Open:
// the inner request then runs exactly like a plaintext one (part K's
// signature check included) and its answer is sealed with Seal. The relay
// sees neither params nor results.
//
// The host's static key is its transfer key (host.transfer.key), the key
// part K's approval code and the controllers' pins already cover. A
// controller's static key is accepted only with a binding signature by its
// approved device key (controllers.json); before any device is approved
// (no enforcement) an unbound key is accepted too, which still keeps the
// relay from reading the traffic (the controller pinned this host's key).

import (
	"context"
	"crypto/ecdh"
	"encoding/base64"
	"encoding/json"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/derzierau/hesper/relay/pkg/devicekey"
	"github.com/derzierau/hesper/relay/pkg/e2e"
	"github.com/derzierau/hesper/relay/pkg/protocol"
)

// Channel limits.
const (
	// E2EIdle ends a session nobody used for this long; E2ELifetime ends
	// any session (controllers rekey hourly).
	E2EIdle     = 30 * time.Minute
	E2ELifetime = 24 * time.Hour
	// maxSessionsPerDevice and maxSessions bound the sessions held.
	maxSessionsPerDevice = 16
	maxSessions          = 256
	// innerTTL is how long an inner request ID is remembered (a request
	// replayed through a new session is refused).
	innerTTL  = 10 * time.Minute
	maxInners = 65536
)

// E2E holds the host's channel sessions.
type E2E struct {
	Key  *ecdh.PrivateKey // the host's static key (its transfer key)
	Auth *Authorizer      // approved devices, enforcement and the audit log
	// Require (always on in hesperd): plaintext is refused for every
	// request but observing ones (and snapshot, devices.request).
	Require bool
	Now     func() time.Time

	mu       sync.Mutex
	sessions map[string]*hostSession // controller device + "/" + session ID
	hellos   map[string]time.Time    // controller ephemeral keys seen (replayed first messages)
	inners   map[string]time.Time    // controller device + "/" + inner request ID
}

type hostSession struct {
	*e2e.Session
	device, name string
	bound        bool
	last         time.Time
}

type e2eKey struct{}

// e2eCall is what an operation learns about the channel it came through:
// the relay's channel session, or the direct path's connection (which is
// end to end too: the same Noise handshake, over TCP).
type e2eCall struct {
	session string
	direct  *directConn
}

// withE2E marks ctx as carrying a request that came through the channel.
func withE2E(ctx context.Context, s *hostSession) context.Context {
	if s == nil {
		return ctx
	}
	return context.WithValue(ctx, e2eKey{}, e2eCall{session: s.ID})
}

// withDirect marks ctx as carrying a request that came on the direct path.
func withDirect(ctx context.Context, c *directConn) context.Context {
	return context.WithValue(ctx, e2eKey{}, e2eCall{direct: c})
}

// ViaE2E reports whether the request in ctx came through the channel (on
// the relay or the direct path).
func ViaE2E(ctx context.Context) bool {
	_, ok := ctx.Value(e2eKey{}).(e2eCall)
	return ok
}

// ViaDirect reports whether the request in ctx came on the direct path.
func ViaDirect(ctx context.Context) bool {
	call, _ := ctx.Value(e2eKey{}).(e2eCall)
	return call.direct != nil
}

func directOf(ctx context.Context) *directConn {
	call, _ := ctx.Value(e2eKey{}).(e2eCall)
	return call.direct
}

// RouteOf is "direct" or "relay": how the request in ctx reached the host
// (audit lines record it).
func RouteOf(ctx context.Context) string {
	if ViaDirect(ctx) {
		return "direct"
	}
	return "relay"
}

func (h *E2E) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

// PublicKey is the base64 static public key snapshots advertise.
func (h *E2E) PublicKey() string {
	if h == nil || h.Key == nil {
		return ""
	}
	return base64.StdEncoding.EncodeToString(h.Key.PublicKey().Bytes())
}

func (h *E2E) audit(event string, fields map[string]any) {
	if h.Auth != nil {
		fields["e2e"] = true
		h.Auth.Audit(event, fields)
	}
}

// Hello answers a controller's handshake (e2e.hello, unsigned by design:
// the Noise handshake authenticates both sides).
func (h *E2E) Hello(m protocol.Message) (json.RawMessage, error) {
	now := h.now()
	var p e2e.Hello
	if err := params(m.Params, &p); err != nil || p.V != e2e.Version || len(p.H) == 0 || len(p.H) > 4096 {
		return nil, protocol.Err("invalid_request", "Invalid end-to-end handshake")
	}
	if !deviceID.MatchString(m.ControllerID) || h.Auth == nil || h.Key == nil {
		return nil, protocol.Err("invalid_request", "Invalid controller")
	}
	refuse := func(code, detail, message string) error {
		h.audit("e2e.refused", map[string]any{"device": m.ControllerID, "method": e2e.HelloMethod, "ok": false, "detail": detail})
		return protocol.Err(code, message)
	}
	r, err := e2e.Respond(h.Key.Bytes(), h.Key.PublicKey().Bytes(), p.H, h.Auth.MachineID, m.ControllerID, now)
	if err != nil {
		return nil, refuse("forbidden", err.Error(), "End-to-end handshake refused: "+err.Error())
	}
	h.mu.Lock()
	h.prune(now)
	replayed := !h.hellos[string(r.Ephemeral)].IsZero()
	if !replayed {
		h.hellos[string(r.Ephemeral)] = now.Add(2 * e2e.MaxSkew)
	}
	h.mu.Unlock()
	if replayed {
		return nil, refuse("forbidden", "replayed handshake", "End-to-end handshake was already received (replay)")
	}
	list, enforcing, err := h.Auth.Store.LoadControllers()
	enforcing = enforcing || h.Auth.Require
	if err != nil {
		return nil, refuse("forbidden", "allowlist unreadable", "This host's device allowlist (controllers.json) is damaged")
	}
	var c *Controller
	for i := range list.Controllers {
		if list.Controllers[i].Device == m.ControllerID {
			c = &list.Controllers[i]
		}
	}
	name, bound := "", false
	switch {
	case c != nil:
		key, _, err := devicekey.ParsePublicKey(c.Key)
		if err != nil {
			return nil, refuse("forbidden", "stored key invalid", "Host cannot verify this device")
		}
		if err := e2e.VerifyBinding(key, r.PeerStatic, r.Payload.Binding); err != nil {
			return nil, refuse(e2e.CodeBinding, "binding: "+err.Error(), "The controller's end-to-end key is not signed by its approved device key")
		}
		name, bound = c.Name, true
		if c.E2EKey != "" && c.E2EKey != base64.StdEncoding.EncodeToString(r.PeerStatic) {
			h.audit("e2e.key-changed", map[string]any{"device": c.Device, "name": c.Name, "ok": true, "detail": "a new end-to-end key, signed by the approved device key"})
		}
	case enforcing:
		return nil, refuse("forbidden", "device not approved", "This device is not approved on this host: run hesperctl pair-host")
	}
	id := e2e.NewID()
	s, answer, err := r.Accept(id, h.Require, now)
	if err != nil {
		return nil, refuse("forbidden", err.Error(), "End-to-end handshake failed")
	}
	session := &hostSession{Session: s, device: m.ControllerID, name: name, bound: bound, last: now}
	h.mu.Lock()
	h.add(session)
	h.mu.Unlock()
	detail := "session " + id[:8] + ", unbound key (no approved devices yet)"
	if bound {
		detail = "session " + id[:8] + ", key bound to the approved device key"
	}
	h.audit("e2e.session", map[string]any{"device": m.ControllerID, "name": name, "method": e2e.HelloMethod, "ok": true, "detail": detail})
	return protocol.JSON(e2e.HelloResult{H: answer}), nil
}

// add stores s, evicting the least recently used sessions over the limits.
func (h *E2E) add(s *hostSession) {
	if h.sessions == nil {
		h.sessions = map[string]*hostSession{}
	}
	h.sessions[s.device+"/"+s.ID] = s
	evict := func(match func(*hostSession) bool, limit int) {
		var mine []*hostSession
		for _, o := range h.sessions {
			if match(o) {
				mine = append(mine, o)
			}
		}
		if len(mine) <= limit {
			return
		}
		sort.Slice(mine, func(i, j int) bool { return mine[i].last.Before(mine[j].last) })
		for _, o := range mine[:len(mine)-limit] {
			delete(h.sessions, o.device+"/"+o.ID)
		}
	}
	evict(func(o *hostSession) bool { return o.device == s.device }, maxSessionsPerDevice)
	evict(func(*hostSession) bool { return true }, maxSessions)
}

// prune drops expired sessions, handshakes and inner IDs (h.mu held).
func (h *E2E) prune(now time.Time) {
	if h.hellos == nil {
		h.hellos = map[string]time.Time{}
	}
	if h.inners == nil {
		h.inners = map[string]time.Time{}
	}
	for k, s := range h.sessions {
		if now.Sub(s.last) > E2EIdle || now.Sub(s.Created) > E2ELifetime {
			delete(h.sessions, k)
		}
	}
	for k, until := range h.hellos {
		if now.After(until) {
			delete(h.hellos, k)
		}
	}
	if len(h.inners) > 4096 {
		for k, until := range h.inners {
			if now.After(until) {
				delete(h.inners, k)
			}
		}
	}
}

// Open decrypts an e2e request into the inner request it carries. Errors
// are answered in plaintext; nothing ran.
func (h *E2E) Open(m protocol.Message) (protocol.Message, *hostSession, string, error) {
	now := h.now()
	var f e2e.Frame
	if err := params(m.Params, &f); err != nil || !e2e.ValidSession(f.S) {
		return m, nil, "", protocol.Err("invalid_request", "Invalid end-to-end frame")
	}
	h.mu.Lock()
	h.prune(now)
	s := h.sessions[m.ControllerID+"/"+f.S]
	h.mu.Unlock()
	if s == nil {
		return m, nil, "", protocol.Err(e2e.CodeSession, "Unknown or expired end-to-end session; handshake again")
	}
	plain, err := s.Open(f.N, f.C)
	if err != nil {
		detail := "frame failed authentication (altered on the way)"
		if errors.Is(err, e2e.ErrReplay) {
			detail = "replayed frame"
		}
		h.audit("e2e.refused", map[string]any{"device": s.device, "name": s.name, "method": e2e.FrameMethod, "ok": false, "detail": detail})
		return m, nil, "", protocol.Err(e2e.CodeIntegrity, "The request was altered or replayed on the way (end-to-end check failed); it was not run")
	}
	var inner e2e.Request
	if err := json.Unmarshal(plain, &inner); err != nil || inner.ID == "" || len(inner.ID) > 64 || !protocol.ValidID(inner.Method) ||
		inner.Method == e2e.HelloMethod || inner.Method == e2e.FrameMethod {
		return m, nil, "", protocol.Err("invalid_request", "Invalid end-to-end request")
	}
	if len(inner.Params) == 0 {
		inner.Params = json.RawMessage("{}")
	}
	if !json.Valid(inner.Params) {
		return m, nil, "", protocol.Err("invalid_request", "Invalid end-to-end request")
	}
	h.mu.Lock()
	s.last = now
	h.mu.Unlock()
	if err := h.remember(s.device, s.name, inner.ID, inner.Method, "relay"); err != nil {
		return m, nil, "", err
	}
	out := protocol.Message{Version: m.Version, Type: m.Type, ID: m.ID, ControllerID: m.ControllerID, Deadline: m.Deadline,
		Method: inner.Method, Params: inner.Params, Auth: inner.Auth}
	return out, s, inner.ID, nil
}

// remember records an inner request ID of a device: the relay's channel
// and the direct path share these, so a request that went out on one and
// is resent on the other runs at most once (duplicate_request).
func (h *E2E) remember(device, name, id, method, route string) error {
	now := h.now()
	key := device + "/" + id
	h.mu.Lock()
	h.prune(now)
	_, seen := h.inners[key]
	full := len(h.inners) >= maxInners
	if !seen && !full {
		h.inners[key] = now.Add(innerTTL)
	}
	h.mu.Unlock()
	if seen {
		h.audit("e2e.refused", map[string]any{"device": device, "name": name, "method": method, "ok": false, "route": route, "detail": "repeated inner request"})
		return protocol.Err("duplicate_request", "This request was already received; it was not run again")
	}
	if full {
		return protocol.Err("busy", "Too many recent requests")
	}
	return nil
}

// Seal answers an inner request inside the channel.
func (h *E2E) Seal(s *hostSession, innerID string, result json.RawMessage, opErr error) json.RawMessage {
	answer := e2e.Response{ID: innerID, Result: result}
	if opErr != nil {
		answer.Result, answer.Error = nil, protocol.PublicError(opErr)
	}
	plain, _ := json.Marshal(answer)
	n, sealed := s.Seal(plain)
	return protocol.JSON(e2e.FrameResult{N: n, C: sealed})
}

// requiresE2E: with Require, which plaintext requests the host refuses:
// everything but the relay-visible snapshot, ping, devices.request and the
// handshake itself (agent lists carry names and tasks, so observing goes
// through the channel too).
func requiresE2E(m protocol.Message) bool {
	switch m.Method {
	case "snapshot", "ping", "devices.request", e2e.HelloMethod:
		return false
	}
	return true
}
