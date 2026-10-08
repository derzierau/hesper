package client

// The controller's side of the end-to-end channel (pkg/e2e, contract Part
// N). With EnableE2E every request but devices.request to a host that
// supports the channel travels inside it: Forward handshakes once per host
// and relay connection (again after RekeyAfter), seals the inner request
// with its part K signature and opens the host's sealed answer. The relay
// sees the outer "e2e" requests only.
//
// When the channel is used: the host advertises it (snapshot capability
// e2e) or this controller already spoke to it through the channel
// (trusted-hosts.json remembers, so a relay that hides the capability cannot
// downgrade a host it once saw). shell.* never go out in plaintext; neither
// does anything sent with RequireE2E. The host's key is the pinned one: a
// host (or relay) presenting another key is refused (key_changed).

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/derzierau/hesper/relay/pkg/devicekey"
	"github.com/derzierau/hesper/relay/pkg/e2e"
	"github.com/derzierau/hesper/relay/pkg/protocol"
)

// RekeyAfter is how long a channel session is used before a fresh
// handshake (new ephemeral keys); RekeyFrames bounds its frames.
var (
	RekeyAfter  = time.Hour
	RekeyFrames = uint64(1 << 20)
)

// E2EConfig turns the channel on for a Controller.
type E2EConfig struct {
	// Identity is this controller's static key (e2e.LoadIdentity).
	Identity *e2e.Identity
	// TrustPath is trusted-hosts.json (host keys pinned on first use).
	TrustPath string
	// Signer (optional) binds Identity to the device key when it has no
	// binding yet or a host refuses it: one signature with the device key
	// (never the strong one).
	Signer devicekey.Signer
	// Notice (optional) receives remarks such as a newly pinned key.
	Notice func(string)
}

type channel struct {
	mu      sync.Mutex // one handshake at a time
	sess    *e2e.Session
	require bool
}

// EnableE2E makes Forward use the channel (see the package comment). It
// must be called before the first request.
func (c *Controller) EnableE2E(cfg E2EConfig) {
	if cfg.Identity == nil {
		return
	}
	if cfg.Identity.Binding == "" && cfg.Signer != nil {
		_ = cfg.Identity.Bind(cfg.Signer)
	}
	c.e2e = &cfg
	c.channels = map[string]*channel{}
}

// E2EEnabled reports whether EnableE2E was called.
func (c *Controller) E2EEnabled() bool { return c.e2e != nil }

// E2EReport tells a caller how a request traveled: Used when inside the
// channel, Require when the host requires the channel, Route "direct" or
// "relay" and Addr the direct connection's address.
type E2EReport struct {
	Used    bool
	Require bool
	Route   string
	Addr    string
}

type reportKey struct{}
type requireKey struct{}

// WithE2EReport makes requests made with ctx fill r.
func WithE2EReport(ctx context.Context, r *E2EReport) context.Context {
	return context.WithValue(ctx, reportKey{}, r)
}

// RequireE2E makes requests made with ctx (and terminal streams opened with
// it) fail with e2e_required instead of going out in plaintext.
func RequireE2E(ctx context.Context) context.Context {
	return context.WithValue(ctx, requireKey{}, true)
}

func requiresE2E(ctx context.Context) bool { v, _ := ctx.Value(requireKey{}).(bool); return v }

func report(ctx context.Context, used, require bool) {
	if r, ok := ctx.Value(reportKey{}).(*E2EReport); ok && r != nil {
		r.Used, r.Require = used, require
	}
}

func reportRoute(ctx context.Context, route, addr string) {
	if r, ok := ctx.Value(reportKey{}).(*E2EReport); ok && r != nil {
		r.Route, r.Addr = route, addr
	}
}

// mustEncrypt: methods that never travel in plaintext from a controller
// that has the channel.
func mustEncrypt(ctx context.Context, method string) bool {
	return requiresE2E(ctx) || strings.HasPrefix(method, "shell.")
}

func errRequired(name string) error {
	return protocol.Err(e2e.CodeRequired, name+" does not offer the end-to-end channel, and this request is never sent in plaintext (update hesperd there)")
}

