package main

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/internal/agents"
	"github.com/derzierau/hesper/relay/internal/controlsock"
	"github.com/derzierau/hesper/relay/internal/host"
	"github.com/derzierau/hesper/relay/internal/identity"
	"github.com/derzierau/hesper/relay/internal/relay"
	"github.com/derzierau/hesper/relay/internal/storage"
	"github.com/derzierau/hesper/relay/internal/transport"
	"github.com/derzierau/hesper/relay/pkg/client"
	"github.com/derzierau/hesper/relay/pkg/devicekey"
	"github.com/derzierau/hesper/relay/pkg/protocol"
	"github.com/derzierau/hesper/relay/pkg/transfer"
)

// countingSigner records which keys a process used.
type countingSigner struct {
	devicekey.Signer
	mu             sync.Mutex
	normal, strong int
}

func (s *countingSigner) Sign(digest []byte, opts devicekey.Options) ([]byte, error) {
	s.mu.Lock()
	if opts.Strong {
		s.strong++
	} else {
		s.normal++
	}
	s.mu.Unlock()
	return s.Signer.Sign(digest, opts)
}

func (s *countingSigner) counts() (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.normal, s.strong
}

type deviceWorld struct {
	dir, hostDir, machine string
	laptop, phone         string // credentials files
	params                string // a key request's params file
}

