package host

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/pkg/devicekey"
	"github.com/derzierau/hesper/relay/pkg/protocol"
)

type authHarness struct {
	t   *testing.T
	dir string
	a   *Authorizer
	mu  sync.Mutex
	now time.Time
}

func hostKeyForTest() []byte {
	k := sha256.Sum256([]byte("host transfer key"))
	return k[:]
}

func newAuthHarness(t *testing.T) *authHarness {
	h := &authHarness{t: t, dir: t.TempDir(), now: time.UnixMilli(1791036424000)}
	h.a = h.authorizer()
	return h
}

// authorizer is a fresh Authorizer on the same state, as after a restart.
func (h *authHarness) authorizer() *Authorizer {
	return &Authorizer{Store: Store{Dir: h.dir}, MachineID: "mini-1", HostKey: hostKeyForTest(), Now: h.clock}
}

func (h *authHarness) clock() time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.now
}

func (h *authHarness) advance(d time.Duration) {
	h.mu.Lock()
	h.now = h.now.Add(d)
	h.mu.Unlock()
}

func publicKeys(t *testing.T, s devicekey.Signer) (string, string) {
	t.Helper()
	a, err := s.PublicKey(false)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := s.PublicKey(true)
	ka, _ := devicekey.EncodePublicKey(a)
	kb, _ := devicekey.EncodePublicKey(b)
	return ka, kb
}

func (h *authHarness) ask(device string, s devicekey.Signer, name string, rights ...string) (ApprovalStatus, error) {
	key, strong := publicKeys(h.t, s)
	m := protocol.Message{Type: "request", ControllerID: device, Method: "devices.request",
		Params: protocol.JSON(ApprovalRequest{Name: name, Key: key, StrongKey: strong, Hardware: false, Rights: rights})}
	raw, err := h.a.RequestApproval(context.Background(), m)
	var status ApprovalStatus
	if err == nil {
		json.Unmarshal(raw, &status)
	}
	return status, err
}

// approve runs the whole approval: devices.request, then a local approve.
func (h *authHarness) approve(device string, s devicekey.Signer, name string, opts ApproveOptions, rights ...string) Controller {
	h.t.Helper()
	status, err := h.ask(device, s, name, rights...)
	if err != nil || status.Status != "pending" {
		h.t.Fatalf("devices.request: %+v %v", status, err)
	}
	c, _, err := h.a.Store.Approve(status.Code, opts, h.clock())
	if err != nil {
		h.t.Fatal(err)
	}
	return c
}

func (h *authHarness) signed(device string, s devicekey.Signer, method, params string, strong bool) protocol.Message {
	h.t.Helper()
	normalized, err := devicekey.NormalizeParams([]byte(params))
	if err != nil {
		h.t.Fatal(err)
	}
	auth, err := devicekey.SignRequest(s, device, "mini-1", method, normalized, devicekey.Options{Strong: strong}, h.clock())
	if err != nil {
		h.t.Fatal(err)
	}
	return protocol.Message{Type: "request", ID: "r", ControllerID: device, Method: method, Params: normalized, Auth: auth}
}

func (h *authHarness) audit() string {
	data, _ := os.ReadFile(filepath.Join(h.dir, AuditFile))
	return string(data)
}

func forbidden(err error) bool {
	var p *protocol.Error
	return errors.As(err, &p) && p.Code == "forbidden"
}

const inputParams = `{"target":{"terminalId":"a3"},"text":"y","submit":true}`