// machine returns the latest inventory entry, waiting a moment for the first
// inventory after connecting.
func (c *Controller) machine(ctx context.Context, id string) (protocol.Machine, bool) {
	if c.ready != nil {
		wait, cancel := context.WithTimeout(ctx, 5*time.Second)
		select {
		case <-c.ready:
		case <-wait.Done():
		}
		cancel()
	}
	c.invMu.Lock()
	defer c.invMu.Unlock()
	m, ok := c.inventory[id]
	return m, ok
}

// hostPlan decides how to reach a host: its pinned static key and whether
// the channel is used (nil key: plaintext).
func (c *Controller) hostPlan(ctx context.Context, machineID string) ([]byte, string, error) {
	m, _ := c.machine(ctx, machineID)
	name := m.Name
	if name == "" {
		name = machineID
	}
	var snap struct {
		Capabilities struct {
			E2E bool `json:"e2e"`
		} `json:"capabilities"`
		E2EKey      string `json:"e2eKey"`
		TransferKey string `json:"transferKey"`
	}
	if len(m.Snapshot) > 0 {
		_ = json.Unmarshal(m.Snapshot, &snap)
	}
	advertised := snap.E2EKey
	if advertised == "" {
		advertised = snap.TransferKey
	}
	var key string
	err := e2e.UpdateTrust(c.e2e.TrustPath, func(t *e2e.Trust) (bool, error) {
		pinned, ok := t.Hosts[machineID]
		if ok {
			if advertised != "" && advertised != pinned.Key {
				return false, e2e.KeyChanged(name, pinned)
			}
			if snap.Capabilities.E2E || pinned.E2E > 0 {
				key = pinned.Key
			}
			return false, nil
		}
		if !snap.Capabilities.E2E || snap.E2EKey == "" {
			return false, nil
		}
		// Trust on first use; part K's approval code covers this key, so
		// pairing (hesperctl pair-host) catches a relay that swapped it.
		t.Hosts[machineID] = e2e.TrustedHost{Name: m.Name, Key: snap.E2EKey, Pinned: time.Now().Unix()}
		key = snap.E2EKey
		if c.e2e.Notice != nil {
			c.e2e.Notice("Pinned the host key of " + name + ".")
		}
		return true, nil
	})
	if err != nil {
		return nil, name, err
	}
	if key == "" {
		return nil, name, nil
	}
	raw, err := base64.StdEncoding.DecodeString(key)
	if err != nil || len(raw) != 32 {
		return nil, name, fmt.Errorf("the pinned key of %s in %s is invalid", name, c.e2e.TrustPath)
	}
	return raw, name, nil
}

// session returns the channel to a host, handshaking when there is none
// yet (or it is due for a rekey); nil without the channel.
func (c *Controller) session(ctx context.Context, machineID string, fresh bool) (*e2e.Session, bool, error) {
	key, name, err := c.hostPlan(ctx, machineID)
	if err != nil || key == nil {
		return nil, false, err
	}
	c.chanMu.Lock()
	ch := c.channels[machineID]
	if ch == nil {
		ch = &channel{}
		c.channels[machineID] = ch
	}
	c.chanMu.Unlock()
	ch.mu.Lock()
	defer ch.mu.Unlock()
	if s := ch.sess; s != nil && !fresh && time.Since(s.Created) < RekeyAfter && s.Sent() < RekeyFrames && string(s.Peer) == string(key) {
		return s, ch.require, nil
	}
	s, p, err := c.handshake(ctx, machineID, name, key)
	if err != nil {
		ch.sess = nil
		return nil, false, err
	}
	ch.sess, ch.require = s, p.Require
	return s, p.Require, nil
}

