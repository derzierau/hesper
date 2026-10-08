package host

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"net"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/pkg/devicekey"
	"github.com/derzierau/hesper/relay/pkg/direct"
	"github.com/derzierau/hesper/relay/pkg/e2e"
	"github.com/derzierau/hesper/relay/pkg/protocol"
)

type directWorld struct {
	*e2eWorld
	d     *Direct
	r     *Runner
	ctx   context.Context
	mu    sync.Mutex
	clock time.Time
}

func newDirectWorld(t *testing.T) *directWorld {
	t.Helper()
	w := &directWorld{e2eWorld: newE2EWorld(t, true), clock: time.Now()}
	w.d = &Direct{E2E: w.h, Addrs: func() []netip.Addr { return []netip.Addr{netip.MustParseAddr("127.0.0.1")} }, Now: w.now}
	reg, _ := testRegistry(t)
	w.r = &Runner{Service: &Service{Agents: reg, Direct: w.d, E2EKey: w.h.PublicKey()}, Auth: w.h.Auth, E2E: w.h, Direct: w.d}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); w.d.Stop() })
	w.ctx = ctx
	if err := w.d.Start(ctx, w.r); err != nil {
		t.Fatal(err)
	}
	return w
}

func (w *directWorld) now() time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.clock
}

func (w *directWorld) advance(d time.Duration) {
	w.mu.Lock()
	w.clock = w.clock.Add(d)
	w.mu.Unlock()
}

func (w *directWorld) addr() string { return w.d.Addresses()[0] }

func (w *directWorld) dial(cfg direct.DialConfig) (*direct.Conn, error) {
	ctx, cancel := context.WithTimeout(w.ctx, 2*time.Second)
	defer cancel()
	return direct.Dial(ctx, w.addr(), cfg)
}

func (w *directWorld) config(token []byte) direct.DialConfig {
	return direct.DialConfig{MachineID: "mini", HostKey: w.h.Key.PublicKey().Bytes(), Identity: w.id, Device: "laptop", Token: token}
}

// call sends one request and waits for its answer.
func directCall(t *testing.T, c *direct.Conn, id, method string, params any, auth *protocol.Auth) e2e.Response {
	t.Helper()
	raw, _ := json.Marshal(params)
	body, _ := json.Marshal(direct.Request{Request: e2e.Request{ID: id, Method: method, Params: raw, Auth: auth}, Deadline: time.Now().Add(5 * time.Second).UnixMilli()})
	if err := c.WriteMessage(context.Background(), direct.KindRequest, body); err != nil {
		t.Fatal(err)
	}
	for {
		kind, data, err := c.ReadMessage()
		if err != nil {
			t.Fatal(err)
		}
		if kind != direct.KindResponse {
			continue
		}
		var resp e2e.Response
		if err := json.Unmarshal(data, &resp); err != nil {
			t.Fatal(err)
		}
		return resp
	}
}