// newDeviceWorld is an in-process relay, one host that checks device keys
// (state in hostDir) and two enrolled controllers.
func newDeviceWorld(t *testing.T) deviceWorld {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", filepath.Join(dir, "home"))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	db, err := storage.Open(filepath.Join(dir, "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	hub := relay.New(nil, 5*time.Second)
	t.Cleanup(hub.Close)
	server := httptest.NewServer(transport.New(db, hub).PublicHandler())
	t.Cleanup(server.Close)
	pair := func(role identity.Role, name string) protocol.Credentials {
		invite, err := db.Invite(ctx, identity.Invitation{Owner: "personal", Role: role, Expires: time.Now().Add(time.Minute)})
		if err != nil {
			t.Fatal(err)
		}
		c, err := client.Pair(ctx, server.URL, invite, name)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	w := deviceWorld{dir: dir, hostDir: filepath.Join(dir, "host-state"), laptop: filepath.Join(dir, "laptop.json"), phone: filepath.Join(dir, "phone.json"), params: filepath.Join(dir, "key.json")}
	hostCreds := pair(identity.Host, "mini · Studio")
	w.machine = hostCreds.DeviceID
	for path, name := range map[string]string{w.laptop: "laptop", w.phone: "phone"} {
		if err := client.SaveCredentials(path, pair(identity.Controller, name)); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(w.params, []byte(`{}`), 0600)
	key, err := transfer.LoadOrCreateKey(filepath.Join(dir, "host.transfer.key"))
	if err != nil {
		t.Fatal(err)
	}
	auth := &host.Authorizer{Store: host.Store{Dir: w.hostDir}, MachineID: hostCreds.DeviceID, HostKey: key.PublicKey().Bytes()}
	link, err := client.Dial(ctx, hostCreds)
	if err != nil {
		t.Fatal(err)
	}
	channel := &host.E2E{Key: key, Auth: auth}
	reg, err := agents.Open(agents.Options{StateDir: filepath.Join(dir, "agents"), ConfigDir: filepath.Join(dir, "config"), Machine: "mini",
		Env: os.Environ(), LoginShell: "/bin/sh", Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reg.Close)
	runner := &host.Runner{Service: &host.Service{Agents: reg, E2EKey: channel.PublicKey()}, Auth: auth, E2E: channel, Credentials: hostCreds, PollInterval: time.Hour}
	done := make(chan struct{})
	go func() { defer close(done); runner.Serve(ctx, link) }()
	t.Cleanup(func() { cancel(); link.Close(); <-done })
	creds, _ := client.LoadCredentials(w.laptop)
	c, err := client.NewController(ctx, creds)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if machines, err := c.Machines(ctx); err == nil && len(machines) == 1 && len(machines[0].Snapshot) > 0 {
			return w
		}
		if time.Now().After(deadline) {
			t.Fatal("host did not come online")
		}
	}
}

// withSigner runs f with commands signing by s (nil: no device keys).
func withSigner(s devicekey.Signer, f func()) {
	saved := deviceSigner
	deviceSigner = func() devicekey.Signer { return s }
	defer func() { deviceSigner = saved }()
	f()
}

func (w deviceWorld) key(t *testing.T, ctx context.Context, credentials string, route ...string) error {
	t.Helper()
	args := append([]string{"request", "--credentials", credentials, "--machine", w.machine, "--method", "agents.list", "--params", w.params}, route...)
	_, err := ctl(t, ctx, args...)
	return err
}

func isForbidden(err error) bool { return err != nil && strings.HasPrefix(err.Error(), "forbidden:") }

func (w deviceWorld) audit() string {
	data, _ := os.ReadFile(filepath.Join(w.hostDir, host.AuditFile))
	return string(data)
}

func pairHost(t *testing.T, ctx context.Context, credentials, machine string, extra ...string) (string, error) {
	t.Helper()
	out, err := ctl(t, ctx, append([]string{"pair-host", "--direct", "--json", "--credentials", credentials, "--machine", machine}, extra...)...)
	if err != nil {
		return "", err
	}
	var v struct{ Code, Status string }
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("pair-host printed %q", out)
	}
	return v.Code, nil
}

// The whole flow through a relay: before any approval the host accepts
// unsigned requests (and audits them); pair-host + approve turn enforcement
// on; unsigned and unknown devices are refused; a denied device cannot ask
// again; revocation ends access.
func TestDeviceApprovalEndToEnd(t *testing.T) {
	w := newDeviceWorld(t)
	ctx := context.Background()
	laptop := &countingSigner{Signer: &devicekey.Software{Dir: filepath.Join(w.dir, "laptop-keys")}}
	phone := &devicekey.Software{Dir: filepath.Join(w.dir, "phone-keys")}

	withSigner(nil, func() {
		if err := w.key(t, ctx, w.laptop, "--direct"); err != nil {
			t.Fatalf("enforcement off: %v", err)
		}
	})
	if !strings.Contains(w.audit(), "unauthenticated request (enforcement off") {
		t.Fatalf("unauthenticated request not audited:\n%s", w.audit())
	}

	var code string
	withSigner(laptop, func() {
		var err error
		if code, err = pairHost(t, ctx, w.laptop, "mini", "--name", "laptop", "--rights", "observe,type"); err != nil {
			t.Fatal(err)
		}
	})
	pending, _ := host.Store{Dir: w.hostDir}.PendingRequests(time.Now())
	if len(pending) != 1 || pending[0].Code != code || pending[0].Name != "laptop" {
		t.Fatalf("pending %+v, code %s", pending, code)
	}
	if trust, _ := loadTrust(trustPath()); trust.Hosts[w.machine].Key == "" {
		t.Fatal("host key not pinned")
	}
	// Waiting still allows unsigned requests: nothing is approved yet.
	withSigner(nil, func() {
		if err := w.key(t, ctx, w.phone, "--direct"); err != nil {
			t.Fatal(err)
		}
	})
	if out, err := ctl(t, ctx, "approve", "--state-dir", w.hostDir, strings.ToLower(code)); err != nil || !strings.Contains(out, `Approved "laptop" with rights observe, type`) {
		t.Fatalf("approve: %q %v", out, err)
	}

	withSigner(laptop, func() {
		if err := w.key(t, ctx, w.laptop, "--direct"); err != nil {
			t.Fatalf("signed request: %v", err)
		}
		if status, err := pairHost(t, ctx, w.laptop, "mini", "--name", "laptop", "--rights", "observe"); err != nil || status != code {
			t.Fatalf("approved device asking again: %q %v", status, err)
		}
	})
	if n, s := laptop.counts(); n == 0 || s != 0 {
		t.Fatalf("laptop signed %d normal, %d strong", n, s)
	}
	withSigner(nil, func() {
		if err := w.key(t, ctx, w.laptop, "--direct"); !isForbidden(err) {
			t.Fatalf("unsigned after approval: %v", err)
		}
	})
	withSigner(phone, func() {
		if err := w.key(t, ctx, w.phone, "--direct"); !isForbidden(err) {
			t.Fatalf("unknown device: %v", err)
		}
		if _, err := pairHost(t, ctx, w.phone, "mini", "--name", "phone"); err != nil {
			t.Fatal(err)
		}
	})
	if out, err := ctl(t, ctx, "approve", "--deny", "phone", "--state-dir", w.hostDir); err != nil || !strings.Contains(out, "Denied") {
		t.Fatalf("deny: %q %v", out, err)
	}
	withSigner(phone, func() {
		time.Sleep(10 * time.Millisecond)
		if _, err := pairHost(t, ctx, w.phone, "mini", "--name", "phone"); !isForbidden(err) {
			t.Fatalf("denied device asked again: %v", err)
		}
	})

	out, err := ctl(t, ctx, "devices-local", "--state-dir", w.hostDir, "--json")
	var listed struct {
		Controllers []host.Controller
		Enforcing   bool
	}
	if err != nil || json.Unmarshal([]byte(out), &listed) != nil || len(listed.Controllers) != 1 || !listed.Enforcing {
		t.Fatalf("devices-local: %q %v", out, err)
	}
	if _, err := ctl(t, ctx, "devices-local", "--state-dir", w.hostDir, "--revoke", "laptop"); err != nil {
		t.Fatal(err)
	}
	withSigner(laptop, func() {
		if err := w.key(t, ctx, w.laptop, "--direct"); !isForbidden(err) {
			t.Fatalf("revoked device: %v", err)
		}
	})
	// Revoking the last device does not switch enforcement off.
	withSigner(nil, func() {
		if err := w.key(t, ctx, w.phone, "--direct"); !isForbidden(err) {
			t.Fatalf("unsigned after the last revocation: %v", err)
		}
	})
	for _, event := range []string{"device.requested", "device.approved", "device.denied", "device.revoked", "request.refused"} {
		if !strings.Contains(w.audit(), `"event":"`+event+`"`) {
			t.Errorf("audit lacks %s", event)
		}
	}
}

// Through hesperd's control socket the command signs; hesperd forwards the
// signature and never signs for anyone. A strong signature (starting a
// shell) is made by the calling command (where Touch ID would prompt).
func TestControlSocketForwardsTheCallersSignature(t *testing.T) {
	w := newDeviceWorld(t)
	ctx := context.Background()
	laptop := &countingSigner{Signer: &devicekey.Software{Dir: filepath.Join(w.dir, "laptop-keys")}}
	withSigner(laptop, func() {
		code, err := pairHost(t, ctx, w.laptop, "mini", "--name", "laptop", "--rights", "observe,type,shell")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ctl(t, ctx, "approve", "--state-dir", w.hostDir, "--allow-software-shell", code); err != nil {
			t.Fatal(err)
		}
	})
	socket := startControl(t, w.laptop)
	forwarded := controlsock.Requests.Load()
	viaFleet := []string{"--socket", socket}

	withSigner(laptop, func() {
		if err := w.key(t, ctx, w.laptop, viaFleet...); err != nil {
			t.Fatalf("signed request through the fleet: %v", err)
		}
		// Starting a shell passes authorization with the caller's strong
		// signature; this host does not offer shells (no --allow-shell),
		// so the service refuses it.
		shell := filepath.Join(w.dir, "shell.json")
		os.WriteFile(shell, []byte(`{"kind":"shell","project":"/tmp"}`), 0600)
		_, err := ctl(t, ctx, "request", "--credentials", w.laptop, "--machine", w.machine, "--method", "agents.spawn", "--params", shell, "--socket", socket)
		if err == nil || !strings.Contains(err.Error(), "does not offer shells") {
			t.Fatalf("strong shell spawn through hesperd: %v", err)
		}
	})
	// The fleet holds the end-to-end channel: the command's signed request
	// traveled inside it (part N).
	if !strings.Contains(w.audit(), `"method":"agents.spawn","ok":true,"detail":"right shell, strong key","e2e":true`) {
		t.Fatalf("the shell spawn through hesperd was not end-to-end:\n%s", w.audit())
	}
	if controlsock.Requests.Load() != forwarded+2 {
		t.Fatalf("%d requests went through hesperd, want 2", controlsock.Requests.Load()-forwarded)
	}
	if n, s := laptop.counts(); n == 0 || s != 1 {
		t.Fatalf("the caller signed %d normal, %d strong", n, s)
	}

	// A caller that does not sign gets nothing from the fleet.
	creds, _ := client.LoadCredentials(w.laptop)
	raw := func(method string, auth *protocol.Auth) *protocol.Error {
		t.Helper()
		conn, err := net.Dial("unix", socket)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		params := json.RawMessage(`{}`)
		if method == "agents.spawn" {
			params = json.RawMessage(`{"kind":"shell","project":"/tmp"}`)
		}
		line, _ := json.Marshal(client.ControlRequest{ID: "1", Op: "request", MachineID: w.machine, Method: method, Params: params, Auth: auth})
		conn.Write(append(line, '\n'))
		reader := bufio.NewScanner(conn)
		reader.Buffer(make([]byte, 64*1024), client.MaxControlLine)
		if !reader.Scan() {
			t.Fatal("no answer")
		}
		var r client.ControlResponse
		json.Unmarshal(reader.Bytes(), &r)
		return r.Error
	}
	if e := raw("agents.list", nil); e == nil || e.Code != "forbidden" {
		t.Fatalf("unsigned through the fleet: %v", e)
	}
	// A device-key signature does not open a shell, also through the fleet.
	shellParams, _ := devicekey.NormalizeParams([]byte(`{"kind":"shell","project":"/tmp"}`))
	weak, _ := devicekey.SignRequest(laptop, creds.DeviceID, w.machine, "agents.spawn", shellParams, devicekey.Options{}, time.Now())
	if e := raw("agents.spawn", weak); e == nil || e.Code != "forbidden" {
		t.Fatalf("device-key shell spawn through hesperd: %v", e)
	}
	params, _ := devicekey.NormalizeParams([]byte(`{}`))
	signed, _ := devicekey.SignRequest(laptop, creds.DeviceID, w.machine, "agents.list", params, devicekey.Options{}, time.Now())
	if e := raw("agents.list", signed); e != nil {
		t.Fatalf("signed raw request: %v", e)
	}
	if e := raw("agents.list", signed); e == nil || e.Code != "forbidden" {
		t.Fatalf("replay through hesperd: %v", e)
	}
}

// startControl serves a control socket over a controller connection with
// the end-to-end channel, as hesperd's controller role does.
func startControl(t *testing.T, credentials string) string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	creds, err := client.LoadCredentials(credentials)
	if err != nil {
		t.Fatal(err)
	}
	c, err := client.NewController(ctx, creds)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	if err := enableE2E(c); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(shortDir(t), "controller.sock")
	l, err := controlsock.Listen(socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	s := controlsock.New(creds.DeviceID)
	s.Set(c)
	go s.Serve(ctx, l)
	return socket
}

// ctl runs hesperctl and returns its stdout.
func ctl(t *testing.T, ctx context.Context, args ...string) (string, error) {
	t.Helper()
	out, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stdout
	os.Stdout = out
	err = run(ctx, args)
	os.Stdout = saved
	out.Close()
	data, _ := os.ReadFile(out.Name())
	return string(data), err
}

func shortDir(t *testing.T) string {
	dir, err := os.MkdirTemp("/tmp", "gc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}