func (c *Controller) handshake(ctx context.Context, machineID, name string, key []byte) (*e2e.Session, e2e.RespPayload, error) {
	for attempt := 0; ; attempt++ {
		ini, msg, err := e2e.Initiate(c.e2e.Identity, key, machineID, c.device, time.Now())
		if err != nil {
			return nil, e2e.RespPayload{}, err
		}
		m, err := c.exchange(ctx, protocol.Message{Type: "request", MachineID: machineID, Method: e2e.HelloMethod, Params: protocol.JSON(e2e.Hello{V: e2e.Version, H: msg})})
		var fault *protocol.Error
		if errors.As(err, &fault) && fault.Code == e2e.CodeBinding && attempt == 0 && c.e2e.Signer != nil {
			if c.e2e.Identity.Bind(c.e2e.Signer) == nil {
				continue
			}
		}
		if err != nil {
			return nil, e2e.RespPayload{}, err
		}
		var answer e2e.HelloResult
		if err := json.Unmarshal(m.Result, &answer); err != nil {
			return nil, e2e.RespPayload{}, protocol.Err(e2e.CodeIntegrity, "Invalid end-to-end handshake answer from "+name)
		}
		s, p, err := ini.Finish(answer.H, time.Now())
		if err != nil {
			return nil, p, protocol.Err("key_changed", name+" did not prove that it holds its pinned host key; refusing to talk to it (someone between the machines may be intercepting). If it was reinstalled, run `hesperctl trust --reset "+name+"` and pair again")
		}
		_ = e2e.UpdateTrust(c.e2e.TrustPath, func(t *e2e.Trust) (bool, error) {
			h, ok := t.Hosts[machineID]
			if !ok || h.E2E > 0 {
				return false, nil
			}
			h.E2E = time.Now().Unix()
			t.Hosts[machineID] = h
			return true, nil
		})
		return s, p, nil
	}
}

// forwardE2E sends one request through the channel, or returns handled
// false when the host is reached in plaintext.
func (c *Controller) forwardE2E(ctx context.Context, machineID, method string, params json.RawMessage, auth *protocol.Auth) (json.RawMessage, bool, error) {
	id := e2e.NewID()
	request := e2e.Request{ID: id, Method: method, Params: params, Auth: auth}
	// The direct path first, when it is up (direct.go); the same inner
	// request goes through the relay when it is not.
	if !viaRelay(ctx) && method != "direct.offer" {
		if result, err, fallback := c.tryDirect(ctx, machineID, request); !fallback {
			return result, true, err
		}
	}
	s, require, err := c.session(ctx, machineID, false)
	if err != nil {
		return nil, true, err
	}
	if s == nil {
		if mustEncrypt(ctx, method) {
			m, _ := c.machine(ctx, machineID)
			name := m.Name
			if name == "" {
				name = machineID
			}
			return nil, true, errRequired(name)
		}
		return nil, false, nil
	}
	inner, _ := json.Marshal(request)
	for attempt := 0; ; attempt++ {
		n, sealed := s.Seal(inner)
		m, err := c.exchange(ctx, protocol.Message{Type: "request", MachineID: machineID, Method: e2e.FrameMethod, Params: protocol.JSON(e2e.Frame{S: s.ID, N: n, C: sealed})})
		var fault *protocol.Error
		if errors.As(err, &fault) && fault.Code == e2e.CodeSession && attempt == 0 {
			// The host does not know the session (it restarted, or the
			// session expired), so it decrypted and ran nothing. The same
			// inner request (same ID, same signature and nonce) goes once
			// through a fresh session; the host refuses a duplicate if a
			// relay faked this answer.
			if s, require, err = c.session(ctx, machineID, true); err != nil {
				return nil, true, err
			}
			continue
		}
		if err != nil {
			return nil, true, err
		}
		var frame e2e.FrameResult
		if err := json.Unmarshal(m.Result, &frame); err != nil {
			return nil, true, protocol.Err(e2e.CodeIntegrity, "The host's answer was not end-to-end encrypted; outcome unknown")
		}
		plain, err := s.Open(frame.N, frame.C)
		if err != nil {
			return nil, true, protocol.Err(e2e.CodeIntegrity, "The host's answer was altered or replayed on the way; outcome unknown")
		}
		var answer e2e.Response
		if err := json.Unmarshal(plain, &answer); err != nil || answer.ID != id {
			return nil, true, protocol.Err(e2e.CodeIntegrity, "The host's answer belongs to another request; outcome unknown")
		}
		report(ctx, true, require)
		reportRoute(ctx, RouteRelay, "")
		if answer.Error != nil {
			return nil, true, answer.Error
		}
		return answer.Result, true, nil
	}
}

// E2EState reports whether the channel to a host is up right now (false
// through a fleet sync, which holds the channels itself).
func (c *Controller) E2EState(machineID string) bool {
	if c.e2e == nil {
		return false
	}
	c.chanMu.Lock()
	ch := c.channels[machineID]
	c.chanMu.Unlock()
	if ch == nil {
		return false
	}
	ch.mu.Lock()
	defer ch.mu.Unlock()
	return ch.sess != nil
}