func (w *directWorld) signed(t *testing.T, method string, params any) *protocol.Auth {
	t.Helper()
	raw, _ := json.Marshal(params)
	normalized, _ := devicekey.NormalizeParams(raw)
	auth, err := devicekey.SignRequest(w.signer, "laptop", "mini", method, normalized, devicekey.Options{}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return auth
}

// The direct path runs requests like the relay's channel: part K's
// signature and rights per request, audited with route "direct"; inner IDs
// are remembered (a resend is duplicate_request).
func TestDirectRequestsAreAuthorizedLikeTheChannel(t *testing.T) {
	w := newDirectWorld(t)
	if !w.d.Listening() || !w.r.Service.Direct.Listening() {
		t.Fatal("not listening")
	}
	offer := w.d.Offer("laptop")
	if len(offer.Addrs) != 1 || !strings.HasPrefix(offer.Addrs[0], "127.0.0.1:") || len(offer.Token) != 16 {
		t.Fatalf("offer %+v", offer)
	}
	c, err := w.dial(w.config(offer.Token))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if resp := directCall(t, c, "r1", "snapshot", map[string]any{}, nil); resp.Error != nil || resp.ID != "r1" {
		t.Fatalf("snapshot %+v", resp)
	}
	params := map[string]any{}
	if resp := directCall(t, c, "r2", "agents.list", params, w.signed(t, "agents.list", params)); resp.Error != nil {
		t.Fatalf("signed capture %+v", resp.Error)
	}
	// Unsigned: refused while enforcing.
	if resp := directCall(t, c, "r3", "agents.list", params, nil); resp.Error == nil || resp.Error.Code != "forbidden" {
		t.Fatalf("unsigned capture %+v", resp)
	}
	// A right the device does not have: refused and audited as direct.
	input := map[string]any{"id": "abc123", "text": "x"}
	if resp := directCall(t, c, "r4", "agents.input", input, w.signed(t, "agents.input", input)); resp.Error == nil || resp.Error.Code != "forbidden" {
		t.Fatalf("input without the right %+v", resp)
	}
	// The same inner ID again: never run twice.
	if resp := directCall(t, c, "r2", "agents.list", params, w.signed(t, "agents.list", params)); resp.Error == nil || resp.Error.Code != "duplicate_request" {
		t.Fatalf("repeated inner ID %+v", resp)
	}
	// Methods of the relay's channel are not taken here.
	for _, method := range []string{"direct.offer", "e2e.hello", "e2e", "devices.request"} {
		if resp := directCall(t, c, "x-"+method, method, map[string]any{}, nil); resp.Error == nil || resp.Error.Code != "invalid_request" {
			t.Fatalf("%s %+v", method, resp)
		}
	}
	audit := w.audit()
	for _, want := range []string{`"event":"direct.session","device":"laptop"`, `"missing right type"`, `"route":"direct"`} {
		if !strings.Contains(audit, want) {
			t.Fatalf("audit lacks %s:\n%s", want, audit)
		}
	}
}

// A handshake is accepted only from an approved device whose channel key
// is bound to its device key, with a token from a recent offer; every
// refusal is silent (the connection just closes) and audited.
func TestDirectHandshakeRefusals(t *testing.T) {
	w := newDirectWorld(t)
	token := w.d.Offer("laptop").Token
	try := func(name string, cfg direct.DialConfig) {
		t.Helper()
		if c, err := w.dial(cfg); err == nil {
			c.Close()
			t.Fatalf("%s: accepted", name)
		}
	}
	other := w.config(token)
	other.Device = "phone"
	try("unknown device", other)
	try("no token", w.config(nil))
	bad := make([]byte, 16)
	rand.Read(bad)
	try("wrong token", w.config(bad))
	stream := w.config(nil)
	stream.Stream = "no-such-stream"
	try("unknown stream", stream)
	// The same static key bound by a device key that is not the approved one.
	unbound := *w.id
	if err := unbound.Bind(&devicekey.Software{Dir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	foreign := w.config(token)
	foreign.Identity = &unbound
	try("unbound key", foreign)
	if !strings.Contains(w.audit(), `"event":"direct.refused"`) {
		t.Fatalf("refusals not audited:\n%s", w.audit())
	}
	// A token from the offer of another device does not help.
	w.d.Offer("phone")
	try("token of another device", w.config(w.d.Offer("phone").Token))
	// Too many failures from 127.0.0.1 by now: even the valid handshake is
	// ignored until the ban ends.
	if c, err := w.dial(w.config(token)); err == nil {
		c.Close()
		t.Fatal("a banned peer got through")
	}
	w.advance(61 * time.Second)
	c, err := w.dial(w.config(token))
	if err != nil {
		t.Fatalf("valid handshake after the ban: %v", err)
	}
	c.Close()
	// Tokens expire with their offer.
	w.d.mu.Lock()
	for i := range w.d.tokens["laptop"] {
		w.d.tokens["laptop"][i].expires = w.now().Add(-time.Second)
	}
	w.d.mu.Unlock()
	try("expired token", w.config(token))
}

// Strangers on the network: anything that is not a handshake for this
// host is closed without a byte in answer; after five failures in a minute
// the peer is ignored for a minute, valid handshake or not.
func TestDirectStrangersLearnNothingAndAreThrottled(t *testing.T) {
	w := newDirectWorld(t)
	for i := 0; i < 5; i++ {
		raw, err := net.Dial("tcp", w.addr())
		if err != nil {
			t.Fatal(err)
		}
		raw.Write([]byte("GHOSTYD1\x00\x30" + strings.Repeat("x", 48)))
		// Wait for the host's close (it counts the failure first), not a
		// timeout: the next attempt must meet the count.
		raw.SetReadDeadline(time.Now().Add(10 * time.Second))
		n, err := raw.Read(make([]byte, 64))
		raw.Close()
		if n != 0 {
			t.Fatalf("a stranger got %d bytes", n)
		}
		if ne, ok := err.(net.Error); ok && ne.Timeout() {
			t.Fatal("the host did not close a stranger's connection")
		}
	}
	token := w.d.Offer("laptop").Token
	if c, err := w.dial(w.config(token)); err == nil {
		c.Close()
		t.Fatal("a banned peer got through")
	}
	w.advance(61 * time.Second)
	token = w.d.Offer("laptop").Token
	c, err := w.dial(w.config(token))
	if err != nil {
		t.Fatalf("after the ban: %v", err)
	}
	c.Close()
	if n := strings.Count(w.audit(), `"event":"direct.refused"`); n != 1 {
		t.Fatalf("refusals audited %d times (at most once a minute per peer):\n%s", n, w.audit())
	}
}

// Connections end when the device is revoked or stops fetching offers
// through the relay; offers come only through a bound channel session.
func TestDirectSweepAndOffers(t *testing.T) {
	w := newDirectWorld(t)
	c, err := w.dial(w.config(w.d.Offer("laptop").Token))
	if err != nil {
		t.Fatal(err)
	}
	waitConns := func(n int) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for w.d.Conns() != n {
			if time.Now().After(deadline) {
				t.Fatalf("%d connections, want %d", w.d.Conns(), n)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	waitConns(1)
	w.d.mu.Lock()
	w.d.offered["laptop"] = w.now().Add(-31 * time.Minute)
	w.d.mu.Unlock()
	w.d.sweep()
	if _, _, err := c.ReadMessage(); err == nil {
		t.Fatal("still open without a fresh offer")
	}
	waitConns(0)
	c, err = w.dial(w.config(w.d.Offer("laptop").Token))
	if err != nil {
		t.Fatal(err)
	}
	waitConns(1)
	if err := w.h.Auth.Store.saveControllers(Controllers{}); err != nil {
		t.Fatal(err)
	}
	w.d.sweep()
	if _, _, err := c.ReadMessage(); err == nil {
		t.Fatal("still open after revocation")
	}
	if !strings.Contains(w.audit(), "device revoked on this host") {
		t.Fatalf("audit:\n%s", w.audit())
	}
	// direct.offer: never in plaintext, never for an unbound session.
	if _, err := w.r.offer(nil); code(err) != e2e.CodeRequired {
		t.Fatalf("plaintext offer: %v", err)
	}
	if _, err := w.r.offer(&hostSession{device: "laptop"}); code(err) != "forbidden" {
		t.Fatalf("unbound offer: %v", err)
	}
	if _, err := w.r.offer(&hostSession{device: "laptop", bound: true}); code(err) != "forbidden" {
		t.Fatalf("revoked device's offer: %v", err)
	}
}

func TestMacFirewallVerdicts(t *testing.T) {
	answers := func(state, blockAll, apps string) func(...string) string {
		return func(args ...string) string {
			switch args[0] {
			case "--getglobalstate":
				return state
			case "--getblockall":
				return blockAll
			}
			return apps
		}
	}
	on, open := "Firewall is enabled. (State = 1)", "Firewall has block all state set to disabled."
	apps := "Total number of apps = 2 \n1 : /opt/x/hesperd \n             (Allow incoming connections)\n2 : /opt/y/hesperd \n             (Block incoming connections)\n"
	for name, c := range map[string]struct {
		run     func(...string) string
		exe     string
		allowed bool
	}{
		"off":       {answers("Firewall is disabled. (State = 0)", "", ""), "/opt/z/hesperd", true},
		"no tool":   {answers("", "", ""), "/opt/z/hesperd", true},
		"block all": {answers(on, "Firewall has block all state set to enabled.", apps), "/opt/x/hesperd", false},
		"allowed":   {answers(on, open, apps), "/opt/x/hesperd", true},
		"blocked":   {answers(on, open, apps), "/opt/y/hesperd", false},
		"unknown":   {answers(on, open, apps), "/opt/z/hesperd", false},
	} {
		if v := MacFirewall(c.exe, c.run); v.Allowed != c.allowed || (!v.Allowed && v.Reason == "") {
			t.Errorf("%s: %+v", name, v)
		}
	}
}

func TestMacFirewallSignedAllowance(t *testing.T) {
	for name, c := range map[string]struct {
		setting, apps, block string
		signed, allowed      bool
	}{
		"downloaded enabled":  {"Automatically allow downloaded signed software ENABLED.", "", "disabled", true, true},
		"downloaded disabled": {"Automatically allow downloaded signed software DISABLED.", "", "disabled", true, false},
		"built in only":       {"Automatically allow built-in signed software ENABLED.\nAutomatically allow downloaded signed software DISABLED.", "", "disabled", true, false},
		"unsigned or invalid": {"Automatically allow downloaded signed software ENABLED.", "", "disabled", false, false},
		"unknown setting":     {"", "", "disabled", true, false},
		"explicit block":      {"Automatically allow downloaded signed software ENABLED.", "1 : /opt/test/hesperd\n (Block incoming connections)", "disabled", true, false},
		"block all":           {"Automatically allow downloaded signed software ENABLED.", "", "enabled", true, false},
	} {
		t.Run(name, func(t *testing.T) {
			run := func(args ...string) string {
				switch args[0] {
				case "--getglobalstate":
					return "Firewall is enabled. (State = 1)"
				case "--getblockall":
					return "Firewall has block all state set to " + c.block + "."
				case "--listapps":
					return c.apps
				case "--getallowsigned":
					return c.setting
				}
				t.Fatalf("unexpected firewall query %v", args)
				return ""
			}
			got := macFirewall("/opt/test/hesperd", run, func(string) bool { return c.signed })
			if got.Allowed != c.allowed {
				t.Fatalf("allowed=%v, want %v: %s", got.Allowed, c.allowed, got.Reason)
			}
		})
	}
}
