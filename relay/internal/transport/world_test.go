package transport_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/internal/agents"
	"github.com/derzierau/hesper/relay/internal/gateway"
	"github.com/derzierau/hesper/relay/internal/host"
	"github.com/derzierau/hesper/relay/internal/identity"
	"github.com/derzierau/hesper/relay/internal/relay"
	"github.com/derzierau/hesper/relay/internal/sessions"
	"github.com/derzierau/hesper/relay/internal/storage"
	"github.com/derzierau/hesper/relay/internal/transport"
	"github.com/derzierau/hesper/relay/pkg/client"
	"github.com/derzierau/hesper/relay/pkg/devicekey"
	"github.com/derzierau/hesper/relay/pkg/protocol"
	"github.com/derzierau/hesper/relay/pkg/transfer"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

// The part R world: an in-process relay behind the tap (which records
// every frame, as the relay's operator could) and two hesperd instances,
// "L" (the laptop) and "M" (the mini), each with a host and a controller
// enrollment, software device keys, temp state and fake agent programs.

type node struct {
	short               string
	dir, home, project  string
	state, config, keys string
	logs, socket        string
	hostCreds, ctlCreds string
	hostID, ctlID       string
	cfg                 gateway.Config
	d                   *gateway.Daemon
}

type world struct {
	ctx  context.Context
	tap  *relayTap
	L, M *node
}

type worldOptions struct {
	delay    time.Duration // one network leg each way at the tap
	direct   bool          // M listens for the direct path (loopback)
	shell    bool          // M offers shells (--allow-shell)
	rights   []string      // what each machine's controller may do on the other
	stranger bool          // L is not approved on M (M enforces device keys)
}

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func newWorld(t *testing.T, opts worldOptions) *world {
	t.Helper()
	t.Setenv("HESPER_KEYS_SOFTWARE", "1")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	repo, err := storage.Open(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { repo.Close() })
	hub := relay.New(nil, 20*time.Second)
	t.Cleanup(hub.Close)
	app := transport.New(repo, hub)
	t.Cleanup(app.Terminals.Close)
	server := httptest.NewServer(app.PublicHandler())
	t.Cleanup(server.Close)
	w := &world{ctx: ctx, tap: newRelayTap(t, server.URL)}
	w.tap.delay.Store(int64(opts.delay))
	issue := func(role identity.Role, name string) protocol.Credentials {
		token, err := repo.Invite(ctx, identity.Invitation{Owner: "owner", Role: role, Expires: time.Now().Add(time.Minute)})
		if err != nil {
			t.Fatal(err)
		}
		c, err := client.Pair(ctx, server.URL, token, name)
		if err != nil {
			t.Fatal(err)
		}
		c.Relay = w.tap.server.URL
		return c
	}
	w.L, w.M = newNode(t, "L", issue), newNode(t, "M", issue)
	for _, n := range []*node{w.L, w.M} {
		names := map[string]any{w.L.hostID: map[string]string{"short": "L"}, w.M.hostID: map[string]string{"short": "M"}}
		writeJSON(t, filepath.Join(n.config, "machines.json"), map[string]any{"machines": names})
	}
	if opts.direct {
		w.M.cfg.Direct = "on"
		w.M.cfg.DirectAddrs = func() []netip.Addr { return []netip.Addr{netip.MustParseAddr("127.0.0.1")} }
	}
	w.M.cfg.AllowShell = opts.shell
	rights := opts.rights
	if rights == nil {
		rights = []string{"observe", "answer", "type", "transfer", "shell"}
	}
	if opts.stranger {
		// M approves only its own controller: L is unknown there.
		w.approve(t, w.M, w.M, rights...)
	} else {
		w.approve(t, w.M, w.L, rights...)
	}
	w.approve(t, w.L, w.M, rights...)
	w.L.start(t, ctx)
	w.M.start(t, ctx)
	return w
}