func TestSignedRequestsFromApprovedDevices(t *testing.T) {
	h := newAuthHarness(t)
	laptop := &devicekey.Software{Dir: t.TempDir()}
	h.approve("laptop-1", laptop, "laptop", ApproveOptions{}, "observe", "answer", "type")

	caller, err := h.a.Authorize(context.Background(), h.signed("laptop-1", laptop, "agents.input", inputParams, false))
	if err != nil {
		t.Fatal(err)
	}
	if !caller.Verified || caller.Name != "laptop" || caller.Device != "laptop-1" || caller.Strong || caller.Hardware || !caller.Has("type") || caller.Has("shell") {
		t.Fatalf("caller %+v", caller)
	}
	// Any request may use the strong key; the caller then says so.
	caller, err = h.a.Authorize(context.Background(), h.signed("laptop-1", laptop, "agents.list", `{}`, true))
	if err != nil || !caller.Strong {
		t.Fatalf("strong capture: %+v %v", caller, err)
	}
	// Snapshot and ping stay open to every routed controller, signed or
	// not, and observe only.
	if c, err := h.a.Authorize(context.Background(), protocol.Message{ControllerID: "stranger", Method: "ping", Params: json.RawMessage(`{}`)}); err != nil || c.Has("type") || c.Verified {
		t.Fatalf("ping %+v %v", c, err)
	}
	if _, err := h.a.Authorize(context.Background(), protocol.Message{ControllerID: "stranger", Method: "snapshot", Params: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	// Mutations are audited, observation is not.
	if a := h.audit(); !strings.Contains(a, `"event":"request","device":"laptop-1","name":"laptop","method":"agents.input","ok":true`) || strings.Contains(a, `"method":"agents.list","ok":true`) {
		t.Fatalf("audit:\n%s", a)
	}
}

func TestRefusals(t *testing.T) {
	h := newAuthHarness(t)
	laptop := &devicekey.Software{Dir: t.TempDir()}
	h.approve("laptop-1", laptop, "laptop", ApproveOptions{}, "observe", "answer", "type")
	stranger := &devicekey.Software{Dir: t.TempDir()}
	ctx := context.Background()

	tamper := map[string]func() protocol.Message{
		"unsigned": func() protocol.Message {
			m := h.signed("laptop-1", laptop, "agents.input", inputParams, false)
			m.Auth = nil
			return m
		},
		"method changed": func() protocol.Message {
			m := h.signed("laptop-1", laptop, "agents.input", inputParams, false)
			m.Method = "agents.answer"
			return m
		},
		"params changed": func() protocol.Message {
			m := h.signed("laptop-1", laptop, "agents.input", inputParams, false)
			m.Params = json.RawMessage(strings.Replace(inputParams, `"y"`, `"rm -rf ~"`, 1))
			return m
		},
		"params with a repeated key": func() protocol.Message {
			m := h.signed("laptop-1", laptop, "agents.input", inputParams, false)
			m.Params = json.RawMessage(strings.Replace(inputParams, `"submit":true`, `"submit":true,"text":"x"`, 1))
			return m
		},
		"ts changed": func() protocol.Message {
			m := h.signed("laptop-1", laptop, "agents.input", inputParams, false)
			a := *m.Auth
			a.TS -= 1000
			m.Auth = &a
			return m
		},
		"nonce changed": func() protocol.Message {
			m := h.signed("laptop-1", laptop, "agents.input", inputParams, false)
			a := *m.Auth
			a.Nonce, _ = devicekey.NewNonce()
			m.Auth = &a
			return m
		},
		"for another host": func() protocol.Message {
			normalized, _ := devicekey.NormalizeParams([]byte(inputParams))
			auth, _ := devicekey.SignRequest(laptop, "laptop-1", "mini-2", "agents.input", normalized, devicekey.Options{}, h.clock())
			return protocol.Message{ControllerID: "laptop-1", Method: "agents.input", Params: normalized, Auth: auth}
		},
		"unknown key": func() protocol.Message { return h.signed("laptop-1", stranger, "agents.input", inputParams, false) },
		"unknown device": func() protocol.Message {
			return h.signed("stranger-1", stranger, "agents.input", inputParams, false)
		},
		"relay names another controller": func() protocol.Message {
			m := h.signed("laptop-1", laptop, "agents.input", inputParams, false)
			m.ControllerID = "other-1"
			return m
		},
		"missing right": func() protocol.Message {
			return h.signed("laptop-1", laptop, "agents.import", `{"upload":"u1"}`, false)
		},
		"shell with the device key": func() protocol.Message {
			return h.signed("laptop-1", laptop, "agents.spawn", `{"kind":"shell","project":"/tmp"}`, false)
		},
		"unknown method": func() protocol.Message { return h.signed("laptop-1", laptop, "shell.exec", `{}`, true) },
		"malformed signature": func() protocol.Message {
			m := h.signed("laptop-1", laptop, "agents.input", inputParams, false)
			a := *m.Auth
			a.Sig = "!!"
			m.Auth = &a
			return m
		},
		"expired": func() protocol.Message {
			m := h.signed("laptop-1", laptop, "agents.input", inputParams, false)
			h.advance(61 * time.Second)
			return m
		},
		"from the future": func() protocol.Message {
			h.advance(61 * time.Second)
			m := h.signed("laptop-1", laptop, "agents.input", inputParams, false)
			h.advance(-61 * time.Second)
			return m
		},
	}
	for name, build := range tamper {
		m := build()
		if _, err := h.a.Authorize(ctx, m); !forbidden(err) {
			t.Errorf("%s: %v", name, err)
		}
	}
	audit := h.audit()
	for _, detail := range []string{"unsigned request", "device not approved", "missing right transfer", "timestamp off by", "auth.device differs", "method not in the rights table", "bad signature"} {
		if !strings.Contains(audit, detail) {
			t.Errorf("audit lacks %q:\n%s", detail, audit)
		}
	}
	if n := strings.Count(audit, `"event":"request.refused"`); n != len(tamper) {
		t.Errorf("%d refusals audited, want %d", n, len(tamper))
	}
}

func TestReplayIsRefusedAlsoAfterRestart(t *testing.T) {
	h := newAuthHarness(t)
	laptop := &devicekey.Software{Dir: t.TempDir()}
	h.approve("laptop-1", laptop, "laptop", ApproveOptions{}, "type")
	m := h.signed("laptop-1", laptop, "agents.input", inputParams, false)
	ctx := context.Background()
	if _, err := h.a.Authorize(ctx, m); err != nil {
		t.Fatal(err)
	}
	if _, err := h.a.Authorize(ctx, m); !forbidden(err) {
		t.Fatalf("replay: %v", err)
	}
	// A restarted host reads the nonces back.
	h.advance(30 * time.Second)
	h.a = h.authorizer()
	if _, err := h.a.Authorize(ctx, m); !forbidden(err) {
		t.Fatalf("replay after restart: %v", err)
	}
	if !strings.Contains(h.audit(), "replayed nonce") {
		t.Fatal("replay not audited")
	}
	info, err := os.Stat(filepath.Join(h.dir, NoncesFile))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("nonces file: %v %v", info, err)
	}
	// Other requests still pass; old nonces expire from the file.
	if _, err := h.a.Authorize(ctx, h.signed("laptop-1", laptop, "agents.input", inputParams, false)); err != nil {
		t.Fatal(err)
	}
	h.advance(NonceTTL + time.Minute)
	h.a = h.authorizer()
	if _, err := h.a.Authorize(ctx, h.signed("laptop-1", laptop, "agents.input", inputParams, false)); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(h.dir, NoncesFile)); strings.Count(string(data), "\n") != 1 {
		t.Fatalf("expired nonces kept:\n%s", data)
	}
}