func newNode(t *testing.T, short string, issue func(identity.Role, string) protocol.Credentials) *node {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "gr"+short)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	dir, _ = filepath.EvalSymlinks(dir)
	n := &node{short: short, dir: dir, home: filepath.Join(dir, "home"), state: filepath.Join(dir, "s"), config: filepath.Join(dir, "c"),
		keys: filepath.Join(dir, "k"), logs: filepath.Join(dir, "logs")}
	n.project = filepath.Join(n.home, "projects", "app")
	for _, d := range []string{n.project, n.config, n.keys, n.logs} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	n.socket = filepath.Join(n.state, "hesperd.sock")
	exe, _ := os.Executable()
	writeJSON(t, filepath.Join(n.config, "profiles.json"), map[string]wire.Profile{
		"fake-claude": {Kind: wire.KindClaude, Argv: []string{exe, "claude", "--permission-mode", "auto", "--remote-control", "{name}"}},
		"fake-shell":  {Kind: wire.KindShell, Argv: []string{exe, "shell"}},
		// Never the real codex (the built-in profile): moved Codex sessions
		// resume with this.
		"fake-codex": {Kind: wire.KindCodex, Argv: []string{exe, "codex", "--dangerously-bypass-approvals-and-sandbox"}},
	})
	writeJSON(t, filepath.Join(n.config, "settings.json"), map[string]any{"defaults": map[string]any{"kind": "claude",
		"kinds": map[string]string{"claude": "fake-claude", "codex": "fake-codex", "shell": "fake-shell"}}})
	writeJSON(t, filepath.Join(dir, "claude.json"), map[string]any{"projects": map[string]any{n.home: map[string]any{"hasTrustDialogAccepted": true},
		n.project: map[string]any{"hasTrustDialogAccepted": true}}})
	host, ctl := issue(identity.Host, short+" host"), issue(identity.Controller, short+" controller")
	n.hostID, n.ctlID = host.DeviceID, ctl.DeviceID
	n.hostCreds, n.ctlCreds = filepath.Join(n.keys, "host.credentials.json"), filepath.Join(n.keys, "controller.credentials.json")
	if err := client.SaveCredentials(n.hostCreds, host); err != nil {
		t.Fatal(err)
	}
	if err := client.SaveCredentials(n.ctlCreds, ctl); err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(), "AGENTS_FAKE=1", "FAKE_LOG="+n.logs)
	n.cfg = gateway.Config{
		Registry: agents.Options{StateDir: n.state, ConfigDir: n.config, Socket: n.socket, Machine: short, Env: env, LoginShell: "/bin/sh",
			WorktreeRoot: filepath.Join(n.home, "worktrees"), ProjectsRoot: filepath.Join(n.home, "projects"), ClaudeConfig: filepath.Join(dir, "claude.json"),
			CodexHome: filepath.Join(n.home, ".codex"), Home: n.home, ClaudeHome: filepath.Join(n.home, ".claude"), StopGrace: 2 * time.Second,
			DeliverPoll: 100 * time.Millisecond, Logf: func(string, ...any) {}},
		HostCredentials: n.hostCreds, ControllerCredentials: n.ctlCreds, KeysDir: n.keys, Direct: "off",
		ControllerDirect: client.DirectConfig{AllowLoopback: true, Retry: 300 * time.Millisecond, MaxRetry: time.Second},
		Grace:            5 * time.Second, PollInterval: time.Hour, Logger: quiet,
		History: sessions.Options{ScanEvery: 100 * time.Millisecond, FullScanEvery: 300 * time.Millisecond, Foreground: true},
	}
	return n
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	data, _ := json.Marshal(v)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// approve: on host, someone approves from's controller with rights (part
// K's flow, done on the host's files before it starts).
func (w *world) approve(t *testing.T, on, from *node, rights ...string) {
	t.Helper()
	key, err := transfer.LoadOrCreateKey(filepath.Join(on.keys, "host.transfer.key"))
	if err != nil {
		t.Fatal(err)
	}
	auth := &host.Authorizer{Store: host.Store{Dir: on.keys}, MachineID: on.hostID, HostKey: key.PublicKey().Bytes()}
	signer := &devicekey.Software{Dir: from.keys}
	k, _ := signer.PublicKey(false)
	s, _ := signer.PublicKey(true)
	ks, _ := devicekey.EncodePublicKey(k)
	ss, _ := devicekey.EncodePublicKey(s)
	raw, err := auth.RequestApproval(w.ctx, protocol.Message{ControllerID: from.ctlID, Method: "devices.request",
		Params: protocol.JSON(host.ApprovalRequest{Name: from.short, Key: ks, StrongKey: ss, Rights: rights})})
	if err != nil {
		t.Fatal(err)
	}
	var status host.ApprovalStatus
	json.Unmarshal(raw, &status)
	if _, _, err := auth.Store.Approve(status.Code, host.ApproveOptions{AllowSoftwareShell: true}, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func (n *node) start(t *testing.T, ctx context.Context) {
	t.Helper()
	d, err := gateway.Start(ctx, n.cfg)
	if err != nil {
		t.Fatal(err)
	}
	n.d = d
	t.Cleanup(n.stop)
}

func (n *node) stop() {
	if n.d != nil {
		n.d.Close()
		n.d = nil
	}
}

func (n *node) client(t *testing.T) *wire.Client {
	t.Helper()
	c, err := wire.Dial(context.Background(), n.socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func (n *node) call(t *testing.T, method string, params, result any) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	return n.client(t).Call(ctx, method, params, result)
}

// waitLinked waits until n reaches the other machine's agents.
func (n *node) waitLinked(t *testing.T, other string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for n.d.Fleet.Route(other) == "" {
		if time.Now().After(deadline) {
			t.Fatalf("%s has no link to %s", n.short, other)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// waitAgent waits until n lists the agent id in a state that ok accepts.
func (n *node) waitAgent(t *testing.T, id string, ok func(wire.Agent) bool) wire.Agent {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	var last []wire.Agent
	for {
		last = nil
		if err := n.call(t, "agents.list", nil, &last); err != nil {
			t.Fatal(err)
		}
		for _, a := range last {
			if a.ID == id && ok(a) {
				return a
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: %s never got there; agents %+v", n.short, id, last)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func inState(state string) func(wire.Agent) bool {
	return func(a wire.Agent) bool { return a.State == state }
}

// argvs are the command lines the fake agent id was started with.
func (n *node) argvs(id string) string {
	data, _ := os.ReadFile(filepath.Join(n.logs, strings.ReplaceAll(id, "/", "_")+".argv"))
	return string(data)
}