func TestShellNeedsTheStrongKeyAndApprovedHardware(t *testing.T) {
	h := newAuthHarness(t)
	laptop := &devicekey.Software{Dir: t.TempDir()}
	// A software-key device does not get shell unless the owner insists.
	c := h.approve("laptop-1", laptop, "laptop", ApproveOptions{}, "observe", "shell")
	if c.Has("shell") {
		t.Fatalf("software device got shell: %v", c.Rights)
	}
	if _, err := h.a.Authorize(context.Background(), h.signed("laptop-1", laptop, "agents.spawn", `{"kind":"shell"}`, true)); !forbidden(err) {
		t.Fatalf("shell without the right: %v", err)
	}
	h.advance(10 * time.Second)
	h.approve("laptop-1", laptop, "laptop", ApproveOptions{AllowSoftwareShell: true}, "observe", "shell")
	caller, err := h.a.Authorize(context.Background(), h.signed("laptop-1", laptop, "agents.spawn", `{"kind":"shell"}`, true))
	if err != nil || !caller.Strong || !caller.Has("shell") {
		t.Fatalf("strong shell.open: %+v %v", caller, err)
	}
	if _, err := h.a.Authorize(context.Background(), h.signed("laptop-1", laptop, "agents.spawn", `{"profile":"shell"}`, false)); !forbidden(err) {
		t.Fatalf("a shell spawn with the device key: %v", err)
	}
}

func (c Controller) Has(right string) bool {
	for _, r := range c.Rights {
		if r == right {
			return true
		}
	}
	return false
}

func TestEnforcementOffUntilADeviceIsApproved(t *testing.T) {
	h := newAuthHarness(t)
	ctx := context.Background()
	unsigned := protocol.Message{ControllerID: "laptop-1", Method: "agents.input", Params: json.RawMessage(inputParams)}
	caller, err := h.a.Authorize(ctx, unsigned)
	if err != nil || caller.Verified || !caller.Has("type") || caller.Has("shell") {
		t.Fatalf("enforcement off: %+v %v", caller, err)
	}
	if !strings.Contains(h.audit(), "unauthenticated request (enforcement off: approve devices with hesperctl approve)") {
		t.Fatalf("not audited:\n%s", h.audit())
	}
	// Shells never work without approved device keys.
	if _, err := h.a.Authorize(ctx, protocol.Message{ControllerID: "laptop-1", Method: "agents.spawn", Params: json.RawMessage(`{"kind":"shell"}`)}); !forbidden(err) {
		t.Fatalf("shell with enforcement off: %v", err)
	}
	if on, _ := h.a.Enforcing(); on {
		t.Fatal("enforcing without devices")
	}
	// --require-device-keys
	h.a.Require = true
	if _, err := h.a.Authorize(ctx, unsigned); !forbidden(err) {
		t.Fatalf("required keys: %v", err)
	}
	h.a.Require = false
	// The first approval turns enforcement on.
	h.approve("laptop-1", &devicekey.Software{Dir: t.TempDir()}, "laptop", ApproveOptions{}, "type")
	if _, err := h.a.Authorize(ctx, unsigned); !forbidden(err) {
		t.Fatalf("unsigned after approval: %v", err)
	}
	// A damaged allowlist refuses everything but snapshot.
	os.WriteFile(filepath.Join(h.dir, ControllersFile), []byte(`{"version":1,"controllers":[{"device":"x"}]}`), 0600)
	if _, err := h.a.Authorize(ctx, unsigned); !forbidden(err) {
		t.Fatalf("damaged allowlist: %v", err)
	}
	os.WriteFile(filepath.Join(h.dir, ControllersFile), []byte(`{"version":1,"controllers":[],"controllers":[]}`), 0600)
	if _, err := h.a.Authorize(ctx, unsigned); !forbidden(err) {
		t.Fatalf("ambiguous allowlist: %v", err)
	}
	os.WriteFile(filepath.Join(h.dir, ControllersFile), []byte(`{"version":1,"controllers":[]}`), 0600)
	os.Chmod(filepath.Join(h.dir, ControllersFile), 0644)
	if _, err := h.a.Authorize(ctx, unsigned); !forbidden(err) {
		t.Fatalf("world-readable allowlist: %v", err)
	}
	if _, err := h.a.Authorize(ctx, protocol.Message{ControllerID: "x", Method: "snapshot"}); err != nil {
		t.Fatal(err)
	}
}

func TestApprovalRequests(t *testing.T) {
	h := newAuthHarness(t)
	laptop := &devicekey.Software{Dir: t.TempDir()}
	status, err := h.ask("laptop-1", laptop, "laptop", "observe", "type")
	if err != nil || status.Status != "pending" {
		t.Fatal(status, err)
	}
	key, strong := publicKeys(t, laptop)
	_, keyDER, _ := devicekey.ParsePublicKey(key)
	_, strongDER, _ := devicekey.ParsePublicKey(strong)
	if want := devicekey.ApprovalCode(hostKeyForTest(), keyDER, strongDER); status.Code != want {
		t.Fatalf("code %s, want %s", status.Code, want)
	}
	pending, _ := h.a.Store.PendingRequests(h.clock())
	if len(pending) != 1 || pending[0].Name != "laptop" || pending[0].Code != status.Code {
		t.Fatalf("pending %+v", pending)
	}
	info, _ := os.Stat(filepath.Join(h.dir, PendingFile))
	if info.Mode().Perm() != 0600 {
		t.Fatalf("pending file mode %v", info.Mode())
	}
	// Asking again while waiting changes nothing (and is not rate limited).
	again, err := h.ask("laptop-1", laptop, "laptop", "observe", "type")
	if err != nil || again.Code != status.Code || again.Expires != status.Expires {
		t.Fatal(again, err)
	}
	// Bad requests.
	for name, params := range map[string]string{
		"no keys":        `{"name":"x","rights":["observe"]}`,
		"same key twice": `{"name":"x","key":"` + key + `","strongKey":"` + key + `","rights":["observe"]}`,
		"bad right":      `{"name":"x","key":"` + key + `","strongKey":"` + strong + `","rights":["root"]}`,
		"no rights":      `{"name":"x","key":"` + key + `","strongKey":"` + strong + `","rights":[]}`,
		"bad name":       `{"name":"x\u0007","key":"` + key + `","strongKey":"` + strong + `","rights":["observe"]}`,
		"unknown field":  `{"name":"x","key":"` + key + `","strongKey":"` + strong + `","rights":["observe"],"approved":true}`,
	} {
		_, err := h.a.RequestApproval(context.Background(), protocol.Message{ControllerID: "d-" + strings.ReplaceAll(name, " ", "-"), Method: "devices.request", Params: json.RawMessage(params)})
		var p *protocol.Error
		if !errors.As(err, &p) || p.Code != "invalid_request" {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Rate limit per device, then at most MaxPending devices wait.
	if _, err := h.ask("laptop-1", laptop, "laptop renamed", "observe"); err == nil {
		t.Fatal("no rate limit")
	}
	for i := 2; i <= MaxPending; i++ {
		h.advance(6 * time.Second)
		if _, err := h.ask("dev-"+string(rune('0'+i)), &devicekey.Software{Dir: t.TempDir()}, "dev"+string(rune('0'+i)), "observe"); err != nil {
			t.Fatal(i, err)
		}
	}
	h.advance(6 * time.Second)
	if _, err := h.ask("dev-9", &devicekey.Software{Dir: t.TempDir()}, "dev9", "observe"); err == nil {
		t.Fatal("more than MaxPending waiting")
	}
	// Approving by code or name; a denied device cannot ask again at once.
	if _, err := h.a.Store.Deny("dev2", h.clock()); err != nil {
		t.Fatal(err)
	}
	h.advance(6 * time.Second)
	dev2 := &devicekey.Software{Dir: t.TempDir()}
	if _, err := h.ask("dev-2", dev2, "dev2", "observe"); !forbidden(err) {
		t.Fatalf("denied device asked again: %v", err)
	}
	c, _, err := h.a.Store.Approve(strings.ToLower(strings.ReplaceAll(status.Code, "-", "")), ApproveOptions{}, h.clock())
	if err != nil || c.Name != "laptop" || strings.Join(c.Rights, ",") != "observe,type" || c.ApprovedBy != "local" {
		t.Fatalf("approve: %+v %v", c, err)
	}
	info, _ = os.Stat(filepath.Join(h.dir, ControllersFile))
	if info.Mode().Perm() != 0600 {
		t.Fatalf("controllers.json mode %v", info.Mode())
	}
	h.advance(6 * time.Second)
	if status, err := h.ask("laptop-1", laptop, "laptop", "observe"); err != nil || status.Status != "approved" {
		t.Fatalf("approved device asking: %+v %v", status, err)
	}
	// Names stay unique; requests expire.
	h.advance(6 * time.Second)
	if _, err := h.ask("dev-7", &devicekey.Software{Dir: t.TempDir()}, "laptop", "observe"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.a.Store.Approve("laptop", ApproveOptions{}, h.clock()); err == nil {
		t.Fatal("second controller named laptop")
	}
	h.advance(PendingTTL)
	if pending, _ := h.a.Store.PendingRequests(h.clock()); len(pending) != 0 {
		t.Fatalf("expired requests: %+v", pending)
	}
	// Revocation.
	if _, err := h.a.Store.Revoke("laptop"); err != nil {
		t.Fatal(err)
	}
	if list, _, _ := h.a.Store.LoadControllers(); len(list.Controllers) != 0 {
		t.Fatal("not revoked")
	}
	for _, event := range []string{"device.requested", "device.approved", "device.denied", "device.revoked", "device.request.refused"} {
		if !strings.Contains(h.audit(), `"event":"`+event+`"`) {
			t.Errorf("audit lacks %s", event)
		}
	}
}

func TestAuditLines(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	s.Audit("shell.open", map[string]any{"device": "d1", "name": "laptop", "method": "shell.open", "ok": true, "detail": "cwd ~", "ignored": 1})
	data, _ := os.ReadFile(filepath.Join(s.Dir, AuditFile))
	var line map[string]any
	if err := json.Unmarshal(data, &line); err != nil {
		t.Fatal(err)
	}
	if line["event"] != "shell.open" || line["device"] != "d1" || line["ok"] != true || line["ignored"] != nil || line["at"] == "" {
		t.Fatalf("line %v", line)
	}
	info, _ := os.Stat(filepath.Join(s.Dir, AuditFile))
	if info.Mode().Perm() != 0600 {
		t.Fatalf("audit mode %v", info.Mode())
	}
}
